package system

import (
	"context"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestStatus_Active(t *testing.T) {
	streams, out, _ := fakeStreams()
	fc := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(testConfig(false)).Build()

	o := &StatusOptions{client: fc, IOStreams: streams}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "Active") {
		t.Errorf("expected Active, got %q", got)
	}
}

func TestStatus_Suspended(t *testing.T) {
	streams, out, _ := fakeStreams()
	fc := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(testConfig(true)).Build()

	o := &StatusOptions{client: fc, IOStreams: streams}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "SUSPENDED") {
		t.Errorf("expected SUSPENDED, got %q", got)
	}
}

func TestStatus_NoCR(t *testing.T) {
	streams, out, _ := fakeStreams()
	fc := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	o := &StatusOptions{client: fc, IOStreams: streams}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "Active") {
		t.Errorf("expected Active when no CR exists, got %q", got)
	}
}
