// Package main is the Orbit CLI entrypoint.
//
// docker-orbit ships as a single binary that works in two ways:
//
//  1. Standalone: `docker-orbit generate`, `docker-orbit rollout web`, …
//  2. Docker CLI plugin: `docker orbit web` (binary named docker-orbit)
//
// Plugin mode is detected automatically via argv[0] or the
// DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND environment variable.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/docker-secret-operator/orbit/internal/api"
	"github.com/docker-secret-operator/orbit/internal/compose"
	"github.com/docker-secret-operator/orbit/internal/config"
	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/plugin"
	"github.com/docker-secret-operator/orbit/internal/proxy"
	"github.com/docker-secret-operator/orbit/internal/rollout"
	"github.com/docker-secret-operator/orbit/internal/state"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// version is injected at build time via -ldflags "-X main.version=...".
// Falls back to "dev" for `go run`/`go build` invocations that don't set it
// (e.g. local development). See Makefile's `build` target and
// docs/governance/RELEASES.md for the release-time injection process.
var version = "dev"

func main() {
	// Docker CLI plugin: handle metadata probe before anything else.
	if plugin.HandleMetadataRequest(version) {
		os.Exit(0)
	}
	// Strip the extra "rollout" arg injected by Docker in plugin mode.
	plugin.StripPluginArgs()

	log, _ := zap.NewProduction()
	defer log.Sync() //nolint:errcheck

	root := buildRoot(log)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// buildRoot constructs the full Cobra command tree.
func buildRoot(log *zap.Logger) *cobra.Command {
	root := &cobra.Command{
		Use:   "docker-orbit",
		Short: "Zero-downtime deployments for Docker Compose",
		Long: `docker orbit injects a built-in TCP proxy into your Docker Compose stack
so that container replacements happen without dropping a single connection.

No external proxy (Traefik, nginx) required.

Example:
  docker orbit generate                         # enhance docker-compose.yml
  docker compose -f docker-rollout-compose.yml up -d
  docker orbit rollout web                      # roll out a new version of 'web'
  docker orbit rollback web                     # restore previous version if deploy fails`,
		SilenceUsage: true,
		// Use stays "docker-orbit" (the actual binary/plugin filename Cobra
		// matches internally); this annotation only changes what --help
		// prints, so it reads as 'docker orbit ...' — how it's actually
		// invoked as a Docker CLI plugin — matching deploy/doctor/history/
		// status's Long/Example text, which already assume that form.
		Annotations: map[string]string{cobra.CommandDisplayNameAnnotation: "docker orbit"},
	}

	root.AddCommand(
		generateCmd(log),
		deployCmd(log),
		rolloutCmd(log),
		rollbackCmd(log),
		statusCmd(log),
		historyCmd(log),
		doctorCmd(log),
		recoverCmd(log),
		scaleCmd(log),
		proxyCmd(log),
		docsCmd(log),
		versionCmd(),
	)
	return root
}

// ── generate ─────────────────────────────────────────────────────────────────

func generateCmd(log *zap.Logger) *cobra.Command {
	var input, output string
	var sharedProxy bool

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate an Orbit-enhanced compose file from docker-compose.yml",
		Long: `Reads docker-compose.yml and writes docker-rollout-compose.yml.

The original file is never modified. The generated file:
  - Moves port bindings from app services to docker-rollout-proxy-<service>
  - Adds a docker_rollout_mesh bridge network
  - Injects docker-rollout labels for service tracking

Auto-detection rules (per service, first match wins):
  1. x-docker-rollout: skip: true  → pass through unchanged
  2. No ports declared       → pass through unchanged
  3. Known database image    → pass through (with warning)
  4. Everything else         → proxy injected

--shared-proxy generates a single shared proxy fronting every eligible
service instead of one proxy per service (ADR-0006), plus a
docker-rollout-services.json companion file the shared proxy reads at
startup. Opt-in and off by default — omitting the flag produces exactly
the same output as before, so existing deployments are never silently
switched to the new topology by regenerating. Only the default service
(the first, alphabetically, among those proxied) is reachable via
unscoped 'docker orbit rollout/recover/status --control-addr' today —
targeting any other service through the shared proxy's control API
requires the service-scoped routes, not yet implemented.

Example:
  docker orbit generate
  docker orbit generate --file docker-compose.prod.yml --output docker-rollout-compose.prod.yml
  docker orbit generate --shared-proxy`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := compose.ParseFile(input)
			if err != nil {
				return fmt.Errorf("generate: %w", err)
			}

			generate := compose.Generate
			if sharedProxy {
				generate = compose.GenerateShared
			}
			out, sum, err := generate(cf)
			if err != nil {
				return fmt.Errorf("generate: %w", err)
			}

			// Emit summary.
			fmt.Fprintf(os.Stderr, "Parsed %d service(s) — %d eligible for proxy injection\n\n",
				len(cf.Services), len(sum.Proxied))
			fmt.Fprintln(os.Stderr, "Orbit Transform Summary:")
			for _, svc := range sum.Proxied {
				fmt.Fprintf(os.Stderr, "  ✓ Enabling zero-downtime for service '%s'\n", svc)
			}
			for _, svc := range sum.Skipped {
				fmt.Fprintf(os.Stderr, "  ⚠ Skipped '%s' (known database image)\n", svc)
			}

			if err := writeComposeFile(output, out); err != nil {
				return fmt.Errorf("generate: write %s: %w", output, err)
			}
			fmt.Fprintf(os.Stderr, "\nGenerated: %s\n", output)

			if sharedProxy && len(sum.SharedProxyBinds) > 0 {
				servicesPath := filepath.Join(filepath.Dir(output), compose.SharedServicesConfigFileName)
				if err := writeServicesConfig(servicesPath, sum.SharedProxyBinds); err != nil {
					return fmt.Errorf("generate: write %s: %w", servicesPath, err)
				}
				fmt.Fprintf(os.Stderr, "Generated: %s (shared proxy, %d service(s))\n", servicesPath, len(sum.Proxied))
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&input, "file", "f", "docker-compose.yml", "Input compose file")
	cmd.Flags().StringVarP(&output, "output", "o", "docker-rollout-compose.yml", "Output file path")
	cmd.Flags().BoolVar(&sharedProxy, "shared-proxy", false, "Generate a single shared proxy fronting every eligible service, instead of one proxy per service (ADR-0006)")
	return cmd
}

// writeServicesConfig builds a config.ServicesConfig from the per-service
// port bindings compose.GenerateShared returns (grouped by service,
// sorted for deterministic file output) and writes it as indented JSON —
// the exact shape internal/config.ResolveServicesConfig reads at shared-
// proxy startup, read-only-mounted into the container this same generate
// invocation just wrote a volume entry for.
func writeServicesConfig(path string, binds []compose.PortBinding) error {
	byService := make(map[string][]config.PortBinding)
	for _, b := range binds {
		byService[b.Service] = append(byService[b.Service], config.PortBinding{
			ListenPort: b.ListenPort,
			TargetPort: b.TargetPort,
		})
	}
	names := make([]string, 0, len(byService))
	for name := range byService {
		names = append(names, name)
	}
	sort.Strings(names)

	sc := config.ServicesConfig{Services: make([]config.ServiceConfig, 0, len(names))}
	for _, name := range names {
		sc.Services = append(sc.Services, config.ServiceConfig{Name: name, Binds: byService[name]})
	}

	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal services config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// resolveAPIToken returns flagValue if the operator passed --api-token
// explicitly, otherwise falls back to ORBIT_API_TOKEN. The --api-token flag
// itself must default to "" (never os.Getenv directly) — pflag prints a
// flag's default verbatim in --help text, so wiring the env var straight
// into the flag default would print the live secret to anyone who runs
// --help with the token exported, and into docs/cli-reference/*.md via
// `docker orbit docs`.
func resolveAPIToken(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("ORBIT_API_TOKEN")
}

// ── rollout ───────────────────────────────────────────────────────────────────

func rolloutCmd(log *zap.Logger) *cobra.Command {
	var opts rollout.Options
	var forceUnlock bool

	cmd := &cobra.Command{
		Use:   "rollout <service>",
		Short: "Zero-downtime rolling update for a service",
		Long: `Performs a zero-downtime rolling update for the named service.

Steps:
  1. Acquire rollout lock (prevents concurrent rollouts for the same service)
  2. Optional: pull latest image
  3. Scale service +1 (start new container)
  4. Wait for new container healthcheck to pass
  5. Register new container with the docker-rollout proxy
  6. Save rollout state to /tmp (enables rollback if deploy fails)
  7. Watch the new container for --stability before touching the old one
  8. Drain period — in-flight requests complete on old container
  9. Deregister old container from proxy
  10. Scale back to original count

If the new container fails its healthcheck within --timeout, docker orbit scales
back to 1 automatically without disrupting traffic. If it fails during the
--stability window (after being registered but before the old container is
touched), docker orbit rolls back automatically — the old container was never
drained, so nothing needs to be restored.

Example:
  docker orbit rollout web
  docker orbit rollout web --pull --timeout 120s --drain 10s --stability 15s`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Service = args[0]
			opts.APIToken = resolveAPIToken(opts.APIToken)

			// Acquire lock (with optional force).
			var lock *rollout.FileLock
			var err error

			if forceUnlock {
				lock, err = rollout.AcquireLockForce(opts.Service)
			} else {
				lock, err = rollout.AcquireLock(opts.Service)
			}

			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
				return err
			}
			defer lock.Release() //nolint:errcheck // deferred lock release on exit; error not actionable

			// Verify the service exists in docker-rollout-compose.yml.
			cf, err := compose.ParseFile(opts.ComposeFile)
			if err != nil {
				return fmt.Errorf("rollout: read compose file: %w\n(did you run docker orbit generate first?)", err)
			}
			if _, ok := cf.Services[opts.Service]; !ok {
				return fmt.Errorf("rollout: service %q not found in %s\n(did you run docker orbit generate first?)",
					opts.Service, opts.ComposeFile)
			}

			ctx, cancel := signal.NotifyContext(context.Background(),
				syscall.SIGTERM, syscall.SIGINT)
			defer cancel()

			return rollout.Run(ctx, opts, log)
		},
	}

	cmd.Flags().StringVarP(&opts.ComposeFile, "file", "f", "docker-rollout-compose.yml", "docker-rollout compose file")
	cmd.Flags().BoolVar(&opts.Pull, "pull", false, "Pull latest image before rolling out")
	cmd.Flags().DurationVarP(&opts.Timeout, "timeout", "t", 60*time.Second, "Healthcheck timeout")
	cmd.Flags().DurationVarP(&opts.Drain, "drain", "d", 5*time.Second, "Drain period before removing old container")
	cmd.Flags().DurationVar(&opts.StabilityWindow, "stability", 10*time.Second, "How long to watch the new backend before draining the old one; auto-rolls back if it fails")
	cmd.Flags().StringVar(&opts.ControlAddr, "control-addr", "http://localhost:9900", "Proxy control API address")
	cmd.Flags().StringVar(&opts.APIToken, "api-token", "", "Control API bearer token (falls back to ORBIT_API_TOKEN)")
	cmd.Flags().BoolVar(&forceUnlock, "force-unlock", false, "Force unlock if previous process died (ONLY use after verifying process is gone)")
	return cmd
}

