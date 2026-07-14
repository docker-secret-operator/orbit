package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/api"
	"github.com/docker-secret-operator/orbit/internal/config"
	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/proxy"
	"github.com/docker-secret-operator/orbit/internal/state"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestExecuteRecovery_CorruptedActiveGenState_LogsError reproduces the
// silent-discard bug: LoadActiveGenerationState/LoadRolloutState return a
// real error only on corruption or I/O failure (never on "no state file
// yet", which is nil, nil) — executeRecovery used to discard that error via
// `activeGenState, _ := sm.Load...`, making a genuine on-disk corruption
// indistinguishable from a fresh install in the logs.
func TestExecuteRecovery_CorruptedActiveGenState_LogsError(t *testing.T) {
	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)

	const service = "web"

	// Write unparseable JSON directly to the active-generation state path,
	// simulating on-disk corruption (e.g. a crash mid-write bypassing the
	// atomic-write path, or bit rot).
	if err := os.MkdirAll(filepath.Dir(sm.ActiveGenerationPath(service)), 0700); err != nil {
		t.Fatalf("failed to create state dir: %v", err)
	}
	if err := os.WriteFile(sm.ActiveGenerationPath(service), []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("failed to write corrupted state file: %v", err)
	}

	core, observed := observer.New(zapcore.WarnLevel)
	log := zap.New(core)

	cfg := &config.ProxyConfig{
		ProxyInstance:     service,
		TCPDialTimeout:    100 * time.Millisecond,
		TransitionTimeout: 5 * time.Minute,
	}
	reg := proxy.NewRegistry()
	mc := metrics.NewMetricsCollector()
	debugHandler := api.NewDebugHandler(sm, mc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	executeRecovery(ctx, cfg, sm, reg, service, mc, debugHandler, log)

	found := false
	for _, entry := range observed.All() {
		msg := strings.ToLower(entry.Message)
		if strings.Contains(msg, "active generation") && (strings.Contains(msg, "corrupt") || strings.Contains(msg, "unreadable") || strings.Contains(msg, "failed")) {
			found = true
		}
	}
	if !found {
		var messages []string
		for _, e := range observed.All() {
			messages = append(messages, e.Message)
		}
		t.Fatalf("expected a warning/error log surfacing the corrupted active generation state, got: %v", messages)
	}
}

// TestForceDegradedOnStateCorruption_OverridesInferredPlan closes the
// go-live audit's finding H5: LoadActiveGenerationState/LoadRolloutState
// correctly fail closed on real corruption (a *state.StateLoadError with
// IsFatal true), but executeRecovery logged the error and then proceeded
// with the state variable nil — identical to a genuine fresh install. That
// collapses "no history" and "history existed but got corrupted" into the
// same handling, letting GenerateRecoveryPlan infer authority from live
// health as if nothing had ever been recorded. A corrupted state file must
// force a Degraded outcome instead, discarding whatever plan health
// inference produced.
func TestForceDegradedOnStateCorruption_OverridesInferredPlan(t *testing.T) {
	plan := &state.RecoveryPlan{
		Service:                 "web",
		Action:                  state.RecoveryInferredFallback,
		AuthoritativeGeneration: "gen-new",
		BackendsToRestore: []state.BackendCandidate{
			{Generation: "gen-new", ID: "b1", ValidityStatus: state.CandidateValid},
		},
	}
	corruptErr := &state.StateLoadError{Path: "/tmp/active-gen.json", Reason: "bad json", IsFatal: true}

	got := forceDegradedOnStateCorruption(plan, corruptErr, nil)

	if got.Action != state.RecoveryDegraded {
		t.Fatalf("Action = %s, want %s — corrupted state must never let health inference stand", got.Action, state.RecoveryDegraded)
	}
	if len(got.BackendsToRestore) != 0 {
		t.Errorf("BackendsToRestore should be cleared when forcing degradation, got %v", got.BackendsToRestore)
	}
	if got.AuthoritativeGeneration != "" {
		t.Errorf("AuthoritativeGeneration should be cleared when forcing degradation, got %q", got.AuthoritativeGeneration)
	}
	if got.Reason == "" || got.FailedReason == "" {
		t.Error("Reason/FailedReason must explain the forced degradation for operator visibility")
	}
}

func TestForceDegradedOnStateCorruption_NoOpWhenNotCorrupted(t *testing.T) {
	plan := &state.RecoveryPlan{Action: state.RecoveryRestoreSingle, AuthoritativeGeneration: "gen-1"}

	got := forceDegradedOnStateCorruption(plan, nil, nil)

	if got.Action != state.RecoveryRestoreSingle || got.AuthoritativeGeneration != "gen-1" {
		t.Errorf("plan should be unchanged with no corruption, got Action=%s Authoritative=%q", got.Action, got.AuthoritativeGeneration)
	}
}

// TestForceDegradedOnStateCorruption_IgnoresOrdinaryAbsence confirms the
// ordinary "no state file yet" case (nil, nil from Load*) never triggers
// forced degradation — only genuine *state.StateLoadError corruption does.
func TestForceDegradedOnStateCorruption_IgnoresOrdinaryAbsence(t *testing.T) {
	plan := &state.RecoveryPlan{Action: state.RecoveryInferredFallback, AuthoritativeGeneration: "gen-1"}
	ordinaryErr := errors.New("some non-corruption error")

	got := forceDegradedOnStateCorruption(plan, nil, nil)
	if got.Action != state.RecoveryInferredFallback {
		t.Errorf("nil errors should never force degradation, got Action=%s", got.Action)
	}

	// A non-StateLoadError error type is not a corruption signal either —
	// only *state.StateLoadError{IsFatal: true} is.
	plan2 := &state.RecoveryPlan{Action: state.RecoveryInferredFallback, AuthoritativeGeneration: "gen-1"}
	got2 := forceDegradedOnStateCorruption(plan2, ordinaryErr, nil)
	if got2.Action != state.RecoveryInferredFallback {
		t.Errorf("non-StateLoadError errors should not force degradation, got Action=%s", got2.Action)
	}
}
