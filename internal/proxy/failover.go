package proxy

import "fmt"

// FailoverMetrics is the minimal observability sink the Traffic Engine (Router)
// and Runtime Registry use to emit passive-failover *infrastructure* counters.
// It is satisfied structurally by *metrics.Proxy. A nil sink is safe everywhere
// (no-op). Emitting a counter is an in-memory atomic increment, so attaching a
// sink does not make the Registry perform I/O (INV-8 preserved).
type FailoverMetrics interface {
	IncDialFailures()
	IncCandidateSelection()
	IncCandidateExhaustion()
}

// RetryPolicy is the runtime-owned passive-failover retry policy.
//
// WP-B1 *defines* the policy; it performs no retries. WP-B2 will consume it to
// drive at-most-N failover dials in the Traffic Engine. Keeping the policy a
// plain, deterministic value (no I/O, no execution) makes the failover-behavior
// phase a small, testable change.
type RetryPolicy struct {
	// MaxRetries is the number of additional dial attempts after the primary.
	// 0 disables failover (primary only). Must be >= 0.
	MaxRetries int
}

// DefaultRetryPolicy returns the default policy: one retry after the primary.
func DefaultRetryPolicy() RetryPolicy { return RetryPolicy{MaxRetries: 1} }

// Candidates returns how many backends the Traffic Engine should request from
// the Router to satisfy this policy (primary + retries). Deterministic; no I/O.
func (p RetryPolicy) Candidates() int {
	if p.MaxRetries < 0 {
		return 1
	}
	return p.MaxRetries + 1
}

// Validate reports whether the policy is well-formed.
func (p RetryPolicy) Validate() error {
	if p.MaxRetries < 0 {
		return fmt.Errorf("retry policy: MaxRetries must be >= 0, got %d", p.MaxRetries)
	}
	return nil
}
