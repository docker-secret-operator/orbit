package compose

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Summary records what the generator transformed.
type Summary struct {
	Proxied  []string // service names that received proxy injection
	Skipped  []string // service names that were auto-excluded (databases)
	PassThru []string // service names passed through unchanged

	// SharedProxyBinds is populated only by GenerateShared: one entry per
	// proxied service, naming the host/container port pair the shared
	// proxy binds on that service's behalf. The caller (cmd/docker-orbit)
	// uses this to write the services.json companion file
	// internal/config.ResolveServicesConfig reads at proxy startup — never
	// populated by Generate (the legacy, one-proxy-per-service path).
	SharedProxyBinds []PortBinding
}

// Generate transforms an input ComposeFile into an Orbit-enhanced output.
//
// Transformation rules (per service, in priority order):
//  1. x-docker-rollout.skip == true → pass through unchanged, no warning
//  2. No ports → pass through unchanged
//  3. Image matches known database → pass through, emit warning (skipped list)
//  4. All other services with ports → inject proxy
//
// The generated file:
//   - Preserves all unrelated fields in every service verbatim, including labels,
//     volumes, healthchecks, restart policies, and user-defined networks.
//   - Adds a docker_rollout_mesh bridge network joined by every proxy+backing pair.
//   - Adds one docker-rollout-proxy-<service> service per injected service.
//   - Injects DSO_PROXY_BINDS and related env vars into each proxy service.
//   - Never modifies the caller-supplied ComposeFile (works on a deep copy).
func Generate(input *ComposeFile) (*ComposeFile, *Summary, error) {
	if input == nil {
		return nil, nil, fmt.Errorf("generator: nil compose file")
	}

	out := &ComposeFile{
		Version:  input.Version,
		Services: make(map[string]Service, len(input.Services)*2),
		Networks: deepCopyMap(input.Networks),
		Volumes:  deepCopyMap(input.Volumes),
	}

	// Ensure the mesh network exists. name: pins the actual Docker network
	// name to "docker_rollout_mesh" regardless of Compose project — without
	// it Compose prefixes the network with the project name (e.g.
	// "myproject_docker_rollout_mesh"), and internal/proxy/recovery.go's
	// exact-match lookup of NetworkSettings.Networks["docker_rollout_mesh"]
	// would never find the container's IP on it.
	if out.Networks == nil {
		out.Networks = make(map[string]interface{})
	}
	out.Networks["docker_rollout_mesh"] = map[string]interface{}{"driver": "bridge", "name": "docker_rollout_mesh"}

	if out.Volumes == nil {
		out.Volumes = make(map[string]interface{})
	}

	sum := &Summary{}

	for name, svc := range input.Services {
		switch {
		case svc.XRollout.Skip:
			// Explicit opt-out: pass through exactly as-is.
			out.Services[name] = copyService(svc)
			sum.PassThru = append(sum.PassThru, name)

		case len(svc.Ports) == 0:
			// No ports — nothing to proxy.
			out.Services[name] = copyService(svc)
			sum.PassThru = append(sum.PassThru, name)

		case IsDatabase(svc.Image):
			// Database auto-exclusion.
			out.Services[name] = copyService(svc)
			sum.Skipped = append(sum.Skipped, name)

		default:
			// Inject proxy.
			proxyName := "docker-rollout-proxy-" + name
			if _, collides := input.Services[proxyName]; collides {
				return nil, nil, fmt.Errorf(
					"generator: service %q needs a proxy named %q, but the input already defines a service with that exact name (likely re-running generate on an already-generated file) — rename or remove it first",
					name, proxyName)
			}
			backing, pairs, err := buildBackingService(name, svc)
			if err != nil {
				return nil, nil, fmt.Errorf("generator: service %q: %w", name, err)
			}
			out.Services[name] = backing
			out.Services[proxyName] = buildLegacyProxyService(name, pairs)
			sum.Proxied = append(sum.Proxied, name)

			// Named volume for the proxy's ORBIT_STATE_DIR (/var/lib/orbit) —
			// see buildLegacyProxyService's volume mount. Declared here, not
			// in the helper, because top-level `volumes:` entries live on
			// ComposeFile, not Service.
			out.Volumes[stateVolumeName(name)] = nil
		}
	}

	return out, sum, nil
}

