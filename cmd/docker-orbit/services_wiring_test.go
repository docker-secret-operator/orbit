package main

import (
	"testing"

	"github.com/docker-secret-operator/orbit/internal/config"
	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/proxy"
	"go.uber.org/zap"
)

// TestWireProjectRegistry_SingleService is the Stage 3.5 single-service
// compatibility test: a ServicesConfig with exactly one service (the shape
// config.ResolveServicesConfig synthesizes when no services.json exists)
// must wire correctly — wireProjectRegistry is now the sole ProjectRegistry
// construction path (Stage 3.1's now-removed newProjectRegistryForService
// helper covered exactly this single-service case before this stage).
func TestWireProjectRegistry_SingleService(t *testing.T) {
	srv := proxy.NewServer(zap.NewNop(), metrics.New())
	defer srv.Close()

	sc := &config.ServicesConfig{Services: []config.ServiceConfig{
		{Name: "web", Binds: []config.PortBinding{{ListenPort: 0, TargetPort: 3000}}},
	}}

	pr, reg, err := wireProjectRegistry(srv, metrics.New(), "web", sc, zap.NewNop())
	if err != nil {
		t.Fatalf("wireProjectRegistry: %v", err)
	}

	services := pr.Services()
	if len(services) != 1 || services[0] != "web" {
		t.Fatalf("expected exactly [\"web\"], got %v", services)
	}
	got, ok := pr.For("web")
	if !ok {
		t.Fatal("expected \"web\" to be registered in ProjectRegistry")
	}
	if got != reg {
		t.Fatal("the *Registry returned directly must be the exact same instance registered in ProjectRegistry")
	}
}

// TestWireProjectRegistry_MultipleServices proves ProjectRegistry contains
// every configured service, and that each one owns a distinct, independent
// *Registry — no sharing, matching ADR-0006's registry isolation guarantee.
func TestWireProjectRegistry_MultipleServices(t *testing.T) {
	srv := proxy.NewServer(zap.NewNop(), metrics.New())
	defer srv.Close()

	sc := &config.ServicesConfig{Services: []config.ServiceConfig{
		{Name: "web", Binds: []config.PortBinding{{ListenPort: 0, TargetPort: 3000}}},
		{Name: "grafana", Binds: []config.PortBinding{{ListenPort: 0, TargetPort: 3000}}},
		{Name: "prometheus", Binds: []config.PortBinding{{ListenPort: 0, TargetPort: 9090}}},
	}}

	pr, defaultReg, err := wireProjectRegistry(srv, metrics.New(), "web", sc, zap.NewNop())
	if err != nil {
		t.Fatalf("wireProjectRegistry: %v", err)
	}

	services := pr.Services()
	if len(services) != 3 {
		t.Fatalf("expected 3 registered services, got %d: %v", len(services), services)
	}
	for _, name := range []string{"web", "grafana", "prometheus"} {
		if _, ok := pr.For(name); !ok {
			t.Errorf("expected %q to be registered in ProjectRegistry", name)
		}
	}

	webReg, _ := pr.For("web")
	grafanaReg, _ := pr.For("grafana")
	prometheusReg, _ := pr.For("prometheus")
	if webReg == grafanaReg || webReg == prometheusReg || grafanaReg == prometheusReg {
		t.Fatal("each service must own a distinct *Registry instance — no sharing")
	}
	if webReg != defaultReg {
		t.Fatal("the returned default *Registry must be the one registered under defaultService (\"web\")")
	}
}

// TestWireProjectRegistry_BindsCarryServiceLabel proves each service's ports
// are bound with PortBinding.Service set to that service's own name — the
// field Stage 1 added specifically so a shared Server can resolve the
// correct router per connection.
func TestWireProjectRegistry_BindsCarryServiceLabel(t *testing.T) {
	srv := proxy.NewServer(zap.NewNop(), metrics.New())
	defer srv.Close()

	sc := &config.ServicesConfig{Services: []config.ServiceConfig{
		{Name: "web", Binds: []config.PortBinding{{ListenPort: 0, TargetPort: 3000}}},
		{Name: "grafana", Binds: []config.PortBinding{{ListenPort: 0, TargetPort: 3000}}},
	}}

	if _, _, err := wireProjectRegistry(srv, metrics.New(), "web", sc, zap.NewNop()); err != nil {
		t.Fatalf("wireProjectRegistry: %v", err)
	}

	bindings := srv.Bindings()
	if len(bindings) != 2 {
		t.Fatalf("expected 2 real bindings, got %d", len(bindings))
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		seen[b.Service] = true
	}
	if !seen["web"] || !seen["grafana"] {
		t.Errorf("expected bindings for both \"web\" and \"grafana\", got services: %v", seen)
	}
}

// TestWireProjectRegistry_DefaultServiceMissing proves a services.json that
// omits cfg.ProxyInstance's own name fails loudly at startup rather than
// silently leaving reg nil for every downstream call site that dereferences
// it (the zero-backend hook, the periodic-rediscovery gate).
func TestWireProjectRegistry_DefaultServiceMissing(t *testing.T) {
	srv := proxy.NewServer(zap.NewNop(), metrics.New())
	defer srv.Close()

	sc := &config.ServicesConfig{Services: []config.ServiceConfig{
		{Name: "grafana", Binds: []config.PortBinding{{ListenPort: 0, TargetPort: 3000}}},
	}}

	_, _, err := wireProjectRegistry(srv, metrics.New(), "web", sc, zap.NewNop())
	if err == nil {
		t.Fatal("expected an error when defaultService (\"web\") isn't among the configured services")
	}
}
