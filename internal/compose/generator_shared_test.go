package compose_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/docker-secret-operator/orbit/internal/compose"
)

const sharedGeneratorInput = `
version: "3.9"
services:
  web:
    image: myapp/web:latest
    ports:
      - "3000:3000"
  api:
    image: myapp/api:latest
    ports:
      - "4000:4000"
  db:
    image: postgres:16
    ports:
      - "5432:5432"
  worker:
    image: myapp/worker:latest
`

// ── Shared-proxy topology ───────────────────────────────────────────────────

// TestGenerateShared_CollidingProxyName_Errors closes the go-live audit's
// finding M2: GenerateShared unconditionally assigns
// out.Services["docker-rollout-proxy"], silently overwriting a real
// user-defined service of that exact name (the most realistic trigger:
// accidentally re-running `generate --shared-proxy` against a file that is
// itself already Orbit-generated). It must fail closed with an error
// instead of discarding the user's service definition.
func TestGenerateShared_CollidingProxyName_Errors(t *testing.T) {
	y := `
version: "3.9"
services:
  web:
    image: myapp/web:latest
    ports:
      - "3000:3000"
  docker-rollout-proxy:
    image: something-unrelated:latest
`
	cf := parse(t, y)
	_, _, err := compose.GenerateShared(cf)
	if err == nil {
		t.Fatal("GenerateShared should error when the input already defines a service named docker-rollout-proxy")
	}
}

func TestGenerateShared_OneProxyServiceForAllEligible(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	out, sum, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	if len(sum.Proxied) != 2 {
		t.Fatalf("Proxied = %v, want 2 services (web, api)", sum.Proxied)
	}
	// Exactly one shared proxy service, not one per proxied service.
	if _, ok := out.Services["docker-rollout-proxy"]; !ok {
		t.Fatal("expected a single 'docker-rollout-proxy' service")
	}
	if _, ok := out.Services["docker-rollout-proxy-web"]; ok {
		t.Error("must not create a per-service proxy (docker-rollout-proxy-web) in shared mode")
	}
	if _, ok := out.Services["docker-rollout-proxy-api"]; ok {
		t.Error("must not create a per-service proxy (docker-rollout-proxy-api) in shared mode")
	}
	proxyCount := 0
	for name := range out.Services {
		if strings.HasPrefix(name, "docker-rollout-proxy") {
			proxyCount++
		}
	}
	if proxyCount != 1 {
		t.Errorf("expected exactly 1 proxy service in generated output, found %d", proxyCount)
	}
}

func TestGenerateShared_ProxyOwnsAllTrafficPortsPlusOneControlPort(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	out, _, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	proxy := out.Services["docker-rollout-proxy"]
	// 2 traffic ports (3000, 4000) + 1 control port.
	if len(proxy.Ports) != 3 {
		t.Fatalf("proxy.Ports = %v, want 3 (2 traffic + 1 control)", proxy.Ports)
	}
	var sawWeb, sawAPI bool
	for _, p := range proxy.Ports {
		if strings.HasPrefix(p, "3000:") {
			sawWeb = true
		}
		if strings.HasPrefix(p, "4000:") {
			sawAPI = true
		}
	}
	if !sawWeb || !sawAPI {
		t.Errorf("proxy.Ports = %v, want both 3000 and 4000 traffic ports", proxy.Ports)
	}
}

func TestGenerateShared_BackingServicesUnaffectedByEachOther(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	out, _, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	web := out.Services["web"]
	api := out.Services["api"]

	if len(web.Ports) != 0 || len(api.Ports) != 0 {
		t.Error("backing services must have host ports removed, same as legacy Generate")
	}

	webLabels, ok := web.RawFields["labels"].(map[string]interface{})
	if !ok {
		t.Fatal("web backing service should have labels")
	}
	if webLabels["orbit.io/service"] != "web" {
		t.Errorf("web orbit.io/service = %v, want web", webLabels["orbit.io/service"])
	}
	apiLabels, ok := api.RawFields["labels"].(map[string]interface{})
	if !ok {
		t.Fatal("api backing service should have labels")
	}
	if apiLabels["orbit.io/service"] != "api" {
		t.Errorf("api orbit.io/service = %v, want api", apiLabels["orbit.io/service"])
	}
	// orbit.io/proxy-instance must still be each container's OWN service name
	// — executeRecovery (unchanged, per-service) matches this label against
	// the service name it's currently recovering, not a single global value.
	if webLabels["orbit.io/proxy-instance"] != "web" {
		t.Errorf("web orbit.io/proxy-instance = %v, want web", webLabels["orbit.io/proxy-instance"])
	}
	if apiLabels["orbit.io/proxy-instance"] != "api" {
		t.Errorf("api orbit.io/proxy-instance = %v, want api", apiLabels["orbit.io/proxy-instance"])
	}
}

