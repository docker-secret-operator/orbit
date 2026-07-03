package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/api"
	"github.com/docker-secret-operator/orbit/internal/cli/clierr"
	"github.com/docker-secret-operator/orbit/internal/cli/output"
	"github.com/spf13/cobra"
)

// newFakeRecoverServer answers GET /status with statusBody and POST /recover
// with (recoverStatus, recoverBody) — a real HTTP server exercising runRecover
// end to end, matching this package's established no-mocked-behavior style.
func newFakeRecoverServer(t *testing.T, statusBody string, recoverStatus int, recoverBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/status" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(statusBody))
		case r.URL.Path == "/recover" && r.Method == http.MethodPost:
			w.WriteHeader(recoverStatus)
			_, _ = w.Write([]byte(recoverBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ── runRecover: realistic end-to-end flows (non-exiting paths only — see
// exitcode_test.go for the os.Exit paths, matching deploy_test.go's convention) ──

func TestRunRecover_Success_NotInterrupted(t *testing.T) {
	srv := newFakeRecoverServer(t,
		`{"service":"web","deployment_state":"completed","proxy_status":"ready"}`,
		http.StatusOK,
		`{"epoch":3,"action":"restore_single","authoritative_generation":"gen-2","backends_restored":1,"proxy_status":"ready"}`,
	)

	var buf bytes.Buffer
	p := output.New(&buf, true)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	if err := runRecover(cmd, p, srv.URL, "", "", 5*time.Second, nil); err != nil {
		t.Fatalf("runRecover: %v", err)
	}

	var summary RecoverSummary
	if err := json.Unmarshal(buf.Bytes(), &summary); err != nil {
		t.Fatalf("decode summary JSON: %v\noutput: %s", err, buf.String())
	}
	if summary.InterruptedDeploy {
		t.Error("InterruptedDeploy = true, want false for a completed deployment")
	}
	if summary.Outcome.Action != "restore_single" {
		t.Errorf("Outcome.Action = %q, want restore_single", summary.Outcome.Action)
	}
	if summary.Outcome.AuthoritativeGeneration != "gen-2" {
		t.Errorf("AuthoritativeGeneration = %q, want gen-2", summary.Outcome.AuthoritativeGeneration)
	}
}

func TestRunRecover_DetectsInterruptedDeployment(t *testing.T) {
	srv := newFakeRecoverServer(t,
		`{"service":"web","deployment_state":"registering","proxy_status":"degraded"}`,
		http.StatusOK,
		`{"epoch":4,"action":"restore_with_draining","authoritative_generation":"gen-3","backends_restored":1,"proxy_status":"ready"}`,
	)

	var buf bytes.Buffer
	p := output.New(&buf, true)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	if err := runRecover(cmd, p, srv.URL, "", "", 5*time.Second, nil); err != nil {
		t.Fatalf("runRecover: %v", err)
	}

	var summary RecoverSummary
	if err := json.Unmarshal(buf.Bytes(), &summary); err != nil {
		t.Fatalf("decode summary JSON: %v", err)
	}
	if !summary.InterruptedDeploy {
		t.Error("InterruptedDeploy = false, want true — deployment_state was 'registering', neither idle nor completed")
	}
	if summary.BeforeState != "registering" {
		t.Errorf("BeforeState = %q, want registering", summary.BeforeState)
	}
}

func TestRunRecover_IdleStateIsNotInterrupted(t *testing.T) {
	srv := newFakeRecoverServer(t,
		`{"service":"web","deployment_state":"idle","proxy_status":"ready"}`,
		http.StatusOK,
		`{"epoch":1,"action":"restore_single","authoritative_generation":"gen-1","backends_restored":1,"proxy_status":"ready"}`,
	)

	var buf bytes.Buffer
	p := output.New(&buf, true)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	if err := runRecover(cmd, p, srv.URL, "", "", 5*time.Second, nil); err != nil {
		t.Fatalf("runRecover: %v", err)
	}
	var summary RecoverSummary
	_ = json.Unmarshal(buf.Bytes(), &summary)
	if summary.InterruptedDeploy {
		t.Error("InterruptedDeploy = true for idle state, want false")
	}
}

func TestRunRecover_AfterStateReflectsPostRecoveryStatus(t *testing.T) {
	// fetchStatus is called twice (before + after); the fake server always
	// returns the same body, so summary.AfterState should be populated from
	// the second call regardless.
	srv := newFakeRecoverServer(t,
		`{"service":"web","deployment_state":"idle","proxy_status":"ready","healthy_backends":[{"id":"b1","addr":"10.0.0.1:3000"}],"unhealthy_backends":[]}`,
		http.StatusOK,
		`{"epoch":1,"action":"restore_single","authoritative_generation":"gen-1","backends_restored":1,"proxy_status":"ready"}`,
	)

	var buf bytes.Buffer
	p := output.New(&buf, true)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	if err := runRecover(cmd, p, srv.URL, "", "", 5*time.Second, nil); err != nil {
		t.Fatalf("runRecover: %v", err)
	}
	var summary RecoverSummary
	_ = json.Unmarshal(buf.Bytes(), &summary)
	if summary.AfterState != "ready" {
		t.Errorf("AfterState = %q, want ready", summary.AfterState)
	}
	if summary.HealthyBackends != 1 {
		t.Errorf("HealthyBackends = %d, want 1", summary.HealthyBackends)
	}
}

// ── postRecover: exact HTTP status code → clierr mapping ────────────────────

func TestPostRecover_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"epoch":7,"action":"restore_single","authoritative_generation":"gen-5","backends_restored":2,"proxy_status":"ready"}`))
	}))
	defer srv.Close()

	outcome, err := postRecover(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("postRecover: %v", err)
	}
	if outcome.Epoch != 7 || outcome.AuthoritativeGeneration != "gen-5" || outcome.BackendsRestored != 2 {
		t.Errorf("outcome = %+v, unexpected fields", outcome)
	}
}

func TestPostRecover_StatusCodeMapping(t *testing.T) {
	cases := []struct {
		name       string
		httpStatus int
		body       string
		wantExit   int
		wantSubstr string
	}{
		{"conflict in-flight", http.StatusConflict, "", output.ExitError, "already in progress"},
		{"unauthorized", http.StatusUnauthorized, "", output.ExitConfig, "authentication failed"},
		{"forbidden", http.StatusForbidden, "", output.ExitConfig, "authentication failed"},
		{"not wired", http.StatusServiceUnavailable, "", output.ExitUnavailable, "does not support on-demand recovery"},
		{"unexpected 500", http.StatusInternalServerError, "boom", output.ExitError, "unexpected status 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.httpStatus)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			_, err := postRecover(context.Background(), srv.URL, "")
			ce, ok := err.(*clierr.Error)
			if !ok {
				t.Fatalf("expected *clierr.Error, got %T: %v", err, err)
			}
			if ce.ExitCode != tc.wantExit {
				t.Errorf("ExitCode = %d, want %d", ce.ExitCode, tc.wantExit)
			}
			if !strings.Contains(ce.Error(), tc.wantSubstr) {
				t.Errorf("error = %q, want substring %q", ce.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestPostRecover_UnreachableProxy(t *testing.T) {
	_, err := postRecover(context.Background(), "http://127.0.0.1:1", "")
	ce, ok := err.(*clierr.Error)
	if !ok {
		t.Fatalf("expected *clierr.Error, got %T: %v", err, err)
	}
	if ce.ExitCode != output.ExitUnavailable {
		t.Errorf("ExitCode = %d, want ExitUnavailable", ce.ExitCode)
	}
}

func TestPostRecover_UnauthorizedIncludesAPIToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"epoch":1,"action":"restore_single","authoritative_generation":"g","backends_restored":0,"proxy_status":"ready"}`))
	}))
	defer srv.Close()

	if _, err := postRecover(context.Background(), srv.URL, "secret-token"); err != nil {
		t.Fatalf("postRecover: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer secret-token")
	}
}