// ── rollback ──────────────────────────────────────────────────────────────────

// rollbackCmd is defined in rollback.go.

// ── status ────────────────────────────────────────────────────────────────────

// statusCmd is defined in status.go.

// ── scale ─────────────────────────────────────────────────────────────────────

func scaleCmd(log *zap.Logger) *cobra.Command {
	var opts rollout.Options

	cmd := &cobra.Command{
		Use:    "scale <service> <n>",
		Short:  "Register n replicas of a service with the proxy",
		Hidden: true, // not yet implemented; hidden so it doesn't appear in --help
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("scale is not yet implemented — use 'docker orbit rollout' to deploy new versions")
		},
	}
	cmd.Flags().StringVarP(&opts.ComposeFile, "file", "f", "docker-rollout-compose.yml", "docker-rollout compose file")
	return cmd
}

// ── proxy (internal — runs inside the docker-rollout-proxy container) ─────────────────

func proxyCmd(log *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "proxy",
		Short:  "Start the built-in TCP proxy (runs inside the proxy container)",
		Hidden: true, // internal; not part of the user-facing CLI
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProxy(log, version)
		},
	}
	return cmd
}

func runProxy(log *zap.Logger, version string) error {
	// Load configuration from environment.
	cfg, err := config.LoadProxyConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: config failed: %v\n", err)
		return err
	}

	log.Info("proxy: starting",
		zap.String("instance", cfg.ProxyInstance),
		zap.String("control_port", cfg.ControlPort))

	// Create metrics.
	m := metrics.New()

	// ADR-0006 Stage 3.5: services.json (or, if absent, cfg.ProxyInstance/
	// cfg.Binds synthesized into an equivalent single-service config —
	// see config.ResolveServicesConfig) replaces the implicit
	// single-service model as the source of truth for which services this
	// process fronts and which ports each one owns.
	servicesCfg, err := config.ResolveServicesConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: services config failed: %v\n", err)
		return err
	}

	srv := proxy.NewServer(log, m)
	pr, reg, err := wireProjectRegistry(srv, m, cfg.ProxyInstance, servicesCfg, log)
	if err != nil {
		log.Error("proxy: startup failed", zap.Error(err))
		return err
	}

	// Initialize state manager for persistent state operations.
	sm := state.NewStateManager(cfg.StateDir, log)

	// Start control API with security.
	controlSrv := api.NewControlServer(pr, cfg.ProxyInstance, srv, log, m, cfg.APIToken, sm)
	controlSrv.SetTransitionTimeout(cfg.TransitionTimeout)

	// Wire the debug/status data source (internal/metrics.MetricsCollector,
	// distinct from the *metrics.Proxy connection counters above) so
	// GET /status and `docker orbit status` report real generation/rollout/
	// recovery state instead of an empty DebugHandler.
	mc := metrics.NewMetricsCollector()
	debugHandler := api.NewDebugHandler(sm, mc)
	controlSrv.SetDebugHandler(debugHandler, cfg.ProxyInstance, version)

	go func() {
		if err := controlSrv.ListenAndServe(":" + cfg.ControlPort); err != nil {
			log.Error("control API stopped", zap.Error(err))
		}
	}()
	defer controlSrv.Close() //nolint:errcheck // deferred control-server close on shutdown; error not actionable

	// PHASE 3: Generation-centric recovery with persistent state.
	// Mark as recovering immediately so readiness probe reflects boot state.
	controlSrv.SetStartupState(proxy.StartupRecovering)

	// Wire the on-demand trigger before the startup pass runs, so `docker
	// orbit recover` is available the moment the control API starts
	// listening, not only after startup recovery finishes.
	controlSrv.SetRecoveryTrigger(func(ctx context.Context) (api.RecoveryOutcome, error) {
		// ADR-0006 Stage 3.4: executeRecoveryForProject orchestrates — it
		// loops over pr.Services() (today: exactly cfg.ProxyInstance) and
		// calls the unmodified executeRecovery once per service. Indexing
		// the result map by cfg.ProxyInstance is the direct, minimal
		// translation of "there is exactly one service" into this map
		// shape — not a new aggregation policy. A true multi-service
		// RecoveryOutcome shape is later work (this stage does not modify
		// ControlServer or api.RecoveryOutcome).
		results := executeRecoveryForProject(ctx, cfg, sm, pr, mc, debugHandler, log)
		result := results[cfg.ProxyInstance]
		if result.Err != nil {
			return api.RecoveryOutcome{}, result.Err
		}
		outcome := recoveryOutcomeFor(result)
		controlSrv.SetStartupState(result.State)
		return outcome, nil
	})

	recoveryCtx, cancel := context.WithTimeout(context.Background(), cfg.StartupTimeout)
	startupResults := executeRecoveryForProject(recoveryCtx, cfg, sm, pr, mc, debugHandler, log)
	startupState := startupResults[cfg.ProxyInstance].State
	cancel()

	// Set control server startup state for readiness endpoint.
	// CRITICAL: /health/ready must reflect actual state. Even failed startups
	// proceed to serve traffic — the readiness endpoint reflects actual state.
	controlSrv.SetStartupState(startupState)
	log.Info("proxy: startup complete",
		zap.String("state", string(startupState)),
		zap.String("behavior", "accepting traffic - readiness reflects actual state"))

	// Wait for shutdown signal.
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// ── Runtime capability activation (WP-B2) ────────────────────────────
	// Passive-failover execution now exists, so the Continuous Health
	// Controller may be activated — but ONLY through the Runtime Feature Gate,
	// which deterministically enforces every prerequisite. No direct startup
	// wiring bypasses the gate (Runtime Constitution activation model).
	features := proxy.NewRuntimeFeatures(m)
	if err := features.Enable(proxy.FeatureContinuousHealth, proxy.ImplementedPrerequisites()); err != nil {
		log.Warn("runtime: continuous health NOT activated (prerequisites unmet)", zap.Error(err))
	} else {
		// Zero-backend protection: a refused demotion emits a warning + metric.
		// Set directly on reg (unchanged) — ProjectHealthController's internal
		// HealthController for this service wraps this exact *Registry
		// instance (via pr.For), so the hook fires exactly as it did before
		// this stage; nothing about SetHealthGuarded's behavior changed.
		//
		// Scope (ADR-0006 Stage 3.5): this hook is set on cfg.ProxyInstance's
		// own Registry only, not on every service wireProjectRegistry may
		// have registered into pr. That's intentional for this stage —
		// Stage 3.5 is configuration-only and does not extend runtime
		// orchestration — not an oversight. Stage 4 is where per-service
		// orchestration across every configured service (not just
		// cfg.ProxyInstance) gets built out.
		reg.SetZeroBackendHook(func(id string) {
			m.IncZeroBackendProtection()
			log.Warn("runtime: zero-backend protection kept the last active backend",
				zap.String("backend", id))
		})
		// ADR-0006 Stage 3.3: ProjectHealthController orchestrates — it is not
		// a second health implementation, it constructs one unmodified
		// HealthController per service internally (today: exactly one, this
		// service) and drives it with the identical config. See II-4: one
		// ticker, one goroutine, sequential iteration — not a goroutine per
		// service.
		phc := proxy.NewProjectHealthController(pr, nil, proxy.DefaultHealthControllerConfig(), m, log)
		go phc.Run(ctx) // stops when the shutdown signal cancels ctx
		log.Info("runtime: continuous health controller activated via feature gate")
	}

	// ADR-0006 Stage 4: Reconciler (PR 4.2, unchanged by this stage) is a
	// second, independent membership-convergence mechanism layered on top
	// of the existing recovery loop (executeRecoveryForProject, unchanged,
	// above) — the periodic safety net that corrects whatever the events
	// fast path below misses. Purely additive: it only ever adds/removes
	// backends to match Docker's current container membership, never
	// evicts on health or changes a routing decision, so — unlike
	// FeatureContinuousHealth above — it does not go through the
	// RuntimeFeatures activation gate.
	//
	// PR 4.3 adds EventSource: Docker Events as a notification mechanism,
	// never a source of truth (INV-4) — it only ever decides *when*
	// Reconciler.ReconcileOnce runs sooner, never mutates a Registry
	// itself. EventSource now owns the periodic tick that used to be
	// Reconciler.Run's job (still valid, still tested, just no longer the
	// production driver) — this is the single serialization point a
	// second reconciliation trigger requires: one goroutine, one select
	// loop, so ReconcileOnce can never run concurrently with itself.
	//
	// PR 4.4: eventSourceWG is waited on before reconcilerDocker.Close()
	// fires (below, at shutdown) — es.Run stopping when ctx is cancelled
	// is necessary but not sufficient, since "stops eventually" and
	// "closing the client it's still using is now safe" are different
	// claims. Without this, Close() could run concurrently with es.Run
	// still mid-ReconcileOnce, closing the Docker client out from under
	// it.
	var eventSourceWG sync.WaitGroup
	reconcilerDocker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Warn("runtime: reconciler NOT activated (docker client unavailable)", zap.Error(err))
	} else {
		defer reconcilerDocker.Close() //nolint:errcheck // deferred docker client close on shutdown; error not actionable
		rc := proxy.NewReconciler(pr, reconcilerDocker, m, log)
		es := proxy.NewEventSource(rc, reconcilerDocker, cfg.ReconcileInterval, m, log)
		eventSourceWG.Add(1)
		go func() {
			defer eventSourceWG.Done()
			es.Run(ctx) // stops when the shutdown signal cancels ctx
		}()
		log.Info("runtime: reconciler + event source activated", zap.Duration("periodic_interval", cfg.ReconcileInterval))
	}

	// Periodic rediscovery. HealthController (above) only re-checks backends
	// already in the registry — by its own Layer 5 contract it must never
	// touch Docker lifecycle — so a service that never got past initial
	// recovery (slower than the startup budget) or one whose only backend
	// later crashes has nothing left to bring it back except a manual
	// `docker orbit recover`. Goes through controlSrv.TriggerRecovery — the
	// same recoveryMu/recoveryInFlight-guarded path POST /recover uses —
	// rather than calling executeRecoveryForProject directly: this ticker
	// and a concurrent POST /recover must never both run a recovery pass at
	// once (each would independently write persisted state, last-write-wins,
	// and double-count metrics). TriggerRecovery already calls
	// controlSrv.SetStartupState internally (see the SetRecoveryTrigger
	// closure above), so this loop doesn't need to.
	//
	// Scope (ADR-0006 Stage 3.5): the emptiness gate below (reg.Active())
	// only covers cfg.ProxyInstance's own service, not every service
	// wireProjectRegistry may have registered into pr. Intentional for this
	// stage — Stage 3.5 is configuration-only and does not extend runtime
	// orchestration. Stage 4 is where this gate is expected to generalize to
	// iterate every service in pr.Services(), the same pattern
	// executeRecoveryForProject and ProjectHealthController already use.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if len(reg.Active()) > 0 {
					continue
				}
				log.Info("runtime: zero active backends, attempting rediscovery")
				if _, err := controlSrv.TriggerRecovery(ctx); err != nil {
					if errors.Is(err, api.ErrRecoveryInFlight) {
						log.Info("runtime: rediscovery skipped — a recovery pass is already in progress")
					} else {
						log.Warn("runtime: rediscovery attempt failed", zap.Error(err))
					}
				}
			}
		}
	}()

	<-ctx.Done()

	log.Info("proxy: shutdown signal received")

	// Graceful drain: stop accepting new connections, wait for in-flight ones.
	if err := srv.CloseGraceful(cfg.DrainTimeout); err != nil {
		log.Warn("proxy: graceful drain timeout, forcing close",
			zap.Error(err))
		srv.Close()
	}

	// Wait for EventSource to actually stop — not just for ctx to be
	// cancelled — before the deferred reconcilerDocker.Close() above runs.
	// EventSource logs "eventsource: stopped" itself once this returns.
	// No fixed timeout: shutdown is bounded by cfg.DrainTimeout above and
	// by ReconcileOnce's own bounded Docker calls, not by anything this
	// wait could hang on indefinitely on its own.
	eventSourceWG.Wait()

	log.Info("proxy: shutdown complete")
	return nil
}

