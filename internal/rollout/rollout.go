// Package rollout implements zero-downtime rolling updates for Orbit services.
package rollout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker-secret-operator/orbit/internal/history"
	"go.uber.org/zap"
)

// Options configures a single rollout operation.
type Options struct {
	// ComposeFile is the path to docker-rollout-compose.yml (default: docker-rollout-compose.yml).
	ComposeFile string

	// Service is the name of the service to roll out.
	Service string

	// Pull fetches the latest image before rolling out.
	Pull bool

	// Timeout is how long to wait for the new container's healthcheck to pass.
	// Default: 60 seconds.
	Timeout time.Duration

	// Drain is how long to wait for in-flight connections to complete on the
	// old container after the new one is healthy. Default: 5 seconds.
	Drain time.Duration

	// StabilityWindow is how long to watch the new backend after it is
	// registered but before the old backend is touched. If the new backend
	// becomes unhealthy or its container stops running during this window,
	// the rollout is rolled back automatically (the old backend was never
	// drained or removed, so recovery is limited to removing the new
	// backend). Zero or negative disables the check. Default: 10 seconds.
	StabilityWindow time.Duration

	// ControlAddr is the HTTP address of the Orbit proxy control API.
	// Default: "http://localhost:9900"
	ControlAddr string

	// APIToken is the Bearer token for the control API. Empty means unauthenticated.
	APIToken string

	// Progress, if set, is called at each existing step transition Run
	// already performs — see Phase's doc comment. This is instrumentation,
	// not a new decision point: nothing here changes what Run does or in
	// what order, only what a caller can observe while it happens. nil is
	// safe and produces no callback overhead (see Options.report).
	Progress ProgressFunc
}

// Phase names a step of the rollout Run already performs, in the order Run's
// own doc comment describes. Do not add a value here without a corresponding
// call from real orchestration code — an unreported phase is not useful
// instrumentation, and a phase that doesn't correspond to a real step is
// exactly the kind of placeholder CONSTITUTION.md rules out.
type Phase string

const (
	PhasePulling       Phase = "pulling"       // Step 2: optional image pull
	PhaseScalingUp     Phase = "scaling_up"    // Step 2: scale +1
	PhaseHealthCheck   Phase = "health_check"  // Step 3: wait for new container healthy
	PhaseRegistering   Phase = "registering"   // Step 5: register new backend
	PhaseSavingState   Phase = "saving_state"  // Step 6: persist rollback state
	PhaseVerifying     Phase = "verifying"     // Step 6b: post-registration stability check
	PhaseRollingBack   Phase = "rolling_back"  // Step 6b: automatic rollback if stability check fails
	PhaseDraining      Phase = "draining"      // Step 7: drain old backend
	PhaseDeregistering Phase = "deregistering" // Step 8-9b: remove old backend/container/seed
	PhaseComplete      Phase = "complete"      // Step 10: state cleared, rollout done
)

// StepDescription pairs a Phase with a human-readable description of what
// happens during it. See PlannedSteps.
type StepDescription struct {
	Phase       Phase
	Description string
}

// PlannedSteps describes, in order, every phase Run reports through
// Options.Progress. It is the single source of truth for previewing a
// rollout (e.g. `docker orbit deploy --dry-run`) — callers should build their
// preview from this instead of hand-copying Run's step sequence, which drifts
// silently the moment Run changes and the copy doesn't (this happened once
// already, when StabilityWindow was added).
//
// Not included: steps a caller performs itself, outside Run — acquiring the
// per-service deployment lock is the caller's responsibility (see Run's doc
// comment), so it has no Phase and isn't part of this list.
func PlannedSteps() []StepDescription {
	return []StepDescription{
		{PhasePulling, "Optional: pull the new image (--pull)"},
		{PhaseScalingUp, "Scale the service +1 (start the new container alongside the old one)"},
		{PhaseHealthCheck, "Wait for the new container's healthcheck to pass (or --timeout)"},
		{PhaseRegistering, "Register the new container with the proxy — traffic starts splitting"},
		{PhaseSavingState, "Save rollback state (enables 'docker orbit rollback' if this fails)"},
		{PhaseVerifying, "Watch the new container for --stability before touching the old one (auto-rolls back on failure)"},
		{PhaseDraining, "Drain the old container for --drain, then remove it"},
		{PhaseDeregistering, "Deregister the old backend and the initial seed backend"},
	}
}

// ProgressFunc receives phase transitions during Run. detail is a short,
// human-readable elaboration (e.g. a container ID or duration) — the same
// information already going to the structured log, offered on a second,
// caller-controlled channel for interactive rendering.
type ProgressFunc func(phase Phase, detail string)

