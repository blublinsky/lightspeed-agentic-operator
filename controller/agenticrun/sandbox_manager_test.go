package agenticrun

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
	"github.com/openshift/lightspeed-agentic-operator/pkg/configuration"
)

func testCache(t *testing.T, mode string) *configuration.Cache {
	t.Helper()
	return testCacheWithOTEL(t, mode, "", "", "")
}

func testCacheWithOTEL(t *testing.T, mode, otelEndpoint, otelAdmin, otelCA string) *configuration.Cache {
	t.Helper()
	c := &configuration.Cache{}
	data := map[string]string{
		configuration.KeySandboxMode:    mode,
		configuration.KeySandboxPodSpec: `{"containers":[{"name":"agent","image":"registry.example.com/agent:latest","ports":[{"containerPort":8080}]}]}`,
	}
	if otelEndpoint != "" {
		data[configuration.KeyOtelCollectorEndpoint] = otelEndpoint
	}
	if otelAdmin != "" {
		data[configuration.KeyOtelAdminEndpoint] = otelAdmin
	}
	if otelCA != "" {
		data[configuration.KeyOtelCASecret] = otelCA
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configuration.ConfigMapName},
		Data:       data,
	}
	if err := c.OnConfigMapChange(context.Background(), cm); err != nil {
		t.Fatalf("testCacheWithOTEL: OnConfigMapChange failed: %v", err)
	}
	return c
}

func testSMRun() *agenticv1alpha1.AgenticRun {
	return &agenticv1alpha1.AgenticRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run",
			Namespace: "test-ns",
			UID:       types.UID("abc-123"),
		},
		Spec: agenticv1alpha1.AgenticRunSpec{
			Request: "fix it",
		},
	}
}

func testSMAgent() *agenticv1alpha1.Agent {
	return &agenticv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: agenticv1alpha1.AgentSpec{
			LLMProvider: agenticv1alpha1.LLMProviderReference{Name: "smart"},
			Model:       "test-model",
		},
	}
}

func testLLMForManager() *agenticv1alpha1.LLMProvider {
	return &agenticv1alpha1.LLMProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "smart"},
		Spec: agenticv1alpha1.LLMProviderSpec{
			Type: agenticv1alpha1.LLMProviderOpenAI,
			OpenAI: agenticv1alpha1.OpenAIConfig{
				CredentialsSecret: agenticv1alpha1.SecretReference{Name: "llm-creds"},
			},
		},
	}
}

func testReaderCRB() *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: defaultReaderClusterRoleBinding},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-reader"},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      defaultSandboxSA,
			Namespace: "test-ns",
		}},
	}
}

func newTestSandboxManager(fc client.Client, cache *configuration.Cache) *SandboxManager {
	resetReaderBindings()
	return &SandboxManager{
		client:          fc,
		config:          cache,
		builder:         &PodSpecBuilder{},
		namespace:       "test-ns",
		deletionTimeout: 1 * time.Second,
	}
}

// --- Create tests ---

