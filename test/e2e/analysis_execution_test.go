//go:build e2e

// Package e2e contains black-box tests that run against a live cluster with
// the operator already running. The tests create CRs and poll for expected
// status updates — they never import or instantiate operator internals.
//
// Prerequisites:
//   - Operator deployed in-cluster (make deploy IMG=...)
//   - Mock agent SandboxTemplate applied: kubectl apply -f test/agent/sandboxtemplate/sandboxtemplate.yaml
//   - Operator SA has cluster-admin (for RBAC escalation): kubectl create clusterrolebinding e2e-operator-admin --clusterrole=cluster-admin --serviceaccount=default:controller-manager
//   - KUBECONFIG pointing at the cluster
//
// Run: make test-e2e
package e2e

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

// TestAnalysisFlow_ProposalToProposed validates the first step of the proposal workflow:
//
//  1. Create prerequisite CRDs (LLMProvider, Agent, ApprovalPolicy, Secret)
//  2. Create a Proposal CR
//  3. Wait for the operator to reconcile through analysis
//  4. Assert: ProposalApproval exists, AnalysisResult exists, Proposal phase = Proposed
//  5. Delete Proposal and verify sandbox released (finalizer completes)
func TestAnalysisFlow_ProposalToProposed(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	createFixtures(t, c)
	prop := createProposal(t, c, "e2e-analysis-flow")

	// Wait for analysis to complete.
	updated := waitForPhase(t, c, prop.Name, agenticv1alpha1.ProposalPhaseProposed)

	// --- Verify outcomes ---

	// Condition: Analyzed=True
	var analyzedFound bool
	for _, cond := range updated.Status.Conditions {
		if cond.Type == agenticv1alpha1.ProposalConditionAnalyzed {
			analyzedFound = true
			if cond.Status != metav1.ConditionTrue {
				t.Errorf("Analyzed condition status = %s, want True", cond.Status)
			}
		}
	}
	if !analyzedFound {
		t.Error("Analyzed condition not found on Proposal status")
	}

	// ProposalApproval exists with owner reference.
	var approval agenticv1alpha1.ProposalApproval
	if err := c.Get(ctx, types.NamespacedName{Name: prop.Name, Namespace: testNS}, &approval); err != nil {
		t.Fatalf("get ProposalApproval: %v", err)
	}
	if len(approval.OwnerReferences) == 0 {
		t.Error("ProposalApproval has no owner references")
	} else if approval.OwnerReferences[0].Name != prop.Name {
		t.Errorf("ProposalApproval owner = %q, want %q", approval.OwnerReferences[0].Name, prop.Name)
	}

	// AnalysisResult exists with owner reference and options.
	var analysisList agenticv1alpha1.AnalysisResultList
	if err := c.List(ctx, &analysisList, client.InNamespace(testNS), client.MatchingLabels{"agentic.openshift.io/proposal": prop.Name}); err != nil {
		t.Fatalf("list AnalysisResult: %v", err)
	}
	if len(analysisList.Items) == 0 {
		t.Fatal("no AnalysisResult found for proposal")
	}
	ar := analysisList.Items[0]
	if len(ar.OwnerReferences) == 0 {
		t.Error("AnalysisResult has no owner references")
	}
	if len(ar.Status.Options) == 0 {
		t.Fatal("AnalysisResult has no options")
	}
	opt := ar.Status.Options[0]
	if opt.Title == "" {
		t.Error("option title is empty")
	}
	if opt.Diagnosis.Summary == "" {
		t.Error("option diagnosis summary is empty")
	}
	if opt.Proposal.Description == "" {
		t.Error("option proposal description is empty")
	}

	// Sandbox info recorded.
	if updated.Status.Steps.Analysis.Sandbox.ClaimName == "" {
		t.Error("status.steps.analysis.sandbox.claimName is empty")
	}

	// Results tracked.
	if len(updated.Status.Steps.Analysis.Results) == 0 {
		t.Fatal("status.steps.analysis.results is empty")
	}
	if updated.Status.Steps.Analysis.Results[0].Name == "" {
		t.Error("analysis result ref name is empty")
	}

	// --- Delete Proposal and verify sandbox released ---
	claimName := updated.Status.Steps.Analysis.Sandbox.ClaimName
	if err := c.Delete(ctx, prop); err != nil {
		t.Fatalf("delete Proposal: %v", err)
	}
	waitForDeletion(t, c, prop.Name)

	t.Logf("PASS: phase=Proposed, analysisResult=%s, sandbox=%s (released after deletion)",
		ar.Name, claimName)
}