// wireProjectRegistry constructs one *proxy.Registry/*proxy.Router pair per
// service in sc, binds that service's ports on srv, and registers each
// Registry into a new ProjectRegistry (ADR-0006 Stage 3.5). This is
// configuration→construction wiring only: it never inspects Docker, never
// decides health or recovery, and never registers a backend — it builds
// the empty per-service Registry instances every other subsystem
// (ProjectHealthController, executeRecoveryForProject, ControlServer) then
// discovers backends into, exactly as a single-service runProxy already
// did before this stage.
//
// Extracted out of runProxy (which cannot be unit-tested directly: it
// binds real ports, starts an HTTP server, and blocks on a shutdown
// signal) so this wiring is independently testable.
//
// Returns the constructed ProjectRegistry, plus defaultService's own
// *proxy.Registry (an error if defaultService — cfg.ProxyInstance — isn't
// among sc's services, which config.ResolveServicesConfig's own synthesis
// path guarantees never happens for the single-service compatibility case,
// only possible with a hand-authored services.json that omits it). Every
// other line in runProxy that references a single service's Registry
// directly (the zero-backend hook, the periodic-rediscovery gate) keeps
// using exactly this one *proxy.Registry, unchanged — Stage 3.5 changes
// only how registries are constructed and registered, not any runtime
// behavior after registration.
func wireProjectRegistry(
	srv *proxy.Server,
	m *metrics.Proxy,
	defaultService string,
	sc *config.ServicesConfig,
	log *zap.Logger,
) (*proxy.ProjectRegistry, *proxy.Registry, error) {
	pr := proxy.NewProjectRegistry()
	var defaultReg *proxy.Registry

	for _, svc := range sc.Services {
		svcReg := proxy.NewRegistry()
		svcReg.SetMetrics(m)
		svcRouter := proxy.NewRouter(svcReg)
		svcRouter.SetMetrics(m)

		for _, binding := range svc.Binds {
			pb := proxy.PortBinding{ListenPort: binding.ListenPort, TargetPort: binding.TargetPort, Service: svc.Name}
			if err := srv.Bind(pb, svcRouter); err != nil {
				return nil, nil, fmt.Errorf("service %q: bind failed: %w", svc.Name, err)
			}
		}

		pr.Register(svc.Name, svcReg)
		log.Info("proxy: service configured",
			zap.String("service", svc.Name),
			zap.Int("binds", len(svc.Binds)))

		if svc.Name == defaultService {
			defaultReg = svcReg
		}
	}

	if defaultReg == nil {
		return nil, nil, fmt.Errorf("configured instance %q not found among the %d service(s) in services.json", defaultService, len(sc.Services))
	}
	return pr, defaultReg, nil
}