// GenerateShared transforms an input ComposeFile the same way Generate does
// — identical eligibility rules (skip / no-ports / database / inject),
// identical per-service backing-service transformation (labels, network,
// env, stripped ports) — but emits exactly one shared docker-rollout-proxy
// service fronting every eligible service, instead of one proxy per
// service (ADR-0006 §"Shared Proxy and Event-Driven Discovery").
//
// This is a distinct, opt-in entry point specifically so Generate's output
// for every existing deployment — single-service or multi-service — is
// completely unaffected: nothing changes unless a caller explicitly calls
// GenerateShared instead (wired to the `--shared-proxy` flag on
// `docker orbit generate`, cmd/docker-orbit/main.go).
//
// The returned Summary's SharedProxyBinds field carries the per-service
// port bindings the caller needs to write the services.json companion file
// (internal/config.ResolveServicesConfig reads it at proxy startup) — this
// function performs no file I/O itself, matching Generate's existing
// pure-transformation contract.
func GenerateShared(input *ComposeFile) (*ComposeFile, *Summary, error) {
	if input == nil {
		return nil, nil, fmt.Errorf("generator: nil compose file")
	}

	out := &ComposeFile{
		Version:  input.Version,
		Services: make(map[string]Service, len(input.Services)+1),
		Networks: deepCopyMap(input.Networks),
		Volumes:  deepCopyMap(input.Volumes),
	}

	if out.Networks == nil {
		out.Networks = make(map[string]interface{})
	}
	out.Networks["docker_rollout_mesh"] = map[string]interface{}{"driver": "bridge", "name": "docker_rollout_mesh"}

	if out.Volumes == nil {
		out.Volumes = make(map[string]interface{})
	}

	sum := &Summary{}
	var entries []sharedEntry

	for name, svc := range input.Services {
		switch {
		case svc.XRollout.Skip:
			out.Services[name] = copyService(svc)
			sum.PassThru = append(sum.PassThru, name)

		case len(svc.Ports) == 0:
			out.Services[name] = copyService(svc)
			sum.PassThru = append(sum.PassThru, name)

		case IsDatabase(svc.Image):
			out.Services[name] = copyService(svc)
			sum.Skipped = append(sum.Skipped, name)

		default:
			backing, pairs, err := buildBackingService(name, svc)
			if err != nil {
				return nil, nil, fmt.Errorf("generator: service %q: %w", name, err)
			}
			out.Services[name] = backing
			sum.Proxied = append(sum.Proxied, name)
			entries = append(entries, sharedEntry{name: name, pairs: pairs})
		}
	}

	if len(entries) > 0 {
		if _, collides := input.Services["docker-rollout-proxy"]; collides {
			return nil, nil, fmt.Errorf(
				"generator: shared proxy needs the name %q, but the input already defines a service with that exact name (likely re-running generate --shared-proxy on an already-generated file) — rename or remove it first",
				"docker-rollout-proxy")
		}

		// Deterministic regardless of Go's randomized map iteration order —
		// the "default" service (ORBIT_PROXY_INSTANCE / ORBIT_BINDS, used
		// for unscoped control-API requests until Control API service
		// scoping ships) must be the same service on every run.
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

		proxy, binds := buildSharedProxyService(entries)
		out.Services["docker-rollout-proxy"] = proxy
		out.Volumes[sharedStateVolumeName] = nil
		sum.SharedProxyBinds = binds
	}

	return out, sum, nil
}

// portPair is a parsed host→container port mapping, shared by the legacy
// (one-proxy-per-service) and shared-proxy generation paths.
type portPair struct{ host, container int }

// sharedEntry is one eligible service's contribution to a shared proxy:
// its name and parsed port pairs, collected by GenerateShared before
// buildSharedProxyService constructs the single proxy stanza.
type sharedEntry struct {
	name  string
	pairs []portPair
}

// sharedStateVolumeName is the one named volume backing every service's
// ORBIT_STATE_DIR in shared-proxy mode. Singular (unlike stateVolumeName,
// one per legacy per-service proxy) because state.StateManager already
// keys persisted files by service internally (ADR-0006 Stage 2.3) — one
// shared proxy process, one state directory, is already the correct shape
// for the runtime it mounts into.
const sharedStateVolumeName = "docker_rollout_state_shared"

