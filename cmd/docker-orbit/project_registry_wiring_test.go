package main

import (
	"testing"

	"github.com/docker-secret-operator/orbit/internal/proxy"
)

// TestNewProjectRegistryForService is the Stage 3.1 test. runProxy itself
// cannot be exercised directly in a unit test (it binds real ports, starts
// an HTTP server, and blocks until a shutdown signal), so this proves the
// exact wiring logic runProxy calls: newProjectRegistryForService(cfg.ProxyInstance, reg).
func TestNewProjectRegistryForService(t *testing.T) {
	reg := proxy.NewRegistry()

	pr := newProjectRegistryForService("web", reg)

	got, ok := pr.For("web")
	if !ok {
		t.Fatal("expected the service to be registered under the given name")
	}
	if got != reg {
		t.Fatal("ProjectRegistry must hold the exact same *Registry instance passed in, not a copy — existing Registry ownership must be unchanged")
	}

	services := pr.Services()
	if len(services) != 1 {
		t.Fatalf("expected exactly 1 registered service (no duplicate registration), got %d: %v", len(services), services)
	}
}

// TestNewProjectRegistryForService_NameMatchesInput proves the registered
// name is exactly what was passed — in production, cfg.ProxyInstance —
// not a hardcoded or derived value.
func TestNewProjectRegistryForService_NameMatchesInput(t *testing.T) {
	reg := proxy.NewRegistry()

	pr := newProjectRegistryForService("cadvisor-verify", reg)

	if _, ok := pr.For("cadvisor-verify"); !ok {
		t.Error("registered service name must match the name argument exactly")
	}
	if _, ok := pr.For("web"); ok {
		t.Error("no other service name should resolve to anything")
	}
}
