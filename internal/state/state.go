// Package state manages generation authority and rollout state persistence.
//
// This package implements deterministic, crash-safe generation-centric recovery
// for zero-downtime deployments. All state is persisted to disk using atomic
// writes with fsync, and cross-process access is protected by advisory locking.
//
// Core concepts:
// - ActiveGenerationState: Authoritative traffic owner (persisted)
// - RolloutState: In-flight rollout metadata (persisted)
// - GenerationInventory: Current health snapshot from Docker
// - RecoveryPlan: Deterministic recovery action (immutable)
package state

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

// SchemaVersion is the current state file format version.
// Increment when making breaking changes to state file format.
const SchemaVersion = 1

// ============================================================================
// Active Generation State (Authoritative)
// ============================================================================

// ActiveGenerationState represents the authoritative traffic owner.
// This file is SOURCE OF TRUTH for which generation should receive traffic.
// File: /var/lib/orbit/active-generation-{service}.json
type ActiveGenerationState struct {
	SchemaVersion    int       `json:"schema_version"`
	Service          string    `json:"service"`
	ActiveGeneration string    `json:"active_generation"`
	Revision         int64     `json:"revision"`          // Monotonic counter
	PreviousRevision int64     `json:"previous_revision"` // For CAS semantics
	UpdatedAt        time.Time `json:"updated_at"`
}

// ============================================================================
// Rollout State (In-Flight Operations)
// ============================================================================

// RolloutPhase represents the current phase of a rollout operation.
type RolloutPhase string

const (
	RolloutPreparing  RolloutPhase = "preparing"  // New generation scaling
	RolloutValidating RolloutPhase = "validating" // Waiting for health
	RolloutDraining   RolloutPhase = "draining"   // Old generation draining
	RolloutCommitting RolloutPhase = "committing" // Authority switch in progress
	RolloutCompleted  RolloutPhase = "completed"  // Rollout finished
	RolloutFailed     RolloutPhase = "failed"     // Rollout failed
)

// AuthorityState represents the current authority ownership during transitions.
type AuthorityState string

const (
	AuthorityOld           AuthorityState = "old"           // Old generation owns all traffic
	AuthorityTransitioning AuthorityState = "transitioning" // Switch in progress
	AuthorityNew           AuthorityState = "new"           // New generation owns all traffic
)

// RolloutState represents an in-flight rollout operation.
// File: /var/lib/orbit/rollout-{service}.json
// Present only during active rollout; deleted on completion.
type RolloutState struct {
	SchemaVersion      int                    `json:"schema_version"`
	Service            string                 `json:"service"`
	OldGeneration      string                 `json:"old_generation"`
	NewGeneration      string                 `json:"new_generation"`
	Phase              RolloutPhase           `json:"phase"`
	Authority          AuthorityState         `json:"authority"`
	RollbackCandidate  string                 `json:"rollback_candidate"`
	StartedAt          time.Time              `json:"started_at"`
	TransitionStart    time.Time              `json:"transition_start"`    // When authority transition began
	TransitionDeadline time.Time              `json:"transition_deadline"` // Max transition duration
	DrainDeadline      time.Time              `json:"drain_deadline"`
	DrainStartedAt     time.Time              `json:"drain_started_at"`  // When drain phase began (for progress detection)
	LastProgressAt     time.Time              `json:"last_progress_at"`  // When last progress detected (resets timeout clock)
	VolumeSnapshots    map[string]interface{} `json:"volumes,omitempty"` // Volume state snapshots for recovery/rollback

	// Revision/PreviousRevision give WriteRolloutState the same CAS
	// (Compare-And-Swap) protection as WriteActiveGenerationState: a caller
	// must set PreviousRevision to the Revision it last read, or the write
	// is rejected as a stale write over a concurrent modification.
	Revision         int64 `json:"revision"`
	PreviousRevision int64 `json:"previous_revision"`
}

// ============================================================================
// Generation Metrics and Inventory
// ============================================================================

// GenerationMetrics describes health state of a single generation.
type GenerationMetrics struct {
	Generation             string
	HealthyCount           int
	StartingCount          int
	UnhealthyCount         int
	TotalCount             int
	CreatedAt              time.Time // When generation was created
	FirstHealthyAt         time.Time // When first became healthy
	ContinuousHealthyStart time.Time // Start of current healthy streak
	LastHealthyCheck       time.Time // When last verified healthy
}

