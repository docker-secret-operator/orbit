// Package proxy implements the Orbit TCP reverse proxy components.
package proxy

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// BackendState is the authoritative runtime lifecycle state of a backend
// (Runtime Constitution §III Layer 2). Only StateActive backends receive new
// connections. Transitions are validated (see canTransition) so the Registry
// can never be driven into an inconsistent lifecycle.
type BackendState string

const (
	StateActive    BackendState = "active"    // receiving new connections
	StateDraining  BackendState = "draining"  // no new connections; finishing in-flight work
	StateUnhealthy BackendState = "unhealthy" // failing health checks; excluded from routing
	StateFailed    BackendState = "failed"    // terminal; excluded until re-registered
)

// canTransition reports whether a backend may move from -> to. Transitions are
// deterministic. A no-op (from == to) is always allowed (idempotent writes).
//
//	active    → draining | unhealthy | failed
//	unhealthy → active   | draining  | failed
//	draining  → (only leaves via Remove)
//	failed    → (terminal until Remove + re-Add)
func canTransition(from, to BackendState) bool {
	if from == to {
		return true
	}
	switch from {
	case StateActive:
		return to == StateDraining || to == StateUnhealthy || to == StateFailed
	case StateUnhealthy:
		return to == StateActive || to == StateDraining || to == StateFailed
	case StateDraining, StateFailed:
		return false
	}
	return false
}

// Backend represents a single registered upstream instance.
type Backend struct {
	// ID uniquely identifies this backend. Caller-supplied; must be non-empty.
	ID string `json:"id"`

	// Addr is the dial address of the upstream: "host:port".
	Addr string `json:"addr"`

	// Generation labels which deployment version this backend belongs to.
	Generation string `json:"generation,omitempty"`

	// Draining is retained for backward compatibility — the control API JSON,
	// /metrics, and the rollout HTTP flow all read it. It is kept in sync with
	// State (Draining == State==StateDraining). New code should read State.
	Draining bool `json:"draining"`

	// State is the authoritative runtime lifecycle state.
	State BackendState `json:"state"`

	// AddedAt is set automatically by Registry.Add.
	AddedAt time.Time `json:"added_at"`

	// LastStateChange records the time of the most recent State transition.
	LastStateChange time.Time `json:"last_state_change,omitempty"`

	// The following counters live on the heap behind pointers so struct copies
	// (snapshots) remain race-safe: the pointer is shared, the atomic is the
	// single source of truth.
	requests    *atomic.Uint64 // total connections routed to this backend
	activeConns *atomic.Int64  // currently-open proxied connections
	failures    *atomic.Uint64 // advisory dial-failure signals
}

// Requests returns the total connections routed to this backend.
func (b *Backend) Requests() uint64 {
	if b.requests == nil {
		return 0
	}
	return b.requests.Load()
}

// IncrRequests atomically increments the connection counter.
func (b *Backend) IncrRequests() {
	if b.requests != nil {
		b.requests.Add(1)
	}
}

// MarshalledRequests is an alias for Requests() — used in JSON serialisation.
func (b *Backend) MarshalledRequests() uint64 { return b.Requests() }

// ActiveConnections returns the number of currently-open proxied connections.
func (b *Backend) ActiveConnections() int64 {
	if b.activeConns == nil {
		return 0
	}
	return b.activeConns.Load()
}

// Failures returns the advisory dial-failure count.
func (b *Backend) Failures() uint64 {
	if b.failures == nil {
		return 0
	}
	return b.failures.Load()
}

