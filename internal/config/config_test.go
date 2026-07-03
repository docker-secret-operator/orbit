package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadProxyConfigDefaults(t *testing.T) {
	// Setup: clear env and set only required var
	t.Helper()
	oldEnv := os.Environ()
	os.Clearenv()
	defer func() {
		os.Clearenv()
		for _, e := range oldEnv {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				os.Setenv(parts[0], parts[1])
			}
		}
	}()

	os.Setenv("ORBIT_BINDS", "3000:3000")
	os.Setenv("ORBIT_STATE_DIR", t.TempDir())

	cfg, err := LoadProxyConfig()
	if err != nil {
		t.Fatalf("LoadProxyConfig failed: %v", err)
	}

	if cfg.ControlPort != "9900" {
		t.Errorf("expected control port 9900, got %s", cfg.ControlPort)
	}
	if cfg.RateLimitPerSec != 100 {
		t.Errorf("expected rate limit 100, got %d", cfg.RateLimitPerSec)
	}
	if cfg.DrainTimeout != 30*time.Second {
		t.Errorf("expected drain timeout 30s, got %v", cfg.DrainTimeout)
	}
	if cfg.ProxyInstance != "default" {
		t.Errorf("expected proxy instance default, got %s", cfg.ProxyInstance)
	}
}

func TestLoadProxyConfigValidateDrainTimeout(t *testing.T) {
	t.Helper()
	os.Clearenv()
	os.Setenv("ORBIT_BINDS", "3000:3000")
	os.Setenv("ORBIT_DRAIN_TIMEOUT", "invalid")

	_, err := LoadProxyConfig()
	if err == nil {
		t.Fatal("expected error for invalid drain timeout")
	}
}

func TestLoadProxyConfigDrainTimeoutRange(t *testing.T) {
	t.Helper()
	os.Clearenv()
	os.Setenv("ORBIT_BINDS", "3000:3000")
	os.Setenv("ORBIT_DRAIN_TIMEOUT", "10m")

	_, err := LoadProxyConfig()
	if err == nil {
		t.Fatal("expected error for timeout too large")
	}
}

func TestLoadProxyConfigValidatePorts(t *testing.T) {
	t.Helper()
	os.Clearenv()
	os.Setenv("ORBIT_BINDS", "0:3000")

	_, err := LoadProxyConfig()
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func TestLoadProxyConfigMissingBinds(t *testing.T) {
	t.Helper()
	os.Clearenv()

	_, err := LoadProxyConfig()
	if err == nil {
		t.Fatal("expected error when ORBIT_BINDS missing")
	}
}

func TestLoadProxyConfigMultipleBinds(t *testing.T) {
	t.Helper()
	os.Clearenv()
	os.Setenv("ORBIT_BINDS", "3000:3000,3001:3001,3002:3002")
	os.Setenv("ORBIT_STATE_DIR", t.TempDir())

	cfg, err := LoadProxyConfig()
	if err != nil {
		t.Fatalf("LoadProxyConfig failed: %v", err)
	}

	if len(cfg.Binds) != 3 {
		t.Errorf("expected 3 bindings, got %d", len(cfg.Binds))
	}
}

func TestLoadProxyConfigRateLimit(t *testing.T) {
	t.Helper()
	os.Clearenv()
	os.Setenv("ORBIT_BINDS", "3000:3000")
	os.Setenv("ORBIT_RATE_LIMIT", "500")
	os.Setenv("ORBIT_STATE_DIR", t.TempDir())

	cfg, err := LoadProxyConfig()
	if err != nil {
		t.Fatalf("LoadProxyConfig failed: %v", err)
	}

	if cfg.RateLimitPerSec != 500 {
		t.Errorf("expected rate limit 500, got %d", cfg.RateLimitPerSec)
	}
}

func TestLoadProxyConfigAPIToken(t *testing.T) {
	t.Helper()
	os.Clearenv()
	os.Setenv("ORBIT_BINDS", "3000:3000")
	os.Setenv("ORBIT_API_TOKEN", "secret-token-123")
	os.Setenv("ORBIT_STATE_DIR", t.TempDir())

	cfg, err := LoadProxyConfig()
	if err != nil {
		t.Fatalf("LoadProxyConfig failed: %v", err)
	}

	if cfg.APIToken != "secret-token-123" {
		t.Errorf("expected token secret-token-123, got %s", cfg.APIToken)
	}
}
