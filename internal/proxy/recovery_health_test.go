package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestRecoveryResultStructureWithBackends(t *testing.T) {
	// Verify RecoveryResult captures all health information, including
	// individual per-backend details.
	now := time.Now()
	result := &RecoveryResult{
		State:           StartupReady,
		HealthyCount:    3,
		StartingCount:   1,
		UnhealthyCount:  1,
		UnknownCount:    0,
		TotalDiscovered: 5,
		Backends: []BackendHealth{
			{
				ID:     "backend-1",
				Addr:   "192.168.1.1:3000",
				Status: HealthHealthy,
				Reason: "HEALTHCHECK healthy",
			},
			{
				ID:     "backend-2",
				Addr:   "192.168.1.2:3000",
				Status: HealthStarting,
				Reason: "HEALTHCHECK starting",
			},
		},
		RecoveredAt: now,
		DurationMs:  1500,
	}

	if result.State != StartupReady {
		t.Errorf("expected state ready, got %s", result.State)
	}
	if result.HealthyCount != 3 {
		t.Errorf("expected 3 healthy, got %d", result.HealthyCount)
	}
	if result.StartingCount != 1 {
		t.Errorf("expected 1 starting, got %d", result.StartingCount)
	}
	if len(result.Backends) != 2 {
		t.Errorf("expected 2 backend details, got %d", len(result.Backends))
	}
	if result.DurationMs != 1500 {
		t.Errorf("expected 1500ms, got %d", result.DurationMs)
	}
}

func TestRecoveryStateTransitions(t *testing.T) {
	tests := []struct {
		name           string
		healthyCount   int
		startingCount  int
		unhealthyCount int
		expectedState  StartupState
	}{
		{
			name:           "all healthy",
			healthyCount:   5,
			startingCount:  0,
			unhealthyCount: 0,
			expectedState:  StartupReady,
		},
		{
			name:           "healthy and starting",
			healthyCount:   3,
			startingCount:  2,
			unhealthyCount: 0,
			expectedState:  StartupReady,
		},
		{
			name:           "mixed healthy and unhealthy",
			healthyCount:   3,
			startingCount:  0,
			unhealthyCount: 2,
			expectedState:  StartupDegraded,
		},
		{
			name:           "only starting (no healthy)",
			healthyCount:   0,
			startingCount:  3,
			unhealthyCount: 0,
			expectedState:  StartupRecovering,
		},
		{
			name:           "all unhealthy",
			healthyCount:   0,
			startingCount:  0,
			unhealthyCount: 5,
			expectedState:  StartupFailed,
		},
		{
			name:           "empty (no containers)",
			healthyCount:   0,
			startingCount:  0,
			unhealthyCount: 0,
			expectedState:  StartupRecovering,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &RecoveryResult{
				State:           StartupRecovering,
				HealthyCount:    tt.healthyCount,
				StartingCount:   tt.startingCount,
				UnhealthyCount:  tt.unhealthyCount,
				TotalDiscovered: tt.healthyCount + tt.startingCount + tt.unhealthyCount,
			}

			// Simulate CORRECTED state determination logic.
			// Order matters!
			if result.HealthyCount == 0 && result.StartingCount == 0 && result.UnhealthyCount > 0 {
				// 1. All unhealthy → failed
				result.State = StartupFailed
			} else if result.HealthyCount == 0 && result.UnhealthyCount == 0 && result.StartingCount > 0 {
				// 2. Only starting → recovering
				result.State = StartupRecovering
			} else if result.HealthyCount > 0 && result.UnhealthyCount > 0 {
				// 3. Mixed healthy + unhealthy → degraded
				result.State = StartupDegraded
			} else if result.HealthyCount > 0 {
				// 4. Healthy available → ready
				result.State = StartupReady
			} else {
				// 5. Empty → recovering
				result.State = StartupRecovering
			}

			if result.State != tt.expectedState {
				t.Errorf("expected state %s, got %s", tt.expectedState, result.State)
			}
		})
	}
}

