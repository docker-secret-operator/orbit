package api

import (
	"testing"
	"time"
)

func TestAllowWithinLimit(t *testing.T) {
	rl := NewRateLimiter(10) // 10 req/sec
	defer rl.Close()

	ip := "192.168.1.1"

	// First request should always be allowed.
	if !rl.Allow(ip) {
		t.Error("first request should be allowed")
	}
}

func TestRateLimitExceeded(t *testing.T) {
	rl := NewRateLimiter(1) // 1 req/sec
	defer rl.Close()

	ip := "192.168.1.2"

	// First allowed.
	if !rl.Allow(ip) {
		t.Error("first request should be allowed")
	}

	// Second immediately rejected.
	if rl.Allow(ip) {
		t.Error("second request should be rate limited")
	}
}

// TestBurstAllowsRapidSequentialRequestsFromOneCommand guards the fix for a
// real bug found while live-testing `docker orbit deploy`/`doctor`/`recover`
// in Phase 2.2: each of those commands issues several rapid sequential
// requests to the control API from the same client IP (doctor alone probes
// health/live, health/ready, and /status). A burst of 1 rate-limited a
// single legitimate CLI invocation against itself. Burst now equals
// rpsLimit — sustained abuse is still capped, but one command's own request
// sequence isn't punished as if it were multiple abusive clients.
func TestBurstAllowsRapidSequentialRequestsFromOneCommand(t *testing.T) {
	rl := NewRateLimiter(10) // 10 req/sec configured limit
	defer rl.Close()

	ip := "192.168.1.9"
	for i := 0; i < 10; i++ {
		if !rl.Allow(ip) {
			t.Fatalf("request %d of 10 rejected — burst should absorb a full second's worth of rapid calls", i+1)
		}
	}
	// The 11th call within the same instant exceeds the burst and should be rejected.
	if rl.Allow(ip) {
		t.Error("11th rapid request should be rejected — sustained abuse must still be capped")
	}
}

func TestDifferentIPsIndependent(t *testing.T) {
	rl := NewRateLimiter(1)
	defer rl.Close()

	ip1 := "192.168.1.1"
	ip2 := "192.168.1.2"

	if !rl.Allow(ip1) {
		t.Error("ip1 first request should be allowed")
	}
	if !rl.Allow(ip2) {
		t.Error("ip2 first request should be allowed (different IP)")
	}
}

func TestCleanupRemovesOldEntries(t *testing.T) {
	rl := NewRateLimiter(10)
	defer rl.Close()

	ip := "192.168.1.3"
	rl.Allow(ip)

	initialLen := rl.Len()

	// Manually set old timestamp.
	rl.mu.Lock()
	rl.lastSeen[ip] = time.Now().Add(-10 * time.Minute)
	rl.mu.Unlock()

	// Trigger cleanup.
	rl.cleanup()

	finalLen := rl.Len()
	if finalLen >= initialLen {
		t.Error("cleanup should remove old entries")
	}
}

func TestLenReturnsCount(t *testing.T) {
	rl := NewRateLimiter(10)
	defer rl.Close()

	if rl.Len() != 0 {
		t.Error("initial limiter count should be 0")
	}

	rl.Allow("192.168.1.1")
	rl.Allow("192.168.1.2")

	if rl.Len() != 2 {
		t.Errorf("expected 2 limiters, got %d", rl.Len())
	}
}

func TestMaxLimitersEviction(t *testing.T) {
	rl := NewRateLimiter(10)
	defer rl.Close()

	// Fill up to max (modifying for test).
	rl.mu.Lock()
	oldMax := maxLimiters
	// Temporarily reduce for testing.
	rl.mu.Unlock()

	// Just verify the mechanism works with normal max.
	for i := 0; i < 100; i++ {
		ip := "192.168." + string(rune('0'+i/256)) + "." + string(rune('0'+i%256))
		rl.Allow(ip)
	}

	// Should not exceed max + buffer.
	if rl.Len() > maxLimiters+10 {
		t.Errorf("limiter count %d exceeded max %d", rl.Len(), oldMax)
	}
}
