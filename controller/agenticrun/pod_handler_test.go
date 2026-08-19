package agenticrun

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestPodFailMessage(t *testing.T) {
	exit := int32(1)

	tests := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{
			name: "succeeded without result",
			pod:  &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
			want: msgSandboxNoResult,
		},
		{
			name: "failed with termination message",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
				ContainerStatuses: []corev1.ContainerStatus{{
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						Message: "OOMKilled",
					}},
				}},
			}},
			want: "OOMKilled",
		},
		{
			name: "failed with exit code",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase: corev1.PodFailed,
				ContainerStatuses: []corev1.ContainerStatus{{
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: exit,
					}},
				}},
			}},
			want: "sandbox pod failed (exit 1)",
		},
		{
			name: "failed without details",
			pod:  &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed}},
			want: "sandbox pod failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := podFailMessage(tt.pod)
			if got != tt.want {
				t.Errorf("podFailMessage = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStartTimedOut(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		phase   corev1.PodPhase
		created time.Time
		timeout time.Duration
		want    bool
	}{
		{"pending past deadline", corev1.PodPending, now.Add(-6 * time.Minute), 5 * time.Minute, true},
		{"pending within deadline", corev1.PodPending, now.Add(-2 * time.Minute), 5 * time.Minute, false},
		{"unknown past deadline", corev1.PodUnknown, now.Add(-6 * time.Minute), 5 * time.Minute, true},
		{"running ignores start timeout", corev1.PodRunning, now.Add(-6 * time.Minute), 5 * time.Minute, false},
		{"succeeded ignores start timeout", corev1.PodSucceeded, now.Add(-6 * time.Minute), 5 * time.Minute, false},
		{"failed ignores start timeout", corev1.PodFailed, now.Add(-6 * time.Minute), 5 * time.Minute, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := startTimedOut(tt.phase, tt.created, now, tt.timeout)
			if got != tt.want {
				t.Errorf("startTimedOut = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOverallTimedOut(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		created time.Time
		timeout time.Duration
		want    bool
	}{
		{"past deadline", now.Add(-15 * time.Minute), 10 * time.Minute, true},
		{"within deadline", now.Add(-5 * time.Minute), 10 * time.Minute, false},
		{"exactly at deadline", now.Add(-10 * time.Minute), 10 * time.Minute, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := overallTimedOut(tt.created, now, tt.timeout)
			if got != tt.want {
				t.Errorf("overallTimedOut = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodTerminatedInfo(t *testing.T) {
	msg, code := podTerminatedInfo(nil)
	if msg != "" || code != nil {
		t.Fatalf("nil pod: %q %v", msg, code)
	}
	pod := &corev1.Pod{Status: corev1.PodStatus{
		ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 2,
				Message:  "boom",
			}},
		}},
	}}
	msg, code = podTerminatedInfo(pod)
	if msg != "boom" || code == nil || *code != 2 {
		t.Fatalf("got msg=%q code=%v", msg, code)
	}
}