// buildBackingService applies every backing-service transformation shared
// by both generation paths — port removal, mesh network join, ownership
// env/labels, container_name stripping — parameterized only by the
// service's own name and definition, never by which proxy (legacy or
// shared) will front it. Extracted from what was buildProxyPair's backing-
// service half so Generate and GenerateShared can never drift apart on
// this logic.
func buildBackingService(name string, svc Service) (backing Service, pairs []portPair, err error) {
	backing = copyService(svc)

	// Collect host→container port mappings before we remove ports.
	pairs = make([]portPair, 0, len(svc.Ports))
	for _, p := range svc.Ports {
		h, c, err := parsePort(p)
		if err != nil {
			return Service{}, nil, fmt.Errorf("parse port %q: %w", p, err)
		}
		pairs = append(pairs, portPair{h, c})
	}

	// Remove host port bindings; add container-side expose.
	backing.Ports = nil
	for _, pp := range pairs {
		backing.Expose = appendUnique(backing.Expose, strconv.Itoa(pp.container))
	}

	// A fixed container_name makes `docker compose up --scale <service>=2`
	// — the mechanism every rollout/deploy uses to start the new container
	// alongside the old one — fail outright ("Docker requires each container
	// to have a unique name"). Any service Orbit injects a proxy for will be
	// scaled during its first rollout, so a name fixed at generate time is
	// incompatible with the one thing this file exists to enable. Docker
	// falls back to its own generated name (<project>-<service>-<n>), which
	// is what every other multi-replica Compose service already uses.
	delete(backing.RawFields, "container_name")

	// Join docker_rollout_mesh. If the service had no explicit networks it was on the
	// implicit "default" network; preserve that so it can still reach stateful
	// services (db, redis) that are not on docker_rollout_mesh.
	if len(backing.Networks) == 0 {
		backing.Networks = append(backing.Networks, "default")
	}
	backing.Networks = appendUnique(backing.Networks, "docker_rollout_mesh")
	// Sync network list to RawFields (Networks has yaml:"-").
	backing.RawFields["networks"] = toRawSlice(backing.Networks)

	// Inject ORBIT_BACKEND env var (informational) and ORBIT_BACKEND_ID — the
	// latter is required by internal/proxy.DockerRecoverySource.extractBackend
	// to register this container as the proxy's seed backend on first startup
	// (before any rollout has run). Matches the "<service>-default" seed ID
	// rollout.go already expects to deregister after the first successful
	// rollout (see its Step 9b).
	if backing.Environment == nil {
		backing.Environment = make(map[string]string)
	}
	if len(pairs) > 0 {
		backing.Environment["ORBIT_BACKEND"] = fmt.Sprintf("%s:%d", name, pairs[0].container)
		backing.Environment["ORBIT_BACKEND_ID"] = name + "-default"
	}
	// Sync environment map to RawFields (Environment has yaml:"-").
	backing.RawFields["environment"] = toRawMap(backing.Environment)

	// Strip x-docker-rollout so Docker never sees it.
	backing.XRollout = XRolloutConfig{}
	delete(backing.RawFields, "x-docker-rollout")

	// ── Labels ───────────────────────────────────────────────────────────
	// orbit.io/proxy and orbit.io/generation are both required, non-empty,
	// by extractBackend's ownership check — without them recovery treats
	// this container as having "incomplete ownership labels" and never
	// registers it, leaving the proxy with zero backends forever.
	//
	// orbit.io/proxy-instance scopes discovery to this service's own proxy
	// (matched against ORBIT_PROXY_INSTANCE below) — every proxy on the
	// mesh network otherwise sees every service's backing containers and
	// can adopt another service's backend as its own authoritative
	// generation.
	labels := map[string]interface{}{
		"orbit.io/managed":        "true",
		"orbit.io/service":        name,
		"orbit.io/proxy":          "false",
		"orbit.io/generation":     name + "-default",
		"orbit.io/proxy-instance": name,
	}
	merged := normalizeLabelsToMap(backing.RawFields["labels"])
	for k, v := range labels {
		merged[k] = v
	}
	backing.RawFields["labels"] = merged

	return backing, pairs, nil
}

// normalizeLabelsToMap converts a Compose `labels:` stanza — which YAML
// allows as either map form (`KEY: value`) or list form (`- "KEY=value"`,
// at least as common in real compose files) — into a single
// map[string]interface{}, so ownership-label injection always has something
// it can merge into regardless of which form the input used. A list entry
// with no "=" is treated as a label with an empty value, matching Docker
// Compose's own handling.
func normalizeLabelsToMap(raw interface{}) map[string]interface{} {
	switch v := raw.(type) {
	case map[string]interface{}:
		return v
	case []interface{}:
		out := make(map[string]interface{}, len(v))
		for _, entry := range v {
			s, ok := entry.(string)
			if !ok {
				continue
			}
			k, val, found := strings.Cut(s, "=")
			if !found {
				out[k] = ""
				continue
			}
			out[k] = val
		}
		return out
	default:
		return make(map[string]interface{})
	}
}

