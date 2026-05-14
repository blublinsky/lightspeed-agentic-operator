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

// TestExecutionFlow_ProposedToVerifying validates the execution phase:
//
//  1. Create Proposal, wait for Proposed (analysis complete)
//  2. Approve execution (select option 0)
//  3. Wait for phase = Executing — assert RBAC exists (mock has 60s delay)
//  4. Wait for phase = Verifying (execution complete)
//  5. Assert: ExecutionResult exists, Executed=True, sandbox info, RBAC annotation
//  6. Delete Proposal, verify RBAC cleaned up
func TestExecutionFlow_ProposedToVerifying(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	createFixtures(t, c)
	prop := createProposal(t, c, "e2e-execution-flow")

	// Drive to Proposed (analysis complete).
	waitForPhase(t, c, prop.Name, agenticv1alpha1.ProposalPhaseProposed)

	// Approve execution with option 0.
	approveExecution(t, c, prop.Name, 0)

	// Wait for Executing phase — RBAC should exist during this window (mock delays 60s).
	waitForPhase(t, c, prop.Name, agenticv1alpha1.ProposalPhaseExecuting)

	// --- Verify: RBAC created ---
	roleName := "ls-exec-" + prop.Name
	var role rbacv1.Role
	if err := c.Get(ctx, types.NamespacedName{Name: roleName, Namespace: "staging"}, &role); err != nil {
		t.Fatalf("get Role %s in staging: %v", roleName, err)
	}
	t.Logf("RBAC Role %s exists in staging namespace", roleName)

	var binding rbacv1.RoleBinding
	if err := c.Get(ctx, types.NamespacedName{Name: roleName, Namespace: "staging"}, &binding); err != nil {
		t.Fatalf("get RoleBinding %s in staging: %v", roleName, err)
	}

	// Verify annotation on Proposal.
	var current agenticv1alpha1.Proposal
	if err := c.Get(ctx, types.NamespacedName{Name: prop.Name, Namespace: testNS}, &current); err != nil {
		t.Fatalf("get Proposal: %v", err)
	}
	if current.Annotations["agentic.openshift.io/rbac-namespaces"] == "" {
		t.Error("rbac-namespaces annotation is empty")
	}

	// Wait for execution to complete → Verifying phase.
	updated := waitForPhase(t, c, prop.Name, agenticv1alpha1.ProposalPhaseVerifying)

	// --- Verify: Executed condition ---
	var executedFound bool
	for _, cond := range updated.Status.Conditions {
		if cond.Type == agenticv1alpha1.ProposalConditionExecuted {
			executedFound = true
			if cond.Status != metav1.ConditionTrue {
				t.Errorf("Executed condition status = %s, want True", cond.Status)
			}
		}
	}
	if !executedFound {
		t.Error("Executed condition not found")
	}

	// --- Verify: ExecutionResult exists ---
	var execList agenticv1alpha1.ExecutionResultList
	if err := c.List(ctx, &execList, client.InNamespace(testNS), client.MatchingLabels{"agentic.openshift.io/proposal": prop.Name}); err != nil {
		t.Fatalf("list ExecutionResult: %v", err)
	}
	if len(execList.Items) == 0 {
		t.Fatal("no ExecutionResult found")
	}
	if len(execList.Items[0].OwnerReferences) == 0 {
		t.Error("ExecutionResult has no owner references")
	}

	// --- Verify: execution sandbox info ---
	if updated.Status.Steps.Execution.Sandbox.ClaimName == "" {
		t.Error("status.steps.execution.sandbox.claimName is empty")
	}

	// --- Cleanup and verify RBAC removed ---
	if err := c.Delete(ctx, prop); err != nil {
		t.Fatalf("delete Proposal: %v", err)
	}
	waitForDeletion(t, c, prop.Name)

	if err := c.Get(ctx, types.NamespacedName{Name: roleName, Namespace: "staging"}, &role); err == nil {
		t.Errorf("Role %s still exists after Proposal deletion — RBAC not cleaned up", roleName)
	}

	t.Logf("PASS: execution complete, RBAC created and cleaned up")
}