// executeRecovery performs one real recovery pass for service: load
// persisted active-generation/rollout state, discover live backends from
// Docker, generate a deterministic recovery plan (state.GenerateRecoveryPlan),
// and register validated candidates with reg. This is the entire recovery
// implementation — called once at proxy startup (runProxy, above) and
// on-demand via POST /recover (wired through
// api.ControlServer.SetRecoveryTrigger, also in runProxy), as well as, per
// service, from executeRecoveryForProject's loop (ADR-0006 Stage 2.3). All
// call sites run this exact function; there is no second implementation to
// drift out of sync with the first.
//
// service identifies which service this pass recovers — it is what was
// cfg.ProxyInstance before Stage 2.3 made it an explicit parameter, so a
// single-service caller passes cfg.ProxyInstance unchanged (identical
// behavior) while executeRecoveryForProject passes each service name in
// turn. Every other cfg field used below (TCPDialTimeout, StartupTimeout,
// TransitionTimeout) is process-wide configuration, not per-service, and is
// still read from cfg exactly as before — nothing about the recovery
// algorithm, planner, persisted-state schema, authority semantics, or
// transaction semantics changed; only which identity string labels the pass.
//
// Returns the resulting proxy.StartupState, the RecoveryPlan (nil only if
// something prevented plan generation entirely, which does not happen in
// the current implementation but is left nil-checked for callers), and the
// RecoveryResult backend health snapshot (for BackendsRestored accounting).
// err is reserved for future use — every failure mode today is absorbed into
// a degraded/failed StartupState rather than a hard error, matching the
// pre-existing startup behavior this replaces.
// forceDegradedOnStateCorruption overrides plan to a safe Degraded outcome
// when either persisted-state load failed with genuine on-disk corruption
// (a *state.StateLoadError, distinct from the ordinary nil/nil "no state
// file yet" case) — see the go-live audit's finding H5. Without this,
// GenerateRecoveryPlan received a nil activeGenState/rolloutState
// indistinguishable from a fresh install and would happily infer authority
// from live health, discarding whatever the corrupted file actually
// recorded (e.g. which generation was authoritative mid-rollout) in favor
// of a different, possibly wrong, generation. No-op when plan is nil or
// neither error is a StateLoadError.
func forceDegradedOnStateCorruption(plan *state.RecoveryPlan, activeGenLoadErr, rolloutLoadErr error) *state.RecoveryPlan {
	if plan == nil {
		return plan
	}

	var reasons []string
	if sle, ok := activeGenLoadErr.(*state.StateLoadError); ok && sle.IsFatal {
		reasons = append(reasons, fmt.Sprintf("active generation state corrupted: %s", sle.Reason))
	}
	if sle, ok := rolloutLoadErr.(*state.StateLoadError); ok && sle.IsFatal {
		reasons = append(reasons, fmt.Sprintf("rollout state corrupted: %s", sle.Reason))
	}
	if len(reasons) == 0 {
		return plan
	}

	reason := "refusing to infer authority after state corruption (never guess): " + strings.Join(reasons, "; ")
	plan.Action = state.RecoveryDegraded
	plan.AuthoritativeGeneration = ""
	plan.BackendsToRestore = nil
	plan.FailedReason = reason
	plan.Reason = reason
	return plan
}