// ContinuousHealthyStart Semantics:
//
// SOURCE OF TRUTH:
//   - If all containers healthy: use oldest container.CreatedAt (conservative)
//   - If any container unhealthy: reset to time.Now() (streak breaks)
//   - Never overestimate uptime
//
// RESET SEMANTICS:
//   - Resets when: any container becomes unhealthy
//   - Preserved when: all containers remain healthy
//   - Exception: transient failures < 5s don't reset (future: implement grace period)
//
// RESTART SEMANTICS (process restart):
//   - Lost on restart (not persisted in state)
//   - Derived from container metadata: DeriveHealthStreakStartTime()
//   - Conservative: assumes streak started at oldest container creation time
//   - Logged: "health streak: derived from oldest container"
//
// USAGE:
//   - Selection algorithm: longest ContinuousHealthyStart wins
//   - Enables deterministic generation selection across restarts
//   - Prevents selecting fresh deployments over stable ones

// GenerationInventory is a snapshot of current container health grouped by generation.
// SnapshotTime ensures recovery plans are generated from a coherent observation window.
type GenerationInventory struct {
	Service               string
	SnapshotTime          time.Time                    // When snapshot was taken (for coherence)
	ActiveGeneration      string                       // From state file (authoritative)
	GenerationStates      map[string]GenerationMetrics // Gen → health
	Backends              map[string][]BackendInfo     // Gen → backends
	HealthyGenerations    []string                     // At least one healthy
	OrphanGenerations     []string                     // Unowned, old gens
	ContainerCount        int
	HealthyBackendCount   int
	StartingBackendCount  int
	UnhealthyBackendCount int
}

// BackendInfo represents raw backend data discovered from Docker.
type BackendInfo struct {
	ID     string
	Addr   string
	Health string
}

// GetBackendsForGeneration returns all backends discovered for a specific generation.
func (i *GenerationInventory) GetBackendsForGeneration(gen string) []BackendInfo {
	if i.Backends == nil {
		return nil
	}
	return i.Backends[gen]
}

// ============================================================================
// Recovery Planning
// ============================================================================

// RecoveryAction describes what recovery will do.
type RecoveryAction string

const (
	RecoveryRestoreSingle       RecoveryAction = "restore_single"        // Only authority gen
	RecoveryRestoreWithDraining RecoveryAction = "restore_with_draining" // Auth + draining gens
	RecoveryInferredFallback    RecoveryAction = "inferred_fallback"     // No state, inferred
	RecoveryDegraded            RecoveryAction = "degraded"              // Unhealthy only
)

// TrafficRole describes the routing responsibility of a backend during recovery.
type TrafficRole string

const (
	TrafficRoleActive   TrafficRole = "active"   // Receives new traffic
	TrafficRoleDraining TrafficRole = "draining" // Finishes existing connections only
)

// CandidateValidity indicates whether a generation can be safely restored.
type CandidateValidity string

const (
	CandidateValid     CandidateValidity = "valid"     // Safe to restore
	CandidateUnhealthy CandidateValidity = "unhealthy" // No healthy backends
	CandidatePruned    CandidateValidity = "pruned"    // Deleted/missing
	CandidatePartial   CandidateValidity = "partial"   // Some containers missing
	CandidateStale     CandidateValidity = "stale"     // Too old (>24h)
)

// BackendSnapshot is a runtime-discovered backend passed into recovery planning.
// It is never persisted; it is rediscovered from Docker on every recovery attempt.
type BackendSnapshot struct {
	Generation string
	ID         string
	Addr       string
	Health     string // "healthy" | "unhealthy" | "starting" | "unknown"
}

// BackendCandidate is a backend eligible for registration during recovery.
type BackendCandidate struct {
	Generation     string
	ID             string
	Addr           string
	Health         string      // proxy.HealthStatus (healthy/unhealthy/starting/unknown)
	TrafficRole    TrafficRole // How this backend will be used
	Reason         string      // Why this candidate was selected
	ValidityStatus CandidateValidity
}

// RecoveryPlan is immutable recovery decision computed at recovery time.
// Once generated, plan cannot be modified during reconciliation.
// DecisionTrace records the reasoning for debugging and incident analysis.
type RecoveryPlan struct {
	Service                  string
	Epoch                    uint64 // Execution epoch for replay tracking
	GeneratedAt              time.Time
	Action                   RecoveryAction
	AuthoritativeGeneration  string
	TempDrainingGenerations  []string
	BackendsToRestore        []BackendCandidate
	OrphanedGenerationsFound []string
	InterruptedRollout       *RolloutState
	FailedReason             string
	Reason                   string
	DecisionTrace            []string // Detailed decision logic trace
}

