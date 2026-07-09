package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeServicesJSON(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "services.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ── LoadServicesConfig: parsing ──────────────────────────────────────────────

func TestLoadServicesConfig_ParsesMultiService(t *testing.T) {
	path := writeServicesJSON(t, `{
		"services": [
			{"name": "web", "binds": [{"listen_port": 8000, "target_port": 3000}]},
			{"name": "grafana", "binds": [{"listen_port": 3001, "target_port": 3000}]}
		]
	}`)

	sc, err := LoadServicesConfig(path)
	if err != nil {
		t.Fatalf("LoadServicesConfig: %v", err)
	}
	if len(sc.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(sc.Services))
	}
	if sc.Services[0].Name != "web" || sc.Services[0].Binds[0].ListenPort != 8000 {
		t.Errorf("service[0] parsed incorrectly: %+v", sc.Services[0])
	}
	if sc.Services[1].Name != "grafana" || sc.Services[1].Binds[0].TargetPort != 3000 {
		t.Errorf("service[1] parsed incorrectly: %+v", sc.Services[1])
	}
}

func TestLoadServicesConfig_MultipleBindsPerService(t *testing.T) {
	path := writeServicesJSON(t, `{
		"services": [
			{"name": "web", "binds": [
				{"listen_port": 8000, "target_port": 3000},
				{"listen_port": 8001, "target_port": 3001}
			]}
		]
	}`)

	sc, err := LoadServicesConfig(path)
	if err != nil {
		t.Fatalf("LoadServicesConfig: %v", err)
	}
	if len(sc.Services[0].Binds) != 2 {
		t.Fatalf("expected 2 binds, got %d", len(sc.Services[0].Binds))
	}
}

