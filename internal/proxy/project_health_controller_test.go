package proxy

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// countingProber scripts health per-address and counts probes per-address,
// so tests can assert exactly which backends were (and were not) probed.
type countingProber struct {
	mu     sync.Mutex
	health map[string]bool
	calls  map[string]int
}

func newCountingProber() *countingProber {
	return &countingProber{health: map[string]bool{}, calls: map[string]int{}}
}
func (c *countingProber) set(addr string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health[addr] = ok
}
func (c *countingProber) Probe(_ context.Context, addr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[addr]++
	ok, present := c.health[addr]
	return !present || ok // default healthy unless scripted otherwise
}
func (c *countingProber) callCount(addr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[addr]
}

func phcFastCfg() HealthControllerConfig {
	return HealthControllerConfig{Interval: time.Millisecond, Timeout: time.Second, UnhealthyThreshold: 2, HealthyThreshold: 3, MaxConcurrent: 8}
}

// TestProjectHealthController_PerServiceIsolation is the load-bearing test
// for Stage 2.2: two services, each with its own Registry and backend,
// evaluated by one ProjectHealthController. A failing backend on service A
// must demote only within registry A — service B's hysteresis counters and
// backend state must be completely unaffected.
func TestProjectHealthController_PerServiceIsolation(t *testing.T) {
	regWeb := NewRegistry()
	addBackend(t, regWeb, "web-b1")
	if err := regWeb.Add(Backend{ID: "web-keep", Addr: "10.9.9.1:80"}); err != nil {
		t.Fatal(err)
	}

	regAPI := NewRegistry()
	if err := regAPI.Add(Backend{ID: "api-b1", Addr: "10.0.0.2:80"}); err != nil {
		t.Fatal(err)
	}

	pr := NewProjectRegistry()
	pr.Register("web", regWeb)
	pr.Register("api", regAPI)

	prober := newCountingProber()
	prober.set("10.0.0.1:80", false) // web-b1 unhealthy
	// api-b1 (10.0.0.2:80) left healthy — never scripted false.

	phc := NewProjectHealthController(pr, prober, phcFastCfg(), nil, nil)
	ctx := context.Background()

	phc.CheckOnce(ctx) // fail 1 for web-b1 — still active (threshold 2)
	if st, _ := regWeb.State("web-b1"); st != StateActive {
		t.Fatalf("after 1 failure want web-b1 active, got %s", st)
	}
	phc.CheckOnce(ctx) // fail 2 for web-b1 — demotes
	if st, _ := regWeb.State("web-b1"); st != StateUnhealthy {
		t.Fatalf("after 2 failures want web-b1 unhealthy, got %s", st)
	}

	// api-b1 must never have been touched by web's failures.
	if st, _ := regAPI.State("api-b1"); st != StateActive {
		t.Fatalf("api-b1 must remain active — services must not affect each other, got %s", st)
	}
	if prober.callCount("10.0.0.2:80") != 2 {
		t.Fatalf("api-b1 should have been probed twice (once per CheckOnce), got %d", prober.callCount("10.0.0.2:80"))
	}
}

// TestProjectHealthController_RegistryReplacement proves that replacing a
// service's Registry (ProjectRegistry.Register called again) produces a
// fresh HealthController for that service — the old Registry's backend is
// never probed again once replaced, and the new Registry's backend is
// evaluated with clean hysteresis state, not whatever the old one had
// accumulated.
func TestProjectHealthController_RegistryReplacement(t *testing.T) {
	regOld := NewRegistry()
	if err := regOld.Add(Backend{ID: "old-backend", Addr: "10.1.1.1:80"}); err != nil {
		t.Fatal(err)
	}

	pr := NewProjectRegistry()
	pr.Register("web", regOld)

	prober := newCountingProber()
	prober.set("10.1.1.1:80", false)

	phc := NewProjectHealthController(pr, prober, phcFastCfg(), nil, nil)
	ctx := context.Background()

	phc.CheckOnce(ctx)
	if prober.callCount("10.1.1.1:80") != 1 {
		t.Fatalf("old-backend should have been probed once, got %d", prober.callCount("10.1.1.1:80"))
	}

	regNew := NewRegistry()
	if err := regNew.Add(Backend{ID: "new-backend", Addr: "10.2.2.2:80"}); err != nil {
		t.Fatal(err)
	}
	pr.Register("web", regNew) // replace

	phc.CheckOnce(ctx)

	if prober.callCount("10.1.1.1:80") != 1 {
		t.Fatalf("old-backend must never be probed again after its Registry was replaced, got %d calls", prober.callCount("10.1.1.1:80"))
	}
	if prober.callCount("10.2.2.2:80") != 1 {
		t.Fatalf("new-backend should have been probed once, got %d", prober.callCount("10.2.2.2:80"))
	}
	if st, _ := regNew.State("new-backend"); st != StateActive {
		t.Fatalf("new-backend should still be active (only 1 healthy probe, no failures), got %s", st)
	}
}