// report calls o.Progress if set. Safe to call unconditionally.
func (o Options) report(phase Phase, detail string) {
	if o.Progress != nil {
		o.Progress(phase, detail)
	}
}

func (o *Options) defaults() {
	if o.ComposeFile == "" {
		o.ComposeFile = "docker-rollout-compose.yml"
	}
	if o.Timeout == 0 {
		o.Timeout = 60 * time.Second
	}
	if o.Drain == 0 {
		o.Drain = 5 * time.Second
	}
	if o.StabilityWindow == 0 {
		o.StabilityWindow = 10 * time.Second
	}
	if o.ControlAddr == "" {
		o.ControlAddr = "http://localhost:9900"
	}
}

// ── Rollout state (for rollback) ──────────────────────────────────────────────

// RolloutState is written to /tmp between steps 5 and 7 of a rollout (after
// the new backend is registered, before the old one is removed). It enables
// the rollback command to restore traffic to the previous version if the new
// deployment is unhealthy.
type RolloutState struct {
	Service      string        `json:"service"`
	OldBackendID string        `json:"old_backend_id"`
	OldAddr      string        `json:"old_addr"`
	NewBackendID string        `json:"new_backend_id"`
	NewAddr      string        `json:"new_addr"`
	ControlAddr  string        `json:"control_addr"`
	APIToken     string        `json:"api_token,omitempty"`
	Drain        time.Duration `json:"drain_ns"`
	StartedAt    time.Time     `json:"started_at"`
}

// Runtime abstracts container runtime operations used by rollout orchestration.
type Runtime interface {
	Pull(ctx context.Context, composeFile, service string) error
	ServiceReplicaCount(ctx context.Context, service string) (int, error)
	ScaleService(ctx context.Context, composeFile, service string, replicas int) error
	WaitForNewContainer(ctx context.Context, opts Options, log *zap.Logger) (id, addr string, err error)
	FindOldContainer(ctx context.Context, service, newID string) (string, error)
	ContainerAddr(ctx context.Context, id string) (string, error)
	RemoveContainer(ctx context.Context, id string) error

	// VerifyStable watches containerID for window and returns an error the
	// moment it becomes unhealthy or stops running. It returns nil once the
	// full window elapses without either. window <= 0 means "no check" —
	// implementations must return nil immediately in that case.
	VerifyStable(ctx context.Context, containerID string, window time.Duration) error
}

// ControlAPI abstracts rollout calls to the proxy control plane.
type ControlAPI interface {
	RegisterBackend(ctx context.Context, opts Options, id, addr string, log *zap.Logger) error
	DrainBackend(ctx context.Context, opts Options, id string, log *zap.Logger) error
	DeregisterBackend(ctx context.Context, opts Options, id string, log *zap.Logger) error
}

// StateStore abstracts rollout state persistence for rollback support.
type StateStore interface {
	Save(state RolloutState) error
	Clear(service string)
}

type runDeps struct {
	runtime Runtime
	control ControlAPI
	state   StateStore
}

func statePath(service string) string {
	return fmt.Sprintf("/tmp/orbit-%s-state.json", service)
}

func saveState(s RolloutState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(s.Service), data, 0600)
}

// LoadState reads the last rollout state for the given service so Rollback can
// consume it. Returns an error if no state file exists.
func LoadState(service string) (RolloutState, error) {
	data, err := os.ReadFile(statePath(service))
	if err != nil {
		if os.IsNotExist(err) {
			return RolloutState{}, fmt.Errorf("no rollout state for %q — run a rollout first", service)
		}
		return RolloutState{}, err
	}
	var s RolloutState
	return s, json.Unmarshal(data, &s)
}

func clearState(service string) {
	os.Remove(statePath(service)) //nolint:errcheck
}

// ── Run ───────────────────────────────────────────────────────────────────────