// buildLegacyProxyService builds the docker-rollout-proxy-<service> stanza
// for a single service, fronting it alone — the pre-Stage-5 topology,
// unchanged in every respect from before this file was split. Only
// Generate calls this; GenerateShared uses buildSharedProxyService instead.
func buildLegacyProxyService(name string, pairs []portPair) Service {
	// Build ORBIT_BINDS from port pairs.
	binds := make([]string, 0, len(pairs))
	for _, pp := range pairs {
		binds = append(binds, fmt.Sprintf("%d:%d", pp.host, pp.container))
	}

	// Initial backend entry: the backing service is reachable by DNS name
	// on docker_rollout_mesh at its container port.
	initialBackend := ""
	if len(pairs) > 0 {
		initialBackend = fmt.Sprintf("%s-default:%s:%d", name, name, pairs[0].container)
	}

	// Ports owned by the proxy (original host port bindings).
	// Convention: control port on the host = first traffic host port + 6900
	// e.g. service at host:3001 → control reachable at localhost:9901
	controlHostPort := 9900
	if len(pairs) > 0 {
		controlHostPort = pairs[0].host + 6900
	}
	proxyPorts := make([]string, 0, len(pairs)+1)
	for _, pp := range pairs {
		proxyPorts = append(proxyPorts, fmt.Sprintf("%d:%d", pp.host, pp.host))
	}
	proxyPorts = append(proxyPorts, fmt.Sprintf("%d:9900", controlHostPort))

	proxyEnv := map[string]string{
		"ORBIT_BINDS":          strings.Join(binds, ","),
		"ORBIT_TARGETS":        initialBackend,
		"ORBIT_CONTROL_PORT":   "9900",
		"ORBIT_PROXY_INSTANCE": name,
	}
	return Service{
		Image:       "technicaltalk/orbit:latest",
		Ports:       proxyPorts,
		Expose:      []string{"9900"},
		Networks:    []string{"docker_rollout_mesh"},
		DependsOn:   []string{name},
		Environment: proxyEnv,
		RawFields: map[string]interface{}{
			// These three fields have yaml:"-" and must live in RawFields to be emitted.
			"environment": toRawMap(proxyEnv),
			"networks":    toRawSlice([]string{"docker_rollout_mesh"}),
			"depends_on":  toRawSlice([]string{name}),
			"labels": map[string]interface{}{
				"orbit.io/proxy":   "true",
				"orbit.io/service": name,
				"orbit.io/managed": "true",
			},
			"restart": "unless-stopped",
			// technicaltalk/orbit:latest is rebuilt on every push to main
			// (.github/workflows/ci.yml) — pull_policy: always makes `docker
			// compose up` re-check the registry and pull a newer digest under
			// the same tag before (re)starting the proxy, instead of reusing
			// whatever was pulled locally the first time. Compose v2 only
			// (required elsewhere in this project already; see installation.md).
			"pull_policy": "always",
			// Required for startup/on-demand recovery (internal/proxy.DockerRecoverySource):
			// the proxy lists and inspects Orbit-managed containers by label to
			// discover and validate backends. Read-only — it never creates,
			// starts, or removes containers from inside the proxy.
			//
			// The second mount backs ORBIT_STATE_DIR (default /var/lib/orbit):
			// without it, ActiveGenerationState/RolloutState live only in the
			// container's writable layer and are lost on every recreation
			// (docker compose up after any change, --force-recreate, a host
			// reboot) — GenerateRecoveryPlan's persisted-state fast path
			// (RecoveryRestoreSingle) then never has anything to restore from
			// and every recovery silently falls back to RecoveryInferredFallback.
			"volumes": toRawSlice([]string{
				"/var/run/docker.sock:/var/run/docker.sock:ro",
				stateVolumeName(name) + ":/var/lib/orbit",
			}),
		},
	}
}

