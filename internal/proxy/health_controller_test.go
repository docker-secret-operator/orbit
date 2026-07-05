package proxy

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedProber returns health per-addr from a mutable map (protected).
type scriptedProber struct {
	mu     sync.Mutex
	health map[string]bool
	calls  atomic.Int64
}

func newScriptedProber() *scriptedProber { return &scriptedProber{health: map[string]bool{}} }
func (s *scriptedProber) set(addr string, ok bool) {
	s.mu.Lock()
	s.health[addr] = ok
	s.mu.Unlock()
}
func (s *scriptedProber) Probe(_ context.Context, addr string) bool {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	ok, present := s.health[addr]
	return !present || ok // default healthy unless scripted unhealthy
}

func newHC(r *Registry, p HealthProber, cfg HealthControllerConfig) *HealthController {
	return NewHealthController(r, p, cfg, nil, nil)
}

func fastCfg() HealthControllerConfig {
	return HealthControllerConfig{Interval: time.Millisecond, Timeout: time.Second, UnhealthyThreshold: 2, HealthyThreshold: 3, MaxConcurrent: 8}
}

// C3 — hysteresis: needs UnhealthyThreshold consecutive fails to demote.
func TestWPC_UnhealthyHysteresis(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	// second, always-healthy backend so guarded demotion is permitted
	// (zero-backend protection would otherwise refuse evicting the last active).
	if err := r.Add(Backend{ID: "keep", Addr: "10.9.9.9:80"}); err != nil {
		t.Fatal(err)
	}
	p := newScriptedProber()
	p.set("10.0.0.1:80", false)
	hc := newHC(r, p, fastCfg())
	ctx := context.Background()

	hc.CheckOnce(ctx) // fail 1 → still active (threshold 2)
	if st, _ := r.State("b1"); st != StateActive {
		t.Fatalf("after 1 failure want active, got %s", st)
	}
	hc.CheckOnce(ctx) // fail 2 → unhealthy
	if st, _ := r.State("b1"); st != StateUnhealthy {
		t.Fatalf("after 2 failures want unhealthy, got %s", st)
	}
	for _, b := range r.Active() {
		if b.ID == "b1" {
			t.Fatal("unhealthy b1 must be excluded from Active()")
		}
	}
	if len(r.Active()) != 1 {
		t.Fatalf("healthy backend must remain active, got %d", len(r.Active()))
	}
}

// C3 — recovery: needs HealthyThreshold consecutive successes to re-promote.
func TestWPC_HealthyHysteresisRecovery(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	if err := r.Add(Backend{ID: "keep", Addr: "10.9.9.9:80"}); err != nil {
		t.Fatal(err)
	}
	p := newScriptedProber()
	p.set("10.0.0.1:80", false)
	hc := newHC(r, p, fastCfg())
	ctx := context.Background()

	hc.CheckOnce(ctx)
	hc.CheckOnce(ctx) // now unhealthy
	if st, _ := r.State("b1"); st != StateUnhealthy {
		t.Fatalf("setup: want unhealthy, got %s", st)
	}

	p.set("10.0.0.1:80", true)
	hc.CheckOnce(ctx) // ok 1
	hc.CheckOnce(ctx) // ok 2 → still unhealthy (threshold 3)
	if st, _ := r.State("b1"); st != StateUnhealthy {
		t.Fatalf("after 2 successes want still unhealthy, got %s", st)
	}
	hc.CheckOnce(ctx) // ok 3 → active
	if st, _ := r.State("b1"); st != StateActive {
		t.Fatalf("after 3 successes want active, got %s", st)
	}
}

// C2 — controller only writes health via the Registry; a healthy backend stays active.
func TestWPC_HealthyBackendStaysActive(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	hc := newHC(r, newScriptedProber(), fastCfg()) // default healthy
	for i := 0; i < 5; i++ {
		hc.CheckOnce(context.Background())
	}
	if st, _ := r.State("b1"); st != StateActive {
		t.Fatalf("healthy backend must stay active, got %s", st)
	}
}

// C1 — draining/failed backends are not evaluated (owned by other layers).
func TestWPC_SkipsDrainingAndFailed(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "drain")
	addBackend(t, r, "fail")
	_ = r.SetState("drain", StateDraining)
	_ = r.SetState("fail", StateFailed)
	p := newScriptedProber()
	p.set("10.0.0.1:80", false)
	hc := newHC(r, p, fastCfg())
	hc.CheckOnce(context.Background())
	if st, _ := r.State("drain"); st != StateDraining {
		t.Fatalf("draining must be untouched, got %s", st)
	}
	if st, _ := r.State("fail"); st != StateFailed {
		t.Fatalf("failed must be untouched, got %s", st)
	}
	if p.calls.Load() != 0 {
		t.Fatalf("draining/failed backends must not be probed, got %d probes", p.calls.Load())
	}
}

// C1 — Run honors context cancellation and shuts down without leaking.
func TestWPC_RunShutdown(t *testing.T) {
	r := NewRegistry()
	addBackend(t, r, "b1")
	hc := newHC(r, newScriptedProber(), fastCfg())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hc.Run(ctx); close(done) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel (goroutine leak)")
	}
}

// C-stress/concurrency — hundreds of backends, frequent polling, race-clean.
func TestWPC_StressManyBackends(t *testing.T) {
	r := NewRegistry()
	p := newScriptedProber()
	for i := 0; i < 300; i++ {
		id := fmt.Sprintf("b%d", i)
		if err := r.Add(Backend{ID: id, Addr: fmt.Sprintf("10.0.%d.%d:80", i/256, i%256)}); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			p.set(fmt.Sprintf("10.0.%d.%d:80", i/256, i%256), false) // a third unhealthy
		}
	}
	hc := newHC(r, p, fastCfg())
	for pass := 0; pass < 3; pass++ {
		hc.CheckOnce(context.Background())
	}
	// every unhealthy-scripted backend (>=2 passes of failure) should be Unhealthy
	unhealthy := 0
	for _, b := range r.Snapshot() {
		if b.State == StateUnhealthy {
			unhealthy++
		}
	}
	if unhealthy != 100 {
		t.Fatalf("want 100 unhealthy backends, got %d", unhealthy)
	}
}

// Concurrent CheckOnce calls are serialized safely (evalMu).
func TestWPC_ConcurrentCheckOnce(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 20; i++ {
		addBackend(t, r, fmt.Sprintf("b%d", i))
	}
	hc := newHC(r, newScriptedProber(), fastCfg())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); hc.CheckOnce(context.Background()) }()
	}
	wg.Wait()
}
