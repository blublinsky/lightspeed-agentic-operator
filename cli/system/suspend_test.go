package system

import (
	"bytes"
	"context"
	"strings"
	"testing"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSuspend_WithConfirmation(t *testing.T) {
	streams, out, _ := fakeStreams()
	streams.In.(*bytes.Buffer).WriteString("y\n")

	fc := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(testConfig(false)).Build()

	o := &SuspendOptions{client: fc, IOStreams: streams}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "suspended") {
		t.Errorf("expected suspended message, got %q", got)
	}

	cfg := &agenticv1alpha1.AgenticOLSConfig{}
	if err := fc.Get(context.Background(), types.NamespacedName{Name: configName}, cfg); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !cfg.Spec.Suspended {
		t.Error("expected Suspended=true after suspend")
	}
}

func TestSuspend_Declined(t *testing.T) {
	streams, out, _ := fakeStreams()
	streams.In.(*bytes.Buffer).WriteString("n\n")

	fc := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(testConfig(false)).Build()

	o := &SuspendOptions{client: fc, IOStreams: streams}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "Aborted") {
		t.Errorf("expected Aborted, got %q", got)
	}

	cfg := &agenticv1alpha1.AgenticOLSConfig{}
	if err := fc.Get(context.Background(), types.NamespacedName{Name: configName}, cfg); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Spec.Suspended {
		t.Error("expected Suspended=false after declining")
	}
}

func TestSuspend_SkipConfirmation(t *testing.T) {
	streams, out, _ := fakeStreams()

	fc := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(testConfig(false)).Build()

	o := &SuspendOptions{client: fc, yes: true, IOStreams: streams}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "suspended") {
		t.Errorf("expected suspended message, got %q", got)
	}
}

func TestSuspend_AlreadySuspended(t *testing.T) {
	streams, out, _ := fakeStreams()

	fc := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(testConfig(true)).Build()

	o := &SuspendOptions{client: fc, yes: true, IOStreams: streams}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "already suspended") {
		t.Errorf("expected already suspended message, got %q", got)
	}
}

func TestSuspend_NoCR_Creates(t *testing.T) {
	streams, out, _ := fakeStreams()

	fc := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	o := &SuspendOptions{client: fc, yes: true, IOStreams: streams}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "suspended") {
		t.Errorf("expected suspended message, got %q", got)
	}

	cfg := &agenticv1alpha1.AgenticOLSConfig{}
	if err := fc.Get(context.Background(), types.NamespacedName{Name: configName}, cfg); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !cfg.Spec.Suspended {
		t.Error("expected Suspended=true on newly created CR")
	}
}