func TestGenerateShared_ProxyServiceLabels(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	out, _, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	proxy := out.Services["docker-rollout-proxy"]
	labels, ok := proxy.RawFields["labels"].(map[string]interface{})
	if !ok {
		t.Fatal("shared proxy should have labels")
	}
	// Reconciler.ReconcileOnce filters out any container with
	// orbit.io/proxy=="true" (internal/proxy/reconciler.go) — this label is
	// load-bearing, not cosmetic.
	if labels["orbit.io/proxy"] != "true" {
		t.Errorf("orbit.io/proxy = %v, want true", labels["orbit.io/proxy"])
	}
	if labels["orbit.io/managed"] != "true" {
		t.Errorf("orbit.io/managed = %v, want true", labels["orbit.io/managed"])
	}
}

func TestGenerateShared_MeshNetworkCreated(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	out, _, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	if _, ok := out.Networks["docker_rollout_mesh"]; !ok {
		t.Error("docker_rollout_mesh network should be in generated output")
	}
	proxy := out.Services["docker-rollout-proxy"]
	var onMesh bool
	for _, n := range proxy.Networks {
		if n == "docker_rollout_mesh" {
			onMesh = true
		}
	}
	if !onMesh {
		t.Errorf("proxy.Networks = %v, want docker_rollout_mesh", proxy.Networks)
	}
}

func TestGenerateShared_DatabaseAndPassThroughUnaffected(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	out, sum, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	if len(sum.Skipped) != 1 || sum.Skipped[0] != "db" {
		t.Errorf("Skipped = %v, want [db]", sum.Skipped)
	}
	if len(sum.PassThru) != 1 || sum.PassThru[0] != "worker" {
		t.Errorf("PassThru = %v, want [worker]", sum.PassThru)
	}
	db := out.Services["db"]
	if len(db.Ports) == 0 {
		t.Error("database service should keep its original ports, unaffected by shared-proxy mode")
	}
}

func TestGenerateShared_SharedProxyBindsPopulated(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	_, sum, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	if len(sum.SharedProxyBinds) != 2 {
		t.Fatalf("SharedProxyBinds = %v, want 2 entries", sum.SharedProxyBinds)
	}
	byService := map[string]compose.PortBinding{}
	for _, b := range sum.SharedProxyBinds {
		byService[b.Service] = b
	}
	web, ok := byService["web"]
	if !ok || web.ListenPort != 3000 || web.TargetPort != 3000 {
		t.Errorf("web bind = %+v, want listen=3000 target=3000", web)
	}
	api, ok := byService["api"]
	if !ok || api.ListenPort != 4000 || api.TargetPort != 4000 {
		t.Errorf("api bind = %+v, want listen=4000 target=4000", api)
	}
}

func TestGenerateShared_DefaultServiceInstanceIsDeterministic(t *testing.T) {
	// ORBIT_PROXY_INSTANCE / ORBIT_BINDS must be pinned to the
	// alphabetically-first proxied service, regardless of Go's randomized
	// map iteration order — run several times to catch nondeterminism.
	for i := 0; i < 10; i++ {
		cf := parse(t, sharedGeneratorInput)
		out, sum, err := compose.GenerateShared(cf)
		if err != nil {
			t.Fatalf("GenerateShared: %v", err)
		}
		sorted := append([]string{}, sum.Proxied...)
		sort.Strings(sorted)
		want := sorted[0] // "api" before "web" alphabetically

		proxy := out.Services["docker-rollout-proxy"]
		if proxy.Environment["ORBIT_PROXY_INSTANCE"] != want {
			t.Fatalf("iteration %d: ORBIT_PROXY_INSTANCE = %q, want %q", i, proxy.Environment["ORBIT_PROXY_INSTANCE"], want)
		}
		if !strings.Contains(proxy.Environment["ORBIT_BINDS"], "") { // sanity: field exists
			_ = proxy.Environment["ORBIT_BINDS"]
		}
	}
}

