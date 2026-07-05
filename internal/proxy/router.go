package proxy

import (
	"fmt"
	"sync/atomic"
)

// Router selects backends using a lock-free per-registry atomic round-robin
// counter over the registry's Active() snapshot. It owns no backend state — it
// only queries the Registry (Runtime Constitution §III Layer 3).
type Router struct {
	registry *Registry
	counter  atomic.Uint64
	metrics  FailoverMetrics // optional observability sink; nil-safe
}

// SetMetrics attaches an optional observability sink for candidate-selection
// counters. Startup-only; nil is safe.
func (r *Router) SetMetrics(m FailoverMetrics) { r.metrics = m }

// ReportDialFailure records a transport dial failure against a backend in the
// Runtime Registry (advisory only — the Health Controller owns eviction). Used
// by the Traffic Engine during passive failover.
func (r *Router) ReportDialFailure(id string) { r.registry.ReportDialFailure(id) }

// NewRouter creates a Router backed by the given registry.
func NewRouter(registry *Registry) *Router {
	return &Router{registry: registry}
}

// Next returns the next active backend using round-robin selection.
//
// Returns an error if no active backends are available. Callers must not
// silently drop the connection — log and close it.
func (r *Router) Next() (*Backend, error) {
	candidates, err := r.NextCandidates(1)
	if err != nil {
		return nil, err
	}
	return candidates[0], nil
}

// NextCandidates returns up to max active backends in deterministic failover
// order: the round-robin primary first, then the remaining active backends in
// stable ID order. The primary's request counter is incremented (it is the
// intended target); callers that fail over to a later candidate own the
// decision to retry, per the Runtime Constitution's Traffic Engine boundary.
//
// Returns an error if no active backends are available. Determinism (INV-7) is
// preserved: given the same Active() set and counter value, the ordering is
// identical.
func (r *Router) NextCandidates(max int) ([]*Backend, error) {
	active := r.registry.Active()
	if len(active) == 0 {
		if r.metrics != nil {
			r.metrics.IncCandidateExhaustion()
		}
		return nil, fmt.Errorf("router: no active backends available")
	}
	if max < 1 {
		max = 1
	}
	if max > len(active) {
		max = len(active)
	}

	start := int(r.counter.Add(1)-1) % len(active)
	if start < 0 {
		start += len(active)
	}

	out := make([]*Backend, 0, max)
	for i := 0; i < max; i++ {
		b := &active[(start+i)%len(active)]
		out = append(out, b)
	}

	if r.metrics != nil {
		r.metrics.IncCandidateSelection()
	}
	// Count the primary target only.
	r.registry.incrRequests(out[0].ID)
	return out, nil
}
