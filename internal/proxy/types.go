package proxy

import (
	"time"
)

// HealthStatus represents the health state of a backend.
type HealthStatus string

const (
	HealthHealthy   HealthStatus = "healthy"
	HealthUnhealthy HealthStatus = "unhealthy"
	HealthStarting  HealthStatus = "starting"
	HealthUnknown   HealthStatus = "unknown"
	HealthDegraded  HealthStatus = "degraded"
)

// StartupState represents the current startup phase of the proxy.
type StartupState string

const (
	StartupStarting   StartupState = "starting"
	StartupReady      StartupState = "ready"
	StartupDegraded   StartupState = "degraded"
	StartupFailed     StartupState = "failed"
	StartupRecovering StartupState = "recovering"
)

// BackendHealth represents health information for a single backend.
type BackendHealth struct {
	ID         string       // Backend ID
	Addr       string       // Backend address (IP:port)
	Generation string       // Generation label (from docker-compose x-docker-rollout)
	Status     HealthStatus // Current health status
	Reason     string       // Human-readable reason for status
	CheckedAt  time.Time    // When last checked
	Attempts   int          // Health check attempts made
	LastErr    error        // Last error if any
}

// RecoveryResult represents the outcome of a recovery operation.
type RecoveryResult struct {
	State            StartupState    // Current startup state
	HealthyCount     int             // Number of healthy backends
	StartingCount    int             // Number of starting backends
	UnhealthyCount   int             // Number of unhealthy backends
	UnknownCount     int             // Number of unknown status backends
	TotalDiscovered  int             // Total containers discovered
	Backends         []BackendHealth // Health details for all backends
	FailedReason     string          // Reason if startup failed
	ExpectedServices int             // Expected service/backend count (0 = not tracked)
	RecoveredAt      time.Time       // When recovery completed
	DurationMs       int64           // Total recovery duration milliseconds
}

// TimeoutConfig holds timeout configuration for recovery operations.
type TimeoutConfig struct {
	DaemonConnect    time.Duration // Docker daemon connection timeout
	Discovery        time.Duration // Container discovery timeout
	HealthValidation time.Duration // Individual health check timeout
	Startup          time.Duration // Overall startup timeout (gates MarkStartupComplete)
	TCPDial          time.Duration // TCP fallback dial timeout (should be short)
}

// DefaultTimeouts returns sensible defaults for recovery timeouts.
func DefaultTimeouts() TimeoutConfig {
	return TimeoutConfig{
		DaemonConnect:    5 * time.Second,
		Discovery:        10 * time.Second,
		HealthValidation: 5 * time.Second,
		Startup:          30 * time.Second,
		TCPDial:          2 * time.Second,
	}
}