// Run executes a zero-downtime rolling update for the given service.
//
// Mutual exclusion is the caller's responsibility: callers (the deploy and
// rollout CLI commands) acquire the per-service lock via AcquireLock before
// invoking Run, which also lets them offer --force-unlock and stale-lock
// detection. Run must NOT acquire its own lock — doing so would collide with
// the caller's lock on the same /tmp/orbit-<service>.lock path and fail every
// real deployment with a false "already in progress" error.
//
// Steps:
//  1. Optionally pull the new image.
//  2. Scale the service to +1 instance (docker compose up --scale).
//  3. Wait for the new container's healthcheck to pass (or timeout).
//  4. Register the new container with the proxy via POST /backends.
//  5. Persist rollout state to /tmp (enables rollback).
//  6. Watch the new container for StabilityWindow; if it becomes unhealthy
//     or stops running, roll back automatically (remove the new backend —
//     the old one was never touched) and return an error.
//  7. Drain old container; wait drain period so in-flight requests complete.
//  8. Deregister the old container via DELETE /backends/{id}.
//  9. Scale back to the original count (remove old container).
//  10. Clear rollout state.
func Run(ctx context.Context, opts Options, log *zap.Logger) error {
	opts.defaults()

	start := time.Now()
	if err := history.Append(history.Event{Service: opts.Service, Type: history.EventRolloutStarted}); err != nil {
		log.Warn("history: could not record rollout start (non-fatal)", zap.Error(err))
	}

	runErr := runWithDeps(ctx, opts, log, defaultRunDeps())

	ev := history.Event{
		Service:    opts.Service,
		DurationMS: time.Since(start).Milliseconds(),
	}
	if runErr != nil {
		ev.Type = history.EventRolloutFailed
		ev.Result = "failure"
		ev.Reason = runErr.Error()
	} else {
		ev.Type = history.EventRolloutCompleted
		ev.Result = "success"
	}
	if err := history.Append(ev); err != nil {
		log.Warn("history: could not record rollout outcome (non-fatal)", zap.Error(err))
	}

	return runErr
}