// MarshalJSON ensures deterministic JSON serialization for audit trails.
// Fields are serialized in explicit order for stable diffs.
func (p *RecoveryPlan) MarshalJSON() ([]byte, error) {
	type ordered struct {
		Service                  string             `json:"service"`
		Epoch                    uint64             `json:"epoch"`
		GeneratedAt              time.Time          `json:"generated_at"`
		Action                   string             `json:"action"`
		AuthoritativeGeneration  string             `json:"authoritative_generation"`
		TempDrainingGenerations  []string           `json:"temp_draining_generations,omitempty"`
		BackendsToRestore        []BackendCandidate `json:"backends_to_restore"`
		OrphanedGenerationsFound []string           `json:"orphaned_generations_found,omitempty"`
		FailedReason             string             `json:"failed_reason,omitempty"`
		Reason                   string             `json:"reason"`
		DecisionTrace            []string           `json:"decision_trace"`
	}

	return json.Marshal(&ordered{
		Service:                  p.Service,
		Epoch:                    p.Epoch,
		GeneratedAt:              p.GeneratedAt,
		Action:                   string(p.Action),
		AuthoritativeGeneration:  p.AuthoritativeGeneration,
		TempDrainingGenerations:  p.TempDrainingGenerations,
		BackendsToRestore:        p.BackendsToRestore,
		OrphanedGenerationsFound: p.OrphanedGenerationsFound,
		FailedReason:             p.FailedReason,
		Reason:                   p.Reason,
		DecisionTrace:            p.DecisionTrace,
	})
}

// ============================================================================
// State Loading Errors
// ============================================================================

// StateLoadError represents failure loading state from disk.
type StateLoadError struct {
	Path    string
	Reason  string
	IsFatal bool // Fatal: requires operator intervention
}

func (e *StateLoadError) Error() string {
	if e.IsFatal {
		return "FATAL: " + e.Reason
	}
	return e.Reason
}

// ============================================================================
// State Manager (Main Interface)
// ============================================================================

// StateManager manages all persistent state operations.
// Provides thread-safe access with in-process RWMutex + cross-process advisory locking.
type StateManager struct {
	stateDir string
	log      interface{} // zap.Logger (avoid circular import)

	// In-process locking (in addition to filesystem locks)
	// Protects against concurrent reads/writes within same process
	stateLocks     map[string]*sync.RWMutex
	stateLocksLock sync.Mutex

	// Recovery epoch counter (for deterministic recovery tracking)
	epochCounter   uint64
	epochCounterMu sync.Mutex
}

// NewStateManager creates a new state manager.
func NewStateManager(stateDir string, log interface{}) *StateManager {
	return &StateManager{
		stateDir:     stateDir,
		log:          log,
		stateLocks:   make(map[string]*sync.RWMutex),
		epochCounter: 0,
	}
}

// NextEpoch returns the next recovery epoch number.
func (sm *StateManager) NextEpoch() uint64 {
	sm.epochCounterMu.Lock()
	defer sm.epochCounterMu.Unlock()
	sm.epochCounter++
	return sm.epochCounter
}

// getInProcessLock returns the in-process lock for a service (not cross-process).
func (sm *StateManager) getInProcessLock(service string) *sync.RWMutex {
	sm.stateLocksLock.Lock()
	defer sm.stateLocksLock.Unlock()

	if _, exists := sm.stateLocks[service]; !exists {
		sm.stateLocks[service] = &sync.RWMutex{}
	}

	return sm.stateLocks[service]
}

// ============================================================================
// Helper Methods
// ============================================================================

// ActiveGenerationPath returns the file path for active generation state.
func (sm *StateManager) ActiveGenerationPath(service string) string {
	return filepath.Join(sm.stateDir, fmt.Sprintf("active-generation-%s.json", service))
}

// rolloutStatePath returns the file path for rollout state.
func (sm *StateManager) rolloutStatePath(service string) string {
	return filepath.Join(sm.stateDir, fmt.Sprintf("rollout-%s.json", service))
}

// StateLockPath returns the file path for advisory lock.
func (sm *StateManager) StateLockPath(service string) string {
	return filepath.Join(sm.stateDir, fmt.Sprintf(".%s.lock", service))
}
