package run

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type LogsOptions struct {
	configFlags *genericclioptions.ConfigFlags
	name        string
	step        string
	follow      bool
	stored      bool

	k8sClient     client.Client
	adminEndpoint string
	httpClient    *http.Client
	clientset     *kubernetes.Clientset
	namespace     string

	genericclioptions.IOStreams
}

func NewLogsCmd(streams genericclioptions.IOStreams) *cobra.Command {
	o := &LogsOptions{
		configFlags: genericclioptions.NewConfigFlags(true),
		IOStreams:   streams,
	}

	cmd := &cobra.Command{
		Use:   "logs NAME",
		Short: "Stream sandbox pod logs for a run",
		Example: `  # Stream logs from the latest sandbox step
  oc agentic run logs fix-crash

  # Stream execution step logs
  oc agentic run logs fix-crash --step=Execution

  # Follow live pod logs
  oc agentic run logs fix-crash -f

  # Read logs persisted by the Collector after the sandbox pod is deleted
  oc agentic run logs fix-crash --step=Execution --stored

  # Override the default Kubernetes Service proxy with an external endpoint
  oc agentic run logs fix-crash --stored \\
    --admin-endpoint=https://collector.example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			return o.Run(cmd.Context())
		},
	}

	o.configFlags.AddFlags(cmd.Flags())
	cmd.Flags().StringVar(&o.step, "step", "", "Sandbox step: Analysis, Execution, or Verification")
	cmd.Flags().BoolVarP(&o.follow, "follow", "f", false, "Follow live pod log output")
	cmd.Flags().BoolVar(&o.stored, "stored", false, "Read logs persisted by the Collector")
	cmd.Flags().StringVar(&o.adminEndpoint, "admin-endpoint", "", "Collector admin API endpoint override for stored logs")

	return cmd
}

func (o *LogsOptions) Complete(cmd *cobra.Command, args []string) error {
	o.name = args[0]

	cfg, err := o.configFlags.ToRESTConfig()
	if err != nil {
		return fmt.Errorf("failed to get REST config: %w", err)
	}

	o.k8sClient, err = NewClientFromConfig(cfg)
	if err != nil {
		return err
	}

	o.clientset, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create clientset: %w", err)
	}

	o.namespace, err = ResolveNamespace(o.configFlags)
	return err
}

func (o *LogsOptions) Validate() error {
	if o.step != "" && !IsValidStep(o.step) {
		return fmt.Errorf("invalid step %q, must be one of: %s", o.step, strings.Join(validSandboxSteps, ", "))
	}
	if o.stored && o.follow {
		return fmt.Errorf("--follow cannot be used with --stored")
	}
	return nil
}

func (o *LogsOptions) Run(ctx context.Context) error {
	p := &agenticv1alpha1.AgenticRun{}
	if err := o.k8sClient.Get(ctx, types.NamespacedName{Name: o.name, Namespace: o.namespace}, p); err != nil {
		return fmt.Errorf("failed to get run %q: %w", o.name, err)
	}

	if o.stored {
		step, err := o.resolveStoredStep(p)
		if err != nil {
			return err
		}
		if o.adminEndpoint == "" {
			return o.fetchStoredLogsViaServiceProxy(ctx, p, step)
		}
		if o.configFlags != nil && o.configFlags.Insecure != nil && *o.configFlags.Insecure {
			o.httpClient = newAdminHTTPClient(true)
		}
		return o.fetchStoredLogs(ctx, string(p.UID), storedPhase(step))
	}

	sandbox := o.resolveSandbox(p)
	if sandbox == nil || sandbox.ClaimName == "" {
		return fmt.Errorf("no sandbox found for run %q (step: %s)", o.name, o.step)
	}

	podName := sandbox.ClaimName
	podNamespace := sandbox.Namespace
	if podNamespace == "" {
		podNamespace = o.namespace
	}

	req := o.clientset.CoreV1().Pods(podNamespace).GetLogs(podName, &corev1.PodLogOptions{
		Follow: o.follow,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("failed to stream logs from pod %s/%s: %w", podNamespace, podName, err)
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		fmt.Fprintln(o.Out, scanner.Text())
	}
	return scanner.Err()
}

const (
	collectorServiceName = "lightspeed-otel-collector"
	collectorServicePort = "8080"
)

func storedPhase(step agenticv1alpha1.SandboxStep) string {
	return strings.ToLower(string(step))
}

func serviceProxyPath(namespace, service, port string) string {
	return strings.Join([]string{
		"api", "v1", "namespaces", namespace, "services",
		"https:" + service + ":" + port, "proxy", "api", "v1", "logs",
	}, "/")
}

type storedLogRecord struct {
	ID        int64           `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Body      json.RawMessage `json:"body"`
}

type storedLogPage struct {
	AgenticRunID string            `json:"agentic_run_id"`
	Records      []storedLogRecord `json:"records"`
	HasMore      bool              `json:"has_more"`
}

func (o *LogsOptions) fetchStoredLogsViaServiceProxy(ctx context.Context, p *agenticv1alpha1.AgenticRun, step agenticv1alpha1.SandboxStep) error {
	namespace := o.namespace
	if sandbox := o.resolveSandboxForStep(p, step); sandbox != nil && sandbox.Namespace != "" {
		namespace = sandbox.Namespace
	}

	return o.fetchAllStoredLogs(ctx, string(p.UID), storedPhase(step), func(after int64) (storedLogPage, error) {
		result := o.clientset.CoreV1().RESTClient().Get().
			AbsPath(serviceProxyPath(namespace, collectorServiceName, collectorServicePort)).
			Param("agentic_run_id", string(p.UID)).
			Param("phase", storedPhase(step)).
			Param("limit", "1000").
			Param("format", "json")
		if after > 0 {
			result = result.Param("after", strconv.FormatInt(after, 10))
		}
		stream, err := result.Stream(ctx)
		if err != nil {
			return storedLogPage{}, fmt.Errorf("fetch stored logs through Collector service proxy: %w", err)
		}
		defer stream.Close()
		var page storedLogPage
		if err := json.NewDecoder(stream).Decode(&page); err != nil {
			return storedLogPage{}, fmt.Errorf("decode stored logs response: %w", err)
		}
		return page, nil
	})
}

func validateAdminEndpoint(rawEndpoint string) error {
	endpoint, err := url.Parse(rawEndpoint)
	if err != nil || endpoint.Host == "" {
		return fmt.Errorf("invalid Collector admin endpoint %q", rawEndpoint)
	}
	if endpoint.Scheme == "https" {
		return nil
	}
	if endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "::1") {
		return nil
	}
	return fmt.Errorf("Collector admin endpoint must use HTTPS (HTTP is allowed only for loopback endpoints): %q", rawEndpoint)
}