func runWithDeps(ctx context.Context, opts Options, log *zap.Logger, deps runDeps) error {
	log.Info("rollout: starting",
		zap.String("service", opts.Service),
		zap.String("compose", opts.ComposeFile))

	// ── Step 1: Pull new image ────────────────────────────────────────────
	if opts.Pull {
		log.Info("rollout: pulling image", zap.String("service", opts.Service))
		opts.report(PhasePulling, "pulling latest image for "+opts.Service)
		if err := deps.runtime.Pull(ctx, opts.ComposeFile, opts.Service); err != nil {
			return fmt.Errorf("rollout: pull: %w", err)
		}
	}

	// ── Step 2: Scale to +1 ───────────────────────────────────────────────
	currentReplicas, err := deps.runtime.ServiceReplicaCount(ctx, opts.Service)
	if err != nil {
		return fmt.Errorf("rollout: detect current replicas: %w", err)
	}
	targetReplicas := currentReplicas + 1
	log.Info("rollout: scaling +1", zap.String("service", opts.Service))
	opts.report(PhaseScalingUp, fmt.Sprintf("%s: %d → %d replicas", opts.Service, currentReplicas, targetReplicas))
	if err := deps.runtime.ScaleService(ctx, opts.ComposeFile, opts.Service, targetReplicas); err != nil {
		return fmt.Errorf("rollout: scale up: %w", err)
	}

	// ── Step 3: Wait for healthcheck ──────────────────────────────────────
	opts.report(PhaseHealthCheck, fmt.Sprintf("waiting up to %s for new container's healthcheck", opts.Timeout))
	newID, newAddr, err := deps.runtime.WaitForNewContainer(ctx, opts, log)
	if err != nil {
		// Cleanup: scale back down on healthcheck timeout.
		_ = deps.runtime.ScaleService(ctx, opts.ComposeFile, opts.Service, currentReplicas)
		return fmt.Errorf("rollout: wait for healthy container: %w", err)
	}

	log.Info("rollout: new container healthy",
		zap.String("id", newID),
		zap.String("addr", newAddr))
	opts.report(PhaseHealthCheck, fmt.Sprintf("container %s healthy at %s", shortID(newID), newAddr))

	// ── Step 4: Find old container ID ────────────────────────────────────
	oldID, err := deps.runtime.FindOldContainer(ctx, opts.Service, newID)
	if err != nil {
		log.Warn("rollout: could not identify old container — skipping deregister",
			zap.Error(err))
	}

	// ── Step 5: Register new backend with proxy ───────────────────────────
	newBackendID := opts.Service + "-" + shortID(newID)
	opts.report(PhaseRegistering, fmt.Sprintf("registering backend %s (%s)", newBackendID, newAddr))
	if err := deps.control.RegisterBackend(ctx, opts, newBackendID, newAddr, log); err != nil {
		return fmt.Errorf("rollout: register new backend: %w", err)
	}
	log.Info("rollout: new backend registered",
		zap.String("backend_id", newBackendID),
		zap.String("addr", newAddr))

	// ── Step 6: Persist rollout state (enables rollback) ─────────────────
	opts.report(PhaseSavingState, "saving rollback state for "+opts.Service)
	oldBackendID := ""
	oldAddr := ""
	if oldID != "" {
		oldBackendID = opts.Service + "-" + shortID(oldID)
		if addr, err := deps.runtime.ContainerAddr(ctx, oldID); err == nil {
			oldAddr = addr
		}
	}
	_ = deps.state.Save(RolloutState{
		Service:      opts.Service,
		OldBackendID: oldBackendID,
		OldAddr:      oldAddr,
		NewBackendID: newBackendID,
		NewAddr:      newAddr,
		ControlAddr:  opts.ControlAddr,
		APIToken:     opts.APIToken,
		Drain:        opts.Drain,
		StartedAt:    time.Now(),
	})

	// ── Step 6b: Verify new backend stability before touching the old one ──
	// The old backend has not been drained or removed yet — it is still
	// fully serving. If the new backend is unstable, recovery only needs to
	// remove the new backend; nothing needs to be restored.
	opts.report(PhaseVerifying, fmt.Sprintf("watching %s for %s before draining %s", newBackendID, opts.StabilityWindow, nonEmptyOr(oldBackendID, "(no prior backend)")))
	if err := deps.runtime.VerifyStable(ctx, newID, opts.StabilityWindow); err != nil {
		log.Warn("rollout: new backend failed stability check — rolling back automatically",
			zap.String("backend_id", newBackendID),
			zap.Error(err))
		opts.report(PhaseRollingBack, fmt.Sprintf("%s failed stability check: %v", newBackendID, err))

		if derr := deps.control.DeregisterBackend(ctx, opts, newBackendID, log); derr != nil {
			log.Warn("rollout: could not deregister failed backend during auto-rollback",
				zap.String("id", newBackendID), zap.Error(derr))
		}
		_ = deps.runtime.RemoveContainer(ctx, newID)
		if serr := deps.runtime.ScaleService(ctx, opts.ComposeFile, opts.Service, currentReplicas); serr != nil {
			log.Warn("rollout: could not reconcile replica count after auto-rollback",
				zap.Int("target_replicas", currentReplicas), zap.Error(serr))
		}
		deps.state.Clear(opts.Service)

		return fmt.Errorf("rollout: new backend failed stability check, rolled back automatically (old backend %s never touched): %w", nonEmptyOr(oldBackendID, "(none)"), err)
	}

	// ── Step 7: Drain old connections ─────────────────────────────────────
	log.Info("rollout: draining old connections", zap.Duration("drain", opts.Drain))
	opts.report(PhaseDraining, fmt.Sprintf("draining %s for %s", nonEmptyOr(oldBackendID, "(no prior backend)"), opts.Drain))
	if oldID != "" {
		if err := deps.control.DrainBackend(ctx, opts, oldBackendID, log); err != nil {
			return fmt.Errorf("rollout: drain old backend %s: %w", oldBackendID, err)
		}
	}
	select {
	case <-time.After(opts.Drain):
	case <-ctx.Done():
		return ctx.Err()
	}

	// ── Step 8: Deregister old backend ────────────────────────────────────
	if oldID != "" {
		opts.report(PhaseDeregistering, "deregistering "+oldBackendID)
		if err := deps.control.DeregisterBackend(ctx, opts, oldBackendID, log); err != nil {
			log.Warn("rollout: could not deregister old backend",
				zap.String("id", oldBackendID),
				zap.Error(err))
		}
	}

	// ── Step 9: Remove old container (keep new one) ───────────────────────
	// We stop and remove the OLD container explicitly instead of using
	// --scale=1, because compose scale-down removes the newest container
	// (api-2) and keeps the old one (api-1), which is the opposite of what
	// we want.
	if oldID != "" {
		log.Info("rollout: removing old container", zap.String("id", oldID))
		opts.report(PhaseDeregistering, "removing old container "+shortID(oldID))
		_ = deps.runtime.RemoveContainer(ctx, oldID)
	} else if err := deps.runtime.ScaleService(ctx, opts.ComposeFile, opts.Service, currentReplicas); err != nil {
		log.Warn("rollout: could not reconcile replica count",
			zap.Int("target_replicas", currentReplicas),
			zap.Error(err))
	}

	// ── Step 9b: Deregister seed backend ─────────────────────────────────
	// The proxy is seeded with a DNS-based "<service>-default" backend via
	// ORBIT_TARGETS. After the first successful rollout the IP-based backend
	// takes over, so the seed backend must be cleaned up — otherwise it stays
	// in the rotation forever and routes traffic to whatever DNS resolves to
	// at any given moment (which may be a stale or wrong container).
	seedID := opts.Service + "-default"
	if err := deps.control.DeregisterBackend(ctx, opts, seedID, log); err != nil {
		// 404 = already gone; all other errors are non-fatal — log and continue.
		log.Warn("rollout: could not deregister seed backend (non-fatal)",
			zap.String("id", seedID),
			zap.Error(err))
	} else {
		log.Info("rollout: seed backend deregistered", zap.String("id", seedID))
	}

	// ── Step 10: Clear state ──────────────────────────────────────────────
	deps.state.Clear(opts.Service)

	log.Info("rollout: complete", zap.String("service", opts.Service))
	opts.report(PhaseComplete, fmt.Sprintf("%s is now serving from %s", opts.Service, newAddr))
	return nil
}

