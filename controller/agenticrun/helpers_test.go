package agenticrun

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

func ptr32(v int32) *int32 { return &v }

func TestIsSuspended(t *testing.T) {
	tests := []struct {
		name    string
		objects []client.Object
		want    bool
		wantErr bool
	}{
		{
			name: "suspended=true returns true",
			objects: []client.Object{&agenticv1alpha1.AgenticOLSConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec:       agenticv1alpha1.AgenticOLSConfigSpec{Suspended: true},
			}},
			want: true,
		},
		{
			name: "suspended=false returns false",
			objects: []client.Object{&agenticv1alpha1.AgenticOLSConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec:       agenticv1alpha1.AgenticOLSConfigSpec{Suspended: false},
			}},
			want: false,
		},
		{
			name:    "config not found returns false",
			objects: nil,
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := tt.objects
			if objects == nil {
				objects = []client.Object{}
			}
			fc := fake.NewClientBuilder().
				WithScheme(testScheme()).
				WithObjects(objects...).
				Build()
			got, err := isSuspended(context.Background(), fc)
			if (err != nil) != tt.wantErr {
				t.Fatalf("isSuspended() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("isSuspended() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSelectedOption_ReturnsFirstOption(t *testing.T) {
	scheme := testScheme()

	run := &agenticv1alpha1.AgenticRun{}
	run.Name = "test"
	run.Namespace = "default"
	run.Status.Steps.Analysis.Results = []agenticv1alpha1.StepResultRef{
		{Name: "test-analysis-1", Outcome: agenticv1alpha1.ActionOutcomeSucceeded},
	}

	analysisResult := &agenticv1alpha1.AnalysisResult{}
	analysisResult.Name = "test-analysis-1"
	analysisResult.Namespace = "default"
	analysisResult.Status.Options = []agenticv1alpha1.RemediationOption{
		{Title: "A"},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(analysisResult).Build()
	r := &AgenticRunReconciler{Client: fc, Namespace: "default"}

	got, err := r.selectedOption(context.Background(), run)
	if err != nil {
		t.Fatalf("selectedOption() error: %v", err)
	}
	if got == nil {
		t.Fatal("selectedOption() returned nil")
	}
	if got.Title != "A" {
		t.Errorf("selectedOption().Title = %q, want %q", got.Title, "A")
	}
}

func TestSelectedOption_NoResults(t *testing.T) {
	scheme := testScheme()

	run := &agenticv1alpha1.AgenticRun{}
	run.Name = "test"
	run.Namespace = "default"

	fc := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgenticRunReconciler{Client: fc, Namespace: "default"}

	got, err := r.selectedOption(context.Background(), run)
	if err != nil {
		t.Fatalf("selectedOption() error: %v", err)
	}
	if got != nil {
		t.Errorf("selectedOption() should return nil when no results, got %+v", got)
	}
}

func TestTrimNonSelectedOptions_SingleOptionNoop(t *testing.T) {
	scheme := testScheme()
	analysisResult := &agenticv1alpha1.AnalysisResult{}
	analysisResult.Name = "test-analysis-1"
	analysisResult.Namespace = "default"
	analysisResult.Status.Options = []agenticv1alpha1.RemediationOption{
		{Title: "Only"},
	}

	run := &agenticv1alpha1.AgenticRun{}
	run.Name = "test"
	run.Namespace = "default"
	run.Status.Steps.Analysis.Results = []agenticv1alpha1.StepResultRef{
		{Name: "test-analysis-1", Outcome: agenticv1alpha1.ActionOutcomeSucceeded},
	}

	approval := &agenticv1alpha1.AgenticRunApproval{
		Spec: agenticv1alpha1.AgenticRunApprovalSpec{
			Stages: []agenticv1alpha1.ApprovalStage{
				{Type: agenticv1alpha1.ApprovalStageExecution, Execution: &agenticv1alpha1.ExecutionApproval{Option: ptr32(0)}},
			},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(analysisResult).WithStatusSubresource(analysisResult).Build()
	r := &AgenticRunReconciler{Client: fc, Namespace: "default"}

	got, err := r.trimNonSelectedOptions(context.Background(), run, approval, nil)
	if err != nil {
		t.Fatalf("trimNonSelectedOptions() error: %v", err)
	}
	if got == nil || got.Title != "Only" {
		t.Errorf("single option should be returned unchanged")
	}
}

func TestTrimThenSelectedOption_EndToEnd(t *testing.T) {
	scheme := testScheme()

	tests := []struct {
		name      string
		options   []agenticv1alpha1.RemediationOption
		selectIdx int32
		wantTitle string
	}{
		{"select first of 3", []agenticv1alpha1.RemediationOption{{Title: "A"}, {Title: "B"}, {Title: "C"}}, 0, "A"},
		{"select middle of 3", []agenticv1alpha1.RemediationOption{{Title: "A"}, {Title: "B"}, {Title: "C"}}, 1, "B"},
		{"select last of 3", []agenticv1alpha1.RemediationOption{{Title: "A"}, {Title: "B"}, {Title: "C"}}, 2, "C"},
		{"select second of 2", []agenticv1alpha1.RemediationOption{{Title: "X"}, {Title: "Y"}}, 1, "Y"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analysisResult := &agenticv1alpha1.AnalysisResult{}
			analysisResult.Name = "test-analysis-1"
			analysisResult.Namespace = "default"
			analysisResult.Status.Options = tt.options

			run := &agenticv1alpha1.AgenticRun{}
			run.Name = "test"
			run.Namespace = "default"
			run.Status.Steps.Analysis.Results = []agenticv1alpha1.StepResultRef{
				{Name: "test-analysis-1", Outcome: agenticv1alpha1.ActionOutcomeSucceeded},
			}

			approval := &agenticv1alpha1.AgenticRunApproval{
				Spec: agenticv1alpha1.AgenticRunApprovalSpec{
					Stages: []agenticv1alpha1.ApprovalStage{
						{Type: agenticv1alpha1.ApprovalStageExecution, Execution: &agenticv1alpha1.ExecutionApproval{Option: &tt.selectIdx}},
					},
				},
			}

			fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(analysisResult).WithStatusSubresource(analysisResult).Build()
			r := &AgenticRunReconciler{Client: fc, Namespace: "default"}

			got, err := r.trimNonSelectedOptions(context.Background(), run, approval, nil)
			if err != nil {
				t.Fatalf("trim error: %v", err)
			}
			if got == nil {
				t.Fatal("trimNonSelectedOptions() returned nil")
			}
			if got.Title != tt.wantTitle {
				t.Errorf("selectedOption().Title = %q, want %q", got.Title, tt.wantTitle)
			}
		})
	}
}

func TestTrimNonSelectedOptions_OutOfRange(t *testing.T) {
	scheme := testScheme()

	analysisResult := &agenticv1alpha1.AnalysisResult{}
	analysisResult.Name = "test-analysis-1"
	analysisResult.Namespace = "default"
	analysisResult.Status.Options = []agenticv1alpha1.RemediationOption{
		{Title: "A"}, {Title: "B"},
	}

	run := &agenticv1alpha1.AgenticRun{}
	run.Name = "test"
	run.Namespace = "default"
	run.Status.Steps.Analysis.Results = []agenticv1alpha1.StepResultRef{
		{Name: "test-analysis-1", Outcome: agenticv1alpha1.ActionOutcomeSucceeded},
	}

	approval := &agenticv1alpha1.AgenticRunApproval{
		Spec: agenticv1alpha1.AgenticRunApprovalSpec{
			Stages: []agenticv1alpha1.ApprovalStage{
				{Type: agenticv1alpha1.ApprovalStageExecution, Execution: &agenticv1alpha1.ExecutionApproval{Option: ptr32(5)}},
			},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(analysisResult).WithStatusSubresource(analysisResult).Build()
	r := &AgenticRunReconciler{Client: fc, Namespace: "default"}

	_, err := r.trimNonSelectedOptions(context.Background(), run, approval, nil)
	if err == nil {
		t.Fatal("expected error for out-of-range option index")
	}
}
