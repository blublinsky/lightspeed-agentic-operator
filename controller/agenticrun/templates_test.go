package agenticrun

import (
	"strings"
	"testing"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildEscalationRequest_UsesOutcome(t *testing.T) {
	run := &agenticv1alpha1.AgenticRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run",
			Namespace: "default",
		},
		Spec: agenticv1alpha1.AgenticRunSpec{
			Request: "fix the widget",
		},
		Status: agenticv1alpha1.AgenticRunStatus{
			Steps: agenticv1alpha1.StepsStatus{
				Analysis: agenticv1alpha1.AnalysisStepStatus{
					Results: []agenticv1alpha1.StepResultRef{
						{Name: "analysis-1", Outcome: agenticv1alpha1.ActionOutcomeSucceeded},
					},
				},
				Execution: agenticv1alpha1.ExecutionStepStatus{
					Results: []agenticv1alpha1.StepResultRef{
						{Name: "exec-1", Outcome: agenticv1alpha1.ActionOutcomeFailed},
					},
				},
				Verification: agenticv1alpha1.VerificationStepStatus{
					Results: []agenticv1alpha1.StepResultRef{
						{Name: "verify-1", Outcome: agenticv1alpha1.ActionOutcomeFailed},
					},
				},
			},
		},
	}

	result := buildEscalationRequest(run)

	if strings.Contains(result, "template") && strings.Contains(result, "error") {
		t.Fatalf("template rendering failed: %s", result)
	}

	if !strings.Contains(result, "outcome=Succeeded") {
		t.Errorf("expected outcome=Succeeded for analysis result, got: %s", result)
	}
	if !strings.Contains(result, "outcome=Failed") {
		t.Errorf("expected outcome=Failed for execution/verification results, got: %s", result)
	}
}
