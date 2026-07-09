// Package config provides centralized configuration management.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ProxyConfig holds all proxy configuration.
type ProxyConfig struct {
	// Port bindings: "8000:3000,8001:3001"
	Binds []PortBinding

	// Control plane
	ControlPort     string
	APIToken        string
	RateLimitPerSec int

	// Timeouts
	DrainTimeout time.Duration
	GraceTimeout time.Duration

	// Recovery
	RecoveryTimeout    time.Duration
	RecoveryMaxRetries int

	// Timeout hierarchy for recovery operations
	DaemonConnectTimeout    time.Duration
	DiscoveryTimeout        time.Duration
	HealthValidationTimeout time.Duration
	StartupTimeout          time.Duration
	TCPDialTimeout          time.Duration
	TransitionTimeout       time.Duration // Max time for authority transition (default: 5m)

	// Reconciliation (ADR-0006 Stage 4). Interval between periodic Docker
	// reconciliation passes — the safety net that corrects whatever the
	// event fast path missed (INV-4). Default 30s: a starting point per the
	// ADR, not a tuned value. Declared here now so the config surface is
	// frozen before the Stage 4 Reconciler that consumes it lands (PR 4.1).
	ReconcileInterval time.Duration

	// Proxy instance ID (for future multi-proxy)
	ProxyInstance string

	// State persistence
	StateDir string // Directory for state files (default: /var/lib/orbit)
}

// PortBinding represents a host:container port mapping.
type PortBinding struct {
	ListenPort int `json:"listen_port"`
	TargetPort int `json:"target_port"`
}