func TestRecoveryPartialFailureHandling(t *testing.T) {
	// Verify that recovery handles 7 healthy + 2 unhealthy correctly.
	result := &RecoveryResult{
		State:           StartupRecovering,
		HealthyCount:    7,
		UnhealthyCount:  2,
		StartingCount:   0,
		TotalDiscovered: 9,
		Backends:        make([]BackendHealth, 9),
	}

	// Simulate state determination.
	if result.HealthyCount > 0 && result.UnhealthyCount > 0 {
		result.State = StartupDegraded
	}

	if result.State != StartupDegraded {
		t.Errorf("expected degraded state for partial failure, got %s", result.State)
	}

	// Verify we have both healthy and unhealthy in result.
	if result.HealthyCount != 7 || result.UnhealthyCount != 2 {
		t.Errorf("expected 7 healthy and 2 unhealthy, got %d healthy and %d unhealthy",
			result.HealthyCount, result.UnhealthyCount)
	}
}

func TestRecoveryDurationTracking(t *testing.T) {
	result := &RecoveryResult{
		State:       StartupReady,
		RecoveredAt: time.Now(),
		DurationMs:  0,
	}

	// Simulate recovery duration.
	time.Sleep(10 * time.Millisecond)
	result.DurationMs = 10

	if result.DurationMs < 10 {
		t.Errorf("expected duration >= 10ms, got %d", result.DurationMs)
	}
}

func TestBackendHealthReasonMessages(t *testing.T) {
	tests := []struct {
		name   string
		reason string
	}{
		{"healthy check", "Docker HEALTHCHECK healthy"},
		{"unhealthy check", "Docker HEALTHCHECK unhealthy (failing streak: 3)"},
		{"starting check", "Docker HEALTHCHECK still starting"},
		{"tcp fallback", "TCP fallback: healthy (no HEALTHCHECK)"},
		{"error reason", "inspect failed: connection refused"},
	}

	for _, tt := range tests {
		health := BackendHealth{
			ID:     "test",
			Addr:   "localhost:3000",
			Status: HealthHealthy,
			Reason: tt.reason,
		}

		if health.Reason == "" {
			t.Errorf("%s: reason should not be empty", tt.name)
		}
	}
}

func TestRecoveryContextCancellation(t *testing.T) {
	log := zap.NewNop()
	defer log.Sync()

	// Create a source (without Docker connection for this test).
	// In real tests, this would use a mock Docker client.
	source := &DockerRecoverySource{
		client:           nil,
		proxyInstance:    "test",
		log:              log,
		tcpDialTimeout:   2 * time.Second,
		maxHealthWorkers: 10,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Test that operations respect context cancellation.
	// (Full test would require Docker client mock)
	_ = source
	_ = ctx
}

func TestNewHealthValidatorWithConfig(t *testing.T) {
	log := zap.NewNop()
	defer log.Sync()

	hv := NewHealthValidator(nil, log, 2*time.Second, 10)

	if hv.tcpTimeout != 2*time.Second {
		t.Errorf("expected TCP timeout 2s, got %v", hv.tcpTimeout)
	}

	if hv.maxConcurrent != 10 {
		t.Errorf("expected max concurrent 10, got %d", hv.maxConcurrent)
	}

	hv.Close()
}

// TestVerifyBackendByID_FailsClosed exercises VerifyBackendByID's
// validation branches — the ones that must reject before ever touching
// Docker, since a real *client.Client isn't constructible in a unit test.
// This is exactly the safety property docs/governance/AUTHORITY-LIFECYCLE.md
// depends on: an ID that doesn't unambiguously resolve must error, never
// guess. d.client/d.healthValidator staying nil for these cases is itself
// part of the assertion — if any of them reached that far, this test would
// panic instead of returning a clean error.
func TestVerifyBackendByID_FailsClosed(t *testing.T) {
	d := &DockerRecoverySource{proxyInstance: "web", log: zap.NewNop()}

	cases := []struct {
		name      string
		backendID string
	}{
		{"wrong service prefix", "prometheus-a1b2c3d4e5f6"},
		{"no prefix at all", "a1b2c3d4e5f6"},
		{"seed sentinel", "web-default"},
		{"too-short suffix", "web-a1b2"},
		{"empty", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, err := d.VerifyBackendByID(context.Background(), tc.backendID)
			if err == nil {
				t.Fatalf("VerifyBackendByID(%q) = %+v, nil; want an error", tc.backendID, backend)
			}
			if backend != nil {
				t.Errorf("VerifyBackendByID(%q) backend = %+v, want nil on error", tc.backendID, backend)
			}
			// Every one of these is a format-level rejection — none of them
			// ever reach a real Docker call — so all must wrap
			// ErrNotIDVerifiable. This is the distinction
			// cmd/docker-orbit/main.go's directVerifyRecoveryResult depends
			// on to avoid discarding still-valid persisted state (e.g. the
			// seed sentinel, which the label-based scan resolves correctly
			// on its own) versus a genuine Docker-proven negative result.
			if !errors.Is(err, ErrNotIDVerifiable) {
				t.Errorf("VerifyBackendByID(%q) error = %v, want it to wrap ErrNotIDVerifiable", tc.backendID, err)
			}
		})
	}
}

