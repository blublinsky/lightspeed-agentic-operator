//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

const pollInterval = 2 * time.Second

var pollTimeout = func() time.Duration {
	if v := os.Getenv("E2E_POLL_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return 10 * time.Minute
}()

var testNS = func() string {
	if ns := os.Getenv("TEST_NAMESPACE"); ns != "" {
		return ns
	}
	return "openshift-lightspeed"
}()

// --- Client ---

func buildClient() (client.Client, error) {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = home + "/.kube/config"
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig: %w", err)
	}

	s := scheme.Scheme
	utilruntime.Must(agenticv1alpha1.AddToScheme(s))
	utilruntime.Must(admissionregistrationv1.AddToScheme(s))

	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}
	return c, nil
}

func newClient(t *testing.T) client.Client {
	t.Helper()
	return suiteClient
}

// --- Pointer helpers ---

func ptrBool(v bool) *bool    { return &v }
func ptrInt32(v int32) *int32 { return &v }

// --- Cleanup ---

// cleanupTimeout is how long cleanup waits for operator finalizers before
// force-stripping. Long enough for normal finalizer processing, short enough
// to not stall the suite if a finalizer is genuinely stuck.
const cleanupTimeout = 2 * time.Minute

// cleanup deletes objects, letting the operator's finalizers run naturally.
// It polls until the object is fully gone (using cleanupTimeout), so the
// next test never races against a lingering finalizer. Only force-strips
// finalizers as a last resort when the operator appears stuck.
func cleanup(t *testing.T, c client.Client, objs ...client.Object) {
	t.Helper()
	ctx := context.Background()

	for _, obj := range objs {
		kind := obj.GetObjectKind().GroupVersionKind().Kind
		if kind == "" {
			kind = fmt.Sprintf("%T", obj)
		}
		name := obj.GetName()
		key := types.NamespacedName{Name: name, Namespace: obj.GetNamespace()}

		if err := c.Get(ctx, key, obj); err != nil {
			t.Logf("cleanup: %s/%s not found (already clean)", kind, name)
			continue
		}

		_ = c.Delete(ctx, obj)

		err := wait.PollUntilContextTimeout(ctx, 1*time.Second, cleanupTimeout, true, func(ctx context.Context) (bool, error) {
			if err := c.Get(ctx, key, obj); err != nil {
				return true, nil
			}
			if obj.GetDeletionTimestamp() != nil && len(obj.GetFinalizers()) > 0 {
				t.Logf("cleanup: %s/%s waiting for finalizer %v", kind, name, obj.GetFinalizers())
			}
			return false, nil
		})
		if err == nil {
			t.Logf("cleanup: %s/%s deleted", kind, name)
			continue
		}

		if err := c.Get(ctx, key, obj); err != nil {
			t.Logf("cleanup: %s/%s deleted", kind, name)
			continue
		}
		if len(obj.GetFinalizers()) > 0 {
			t.Logf("cleanup: %s/%s force-stripping finalizers %v (operator did not process within %s)", kind, name, obj.GetFinalizers(), cleanupTimeout)
			obj.SetFinalizers(nil)
			_ = c.Update(ctx, obj)
		}
	}
}

// --- AgenticRun builder ---

// createAgenticRun creates a AgenticRun, cleans up leftovers from previous
// runs, and registers cleanup. Returns the created AgenticRun.
func createAgenticRun(t *testing.T, c client.Client, name string) *agenticv1alpha1.AgenticRun {
	t.Helper()
	return createAgenticRunWithRequest(t, c, name, "Pod crash-looping in staging namespace")
}

// createAgenticRunWithRequest is like createAgenticRun but allows a custom
// request string. Embed mock failure keywords (MOCK_CRASH, MOCK_TIMEOUT, etc.)
// in the request to trigger failure modes.
func createAgenticRunWithRequest(t *testing.T, c client.Client, name, request string) *agenticv1alpha1.AgenticRun {
	t.Helper()
	ctx := context.Background()

	prop := &agenticv1alpha1.AgenticRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: agenticv1alpha1.AgenticRunSpec{
			Request:          request,
			TargetNamespaces: []string{"staging"},
			Tools:            agenticv1alpha1.ToolsSpec{Skills: []agenticv1alpha1.SkillsSource{{Image: "quay.io/openshift-lightspeed/ols-qe:lightspeed-mock-agent", Paths: []string{"/skills"}}}},
			Analysis:         agenticv1alpha1.AgenticRunStep{Agent: "e2e-agent"},
			Execution:        agenticv1alpha1.AgenticRunStep{Agent: "e2e-agent"},
			Verification:     agenticv1alpha1.AgenticRunStep{Agent: "e2e-agent"},
		},
	}

	cleanup(t, c, &agenticv1alpha1.AgenticRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS}})
	cleanup(t, c, &agenticv1alpha1.AgenticRunApproval{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS}})

	if err := c.Create(ctx, prop); err != nil {
		t.Fatalf("create AgenticRun: %v", err)
	}
	t.Cleanup(func() { cleanup(t, c, prop) })

	return prop
}

