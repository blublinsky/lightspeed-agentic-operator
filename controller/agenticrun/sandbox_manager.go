package agenticrun

import (
	"context"
	"encoding/json"
	"fmt"

	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
	"github.com/openshift/lightspeed-agentic-operator/pkg/configuration"
)

const (
	sandboxModeSandboxClaim = "sandbox-claim"

	errBuildPodSpec             = "build pod spec"
	errCreatePod                = "create pod for"
	errDeletePod                = "delete pod"
	errEnsureAgentTemplate      = "ensure agent template"
	errCreateSandboxClaim       = "failed to create SandboxClaim for"
	errDeleteSandboxClaim       = "failed to delete SandboxClaim"
	errCreateSandbox            = "create sandbox"
	errExtractSandboxName       = "extract sandbox name from claim"
	errExtractSandboxConditions = "extract conditions from sandbox"
	errExtractServiceFQDN       = "extract serviceFQDN from sandbox"

	sandboxDeletionTimeout = 2 * time.Minute
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=extensions.agents.x-k8s.io,resources=sandboxtemplates,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=extensions.agents.x-k8s.io,resources=sandboxclaims,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch

var (
	smClaimGVK = schema.GroupVersionKind{
		Group: "extensions.agents.x-k8s.io", Version: "v1alpha1", Kind: "SandboxClaim",
	}
	smSandboxGVK = schema.GroupVersionKind{
		Group: "agents.x-k8s.io", Version: "v1alpha1", Kind: "Sandbox",
	}
)

// SandboxManager manages sandbox lifecycle: create, wait-ready, release.
// Internally it decides between bare-pod and sandbox-claim mode based on
// the configuration cache, builds the PodSpec via PodSpecBuilder, and
// delegates to the appropriate creation path.
type SandboxManager struct {
	client  client.Client
	config  *configuration.Cache
	builder *PodSpecBuilder

	namespace       string
	deletionTimeout time.Duration
}

func NewSandboxManager(c client.Client, config *configuration.Cache, namespace string) *SandboxManager {
	return &SandboxManager{
		client:    c,
		config:    config,
		builder:   &PodSpecBuilder{},
		namespace: namespace,
	}
}

// Create builds a PodSpec from the cached base + agent configuration, then
// creates either a bare Pod or a SandboxClaim depending on the configured
// sandbox mode. Returns the resource name used for WaitReady/Release.
func (m *SandboxManager) Create(
	ctx context.Context,
	run *agenticv1alpha1.AgenticRun,
	step string,
	agent *agenticv1alpha1.Agent,
	llm *agenticv1alpha1.LLMProvider,
	tools *agenticv1alpha1.ToolsSpec,
	serviceAccount string,
	deadline time.Duration,
) (string, error) {
	cfg := m.config.Get()
	if cfg == nil {
		return "", fmt.Errorf("%s: configuration not available", errCreateSandbox)
	}

	podSpec, err := m.builder.Build(cfg.Sandbox.PodSpec, agent, llm, tools, &cfg.OTEL, step, string(run.UID), serviceAccount)
	if err != nil {
		return "", fmt.Errorf("%s: %w", errBuildPodSpec, err)
	}

	if deadline > 0 {
		secs := int64(deadline.Seconds())
		podSpec.ActiveDeadlineSeconds = &secs
	}

	name := truncateK8sName(fmt.Sprintf("ls-%s-%s", step, run.Name))
	if cfg.Sandbox.Mode == sandboxModeSandboxClaim {
		return m.createSandboxClaim(ctx, run, name, step, podSpec)
	}
	return m.createBarePod(ctx, run, name, step, podSpec)
}

func (m *SandboxManager) createBarePod(
	ctx context.Context,
	run *agenticv1alpha1.AgenticRun,
	podName string,
	step string,
	podSpec *corev1.PodSpec,
) (string, error) {
	log := logf.FromContext(ctx)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: m.namespace,
			Labels: map[string]string{
				LabelRun:  truncateK8sName(run.Name),
				LabelStep: step,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         agenticv1alpha1.GroupVersion.String(),
				Kind:               "AgenticRun",
				Name:               run.Name,
				UID:                run.UID,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: *podSpec,
	}

	if err := m.client.Create(ctx, pod); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("%s %s: %w", errCreatePod, step, err)
		}

		var existing corev1.Pod
		key := types.NamespacedName{Name: podName, Namespace: m.namespace}
		if getErr := m.client.Get(ctx, key, &existing); getErr != nil {
			return "", fmt.Errorf("get existing pod %q: %w", podName, getErr)
		}
		if existing.DeletionTimestamp.IsZero() {
			return podName, nil
		}

		log.Info("Waiting for terminating pod to disappear", LogKeyName, podName)
		if err := m.waitForPodDeletion(ctx, key); err != nil {
			return "", fmt.Errorf("wait for terminating pod %q: %w", podName, err)
		}
		if err := m.client.Create(ctx, pod); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return podName, nil
			}
			return "", fmt.Errorf("%s %s: %w", errCreatePod, step, err)
		}
	}

	log.Info("Created bare pod", LogKeyName, podName, LogKeyStep, step)
	return podName, nil
}