func (o *LogsOptions) fetchStoredLogs(ctx context.Context, runUID, phase string) error {
	if err := validateAdminEndpoint(o.adminEndpoint); err != nil {
		return err
	}
	return o.fetchAllStoredLogs(ctx, runUID, phase, func(after int64) (storedLogPage, error) {
		endpoint, err := url.Parse(strings.TrimRight(o.adminEndpoint, "/") + "/api/v1/logs")
		if err != nil {
			return storedLogPage{}, fmt.Errorf("invalid Collector admin endpoint: %w", err)
		}
		query := endpoint.Query()
		query.Set("agentic_run_id", runUID)
		query.Set("phase", phase)
		query.Set("limit", "1000")
		query.Set("format", "json")
		if after > 0 {
			query.Set("after", strconv.FormatInt(after, 10))
		}
		endpoint.RawQuery = query.Encode()

		httpClient := o.httpClient
		if httpClient == nil {
			httpClient = &http.Client{Timeout: 30 * time.Second}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return storedLogPage{}, fmt.Errorf("create stored log request: %w", err)
		}
		response, err := httpClient.Do(request)
		if err != nil {
			return storedLogPage{}, fmt.Errorf("fetch stored logs: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<10))
			if readErr != nil {
				return storedLogPage{}, fmt.Errorf("read Collector error response: %w", readErr)
			}
			return storedLogPage{}, fmt.Errorf("Collector returned %s: %s", response.Status, strings.TrimSpace(string(body)))
		}
		var page storedLogPage
		if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
			return storedLogPage{}, fmt.Errorf("decode stored logs response: %w", err)
		}
		return page, nil
	})
}