// terminalPhases lists phases that will never transition further.
var terminalPhases = map[agenticv1alpha1.AgenticRunPhase]bool{
	agenticv1alpha1.AgenticRunPhaseCompleted:        true,
	agenticv1alpha1.AgenticRunPhaseFailed:           true,
	agenticv1alpha1.AgenticRunPhaseDenied:           true,
	agenticv1alpha1.AgenticRunPhaseEscalated:        true,
	agenticv1alpha1.AgenticRunPhaseEmergencyStopped: true,
	agenticv1alpha1.AgenticRunPhaseNoActionRequired: true,
}

// waitForPhase polls until the AgenticRun reaches the target phase. It fails
// immediately if the run reaches a different terminal phase, since terminal
// phases never transition further.
func waitForPhase(t *testing.T, c client.Client, name string, target agenticv1alpha1.AgenticRunPhase) agenticv1alpha1.AgenticRun {
	t.Helper()
	return waitForPhaseWithTimeout(t, c, name, target, pollTimeout)
}

func waitForPhaseWithTimeout(t *testing.T, c client.Client, name string, target agenticv1alpha1.AgenticRunPhase, timeout time.Duration) agenticv1alpha1.AgenticRun {
	t.Helper()
	ctx := context.Background()
	var updated agenticv1alpha1.AgenticRun

	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: testNS}, &updated); err != nil {
			return false, nil
		}
		phase := agenticv1alpha1.DerivePhase(updated.Status.Conditions)
		t.Logf("polling %s: phase=%s conditions=%d", name, phase, len(updated.Status.Conditions))
		if phase == target {
			return true, nil
		}
		if terminalPhases[phase] {
			return false, fmt.Errorf("reached terminal phase %s while waiting for %s", phase, target)
		}
		return false, nil
	})
	if err != nil {
		phase := agenticv1alpha1.DerivePhase(updated.Status.Conditions)
		t.Fatalf("waiting for phase %s failed: %v; current=%s conditions=%v", target, err, phase, updated.Status.Conditions)
	}
	return updated
}

// waitForDeletion polls until the AgenticRun is gone (finalizer completed).
func waitForDeletion(t *testing.T, c client.Client, name string) {
	t.Helper()
	ctx := context.Background()

	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		var gone agenticv1alpha1.AgenticRun
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: testNS}, &gone); err != nil {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for AgenticRun %s deletion (finalizer may be stuck)", name)
	}
}

// denyStage patches the AgenticRunApproval to deny the given stage.
func denyStage(t *testing.T, c client.Client, name string, stageType agenticv1alpha1.ApprovalStageType) {
	t.Helper()
	ctx := context.Background()

	var approval agenticv1alpha1.AgenticRunApproval
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: testNS}, &approval); err != nil {
		t.Fatalf("get AgenticRunApproval for denial: %v", err)
	}

	base := approval.DeepCopy()
	found := false
	for i, s := range approval.Spec.Stages {
		if s.Type == stageType {
			approval.Spec.Stages[i].Decision = agenticv1alpha1.ApprovalDecisionDenied
			found = true
			break
		}
	}
	if !found {
		stage := agenticv1alpha1.ApprovalStage{
			Type:     stageType,
			Decision: agenticv1alpha1.ApprovalDecisionDenied,
		}
		switch stageType {
		case agenticv1alpha1.ApprovalStageAnalysis:
			stage.Analysis = &agenticv1alpha1.AnalysisApproval{Agent: "e2e-agent"}
		case agenticv1alpha1.ApprovalStageExecution:
			stage.Execution = &agenticv1alpha1.ExecutionApproval{Agent: "e2e-agent"}
		case agenticv1alpha1.ApprovalStageVerification:
			stage.Verification = &agenticv1alpha1.VerificationApproval{Agent: "e2e-agent"}
		case agenticv1alpha1.ApprovalStageEscalation:
			stage.Escalation = &agenticv1alpha1.EscalationApproval{Agent: "e2e-agent"}
		}
		approval.Spec.Stages = append(approval.Spec.Stages, stage)
	}
	if err := c.Patch(ctx, &approval, client.MergeFrom(base)); err != nil {
		t.Fatalf("deny stage %s: %v", stageType, err)
	}
	t.Logf("denied stage %s", stageType)
}

