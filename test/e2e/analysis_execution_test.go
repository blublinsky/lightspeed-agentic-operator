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

// TestAnalysisFlow_AgenticRunToProposed validates the first step of the run workflow:
//
//  1. Create prerequisite CRDs (LLMProvider, Agent, ApprovalPolicy, Secret)
//  2. Create a AgenticRun CR
//  3. Wait for the operator to reconcile through analysis
//  4. Assert: AgenticRunApproval exists, AnalysisResult exists, AgenticRun phase = Proposed
//  5. Delete AgenticRun and verify sandbox released (finalizer completes)
func TestAnalysisFlow_AgenticRunToProposed(t *testing.T) {
	t.Log("=== TestAnalysisFlow_AgenticRunToProposed: validates Pending → Analyzing → Proposed ===")
	c := newClient(t)
	ctx := context.Background()

	t.Log("Creating fixtures (LLMProvider, Agent, ApprovalPolicy, Secret)")
	createFixtures(t, c)
	prop := createAgenticRun(t, c, "e2e-analysis-flow")
	t.Logf("AgenticRun created: %s/%s", testNS, prop.Name)

	t.Log("Waiting for phase: Proposed (analysis complete)")
	updated := waitForPhase(t, c, prop.Name, agenticv1alpha1.AgenticRunPhaseProposed)
	t.Log("Phase reached: Proposed")

	// Condition: Analyzed=True
	var analyzedFound bool
	for _, cond := range updated.Status.Conditions {
		if cond.Type == agenticv1alpha1.AgenticRunConditionAnalyzed {
			analyzedFound = true
			if cond.Status != metav1.ConditionTrue {
				t.Errorf("Analyzed condition status = %s, want True", cond.Status)
			}
		}
	}
	if !analyzedFound {
		t.Error("Analyzed condition not found on AgenticRun status")
	}
	t.Log("Verified: Analyzed=True condition present")

	// AgenticRunApproval exists with owner reference.
	var approval agenticv1alpha1.AgenticRunApproval
	if err := c.Get(ctx, types.NamespacedName{Name: prop.Name, Namespace: testNS}, &approval); err != nil {
		t.Fatalf("get AgenticRunApproval: %v", err)
	}
	if len(approval.OwnerReferences) == 0 {
		t.Error("AgenticRunApproval has no owner references")
	} else if approval.OwnerReferences[0].Name != prop.Name {
		t.Errorf("AgenticRunApproval owner = %q, want %q", approval.OwnerReferences[0].Name, prop.Name)
	}
	t.Log("Verified: AgenticRunApproval exists with correct owner reference")

	// AnalysisResult exists with owner reference and options.
	var analysisList agenticv1alpha1.AnalysisResultList
	if err := c.List(ctx, &analysisList, client.InNamespace(testNS), client.MatchingLabels{"agentic.openshift.io/run": prop.Name}); err != nil {
		t.Fatalf("list AnalysisResult: %v", err)
	}
	if len(analysisList.Items) == 0 {
		t.Fatal("no AnalysisResult found for run")
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
	if opt.RemediationPlan.Description == "" {
		t.Error("option run description is empty")
	}
	t.Logf("Verified: AnalysisResult %s with %d option(s), title=%q", ar.Name, len(ar.Status.Options), opt.Title)

	// Sandbox info recorded.
	if updated.Status.Steps.Analysis.Sandbox.ClaimName == "" {
		t.Error("status.steps.analysis.sandbox.claimName is empty")
	}
	t.Logf("Verified: sandbox info recorded, claimName=%s", updated.Status.Steps.Analysis.Sandbox.ClaimName)

	// Results tracked.
	if len(updated.Status.Steps.Analysis.Results) == 0 {
		t.Fatal("status.steps.analysis.results is empty")
	}
	if updated.Status.Steps.Analysis.Results[0].Name == "" {
		t.Error("analysis result ref name is empty")
	}
	t.Logf("Verified: analysis results tracked, ref=%s", updated.Status.Steps.Analysis.Results[0].Name)

	// Delete AgenticRun and verify sandbox released.
	claimName := updated.Status.Steps.Analysis.Sandbox.ClaimName
	t.Log("Deleting AgenticRun — verifying finalizer cleanup")
	if err := c.Delete(ctx, prop); err != nil {
		t.Fatalf("delete AgenticRun: %v", err)
	}
	waitForDeletion(t, c, prop.Name)
	t.Logf("Verified: AgenticRun deleted, sandbox %s released", claimName)
	t.Log("PASS: analysis complete, phase=Proposed, sandbox released")
}
