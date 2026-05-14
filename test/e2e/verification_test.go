//go:build e2e

package e2e

import (
	"context"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

// TestVerificationFlow_VerifyingToCompleted validates the verification phase:
//
//  1. Create Proposal, drive through analysis + execution to Verifying
//  2. Wait for phase = Completed (verification auto-approved, runs, passes)
//  3. Assert: VerificationResult exists, Verified=True, terminal state
//  4. Delete Proposal, verify RBAC cleaned up
func TestVerificationFlow_VerifyingToCompleted(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	createFixtures(t, c)
	prop := createProposal(t, c, "e2e-verification-flow")

	// Drive to Proposed.
	waitForPhase(t, c, prop.Name, agenticv1alpha1.ProposalPhaseProposed)

	// Approve execution.
	approveExecution(t, c, prop.Name, 0)

	// Wait for Completed (execution + verification both finish).
	updated := waitForPhase(t, c, prop.Name, agenticv1alpha1.ProposalPhaseCompleted)

	// --- Verify: Verified condition ---
	var verifiedFound bool
	for _, cond := range updated.Status.Conditions {
		if cond.Type == agenticv1alpha1.ProposalConditionVerified {
			verifiedFound = true
			if cond.Status != metav1.ConditionTrue {
				t.Errorf("Verified condition status = %s, want True", cond.Status)
			}
		}
	}
	if !verifiedFound {
		t.Error("Verified condition not found")
	}

	// --- Verify: VerificationResult exists ---
	var verifyList agenticv1alpha1.VerificationResultList
	if err := c.List(ctx, &verifyList, client.InNamespace(testNS), client.MatchingLabels{"agentic.openshift.io/proposal": prop.Name}); err != nil {
		t.Fatalf("list VerificationResult: %v", err)
	}
	if len(verifyList.Items) == 0 {
		t.Fatal("no VerificationResult found")
	}
	if len(verifyList.Items[0].OwnerReferences) == 0 {
		t.Error("VerificationResult has no owner references")
	}

	// --- Verify: verification sandbox info ---
	if updated.Status.Steps.Verification.Sandbox.ClaimName == "" {
		t.Error("status.steps.verification.sandbox.claimName is empty")
	}

	// --- Cleanup and verify RBAC removed ---
	roleName := "ls-exec-" + prop.Name
	if err := c.Delete(ctx, prop); err != nil {
		t.Fatalf("delete Proposal: %v", err)
	}
	waitForDeletion(t, c, prop.Name)

	var role rbacv1.Role
	if err := c.Get(ctx, types.NamespacedName{Name: roleName, Namespace: "staging"}, &role); err == nil {
		t.Errorf("Role %s still exists after deletion — RBAC not cleaned up", roleName)
	}

	t.Logf("PASS: verification complete, phase=Completed, RBAC cleaned after deletion")
}
