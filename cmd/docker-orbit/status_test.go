package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker-secret-operator/orbit/internal/api"
)

// TestRenderStatusHumanGolden pins renderStatusHuman's exact byte output
// against a fixture in testdata/ — Phase 2.1's Phase 7 explicitly requires
// golden output tests for human-readable mode, on top of the JSON-shape and
// exit-code tests elsewhere in this file/package. Run with
// UPDATE_GOLDEN=1 to regenerate testdata/status_golden.txt after an
// intentional rendering change.
func TestRenderStatusHumanGolden(t *testing.T) {
	report := api.StatusReport{
		Service:            "myapp",
		RuntimeVersion:     "1.2.3",
		ProxyStatus:        "ready",
		CurrentGeneration:  "gen-2",
		PreviousGeneration: "gen-1",
		DeploymentState:    "completed",
		HealthyBackends: []api.BackendStatus{
			{ID: "myapp-gen2-abc123", Addr: "172.20.0.5:3000", Draining: false},
		},
		UnhealthyBackends: []api.BackendStatus{
			{ID: "myapp-gen1-old999", Addr: "172.20.0.4:3000", Draining: true},
		},
		ActiveTrafficTarget: []string{"172.20.0.5:3000"},
		Recovery: api.RecoveryStatus{
			Degraded:             false,
			RecoveryCount:        2,
			RecoveryFailureCount: 0,
		},
	}

	var buf bytes.Buffer
	renderStatusHuman(&buf, report)
	assertGolden(t, "status_golden.txt", buf.Bytes())
}

// assertGolden compares got against testdata/name, byte for byte. Set
// UPDATE_GOLDEN=1 in the environment to write got as the new fixture instead
// of failing — the standard Go community convention for maintaining golden
// files without hand-editing them.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0644); err != nil {
			t.Fatalf("update golden file %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file %s: %v (run with UPDATE_GOLDEN=1 to create it)", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("output does not match %s.\n--- want ---\n%s\n--- got ---\n%s\n(run with UPDATE_GOLDEN=1 to update)", path, want, got)
	}
}

func TestNonEmpty(t *testing.T) {
	if got := nonEmpty("", "fallback"); got != "fallback" {
		t.Errorf("nonEmpty(\"\", fallback) = %q, want fallback", got)
	}
	if got := nonEmpty("value", "fallback"); got != "value" {
		t.Errorf("nonEmpty(value, fallback) = %q, want value", got)
	}
}

func TestNonEmptyList(t *testing.T) {
	if got := nonEmptyList(nil); got != "(none)" {
		t.Errorf("nonEmptyList(nil) = %q, want (none)", got)
	}
	if got := nonEmptyList([]string{"a"}); got != "a" {
		t.Errorf("nonEmptyList([a]) = %q, want a", got)
	}
	if got := nonEmptyList([]string{"a", "b"}); got != "a, b" {
		t.Errorf("nonEmptyList([a b]) = %q, want \"a, b\"", got)
	}
}

func TestDrainSuffix(t *testing.T) {
	if got := drainSuffix(true); got != " (draining)" {
		t.Errorf("drainSuffix(true) = %q, want \" (draining)\"", got)
	}
	if got := drainSuffix(false); got != "" {
		t.Errorf("drainSuffix(false) = %q, want \"\"", got)
	}
}

func TestResolveProjectExplicitWins(t *testing.T) {
	t.Setenv("ORBIT_PROXY_INSTANCE", "env-value")
	if got := resolveProject("explicit"); got != "explicit" {
		t.Errorf("resolveProject with explicit value = %q, want explicit", got)
	}
}

func TestResolveProjectFallsBackToEnv(t *testing.T) {
	t.Setenv("ORBIT_PROXY_INSTANCE", "env-value")
	if got := resolveProject(""); got != "env-value" {
		t.Errorf("resolveProject(\"\") = %q, want env-value", got)
	}
}

func TestResolveProjectDefaultsToDefault(t *testing.T) {
	t.Setenv("ORBIT_PROXY_INSTANCE", "")
	if got := resolveProject(""); got != "default" {
		t.Errorf("resolveProject(\"\") with no env = %q, want default", got)
	}
}