// shortID returns the first 12 characters of a Docker container ID, or the
// whole string if it's already shorter — never panics on a short input,
// unlike the id[:12] slicing this replaces.
func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// nonEmptyOr returns s, or fallback if s is empty — used for progress detail
// strings where an empty value would otherwise render as a confusing blank.
func nonEmptyOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ── Rollback ──────────────────────────────────────────────────────────────────

// RollbackPhase names a step Rollback performs, mirroring Phase's role for
// Run — see Phase's doc comment for the same "no unreported/unreal phase"
// rule.
type RollbackPhase string

const (
	RollbackPhaseRestoring     RollbackPhase = "restoring"     // re-registering the old backend
	RollbackPhaseDraining      RollbackPhase = "draining"      // draining the failing new backend
	RollbackPhaseDeregistering RollbackPhase = "deregistering" // removing the new backend
	RollbackPhaseComplete      RollbackPhase = "complete"
)

// RollbackProgressFunc receives phase transitions during Rollback. nil is safe.
type RollbackProgressFunc func(phase RollbackPhase, detail string)

// Rollback restores traffic to the previous backend recorded in the rollout
// state file, and drains/removes the new (failing) backend.
//
// Call this when a just-deployed service is unhealthy and you need to restore
// the previous version without a full re-deploy. The rollout state is cleared
// after a successful rollback. progress, if non-nil, is called at each step
// below — purely additive instrumentation, matching Options.Progress for Run.
func Rollback(ctx context.Context, state RolloutState, log *zap.Logger, progress RollbackProgressFunc) (err error) {
	report := func(phase RollbackPhase, detail string) {
		if progress != nil {
			progress(phase, detail)
		}
	}

	if state.OldBackendID == "" || state.OldAddr == "" {
		return fmt.Errorf("rollback: no old backend recorded in state — cannot roll back")
	}
	start := time.Now()

	defer func() {
		ev := history.Event{
			Service:       state.Service,
			Type:          history.EventRollback,
			OldGeneration: state.OldBackendID,
			NewGeneration: state.NewBackendID,
			DurationMS:    time.Since(start).Milliseconds(),
		}
		if err != nil {
			ev.Result = "failure"
			ev.Reason = err.Error()
		} else {
			ev.Result = "success"
		}
		if herr := history.Append(ev); herr != nil {
			log.Warn("history: could not record rollback outcome (non-fatal)", zap.Error(herr))
		}
	}()

	log.Info("rollback: starting",
		zap.String("service", state.Service),
		zap.String("restoring", state.OldBackendID),
		zap.String("draining", state.NewBackendID))

	opts := Options{
		ControlAddr: state.ControlAddr,
		APIToken:    state.APIToken,
		Drain:       state.Drain,
	}
	if opts.Drain == 0 {
		opts.Drain = 5 * time.Second
	}

	// Re-register old backend (it may have been removed; 409 if still present is ok).
	report(RollbackPhaseRestoring, "restoring "+state.OldBackendID+" ("+state.OldAddr+")")
	if err := registerBackend(ctx, opts, state.OldBackendID, state.OldAddr, log); err != nil {
		if !strings.Contains(err.Error(), "409") {
			return fmt.Errorf("rollback: restore old backend: %w", err)
		}
		log.Info("rollback: old backend already registered", zap.String("id", state.OldBackendID))
	} else {
		log.Info("rollback: old backend restored", zap.String("id", state.OldBackendID))
	}

	// Drain the new (failing) backend.
	if state.NewBackendID != "" {
		_ = drainBackend(ctx, opts, state.NewBackendID, log)
		log.Info("rollback: draining new backend",
			zap.String("id", state.NewBackendID),
			zap.Duration("drain", opts.Drain))
		report(RollbackPhaseDraining, fmt.Sprintf("draining %s for %s", state.NewBackendID, opts.Drain))

		select {
		case <-time.After(opts.Drain):
		case <-ctx.Done():
			return ctx.Err()
		}

		report(RollbackPhaseDeregistering, "removing "+state.NewBackendID)
		if err := deregisterBackend(ctx, opts, state.NewBackendID, log); err != nil {
			log.Warn("rollback: could not remove new backend (may not exist)",
				zap.String("id", state.NewBackendID),
				zap.Error(err))
		}
	}

	clearState(state.Service)
	log.Info("rollback: complete", zap.String("service", state.Service))
	report(RollbackPhaseComplete, state.Service+" restored to "+state.OldBackendID)
	return nil
}