func (m *SandboxManager) createSandboxClaim(
	ctx context.Context,
	run *agenticv1alpha1.AgenticRun,
	name string,
	step string,
	podSpec *corev1.PodSpec,
) (string, error) {
	log := logf.FromContext(ctx)

	podSpecMap, err := podSpecToUnstructured(podSpec)
	if err != nil {
		return "", fmt.Errorf("convert PodSpec to unstructured: %w", err)
	}

	ownerRef := map[string]any{
		"apiVersion":         agenticv1alpha1.GroupVersion.String(),
		"kind":               "AgenticRun",
		"name":               run.Name,
		"uid":                string(run.UID),
		"controller":         true,
		"blockOwnerDeletion": true,
	}

	template := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "extensions.agents.x-k8s.io/v1alpha1",
			"kind":       "SandboxTemplate",
			"metadata": map[string]any{
				"name":      name,
				"namespace": m.namespace,
				"labels": map[string]any{
					LabelRun:  truncateK8sName(run.Name),
					LabelStep: step,
				},
				"ownerReferences": []any{ownerRef},
			},
			"spec": map[string]any{
				"podTemplate": map[string]any{
					"spec": podSpecMap,
				},
			},
		},
	}

	if err := m.client.Create(ctx, template); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("%s: %w", errEnsureAgentTemplate, err)
	}

	claim := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": smClaimGVK.Group + "/" + smClaimGVK.Version,
			"kind":       smClaimGVK.Kind,
			"metadata": map[string]any{
				"name":      name,
				"namespace": m.namespace,
				"labels": map[string]any{
					LabelRun:  truncateK8sName(run.Name),
					LabelStep: step,
				},
				"ownerReferences": []any{ownerRef},
			},
			"spec": map[string]any{
				"sandboxTemplateRef": map[string]any{
					"name": name,
				},
				"lifecycle": map[string]any{
					"shutdownPolicy": "Delete",
				},
			},
		},
	}

	if err := m.client.Create(ctx, claim); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return name, nil
		}
		return "", fmt.Errorf("%s %s: %w", errCreateSandboxClaim, step, err)
	}

	log.Info("Created SandboxClaim", LogKeyClaim, name, LogKeyStep, step)
	return name, nil
}

func podSpecToUnstructured(podSpec *corev1.PodSpec) (map[string]any, error) {
	raw, err := json.Marshal(podSpec)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// WaitReady polls until the sandbox is ready and returns its endpoint.
// For bare pods this is the PodIP; for sandbox claims it's the serviceFQDN.
func (m *SandboxManager) WaitReady(ctx context.Context, name string, timeout time.Duration) (string, error) {
	cfg := m.config.Get()
	if cfg != nil && cfg.Sandbox.Mode == sandboxModeSandboxClaim {
		return m.waitSandboxClaimReady(ctx, name, timeout)
	}
	return m.waitPodReady(ctx, name, timeout)
}

func (m *SandboxManager) waitPodReady(ctx context.Context, podName string, timeout time.Duration) (string, error) {
	log := logf.FromContext(ctx)

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	key := types.NamespacedName{Name: podName, Namespace: m.namespace}

	check := func() (string, bool, error) {
		var pod corev1.Pod
		if err := m.client.Get(ctx, key, &pod); err != nil {
			if apierrors.IsNotFound(err) {
				return "", false, fmt.Errorf("pod %q was deleted while waiting for readiness", podName)
			}
			log.V(1).Info("Waiting for pod", LogKeyName, podName, "error", err)
			return "", false, nil
		}
		if !pod.DeletionTimestamp.IsZero() {
			return "", false, fmt.Errorf("pod %q is terminating, will not become ready", podName)
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue && pod.Status.PodIP != "" {
				log.Info("Pod ready", LogKeyName, podName, "podIP", pod.Status.PodIP)
				return pod.Status.PodIP, true, nil
			}
		}
		return "", false, nil
	}

	if ip, ready, err := check(); err != nil || ready {
		return ip, err
	}

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return "", fmt.Errorf("timeout waiting for pod %q after %s", podName, timeout)
			}
			if ip, ready, err := check(); err != nil || ready {
				return ip, err
			}
		}
	}
}

