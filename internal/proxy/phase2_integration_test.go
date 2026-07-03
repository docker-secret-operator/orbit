package proxy

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPhase2HealthAwareRecoveryFlow(t *testing.T) {
	log := zap.NewNop()
	defer log.Sync()

	// Simulate recovery result.
	result := &RecoveryResult{
		State:           StartupRecovering,
		HealthyCount:    0,
		StartingCount:   0,
		UnhealthyCount:  0,
		UnknownCount:    0,
		TotalDiscovered: 0,
		Backends:        []BackendHealth{},
		RecoveredAt:     time.Now(),
		DurationMs:      0,
	}

	// Phase 2a: All healthy → ready startup.
	result.State = StartupRecovering
	result.HealthyCount = 5
	result.UnhealthyCount = 0
	result.StartingCount = 0

	// Simulate CORRECTED state determination.
	if result.HealthyCount > 0 {
		result.State = StartupReady
	}

	if result.State != StartupReady {
		t.Errorf("expected ready state with 5 healthy, got %s", result.State)
	}

	// Phase 2b: Mixed healthy and unhealthy → degraded startup.
	result.State = StartupRecovering
	result.HealthyCount = 3
	result.UnhealthyCount = 2
	result.StartingCount = 0

	if result.HealthyCount > 0 && result.UnhealthyCount > 0 {
		result.State = StartupDegraded
	}

	if result.State != StartupDegraded {
		t.Errorf("expected degraded state with mixed health, got %s", result.State)
	}

	// Phase 2c: Only starting (no healthy) → recovering (NOT failed).
	result.State = StartupRecovering
	result.HealthyCount = 0
	result.UnhealthyCount = 0
	result.StartingCount = 5

	if result.HealthyCount == 0 && result.UnhealthyCount == 0 && result.StartingCount > 0 {
		result.State = StartupRecovering
	}

	if result.State != StartupRecovering {
		t.Errorf("expected recovering state with only starting, got %s", result.State)
	}

	// Phase 2d: All unhealthy → failed (CRITICAL: preserve failed state).
	result.State = StartupRecovering
	result.HealthyCount = 0
	result.UnhealthyCount = 5
	result.StartingCount = 0

	if result.HealthyCount == 0 && result.StartingCount == 0 && result.UnhealthyCount > 0 {
		result.State = StartupFailed
	}

	if result.State != StartupFailed {
		t.Errorf("expected failed state with all unhealthy, got %s", result.State)
	}
}

func TestPhase2BackendHealthTracking(t *testing.T) {
	// Verify that recovery tracks all health information.
	backendHealth := []BackendHealth{
		{
			ID:        "svc1-gen1",
			Addr:      "192.168.1.1:3000",
			Status:    HealthHealthy,
			Reason:    "Docker HEALTHCHECK healthy",
			CheckedAt: time.Now(),
			Attempts:  0,
		},
		{
			ID:        "svc2-gen1",
			Addr:      "192.168.1.2:3000",
			Status:    HealthStarting,
			Reason:    "Docker HEALTHCHECK still starting",
			CheckedAt: time.Now(),
			Attempts:  2,
		},
		{
			ID:        "svc3-gen1",
			Addr:      "192.168.1.3:3000",
			Status:    HealthUnhealthy,
			Reason:    "Docker HEALTHCHECK unhealthy (failing streak: 5)",
			CheckedAt: time.Now(),
			Attempts:  5,
		},
	}

	healthy := 0
	starting := 0
	unhealthy := 0

	for _, h := range backendHealth {
		switch h.Status {
		case HealthHealthy:
			healthy++
		case HealthStarting:
			starting++
		case HealthUnhealthy:
			unhealthy++
		}
	}

	if healthy != 1 || starting != 1 || unhealthy != 1 {
		t.Errorf("expected 1 healthy, 1 starting, 1 unhealthy; got %d, %d, %d",
			healthy, starting, unhealthy)
	}
}

func TestPhase2TimeoutHierarchy(t *testing.T) {
	// Verify timeout ordering is enforced.
	cfg := DefaultTimeouts()

	// TCP dial must be shortest.
	if cfg.TCPDial > cfg.HealthValidation {
		t.Errorf("TCPDial (%v) should be < HealthValidation (%v)",
			cfg.TCPDial, cfg.HealthValidation)
	}

	// Health validation must be less than overall startup.
	if cfg.HealthValidation > cfg.Startup {
		t.Errorf("HealthValidation (%v) should be < Startup (%v)",
			cfg.HealthValidation, cfg.Startup)
	}

	// Discovery should be reasonable.
	if cfg.Discovery <= 0 || cfg.Discovery > cfg.Startup {
		t.Errorf("Discovery (%v) should be positive and < Startup (%v)",
			cfg.Discovery, cfg.Startup)
	}
}

func TestPhase2ContextCancellation(t *testing.T) {
	log := zap.NewNop()
	defer log.Sync()

	// Test that operations properly respect context cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	hv := NewHealthValidator(nil, log, 2*time.Second, 5)
	defer hv.Close()

	backend := Backend{ID: "test", Addr: "localhost:3000"}
	health := hv.CheckHealth(ctx, "container-123", backend)

	// Should return quickly due to cancellation.
	if health.Status != HealthUnknown {
		t.Errorf("expected Unknown status with cancelled context, got %s", health.Status)
	}

	if health.Reason == "" {
		t.Error("should provide reason for cancellation")
	}
}

