package proxy

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// RuntimeFeature identifies a gated runtime capability. Every major runtime
// capability passes through: Implemented → Validated → Activation Gate →
// Production Enabled. No capability may skip a stage.
type RuntimeFeature string

const (
	FeatureContinuousHealth    RuntimeFeature = "continuous_health"
	FeaturePassiveFailover     RuntimeFeature = "passive_failover"
	FeatureIntelligentDraining RuntimeFeature = "intelligent_draining"
	FeatureRuntimeHA           RuntimeFeature = "runtime_ha"
)

// Prerequisites enumerates runtime capabilities an activation may depend on.
// It is a plain value so prerequisite evaluation is deterministic and testable.
type Prerequisites struct {
	RegistryAuthoritative    bool // WP-A
	CandidateSelection       bool // WP-B1
	RetryPolicy              bool // WP-B1
	PassiveFailoverExecution bool // WP-B2 (pending)
	RuntimeMetrics           bool // WP-B1 / WP-C
	ZeroBackendProtection    bool // WP-C.5
}

// ImplementedPrerequisites reflects the capabilities that exist as of the
// current build. PassiveFailoverExecution is false until WP-B2 lands — which is
// precisely why FeatureContinuousHealth cannot yet be enabled: if Health could
// evict the only backend of a service before failover exists, availability
// would drop, violating the Runtime Constitution.
func ImplementedPrerequisites() Prerequisites {
	return Prerequisites{
		RegistryAuthoritative:    true,
		CandidateSelection:       true,
		RetryPolicy:              true,
		RuntimeMetrics:           true,
		ZeroBackendProtection:    true,
		PassiveFailoverExecution: true, // WP-B2 (implemented)
	}
}

// requiredPrereqs returns the capabilities that must all be present before a
// feature may be enabled. Deterministic.
func requiredPrereqs(f RuntimeFeature) Prerequisites {
	switch f {
	case FeatureContinuousHealth:
		// Health may only shed backends once the runtime can serve a request
		// despite an unhealthy backend: passive-failover execution and
		// zero-backend protection must both exist.
		return Prerequisites{
			RegistryAuthoritative:    true,
			CandidateSelection:       true,
			RetryPolicy:              true,
			PassiveFailoverExecution: true,
			RuntimeMetrics:           true,
			ZeroBackendProtection:    true,
		}
	case FeaturePassiveFailover:
		return Prerequisites{
			RegistryAuthoritative: true,
			CandidateSelection:    true,
			RetryPolicy:           true,
			RuntimeMetrics:        true,
		}
	default:
		return Prerequisites{}
	}
}

// missingFrom returns, sorted, the names of required prerequisites not present
// in have. Empty result means all requirements are satisfied.
func (need Prerequisites) missingFrom(have Prerequisites) []string {
	var missing []string
	check := func(name string, req, got bool) {
		if req && !got {
			missing = append(missing, name)
		}
	}
	check("registry_authoritative", need.RegistryAuthoritative, have.RegistryAuthoritative)
	check("candidate_selection", need.CandidateSelection, have.CandidateSelection)
	check("retry_policy", need.RetryPolicy, have.RetryPolicy)
	check("passive_failover_execution", need.PassiveFailoverExecution, have.PassiveFailoverExecution)
	check("runtime_metrics", need.RuntimeMetrics, have.RuntimeMetrics)
	check("zero_backend_protection", need.ZeroBackendProtection, have.ZeroBackendProtection)
	sort.Strings(missing)
	return missing
}

// ActivationMetrics is the optional runtime-readiness observability sink
// (nil-safe), satisfied structurally by *metrics.Proxy. These describe runtime
// readiness, not health.
type ActivationMetrics interface {
	IncActivationAttempts()
	IncFeatureBlocked()
	SetFeaturesEnabled(n int)
}

// RuntimeFeatures is the single activation gate for runtime capabilities. All
// features default to DISABLED; activation is explicit and permitted only when
// every required prerequisite exists (deterministic). Future capabilities
// (passive failover, intelligent draining, runtime HA) reuse this same gate.
type RuntimeFeatures struct {
	mu      sync.RWMutex
	enabled map[RuntimeFeature]bool
	metrics ActivationMetrics
}

// NewRuntimeFeatures returns a gate with all features disabled.
func NewRuntimeFeatures(m ActivationMetrics) *RuntimeFeatures {
	return &RuntimeFeatures{enabled: map[RuntimeFeature]bool{}, metrics: m}
}

// IsEnabled reports whether a feature is currently active. Features are
// disabled until explicitly enabled through Enable.
func (rf *RuntimeFeatures) IsEnabled(f RuntimeFeature) bool {
	rf.mu.RLock()
	defer rf.mu.RUnlock()
	return rf.enabled[f]
}

// Enable attempts to activate a feature given the capabilities currently
// available (have). It succeeds only if every prerequisite is present;
// otherwise the feature stays disabled and an error naming the missing
// prerequisites is returned. Deterministic and idempotent.
func (rf *RuntimeFeatures) Enable(f RuntimeFeature, have Prerequisites) error {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.metrics != nil {
		rf.metrics.IncActivationAttempts()
	}
	if missing := requiredPrereqs(f).missingFrom(have); len(missing) > 0 {
		if rf.metrics != nil {
			rf.metrics.IncFeatureBlocked()
		}
		return fmt.Errorf("runtime: cannot enable %q — missing prerequisites: %s",
			f, strings.Join(missing, ", "))
	}
	rf.enabled[f] = true
	if rf.metrics != nil {
		rf.metrics.SetFeaturesEnabled(rf.countLocked())
	}
	return nil
}

// Disable deactivates a feature (used for controlled rollback of an activation).
func (rf *RuntimeFeatures) Disable(f RuntimeFeature) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	delete(rf.enabled, f)
	if rf.metrics != nil {
		rf.metrics.SetFeaturesEnabled(rf.countLocked())
	}
}

func (rf *RuntimeFeatures) countLocked() int {
	n := 0
	for _, on := range rf.enabled {
		if on {
			n++
		}
	}
	return n
}

// ── Reserved runtime-state design (G4) ─────────────────────────────────────────
//
// A future, finer-grained health lifecycle is intentionally RESERVED here but
// NOT implemented — WP-C.5 changes no Registry semantics and adds no routing
// behavior:
//
//	Healthy → Suspect → Unhealthy → Failed
//
// "Suspect" would let a backend that has begun failing probes be de-prioritized
// (lower routing weight) before full eviction, softening transitions further.
// Introducing it will require its own work package; today's BackendState machine
// (Active/Draining/Unhealthy/Failed) is unchanged.
