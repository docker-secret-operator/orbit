package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckComposeFileMissing(t *testing.T) {
	c := checkComposeFile(filepath.Join(t.TempDir(), "does-not-exist.yml"))
	if c.Status != StatusWarn {
		t.Errorf("Status = %v, want WARNING for a missing file", c.Status)
	}
	if c.Remediation == "" {
		t.Error("missing-file check has no remediation, want a next step")
	}
}

func TestCheckComposeFileInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(path, []byte("not: valid: yaml: [["), 0600); err != nil {
		t.Fatal(err)
	}

	c := checkComposeFile(path)
	if c.Status != StatusFail {
		t.Errorf("Status = %v, want ERROR for invalid YAML", c.Status)
	}
}

func TestCheckComposeFileValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	content := `version: "3.9"
services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	c := checkComposeFile(path)
	if c.Status != StatusPass {
		t.Errorf("Status = %v, want PASS for a valid compose file, detail: %s", c.Status, c.Detail)
	}
}

func TestCheckControlAddrInvalidURL(t *testing.T) {
	c := checkControlAddr("not a url")
	if c.Status != StatusFail {
		t.Errorf("Status = %v, want ERROR for an invalid URL", c.Status)
	}
}

func TestCheckControlAddrMissingScheme(t *testing.T) {
	c := checkControlAddr("localhost:9900")
	if c.Status != StatusFail {
		t.Errorf("Status = %v, want ERROR for a URL with no scheme", c.Status)
	}
}

func TestCheckControlAddrValid(t *testing.T) {
	c := checkControlAddr("http://localhost:9900")
	if c.Status != StatusPass {
		t.Errorf("Status = %v, want PASS for a well-formed URL, detail: %s", c.Status, c.Detail)
	}
}

func TestCheckControlAddrWarnsOnShortToken(t *testing.T) {
	t.Setenv("ORBIT_API_TOKEN", "short")
	c := checkControlAddr("http://localhost:9900")
	if c.Status != StatusWarn {
		t.Errorf("Status = %v, want WARNING for a short ORBIT_API_TOKEN", c.Status)
	}
}

func TestCheckProxyReachableAgainstRealServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := checkProxyReachable(srv.URL)
	if c.Status != StatusPass {
		t.Errorf("Status = %v, want PASS against a real reachable server, detail: %s", c.Status, c.Detail)
	}
}

func TestCheckProxyReachableAgainstNothing(t *testing.T) {
	c := checkProxyReachable("http://127.0.0.1:1") // port 1 — nothing listens here
	if c.Status != StatusWarn {
		t.Errorf("Status = %v, want WARNING when nothing is listening", c.Status)
	}
	if c.Remediation == "" {
		t.Error("unreachable-proxy check has no remediation")
	}
}

func TestCheckProxyReadyReportsNotReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"status": "not_ready",
			"reason": "still starting up",
			"state":  "recovering",
		})
	}))
	defer srv.Close()

	c := checkProxyReady(srv.URL)
	if c.Status != StatusWarn {
		t.Errorf("Status = %v, want WARNING for a not_ready proxy", c.Status)
	}
}

func TestCheckProxyReadyReportsReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ready", "state": "ready"}) //nolint:errcheck
	}))
	defer srv.Close()

	c := checkProxyReady(srv.URL)
	if c.Status != StatusPass {
		t.Errorf("Status = %v, want PASS for a ready proxy, detail: %s", c.Status, c.Detail)
	}
}

func TestCheckStateDirWritable(t *testing.T) {
	t.Setenv("ORBIT_STATE_DIR", t.TempDir())
	c := checkStateDirWritable()
	if c.Status != StatusPass {
		t.Errorf("Status = %v, want PASS for a writable temp dir, detail: %s", c.Status, c.Detail)
	}
}

func TestCheckStateDirWritableFailsOnReadOnlyParent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — permission checks don't apply")
	}
	dir := t.TempDir()
	roParent := filepath.Join(dir, "readonly")
	if err := os.Mkdir(roParent, 0500); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORBIT_STATE_DIR", filepath.Join(roParent, "nested"))

	c := checkStateDirWritable()
	if c.Status != StatusFail {
		t.Errorf("Status = %v, want ERROR when the state dir can't be created", c.Status)
	}
}

func TestCheckComposeAvailableReturnsAResult(t *testing.T) {
	// Whether `docker compose` is actually installed varies by environment
	// (CI vs. local dev vs. a machine with no Docker at all), so this test
	// asserts the check runs and reports *something* actionable rather than
	// hard-coding PASS or FAIL for a binary we don't control.
	c := checkComposeAvailable(context.Background())
	if c.Name != "Docker Compose available" {
		t.Errorf("Name = %q, want %q", c.Name, "Docker Compose available")
	}
	if c.Detail == "" {
		t.Error("Detail is empty — check should always explain what it found")
	}
	if c.Status == StatusFail && c.Remediation == "" {
		t.Error("FAIL status has no remediation")
	}
}

func TestCheckPortsAvailableAllFree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	// Port 0 always looks "free" to net.Listen when queried directly, so
	// instead assert PASS against a service with no declared ports at all —
	// the check should treat "nothing to check" as trivially satisfied.
	content := "services:\n  worker:\n    image: busybox:latest\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	c := checkPortsAvailable(path)
	if c.Status != StatusPass {
		t.Errorf("Status = %v, want PASS when no ports are declared, detail: %s", c.Status, c.Detail)
	}
}

func TestCheckPortsAvailableDetectsBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("could not bind a test listener: %v", err)
	}
	defer ln.Close() //nolint:errcheck // best-effort cleanup of a test-only listener
	port := ln.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	content := fmt.Sprintf("services:\n  web:\n    image: myapp:1.0\n    ports:\n      - \"%d:3000\"\n", port)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	c := checkPortsAvailable(path)
	if c.Status != StatusWarn {
		t.Errorf("Status = %v, want WARNING when the port is already bound, detail: %s", c.Status, c.Detail)
	}
	if c.Remediation == "" {
		t.Error("busy-port check has no remediation")
	}
}

func TestCheckPortsAvailableMissingFile(t *testing.T) {
	c := checkPortsAvailable(filepath.Join(t.TempDir(), "does-not-exist.yml"))
	if c.Status != StatusSkip {
		t.Errorf("Status = %v, want SKIPPED when the compose file can't be read (it's the same root cause as the 'Compose file' check, not an independent warning)", c.Status)
	}
}

// TestRenderDoctorHumanGolden pins renderDoctorHuman's exact byte output
// against a fixture in testdata/, covering the mixed PASS/WARNING/ERROR
// rendering (including the "→ remediation" line) — see assertGolden in
// status_test.go for the UPDATE_GOLDEN=1 regeneration convention.
func TestRenderDoctorHumanGolden(t *testing.T) {
	report := DoctorReport{
		Checks: []Check{
			{Name: "Docker Engine reachable", Status: StatusPass, Detail: "Docker daemon responded to ping"},
			{Name: "Proxy reachable", Status: StatusWarn,
				Detail:      "no response from http://localhost:9900: connection refused",
				Remediation: "If you expect a proxy to be running: docker ps --filter name=docker-rollout-proxy"},
			{Name: "Recovery state consistent", Status: StatusFail,
				Detail:      "proxy reports degraded recovery state",
				Remediation: "Run 'docker orbit status' for detail"},
		},
	}
	report.Summary.Pass = 1
	report.Summary.Warning = 1
	report.Summary.Error = 1

	var buf bytes.Buffer
	renderDoctorHuman(&buf, report)
	assertGolden(t, "doctor_golden.txt", buf.Bytes())
}

func TestRunDoctorChecksSummaryCounts(t *testing.T) {
	report := runDoctorChecks(context.Background(), "http://127.0.0.1:1", filepath.Join(t.TempDir(), "missing.yml"), "test")

	total := report.Summary.Pass + report.Summary.Warning + report.Summary.Error + report.Summary.Skipped
	if total != len(report.Checks) {
		t.Errorf("summary counts (%d) don't add up to len(Checks) (%d)", total, len(report.Checks))
	}
	if len(report.Checks) != 10 {
		t.Errorf("got %d checks, want 10 (one per registered check)", len(report.Checks))
	}
}

func TestEveryNonPassCheckHasRemediation(t *testing.T) {
	// Regression guard for Phase 2.1's explicit requirement: "Provide
	// actionable remediation steps" for anything that isn't PASS.
	report := runDoctorChecks(context.Background(), "http://127.0.0.1:1", filepath.Join(t.TempDir(), "missing.yml"), "test")
	for _, c := range report.Checks {
		if c.Status != StatusPass && c.Remediation == "" {
			t.Errorf("check %q has status %v but no remediation", c.Name, c.Status)
		}
	}
}

func TestDoctorNeverShowsRawStackTrace(t *testing.T) {
	report := runDoctorChecks(context.Background(), "http://127.0.0.1:1", filepath.Join(t.TempDir(), "missing.yml"), "test")
	for _, c := range report.Checks {
		if containsStackTraceMarkers(c.Detail) || containsStackTraceMarkers(c.Remediation) {
			t.Errorf("check %q leaked stack-trace-like content: detail=%q remediation=%q", c.Name, c.Detail, c.Remediation)
		}
	}
}

func containsStackTraceMarkers(s string) bool {
	markers := []string{"goroutine ", ".go:", "runtime.", "\tmain."}
	for _, m := range markers {
		if len(s) >= len(m) {
			for i := 0; i+len(m) <= len(s); i++ {
				if s[i:i+len(m)] == m {
					return true
				}
			}
		}
	}
	return false
}
