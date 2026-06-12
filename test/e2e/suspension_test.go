//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

func TestSuspension(t *testing.T) {
	c := newClient(t)
	createFixtures(t, c)
	ctx := context.Background()

	config := &agenticv1alpha1.AgenticOLSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       agenticv1alpha1.AgenticOLSConfigSpec{Suspended: true},
	}
	cleanup(t, c, config)
	t.Cleanup(func() { cleanup(t, c, config) })

	// Activate kill switch.
	config.SetResourceVersion("")
	config.SetUID("")
	if err := c.Create(ctx, config); err != nil {
		t.Fatalf("create AgenticOLSConfig: %v", err)
	}

	// Let the controller cache sync.
	time.Sleep(5 * time.Second)

	prop := createProposal(t, c, "suspend-inflight")

	// Proposal should reach EmergencyStopped on its first reconcile.
	waitForPhase(t, c, prop.Name, agenticv1alpha1.ProposalPhaseEmergencyStopped)
	t.Log("proposal terminated by suspension guard")

	// Resume: delete config.
	if err := c.Delete(ctx, &agenticv1alpha1.AgenticOLSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
	}); err != nil {
		t.Fatalf("delete config to resume: %v", err)
	}
	time.Sleep(5 * time.Second)

	// Verify: stopped proposal stays EmergencyStopped after resume.
	var updated agenticv1alpha1.Proposal
	if err := c.Get(ctx, client.ObjectKeyFromObject(prop), &updated); err != nil {
		t.Fatalf("get stopped proposal: %v", err)
	}
	phase := agenticv1alpha1.DerivePhase(updated.Status.Conditions)
	if phase != agenticv1alpha1.ProposalPhaseEmergencyStopped {
		t.Fatalf("expected EmergencyStopped after resume, got %s", phase)
	}
	t.Log("stopped proposal remains terminal after resume")
}