func TestCreate_BarePod(t *testing.T) {
	cache := testCache(t, "bare-pod")
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	name, err := mgr.Create(context.Background(), testSMRun(), "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if name == "" {
		t.Fatal("expected non-empty name")
	}
	if name[0:3] != "ls-" {
		t.Fatalf("bare-pod name should start with 'ls-', got %q", name)
	}

	var pod corev1.Pod
	if err := fc.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "test-ns"}, &pod); err != nil {
		t.Fatalf("pod not found: %v", err)
	}
	if len(pod.OwnerReferences) == 0 {
		t.Fatal("expected OwnerReferences on pod")
	}
	if pod.OwnerReferences[0].Name != "test-run" {
		t.Fatalf("expected owner name 'test-run', got %q", pod.OwnerReferences[0].Name)
	}
	if pod.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("expected ActiveDeadlineSeconds to be set on pod")
	}
	if *pod.Spec.ActiveDeadlineSeconds != int64(15*time.Minute/time.Second) {
		t.Fatalf("expected ActiveDeadlineSeconds=%d, got %d", int64(15*time.Minute/time.Second), *pod.Spec.ActiveDeadlineSeconds)
	}

	var cm corev1.ConfigMap
	run := testSMRun()
	if err := fc.Get(context.Background(), types.NamespacedName{Name: inputConfigMapName("analysis", string(run.UID)), Namespace: "test-ns"}, &cm); err != nil {
		t.Fatalf("input ConfigMap not found: %v", err)
	}
	if !strings.Contains(cm.Data[inputConfigMapKeyQuery], "fix it") {
		t.Errorf("ConfigMap query should contain request text, got %q", cm.Data[inputConfigMapKeyQuery])
	}
	if len(cm.OwnerReferences) == 0 {
		t.Fatal("expected OwnerReferences on input ConfigMap")
	}
	if cm.OwnerReferences[0].Kind != "Pod" || cm.OwnerReferences[0].Name != name {
		t.Fatalf("expected ConfigMap owned by Pod %q, got %s/%s", name, cm.OwnerReferences[0].Kind, cm.OwnerReferences[0].Name)
	}
	foundMount := false
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.Name == inputConfigMapVolumeName && m.MountPath == inputConfigMapMountPath {
			foundMount = true
			break
		}
	}
	if !foundMount {
		t.Error("pod missing /input ConfigMap mount")
	}
}

func TestCreate_SandboxClaim(t *testing.T) {
	cache := testCache(t, "sandbox-claim")
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	name, err := mgr.Create(context.Background(), testSMRun(), "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if name[0:3] != "ls-" {
		t.Fatalf("sandbox-claim name should start with 'ls-', got %q", name)
	}

	tmpl := &unstructured.Unstructured{}
	tmpl.SetGroupVersionKind(smClaimGVK)
	// Actually check the template was created
	stmpl := &unstructured.Unstructured{}
	stmpl.SetGroupVersionKind(smClaimGVK)
	stmpl.SetGroupVersionKind(smClaimGVK)
	if err := fc.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "test-ns"}, stmpl); err != nil {
		t.Fatalf("SandboxClaim not found: %v", err)
	}

	ownerRefs, found, _ := unstructured.NestedSlice(stmpl.Object, "metadata", "ownerReferences")
	if !found || len(ownerRefs) == 0 {
		t.Fatal("expected ownerReferences on SandboxClaim")
	}
}

func TestCreate_ConfigNotAvailable(t *testing.T) {
	cache := &configuration.Cache{} // empty — no ConfigMap loaded
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	_, err := mgr.Create(context.Background(), testSMRun(), "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err == nil {
		t.Fatal("expected error when config is not available")
	}
}

func TestCreate_Idempotent_BarePod(t *testing.T) {
	cache := testCache(t, "bare-pod")
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	name1, err := mgr.Create(context.Background(), testSMRun(), "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("first Create failed: %v", err)
	}
	name2, err := mgr.Create(context.Background(), testSMRun(), "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("second Create failed: %v", err)
	}
	if name1 != name2 {
		t.Fatalf("expected same name on idempotent create, got %q and %q", name1, name2)
	}
}

func TestCreate_Idempotent_SandboxClaim(t *testing.T) {
	cache := testCache(t, "sandbox-claim")
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	name1, err := mgr.Create(context.Background(), testSMRun(), "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("first Create failed: %v", err)
	}
	name2, err := mgr.Create(context.Background(), testSMRun(), "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("second Create failed: %v", err)
	}
	if name1 != name2 {
		t.Fatalf("expected same name on idempotent create, got %q and %q", name1, name2)
	}
}