func executeRecovery(
	ctx context.Context,
	cfg *config.ProxyConfig,
	sm *state.StateManager,
	reg *proxy.Registry,
	service string,
	mc *metrics.MetricsCollector,
	debugHandler *api.DebugHandler,
	log *zap.Logger,
) (proxy.StartupState, *state.RecoveryPlan, *proxy.RecoveryResult, error) {
	// Time the recovery pass for MetricsCollector / `docker orbit status`.
	recoveryDone := mc.RecordRecoveryStart()

	var recoveryResult *proxy.RecoveryResult
	var plan *state.RecoveryPlan
	startupState := proxy.StartupRecovering

	// Load persistent states (ActiveGenerationState, RolloutState). Both
	// return (nil, nil) when no state file exists yet — a non-nil error here
	// means real corruption or an I/O failure, which must not be silently
	// treated the same as "no prior state".
	activeGenState, activeGenLoadErr := sm.LoadActiveGenerationState(service)
	if activeGenLoadErr != nil {
		log.Error("recovery: active generation state unreadable, proceeding as if absent",
			zap.Error(activeGenLoadErr))
	}
	rolloutState, rolloutLoadErr := sm.LoadRolloutState(service)
	if rolloutLoadErr != nil {
		log.Error("recovery: rollout state unreadable, proceeding as if absent",
			zap.Error(rolloutLoadErr))
	}
	debugHandler.RecordActiveGenState(service, activeGenState)
	debugHandler.RecordRolloutState(service, rolloutState)

	// Discover backends and build inventory snapshot. One retry budget
	// (bounded to cfg.StartupTimeout, not the caller's ctx — the on-demand
	// `docker orbit recover` path hits this via a bare HTTP request context
	// with no deadline of its own) covers both connecting to the daemon and
	// the discovery/health pass below, so a daemon that's transiently
	// restarting gets the same chance to recover as a slow-starting backend.
	retryCtx, retryCancel := context.WithTimeout(ctx, cfg.StartupTimeout)
	defer retryCancel()

	var source *proxy.DockerRecoverySource
	source, err := proxy.NewDockerRecoverySourceWithConfig(service, log, cfg.TCPDialTimeout, 10)
	for err != nil && retryCtx.Err() == nil {
		log.Warn("recovery: docker daemon unreachable, retrying within startup budget",
			zap.Error(err))
		select {
		case <-time.After(time.Second):
		case <-retryCtx.Done():
		}
		source, err = proxy.NewDockerRecoverySourceWithConfig(service, log, cfg.TCPDialTimeout, 10)
	}
	if err != nil {
		log.Warn("recovery: docker unavailable, generating degraded plan",
			zap.Error(err))
		// Proceed with empty inventory (state-only recovery)
		startupState = proxy.StartupRecovering
	} else {
		defer source.Close() //nolint:errcheck // deferred source close; error not actionable

		// Trusted-authority fast path (docs/governance/AUTHORITY-LIFECYCLE.md):
		// if persisted state names a specific backend ID, verify it directly
		// against Docker before falling back to the broad label-based scan
		// below — a label-based scan can never find a per-rollout backend ID
		// (the label is static per service, identical across every replica),
		// so without this, persisted authority would always look stale.
		var provenStale bool
		recoveryResult, provenStale = directVerifyRecoveryResult(retryCtx, source, activeGenState, rolloutState, log)
		if provenStale {
			// Direct-verify didn't just find "nothing to check" — it proved
			// the persisted ID no longer corresponds to a live container
			// (e.g. every container ID changed after a host reboot). The
			// label-based scan below will still correctly find a healthy
			// backend under its current label, but determineAuthority
			// (internal/state) trusts activeGenState/rolloutState
			// unconditionally when non-nil — it has no way to know they
			// were just disproven. Passing them through unchanged here
			// makes GenerateRecoveryPlan compare a live, label-keyed
			// inventory against a dead, ID-keyed authority string that can
			// never match, producing a false RecoveryDegraded with a
			// perfectly healthy backend sitting right there — and since
			// this reloads the same stale file every time, it never
			// self-heals, including through the periodic rediscovery loop
			// in runProxy. Treating persisted state as absent for the rest
			// of this pass is what actually makes the fallback a fallback.
			activeGenState = nil
			rolloutState = nil
		}

		// Discover and validate backends with health checks. A backend that
		// hasn't opened its port yet (e.g. Grafana takes several seconds to
		// boot, and has no Docker HEALTHCHECK to report "starting") fails a
		// single TCP dial and comes back StartupFailed — permanently, since
		// nothing else ever retries. A backend with its own Docker
		// HEALTHCHECK reports "starting" instead (StartupRecovering, e.g.
		// cadvisor) but is just as unregistered until it flips to healthy.
		// Retry both cases within the same budget spent above.
		needsRetry := func(res *proxy.RecoveryResult) bool {
			return res.HealthyCount == 0 &&
				(res.State == proxy.StartupFailed || res.State == proxy.StartupRecovering)
		}
		if recoveryResult == nil {
			recoveryResult, err = source.DiscoverAndValidateBackends(retryCtx)
		}
		for err == nil && needsRetry(recoveryResult) && retryCtx.Err() == nil {
			select {
			case <-time.After(time.Second):
			case <-retryCtx.Done():
			}
			recoveryResult, err = source.DiscoverAndValidateBackends(retryCtx)
		}
		if err != nil {
			log.Error("recovery: discovery failed",
				zap.Error(err))
			startupState = proxy.StartupFailed
		}
	}

	// Build GenerationInventory from discovery result.
	var inventory *state.GenerationInventory
	if recoveryResult != nil {
		inventory = buildGenerationInventory(service, recoveryResult, activeGenState)
	} else {
		// Empty inventory if discovery failed
		inventory = &state.GenerationInventory{
			Service:          service,
			GenerationStates: make(map[string]state.GenerationMetrics),
		}
	}

	// Build runtime-discovered backend snapshot (never persisted; rediscovered
	// from Docker on every recovery attempt).
	var backendSnapshots []state.BackendSnapshot
	if recoveryResult != nil {
		for _, backend := range recoveryResult.Backends {
			gen := backend.Generation
			if gen == "" {
				gen = service + "-default"
			}
			backendSnapshots = append(backendSnapshots, state.BackendSnapshot{
				Generation: gen,
				ID:         backend.ID,
				Addr:       backend.Addr,
				Health:     string(backend.Status),
			})
		}
	}

	// Generate deterministic recovery plan.
	plan = state.GenerateRecoveryPlan(sm, service, rolloutState, activeGenState, inventory, backendSnapshots, cfg.TransitionTimeout, log)
	plan = forceDegradedOnStateCorruption(plan, activeGenLoadErr, rolloutLoadErr)
	debugHandler.RecordRecoveryPlan(service, plan)

	// Execute recovery plan: register backends according to traffic roles.
	if plan != nil {
		log.Info("recovery: plan generated",
			zap.Uint64("epoch", plan.Epoch),
			zap.String("action", string(plan.Action)),
			zap.String("authority", plan.AuthoritativeGeneration),
			zap.String("reason", plan.Reason))

		registeredCount := 0
		for _, candidate := range plan.BackendsToRestore {
			if candidate.ValidityStatus != state.CandidateValid {
				log.Warn("recovery: skipping invalid backend candidate",
					zap.String("id", candidate.ID),
					zap.String("validity", string(candidate.ValidityStatus)))
				continue
			}

			// Revalidate before registration (lightweight, < 500ms).
			// Verify: container exists + health status still good.
			revalidateCtx, revalidateCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			valid := revalidateBackendHealth(revalidateCtx, source, &candidate, log)
			revalidateCancel()

			if !valid {
				log.Warn("recovery: backend failed revalidation, skipping",
					zap.String("id", candidate.ID),
					zap.String("addr", candidate.Addr))
				continue
			}

			b := proxy.Backend{
				ID:   candidate.ID,
				Addr: candidate.Addr,
			}
			if err := reg.Add(b); err != nil {
				log.Warn("recovery: could not register backend",
					zap.String("id", candidate.ID),
					zap.Error(err))
				continue
			}

			log.Info("recovery: registered backend",
				zap.String("id", candidate.ID),
				zap.String("generation", candidate.Generation),
				zap.String("traffic_role", string(candidate.TrafficRole)),
				zap.String("reason", candidate.Reason))
			registeredCount++
		}

		// Log recovery action details.
		switch plan.Action {
		case state.RecoveryRestoreSingle:
			log.Info("recovery: single generation restore",
				zap.String("generation", plan.AuthoritativeGeneration),
				zap.Int("backends", len(plan.BackendsToRestore)))

		case state.RecoveryRestoreWithDraining:
			log.Info("recovery: restore with draining",
				zap.String("generation", plan.AuthoritativeGeneration),
				zap.Strings("draining_generations", plan.TempDrainingGenerations),
				zap.Int("backends", len(plan.BackendsToRestore)))

		case state.RecoveryInferredFallback:
			log.Warn("recovery: inferred authority (no persistent state)",
				zap.String("generation", plan.AuthoritativeGeneration),
				zap.String("reason", plan.Reason))

			// This branch runs both when no persisted authority ever existed
			// (determineAuthority's Priority 1/2 require activeGenState/
			// rolloutState non-nil to skip inference) AND when direct-verify
			// just proved an existing one stale and this pass nulled it out
			// (see directVerifyRecoveryResult's provenStale doc) — the second
			// case means a real file, with a real revision, is still on disk.
			// Persist what was just inferred so the *next* boot gets a
			// trusted restore instead of inferring again — reading the
			// current on-disk revision first (same pattern
			// internal/api/authority.go's handlers use) so this correctly
			// overwrites a stale entry via CAS instead of being silently
			// rejected as a conflicting write. Same-process write (sm, not
			// the HTTP /authority/* path a separate CLI process must use) —
			// best-effort, never fails the recovery pass itself.
			if registeredCount > 0 {
				newActiveGen := &state.ActiveGenerationState{
					SchemaVersion:    state.SchemaVersion,
					Service:          service,
					ActiveGeneration: plan.AuthoritativeGeneration,
				}
				if current, loadErr := sm.LoadActiveGenerationState(service); loadErr == nil && current != nil {
					newActiveGen.PreviousRevision = current.Revision
				}
				if err := sm.WriteActiveGenerationState(newActiveGen, log); err != nil {
					log.Warn("recovery: could not persist inferred authority (non-fatal)", zap.Error(err))
				}
				// A stale in-flight RolloutState (the provenStale
				// transitioning case) is superseded by the fresh single-
				// generation authority just written above — clear it so a
				// future boot doesn't keep reloading and re-disproving the
				// same dead transition on every single recovery pass.
				if err := sm.DeleteRolloutState(service); err != nil {
					log.Warn("recovery: could not clear stale rollout state (non-fatal)", zap.Error(err))
				}
			}

		case state.RecoveryDegraded:
			log.Error("recovery: degraded - no healthy generations",
				zap.String("reason", plan.FailedReason))
			startupState = proxy.StartupFailed
		}
	}

	// Update startup state from recovery result if available.
	if recoveryResult != nil {
		startupState = recoveryResult.State
		log.Info("recovery: health state",
			zap.String("state", string(recoveryResult.State)),
			zap.Int("healthy", recoveryResult.HealthyCount),
			zap.Int("starting", recoveryResult.StartingCount),
			zap.Int("unhealthy", recoveryResult.UnhealthyCount))
	}

	// Record recovery outcome for MetricsCollector / `docker orbit status`.
	recoveryDone()
	if startupState == proxy.StartupFailed {
		mc.RecordRecoveryFailure()
	}
	rolloutPhase := ""
	if rolloutState != nil {
		rolloutPhase = string(rolloutState.Phase)
	}
	authority := ""
	if plan != nil {
		authority = plan.AuthoritativeGeneration
		if plan.Action == state.RecoveryInferredFallback {
			mc.RecordStaleTransition()
		}
	}
	if authority != "" && (activeGenState == nil || activeGenState.ActiveGeneration != authority) {
		previous := ""
		if activeGenState != nil {
			previous = activeGenState.ActiveGeneration
		}
		mc.RecordAuthorityTransition(previous, authority)
	}
	mc.SetCurrentState(authority, rolloutPhase, string(startupState), startupState == proxy.StartupDegraded || startupState == proxy.StartupFailed)

	return startupState, plan, recoveryResult, nil
}