// buildSharedProxyService builds the single docker-rollout-proxy stanza
// fronting every service in entries (ADR-0006 Stage 4/5: one Docker API
// connection per project, one proxy process per project — see
// docs/adr/ADR-0006-shared-proxy-and-event-driven-discovery.md). entries
// must already be sorted by name (GenerateShared sorts before calling)
// so the "default" service chosen below is deterministic across runs,
// not dependent on Go's randomized map iteration order.
//
// Returns the proxy Service and the flat list of per-service port
// bindings the caller writes into the services.json companion file —
// internal/config.ResolveServicesConfig (unchanged) reads that file at
// proxy startup and is the sole source of truth for which services this
// process fronts; nothing generated here talks to that runtime code
// directly, matching Generate's existing pure-transformation contract.
func buildSharedProxyService(entries []sharedEntry) (Service, []PortBinding) {
	var (
		proxyPorts []string
		depends    []string
		binds      []PortBinding
	)

	// The alphabetically-first proxied service (entries is pre-sorted) is
	// the "default" for ORBIT_PROXY_INSTANCE/ORBIT_BINDS — the same
	// backward-compatibility role config.ResolveServicesConfig's own
	// single-service synthesis fallback already documents, and the value
	// every unscoped control-API request resolves to until Control API
	// service scoping (ADR-0006 §"Control API: Service Dimension") ships.
	defaultEntry := entries[0]
	var defaultBinds []string

	for _, e := range entries {
		depends = append(depends, e.name)
		for _, pp := range e.pairs {
			proxyPorts = append(proxyPorts, fmt.Sprintf("%d:%d", pp.host, pp.host))
			binds = append(binds, PortBinding{ListenPort: pp.host, Service: e.name, TargetPort: pp.container})
			if e.name == defaultEntry.name {
				defaultBinds = append(defaultBinds, fmt.Sprintf("%d:%d", pp.host, pp.container))
			}
		}
	}

	// One control port for the whole shared process — not one per
	// service, since there is only one proxy container now. Same
	// first-host-port+6900 convention as the legacy path, applied once
	// against the default service's own first port.
	controlHostPort := 9900
	if len(defaultEntry.pairs) > 0 {
		controlHostPort = defaultEntry.pairs[0].host + 6900
	}
	proxyPorts = append(proxyPorts, fmt.Sprintf("%d:9900", controlHostPort))

	proxyEnv := map[string]string{
		// ORBIT_BINDS/ORBIT_PROXY_INSTANCE satisfy config.LoadProxyConfig's
		// baseline validation (ORBIT_BINDS must be non-empty) and back the
		// single-service synthesis fallback in config.ResolveServicesConfig
		// — but services.json (mounted below) is present and takes
		// precedence, so every configured service is wired into
		// ProjectRegistry, not just the default one.
		"ORBIT_BINDS":           strings.Join(defaultBinds, ","),
		"ORBIT_CONTROL_PORT":    "9900",
		"ORBIT_PROXY_INSTANCE":  defaultEntry.name,
		"ORBIT_SERVICES_CONFIG": servicesConfigMountPath,
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.name)
	}

	return Service{
		Image:       "technicaltalk/orbit:latest",
		Ports:       proxyPorts,
		Expose:      []string{"9900"},
		Networks:    []string{"docker_rollout_mesh"},
		DependsOn:   depends,
		Environment: proxyEnv,
		RawFields: map[string]interface{}{
			"environment": toRawMap(proxyEnv),
			"networks":    toRawSlice([]string{"docker_rollout_mesh"}),
			"depends_on":  toRawSlice(depends),
			"labels": map[string]interface{}{
				// orbit.io/proxy=true is load-bearing: Reconciler
				// (internal/proxy/reconciler.go) filters out any
				// container carrying it, so the shared proxy is never
				// mistaken for one of its own backends. No singular
				// orbit.io/service value — this container fronts N
				// services, not one — orbit.io/services below is
				// purely informational (docker ps/inspect ergonomics),
				// consumed by no runtime code.
				"orbit.io/proxy":    "true",
				"orbit.io/managed":  "true",
				"orbit.io/services": strings.Join(names, ","),
			},
			"restart": "unless-stopped",
			// See buildLegacyProxyService's identical field for why.
			"pull_policy": "always",
			"volumes": toRawSlice([]string{
				"/var/run/docker.sock:/var/run/docker.sock:ro",
				sharedStateVolumeName + ":/var/lib/orbit",
				// services.json companion file — cmd/docker-orbit's
				// generateCmd writes it alongside the compose file
				// this service is generated into; mounted read-only,
				// matching every other config-surface mount here.
				servicesConfigHostFile + ":" + servicesConfigMountPath + ":ro",
			}),
		},
	}, binds
}