// ── Docker / Compose helpers ──────────────────────────────────────────────────

type dockerRuntime struct{}

func (dockerRuntime) Pull(ctx context.Context, composeFile, service string) error {
	return composeRun(ctx, composeFile, "pull", service)
}

func (dockerRuntime) ServiceReplicaCount(ctx context.Context, service string) (int, error) {
	return serviceReplicaCount(ctx, service)
}

func (dockerRuntime) ScaleService(ctx context.Context, composeFile, service string, replicas int) error {
	return scaleService(ctx, composeFile, service, replicas)
}

func (dockerRuntime) WaitForNewContainer(ctx context.Context, opts Options, log *zap.Logger) (id, addr string, err error) {
	return waitForNewContainer(ctx, opts, log)
}

func (dockerRuntime) FindOldContainer(ctx context.Context, service, newID string) (string, error) {
	return findOldContainer(ctx, service, newID)
}

func (dockerRuntime) ContainerAddr(ctx context.Context, id string) (string, error) {
	return containerAddr(ctx, id)
}

func (dockerRuntime) RemoveContainer(ctx context.Context, id string) error {
	if err := exec.CommandContext(ctx, "docker", "stop", id).Run(); err != nil {
		return err
	}
	return exec.CommandContext(ctx, "docker", "rm", id).Run()
}

func (dockerRuntime) VerifyStable(ctx context.Context, containerID string, window time.Duration) error {
	return verifyContainerStable(ctx, containerID, window)
}

type httpControlAPI struct{}

func (httpControlAPI) RegisterBackend(ctx context.Context, opts Options, id, addr string, log *zap.Logger) error {
	return registerBackend(ctx, opts, id, addr, log)
}

func (httpControlAPI) DrainBackend(ctx context.Context, opts Options, id string, log *zap.Logger) error {
	return drainBackend(ctx, opts, id, log)
}

func (httpControlAPI) DeregisterBackend(ctx context.Context, opts Options, id string, log *zap.Logger) error {
	return deregisterBackend(ctx, opts, id, log)
}

type fileStateStore struct{}

func (fileStateStore) Save(state RolloutState) error { return saveState(state) }
func (fileStateStore) Clear(service string)          { clearState(service) }

func defaultRunDeps() runDeps {
	return runDeps{
		runtime: dockerRuntime{},
		control: httpControlAPI{},
		state:   fileStateStore{},
	}
}

func composeRun(ctx context.Context, file string, args ...string) error {
	cmdArgs := append([]string{"compose", "-f", file}, args...)
	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w\n%s",
			strings.Join(cmdArgs, " "), err, string(out))
	}
	return nil
}

// waitForNewContainer polls for a second instance of the service to appear
// and pass its healthcheck. Returns the container ID and its docker_rollout_mesh IP.
func waitForNewContainer(ctx context.Context, opts Options, log *zap.Logger) (id, addr string, err error) {
	deadline := time.Now().Add(opts.Timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return "", "", fmt.Errorf("timeout (%s) waiting for healthy container", opts.Timeout)
			}

			id, addr, err = inspectNewestHealthy(ctx, opts.Service)
			if err == nil {
				return id, addr, nil
			}
			log.Debug("rollout: waiting for healthy container", zap.Error(err))
		}
	}
}

