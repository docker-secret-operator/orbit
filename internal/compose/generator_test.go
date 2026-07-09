package compose_test

import (
	"strings"
	"testing"

	"github.com/docker-secret-operator/orbit/internal/compose"
)

const generatorInput = `
version: "3.9"
services:
  api:
    image: myapp:latest
    ports:
      - "3000:3000"
    environment:
      PORT: "3000"
  db:
    image: postgres:16
    ports:
      - "5432:5432"
  worker:
    image: myapp:latest
`

func parse(t *testing.T, y string) *compose.ComposeFile {
	t.Helper()
	cf, err := compose.ParseBytes([]byte(y))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	return cf
}

func TestGenerate_Basic(t *testing.T) {
	cf := parse(t, generatorInput)
	_, sum, err := compose.Generate(cf)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(sum.Proxied) != 1 || sum.Proxied[0] != "api" {
		t.Errorf("Proxied = %v, want [api]", sum.Proxied)
	}
	if len(sum.Skipped) != 1 || sum.Skipped[0] != "db" {
		t.Errorf("Skipped = %v, want [db]", sum.Skipped)
	}
	if len(sum.PassThru) != 1 || sum.PassThru[0] != "worker" {
		t.Errorf("PassThru = %v, want [worker]", sum.PassThru)
	}
}

func TestGenerate_ProxyServiceCreated(t *testing.T) {
	cf := parse(t, generatorInput)
	out, _, _ := compose.Generate(cf)
	if _, ok := out.Services["docker-rollout-proxy-api"]; !ok {
		t.Error("docker-rollout-proxy-api service should be in generated output")
	}
}

func TestGenerate_BackingServiceHasNoHostPorts(t *testing.T) {
	cf := parse(t, generatorInput)
	out, _, _ := compose.Generate(cf)
	api := out.Services["api"]
	if len(api.Ports) != 0 {
		t.Errorf("backing service should have no host ports, got %v", api.Ports)
	}
}

func TestGenerate_BackingServiceHasExpose(t *testing.T) {
	cf := parse(t, generatorInput)
	out, _, _ := compose.Generate(cf)
	api := out.Services["api"]
	found := false
	for _, e := range api.Expose {
		if e == "3000" {
			found = true
		}
	}
	if !found {
		t.Errorf("backing api.Expose should contain 3000, got %v", api.Expose)
	}
}

func TestGenerate_BackingServiceJoinsMesh(t *testing.T) {
	cf := parse(t, generatorInput)
	out, _, _ := compose.Generate(cf)
	api := out.Services["api"]
	found := false
	for _, n := range api.Networks {
		if n == "docker_rollout_mesh" {
			found = true
		}
	}
	if !found {
		t.Errorf("backing api.Networks should contain docker_rollout_mesh, got %v", api.Networks)
	}
}

func TestGenerate_ProxyOwnsHostPorts(t *testing.T) {
	cf := parse(t, generatorInput)
	out, _, _ := compose.Generate(cf)
	proxyService := out.Services["docker-rollout-proxy-api"]
	if len(proxyService.Ports) == 0 {
		t.Error("proxy service should own the host port")
	}
}

func TestGenerate_ProxyHasDpivotBinds(t *testing.T) {
	cf := parse(t, generatorInput)
	out, _, _ := compose.Generate(cf)
	proxy := out.Services["docker-rollout-proxy-api"]
	if proxy.Environment["ORBIT_BINDS"] == "" {
		t.Error("proxy service should have ORBIT_BINDS set")
	}
}

func TestGenerate_MeshNetworkCreated(t *testing.T) {
	cf := parse(t, generatorInput)
	out, _, _ := compose.Generate(cf)
	if _, ok := out.Networks["docker_rollout_mesh"]; !ok {
		t.Error("docker_rollout_mesh network should be in generated output")
	}
}

func TestGenerate_DatabaseNotProxied(t *testing.T) {
	cf := parse(t, generatorInput)
	out, _, _ := compose.Generate(cf)
	if _, ok := out.Services["docker-rollout-proxy-db"]; ok {
		t.Error("docker-rollout-proxy-db should NOT be created for database service")
	}
	db := out.Services["db"]
	if len(db.Ports) == 0 {
		t.Error("database service should keep its original ports")
	}
}

