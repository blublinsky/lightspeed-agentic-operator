package run

import (
	"context"
	"strings"
	"testing"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDelete_Success(t *testing.T) {
	streams, out, _ := fakeStreams()
	p := testAgenticRunWithStatus("fix-crash", "default", agenticv1alpha1.AgenticRunPhaseCompleted)

	fc := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(p).Build()

	o := &DeleteOptions{
		client:    fc,
		name:      "fix-crash",
		namespace: "default",
		IOStreams: streams,
	}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "deleted") {
		t.Errorf("expected 'deleted' in output, got: %s", out.String())
	}

	// Verify it's gone
	list := &agenticv1alpha1.AgenticRunList{}
	if err := fc.List(context.Background(), list); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("expected 0 runs after delete, got %d", len(list.Items))
	}
}

func TestDelete_NotFound(t *testing.T) {
	streams, _, _ := fakeStreams()
	fc := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	o := &DeleteOptions{
		client:    fc,
		name:      "nonexistent",
		namespace: "default",
		IOStreams: streams,
	}
	err := o.Run(context.Background())
	if err == nil {
		t.Fatal("expected error for nonexistent run")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention run name, got: %v", err)
	}
}

func TestDelete_OutputMessage(t *testing.T) {
	streams, out, _ := fakeStreams()
	p := testAgenticRunWithStatus("my-run", "default", agenticv1alpha1.AgenticRunPhasePending)

	fc := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(p).Build()

	o := &DeleteOptions{
		client:    fc,
		name:      "my-run",
		namespace: "default",
		IOStreams: streams,
	}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if out.String() != "run/my-run deleted\n" {
		t.Errorf("unexpected output: %q", out.String())
	}
}
