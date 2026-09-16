package run

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agenticv1alpha1 "github.com/openshift/lightspeed-agentic-operator/api/v1alpha1"
)

func TestLogs_Validate(t *testing.T) {
	tests := []struct {
		name    string
		step    string
		wantErr bool
	}{
		{"empty step", "", false},
		{"Analysis", "Analysis", false},
		{"Execution", "Execution", false},
		{"Verification", "Verification", false},
		{"lowercase", "analysis", false},
		{"uppercase", "ANALYSIS", false},
		{"invalid", "invalid", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &LogsOptions{step: tc.step}
			err := o.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestLogs_ValidateStoredFollowError(t *testing.T) {
	o := &LogsOptions{stored: true, follow: true}
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "--follow") {
		t.Fatalf("Validate() error = %v, want --follow error", err)
	}
}

func TestLogs_ValidateAdminEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint string
		wantErr  bool
	}{
		{name: "https", endpoint: "https://collector.example.com", wantErr: false},
		{name: "loopback http", endpoint: "http://127.0.0.1:18080", wantErr: false},
		{name: "external http", endpoint: "http://collector.example.com", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateAdminEndpoint(tc.endpoint); (err != nil) != tc.wantErr {
				t.Errorf("validateAdminEndpoint() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestLogs_AdminHTTPClientCanSkipVerification(t *testing.T) {
	client := newAdminHTTPClient(true)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify { //nolint:gosec // test verifies the explicit opt-in
		t.Fatal("expected TLS certificate verification to be disabled")
	}
}

func TestLogs_StoredPhase(t *testing.T) {
	if got := storedPhase(agenticv1alpha1.SandboxStepAnalysis); got != "analysis" {
		t.Errorf("storedPhase() = %q, want analysis", got)
	}
	if got := storedLogsPhase(""); got != "" {
		t.Errorf("storedLogsPhase(\"\") = %q, want empty phase", got)
	}
	if got := storedLogsPhase("execution"); got != "execution" {
		t.Errorf("storedLogsPhase(\"execution\") = %q, want execution", got)
	}
}

func TestLogs_ServiceProxyPath(t *testing.T) {
	got := serviceProxyPath("openshift-lightspeed", "lightspeed-otel-collector", "8080")
	want := "api/v1/namespaces/openshift-lightspeed/services/https:lightspeed-otel-collector:8080/proxy/api/v1/logs"
	if got != want {
		t.Errorf("serviceProxyPath() = %q, want %q", got, want)
	}
}

func TestLogs_FetchStoredLogs(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("agentic_run_id"); got != "run-uid" {
			t.Errorf("agentic_run_id = %q, want run-uid", got)
		}
		if got := r.URL.Query().Get("phase"); got != "execution" {
			t.Errorf("phase = %q, want execution", got)
		}
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Errorf("format = %q, want json", got)
		}
		if _, err := w.Write([]byte(`{"agentic_run_id":"run-uid","phase":"execution","records":[{"id":1,"timestamp":"2026-09-16T07:29:21Z","body":"stored execution log"}],"has_more":false}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	var out strings.Builder
	o := &LogsOptions{adminEndpoint: server.URL, httpClient: server.Client()}
	o.IOStreams.Out = &out
	if err := o.fetchStoredLogs(context.Background(), "run-uid", "execution"); err != nil {
		t.Fatalf("fetchStoredLogs() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "stored execution log") || !strings.Contains(got, "records: 1") {
		t.Errorf("output = %q, want formatted stored log", got)
	}
}

func TestLogs_FetchStoredLogs_AllPhases(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["phase"]; ok {
			t.Errorf("phase query parameter = %q, want it omitted", r.URL.Query().Get("phase"))
		}
		if _, err := w.Write([]byte(`{"agentic_run_id":"run-uid","records":[{"id":1,"timestamp":"2026-09-16T07:29:21Z","body":"all phases"}],"has_more":false}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	var out strings.Builder
	o := &LogsOptions{adminEndpoint: server.URL, httpClient: server.Client()}
	o.IOStreams.Out = &out
	if err := o.fetchStoredLogs(context.Background(), "run-uid", ""); err != nil {
		t.Fatalf("fetchStoredLogs() error = %v", err)
	}
	if !strings.Contains(out.String(), "all phases") {
		t.Errorf("output = %q, want all-phase record", out.String())
	}
}

func TestLogs_FetchStoredLogs_Paginates(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.URL.Query().Get("limit"); got != "1000" {
			t.Errorf("limit = %q, want 1000", got)
		}
		var page string
		switch r.URL.Query().Get("after") {
		case "":
			page = `{"agentic_run_id":"run-uid","records":[{"id":10,"timestamp":"2026-09-16T07:29:21Z","body":"first"}],"has_more":true}`
		case "10":
			page = `{"agentic_run_id":"run-uid","records":[{"id":20,"timestamp":"2026-09-16T07:29:22Z","body":"second"}],"has_more":false}`
		default:
			t.Errorf("unexpected after=%q", r.URL.Query().Get("after"))
		}
		if _, err := w.Write([]byte(page)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	var out strings.Builder
	o := &LogsOptions{adminEndpoint: server.URL, httpClient: server.Client()}
	o.IOStreams.Out = &out
	if err := o.fetchStoredLogs(context.Background(), "run-uid", "execution"); err != nil {
		t.Fatalf("fetchStoredLogs() error = %v", err)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
	if !strings.Contains(out.String(), "first") || !strings.Contains(out.String(), "second") {
		t.Errorf("output = %q, want both pages", out.String())
	}
}

func TestLogs_ValidateErrorMessage(t *testing.T) {
	o := &LogsOptions{step: "badvalue"}
	err := o.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, expected := range []string{"Analysis", "Execution", "Verification"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("error should list valid steps, missing %q: %v", expected, err)
		}
	}
}

func TestLogs_ResolveSandbox_ExplicitStep(t *testing.T) {
	p := testAgenticRunWithStatus("test", "default", agenticv1alpha1.AgenticRunPhaseExecuting)
	p.Status.Steps.Analysis.Sandbox = agenticv1alpha1.SandboxInfo{ClaimName: "analysis-pod"}
	p.Status.Steps.Execution.Sandbox = agenticv1alpha1.SandboxInfo{ClaimName: "exec-pod"}
	p.Status.Steps.Verification.Sandbox = agenticv1alpha1.SandboxInfo{ClaimName: "verify-pod"}

	tests := []struct {
		step string
		want string
	}{
		{"Analysis", "analysis-pod"},
		{"Execution", "exec-pod"},
		{"Verification", "verify-pod"},
		{"analysis", "analysis-pod"},
	}
	for _, tc := range tests {
		t.Run(tc.step, func(t *testing.T) {
			o := &LogsOptions{step: tc.step}
			sandbox := o.resolveSandbox(p)
			if sandbox == nil {
				t.Fatal("expected non-nil sandbox")
			}
			if sandbox.ClaimName != tc.want {
				t.Errorf("expected claim %q, got %q", tc.want, sandbox.ClaimName)
			}
		})
	}
}

func TestLogs_ResolveSandbox_AutoDetect(t *testing.T) {
	tests := []struct {
		name      string
		analysis  string
		execution string
		verify    string
		wantClaim string
		wantNil   bool
	}{
		{"prefer verification", "a-pod", "e-pod", "v-pod", "v-pod", false},
		{"fallback to execution", "a-pod", "e-pod", "", "e-pod", false},
		{"fallback to analysis", "a-pod", "", "", "a-pod", false},
		{"no sandbox", "", "", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := testAgenticRunWithStatus("test", "default", agenticv1alpha1.AgenticRunPhaseVerifying)
			p.Status.Steps.Analysis.Sandbox = agenticv1alpha1.SandboxInfo{ClaimName: tc.analysis}
			p.Status.Steps.Execution.Sandbox = agenticv1alpha1.SandboxInfo{ClaimName: tc.execution}
			p.Status.Steps.Verification.Sandbox = agenticv1alpha1.SandboxInfo{ClaimName: tc.verify}

			o := &LogsOptions{}
			sandbox := o.resolveSandbox(p)
			if tc.wantNil {
				if sandbox != nil {
					t.Errorf("expected nil sandbox, got %+v", sandbox)
				}
				return
			}
			if sandbox == nil {
				t.Fatal("expected non-nil sandbox")
			}
			if sandbox.ClaimName != tc.wantClaim {
				t.Errorf("expected claim %q, got %q", tc.wantClaim, sandbox.ClaimName)
			}
		})
	}
}
