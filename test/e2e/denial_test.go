//go:build e2e

package e2e

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

// TestDenialFlow_ProposedToDenied validates that denying execution terminates the proposal:
//
//  1. Create Proposal, wait for Proposed (analysis complete)
//  2. Deny execution on ProposalApproval
//  3. Wait for phase = Denied (terminal)
//  4. Assert: Denied condition present, sandboxes released on deletion
func TestDenialFlow_ProposedToDenied(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	createFixtures(t, c)
	prop := createProposal(t, c, "e2e-denial-flow")

	// Drive to Proposed (analysis complete).
	waitForPhase(t, c, prop.Name, agenticv1alpha1.ProposalPhaseProposed)

	// Deny execution.
	denyStage(t, c, prop.Name, agenticv1alpha1.ApprovalStageExecution)

	// Wait for Denied (terminal).
	updated := waitForPhase(t, c, prop.Name, agenticv1alpha1.ProposalPhaseDenied)

	// --- Verify: Denied condition ---
	var deniedFound bool
	for _, cond := range updated.Status.Conditions {
		if cond.Type == agenticv1alpha1.ProposalConditionDenied {
			deniedFound = true
			if cond.Status != metav1.ConditionTrue {
				t.Errorf("Denied condition status = %s, want True", cond.Status)
			}
		}
	}
	if !deniedFound {
		t.Error("Denied condition not found")
	}

	// --- Cleanup ---
	if err := c.Delete(ctx, prop); err != nil {
		t.Fatalf("delete Proposal: %v", err)
	}
	waitForDeletion(t, c, prop.Name)

	t.Logf("PASS: execution denied, phase=Denied (terminal)")
}
