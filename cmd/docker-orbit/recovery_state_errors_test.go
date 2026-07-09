package main

import (
	"context"
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