// approveExecution patches the AgenticRunApproval to approve execution with the given option index.
func approveExecution(t *testing.T, c client.Client, name string, optionIdx int32) {
	t.Helper()
	ctx := context.Background()

	var approval agenticv1alpha1.AgenticRunApproval
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: testNS}, &approval); err != nil {
		t.Fatalf("get AgenticRunApproval for execution approval: %v", err)
	}

	base := approval.DeepCopy()
	found := false
	for i, s := range approval.Spec.Stages {
		if s.Type == agenticv1alpha1.ApprovalStageExecution {
			approval.Spec.Stages[i].Execution = &agenticv1alpha1.ExecutionApproval{
				Agent:  "e2e-agent",
				Option: ptrInt32(optionIdx),
			}
			found = true
			break
		}
	}
	if !found {
		approval.Spec.Stages = append(approval.Spec.Stages, agenticv1alpha1.ApprovalStage{
			Type:      agenticv1alpha1.ApprovalStageExecution,
			Execution: &agenticv1alpha1.ExecutionApproval{Agent: "e2e-agent", Option: ptrInt32(optionIdx)},
		})
	}
	if err := c.Patch(ctx, &approval, client.MergeFrom(base)); err != nil {
		t.Fatalf("approve execution: %v", err)
	}
	t.Logf("approved execution with option %d", optionIdx)
}

// approveVerification patches the AgenticRunApproval to approve verification.
func approveVerification(t *testing.T, c client.Client, name string) {
	t.Helper()
	ctx := context.Background()

	var approval agenticv1alpha1.AgenticRunApproval
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: testNS}, &approval); err != nil {
		t.Fatalf("get AgenticRunApproval for verification approval: %v", err)
	}

	base := approval.DeepCopy()
	found := false
	for i, s := range approval.Spec.Stages {
		if s.Type == agenticv1alpha1.ApprovalStageVerification {
			approval.Spec.Stages[i].Verification = &agenticv1alpha1.VerificationApproval{Agent: "e2e-agent"}
			found = true
			break
		}
	}
	if !found {
		approval.Spec.Stages = append(approval.Spec.Stages, agenticv1alpha1.ApprovalStage{
			Type:         agenticv1alpha1.ApprovalStageVerification,
			Verification: &agenticv1alpha1.VerificationApproval{Agent: "e2e-agent"},
		})
	}
	if err := c.Patch(ctx, &approval, client.MergeFrom(base)); err != nil {
		t.Fatalf("approve verification: %v", err)
	}
	t.Logf("approved verification")
}

// --- OTEL trace verification ---

const otelCollectorLabelSelector = "app=lightspeed-otel-collector"

// collectorLogs returns the OTEL collector pod logs, or empty string
// (with t.Skip) if the collector is not deployed.
func collectorLogs(t *testing.T) string {
	t.Helper()

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = home + "/.kube/config"
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("build kubeconfig for pod logs: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("create kubernetes clientset: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pods, err := clientset.CoreV1().Pods(testNS).List(ctx, metav1.ListOptions{
		LabelSelector: otelCollectorLabelSelector,
	})
	if err != nil {
		t.Fatalf("list OTEL collector pods: %v", err)
	}
	var podName string
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil {
			podName = p.Name
			break
		}
	}
	if podName == "" {
		t.Skip("OTEL collector not deployed, skipping trace assertion")
		return ""
	}
	req := clientset.CoreV1().Pods(testNS).GetLogs(podName, &corev1.PodLogOptions{})
	stream, err := req.Stream(ctx)
	if err != nil {
		t.Fatalf("stream collector logs: %v", err)
	}
	defer stream.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(stream); err != nil {
		t.Fatalf("read collector logs: %v", err)
	}
	return buf.String()
}

// assertTracesExported verifies that the OTEL collector received spans
// for the given AgenticRun (matched by UID) and that each of the
// expected span names appears in a span block that also carries this
// run's UID attribute. Retries for up to 30 seconds to allow the batch
// span processor to flush recently completed spans.
func assertTracesExported(t *testing.T, run *agenticv1alpha1.AgenticRun, expectedSpans []string) {
	t.Helper()

	uid := string(run.UID)
	if uid == "" {
		t.Fatal("AgenticRun has no UID, cannot verify traces")
	}

	uidNeedle := fmt.Sprintf("agenticrun.uid: Str(%s)", uid)
	var missing []string

	err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 30*time.Second, true, func(_ context.Context) (bool, error) {
		logs := collectorLogs(t)
		if !strings.Contains(logs, uidNeedle) {
			return false, nil
		}

		blocks := strings.Split(logs, "Span #")
		missing = nil
		for _, span := range expectedSpans {
			nameNeedle := fmt.Sprintf("Name           : %s", span)
			found := false
			for _, block := range blocks {
				if strings.Contains(block, uidNeedle) && strings.Contains(block, nameNeedle) {
					found = true
					break
				}
			}
			if !found {
				missing = append(missing, span)
			}
		}
		return len(missing) == 0, nil
	})

	if err != nil {
		if !strings.Contains(collectorLogs(t), uidNeedle) {
			t.Errorf("OTEL collector logs do not contain any spans for run UID %s", uid)
			return
		}
		for _, span := range missing {
			t.Errorf("OTEL collector logs missing span %q for run %s (UID %s)", span, run.Name, uid)
		}
	}

	for _, span := range expectedSpans {
		found := true
		for _, m := range missing {
			if m == span {
				found = false
				break
			}
		}
		if found {
			t.Logf("Verified: span %q present for UID %s", span, uid)
		}
	}
}