// serviceRecoveryOutcome is one service's result from
// executeRecoveryForProject — the same four values executeRecovery returns,
// carried alongside the service name.
type serviceRecoveryOutcome struct {
	State  proxy.StartupState
	Plan   *state.RecoveryPlan
	Result *proxy.RecoveryResult
	Err    error
}

// executeRecoveryForProject runs executeRecovery once per service currently
// registered in pr (ADR-0006 Stage 2.3). It changes only WHERE recovery is
// invoked, never HOW: each iteration calls the exact same executeRecovery
// function a single-service proxy already uses, unmodified in algorithm,
// against that service's own Registry (via pr.For) and its own persisted
// state (state.StateManager is already keyed by service — see
// internal/state, untouched by this function).
//
// Per implementation invariant II-4, this is one loop in the caller's own
// goroutine — no goroutine per service, no worker pool, no channels.
// Services are visited in sorted order for determinism.
//
// Each service recovers completely independently, per the Recovery
// Invariants: its own Registry, its own persisted authority/rollout state,
// its own DockerRecoverySource, its own RecoveryPlan. One service's pass
// returning proxy.StartupFailed (or a non-nil err — reserved for future use,
// never populated by executeRecovery today) does not stop the loop; every
// other configured service still gets its own attempt. A service that
// disappears from pr between listing and lookup (concurrent Remove) is
// skipped for this pass, not treated as an error — the next pass simply
// won't see it either, exactly like ProjectHealthController.CheckOnce.
func executeRecoveryForProject(
	ctx context.Context,
	cfg *config.ProxyConfig,
	sm *state.StateManager,
	pr *proxy.ProjectRegistry,
	mc *metrics.MetricsCollector,
	debugHandler *api.DebugHandler,
	log *zap.Logger,
) map[string]serviceRecoveryOutcome {
	services := pr.Services()
	sort.Strings(services)

	results := make(map[string]serviceRecoveryOutcome, len(services))
	for _, service := range services {
		reg, ok := pr.For(service)
		if !ok {
			continue
		}
		// Scoped once per service and reused for this entire recovery pass
		// (Stage 2.4) — every log line executeRecovery already emits (plan
		// generated, backend registered, health state, discovery retries,
		// inferred-authority persistence, ...) is now attributable to this
		// service with zero changes to executeRecovery's own log call
		// sites: the field rides along on every call made through this
		// logger, not because each statement was individually edited.
		serviceLog := log.With(zap.String("service", service))
		st, plan, result, err := executeRecovery(ctx, cfg, sm, reg, service, mc, debugHandler, serviceLog)
		results[service] = serviceRecoveryOutcome{State: st, Plan: plan, Result: result, Err: err}
		// Deliberately no early return/break on err or a failed state — one
		// service's outcome must never prevent the remaining services from
		// getting their own recovery attempt.
	}
	return results
}