// inspectNewestHealthy finds the most recently started container for the
// service that is either healthy (has healthcheck) or running (no healthcheck).
// Returns id and addr in "ip:port" form ready for the proxy control API.
func inspectNewestHealthy(ctx context.Context, service string) (id, addr string, err error) {
	out, err := exec.CommandContext(ctx, "docker", "ps",
		"--filter", "label=com.docker.compose.service="+service,
		"--format", "{{.ID}}",
	).Output()
	if err != nil {
		return "", "", fmt.Errorf("docker ps: %w", err)
	}

	ids := strings.Fields(string(out))
	if len(ids) < 2 {
		return "", "", fmt.Errorf("service %q: waiting for second container (found %d)", service, len(ids))
	}

	id = ids[0]
	// Emit health status, "name=ip" network pairs, and "port/proto" exposed port pairs.
	// ExposedPorts is map[Port]struct{} so we range with $k,$v to get the key.
	inspectOut, err := exec.CommandContext(ctx, "docker", "inspect",
		"--format",
		`{{.State.Health.Status}}{{range $n, $v := .NetworkSettings.Networks}} net={{$n}}={{$v.IPAddress}}{{end}}{{range $k, $v := .Config.ExposedPorts}} port={{$k}}{{end}}{{range .Config.Env}} env={{.}}{{end}}`,
		id,
	).Output()
	if err != nil {
		return "", "", fmt.Errorf("docker inspect %s: %w", id, err)
	}

	fields := strings.Fields(string(inspectOut))
	if len(fields) < 1 {
		return "", "", fmt.Errorf("docker inspect: empty output for %s", id)
	}

	healthStatus := fields[0]
	if healthStatus == "unhealthy" {
		return "", "", fmt.Errorf("container %s is unhealthy", id)
	}
	if healthStatus == "starting" {
		return "", "", fmt.Errorf("container %s healthcheck is still starting", id)
	}

	// Parse network and port tokens.
	var netTokens, portTokens, envTokens []string
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "net=") {
			netTokens = append(netTokens, strings.TrimPrefix(f, "net="))
		} else if strings.HasPrefix(f, "port=") {
			portTokens = append(portTokens, strings.TrimPrefix(f, "port="))
		} else if strings.HasPrefix(f, "env=") {
			envTokens = append(envTokens, strings.TrimPrefix(f, "env="))
		}
	}

	ip := pickMeshIP(netTokens)
	if ip == "" {
		return "", "", fmt.Errorf("container %s has no IP address", id)
	}

	port, err := pickBackendPort(portTokens, envTokens)
	if err != nil {
		return "", "", fmt.Errorf("container %s port resolution failed: %w", id, err)
	}

	return id, ip + ":" + port, nil
}

// stabilityProbeInterval is how often verifyContainerStable polls during the
// stability window.
const stabilityProbeInterval = 1 * time.Second

