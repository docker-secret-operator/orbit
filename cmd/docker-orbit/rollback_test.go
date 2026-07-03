package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/cli/clierr"
	"github.com/docker-secret-operator/orbit/internal/cli/output"
	"github.com/docker-secret-operator/orbit/internal/rollout"
	"github.com/spf13/cobra"
)

// ── applyRollbackTargetOverride ──────────────────────────────────────────────

func TestApplyRollbackTargetOverride_EmptyIsNoOp(t *testing.T) {
	rs := &rollout.RolloutState{Service: "web", OldBackendID: "web-old-1"}
	if err := applyRollbackTargetOverride(rs, ""); err != nil {
		t.Errorf("empty --to should be a no-op, got %v", err)
	}
}

func TestApplyRollbackTargetOverride_MatchesRecordedTarget(t *testing.T) {
	rs := &rollout.RolloutState{Service: "web", OldBackendID: "web-old-1"}
	if err := applyRollbackTargetOverride(rs, "web-old-1"); err != nil {
		t.Errorf("--to matching the recorded target should succeed, got %v", err)
	}
}

func TestApplyRollbackTargetOverride_MismatchReturnsExplainedError(t *testing.T) {
	rs := &rollout.RolloutState{Service: "web", OldBackendID: "web-old-1"}
	err := applyRollbackTargetOverride(rs, "some-other-generation")

	ce, ok := err.(*clierr.Error)
	if !ok {
		t.Fatalf("expected *clierr.Error, got %T: %v", err, err)
	}
	if ce.ExitCode != output.ExitConfig {
		t.Errorf("ExitCode = %d, want ExitConfig (%d)", ce.ExitCode, output.ExitConfig)
	}
	if !strings.Contains(ce.What, "some-other-generation") || !strings.Contains(ce.What, "web-old-1") {
		t.Errorf("error should name both the requested and recoverable target: %q", ce.What)
	}
	if ce.Action == "" {
		t.Error("clierr requires a remediation Action — got empty")
	}
}

// ── nonEmptyDuration ──────────────────────────────────────────────────────────

func TestNonEmptyDuration(t *testing.T) {
	if got := nonEmptyDuration(0, 5*time.Second); got != "5s" {
		t.Errorf("nonEmptyDuration(0, 5s) = %q, want 5s", got)
	}
	if got := nonEmptyDuration(15*time.Second, 5*time.Second); got != "15s" {
		t.Errorf("nonEmptyDuration(15s, 5s) = %q, want 15s", got)
	}
}

// ── Rendering ─────────────────────────────────────────────────────────────────

func TestRenderRollbackPlanHuman(t *testing.T) {
	var buf bytes.Buffer
	renderRollbackPlanHuman(&buf, RollbackPlan{
		Service:        "web",
		RestoreTarget:  "web-old-1",
		RestoreAddr:    "10.0.0.1:3000",
		DrainingTarget: "web-new-2",
		Reason:         "restoring the generation active before the last rollout/deploy",
		Drain:          "5s",
		ExpectedImpact: "web traffic returns to web-old-1; web-new-2 is drained and removed",
	})
	out := buf.String()
	for _, want := range []string{"web-old-1", "10.0.0.1:3000", "web-new-2", "5s", "Expected impact"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderRollbackResultHuman_Success(t *testing.T) {
	var buf bytes.Buffer
	renderRollbackResultHuman(&buf, RollbackResult{
		Service: "web", Success: true, DurationMS: 321, RestoredTo: "web-old-1", ProxyStatus: "ready",
	})
	out := buf.String()
	for _, want := range []string{"Rollback complete", "321ms", "web-old-1", "ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderRollbackResultHuman_Failure(t *testing.T) {
	var buf bytes.Buffer
	renderRollbackResultHuman(&buf, RollbackResult{
		Service: "web", Success: false, DurationMS: 88, Error: "proxy unreachable",
	})
	out := buf.String()
	for _, want := range []string{"Rollback failed", "proxy unreachable"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// ── runRollback: dry-run against a real, on-disk rollout state file ─────────
//
// rollout.LoadState reads a fixed path (/tmp/orbit-<service>-state.json —
// see internal/rollout's unexported statePath) with no injection seam, so
// this test writes a real state file there directly, using a unique service
// name to avoid colliding with any concurrent test or real rollout, and
// cleans it up afterward. This is the same "exercise the real thing" bias
// the rest of this package's tests use rather than adding a mock.

func rollbackStatePath(service string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("orbit-%s-state.json", service))
}

func writeTestRolloutState(t *testing.T, rs rollout.RolloutState) {
	t.Helper()
	path := rollbackStatePath(rs.Service)
	data, err := json.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

func TestRunRollback_DryRun_ShowsPlanFromRecordedState(t *testing.T) {
	service := "rollback-dryrun-svc"
	writeTestRolloutState(t, rollout.RolloutState{
		Service:      service,
		OldBackendID: "old-abc",
		OldAddr:      "10.0.0.1:3000",
		NewBackendID: "new-xyz",
		NewAddr:      "10.0.0.2:3000",
		ControlAddr:  "http://localhost:9900",
	})

	var buf bytes.Buffer
	p := output.New(&buf, true) // JSON mode for easy assertion

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runRollback(cmd, p, service, "", "", "", 0, true /* dryRun */, false, nil)
	if err != nil {
		t.Fatalf("runRollback dry-run: %v", err)
	}

	var plan RollbackPlan
	if err := json.Unmarshal(buf.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan JSON: %v\noutput: %s", err, buf.String())
	}
	if plan.RestoreTarget != "old-abc" {
		t.Errorf("RestoreTarget = %q, want old-abc", plan.RestoreTarget)
	}
	if plan.DrainingTarget != "new-xyz" {
		t.Errorf("DrainingTarget = %q, want new-xyz", plan.DrainingTarget)
	}

	// Dry-run must not consume/clear the recorded state.
	if _, err := rollout.LoadState(service); err != nil {
		t.Errorf("dry-run should not clear state, but LoadState failed: %v", err)
	}
}

func TestRunRollback_DryRun_RespectsDrainOverride(t *testing.T) {
	service := "rollback-override-svc"
	writeTestRolloutState(t, rollout.RolloutState{
		Service:      service,
		OldBackendID: "old-abc",
		OldAddr:      "10.0.0.1:3000",
		ControlAddr:  "http://localhost:9900",
		Drain:        5 * time.Second,
	})

	var buf bytes.Buffer
	p := output.New(&buf, true)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	if err := runRollback(cmd, p, service, "", "", "", 20*time.Second, true, false, nil); err != nil {
		t.Fatalf("runRollback: %v", err)
	}

	var plan RollbackPlan
	if err := json.Unmarshal(buf.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan JSON: %v\noutput: %s", err, buf.String())
	}
	if plan.Drain != "20s" {
		t.Errorf("Drain = %q, want 20s (the --drain override, not the recorded 5s)", plan.Drain)
	}
}