func TestLoadServicesConfig_MissingFile(t *testing.T) {
	_, err := LoadServicesConfig(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestLoadServicesConfig_MalformedJSON(t *testing.T) {
	path := writeServicesJSON(t, `{ not valid json`)
	_, err := LoadServicesConfig(path)
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

// ── Validate: structural rules ────────────────────────────────────────────────

func TestValidate_EmptyServicesRejected(t *testing.T) {
	sc := &ServicesConfig{}
	if err := sc.Validate(); err == nil {
		t.Fatal("expected an error for an empty services list")
	}
}

func TestValidate_NilReceiverRejected(t *testing.T) {
	var sc *ServicesConfig
	if err := sc.Validate(); err == nil {
		t.Fatal("expected an error for a nil ServicesConfig")
	}
}

func TestValidate_MissingNameRejected(t *testing.T) {
	sc := &ServicesConfig{Services: []ServiceConfig{
		{Binds: []PortBinding{{ListenPort: 8000, TargetPort: 3000}}},
	}}
	if err := sc.Validate(); err == nil {
		t.Fatal("expected an error for a missing service name")
	}
}

func TestValidate_MissingBindsRejected(t *testing.T) {
	sc := &ServicesConfig{Services: []ServiceConfig{
		{Name: "web"},
	}}
	if err := sc.Validate(); err == nil {
		t.Fatal("expected an error for a service with no binds")
	}
}

func TestValidate_InvalidNameRejected(t *testing.T) {
	for _, name := range []string{"-web", " web", "web!", "web/x", ""} {
		sc := &ServicesConfig{Services: []ServiceConfig{
			{Name: name, Binds: []PortBinding{{ListenPort: 8000, TargetPort: 3000}}},
		}}
		if err := sc.Validate(); err == nil {
			t.Errorf("expected an error for invalid name %q", name)
		}
	}
}

func TestValidate_ValidNamesAccepted(t *testing.T) {
	for _, name := range []string{"web", "web-1", "web_1", "web.1", "Web123"} {
		sc := &ServicesConfig{Services: []ServiceConfig{
			{Name: name, Binds: []PortBinding{{ListenPort: 8000, TargetPort: 3000}}},
		}}
		if err := sc.Validate(); err != nil {
			t.Errorf("expected name %q to be valid, got error: %v", name, err)
		}
	}
}

func TestValidate_DuplicateServiceNameRejected(t *testing.T) {
	sc := &ServicesConfig{Services: []ServiceConfig{
		{Name: "web", Binds: []PortBinding{{ListenPort: 8000, TargetPort: 3000}}},
		{Name: "web", Binds: []PortBinding{{ListenPort: 8001, TargetPort: 3001}}},
	}}
	if err := sc.Validate(); err == nil {
		t.Fatal("expected an error for duplicate service names")
	}
}

func TestValidate_DuplicatePortAcrossServicesRejected(t *testing.T) {
	sc := &ServicesConfig{Services: []ServiceConfig{
		{Name: "web", Binds: []PortBinding{{ListenPort: 8000, TargetPort: 3000}}},
		{Name: "grafana", Binds: []PortBinding{{ListenPort: 8000, TargetPort: 3001}}},
	}}
	if err := sc.Validate(); err == nil {
		t.Fatal("expected an error for a listen port claimed by two services")
	}
}

func TestValidate_DuplicatePortWithinServiceRejected(t *testing.T) {
	sc := &ServicesConfig{Services: []ServiceConfig{
		{Name: "web", Binds: []PortBinding{
			{ListenPort: 8000, TargetPort: 3000},
			{ListenPort: 8000, TargetPort: 3001},
		}},
	}}
	if err := sc.Validate(); err == nil {
		t.Fatal("expected an error for a listen port repeated within one service's own binds")
	}
}

func TestValidate_OutOfRangePortsRejected(t *testing.T) {
	cases := []PortBinding{
		{ListenPort: 0, TargetPort: 3000},
		{ListenPort: 70000, TargetPort: 3000},
		{ListenPort: 8000, TargetPort: 0},
		{ListenPort: 8000, TargetPort: 70000},
	}
	for _, b := range cases {
		sc := &ServicesConfig{Services: []ServiceConfig{
			{Name: "web", Binds: []PortBinding{b}},
		}}
		if err := sc.Validate(); err == nil {
			t.Errorf("expected an error for out-of-range binding %+v", b)
		}
	}
}

func TestValidate_ValidMultiServiceConfigAccepted(t *testing.T) {
	sc := &ServicesConfig{Services: []ServiceConfig{
		{Name: "web", Binds: []PortBinding{{ListenPort: 8000, TargetPort: 3000}}},
		{Name: "grafana", Binds: []PortBinding{{ListenPort: 8001, TargetPort: 3000}}},
		{Name: "prometheus", Binds: []PortBinding{{ListenPort: 8002, TargetPort: 9090}}},
	}}
	if err := sc.Validate(); err != nil {
		t.Errorf("expected a valid 3-service config to pass, got: %v", err)
	}
}

// ── ResolveServicesConfig: single-service compatibility + services.json ─────

func TestResolveServicesConfig_NoFileSynthesizesSingleService(t *testing.T) {
	t.Setenv("ORBIT_SERVICES_CONFIG", filepath.Join(t.TempDir(), "does-not-exist.json"))

	cfg := &ProxyConfig{
		ProxyInstance: "default",
		Binds:         []PortBinding{{ListenPort: 9900, TargetPort: 3000}},
	}
	sc, err := ResolveServicesConfig(cfg)
	if err != nil {
		t.Fatalf("ResolveServicesConfig: %v", err)
	}
	if len(sc.Services) != 1 {
		t.Fatalf("expected exactly 1 synthesized service, got %d", len(sc.Services))
	}
	if sc.Services[0].Name != cfg.ProxyInstance {
		t.Errorf("synthesized service name = %q, want %q (cfg.ProxyInstance)", sc.Services[0].Name, cfg.ProxyInstance)
	}
	if len(sc.Services[0].Binds) != 1 || sc.Services[0].Binds[0] != cfg.Binds[0] {
		t.Errorf("synthesized binds = %+v, want %+v (cfg.Binds, unchanged)", sc.Services[0].Binds, cfg.Binds)
	}
}

func TestResolveServicesConfig_FilePresentLoadsMultiService(t *testing.T) {
	path := writeServicesJSON(t, `{
		"services": [
			{"name": "web", "binds": [{"listen_port": 8000, "target_port": 3000}]},
			{"name": "grafana", "binds": [{"listen_port": 8001, "target_port": 3000}]}
		]
	}`)
	t.Setenv("ORBIT_SERVICES_CONFIG", path)

	cfg := &ProxyConfig{ProxyInstance: "default", Binds: []PortBinding{{ListenPort: 9900, TargetPort: 3000}}}
	sc, err := ResolveServicesConfig(cfg)
	if err != nil {
		t.Fatalf("ResolveServicesConfig: %v", err)
	}
	if len(sc.Services) != 2 {
		t.Fatalf("expected the 2 services declared in the file, got %d", len(sc.Services))
	}
}

func TestResolveServicesConfig_InvalidFilePropagatesError(t *testing.T) {
	path := writeServicesJSON(t, `{
		"services": [
			{"name": "web", "binds": [{"listen_port": 8000, "target_port": 3000}]},
			{"name": "web", "binds": [{"listen_port": 8001, "target_port": 3000}]}
		]
	}`)
	t.Setenv("ORBIT_SERVICES_CONFIG", path)

	cfg := &ProxyConfig{ProxyInstance: "default", Binds: []PortBinding{{ListenPort: 9900, TargetPort: 3000}}}
	if _, err := ResolveServicesConfig(cfg); err == nil {
		t.Fatal("expected the duplicate-name validation error to propagate, not be silently repaired")
	}
}