func (m *SandboxManager) waitSandboxClaimReady(ctx context.Context, claimName string, timeout time.Duration) (string, error) {
	log := logf.FromContext(ctx)

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	claim := &unstructured.Unstructured{}
	sandbox := &unstructured.Unstructured{}
	claimKey := types.NamespacedName{Name: claimName, Namespace: m.namespace}

	check := func() (string, bool, error) {
		claim.SetGroupVersionKind(smClaimGVK)
		if err := m.client.Get(ctx, claimKey, claim); err != nil {
			log.V(1).Info("Waiting for SandboxClaim", LogKeyClaim, claimName)
			return "", false, nil
		}

		sandboxName, found, nestedErr := unstructured.NestedString(claim.Object, "status", "sandbox", "name")
		if nestedErr != nil {
			return "", false, fmt.Errorf("%s %q: %w", errExtractSandboxName, claimName, nestedErr)
		}
		if !found || sandboxName == "" {
			return "", false, nil
		}

		sandbox.SetGroupVersionKind(smSandboxGVK)
		if err := m.client.Get(ctx, types.NamespacedName{
			Name: sandboxName, Namespace: m.namespace,
		}, sandbox); err != nil {
			log.V(1).Info("Waiting for Sandbox", LogKeyName, sandboxName, "error", err)
			return "", false, nil
		}

		conditions, found, nestedErr := unstructured.NestedSlice(sandbox.Object, "status", "conditions")
		if nestedErr != nil {
			return "", false, fmt.Errorf("%s %q: %w", errExtractSandboxConditions, sandboxName, nestedErr)
		}
		if !found {
			return "", false, nil
		}

		for _, c := range conditions {
			cond, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cond["type"] == "Ready" && cond["status"] == string(metav1.ConditionTrue) {
				fqdn, fqdnFound, fqdnErr := unstructured.NestedString(sandbox.Object, "status", "serviceFQDN")
				if fqdnErr != nil {
					return "", false, fmt.Errorf("%s %q: %w", errExtractServiceFQDN, sandboxName, fqdnErr)
				}
				if !fqdnFound || fqdn == "" {
					return "", false, nil
				}
				log.Info("Sandbox ready", LogKeyName, sandboxName, "fqdn", fqdn)
				return fqdn, true, nil
			}
		}
		return "", false, nil
	}

	if fqdn, ready, err := check(); err != nil || ready {
		return fqdn, err
	}

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return "", fmt.Errorf("timeout waiting for sandbox %q after %s", claimName, timeout)
			}
			if fqdn, ready, err := check(); err != nil || ready {
				return fqdn, err
			}
		}
	}
}

// Release deletes the sandbox resource (Pod or SandboxClaim).
// Idempotent: returns nil if already gone.
func (m *SandboxManager) Release(ctx context.Context, name string) error {
	cfg := m.config.Get()
	if cfg != nil && cfg.Sandbox.Mode == sandboxModeSandboxClaim {
		return m.releaseSandboxClaim(ctx, name)
	}
	return m.releaseBarePod(ctx, name)
}

func (m *SandboxManager) releaseBarePod(ctx context.Context, podName string) error {
	log := logf.FromContext(ctx)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: m.namespace,
		},
	}

	if err := m.client.Delete(ctx, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("%s %q: %w", errDeletePod, podName, err)
	}

	log.Info("Released bare pod", LogKeyName, podName)
	return nil
}

func (m *SandboxManager) releaseSandboxClaim(ctx context.Context, claimName string) error {
	log := logf.FromContext(ctx)

	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(smClaimGVK)
	claim.SetName(claimName)
	claim.SetNamespace(m.namespace)

	if err := m.client.Delete(ctx, claim); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("%s %q: %w", errDeleteSandboxClaim, claimName, err)
	}

	tmpl := &unstructured.Unstructured{}
	tmpl.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "extensions.agents.x-k8s.io", Version: "v1alpha1", Kind: "SandboxTemplate",
	})
	tmpl.SetName(claimName)
	tmpl.SetNamespace(m.namespace)

	if err := m.client.Delete(ctx, tmpl); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete SandboxTemplate %q: %w", claimName, err)
	}

	log.Info("Released SandboxClaim and SandboxTemplate", LogKeyClaim, claimName)
	return nil
}

func (m *SandboxManager) waitForPodDeletion(ctx context.Context, key types.NamespacedName) error {
	timeout := m.deletionTimeout
	if timeout == 0 {
		timeout = sandboxDeletionTimeout
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		var pod corev1.Pod
		err := m.client.Get(ctx, key, &pod)
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return fmt.Errorf("get pod %q: %w", key.Name, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for pod %q to be deleted after %s", key.Name, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