// recoveryOutcomeFor translates one service's serviceRecoveryOutcome (from
// executeRecoveryForProject's result map) into the api.RecoveryOutcome shape
// POST /recover returns. ADR-0006 Stage 3.4 — extracted out of runProxy's
// SetRecoveryTrigger closure, which cannot be unit-tested directly, so this
// translation is: it reshapes already-computed values (epoch, action,
// authority, reason, backend count) without recomputing or reinterpreting
// any of them — no recovery logic lives here.
func recoveryOutcomeFor(result serviceRecoveryOutcome) api.RecoveryOutcome {
	outcome := api.RecoveryOutcome{
		Timestamp:   time.Now().UTC(),
		ProxyStatus: string(result.State),
	}
	if result.Plan != nil {
		outcome.Epoch = result.Plan.Epoch
		outcome.Action = string(result.Plan.Action)
		outcome.AuthoritativeGeneration = result.Plan.AuthoritativeGeneration
		outcome.Reason = result.Plan.Reason
		outcome.FailedReason = result.Plan.FailedReason
		outcome.BackendsRestored = countRestoredBackends(result.Plan)
	}
	return outcome
}

// directVerifyRecoveryResult attempts to resolve persisted authority
// directly against Docker via source.VerifyBackendByID, bypassing the broad
// label-based scan. See docs/governance/AUTHORITY-LIFECYCLE.md and
// executeRecovery's call site for why this must run first, not as a
// fallback.
//
// Returns (nil, false) when there is nothing persisted to verify — the
// caller runs the label-based scan and trusts activeGenState/rolloutState
// exactly as it always has.
//
// Returns (nil, true) when persisted state exists but direct-verify PROVED
// it stale (the container it names no longer exists, or failed health).
// This is the critical case: caller MUST discard activeGenState/
// rolloutState before falling back to the label-based scan, not merely
// fall back to it while still holding them. A stale persisted generation
// string that GenerateRecoveryPlan's determineAuthority still trusts
// (state.go's Priority 1/2) will never match anything in a label-keyed
// inventory — a live rollout's backend ID was never a label — so
// determineRecoveryAction sees "authority exists, 0 healthy, 0 unhealthy"
// (map miss, not a real health verdict) and goes RecoveryDegraded even
// though the label scan just found a perfectly healthy backend under its
// current, correct label. This was caught live: after simulating a host
// reboot (full container recreation — new container IDs, persisted volume
// intact), the proxy got stuck in a permanent, never-self-healing degraded
// state despite `docker orbit status` and the label scan agreeing a
// healthy backend existed the entire time.
func directVerifyRecoveryResult(
	ctx context.Context,
	source *proxy.DockerRecoverySource,
	activeGenState *state.ActiveGenerationState,
	rolloutState *state.RolloutState,
	log *zap.Logger,
) (result *proxy.RecoveryResult, provenStale bool) {
	start := time.Now()

	// Interrupted rollout: verify the new generation first — it's the one
	// that must be restorable for this path to be worth anything. The old
	// (draining) generation is opportunistic: if it doesn't verify, the new
	// generation alone is still a valid, safe result (RecoveryRestoreSingle
	// territory in practice, even though the persisted RolloutState still
	// says Transitioning).
	if rolloutState != nil && rolloutState.Authority == state.AuthorityTransitioning {
		newBackend, err := source.VerifyBackendByID(ctx, rolloutState.NewGeneration)
		if err != nil {
			log.Info("recovery: direct-verify of in-flight new generation failed, falling back to label scan",
				zap.String("id", rolloutState.NewGeneration), zap.Error(err))
			// ErrNotIDVerifiable means this ID was never eligible for this
			// mechanism (e.g. the seed sentinel) — the label scan can
			// resolve it correctly on its own, so the persisted state isn't
			// actually disproven and must not be discarded. Only a genuine
			// Docker-level negative result (container gone/unhealthy) means
			// the fallback needs to run without it.
			return nil, !errors.Is(err, proxy.ErrNotIDVerifiable)
		}
		backends := []proxy.BackendHealth{directVerifiedHealth(*newBackend)}
		if rolloutState.OldGeneration != "" {
			if oldBackend, oldErr := source.VerifyBackendByID(ctx, rolloutState.OldGeneration); oldErr == nil {
				backends = append(backends, directVerifiedHealth(*oldBackend))
			} else {
				log.Info("recovery: direct-verify of in-flight old generation failed (new generation still restorable)",
					zap.String("id", rolloutState.OldGeneration), zap.Error(oldErr))
			}
		}
		log.Info("recovery: restored via direct-verify (interrupted rollout)",
			zap.String("new", rolloutState.NewGeneration), zap.Int("verified", len(backends)))
		return &proxy.RecoveryResult{
			State:           proxy.StartupReady,
			HealthyCount:    len(backends),
			Backends:        backends,
			TotalDiscovered: len(backends),
			RecoveredAt:     time.Now(),
			DurationMs:      time.Since(start).Milliseconds(),
		}, false
	}

	// Steady state: one persisted, trusted generation.
	if activeGenState != nil && activeGenState.ActiveGeneration != "" {
		backend, err := source.VerifyBackendByID(ctx, activeGenState.ActiveGeneration)
		if err != nil {
			log.Info("recovery: direct-verify of persisted authority failed, falling back to label scan",
				zap.String("id", activeGenState.ActiveGeneration), zap.Error(err))
			// See the AuthorityTransitioning branch above for why
			// ErrNotIDVerifiable (e.g. the seed sentinel) must not count as
			// disproving the persisted value — the label scan resolves it
			// correctly on its own.
			return nil, !errors.Is(err, proxy.ErrNotIDVerifiable)
		}
		log.Info("recovery: restored via direct-verify (trusted persisted authority)",
			zap.String("generation", activeGenState.ActiveGeneration))
		return &proxy.RecoveryResult{
			State:           proxy.StartupReady,
			HealthyCount:    1,
			Backends:        []proxy.BackendHealth{directVerifiedHealth(*backend)},
			TotalDiscovered: 1,
			RecoveredAt:     time.Now(),
			DurationMs:      time.Since(start).Milliseconds(),
		}, false
	}

	return nil, false
}

