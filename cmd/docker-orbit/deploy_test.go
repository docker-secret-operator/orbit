package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker-secret-operator/orbit/internal/cli/output"
	"github.com/docker-secret-operator/orbit/internal/rollout"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// ── deployPreflightPassed ────────────────────────────────────────────────────

func TestDeployPreflightPassed_AllPass(t *testing.T) {
	report := DoctorReport{Checks: []Check{
		{Name: "Docker Engine reachable", Status: StatusPass},
		{Name: "Proxy reachable", Status: StatusPass},
	}}
	if !deployPreflightPassed(report) {
		t.Error("expected preflight to pass when all checks pass")
	}
}

func TestDeployPreflightPassed_AnyErrorBlocks(t *testing.T) {
	report := DoctorReport{Checks: []Check{
		{Name: "Docker Engine reachable", Status: StatusFail},
	}}
	if deployPreflightPassed(report) {
		t.Error("expected preflight to fail when any check is ERROR")
	}
}

func TestDeployPreflightPassed_ProxyWarningBlocksDeploySpecifically(t *testing.T) {
	// Unlike a general `doctor` run (where an unreachable proxy is only a
	// WARNING, since it's expected before the first deploy), `deploy` itself
	// cannot proceed without a live proxy target — this is the one place a
	// WARNING should still block.
	for _, name := range []string{"Proxy reachable", "Proxy healthy"} {
		report := DoctorReport{Checks: []Check{{Name: name, Status: StatusWarn}}}
		if deployPreflightPassed(report) {
			t.Errorf("%s WARNING should block deploy preflight, but didn't", name)
		}
	}
}

func TestDeployPreflightPassed_OtherWarningsDoNotBlock(t *testing.T) {
	report := DoctorReport{Checks: []Check{
		{Name: "Plugin installation", Status: StatusWarn},
		{Name: "Recovery state consistent", Status: StatusWarn},
	}}
	if !deployPreflightPassed(report) {
		t.Error("non-proxy WARNINGs should not block deploy preflight")
	}
}

// ── confirmPrompt ─────────────────────────────────────────────────────────────

func TestConfirmPrompt(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"Y\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false},
		{"", false}, // EOF
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		got, err := confirmPrompt(strings.NewReader(tc.input), &buf, "Proceed?")
		if err != nil && tc.input != "" {
			t.Errorf("input %q: unexpected error %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("input %q: confirmPrompt = %v, want %v", tc.input, got, tc.want)
		}
		if !strings.Contains(buf.String(), "Proceed?") {
			t.Errorf("input %q: prompt not written to output", tc.input)
		}
	}
}

// ── runDeploy: dry-run, realistic flow against a fake compose file + status server ──

func writeTestComposeFile(t *testing.T, service string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-rollout-compose.yml")
	content := "services:\n  " + service + ":\n    image: myapp:1.0\n    ports:\n      - \"3000:3000\"\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// newFakeStatusServer returns an httptest server answering GET /status with
// a fixed body — realistic enough to drive runDeploy's plan-building logic
// without a real proxy.
func newFakeStatusServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body)) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRunDeploy_DryRun_ServiceNotInComposeFile and other error-path
// scenarios that go through renderCLIErr (which calls os.Exit) are covered
// by the subprocess-based tests in exitcode_test.go instead of an in-process
// call here — renderCLIErr's os.Exit would otherwise terminate the entire
// `go test` process, not just fail the assertion. This is the same
// constraint status_test.go/doctor_test.go already work within.

func TestRunDeploy_DryRun_ShowsPlanFromLiveStatus(t *testing.T) {
	composeFile := writeTestComposeFile(t, "web")
	statusSrv := newFakeStatusServer(t, `{
		"service": "web",
		"current_generation": "gen-1",
		"proxy_status": "ready",
		"healthy_backends": [{"id":"web-abc","addr":"10.0.0.1:3000","draining":false}],
		"unhealthy_backends": [],
		"active_traffic_target": ["10.0.0.1:3000"],
		"recovery": {"degraded": false, "recovery_count": 1, "recovery_failure_count": 0, "authority_transitions": 1}
	}`)

	var buf bytes.Buffer
	p := output.New(&buf, true) // JSON mode for easy assertion

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	opts := rollout.Options{Service: "web", ComposeFile: composeFile, ControlAddr: statusSrv.URL}
	if err := runDeploy(cmd, p, opts, "", true, true, false, zap.NewNop()); err != nil {
		t.Fatalf("runDeploy: %v", err)
	}

	var plan DeployPlan
	if err := json.Unmarshal(buf.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan JSON: %v\noutput: %s", err, buf.String())
	}
	if plan.CurrentGeneration != "gen-1" {
		t.Errorf("CurrentGeneration = %q, want gen-1", plan.CurrentGeneration)
	}
	if plan.HealthyBackends != 1 {
		t.Errorf("HealthyBackends = %d, want 1", plan.HealthyBackends)
	}
	if len(plan.Steps) == 0 {
		t.Error("plan should list the steps that will run")
	}
}

// ── Rendering ─────────────────────────────────────────────────────────────────

func TestRenderDeployResultHuman_Success(t *testing.T) {
	var buf bytes.Buffer
	renderDeployResultHuman(&buf, DeployResult{
		Service: "web", Success: true, DurationMS: 1234,
		CurrentGeneration: "gen-2", ProxyStatus: "ready",
	})
	out := buf.String()
	for _, want := range []string{"Deployment complete", "1234ms", "gen-2", "ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderDeployResultHuman_Failure(t *testing.T) {
	var buf bytes.Buffer
	renderDeployResultHuman(&buf, DeployResult{
		Service: "web", Success: false, DurationMS: 500, Error: "healthcheck timeout",
	})
	out := buf.String()
	for _, want := range []string{"Deployment failed", "healthcheck timeout", "rollback web"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
