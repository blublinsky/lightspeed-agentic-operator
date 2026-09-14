package agenticrun

import (
	"context"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

const testOperatorNS = "openshift-lightspeed"

// fakeNewClient replaces NewClientFromConfig in tests so no real
// cluster connection is attempted. Returns a fresh fake client each time.
func fakeNewClient(cfg *rest.Config) (client.Client, error) {
	return fake.NewClientBuilder().WithScheme(testScheme()).Build(), nil
}

func TestSpokeAccessForRun_EmptyTargetCluster(t *testing.T) {
	run := &agenticv1alpha1.AgenticRun{
		Spec: agenticv1alpha1.AgenticRunSpec{},
	}
	hubClient := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	sa, err := spokeAccessForRun(context.Background(), hubClient, run, testOperatorNS)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sa != nil {
		t.Fatal("expected nil SpokeAccess for empty targetCluster")
	}
}

func TestSpokeAccessForRun_SecretExists(t *testing.T) {
	orig := NewClientFromConfig
	NewClientFromConfig = fakeNewClient
	t.Cleanup(func() { NewClientFromConfig = orig })

	kubeconfig := []byte(`
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
`)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spoke-kubeconfig-test-spoke",
			Namespace: testOperatorNS,
		},
		Data: map[string][]byte{
			kubeconfigKey: kubeconfig,
		},
	}

	hubClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(secret).
		Build()

	run := &agenticv1alpha1.AgenticRun{
		Spec: agenticv1alpha1.AgenticRunSpec{
			TargetCluster: "test-spoke",
		},
	}

	sa, err := spokeAccessForRun(context.Background(), hubClient, run, testOperatorNS)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sa == nil {
		t.Fatal("expected non-nil SpokeAccess")
	}
	if sa.Namespace != spokeManagedNamespace {
		t.Errorf("namespace = %q, want %q", sa.Namespace, spokeManagedNamespace)
	}
	if sa.Client == nil {
		t.Error("expected non-nil spoke client")
	}
	if sa.Config == nil {
		t.Error("expected non-nil rest.Config")
	}
	if sa.Config.Timeout != spokeDialTimeout {
		t.Errorf("timeout = %v, want %v", sa.Config.Timeout, spokeDialTimeout)
	}
}

func TestSpokeAccessForRun_SecretMissing(t *testing.T) {
	hubClient := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	run := &agenticv1alpha1.AgenticRun{
		Spec: agenticv1alpha1.AgenticRunSpec{
			TargetCluster: "nonexistent",
		},
	}

	sa, err := spokeAccessForRun(context.Background(), hubClient, run, testOperatorNS)
	if err == nil {
		t.Fatal("expected error for missing Secret")
	}
	if sa != nil {
		t.Fatal("expected nil SpokeAccess on error")
	}

	// Error message should include the Secret name for debugging.
	want := "spoke-kubeconfig-nonexistent"
	if got := err.Error(); !contains(got, want) {
		t.Errorf("error = %q, want it to contain %q", got, want)
	}
}

func TestSpokeAccessForRun_MalformedKubeconfig(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spoke-kubeconfig-bad",
			Namespace: testOperatorNS,
		},
		Data: map[string][]byte{
			kubeconfigKey: []byte("not valid yaml {{{"),
		},
	}

	hubClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(secret).
		Build()

	run := &agenticv1alpha1.AgenticRun{
		Spec: agenticv1alpha1.AgenticRunSpec{
			TargetCluster: "bad",
		},
	}

	sa, err := spokeAccessForRun(context.Background(), hubClient, run, testOperatorNS)
	if err == nil {
		t.Fatal("expected error for malformed kubeconfig")
	}
	if sa != nil {
		t.Fatal("expected nil SpokeAccess on error")
	}
}

