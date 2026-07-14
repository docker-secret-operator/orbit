package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// ErrRecoveryNotWired is returned by TriggerRecovery when no RecoveryFunc
// has been attached via SetRecoveryTrigger.
var ErrRecoveryNotWired = errors.New("recovery trigger not wired")

// ErrRecoveryInFlight is returned by TriggerRecovery when another recovery
// pass is already running.
var ErrRecoveryInFlight = errors.New("a recovery pass is already in progress")

// RecoveryOutcome is the response body for POST /recover — the same
// information state.GenerateRecoveryPlan already computes and the proxy
// already acts on at startup, exposed on demand instead of only once per
// container lifetime. Field names are part of Orbit's Stable API Policy
// once released — do not rename without a major version bump.
type RecoveryOutcome struct {
	Timestamp time.Time `json:"timestamp"`

	// Epoch is the recovery plan's execution epoch (state.RecoveryPlan.Epoch)
	// — a monotonic counter so callers can tell two recovery passes apart
	// even if their other fields happen to match.
	Epoch uint64 `json:"epoch"`

	// Action mirrors state.RecoveryAction: restore_single,
	// restore_with_draining, inferred_fallback, or degraded.
	Action string `json:"action"`

	// AuthoritativeGeneration is the generation recovery determined should
	// hold traffic. Empty only when Action is "degraded" — see FailedReason.
	AuthoritativeGeneration string `json:"authoritative_generation,omitempty"`

	Reason       string `json:"reason,omitempty"`
	FailedReason string `json:"failed_reason,omitempty"`

	// BackendsRestored is how many candidates from the recovery plan were
	// actually re-registered with the proxy (after revalidation — see
	// cmd/docker-orbit/main.go's runProxy for why some candidates are
	// skipped even when the plan lists them).
	BackendsRestored int `json:"backends_restored"`

	// ProxyStatus is the resulting startup/health state after this recovery
	// pass, using the same vocabulary as StatusReport.ProxyStatus.
	ProxyStatus string `json:"proxy_status"`
}

// RecoveryFunc performs one real recovery pass — the identical
// state.GenerateRecoveryPlan + backend-registration sequence
// cmd/docker-orbit/main.go's runProxy runs at startup — and reports its
// outcome. Set via ControlServer.SetRecoveryTrigger by that same startup
// code, so `docker orbit recover` (via POST /recover) can never diverge from
// what actually happens when the proxy boots: there is exactly one
// implementation of "what recovery does," with two triggers (startup,
// on-demand), not two implementations.
type RecoveryFunc func(ctx context.Context) (RecoveryOutcome, error)

// SetRecoveryTrigger attaches the real recovery implementation. Must be
// called before ListenAndServe for POST /recover to do anything; without it,
// the endpoint returns 503 rather than silently no-op'ing or faking a result.
func (cs *ControlServer) SetRecoveryTrigger(fn RecoveryFunc) {
	cs.recoveryMu.Lock()
	defer cs.recoveryMu.Unlock()
	cs.recoveryFn = fn
}

// TriggerRecovery runs one serialized, on-demand recovery pass through the
// exact same recoveryMu/recoveryInFlight guard POST /recover uses — the
// choke point every caller of recovery must go through, in-process or over
// HTTP, so no two passes ever run concurrently and both mutate the backend
// registry at once (state.GenerateRecoveryPlan's epoch counter alone
// doesn't prevent that — the registry mutation does need this here).
//
// In-process callers (e.g. cmd/docker-orbit's periodic rediscovery ticker)
// must call this instead of invoking their own recovery pass directly —
// doing so would bypass this guard and let a ticker-triggered pass race a
// concurrent POST /recover, each independently writing persisted state
// (last-write-wins) and double-counting metrics.
func (cs *ControlServer) TriggerRecovery(ctx context.Context) (RecoveryOutcome, error) {
	cs.recoveryMu.Lock()
	fn := cs.recoveryFn
	if fn == nil {
		cs.recoveryMu.Unlock()
		return RecoveryOutcome{}, ErrRecoveryNotWired
	}
	if cs.recoveryInFlight {
		cs.recoveryMu.Unlock()
		return RecoveryOutcome{}, ErrRecoveryInFlight
	}
	cs.recoveryInFlight = true
	cs.recoveryMu.Unlock()

	defer func() {
		cs.recoveryMu.Lock()
		cs.recoveryInFlight = false
		cs.recoveryMu.Unlock()
	}()

	return fn(ctx)
}

// POST /recover — triggers a real, on-demand recovery pass via TriggerRecovery.
func (cs *ControlServer) handleRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r.Method, "POST")
		return
	}

	outcome, err := cs.TriggerRecovery(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, ErrRecoveryNotWired):
			writeErr(w, http.StatusServiceUnavailable,
				"recovery trigger not wired — this proxy build predates POST /recover, or SetRecoveryTrigger was never called",
				"unavailable")
		case errors.Is(err, ErrRecoveryInFlight):
			writeErr(w, http.StatusConflict, "a recovery pass is already in progress", "conflict")
		default:
			writeErr(w, http.StatusInternalServerError, "recovery failed: "+err.Error(), "internal_error")
		}
		return
	}
	writeJSON(w, http.StatusOK, outcome)
}

// recoveryState holds POST /recover's serialization guard — a separate
// sync.Mutex from startupStateMu because recovery execution can take
// meaningfully longer than a startup-state read/write and shouldn't block
// unrelated readiness checks.
type recoveryState struct {
	recoveryMu       sync.Mutex
	recoveryFn       RecoveryFunc
	recoveryInFlight bool
}
