package run

import (
	"bytes"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(agenticv1alpha1.AddToScheme(s))
	return s
}

func testAgenticRun(name, namespace string) *agenticv1alpha1.AgenticRun {
	return &agenticv1alpha1.AgenticRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			CreationTimestamp: metav1.Now(),
		},
		Spec: agenticv1alpha1.AgenticRunSpec{
			Request:          "Pod crashing in production",
			TargetNamespaces: []string{"production"},
			Analysis:         agenticv1alpha1.AgenticRunStep{Agent: "default"},
		},
	}
}

func testAgenticRunWithStatus(name, namespace string, phase agenticv1alpha1.AgenticRunPhase) *agenticv1alpha1.AgenticRun {
	p := testAgenticRun(name, namespace)
	p.Status = agenticv1alpha1.AgenticRunStatus{}
	setPhaseConditions(&p.Status, phase)
	return p
}

func setPhaseConditions(s *agenticv1alpha1.AgenticRunStatus, phase agenticv1alpha1.AgenticRunPhase) {
	switch phase {
	case agenticv1alpha1.AgenticRunPhaseDenied:
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{
			Type: agenticv1alpha1.AgenticRunConditionDenied, Status: metav1.ConditionTrue, Reason: "UserDenied",
		})
	case agenticv1alpha1.AgenticRunPhaseAnalyzing:
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{
			Type: agenticv1alpha1.AgenticRunConditionAnalyzed, Status: metav1.ConditionUnknown, Reason: "InProgress",
		})
	case agenticv1alpha1.AgenticRunPhaseExecuting:
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{
			Type: agenticv1alpha1.AgenticRunConditionAnalyzed, Status: metav1.ConditionTrue, Reason: "AnalysisComplete",
		})
	case agenticv1alpha1.AgenticRunPhaseVerifying:
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{
			Type: agenticv1alpha1.AgenticRunConditionAnalyzed, Status: metav1.ConditionTrue, Reason: "AnalysisComplete",
		})
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{
			Type: agenticv1alpha1.AgenticRunConditionExecuted, Status: metav1.ConditionTrue, Reason: "ExecutionComplete",
		})
	case agenticv1alpha1.AgenticRunPhaseCompleted:
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{
			Type: agenticv1alpha1.AgenticRunConditionAnalyzed, Status: metav1.ConditionTrue, Reason: "AnalysisComplete",
		})
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{
			Type: agenticv1alpha1.AgenticRunConditionExecuted, Status: metav1.ConditionTrue, Reason: "ExecutionComplete",
		})
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{
			Type: agenticv1alpha1.AgenticRunConditionVerified, Status: metav1.ConditionTrue, Reason: "VerificationPassed",
		})
	case agenticv1alpha1.AgenticRunPhaseFailed:
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{
			Type: agenticv1alpha1.AgenticRunConditionAnalyzed, Status: metav1.ConditionFalse, Reason: "AnalysisFailed",
		})
	case agenticv1alpha1.AgenticRunPhaseEscalated:
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{
			Type: agenticv1alpha1.AgenticRunConditionEscalated, Status: metav1.ConditionTrue, Reason: "MaxRetriesExhausted",
		})
	case agenticv1alpha1.AgenticRunPhaseEmergencyStopped:
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{
			Type: agenticv1alpha1.AgenticRunConditionEmergencyStopped, Status: metav1.ConditionTrue, Reason: "SystemSuspended",
		})
	case agenticv1alpha1.AgenticRunPhasePending:
		// No conditions — fresh run
	}
}

func int32Ptr(v int32) *int32 {
	return &v
}

func fakeStreams() (genericclioptions.IOStreams, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	streams := genericclioptions.IOStreams{
		In:     &bytes.Buffer{},
		Out:    out,
		ErrOut: errOut,
	}
	return streams, out, errOut
}