func TestSpokeAccessForRun_MissingKubeconfigKey(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spoke-kubeconfig-nokey",
			Namespace: testOperatorNS,
		},
		Data: map[string][]byte{
			"wrong-key": []byte("some data"),
		},
	}

	hubClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(secret).
		Build()

	run := &agenticv1alpha1.AgenticRun{
		Spec: agenticv1alpha1.AgenticRunSpec{
			TargetCluster: "nokey",
		},
	}

	sa, err := spokeAccessForRun(context.Background(), hubClient, run, testOperatorNS)
	if err == nil {
		t.Fatal("expected error for missing kubeconfig key")
	}
	if sa != nil {
		t.Fatal("expected nil SpokeAccess on error")
	}

	want := `missing "kubeconfig" key`
	if got := err.Error(); !contains(got, want) {
		t.Errorf("error = %q, want it to contain %q", got, want)
	}
}

func TestSpokeAccessForRun_RejectsHTTP(t *testing.T) {
	orig := NewClientFromConfig
	NewClientFromConfig = fakeNewClient
	t.Cleanup(func() { NewClientFromConfig = orig })

	kubeconfig := []byte(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: http://api.spoke.example.com:6443
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
`)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spoke-kubeconfig-http-spoke",
			Namespace: testOperatorNS,
		},
		Data: map[string][]byte{
			kubeconfigKey: kubeconfig,
		},
	}

	hubClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(secret).
		Build()

	run := &agenticv1alpha1.AgenticRun{
		Spec: agenticv1alpha1.AgenticRunSpec{
			TargetCluster: "http-spoke",
		},
	}

	sa, err := spokeAccessForRun(context.Background(), hubClient, run, testOperatorNS)
	if err == nil {
		t.Fatal("expected error for HTTP kubeconfig")
	}
	if sa != nil {
		t.Fatal("expected nil SpokeAccess on error")
	}
	if !contains(err.Error(), ErrSpokeInsecureTLS) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), ErrSpokeInsecureTLS)
	}
}

func TestSpokeAccessForRun_RejectsInsecureTLS(t *testing.T) {
	orig := NewClientFromConfig
	NewClientFromConfig = fakeNewClient
	t.Cleanup(func() { NewClientFromConfig = orig })

	kubeconfig := []byte(`
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://api.spoke.example.com:6443
    insecure-skip-tls-verify: true
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
`)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spoke-kubeconfig-insecure-spoke",
			Namespace: testOperatorNS,
		},
		Data: map[string][]byte{
			kubeconfigKey: kubeconfig,
		},
	}

	hubClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(secret).
		Build()

	run := &agenticv1alpha1.AgenticRun{
		Spec: agenticv1alpha1.AgenticRunSpec{
			TargetCluster: "insecure-spoke",
		},
	}

	sa, err := spokeAccessForRun(context.Background(), hubClient, run, testOperatorNS)
	if err == nil {
		t.Fatal("expected error for insecure TLS kubeconfig")
	}
	if sa != nil {
		t.Fatal("expected nil SpokeAccess on error")
	}
	if !contains(err.Error(), ErrSpokeInsecureTLS) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), ErrSpokeInsecureTLS)
	}
}

// ---------------------------------------------------------------------------
// spokeLabels
// ---------------------------------------------------------------------------

func TestSpokeLabels(t *testing.T) {
	labels := spokeLabels("uid-123", "my-run", "prod-spoke", "sandbox-sa")

	want := map[string]string{
		LabelRun:          "uid-123",
		LabelComponent:    "sandbox-sa",
		LabelSpokeCluster: "prod-spoke",
		LabelAgenticRun:   "my-run",
	}
	if len(labels) != len(want) {
		t.Fatalf("expected %d labels, got %d: %v", len(want), len(labels), labels)
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %q = %q, want %q", k, labels[k], v)
		}
	}
}

func TestSpokeLabels_LongValuesTruncated(t *testing.T) {
	// 80-char cluster name exceeds the 63-char label limit.
	longCluster := "my-very-long-spoke-cluster-name-that-definitely-exceeds-sixty-three-characters-x"
	longRun := "my-very-long-agentic-run-name-that-also-exceeds-sixty-three-characters-padding-y"

	labels := spokeLabels("uid-123", longRun, longCluster, "analysis-sa")

	for _, key := range []string{LabelSpokeCluster, LabelAgenticRun} {
		v := labels[key]
		if len(v) > maxLabelValueLen {
			t.Errorf("label %q value length %d exceeds %d: %q", key, len(v), maxLabelValueLen, v)
		}
	}
}

func TestTruncateLabelValue_ShortUnchanged(t *testing.T) {
	v := "short-value"
	if got := truncateLabelValue(v); got != v {
		t.Errorf("expected %q unchanged, got %q", v, got)
	}
}

func TestTruncateLabelValue_ExactlyMaxUnchanged(t *testing.T) {
	v := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 63 chars
	if len(v) != maxLabelValueLen {
		t.Fatalf("test setup: len = %d, want %d", len(v), maxLabelValueLen)
	}
	if got := truncateLabelValue(v); got != v {
		t.Errorf("expected %q unchanged, got %q", v, got)
	}
}

func TestTruncateLabelValue_LongTruncatedWithHash(t *testing.T) {
	v := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-overflow" // 72 chars
	got := truncateLabelValue(v)
	if len(got) != maxLabelValueLen {
		t.Errorf("expected length %d, got %d: %q", maxLabelValueLen, len(got), got)
	}
	// same input must produce same output (deterministic)
	if got2 := truncateLabelValue(v); got != got2 {
		t.Errorf("non-deterministic: %q vs %q", got, got2)
	}
	// different inputs must produce different outputs
	v2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-overflow" // 72 chars
	if got2 := truncateLabelValue(v2); got == got2 {
		t.Errorf("collision: both %q", got)
	}
}

func TestSpokeLabels_HubResourcesNotLabeled(t *testing.T) {
	// Hub resources use rbacLabels() which has only LabelRun + LabelComponent.
	hubLabels := rbacLabels("uid-123", "execution-rbac")
	if _, ok := hubLabels[LabelSpokeCluster]; ok {
		t.Fatal("hub resources should NOT have spoke-cluster label")
	}
	if _, ok := hubLabels[LabelAgenticRun]; ok {
		t.Fatal("hub resources should NOT have agentic-run label")
	}
}

// ---------------------------------------------------------------------------
// requestSpokeToken
// ---------------------------------------------------------------------------

func TestRequestSpokeToken(t *testing.T) {
	orig := NewClientsetFromConfig
	cs := k8sfake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts/token", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction := action.(k8stesting.CreateAction)
		treq := createAction.GetObject().(*authenticationv1.TokenRequest)
		// Verify 24h expiration was requested.
		if treq.Spec.ExpirationSeconds == nil || *treq.Spec.ExpirationSeconds != 86400 {
			t.Errorf("expiration = %v, want 86400", treq.Spec.ExpirationSeconds)
		}
		return true, &authenticationv1.TokenRequest{
			Status: authenticationv1.TokenRequestStatus{Token: "test-spoke-token-abc"},
		}, nil
	})
	NewClientsetFromConfig = func(cfg *rest.Config) (kubernetes.Interface, error) {
		return cs, nil
	}
	t.Cleanup(func() { NewClientsetFromConfig = orig })

	token, err := requestSpokeToken(context.Background(), &rest.Config{}, "ls-anl-uid1", spokeManagedNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "test-spoke-token-abc" {
		t.Errorf("token = %q, want test-spoke-token-abc", token)
	}
}

func TestRequestSpokeToken_Error(t *testing.T) {
	orig := NewClientsetFromConfig
	cs := k8sfake.NewSimpleClientset()
	// No reactor — CreateToken will fail because SA doesn't exist in fake.
	NewClientsetFromConfig = func(cfg *rest.Config) (kubernetes.Interface, error) {
		return cs, nil
	}
	t.Cleanup(func() { NewClientsetFromConfig = orig })

	_, err := requestSpokeToken(context.Background(), &rest.Config{}, "nonexistent", spokeManagedNamespace)
	if err == nil {
		t.Fatal("expected error for TokenRequest on non-existent SA")
	}
}

// contains is a simple helper to avoid importing strings in tests.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
