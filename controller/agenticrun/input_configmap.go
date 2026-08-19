package agenticrun

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

const (
	ErrBuildInputConfigMap   = "build input ConfigMap"
	ErrBuildResultTemplate   = "build result template"
	ErrMarshalInputContext   = "marshal input context"
	ErrMarshalResultTemplate = "marshal result template"
	ErrUnknownStep           = "unknown step"
)

// buildInputConfigMap builds the batch input ConfigMap for a step (rule 7).
// Name is the AgenticRun UID. Caller creates it in the operator namespace.
func buildInputConfigMap(
	operatorNamespace string,
	run *agenticv1alpha1.AgenticRun,
	step string,
	query string,
	schema json.RawMessage,
	agentCtx *agentContext,
) (*corev1.ConfigMap, error) {
	if agentCtx == nil {
		agentCtx = &agentContext{}
	}
	ctxJSON, err := json.Marshal(agentCtx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ErrMarshalInputContext, err)
	}
	tmpl, err := buildResultTemplate(run, step, operatorNamespace)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ErrBuildInputConfigMap, err)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      string(run.UID),
			Namespace: operatorNamespace,
			Labels: map[string]string{
				LabelRun:  string(run.UID),
				LabelStep: step,
			},
			OwnerReferences: []metav1.OwnerReference{agenticRunOwnerRef(run)},
		},
		Data: map[string]string{
			inputConfigMapKeyQuery:  query,
			inputConfigMapKeySchema: string(schema),
			inputConfigMapKeyCtx:    string(ctxJSON),
			inputConfigMapKeyTmpl:   tmpl,
		},
	}, nil
}

// buildResultTemplate returns JSON for result-template (rule 7a): apiVersion,
// kind, metadata, and spec only — sandbox fills status.
func buildResultTemplate(run *agenticv1alpha1.AgenticRun, step, namespace string) (string, error) {
	index := nextResultIndex(run, step)
	name := resultCRName(run.Name, step, index)
	apiVersion := agenticv1alpha1.GroupVersion.String()
	ownerRef := agenticRunOwnerRef(run)

	var obj any
	switch step {
	case "analysis":
		obj = &agenticv1alpha1.AnalysisResult{
			TypeMeta: metav1.TypeMeta{APIVersion: apiVersion, Kind: "AnalysisResult"},
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				Namespace:       namespace,
				Labels:          resultLabels(string(run.UID), step),
				OwnerReferences: []metav1.OwnerReference{ownerRef},
			},
			Spec: agenticv1alpha1.AnalysisResultSpec{AgenticRunName: run.Name},
		}
	case "execution":
		obj = &agenticv1alpha1.ExecutionResult{
			TypeMeta: metav1.TypeMeta{APIVersion: apiVersion, Kind: "ExecutionResult"},
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				Namespace:       namespace,
				Labels:          resultLabels(string(run.UID), step),
				OwnerReferences: []metav1.OwnerReference{ownerRef},
			},
			Spec: agenticv1alpha1.ExecutionResultSpec{
				AgenticRunName: run.Name,
				RetryIndex:     ptr.To(executionRetryIndex(run)),
			},
		}
	case "verification":
		obj = &agenticv1alpha1.VerificationResult{
			TypeMeta: metav1.TypeMeta{APIVersion: apiVersion, Kind: "VerificationResult"},
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				Namespace:       namespace,
				Labels:          resultLabels(string(run.UID), step),
				OwnerReferences: []metav1.OwnerReference{ownerRef},
			},
			Spec: agenticv1alpha1.VerificationResultSpec{
				AgenticRunName: run.Name,
				RetryIndex:     ptr.To(executionRetryIndex(run)),
			},
		}
	case "escalation":
		obj = &agenticv1alpha1.EscalationResult{
			TypeMeta: metav1.TypeMeta{APIVersion: apiVersion, Kind: "EscalationResult"},
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				Namespace:       namespace,
				Labels:          resultLabels(string(run.UID), step),
				OwnerReferences: []metav1.OwnerReference{ownerRef},
			},
			Spec: agenticv1alpha1.EscalationResultSpec{AgenticRunName: run.Name},
		}
	default:
		return "", fmt.Errorf("%s: %s %q", ErrBuildResultTemplate, ErrUnknownStep, step)
	}

	raw, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ErrMarshalResultTemplate, err)
	}
	return string(raw), nil
}

func nextResultIndex(run *agenticv1alpha1.AgenticRun, step string) int {
	switch step {
	case "analysis":
		return len(run.Status.Steps.Analysis.Results) + 1
	case "execution":
		return len(run.Status.Steps.Execution.Results) + 1
	case "verification":
		return len(run.Status.Steps.Verification.Results) + 1
	case "escalation":
		return len(run.Status.Steps.Escalation.Results) + 1
	default:
		return 1
	}
}