func TestCreate_OTELEnvVars(t *testing.T) {
	cache := testCacheWithOTEL(t, "bare-pod", "dns:///otel-collector.ns.svc:4317", "https://otel-collector.ns.svc:8080", "otel-ca-secret")
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	run := testSMRun()
	name, err := mgr.Create(context.Background(), run, "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var pod corev1.Pod
	if err := fc.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "test-ns"}, &pod); err != nil {
		t.Fatalf("pod not found: %v", err)
	}

	envMap := map[string]string{}
	for _, e := range pod.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}

	if v := envMap["OTEL_EXPORTER_OTLP_ENDPOINT"]; v != "dns:///otel-collector.ns.svc:4317" {
		t.Fatalf("expected OTEL endpoint, got %q", v)
	}
	if v := envMap["LIGHTSPEED_AGENTICRUN_UID"]; v != string(run.UID) {
		t.Fatalf("expected run UID %q, got %q", run.UID, v)
	}
	if v := envMap["LIGHTSPEED_AGENTICRUN_STEP"]; v != "analysis" {
		t.Fatalf("expected run step %q, got %q", "analysis", v)
	}
	hasVolume := false
	for _, v := range pod.Spec.Volumes {
		if v.Name == otelCAVolumeName {
			hasVolume = true
			if v.Secret.SecretName != "otel-ca-secret" {
				t.Fatalf("expected otel-ca-secret, got %q", v.Secret.SecretName)
			}
		}
	}
	if !hasVolume {
		t.Fatal("expected otel-ca volume")
	}

	hasMount := false
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.Name == otelCAVolumeName {
			hasMount = true
			if m.MountPath != otelCAMountPath {
				t.Fatalf("expected mount path %q, got %q", otelCAMountPath, m.MountPath)
			}
		}
	}
	if !hasMount {
		t.Fatal("expected otel-ca volume mount")
	}
}

func TestCreate_NoOTEL_NoEnvVars(t *testing.T) {
	cache := testCache(t, "bare-pod")
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	name, err := mgr.Create(context.Background(), testSMRun(), "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var pod corev1.Pod
	if err := fc.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "test-ns"}, &pod); err != nil {
		t.Fatalf("pod not found: %v", err)
	}

	for _, e := range pod.Spec.Containers[0].Env {
		switch e.Name {
		case "OTEL_EXPORTER_OTLP_ENDPOINT", "LIGHTSPEED_AGENTICRUN_UID", "LIGHTSPEED_AGENTICRUN_STEP":
			t.Fatalf("%s should not be present when the endpoint is empty", e.Name)
		}
	}
}

func TestCreate_OTELEnvVars_SandboxClaim(t *testing.T) {
	cache := testCacheWithOTEL(t, "sandbox-claim", "dns:///otel-collector.ns.svc:4317", "https://otel-collector.ns.svc:8080", "otel-ca-secret")
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	run := testSMRun()
	name, err := mgr.Create(context.Background(), run, "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if name[0:3] != "ls-" {
		t.Fatalf("expected 'ls-' prefix, got %q", name)
	}

	tmpl := &unstructured.Unstructured{}
	tmpl.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Kind: "SandboxTemplate",
	})
	if err := fc.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "test-ns"}, tmpl); err != nil {
		t.Fatalf("SandboxTemplate not found: %v", err)
	}

	containers, found, _ := unstructured.NestedSlice(tmpl.Object, "spec", "podTemplate", "spec", "containers")
	if !found || len(containers) == 0 {
		t.Fatal("expected containers in SandboxTemplate podTemplate spec")
	}
	container := containers[0].(map[string]interface{})
	envList, _, _ := unstructured.NestedSlice(container, "env")

	envMap := map[string]string{}
	for _, e := range envList {
		em := e.(map[string]interface{})
		if n, ok := em["name"].(string); ok {
			if v, ok := em["value"].(string); ok {
				envMap[n] = v
			}
		}
	}

	if v := envMap["OTEL_EXPORTER_OTLP_ENDPOINT"]; v != "dns:///otel-collector.ns.svc:4317" {
		t.Fatalf("expected OTEL endpoint in SandboxTemplate, got %q", v)
	}
	if v := envMap["LIGHTSPEED_AGENTICRUN_UID"]; v != string(run.UID) {
		t.Fatalf("expected run UID %q in SandboxTemplate, got %q", run.UID, v)
	}
	if v := envMap["LIGHTSPEED_AGENTICRUN_STEP"]; v != "analysis" {
		t.Fatalf("expected run step %q in SandboxTemplate, got %q", "analysis", v)
	}
	deadline, found, _ := unstructured.NestedInt64(tmpl.Object, "spec", "podTemplate", "spec", "activeDeadlineSeconds")
	if !found {
		t.Fatal("expected activeDeadlineSeconds in SandboxTemplate podTemplate spec")
	}
	if deadline != int64(15*time.Minute/time.Second) {
		t.Fatalf("expected activeDeadlineSeconds=%d, got %d", int64(15*time.Minute/time.Second), deadline)
	}
}

