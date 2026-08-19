package agenticrun

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

func TestBuildInputConfigMap(t *testing.T) {
	run := &agenticv1alpha1.AgenticRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-run",
			Namespace: "run-ns",
			UID:       types.UID("uid-aaaa-bbbb"),
		},
		Spec: agenticv1alpha1.AgenticRunSpec{
			TargetNamespaces: []string{"payments"},
		},
	}
	schema := json.RawMessage(`{"type":"object"}`)
	agentCtx := &agentContext{TargetNamespaces: []string{"payments"}}

	cm, err := buildInputConfigMap("op-ns", run, "analysis", "analyze this", schema, agentCtx)
	if err != nil {
		t.Fatalf("buildInputConfigMap: %v", err)
	}
	if cm.Name != "uid-aaaa-bbbb" {
		t.Errorf("name = %q, want run UID", cm.Name)
	}
	if cm.Namespace != "op-ns" {
		t.Errorf("namespace = %q, want op-ns", cm.Namespace)
	}
	if cm.Labels[LabelRun] != "uid-aaaa-bbbb" || cm.Labels[LabelStep] != "analysis" {
		t.Errorf("labels = %v", cm.Labels)
	}
	if len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].UID != run.UID {
		t.Errorf("ownerRefs = %+v", cm.OwnerReferences)
	}
	for _, key := range []string{inputConfigMapKeyQuery, inputConfigMapKeySchema, inputConfigMapKeyCtx, inputConfigMapKeyTmpl} {
		if _, ok := cm.Data[key]; !ok {
			t.Errorf("missing data key %q", key)
		}
	}
	if cm.Data[inputConfigMapKeyQuery] != "analyze this" {
		t.Errorf("query = %q", cm.Data[inputConfigMapKeyQuery])
	}
	if cm.Data[inputConfigMapKeySchema] != string(schema) {
		t.Errorf("schema = %q", cm.Data[inputConfigMapKeySchema])
	}

	var tmpl map[string]any
	if err := json.Unmarshal([]byte(cm.Data[inputConfigMapKeyTmpl]), &tmpl); err != nil {
		t.Fatalf("result-template JSON: %v", err)
	}
	if tmpl["kind"] != "AnalysisResult" {
		t.Errorf("kind = %v", tmpl["kind"])
	}
	if _, hasStatus := tmpl["status"]; hasStatus {
		t.Error("result-template must not include status")
	}
	meta, _ := tmpl["metadata"].(map[string]any)
	if meta["name"] != resultCRName("my-run", "analysis", 1) {
		t.Errorf("template metadata.name = %v", meta["name"])
	}
	if meta["namespace"] != "op-ns" {
		t.Errorf("template metadata.namespace = %v", meta["namespace"])
	}
}

func TestBuildResultTemplate_ExecutionRetryIndex(t *testing.T) {
	retry := int32(2)
	run := &agenticv1alpha1.AgenticRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", UID: "u"},
		Status: agenticv1alpha1.AgenticRunStatus{
			Steps: agenticv1alpha1.StepsStatus{
				Execution: agenticv1alpha1.ExecutionStepStatus{
					RetryCount: &retry,
					Results:    []agenticv1alpha1.StepResultRef{{}, {}},
				},
			},
		},
	}
	raw, err := buildResultTemplate(run, "execution", run.Namespace)
	if err != nil {
		t.Fatalf("buildResultTemplate: %v", err)
	}
	var tmpl map[string]any
	if err := json.Unmarshal([]byte(raw), &tmpl); err != nil {
		t.Fatal(err)
	}
	if tmpl["kind"] != "ExecutionResult" {
		t.Errorf("kind = %v", tmpl["kind"])
	}
	spec, _ := tmpl["spec"].(map[string]any)
	if spec["retryIndex"] != float64(2) {
		t.Errorf("retryIndex = %v, want 2", spec["retryIndex"])
	}
	meta, _ := tmpl["metadata"].(map[string]any)
	if meta["name"] != resultCRName("r", "execution", 3) {
		t.Errorf("name = %v, want index 3", meta["name"])
	}
}

func TestBuildResultTemplate_UnknownStep(t *testing.T) {
	run := &agenticv1alpha1.AgenticRun{ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", UID: "u"}}
	_, err := buildResultTemplate(run, "nope", run.Namespace)
	if err == nil {
		t.Fatal("expected error for unknown step")
	}
}