// SharedServicesConfigFileName is the bare filename of the services.json
// companion file a shared-proxy deployment needs alongside its generated
// compose file. Exported so cmd/docker-orbit's generateCmd — the only
// place that performs file I/O in this whole pipeline, per Generate's
// existing pure-transformation contract — knows exactly what to name the
// file it writes; the two must agree, so one exported constant is the
// single source of truth rather than a literal duplicated in two packages.
const SharedServicesConfigFileName = "docker-rollout-services.json"

// servicesConfigHostFile is the relative path form written into the
// generated compose file's volume mount — resolved by Docker Compose
// relative to the compose file's own directory, the same convention every
// other bind mount in this generator already relies on.
const servicesConfigHostFile = "./" + SharedServicesConfigFileName

// servicesConfigMountPath matches internal/config.DefaultServicesConfigPath
// exactly — duplicated as a literal (not imported) so internal/compose
// never depends on internal/config, keeping the two packages peers per
// the existing architecture (cmd/docker-orbit is the integration point
// that already imports both).
const servicesConfigMountPath = "/etc/orbit/services.json"

// ── Helpers ───────────────────────────────────────────────────────────────────

// ParsePort exports parsePort for callers outside this package (e.g. `docker
// orbit doctor`'s port-availability check) that need the same host/container
// port parsing Generate uses — reusing this instead of re-implementing
// Compose's port-string forms a second time.
func ParsePort(s string) (host, container int, err error) { return parsePort(s) }

// parsePort parses a Compose port mapping string into (hostPort, containerPort).
// Supported forms:
//   - "3000:3000"        → (3000, 3000)
//   - "3000"             → (3000, 3000)  single port is both host and container
//   - "0.0.0.0:3000:3000" → (3000, 3000)  IP prefix stripped
func parsePort(s string) (host, container int, err error) {
	// Strip optional IP prefix (e.g. "0.0.0.0:8080:8080").
	if strings.Count(s, ":") > 1 {
		idx := strings.Index(s, ":")
		s = s[idx+1:]
	}

	parts := strings.SplitN(s, ":", 2)
	switch len(parts) {
	case 1:
		n, e := strconv.Atoi(parts[0])
		if e != nil {
			return 0, 0, fmt.Errorf("invalid port number %q", parts[0])
		}
		if !isValidPort(n) {
			return 0, 0, fmt.Errorf("port %d out of range (1-65535)", n)
		}
		return n, n, nil
	case 2:
		h, e := strconv.Atoi(parts[0])
		if e != nil {
			return 0, 0, fmt.Errorf("invalid host port %q", parts[0])
		}
		if !isValidPort(h) {
			return 0, 0, fmt.Errorf("host port %d out of range (1-65535)", h)
		}
		c, e := strconv.Atoi(parts[1])
		if e != nil {
			return 0, 0, fmt.Errorf("invalid container port %q", parts[1])
		}
		if !isValidPort(c) {
			return 0, 0, fmt.Errorf("container port %d out of range (1-65535)", c)
		}
		return h, c, nil
	}
	return 0, 0, fmt.Errorf("unrecognised port format %q", s)
}

// isValidPort reports whether n is a valid TCP/UDP port number.
func isValidPort(n int) bool {
	return n >= 1 && n <= 65535
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func copyService(svc Service) Service {
	out := svc
	// Deep copy maps and slices so mutations don't affect the input.
	out.Ports = copyStrSlice(svc.Ports)
	out.Expose = copyStrSlice(svc.Expose)
	out.Networks = copyStrSlice(svc.Networks)
	out.DependsOn = copyStrSlice(svc.DependsOn)
	out.Environment = copyStrMap(svc.Environment)
	out.RawFields = deepCopyMap(svc.RawFields)
	return out
}

func copyStrSlice(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

func copyStrMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// stateVolumeName returns the named volume backing a proxied service's
// ORBIT_STATE_DIR. One volume per service, matching the one-proxy-per-service
// model: each proxy only ever writes files for its own service name.
func stateVolumeName(service string) string {
	return "docker_rollout_state_" + service
}

// toRawSlice converts []string to []interface{} for storage in RawFields.
// Necessary because RawFields values are map[string]interface{} and yaml
// marshals []interface{} correctly while []string inside interface{} may not.
func toRawSlice(s []string) []interface{} {
	out := make([]interface{}, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// toRawMap converts map[string]string to map[string]interface{} for RawFields.
func toRawMap(m map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func deepCopyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v // shallow copy of values is sufficient for our use-case
	}
	return out
}