// --- Release tests ---

func TestRelease_BarePod(t *testing.T) {
	cache := testCache(t, "bare-pod")
	run := testSMRun()
	run.Status.Steps.Analysis.Sandbox.ClaimName = "p-analysis-test-run"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p-analysis-test-run", Namespace: "test-ns"},
	}
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(pod).Build()
	mgr := newTestSandboxManager(fc, cache)

	if err := mgr.Release(context.Background(), run, "analysis", nil); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	var check corev1.Pod
	err := fc.Get(context.Background(), types.NamespacedName{Name: "p-analysis-test-run", Namespace: "test-ns"}, &check)
	if err == nil {
		t.Fatal("expected pod to be deleted")
	}
}

func TestRelease_BarePod_Idempotent(t *testing.T) {
	cache := testCache(t, "bare-pod")
	run := testSMRun()
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	if err := mgr.Release(context.Background(), run, "analysis", nil); err != nil {
		t.Fatalf("Release of non-existent pod should succeed, got: %v", err)
	}
}

func TestRelease_SandboxClaim_Idempotent(t *testing.T) {
	cache := testCache(t, "sandbox-claim")
	run := testSMRun()
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	if err := mgr.Release(context.Background(), run, "analysis", nil); err != nil {
		t.Fatalf("Release of non-existent claim should succeed, got: %v", err)
	}
}

// --- Name prefix ---

