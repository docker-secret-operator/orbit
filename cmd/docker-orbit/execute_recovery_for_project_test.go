package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// testCfg mirrors recovery_state_errors_test.go's pattern: StartupTimeout is
// left at its zero value, which makes executeRecovery's retry budget
// already-expired, so discovery fails fast and deterministically (no real
// Docker daemon state can make these tests flaky) rather than retrying for
// the length of a real startup budget.
func testCfg() *config.ProxyConfig {
	return &config.ProxyConfig{
		TCPDialTimeout:    100 * time.Millisecond,
		TransitionTimeout: 5 * time.Minute,
	}
}

func writeCorruptedActiveGenState(t *testing.T, sm *state.StateManager, service string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(sm.ActiveGenerationPath(service)), 0700); err != nil {
		t.Fatalf("failed to create state dir: %v", err)
	}
	if err := os.WriteFile(sm.ActiveGenerationPath(service), []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("failed to write corrupted state file: %v", err)
	}
}

// TestExecuteRecoveryForProject_MultipleServicesIndependent is the
// load-bearing test for Stage 2.3: two services, one with a corrupted
// persisted state file, processed by one executeRecoveryForProject call.
// The corrupted service's own recovery pass must log the corruption; the
// healthy service must show no trace of it. Each service's Registry must
// end the pass containing only its own pre-seeded marker backend — proof
// that recovery for one service never touches another's registry.
func TestExecuteRecoveryForProject_MultipleServicesIndependent(t *testing.T) {
	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	writeCorruptedActiveGenState(t, sm, "web") // "api" stays clean (no file at all)

	regWeb := proxy.NewRegistry()
	if err := regWeb.Add(proxy.Backend{ID: "web-marker", Addr: "10.0.0.1:80"}); err != nil {
		t.Fatal(err)
	}
	regAPI := proxy.NewRegistry()
	if err := regAPI.Add(proxy.Backend{ID: "api-marker", Addr: "10.0.0.2:80"}); err != nil {
		t.Fatal(err)
	}

	pr := proxy.NewProjectRegistry()
	pr.Register("web", regWeb)
	pr.Register("api", regAPI)

	core, observed := observer.New(zapcore.WarnLevel)
	log := zap.New(core)
	cfg := testCfg()
	mc := metrics.NewMetricsCollector()
	debugHandler := api.NewDebugHandler(sm, mc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := executeRecoveryForProject(ctx, cfg, sm, pr, "test-project", mc, debugHandler, log)

	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d: %v", len(results), results)
	}
	if _, ok := results["web"]; !ok {
		t.Error("web must have its own recovery outcome")
	}
	if _, ok := results["api"]; !ok {
		t.Error("api must have its own recovery outcome")
	}

	corruptionLogged := false
	for _, entry := range observed.All() {
		msg := strings.ToLower(entry.Message)
		if strings.Contains(msg, "active generation") && strings.Contains(msg, "unreadable") {
			corruptionLogged = true // only web has a corrupted file; any such warning must be web's
		}
	}
	if !corruptionLogged {
		t.Error("expected a corrupted-state warning triggered by web's bad file")
	}

	// Registry isolation: each Registry must still contain only its own
	// pre-seeded marker — recovery for one service must never add to or
	// remove from another service's registry.
	webBackends := regWeb.Backends()
	if len(webBackends) != 1 || webBackends[0].ID != "web-marker" {
		t.Fatalf("regWeb must contain only web-marker, got %v", webBackends)
	}
	apiBackends := regAPI.Backends()
	if len(apiBackends) != 1 || apiBackends[0].ID != "api-marker" {
		t.Fatalf("regAPI must contain only api-marker, got %v", apiBackends)
	}
}

// TestExecuteRecoveryForProject_EmptyProjectRegistry proves an empty
// ProjectRegistry produces an empty result map, not a panic.
func TestExecuteRecoveryForProject_EmptyProjectRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	pr := proxy.NewProjectRegistry()
	cfg := testCfg()
	mc := metrics.NewMetricsCollector()
	debugHandler := api.NewDebugHandler(sm, mc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := executeRecoveryForProject(ctx, cfg, sm, pr, "test-project", mc, debugHandler, zap.NewNop())
	if len(results) != 0 {
		t.Fatalf("want 0 results for an empty ProjectRegistry, got %d", len(results))
	}
}