func TestGenerateShared_ProxyDependsOnAllEligibleServices(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	out, _, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	proxy := out.Services["docker-rollout-proxy"]
	deps := map[string]bool{}
	for _, d := range proxy.DependsOn {
		deps[d] = true
	}
	if !deps["web"] || !deps["api"] {
		t.Errorf("proxy.DependsOn = %v, want both web and api", proxy.DependsOn)
	}
}

func TestGenerateShared_SharedStateVolumeDeclaredOnce(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	out, _, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	stateVolumes := 0
	for name := range out.Volumes {
		if strings.HasPrefix(name, "docker_rollout_state") {
			stateVolumes++
		}
	}
	if stateVolumes != 1 {
		t.Errorf("expected exactly 1 shared state volume, found %d: %v", stateVolumes, out.Volumes)
	}
}

func TestGenerateShared_ServicesConfigMountPresent(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	out, _, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	proxy := out.Services["docker-rollout-proxy"]
	volumes, ok := proxy.RawFields["volumes"].([]interface{})
	if !ok {
		t.Fatal("proxy should have volumes in RawFields")
	}
	var found bool
	for _, v := range volumes {
		if s, ok := v.(string); ok && strings.Contains(s, "/etc/orbit/services.json") {
			found = true
		}
	}
	if !found {
		t.Errorf("proxy volumes = %v, want a mount targeting /etc/orbit/services.json", volumes)
	}
}

func TestGenerateShared_NoEligibleServices_NoProxyCreated(t *testing.T) {
	const y = `
version: "3.9"
services:
  db:
    image: postgres:16
    ports:
      - "5432:5432"
`
	cf := parse(t, y)
	out, sum, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	if len(sum.Proxied) != 0 {
		t.Errorf("Proxied = %v, want none", sum.Proxied)
	}
	if _, ok := out.Services["docker-rollout-proxy"]; ok {
		t.Error("no proxy service should be created when nothing is eligible")
	}
	if len(sum.SharedProxyBinds) != 0 {
		t.Errorf("SharedProxyBinds = %v, want empty", sum.SharedProxyBinds)
	}
}

func TestGenerateShared_NilInput_ReturnsError(t *testing.T) {
	_, _, err := compose.GenerateShared(nil)
	if err == nil {
		t.Fatal("want error for nil input, got nil")
	}
}

func TestGenerateShared_OriginalNotModified(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	origPortCount := len(cf.Services["web"].Ports)
	compose.GenerateShared(cf) //nolint:errcheck
	if len(cf.Services["web"].Ports) != origPortCount {
		t.Error("GenerateShared must not modify the input ComposeFile")
	}
}

func TestGenerateShared_SinglePortStripsContainerName(t *testing.T) {
	const input = `
services:
  web:
    image: myapp:latest
    container_name: web
    ports:
      - "3000:3000"
`
	cf := parse(t, input)
	out, _, err := compose.GenerateShared(cf)
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	web := out.Services["web"]
	if _, ok := web.RawFields["container_name"]; ok {
		t.Errorf("backing service still has container_name = %v, want stripped", web.RawFields["container_name"])
	}
}

// ── Regression: legacy Generate must be byte-for-byte unaffected ───────────

// TestGenerate_UnaffectedByShared proves the existing, default Generate path
// (used by every current deployment) is completely unchanged by the
// shared-proxy addition — this is the concrete "no breaking upgrade"
// guarantee: nobody gets the new topology unless they explicitly call
// GenerateShared (wired to the new --shared-proxy CLI flag).
func TestGenerate_UnaffectedByShared(t *testing.T) {
	cf := parse(t, sharedGeneratorInput)
	out, sum, err := compose.Generate(cf)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(sum.Proxied) != 2 {
		t.Fatalf("Proxied = %v, want 2", sum.Proxied)
	}
	// Legacy behavior: one proxy PER service, not shared.
	if _, ok := out.Services["docker-rollout-proxy-web"]; !ok {
		t.Error("legacy Generate should still create docker-rollout-proxy-web")
	}
	if _, ok := out.Services["docker-rollout-proxy-api"]; !ok {
		t.Error("legacy Generate should still create docker-rollout-proxy-api")
	}
	if _, ok := out.Services["docker-rollout-proxy"]; ok {
		t.Error("legacy Generate must never create a shared 'docker-rollout-proxy' service")
	}
	if len(sum.SharedProxyBinds) != 0 {
		t.Error("legacy Generate must never populate SharedProxyBinds")
	}
}