func TestNamePrefix_LSPrefix(t *testing.T) {
	for _, mode := range []string{"bare-pod", "sandbox-claim", ""} {
		t.Run(mode, func(t *testing.T) {
			cache := testCache(t, mode)
			fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
			mgr := newTestSandboxManager(fc, cache)

			name, err := mgr.Create(context.Background(), testSMRun(), "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}
			if name[:3] != "ls-" {
				t.Fatalf("expected 'ls-' prefix, got %q (full name: %q)", name[:3], name)
			}
		})
	}
}

func TestNamePrefix_LongNameTruncated(t *testing.T) {
	cache := testCache(t, "bare-pod")
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	longRun := testSMRun()
	longRun.Name = "a-very-long-run-name-that-exceeds-sixty-three-characters-in-total-length"

	name, err := mgr.Create(context.Background(), longRun, "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if len(name) > 63 {
		t.Fatalf("name exceeds 63 chars: len=%d, name=%q", len(name), name)
	}
	if name[:3] != "ls-" {
		t.Fatalf("prefix lost after truncation: %q", name)
	}
}

// --- podSpecToUnstructured ---

func TestPodSpecToUnstructured(t *testing.T) {
	spec := &corev1.PodSpec{
		Containers: []corev1.Container{
			{Name: "agent", Image: "test:latest"},
		},
	}
	result, err := podSpecToUnstructured(spec)
	if err != nil {
		t.Fatalf("podSpecToUnstructured failed: %v", err)
	}
	containers, ok := result["containers"]
	if !ok {
		t.Fatal("expected 'containers' key in result")
	}
	arr, ok := containers.([]any)
	if !ok || len(arr) == 0 {
		t.Fatal("expected non-empty containers array")
	}
}

// --- Spoke tests ---

func testSpokeKubeconfigSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spoke-kubeconfig-test-spoke",
			Namespace: "test-ns",
		},
		Data: map[string][]byte{
			"kubeconfig": []byte(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://api.spoke.example.com:6443
  name: spoke
contexts:
- context:
    cluster: spoke
    user: spoke-user
  name: spoke
current-context: spoke
users:
- name: spoke-user
  user:
    token: test-token
`),
		},
	}
}

func testSpokeRun() *agenticv1alpha1.AgenticRun {
	return &agenticv1alpha1.AgenticRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spoke-run",
			Namespace: "test-ns",
			UID:       types.UID("spoke-uid-123"),
		},
		Spec: agenticv1alpha1.AgenticRunSpec{
			Request:       "fix spoke issue",
			TargetCluster: "test-spoke",
		},
	}
}

// TestCreate_SpokeRun_Gated verifies that targetCluster runs are rejected
// until OLS-3951 wires sandbox credentials.
func TestCreate_SpokeRun_Gated(t *testing.T) {
	origClient := NewClientFromConfig
	NewClientFromConfig = fakeNewClient
	t.Cleanup(func() { NewClientFromConfig = origClient })

	cache := testCache(t, "bare-pod")
	hubFC := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(testReaderCRB(), testSpokeKubeconfigSecret()).Build()
	mgr := newTestSandboxManager(hubFC, cache)

	run := testSpokeRun()
	_, err := mgr.Create(context.Background(), run, "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err == nil {
		t.Fatal("expected error for gated targetCluster run")
	}
	if !strings.Contains(err.Error(), "not yet supported") {
		t.Fatalf("expected 'not yet supported' error, got: %v", err)
	}
}

func TestRelease_SpokeRun_CleansUpSpoke(t *testing.T) {
	origClient := NewClientFromConfig

	// Pre-populate the spoke with SA + reader CRB subjects.
	saName := sandboxSAName(testSpokeRun(), "analysis")
	spokeSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: spokeManagedNamespace},
	}
	spokeCRBs := spokeReaderBindings()
	// Add the SA as subject to both CRBs (as Create would have done).
	for _, crb := range spokeCRBs {
		crb.Subjects = append(crb.Subjects, rbacv1.Subject{
			Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: spokeManagedNamespace,
		})
	}
	spokeFC := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		spokeCRBs[0], spokeCRBs[1], spokeSA,
	).Build()
	NewClientFromConfig = func(cfg *rest.Config) (client.Client, error) {
		return spokeFC, nil
	}
	t.Cleanup(func() { NewClientFromConfig = origClient })

	cache := testCache(t, "bare-pod")
	hubFC := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(testReaderCRB(), testSpokeKubeconfigSecret()).Build()
	mgr := newTestSandboxManager(hubFC, cache)

	run := testSpokeRun()
	run.Status.Steps.Analysis.Sandbox.ClaimName = "ls-analysis-" + string(run.UID)

	// Create the hub-side pod so releaseBarePod doesn't error.
	hubPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: run.Status.Steps.Analysis.Sandbox.ClaimName, Namespace: "test-ns"},
	}
	if err := hubFC.Create(context.Background(), hubPod); err != nil {
		t.Fatalf("create hub pod: %v", err)
	}

	spoke := &SpokeAccess{Client: spokeFC, Namespace: spokeManagedNamespace}
	if err := mgr.Release(context.Background(), run, "analysis", spoke); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// SA should be deleted from spoke.
	var sa corev1.ServiceAccount
	if err := spokeFC.Get(context.Background(), types.NamespacedName{Name: saName, Namespace: spokeManagedNamespace}, &sa); err == nil {
		t.Fatal("spoke SA should be deleted after Release")
	}

	// SA subject should be removed from spoke CRBs.
	for _, name := range spokeReaderBindingNames {
		var crb rbacv1.ClusterRoleBinding
		if err := spokeFC.Get(context.Background(), types.NamespacedName{Name: name}, &crb); err != nil {
			t.Fatalf("get spoke CRB %s: %v", name, err)
		}
		for _, s := range crb.Subjects {
			if s.Name == saName {
				t.Fatalf("SA %s should be removed from spoke CRB %s", saName, name)
			}
		}
	}
}

func TestRelease_SpokeUnreachable_HubCleanupContinues(t *testing.T) {
	// Spoke kubeconfig Secret is missing (spoke decommissioned).
	// Hub cleanup should still proceed.
	cache := testCache(t, "bare-pod")
	hubFC := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(hubFC, cache)

	run := testSpokeRun()
	run.Status.Steps.Analysis.Sandbox.ClaimName = "ls-analysis-" + string(run.UID)

	// Create the hub-side pod.
	hubPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: run.Status.Steps.Analysis.Sandbox.ClaimName, Namespace: "test-ns"},
	}
	if err := hubFC.Create(context.Background(), hubPod); err != nil {
		t.Fatalf("create hub pod: %v", err)
	}

	// Release should not error — spoke failure is logged, hub cleanup proceeds.
	if err := mgr.Release(context.Background(), run, "analysis", nil); err != nil {
		t.Fatalf("Release should succeed even when spoke is unreachable, got: %v", err)
	}

	// Hub pod should be deleted.
	var pod corev1.Pod
	if err := hubFC.Get(context.Background(), types.NamespacedName{Name: run.Status.Steps.Analysis.Sandbox.ClaimName, Namespace: "test-ns"}, &pod); err == nil {
		t.Fatal("hub pod should be deleted")
	}
}

func TestCreate_LocalRun_Unchanged(t *testing.T) {
	// Regression: local run (no targetCluster) still works as before.
	cache := testCache(t, "bare-pod")
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(testReaderCRB()).Build()
	mgr := newTestSandboxManager(fc, cache)

	run := testSMRun()
	name, err := mgr.Create(context.Background(), run, "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// SA should be on hub.
	saName := sandboxSAName(run, "analysis")
	var sa corev1.ServiceAccount
	if err := fc.Get(context.Background(), types.NamespacedName{Name: saName, Namespace: "test-ns"}, &sa); err != nil {
		t.Fatalf("SA not found on hub: %v", err)
	}
	// Hub SA should have owner ref (set by setSAOwner).
	if len(sa.OwnerReferences) == 0 {
		t.Fatal("hub SA should have owner refs")
	}
	// Hub SA should NOT have spoke labels.
	if _, ok := sa.Labels[LabelSpokeCluster]; ok {
		t.Fatal("hub SA should not have spoke-cluster label")
	}

	// Pod should exist.
	var pod corev1.Pod
	if err := fc.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "test-ns"}, &pod); err != nil {
		t.Fatalf("pod not found: %v", err)
	}
}

// TestCreate_OwnerPatchFailure_DoesNotDeleteWorkload verifies that a
// post-workload owner-ref patch failure does NOT trigger
// cleanupOnCreateFailure. The pod should still exist, and its
// dependencies (ConfigMap, SA, RBAC) should remain intact.
func TestCreate_OwnerPatchFailure_DoesNotDeleteWorkload(t *testing.T) {
	fc := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(testReaderCRB()).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				// Fail the ConfigMap owner-ref patch (setInputConfigMapOwner).
				if _, ok := obj.(*corev1.ConfigMap); ok {
					return fmt.Errorf("simulated owner patch failure")
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	cache := testCache(t, "bare-pod")
	mgr := newTestSandboxManager(fc, cache)
	run := testSMRun()

	_, err := mgr.Create(context.Background(), run, "analysis", testSMAgent(), testLLMForManager(), nil, 15*time.Minute, nil)
	if err == nil {
		t.Fatal("expected error from owner-ref patch failure")
	}

	// Pod should still exist — cleanup must not delete it.
	podName := fmt.Sprintf("ls-analysis-%s", run.UID)
	var pod corev1.Pod
	if err := fc.Get(context.Background(), types.NamespacedName{Name: podName, Namespace: "test-ns"}, &pod); err != nil {
		t.Fatalf("pod should still exist after owner-patch failure, got: %v", err)
	}

	// ConfigMap should still exist — cleanup must not delete it.
	cmName := inputConfigMapName("analysis", string(run.UID))
	var cm corev1.ConfigMap
	if err := fc.Get(context.Background(), types.NamespacedName{Name: cmName, Namespace: "test-ns"}, &cm); err != nil {
		t.Fatalf("ConfigMap should still exist after owner-patch failure, got: %v", err)
	}

	// SA should still exist — cleanup must not delete it.
	saName := sandboxSAName(run, "analysis")
	var sa corev1.ServiceAccount
	if err := fc.Get(context.Background(), types.NamespacedName{Name: saName, Namespace: "test-ns"}, &sa); err != nil {
		t.Fatalf("SA should still exist after owner-patch failure, got: %v", err)
	}
}