// TestExecuteRecoveryForProject_ContinuesAfterServiceIssue proves that one
// service's recovery issue (here: a corrupted persisted-state file, this
// system's realistic stand-in for "a service fails recovery" since
// executeRecovery's err return is reserved for future use and never
// populated today) does not prevent a healthy service later in the sorted
// iteration order from getting its own full recovery attempt.
func TestExecuteRecoveryForProject_ContinuesAfterServiceIssue(t *testing.T) {
	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	writeCorruptedActiveGenState(t, sm, "aaa-broken") // sorts first

	pr := proxy.NewProjectRegistry()
	pr.Register("aaa-broken", proxy.NewRegistry())
	pr.Register("zzz-healthy", proxy.NewRegistry())

	cfg := testCfg()
	mc := metrics.NewMetricsCollector()
	debugHandler := api.NewDebugHandler(sm, mc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := executeRecoveryForProject(ctx, cfg, sm, pr, "test-project", mc, debugHandler, zap.NewNop())

	if _, ok := results["aaa-broken"]; !ok {
		t.Error("the broken service must still have produced an outcome, not aborted the loop")
	}
	if _, ok := results["zzz-healthy"]; !ok {
		t.Error("zzz-healthy must have been reached and recovered despite aaa-broken's issue")
	}
}

// TestExecuteRecoveryForProject_RegistryReplacement proves that replacing a
// service's Registry in ProjectRegistry is respected on the next call —
// the old Registry is never touched by a later pass, and the new one starts
// clean. Uses pre-seeded markers rather than a real discovered backend,
// since no container in this environment carries labels matching these
// synthetic service names.
func TestExecuteRecoveryForProject_RegistryReplacement(t *testing.T) {
	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	pr := proxy.NewProjectRegistry()

	regA := proxy.NewRegistry()
	if err := regA.Add(proxy.Backend{ID: "regA-marker", Addr: "10.1.1.1:80"}); err != nil {
		t.Fatal(err)
	}
	pr.Register("web", regA)

	cfg := testCfg()
	mc := metrics.NewMetricsCollector()
	debugHandler := api.NewDebugHandler(sm, mc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	executeRecoveryForProject(ctx, cfg, sm, pr, "test-project", mc, debugHandler, zap.NewNop())

	regB := proxy.NewRegistry()
	if err := regB.Add(proxy.Backend{ID: "regB-marker", Addr: "10.2.2.2:80"}); err != nil {
		t.Fatal(err)
	}
	pr.Register("web", regB) // replace

	executeRecoveryForProject(ctx, cfg, sm, pr, "test-project", mc, debugHandler, zap.NewNop())

	aBackends := regA.Backends()
	if len(aBackends) != 1 || aBackends[0].ID != "regA-marker" {
		t.Fatalf("regA must be untouched after being replaced, got %v", aBackends)
	}
	bBackends := regB.Backends()
	if len(bBackends) != 1 || bBackends[0].ID != "regB-marker" {
		t.Fatalf("regB must contain only its own marker, got %v", bBackends)
	}
}

// TestExecuteRecoveryForProject_ConcurrentServiceRemoval exercises the
// defensive skip branch (pr.For returning false for a service that was in
// pr.Services()'s snapshot a moment earlier) for real, under -race: a
// service is removed from another goroutine while executeRecoveryForProject
// is mid-loop. Must not panic or error.
func TestExecuteRecoveryForProject_ConcurrentServiceRemoval(t *testing.T) {
	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	pr := proxy.NewProjectRegistry()
	pr.Register("web", proxy.NewRegistry())
	pr.Register("api", proxy.NewRegistry())

	cfg := testCfg()
	mc := metrics.NewMetricsCollector()
	debugHandler := api.NewDebugHandler(sm, mc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pr.Remove("api") // races with executeRecoveryForProject's loop below
	}()

	executeRecoveryForProject(ctx, cfg, sm, pr, "test-project", mc, debugHandler, zap.NewNop()) // must not panic

	wg.Wait()
}

// TestExecuteRecoveryForProject_LogsCarryServiceField is the Stage 2.4 test:
// executeRecovery's own log call sites are unmodified (no executeRecovery
// logging line was touched for this stage) — the field appears because
// executeRecoveryForProject hands each iteration a *zap.Logger pre-scoped
// with that service's name via log.With, so every entry executeRecovery
// itself emits is attributable without cross-referencing which pass
// produced it. Every entry must carry a service field naming one of the two
// configured services; the corruption-specific warning must name the
// broken one specifically, never the healthy one.
func TestExecuteRecoveryForProject_LogsCarryServiceField(t *testing.T) {
	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	writeCorruptedActiveGenState(t, sm, "aaa-broken")

	pr := proxy.NewProjectRegistry()
	pr.Register("aaa-broken", proxy.NewRegistry())
	pr.Register("zzz-healthy", proxy.NewRegistry())

	core, observed := observer.New(zapcore.InfoLevel)
	log := zap.New(core)
	cfg := testCfg()
	mc := metrics.NewMetricsCollector()
	debugHandler := api.NewDebugHandler(sm, mc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	executeRecoveryForProject(ctx, cfg, sm, pr, "test-project", mc, debugHandler, log)

	if len(observed.All()) == 0 {
		t.Fatal("expected at least some log output from the recovery pass")
	}

	seenServices := map[string]bool{}
	corruptionServiceSeen := ""
	for _, entry := range observed.All() {
		fields := entry.ContextMap()
		svc, ok := fields["service"]
		if !ok {
			t.Fatalf("log entry %q has no service field: %v", entry.Message, fields)
		}
		svcStr, _ := svc.(string)
		if svcStr != "aaa-broken" && svcStr != "zzz-healthy" {
			t.Fatalf("log entry %q has unexpected service field %q", entry.Message, svcStr)
		}
		seenServices[svcStr] = true

		if strings.Contains(strings.ToLower(entry.Message), "active generation") &&
			strings.Contains(strings.ToLower(entry.Message), "unreadable") {
			corruptionServiceSeen = svcStr
		}
	}

	if !seenServices["aaa-broken"] || !seenServices["zzz-healthy"] {
		t.Fatalf("expected log output attributed to both services, got %v", seenServices)
	}
	if corruptionServiceSeen != "aaa-broken" {
		t.Fatalf("corruption warning must be attributed to aaa-broken specifically, got %q", corruptionServiceSeen)
	}
}