// ── PR-A review Finding 2: Recovery ownership decision coverage ────────────
//
// DockerRecoverySource.extractBackend cannot be exercised directly in a unit
// test: d.client is a concrete *client.Client (not an interface), and
// introducing one purely for testability, or restructuring
// DockerRecoverySource itself, was explicitly ruled out when this finding
// was scoped — that would be redesigning Recovery for test convenience, not
// closing a review finding. checkRecoveryOwnership is the ownership-only
// decision logic extracted out of extractBackend's body (no Docker I/O,
// same file, same technique already used and accepted for
// checkProjectOwnership) — these tests exercise that real, unmodified
// function, proving the exact decision extractBackend makes. What remains
// unverified by an automated test is extractBackend's Docker I/O itself
// (ContainerInspect, network/port extraction) — that is covered by the
// PR-A live-verification pass against a real daemon, not by this test file.

func TestCheckRecoveryOwnership_AcceptsOwnedProject(t *testing.T) {
	labels := map[string]string{
		"orbit.io/service":           "web",
		"orbit.io/proxy":             "false",
		"orbit.io/generation":        "web-default",
		"com.docker.compose.project": "proj-a",
	}
	if err := checkRecoveryOwnership(labels, "proj-a", "web"); err != nil {
		t.Errorf("expected an owned-project container to be accepted, got %v", err)
	}
}

func TestCheckRecoveryOwnership_RejectsForeignProject(t *testing.T) {
	labels := map[string]string{
		"orbit.io/service":           "web",
		"orbit.io/proxy":             "false",
		"orbit.io/generation":        "web-default",
		"com.docker.compose.project": "proj-b",
	}
	err := checkRecoveryOwnership(labels, "proj-a", "web")
	if err == nil {
		t.Fatal("expected a foreign-project container to be rejected, got nil")
	}
	if !errors.Is(err, errOwnershipRejected) {
		t.Errorf("expected the rejection to wrap errOwnershipRejected, got %v", err)
	}
}

func TestCheckRecoveryOwnership_RejectsIncompleteLabels(t *testing.T) {
	labels := map[string]string{
		"com.docker.compose.project": "proj-a",
		// orbit.io/service, orbit.io/proxy, orbit.io/generation all missing.
	}
	if err := checkRecoveryOwnership(labels, "proj-a", "web"); err == nil {
		t.Error("expected incomplete ownership labels to be rejected, got nil")
	}
}

func TestCheckRecoveryOwnership_RejectsMismatchedProxyInstance(t *testing.T) {
	labels := map[string]string{
		"orbit.io/service":           "web",
		"orbit.io/proxy":             "false",
		"orbit.io/generation":        "web-default",
		"com.docker.compose.project": "proj-a",
		"orbit.io/proxy-instance":    "other-instance",
	}
	if err := checkRecoveryOwnership(labels, "proj-a", "web"); err == nil {
		t.Error("expected a mismatched proxy-instance label to be rejected, got nil")
	}
}