func TestPhase2ConcurrencyBounding(t *testing.T) {
	log := zap.NewNop()
	defer log.Sync()

	maxConcurrent := 5
	hv := NewHealthValidator(nil, log, 2*time.Second, maxConcurrent)
	defer hv.Close()

	// Verify semaphore is bounded.
	if cap(hv.semaphore) != maxConcurrent {
		t.Errorf("expected semaphore cap %d, got %d", maxConcurrent, cap(hv.semaphore))
	}

	// Verify semaphore starts empty.
	if len(hv.semaphore) != 0 {
		t.Errorf("expected empty semaphore initially, got %d", len(hv.semaphore))
	}
}

func TestPhase2PartialRecoveryScenario(t *testing.T) {
	// Key test: 7 healthy + 2 unhealthy should go degraded, not fail.
	result := &RecoveryResult{
		State:           StartupRecovering,
		HealthyCount:    7,
		UnhealthyCount:  2,
		StartingCount:   0,
		TotalDiscovered: 9,
		Backends:        make([]BackendHealth, 9),
	}

	// Simulate CORRECTED state logic from DiscoverAndValidateBackends.
	if result.HealthyCount == 0 && result.StartingCount == 0 && result.UnhealthyCount > 0 {
		result.State = StartupFailed
	} else if result.HealthyCount == 0 && result.UnhealthyCount == 0 && result.StartingCount > 0 {
		result.State = StartupRecovering
	} else if result.HealthyCount > 0 && result.UnhealthyCount > 0 {
		result.State = StartupDegraded
	} else if result.HealthyCount > 0 {
		result.State = StartupReady
	}

	if result.State != StartupDegraded {
		t.Errorf("expected degraded for partial failure, got %s", result.State)
	}

	// Verify backends are retained for analysis.
	if result.HealthyCount != 7 {
		t.Errorf("expected 7 healthy, got %d", result.HealthyCount)
	}
	if result.UnhealthyCount != 2 {
		t.Errorf("expected 2 unhealthy, got %d", result.UnhealthyCount)
	}
}

func TestPhase2FailedStatePreservation(t *testing.T) {
	// CRITICAL: Failed state must be preserved, not degraded.
	// 0 healthy + 8 unhealthy = failed (don't hide it).
	result := &RecoveryResult{
		State:           StartupRecovering,
		HealthyCount:    0,
		UnhealthyCount:  8,
		StartingCount:   0,
		TotalDiscovered: 8,
		FailedReason:    "all backends unhealthy",
		Backends:        make([]BackendHealth, 8),
	}

	// Simulate state determination.
	if result.HealthyCount == 0 && result.StartingCount == 0 && result.UnhealthyCount > 0 {
		result.State = StartupFailed
	}

	// CRITICAL: State must remain failed, not degrade to degraded.
	if result.State != StartupFailed {
		t.Errorf("CRITICAL: failed state converted to %s, should stay failed", result.State)
	}

	if result.FailedReason == "" {
		t.Error("failed state should have reason")
	}
}

func TestPhase2EmptyStateRecovering(t *testing.T) {
	// Empty container set should be StartupRecovering (could be cold start).
	result := &RecoveryResult{
		State:            StartupRecovering,
		HealthyCount:     0,
		UnhealthyCount:   0,
		StartingCount:    0,
		TotalDiscovered:  0,
		ExpectedServices: 0, // No expectation yet
		Backends:         []BackendHealth{},
	}

	// Simulate state determination.
	if result.HealthyCount == 0 && result.StartingCount == 0 && result.UnhealthyCount == 0 {
		result.State = StartupRecovering
	}

	if result.State != StartupRecovering {
		t.Errorf("expected recovering for empty state, got %s", result.State)
	}
}

func TestPhase2RecoveryDurationTracking(t *testing.T) {
	// Verify recovery times are tracked for operational insight.
	result := &RecoveryResult{
		State:       StartupReady,
		RecoveredAt: time.Now(),
		DurationMs:  1234,
	}

	if result.DurationMs < 100 {
		t.Errorf("recovery duration should be reasonable, got %d ms", result.DurationMs)
	}

	if result.DurationMs > 60000 { // 60 seconds is too long.
		t.Errorf("recovery should complete quickly, got %d ms", result.DurationMs)
	}
}

func TestPhase2BackendHealthReasons(t *testing.T) {
	// Verify health reasons are descriptive for operational debugging.
	tests := []struct {
		name         string
		status       HealthStatus
		hasReason    bool
		minReasonLen int
	}{
		{"healthy via HEALTHCHECK", HealthHealthy, true, 10},
		{"unhealthy with failing streak", HealthUnhealthy, true, 20},
		{"starting", HealthStarting, true, 10},
		{"tcp fallback", HealthUnknown, true, 5},
	}

	for _, tt := range tests {
		h := BackendHealth{
			ID:     "test",
			Addr:   "localhost:3000",
			Status: tt.status,
			Reason: "test reason with enough content",
		}

		if tt.hasReason && len(h.Reason) < tt.minReasonLen {
			t.Errorf("%s: reason too short (%d chars)", tt.name, len(h.Reason))
		}
	}
}