// TestProjectHealthController_EmptyProjectRegistry proves CheckOnce on an
// empty ProjectRegistry is a safe no-op.
func TestProjectHealthController_EmptyProjectRegistry(t *testing.T) {
	pr := NewProjectRegistry()
	phc := NewProjectHealthController(pr, newCountingProber(), phcFastCfg(), nil, nil)
	phc.CheckOnce(context.Background()) // must not panic
}

// TestProjectHealthController_ServiceRemovedBetweenTicks proves that
// removing a service from the ProjectRegistry mid-stream is handled
// gracefully — the next CheckOnce simply stops evaluating it, no error.
func TestProjectHealthController_ServiceRemovedBetweenTicks(t *testing.T) {
	reg := NewRegistry()
	addBackend(t, reg, "b1")

	pr := NewProjectRegistry()
	pr.Register("web", reg)

	phc := NewProjectHealthController(pr, newCountingProber(), phcFastCfg(), nil, nil)
	ctx := context.Background()

	phc.CheckOnce(ctx) // fine, web exists

	pr.Remove("web")

	phc.CheckOnce(ctx) // must not panic or error now that web is gone
}

// TestProjectHealthController_Run exercises the real ticker-driven path
// (not just CheckOnce) under -race, with concurrent Register/Remove churn
// on the ProjectRegistry while Run is ticking — this is the scenario
// flagged in the implementation plan's Concurrency Review (§ Stage 2 test
// row: "Concurrent registration across services").
func TestProjectHealthController_Run(t *testing.T) {
	pr := NewProjectRegistry()
	phc := NewProjectHealthController(pr, newCountingProber(), phcFastCfg(), nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go phc.Run(ctx)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reg := NewRegistry()
			reg.Add(Backend{ID: "b", Addr: "10.0.0.1:80"}) //nolint:errcheck
			pr.Register("svc", reg)
			time.Sleep(2 * time.Millisecond)
			pr.Remove("svc")
		}(i)
	}
	wg.Wait()
	cancel()
}

// TestProjectHealthController_LogsCarryServiceField is the Stage 2.4 test:
// HealthController's own "health: backend transitioned" log line is emitted
// unmodified (no HealthController code changed for this stage), but the
// *zap.Logger ProjectHealthController hands it must be pre-scoped with a
// service field, so the resulting log entry is attributable without an
// operator having to cross-reference which HealthController instance
// produced it. Two services are evaluated; each one's transition log must
// carry its own service name, never the other's.
func TestProjectHealthController_LogsCarryServiceField(t *testing.T) {
	regWeb := NewRegistry()
	addBackend(t, regWeb, "web-b1")
	if err := regWeb.Add(Backend{ID: "web-keep", Addr: "10.9.9.1:80"}); err != nil {
		t.Fatal(err)
	}
	regAPI := NewRegistry()
	addBackend(t, regAPI, "api-b1")
	if err := regAPI.Add(Backend{ID: "api-keep", Addr: "10.9.9.2:80"}); err != nil {
		t.Fatal(err)
	}

	pr := NewProjectRegistry()
	pr.Register("web", regWeb)
	pr.Register("api", regAPI)

	prober := newCountingProber()
	prober.set("10.0.0.1:80", false) // both web-b1 and api-b1 share this addr from addBackend — both fail identically

	core, observed := observer.New(zapcore.InfoLevel)
	log := zap.New(core)
	phc := NewProjectHealthController(pr, prober, phcFastCfg(), nil, log)
	ctx := context.Background()

	phc.CheckOnce(ctx) // fail 1 — still active
	phc.CheckOnce(ctx) // fail 2 — both demote, both should log a transition

	var webTagged, apiTagged, untagged int
	for _, entry := range observed.All() {
		if entry.Message != "health: backend transitioned" {
			continue
		}
		fields := entry.ContextMap()
		svc, ok := fields["service"]
		if !ok {
			untagged++
			continue
		}
		switch svc {
		case "web":
			webTagged++
		case "api":
			apiTagged++
		default:
			t.Fatalf("unexpected service field value %q", svc)
		}
	}

	if untagged != 0 {
		t.Errorf("every transition log must carry a service field, got %d without one", untagged)
	}
	if webTagged == 0 {
		t.Error("expected at least one transition log tagged service=web")
	}
	if apiTagged == 0 {
		t.Error("expected at least one transition log tagged service=api")
	}
}