// LoadProxyConfig reads configuration from environment variables.
func LoadProxyConfig() (*ProxyConfig, error) {
	cfg := &ProxyConfig{
		ControlPort:             getEnvOrDefault("ORBIT_CONTROL_PORT", "9900"),
		APIToken:                os.Getenv("ORBIT_API_TOKEN"),
		RateLimitPerSec:         100,
		DrainTimeout:            30 * time.Second,
		GraceTimeout:            30 * time.Second,
		RecoveryTimeout:         30 * time.Second,
		RecoveryMaxRetries:      10,
		DaemonConnectTimeout:    5 * time.Second,
		DiscoveryTimeout:        10 * time.Second,
		HealthValidationTimeout: 5 * time.Second,
		StartupTimeout:          30 * time.Second,
		TCPDialTimeout:          2 * time.Second,
		TransitionTimeout:       5 * time.Minute,
		ReconcileInterval:       30 * time.Second,
		ProxyInstance:           getEnvOrDefault("ORBIT_PROXY_INSTANCE", "default"),
	}

	// Parse drain timeout with validation.
	if drainStr := os.Getenv("ORBIT_DRAIN_TIMEOUT"); drainStr != "" {
		d, err := time.ParseDuration(drainStr)
		if err != nil {
			return nil, fmt.Errorf("invalid ORBIT_DRAIN_TIMEOUT %q: %w", drainStr, err)
		}
		if d < 100*time.Millisecond || d > 5*time.Minute {
			return nil, fmt.Errorf("ORBIT_DRAIN_TIMEOUT must be 100ms-5m, got %v", d)
		}
		cfg.DrainTimeout = d
	}

	// Parse rate limit.
	if rpsStr := os.Getenv("ORBIT_RATE_LIMIT"); rpsStr != "" {
		rps, err := strconv.Atoi(rpsStr)
		if err != nil {
			return nil, fmt.Errorf("invalid ORBIT_RATE_LIMIT %q: %w", rpsStr, err)
		}
		if rps < 1 || rps > 10000 {
			return nil, fmt.Errorf("ORBIT_RATE_LIMIT must be 1-10000, got %d", rps)
		}
		cfg.RateLimitPerSec = rps
	}

	// Parse timeout hierarchy.
	timeoutVars := map[string]*time.Duration{
		"ORBIT_DAEMON_CONNECT_TIMEOUT":    &cfg.DaemonConnectTimeout,
		"ORBIT_DISCOVERY_TIMEOUT":         &cfg.DiscoveryTimeout,
		"ORBIT_HEALTH_VALIDATION_TIMEOUT": &cfg.HealthValidationTimeout,
		"ORBIT_STARTUP_TIMEOUT":           &cfg.StartupTimeout,
		"ORBIT_TCP_DIAL_TIMEOUT":          &cfg.TCPDialTimeout,
		"ORBIT_TRANSITION_TIMEOUT":        &cfg.TransitionTimeout,
		"ORBIT_RECONCILE_INTERVAL":        &cfg.ReconcileInterval,
	}

	for envVar, dest := range timeoutVars {
		if val := os.Getenv(envVar); val != "" {
			d, err := time.ParseDuration(val)
			if err != nil {
				return nil, fmt.Errorf("invalid %s %q: %w", envVar, val, err)
			}
			if d <= 0 {
				return nil, fmt.Errorf("%s must be positive, got %v", envVar, d)
			}
			*dest = d
		}
	}

	// Validate timeout hierarchy: TCP dial must be shortest.
	if cfg.TCPDialTimeout > cfg.HealthValidationTimeout {
		return nil, fmt.Errorf("TCPDialTimeout (%v) must be <= HealthValidationTimeout (%v)",
			cfg.TCPDialTimeout, cfg.HealthValidationTimeout)
	}

	// Port bindings: "8000:3000,8001:3001"
	if bindsStr := os.Getenv("ORBIT_BINDS"); bindsStr != "" {
		for _, spec := range strings.Split(bindsStr, ",") {
			spec = strings.TrimSpace(spec)
			if spec == "" {
				continue
			}
			parts := strings.SplitN(spec, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid ORBIT_BINDS spec %q: expected listenPort:targetPort", spec)
			}

			lp, err := strconv.Atoi(parts[0])
			if err != nil {
				return nil, fmt.Errorf("invalid listen port in ORBIT_BINDS %q: %w", spec, err)
			}
			tp, err := strconv.Atoi(parts[1])
			if err != nil {
				return nil, fmt.Errorf("invalid target port in ORBIT_BINDS %q: %w", spec, err)
			}

			if lp < 1 || lp > 65535 || tp < 1 || tp > 65535 {
				return nil, fmt.Errorf("ORBIT_BINDS ports must be 1-65535: %s", spec)
			}

			cfg.Binds = append(cfg.Binds, PortBinding{
				ListenPort: lp,
				TargetPort: tp,
			})
		}
	}

	if len(cfg.Binds) == 0 {
		return nil, fmt.Errorf("ORBIT_BINDS is required and must not be empty")
	}

	// State directory for persistent state files
	cfg.StateDir = getEnvOrDefault("ORBIT_STATE_DIR", "/var/lib/orbit")

	// Validate state directory exists and is accessible
	if err := validateStateDir(cfg.StateDir); err != nil {
		return nil, fmt.Errorf("invalid ORBIT_STATE_DIR %q: %w", cfg.StateDir, err)
	}

	return cfg, nil
}

func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// validateStateDir checks that the state directory exists and is writable.
func validateStateDir(stateDir string) error {
	// Check if directory exists
	info, err := os.Stat(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Try to create it
			if err := os.MkdirAll(stateDir, 0755); err != nil {
				return fmt.Errorf("directory does not exist and cannot be created: %w", err)
			}
			return nil
		}
		return fmt.Errorf("stat failed: %w", err)
	}

	// Check it's a directory
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}

	// Check writability by attempting to create a test file
	testFile := fmt.Sprintf("%s/.docker-rollout-write-test-%d", stateDir, os.Getpid())
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		return fmt.Errorf("directory is not writable: %w", err)
	}
	os.Remove(testFile) // Clean up test file

	return nil
}
