package system

import (
	"context"
	"strings"
	"testing"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestResume_Suspended(t *testing.T) {
	streams, out, _ := fakeStreams()
	fc := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(testConfig(true)).Build()

	o := &ResumeOptions{client: fc, IOStreams: streams}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "resumed") {
		t.Errorf("expected resumed message, got %q", got)
	}

	cfg := &agenticv1alpha1.AgenticOLSConfig{}
	if err := fc.Get(context.Background(), types.NamespacedName{Name: configName}, cfg); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Spec.Suspended {
		t.Error("expected Suspended=false after resume")
	}
}

func TestResume_NotSuspended(t *testing.T) {
	streams, out, _ := fakeStreams()
	fc := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(testConfig(false)).Build()

	o := &ResumeOptions{client: fc, IOStreams: streams}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "not suspended") {
		t.Errorf("expected not suspended message, got %q", got)
	}
}

func TestResume_NoCR(t *testing.T) {
	streams, out, _ := fakeStreams()
	fc := fake.NewClientBuilder().WithScheme(testScheme()).Build()

	o := &ResumeOptions{client: fc, IOStreams: streams}
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := out.String(); !strings.Contains(got, "not suspended") {
		t.Errorf("expected not suspended message when no CR, got %q", got)
	}
}
