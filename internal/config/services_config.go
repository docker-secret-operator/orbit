package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// DefaultServicesConfigPath is where a shared proxy's services.json is
// mounted (ADR-0006 § Control API: Service Dimension, "Service-list
// configuration"). Overridable via ORBIT_SERVICES_CONFIG for tests and
// non-default mount points.
const DefaultServicesConfigPath = "/etc/orbit/services.json"

// ServicesConfig is the shared proxy's declarative, multi-service
// configuration (ADR-0006 Stage 3.5). It replaces the implicit
// single-service model (ProxyConfig.ProxyInstance + ProxyConfig.Binds)
// with an explicit list of services and the ports the proxy binds on
// each one's behalf.
//
// ServicesConfig carries no runtime state — no health, authority,
// rollout, or backend data. It is read once at startup and never
// mutated afterward; runtime state lives in proxy.ProjectRegistry and
// the types it wraps, constructed FROM this configuration, never inside
// it.
type ServicesConfig struct {
	Services []ServiceConfig `json:"services"`
}

// ServiceConfig identifies one proxied service and the ports the shared
// proxy binds on its behalf — only the information required to identify
// the service, per ADR-0006 Stage 3.5's minimal-schema requirement.
type ServiceConfig struct {
	Name  string        `json:"name"`
	Binds []PortBinding `json:"binds"`
}

// serviceNamePattern mirrors the constraint Docker Compose already places
// on service names (and, transitively, the orbit.io/service label value
// DockerRecoverySource matches against) — not a new constraint invented
// here.
var serviceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// LoadServicesConfig reads and parses a services.json file. It performs no
// validation beyond well-formedness — call Validate separately. The loader
// never inspects Docker, never constructs a Registry, and never makes a
// health or recovery decision; it only produces a configuration object.
func LoadServicesConfig(path string) (*ServicesConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read services config %q: %w", path, err)
	}
	var sc ServicesConfig
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("parse services config %q: %w", path, err)
	}
	return &sc, nil
}

// Validate checks sc for the errors that would make it unsafe or
// ambiguous to wire into a running proxy: duplicate service names,
// duplicate listen ports (one process owns one port namespace regardless
// of how many services it fronts), invalid names, and missing required
// fields. It never silently repairs anything — callers must fix the
// configuration and reload. Returns the first error found; deterministic
// for a given input (services are checked in declaration order).
func (sc *ServicesConfig) Validate() error {
	if sc == nil || len(sc.Services) == 0 {
		return fmt.Errorf("services config must declare at least one service")
	}

	seenNames := make(map[string]bool, len(sc.Services))
	seenPorts := make(map[int]string, len(sc.Services))

	for i, svc := range sc.Services {
		if svc.Name == "" {
			return fmt.Errorf("service[%d]: name is required", i)
		}
		if !serviceNamePattern.MatchString(svc.Name) {
			return fmt.Errorf("service[%d]: invalid name %q (must start with a letter or digit, and contain only letters, digits, '-', '_', '.')", i, svc.Name)
		}
		if seenNames[svc.Name] {
			return fmt.Errorf("duplicate service name %q", svc.Name)
		}
		seenNames[svc.Name] = true

		if len(svc.Binds) == 0 {
			return fmt.Errorf("service %q: at least one bind is required", svc.Name)
		}
		for _, b := range svc.Binds {
			if b.ListenPort < 1 || b.ListenPort > 65535 {
				return fmt.Errorf("service %q: listen port %d out of range 1-65535", svc.Name, b.ListenPort)
			}
			if b.TargetPort < 1 || b.TargetPort > 65535 {
				return fmt.Errorf("service %q: target port %d out of range 1-65535", svc.Name, b.TargetPort)
			}
			if owner, ok := seenPorts[b.ListenPort]; ok {
				return fmt.Errorf("listen port %d is claimed by both %q and %q", b.ListenPort, owner, svc.Name)
			}
			seenPorts[b.ListenPort] = svc.Name
		}
	}
	return nil
}

// ResolveServicesConfig produces the ServicesConfig runProxy wires into
// ProjectRegistry. If a services.json file exists at the path named by
// ORBIT_SERVICES_CONFIG (or DefaultServicesConfigPath if that env var is
// unset), it is loaded and validated. Otherwise, ServicesConfig is
// synthesized from cfg's existing single-service fields (ProxyInstance,
// Binds) — exactly the configuration a pre-Stage-3.5 proxy already runs
// with — so existing single-service deployments start identically
// without requiring a services.json file to exist at all.
func ResolveServicesConfig(cfg *ProxyConfig) (*ServicesConfig, error) {
	path := getEnvOrDefault("ORBIT_SERVICES_CONFIG", DefaultServicesConfigPath)

	if _, statErr := os.Stat(path); statErr == nil {
		sc, err := LoadServicesConfig(path)
		if err != nil {
			return nil, err
		}
		if err := sc.Validate(); err != nil {
			return nil, fmt.Errorf("services config %q: %w", path, err)
		}
		return sc, nil
	} else if !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("stat services config %q: %w", path, statErr)
	}

	sc := &ServicesConfig{
		Services: []ServiceConfig{
			{Name: cfg.ProxyInstance, Binds: cfg.Binds},
		},
	}
	if err := sc.Validate(); err != nil {
		return nil, fmt.Errorf("synthesized single-service config: %w", err)
	}
	return sc, nil
}