// ── renderRecoverSummaryHuman: distinguishes degraded / inferred / success ──

func TestRenderRecoverSummaryHuman_Degraded_NeverGuesses(t *testing.T) {
	var buf bytes.Buffer
	renderRecoverSummaryHuman(&buf, RecoverSummary{
		Service: "web",
		Outcome: api.RecoveryOutcome{
			Action:       "degraded",
			FailedReason: "no persisted authority and no healthy generation to infer from",
		},
	})
	out := buf.String()
	for _, want := range []string{"could not establish an authoritative generation", "no persisted authority", "stopped rather than", "pick one arbitrarily"} {
		if !strings.Contains(out, want) {
			t.Errorf("degraded output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderRecoverSummaryHuman_InferredFallback_ShowsWarning(t *testing.T) {
	var buf bytes.Buffer
	renderRecoverSummaryHuman(&buf, RecoverSummary{
		Service: "web",
		Outcome: api.RecoveryOutcome{
			Action:                  "inferred_fallback",
			AuthoritativeGeneration: "gen-2",
			Reason:                  "only one healthy generation found",
			BackendsRestored:        1,
			ProxyStatus:             "ready",
		},
	})
	out := buf.String()
	for _, want := range []string{"had no persisted authority to confirm against", "gen-2", "only one healthy generation found"} {
		if !strings.Contains(out, want) {
			t.Errorf("inferred_fallback output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderRecoverSummaryHuman_Success(t *testing.T) {
	var buf bytes.Buffer
	renderRecoverSummaryHuman(&buf, RecoverSummary{
		Service: "web",
		Outcome: api.RecoveryOutcome{
			Action:                  "restore_single",
			AuthoritativeGeneration: "gen-4",
			BackendsRestored:        2,
			ProxyStatus:             "ready",
		},
		AfterState:      "ready",
		HealthyBackends: 2,
	})
	out := buf.String()
	for _, want := range []string{"Recovery complete", "gen-4", "2"} {
		if !strings.Contains(out, want) {
			t.Errorf("success output missing %q:\n%s", want, out)
		}
	}
}
