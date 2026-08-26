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
	if cm.Name != "ls-analysis-uid-aaaa-bbbb" {
		t.Errorf("name = %q, want ls-analysis-uid-aaaa-bbbb", cm.Name)
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

func TestBuildResultTemplate_UnknownStep(t *testing.T) {
	run := &agenticv1alpha1.AgenticRun{ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", UID: "u"}}
	_, err := buildResultTemplate(run, "nope", run.Namespace)
	if err == nil {
		t.Fatal("expected error for unknown step")
	}
}