func directVerifiedHealth(b proxy.Backend) proxy.BackendHealth {
	return proxy.BackendHealth{
		ID:         b.ID,
		Addr:       b.Addr,
		Generation: b.Generation,
		Status:     proxy.HealthHealthy,
		Reason:     "direct-verify against persisted authority",
		CheckedAt:  time.Now(),
	}
}

// countRestoredBackends reports how many of the recovery plan's candidates
// were valid restoration targets — the same ValidityStatus check
// executeRecovery's registration loop uses to decide what to register.
func countRestoredBackends(plan *state.RecoveryPlan) int {
	if plan == nil {
		return 0
	}
	count := 0
	for _, c := range plan.BackendsToRestore {
		if c.ValidityStatus == state.CandidateValid {
			count++
		}
	}
	return count
}

// ── version ───────────────────────────────────────────────────────────────────

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print docker orbit version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("docker orbit %s\n", version)
		},
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeComposeFile(path string, cf *compose.ComposeFile) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := yaml.NewEncoder(f)
	enc.SetIndent(4)
	return enc.Encode(cf)
}

func doGet(url string) (string, error) {
	resp, err := httpClient.Get(url) //nolint:noctx
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("GET %s: unexpected status %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

var httpClient = &http.Client{Timeout: 5 * time.Second}

// buildGenerationInventory converts recovery result into generation inventory.
// Groups backends by generation label (from docker-compose x-docker-rollout: generation).
func buildGenerationInventory(
	service string,
	result *proxy.RecoveryResult,
	activeGen *state.ActiveGenerationState,
) *state.GenerationInventory {
	inventory := &state.GenerationInventory{
		Service:          service,
		SnapshotTime:     time.Now(),
		GenerationStates: make(map[string]state.GenerationMetrics),
		Backends:         make(map[string][]state.BackendInfo),
		ContainerCount:   len(result.Backends),
	}

	// Set active generation from persistent state if available.
	if activeGen != nil {
		inventory.ActiveGeneration = activeGen.ActiveGeneration
	}

	// Group backends by generation and track health.
	generationMap := make(map[string]*state.GenerationMetrics)

	now := time.Now()
	for _, backend := range result.Backends {
		// Extract generation label (default to service name if not labeled).
		gen := backend.Generation
		if gen == "" {
			gen = service + "-default"
		}

		// Real Docker container creation time when known (see
		// RecoveryResult.BackendCreatedAt) — falls back to now only when
		// unavailable. Using the same now for every generation made
		// recovery's "longest healthy uptime" tie-break unable to
		// distinguish generations, falling through to Go's randomized map
		// iteration order whenever 2+ were simultaneously healthy.
		created := now
		if t, ok := result.BackendCreatedAt[backend.ID]; ok && !t.IsZero() {
			created = t
		}

		// Get or create generation metrics.
		if _, exists := generationMap[gen]; !exists {
			generationMap[gen] = &state.GenerationMetrics{
				Generation:             gen,
				CreatedAt:              created,
				ContinuousHealthyStart: created,
			}
		} else if created.Before(generationMap[gen].CreatedAt) {
			// Oldest backend's creation time represents the generation —
			// mirrors DeriveHealthStreakStartTime's "all healthy: use
			// oldest container creation" rule, applied per generation.
			generationMap[gen].CreatedAt = created
			generationMap[gen].ContinuousHealthyStart = created
		}

		m := generationMap[gen]
		m.TotalCount++

		// Store backend info.
		inventory.Backends[gen] = append(inventory.Backends[gen], state.BackendInfo{
			ID:     backend.ID,
			Addr:   backend.Addr,
			Health: string(backend.Status),
		})

		// Count health status.
		switch backend.Status {
		case proxy.HealthHealthy:
			m.HealthyCount++
			inventory.HealthyBackendCount++
			if m.FirstHealthyAt.IsZero() {
				m.FirstHealthyAt = now
			}
		case proxy.HealthStarting:
			m.StartingCount++
			inventory.StartingBackendCount++
		default:
			m.UnhealthyCount++
			inventory.UnhealthyBackendCount++
		}

		m.LastHealthyCheck = now
	}

	// Copy to inventory state map.
	for gen, metrics := range generationMap {
		inventory.GenerationStates[gen] = *metrics

		// Track healthy and orphan generations.
		if metrics.HealthyCount > 0 {
			inventory.HealthyGenerations = append(inventory.HealthyGenerations, gen)
		}
	}

	return inventory
}

// revalidateBackendHealth performs a lightweight health recheck before registration.
// Verifies: container exists + responds to health checks.
// Returns false if backend is no longer viable.
func revalidateBackendHealth(
	ctx context.Context,
	source *proxy.DockerRecoverySource,
	candidate *state.BackendCandidate,
	log *zap.Logger,
) bool {
	if source == nil {
		// Docker unavailable; trust snapshot
		return true
	}

	// Quick check: container still exists
	if source == nil {
		return true
	}

	// Lightweight: TCP dial to backend address (fast health check)
	dialCtx, dialCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer dialCancel()

	dialer := net.Dialer{Timeout: 100 * time.Millisecond}
	conn, err := dialer.DialContext(dialCtx, "tcp", candidate.Addr)
	if err != nil {
		log.Debug("healing: backend TCP check failed",
			zap.String("id", candidate.ID),
			zap.String("addr", candidate.Addr),
			zap.Error(err))
		return false
	}
	conn.Close()

	log.Debug("healing: backend revalidation passed",
		zap.String("id", candidate.ID),
		zap.String("addr", candidate.Addr))
	return true
}
