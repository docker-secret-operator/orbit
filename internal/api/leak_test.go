package api

import (
	"runtime"
	"testing"
	"time"
)

// Phase 3.0 (Production Reliability) leak detection: the rate limiter spawns a
// background cleanup goroutine (ratelimit.go: `go rl.cleanupLoop()`) that must
// be reaped by Close(). A proxy that created rate limiters without reaping them
// — or a Close() that failed to stop the loop — would leak one goroutine per
// limiter, growing unbounded over a long-running deployment host. This test
// churns many create/Close cycles and asserts the goroutine count returns to
// baseline.
func TestRateLimiterNoGoroutineLeak(t *testing.T) {
	base := settleGoroutines()

	for i := 0; i < 300; i++ {
		rl := NewRateLimiter(100)
		rl.Allow("10.0.0.1")
		rl.Allow("10.0.0.2")
		if err := rl.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}

	assertNoGoroutineLeak(t, base)
}

// settleGoroutines forces a GC and waits for transient goroutines to exit,
// returning a stable baseline count.
func settleGoroutines() int {
	runtime.GC()
	prev := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		time.Sleep(10 * time.Millisecond)
		runtime.GC()
		n := runtime.NumGoroutine()
		if n == prev {
			return n
		}
		prev = n
	}
	return prev
}

// assertNoGoroutineLeak polls (rather than sleeping a fixed amount) until the
// goroutine count returns to within a small tolerance of baseline, failing only
// if it never settles. A tolerance of a few goroutines absorbs runtime/test
// scheduler background goroutines without masking a real per-iteration leak
// (which, over 300 iterations, would be hundreds of extra goroutines).
func assertNoGoroutineLeak(t *testing.T, base int) {
	t.Helper()
	const tolerance = 3
	var last int
	for i := 0; i < 50; i++ {
		runtime.GC()
		last = runtime.NumGoroutine()
		if last <= base+tolerance {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: baseline=%d, after churn=%d (delta=%d, tolerance=%d)", base, last, last-base, tolerance)
}