func TestGenerate_WorkerPassThrough(t *testing.T) {
	cf := parse(t, generatorInput)
	out, _, _ := compose.Generate(cf)
	if _, ok := out.Services["docker-rollout-proxy-worker"]; ok {
		t.Error("docker-rollout-proxy-worker should NOT be created for worker (no ports)")
	}
}

func TestGenerate_SkipOverride(t *testing.T) {
	y := `
version: "3.9"
services:
  app:
    image: myapp:latest
    ports: ["8080:8080"]
    x-docker-rollout:
      skip: true
`
	cf := parse(t, y)
	out, sum, _ := compose.Generate(cf)
	if _, ok := out.Services["docker-rollout-proxy-app"]; ok {
		t.Error("proxy should not be created when x-docker-rollout.skip is true")
	}
	if len(sum.PassThru) != 1 {
		t.Errorf("sum.PassThru = %v, want [app]", sum.PassThru)
	}
}

func TestGenerate_OriginalNotModified(t *testing.T) {
	cf := parse(t, generatorInput)
	origPortCount := len(cf.Services["api"].Ports)
	compose.Generate(cf) //nolint:errcheck
	if len(cf.Services["api"].Ports) != origPortCount {
		t.Error("Generate must not modify the input ComposeFile")
	}
}

func TestGenerate_BackingServiceLabels(t *testing.T) {
	cf := parse(t, generatorInput)
	out, _, _ := compose.Generate(cf)
	api := out.Services["api"]
	labels, ok := api.RawFields["labels"].(map[string]interface{})
	if !ok {
		t.Fatal("backing service should have labels in RawFields")
	}
	if labels["orbit.io/managed"] != "true" {
		t.Errorf("orbit.io/managed = %v, want true", labels["orbit.io/managed"])
	}
}

func TestGenerate_MultiPort(t *testing.T) {
	y := `
version: "3.9"
services:
  frontend:
    image: nginx:alpine
    ports:
      - "80:80"
      - "443:443"
`
	cf := parse(t, y)
	out, _, err := compose.Generate(cf)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	proxy := out.Services["docker-rollout-proxy-frontend"]
	// 2 traffic ports (80:80, 443:443) + 1 control port (firstHostPort+6900 → 9900)
	if len(proxy.Ports) != 3 {
		t.Errorf("multi-port proxy should have 3 ports (2 traffic + 1 control), got %d: %v", len(proxy.Ports), proxy.Ports)
	}
	binds := proxy.Environment["ORBIT_BINDS"]
	if !strings.Contains(binds, "80") || !strings.Contains(binds, "443") {
		t.Errorf("ORBIT_BINDS = %q, want both port 80 and 443", binds)
	}
	// Control port should be 80+6900=6980 mapped to 9900
	controlPort := proxy.Ports[len(proxy.Ports)-1]
	if controlPort != "6980:9900" {
		t.Errorf("control port mapping = %q, want 6980:9900", controlPort)
	}
}

func TestGenerate_NilInput_ReturnsError(t *testing.T) {
	_, _, err := compose.Generate(nil)
	if err == nil {
		t.Fatal("want error for nil input, got nil")
	}
}

// TestGenerate_StripsContainerName guards against a real, confirmed bug: a
// fixed container_name (common — the reference test stack this project was
// verified against sets one on every service) makes `docker compose up
// --scale <service>=2` refuse to run at all ("Docker requires each
// container to have a unique name"), which is the exact mechanism every
// rollout/deploy depends on. Any service Orbit injects a proxy for will be
// scaled during its first rollout, so a name fixed at generate time is
// incompatible with the one thing this file exists to enable.
func TestGenerate_StripsContainerName(t *testing.T) {
	const input = `
services:
  api:
    image: myapp:latest
    container_name: api
    ports:
      - "3000:3000"
`
	cf := parse(t, input)
	out, _, err := compose.Generate(cf)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	api := out.Services["api"]
	if _, ok := api.RawFields["container_name"]; ok {
		t.Errorf("backing service still has container_name = %v, want stripped", api.RawFields["container_name"])
	}
}
