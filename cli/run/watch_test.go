package run

import (
	"testing"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

func TestWatch_AgenticRunGVR(t *testing.T) {
	if agenticRunGVR.Group != "agentic.openshift.io" {
		t.Errorf("expected group agentic.openshift.io, got %s", agenticRunGVR.Group)
	}
	if agenticRunGVR.Version != "v1alpha1" {
		t.Errorf("expected version v1alpha1, got %s", agenticRunGVR.Version)
	}
	if agenticRunGVR.Resource != "agenticruns" {
		t.Errorf("expected resource agenticruns, got %s", agenticRunGVR.Resource)
	}
}

func TestWatch_TerminalPhaseExits(t *testing.T) {
	terminal := []agenticv1alpha1.AgenticRunPhase{
		agenticv1alpha1.AgenticRunPhaseCompleted,
		agenticv1alpha1.AgenticRunPhaseFailed,
		agenticv1alpha1.AgenticRunPhaseDenied,
		agenticv1alpha1.AgenticRunPhaseEscalated,
	}
	for _, p := range terminal {
		if !IsTerminalPhase(p) {
			t.Errorf("expected %s to be terminal", p)
		}
	}

	nonTerminal := []agenticv1alpha1.AgenticRunPhase{
		agenticv1alpha1.AgenticRunPhasePending,
		agenticv1alpha1.AgenticRunPhaseAnalyzing,
		agenticv1alpha1.AgenticRunPhaseExecuting,
		agenticv1alpha1.AgenticRunPhaseVerifying,
	}
	for _, p := range nonTerminal {
		if IsTerminalPhase(p) {
			t.Errorf("expected %s to be non-terminal", p)
		}
	}
}