func (o *LogsOptions) fetchAllStoredLogs(ctx context.Context, runUID, phase string, fetchPage func(after int64) (storedLogPage, error)) error {
	var records []storedLogRecord
	after := int64(0)
	for {
		page, err := fetchPage(after)
		if err != nil {
			return err
		}
		records = append(records, page.Records...)
		if !page.HasMore {
			break
		}
		if len(page.Records) == 0 || page.Records[len(page.Records)-1].ID <= after {
			return fmt.Errorf("Collector returned has_more=true without advancing the log cursor")
		}
		after = page.Records[len(page.Records)-1].ID
	}

	if _, err := fmt.Fprintf(o.Out, "agentic_run_id: %s\nrecords: %d\nhas_more: false\n\n", runUID, len(records)); err != nil {
		return fmt.Errorf("write stored logs: %w", err)
	}
	for _, record := range records {
		var body string
		if err := json.Unmarshal(record.Body, &body); err != nil {
			body = string(record.Body)
		}
		if _, err := fmt.Fprintf(o.Out, "%s: %s\n", record.Timestamp.Format(time.RFC3339Nano), body); err != nil {
			return fmt.Errorf("write stored logs: %w", err)
		}
	}
	return nil
}

func (o *LogsOptions) resolveStoredStep(p *agenticv1alpha1.AgenticRun) (agenticv1alpha1.SandboxStep, error) {
	if o.step != "" {
		return NormalizeStep(o.step), nil
	}
	if p.Status.Steps.Verification.Sandbox.ClaimName != "" {
		return agenticv1alpha1.SandboxStepVerification, nil
	}
	if p.Status.Steps.Execution.Sandbox.ClaimName != "" {
		return agenticv1alpha1.SandboxStepExecution, nil
	}
	if p.Status.Steps.Analysis.Sandbox.ClaimName != "" {
		return agenticv1alpha1.SandboxStepAnalysis, nil
	}
	return "", fmt.Errorf("no sandbox step found for run %q; specify --step", o.name)
}

func newAdminHTTPClient(insecureSkipTLSVerify bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecureSkipTLSVerify {
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // explicitly requested by the user for a local port-forward
			MinVersion:         tls.VersionTLS12,
		}
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}

func (o *LogsOptions) resolveSandboxForStep(p *agenticv1alpha1.AgenticRun, step agenticv1alpha1.SandboxStep) *agenticv1alpha1.SandboxInfo {
	switch step {
	case agenticv1alpha1.SandboxStepAnalysis:
		return &p.Status.Steps.Analysis.Sandbox
	case agenticv1alpha1.SandboxStepExecution:
		return &p.Status.Steps.Execution.Sandbox
	case agenticv1alpha1.SandboxStepVerification:
		return &p.Status.Steps.Verification.Sandbox
	default:
		return nil
	}
}

func (o *LogsOptions) resolveSandbox(p *agenticv1alpha1.AgenticRun) *agenticv1alpha1.SandboxInfo {
	if o.step != "" {
		step := NormalizeStep(o.step)
		switch step {
		case agenticv1alpha1.SandboxStepAnalysis:
			return &p.Status.Steps.Analysis.Sandbox
		case agenticv1alpha1.SandboxStepExecution:
			return &p.Status.Steps.Execution.Sandbox
		case agenticv1alpha1.SandboxStepVerification:
			return &p.Status.Steps.Verification.Sandbox
		}
	}

	if p.Status.Steps.Verification.Sandbox.ClaimName != "" {
		return &p.Status.Steps.Verification.Sandbox
	}
	if p.Status.Steps.Execution.Sandbox.ClaimName != "" {
		return &p.Status.Steps.Execution.Sandbox
	}
	if p.Status.Steps.Analysis.Sandbox.ClaimName != "" {
		return &p.Status.Steps.Analysis.Sandbox
	}
	return nil
}