// verifyContainerStable polls containerID's health/running state until
// window elapses, failing fast the moment it becomes unhealthy or stops
// running. window <= 0 skips the check entirely (returns nil immediately).
func verifyContainerStable(ctx context.Context, containerID string, window time.Duration) error {
	if window <= 0 {
		return nil
	}
	deadline := time.Now().Add(window)
	ticker := time.NewTicker(stabilityProbeInterval)
	defer ticker.Stop()

	for {
		status, running, err := inspectHealthAndRunning(ctx, containerID)
		if err != nil {
			return fmt.Errorf("inspect during stability check: %w", err)
		}
		if !running {
			return fmt.Errorf("container %s stopped running during stability window", shortID(containerID))
		}
		if status == "unhealthy" {
			return fmt.Errorf("container %s became unhealthy during stability window", shortID(containerID))
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// inspectHealthAndRunning returns a container's Docker healthcheck status
// (empty string if no HEALTHCHECK is defined, mirroring inspectNewestHealthy's
// treatment) and whether it is currently running.
func inspectHealthAndRunning(ctx context.Context, id string) (status string, running bool, err error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect",
		"--format", `{{.State.Health.Status}}|{{.State.Running}}`,
		id,
	).Output()
	if err != nil {
		return "", false, fmt.Errorf("docker inspect %s: %w", id, err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
	if len(parts) != 2 {
		return "", false, fmt.Errorf("docker inspect %s: unexpected output %q", id, string(out))
	}
	return parts[0], parts[1] == "true", nil
}

// findOldContainer returns the ID of the container that is NOT the newID.
func findOldContainer(ctx context.Context, service, newID string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps",
		"--filter", "label=com.docker.compose.service="+service,
		"--format", "{{.ID}}",
	).Output()
	if err != nil {
		return "", fmt.Errorf("docker ps: %w", err)
	}
	return selectOldContainer(strings.Fields(string(out)), newID, service)
}

// selectOldContainer picks the first candidate ID that is not newID (by
// prefix match in either direction, since Docker IDs can appear truncated or
// full-length depending on the source). Uses shortID rather than slicing
// newID directly — newID can be shorter than 12 characters (a mocked
// runtime, or a future Docker output format change), and a raw newID[:12]
// would panic in that case.
func selectOldContainer(candidateIDs []string, newID, service string) (string, error) {
	newShort := shortID(newID)
	for _, id := range candidateIDs {
		if !strings.HasPrefix(newID, id) && !strings.HasPrefix(id, newShort) {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not find old container for service %q", service)
}

// containerAddr returns the docker_rollout_mesh "ip:port" of the given container.
func containerAddr(ctx context.Context, id string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect",
		"--format",
		`{{range $n, $v := .NetworkSettings.Networks}}net={{$n}}={{$v.IPAddress}} {{end}}{{range $k, $v := .Config.ExposedPorts}}port={{$k}} {{end}}{{range .Config.Env}}env={{.}} {{end}}`,
		id,
	).Output()
	if err != nil {
		return "", err
	}
	var netTokens, portTokens, envTokens []string
	for _, f := range strings.Fields(string(out)) {
		if strings.HasPrefix(f, "net=") {
			netTokens = append(netTokens, strings.TrimPrefix(f, "net="))
		} else if strings.HasPrefix(f, "port=") {
			portTokens = append(portTokens, strings.TrimPrefix(f, "port="))
		} else if strings.HasPrefix(f, "env=") {
			envTokens = append(envTokens, strings.TrimPrefix(f, "env="))
		}
	}
	ip := pickMeshIP(netTokens)
	if ip == "" {
		return "", fmt.Errorf("container %s has no IP", id)
	}
	port, err := pickBackendPort(portTokens, envTokens)
	if err != nil {
		return "", fmt.Errorf("container %s port resolution failed: %w", id, err)
	}
	return ip + ":" + port, nil
}

func pickBackendPort(portTokens, envTokens []string) (string, error) {
	// Prefer ORBIT_BACKEND from container env because it's deterministic and
	// reflects the intended target port from generation time.
	for _, env := range envTokens {
		if !strings.HasPrefix(env, "ORBIT_BACKEND=") {
			continue
		}
		backend := strings.TrimPrefix(env, "ORBIT_BACKEND=")
		_, port, found := strings.Cut(backend, ":")
		if found && port != "" {
			if _, err := strconv.Atoi(port); err == nil {
				return port, nil
			}
		}
	}

	if len(portTokens) == 0 {
		return "80", nil
	}
	ports := make([]int, 0, len(portTokens))
	for _, token := range portTokens {
		portStr := token
		if p, _, found := strings.Cut(token, "/"); found {
			portStr = p
		}
		p, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		ports = append(ports, p)
	}
	if len(ports) == 0 {
		return "", fmt.Errorf("no parseable exposed ports in %v", portTokens)
	}
	sort.Ints(ports)
	return strconv.Itoa(ports[0]), nil
}

// pickMeshIP selects the IP from the docker_rollout_mesh network out of a slice of
// "networkname=ip" tokens. Falls back to the first parseable IP if no mesh
// network is found.
func pickMeshIP(tokens []string) string {
	fallback := ""
	for _, token := range tokens {
		eq := strings.IndexByte(token, '=')
		if eq < 0 {
			continue
		}
		name, ip := token[:eq], token[eq+1:]
		if ip == "" {
			continue
		}
		if fallback == "" {
			fallback = ip
		}
		if strings.HasSuffix(name, "docker_rollout_mesh") {
			return ip
		}
	}
	return fallback
}

// ── Control API helpers ───────────────────────────────────────────────────────

func registerBackend(ctx context.Context, opts Options, id, addr string, log *zap.Logger) error {
	body, _ := json.Marshal(map[string]string{"id": id, "addr": addr})
	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost, opts.ControlAddr+"/backends", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if opts.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+opts.APIToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST /backends: %w", err)
	}
	defer resp.Body.Close()
	// 201 Created = registered; 409 Conflict = already registered (idempotent — safe to continue).
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("POST /backends: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func serviceReplicaCount(ctx context.Context, service string) (int, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps",
		"--filter", "label=com.docker.compose.service="+service,
		"--format", "{{.ID}}",
	).Output()
	if err != nil {
		return 0, fmt.Errorf("docker ps: %w", err)
	}
	return len(strings.Fields(string(out))), nil
}

func scaleService(ctx context.Context, composeFile, service string, replicas int) error {
	return composeRun(ctx, composeFile, "up", "-d", "--no-deps",
		"--scale", fmt.Sprintf("%s=%d", service, replicas), service)
}

func drainBackend(ctx context.Context, opts Options, id string, log *zap.Logger) error {
	req, err := http.NewRequestWithContext(ctx,
		http.MethodPut, opts.ControlAddr+"/backends/"+id+"/drain", nil)
	if err != nil {
		return err
	}
	if opts.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+opts.APIToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("PUT /backends/%s/drain: unexpected status %d", id, resp.StatusCode)
	}
	return nil
}

func deregisterBackend(ctx context.Context, opts Options, id string, log *zap.Logger) error {
	req, err := http.NewRequestWithContext(ctx,
		http.MethodDelete, opts.ControlAddr+"/backends/"+id, nil)
	if err != nil {
		return err
	}
	if opts.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+opts.APIToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE /backends/%s: %w", id, err)
	}
	defer resp.Body.Close()
	// 204 No Content = removed; 404 Not Found = already gone (idempotent — both are success).
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("DELETE /backends/%s: unexpected status %d", id, resp.StatusCode)
	}
	return nil
}
