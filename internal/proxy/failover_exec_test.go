package proxy

import (
	"net"
	"sync"
	"testing"

	"github.com/docker-secret-operator/orbit/internal/metrics"
	"go.uber.org/zap"
)

// healthyBackend starts a TCP listener that accepts and immediately closes
// connections, returning its address.
func healthyBackend(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln.Addr().String()
}

// deadBackend returns an address that nothing is listening on (dial → refused).
func deadBackend(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func newTestServer(reg *Registry) (*Server, *Router, *metrics.Proxy) {
	m := metrics.New()
	router := NewRouter(reg)
	router.SetMetrics(m)
	reg.SetMetrics(m)
	srv := NewServer(zap.NewNop(), m)
	return srv, router, m
}

// B2 — single healthy backend: connects, no failover.
func TestWPB2_SingleHealthyNoRetry(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(Backend{ID: "a", Addr: healthyBackend(t)}); err != nil {
		t.Fatal(err)
	}
	srv, router, m := newTestServer(reg)
	up, b, err := srv.dialWithFailover(router)
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	up.Close()
	if b.ID != "a" {
		t.Fatalf("want backend a, got %s", b.ID)
	}
	if m.FailoverAttempts.Load() != 0 || m.FailoverSuccess.Load() != 0 {
		t.Fatalf("no failover expected: attempts=%d success=%d", m.FailoverAttempts.Load(), m.FailoverSuccess.Load())
	}
}

// B2 — single dead backend: exhausted, recorded.
func TestWPB2_SingleDeadExhausted(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(Backend{ID: "a", Addr: deadBackend(t)}); err != nil {
		t.Fatal(err)
	}
	srv, router, m := newTestServer(reg)
	_, _, err := srv.dialWithFailover(router)
	if err == nil {
		t.Fatal("expected exhaustion error")
	}
	if m.FailoverExhausted.Load() != 1 {
		t.Fatalf("exhausted metric want 1, got %d", m.FailoverExhausted.Load())
	}
	if reg.FailureCount("a") != 1 {
		t.Fatalf("registry must record the dial failure, got %d", reg.FailureCount("a"))
	}
}

// B2 — two backends: primary dead, failover to healthy.
func TestWPB2_TwoBackendFailover(t *testing.T) {
	reg := NewRegistry()
	// "a" sorts first → becomes the round-robin primary on the first call.
	if err := reg.Add(Backend{ID: "a", Addr: deadBackend(t)}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(Backend{ID: "b", Addr: healthyBackend(t)}); err != nil {
		t.Fatal(err)
	}
	srv, router, m := newTestServer(reg)
	up, b, err := srv.dialWithFailover(router)
	if err != nil {
		t.Fatalf("failover should succeed: %v", err)
	}
	up.Close()
	if b.ID != "b" {
		t.Fatalf("should have failed over to b, got %s", b.ID)
	}
	if m.FailoverAttempts.Load() != 1 || m.FailoverSuccess.Load() != 1 {
		t.Fatalf("attempts/success want 1/1, got %d/%d", m.FailoverAttempts.Load(), m.FailoverSuccess.Load())
	}
	if reg.FailureCount("a") != 1 {
		t.Fatalf("dead primary failure must be recorded, got %d", reg.FailureCount("a"))
	}
	if m.RetryLatencyCount.Load() != 1 {
		t.Fatal("retry latency sample must be recorded")
	}
}

// B2 — three backends, first two dead → third serves.
func TestWPB2_ThreeBackendFailover(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(Backend{ID: "a", Addr: deadBackend(t)}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(Backend{ID: "b", Addr: deadBackend(t)}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(Backend{ID: "c", Addr: healthyBackend(t)}); err != nil {
		t.Fatal(err)
	}
	srv, router, m := newTestServer(reg)
	srv.SetRetryPolicy(RetryPolicy{MaxRetries: 2}) // primary + 2 retries = all 3
	up, b, err := srv.dialWithFailover(router)
	if err != nil {
		t.Fatalf("failover should reach c: %v", err)
	}
	up.Close()
	if b.ID != "c" {
		t.Fatalf("want c, got %s", b.ID)
	}
	if m.FailoverAttempts.Load() != 2 {
		t.Fatalf("want 2 failover attempts, got %d", m.FailoverAttempts.Load())
	}
}

// B2 — retry budget bounds the attempts (MaxRetries=0 → primary only).
func TestWPB2_RetryBudgetRespected(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(Backend{ID: "a", Addr: deadBackend(t)}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(Backend{ID: "b", Addr: healthyBackend(t)}); err != nil {
		t.Fatal(err)
	}
	srv, router, m := newTestServer(reg)
	srv.SetRetryPolicy(RetryPolicy{MaxRetries: 0}) // no failover
	_, _, err := srv.dialWithFailover(router)
	if err == nil {
		t.Fatal("with 0 retries and a dead primary, expected failure")
	}
	if m.FailoverAttempts.Load() != 0 {
		t.Fatalf("budget 0 must make no failover attempts, got %d", m.FailoverAttempts.Load())
	}
}

// B2.3 — never route to Unhealthy/Draining/Failed backends.
func TestWPB2_ExcludesNonActive(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(Backend{ID: "healthy", Addr: healthyBackend(t)}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(Backend{ID: "sick", Addr: healthyBackend(t)}); err != nil {
		t.Fatal(err)
	}
	_ = reg.SetState("sick", StateUnhealthy)
	srv, router, _ := newTestServer(reg)
	for i := 0; i < 10; i++ {
		up, b, err := srv.dialWithFailover(router)
		if err != nil {
			t.Fatal(err)
		}
		up.Close()
		if b.ID != "healthy" {
			t.Fatalf("must never route to non-active backend, got %s", b.ID)
		}
	}
}

// B2 — isRetryableDialError classifies transport failures.
func TestWPB2_DialErrorClassification(t *testing.T) {
	if isRetryableDialError(nil) {
		t.Fatal("nil error is not retryable")
	}
	_, err := net.Dial("tcp", deadBackend(t))
	if err == nil {
		t.Skip("expected a dial error")
	}
	if !isRetryableDialError(err) {
		t.Fatalf("a transport dial error must be retryable: %v", err)
	}
}

// B2/perf — concurrent failover is race-free.
func TestWPB2_ConcurrentFailover(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(Backend{ID: "a", Addr: deadBackend(t)}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(Backend{ID: "b", Addr: healthyBackend(t)}); err != nil {
		t.Fatal(err)
	}
	srv, router, _ := newTestServer(reg)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			up, _, err := srv.dialWithFailover(router)
			if err == nil {
				up.Close()
			}
		}()
	}
	wg.Wait()
}