// Registry is the authoritative runtime state plane: a thread-safe store of
// backends keyed by ID (Runtime Constitution §III Layer 2).
//
// Invariants:
//   - IDs are globally unique.
//   - All lifecycle mutations (Add/Remove/SetState/SetDraining/SetHealth) are
//     atomic with respect to each other under mu.
//   - Snapshot reads (Active, Backends, Snapshot, Get) return value copies that
//     are consistent: mutable counters live behind pointers, and State/Draining
//     are copied under the lock.
//   - The Registry performs NO I/O (INV-8): no network, Docker, or disk access.
type Registry struct {
	mu            sync.RWMutex
	store         map[string]*Backend
	metrics       FailoverMetrics // optional observability sink; nil-safe; in-memory counters only (INV-8 preserved)
	onZeroBackend func(id string) // optional zero-backend-protection callback (metric/log); nil-safe
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() *Registry {
	return &Registry{store: make(map[string]*Backend)}
}

// Add registers a new backend in StateActive. Returns an error if ID or Addr is
// empty, or the ID is already registered.
func (r *Registry) Add(b Backend) error {
	if b.ID == "" {
		return fmt.Errorf("registry: backend ID must not be empty")
	}
	if b.Addr == "" {
		return fmt.Errorf("registry: backend %q: addr must not be empty", b.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.store[b.ID]; exists {
		return fmt.Errorf("registry: backend %q is already registered", b.ID)
	}
	now := time.Now()
	b.AddedAt = now
	b.LastStateChange = now
	b.State = StateActive
	b.Draining = false
	b.requests = &atomic.Uint64{}
	b.activeConns = &atomic.Int64{}
	b.failures = &atomic.Uint64{}
	r.store[b.ID] = &b
	return nil
}

// Remove deregisters a backend by ID.
func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.store[id]; !exists {
		return fmt.Errorf("registry: backend %q not found", id)
	}
	delete(r.store, id)
	return nil
}

// SetState transitions a backend to the given runtime state, validating the
// transition. Returns an error if the backend is unknown or the transition is
// invalid. Draining is kept in sync for backward compatibility.
func (r *Registry) SetState(id string, to BackendState) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	b, ok := r.store[id]
	if !ok {
		return fmt.Errorf("registry: backend %q not found", id)
	}
	if !canTransition(b.State, to) {
		return fmt.Errorf("registry: backend %q: invalid transition %s -> %s", id, b.State, to)
	}
	if b.State != to {
		b.State = to
		b.Draining = to == StateDraining
		b.LastStateChange = time.Now()
	}
	return nil
}

// State returns the current runtime state of a backend.
func (r *Registry) State(id string) (BackendState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.store[id]
	if !ok {
		return "", false
	}
	return b.State, true
}

// SetDraining marks a backend as draining (no new connections) without removing
// it. Idempotent. Returns an error if the ID is not found or the backend is in
// a terminal state that cannot drain.
func (r *Registry) SetDraining(id string) error {
	return r.SetState(id, StateDraining)
}

// SetHealth records a health verdict decided by the Health Controller
// (Runtime Constitution §III Layer 5). It only flips between Active and
// Unhealthy; it never overrides Draining or Failed (owned by the deployment /
// terminal lifecycle). Unknown backend returns an error.
func (r *Registry) SetHealth(id string, healthy bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	b, ok := r.store[id]
	if !ok {
		return fmt.Errorf("registry: backend %q not found", id)
	}
	if b.State == StateDraining || b.State == StateFailed {
		return nil // health does not override deployment/terminal state
	}
	to := StateUnhealthy
	if healthy {
		to = StateActive
	}
	if b.State != to && canTransition(b.State, to) {
		b.State = to
		b.LastStateChange = time.Now()
	}
	return nil
}

// ReportDialFailure records an advisory dial-failure signal from the Traffic
// Engine. It does NOT change state — eviction decisions belong to the Health
// Controller (Runtime Constitution §III Layer 5). Unknown backend is ignored.
func (r *Registry) ReportDialFailure(id string) {
	r.mu.RLock()
	b := r.store[id]
	r.mu.RUnlock()
	if b != nil && b.failures != nil {
		b.failures.Add(1)
		if r.metrics != nil {
			r.metrics.IncDialFailures()
		}
	}
}

// SetMetrics attaches an optional observability sink for infrastructure
// counters. Startup-only; nil is safe. Does not affect routing (INV-8-safe).
func (r *Registry) SetMetrics(m FailoverMetrics) { r.metrics = m }

// SetZeroBackendHook sets an optional callback invoked when zero-backend
// protection refuses a demotion (SetHealthGuarded). The runtime wires it to a
// warning metric and structured log; the Registry stays free of I/O (INV-8).
func (r *Registry) SetZeroBackendHook(fn func(id string)) { r.onZeroBackend = fn }

// SetHealthGuarded records a health verdict like SetHealth, but with
// ZERO-BACKEND PROTECTION: a demotion to Unhealthy that would leave the service
// with zero routable (Active) backends is refused to preserve availability —
// safety takes priority over correctness (Runtime Constitution). Promotions
// never reduce availability and are always applied. Returns whether the change
// was applied and, if refused, a human-readable reason.
//
// This is additive; existing SetHealth semantics are unchanged. The activated
// Health path (WP-B2) uses this guarded variant.
func (r *Registry) SetHealthGuarded(id string, healthy bool) (applied bool, reason string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	b, ok := r.store[id]
	if !ok {
		return false, "", fmt.Errorf("registry: backend %q not found", id)
	}
	if b.State == StateDraining || b.State == StateFailed {
		return false, "state not owned by health", nil
	}
	if healthy {
		if b.State != StateActive && canTransition(b.State, StateActive) {
			b.State = StateActive
			b.Draining = false
			b.LastStateChange = time.Now()
			return true, "", nil
		}
		return false, "no-op", nil
	}
	// Demotion: refuse if this is the last active backend.
	if b.State == StateActive {
		activeCount := 0
		for _, bb := range r.store {
			if bb.State == StateActive {
				activeCount++
			}
		}
		if activeCount <= 1 {
			if r.onZeroBackend != nil {
				r.onZeroBackend(id)
			}
			return false, "zero-backend protection: refused to evict the last active backend", nil
		}
		b.State = StateUnhealthy
		b.LastStateChange = time.Now()
		return true, "", nil
	}
	return false, "no-op", nil
}

// FailureCount returns the advisory dial-failure count for a backend (0 if
// unknown). Advisory only — state transitions are owned by later work packages.
func (r *Registry) FailureCount(id string) uint64 {
	r.mu.RLock()
	b := r.store[id]
	r.mu.RUnlock()
	if b == nil {
		return 0
	}
	return b.Failures()
}

// ResetFailureCount clears the advisory dial-failure count for a backend.
func (r *Registry) ResetFailureCount(id string) {
	r.mu.RLock()
	b := r.store[id]
	r.mu.RUnlock()
	if b != nil && b.failures != nil {
		b.failures.Store(0)
	}
}

// IncrementConnections records a newly-opened proxied connection to a backend.
func (r *Registry) IncrementConnections(id string) {
	r.mu.RLock()
	b := r.store[id]
	r.mu.RUnlock()
	if b != nil && b.activeConns != nil {
		b.activeConns.Add(1)
	}
}

// DecrementConnections records a closed proxied connection to a backend.
func (r *Registry) DecrementConnections(id string) {
	r.mu.RLock()
	b := r.store[id]
	r.mu.RUnlock()
	if b != nil && b.activeConns != nil {
		b.activeConns.Add(-1)
	}
}

// ActiveConnections returns the number of currently-open proxied connections
// for a backend (0 if unknown).
func (r *Registry) ActiveConnections(id string) int64 {
	r.mu.RLock()
	b := r.store[id]
	r.mu.RUnlock()
	if b == nil {
		return 0
	}
	return b.ActiveConnections()
}

// Get returns a copy of the backend with the given ID.
func (r *Registry) Get(id string) (Backend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	b, ok := r.store[id]
	if !ok {
		return Backend{}, false
	}
	return *b, true
}

// Backends returns a point-in-time snapshot of all registered backends (every
// state), sorted by ID. Safe to read concurrently with mutations. Returned
// values are read-only views; mutate only via Registry APIs.
func (r *Registry) Backends() []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Backend, 0, len(r.store))
	for _, b := range r.store {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Snapshot is the authoritative point-in-time view of the runtime state plane:
// every backend (all states), sorted by ID. Alias of Backends with an intent-
// revealing name for runtime consumers.
func (r *Registry) Snapshot() []Backend { return r.Backends() }

// Active returns a sorted snapshot of all backends in StateActive — the only
// backends eligible to receive new connections.
func (r *Registry) Active() []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Backend, 0, len(r.store))
	for _, b := range r.store {
		if b.State == StateActive {
			out = append(out, *b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len returns the total number of registered backends (all states).
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.store)
}

// incrRequests atomically increments the request counter for the backend.
// Called by the router after each connection is assigned.
func (r *Registry) incrRequests(id string) {
	r.mu.RLock()
	b := r.store[id]
	r.mu.RUnlock()
	if b != nil {
		b.IncrRequests()
	}
}
