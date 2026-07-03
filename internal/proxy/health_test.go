package proxy

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestHealthValidatorBackendHealthStructure(t *testing.T) {
	// Verify BackendHealth captures all required fields.
	health := BackendHealth{
		ID:        "backend-1",
		Addr:      "192.168.1.1:3000",
		Status:    HealthHealthy,
		Reason:    "test",
		CheckedAt: time.Now(),
		Attempts:  1,
	}

	if health.ID != "backend-1" {
		t.Errorf("expected ID backend-1, got %s", health.ID)
	}
	if health.Status != HealthHealthy {
		t.Errorf("expected status healthy, got %s", health.Status)
	}
	if health.CheckedAt.IsZero() {
		t.Error("CheckedAt should not be zero")
	}
}

func TestHealthStatusConstants(t *testing.T) {
	tests := []struct {
		name   string
		status HealthStatus
	}{
		{"Healthy", HealthHealthy},
		{"Unhealthy", HealthUnhealthy},
		{"Starting", HealthStarting},
		{"Unknown", HealthUnknown},
		{"Degraded", HealthDegraded},
	}

	for _, tt := range tests {
		if tt.status == "" {
			t.Errorf("%s status is empty", tt.name)
		}
	}
}

func TestStartupStateConstants(t *testing.T) {
	tests := []struct {
		name  string
		state StartupState
	}{
		{"Starting", StartupStarting},
		{"Ready", StartupReady},
		{"Degraded", StartupDegraded},
		{"Failed", StartupFailed},
		{"Recovering", StartupRecovering},
	}

	for _, tt := range tests {
		if tt.state == "" {
			t.Errorf("%s state is empty", tt.name)
		}
	}
}

func TestTimeoutConfigDefaults(t *testing.T) {
	cfg := DefaultTimeouts()

	if cfg.DaemonConnect <= 0 {
		t.Error("DaemonConnect timeout must be positive")
	}
	if cfg.Discovery <= 0 {
		t.Error("Discovery timeout must be positive")
	}
	if cfg.HealthValidation <= 0 {
		t.Error("HealthValidation timeout must be positive")
	}
	if cfg.Startup <= 0 {
		t.Error("Startup timeout must be positive")
	}
	if cfg.TCPDial <= 0 {
		t.Error("TCPDial timeout must be positive")
	}

	// Verify timeout ordering.
	if cfg.TCPDial > cfg.HealthValidation {
		t.Errorf("TCPDial (%v) should be <= HealthValidation (%v)",
			cfg.TCPDial, cfg.HealthValidation)
	}
}

func TestNewHealthValidatorCreation(t *testing.T) {
	log := zap.NewNop()
	defer log.Sync()

	// Test that NewHealthValidator can be instantiated.
	// (Full testing requires Docker client mock, tested in integration tests)
	hv := NewHealthValidator(nil, log, 2*time.Second, 10)

	if hv.tcpTimeout != 2*time.Second {
		t.Errorf("expected tcpTimeout 2s, got %v", hv.tcpTimeout)
	}
	if hv.maxConcurrent != 10 {
		t.Errorf("expected maxConcurrent 10, got %d", hv.maxConcurrent)
	}
	if len(hv.semaphore) != 0 {
		t.Errorf("semaphore should be empty initially, got %d", len(hv.semaphore))
	}
	if cap(hv.semaphore) != 10 {
		t.Errorf("semaphore capacity should be 10, got %d", cap(hv.semaphore))
	}
}

func TestContextCancellationHandling(t *testing.T) {
	log := zap.NewNop()
	defer log.Sync()

	hv := NewHealthValidator(nil, log, 2*time.Second, 10)
	defer hv.Close()

	// Create cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	backend := Backend{ID: "test", Addr: "localhost:3000"}
	health := hv.CheckHealth(ctx, "container-123", backend)

	if health.Status != HealthUnknown {
		t.Errorf("expected Unknown status on cancelled context, got %s", health.Status)
	}
	if health.Reason == "" {
		t.Error("expected non-empty reason for cancelled context")
	}
}

func TestBatchCheckEmptyInput(t *testing.T) {
	log := zap.NewNop()
	defer log.Sync()

	hv := NewHealthValidator(nil, log, 2*time.Second, 10)
	defer hv.Close()

	ctx := context.Background()
	containers := make(map[string]Backend)

	results := hv.BatchCheck(ctx, containers)

	if len(results) != 0 {
		t.Errorf("expected 0 results for empty input, got %d", len(results))
	}
}

func TestBackendHealthErrorTracking(t *testing.T) {
	health := BackendHealth{
		ID:        "test",
		Addr:      "localhost:3000",
		Status:    HealthUnhealthy,
		LastErr:   context.Canceled,
		CheckedAt: time.Now(),
	}

	if health.LastErr != context.Canceled {
		t.Error("BackendHealth should track LastErr")
	}
}
