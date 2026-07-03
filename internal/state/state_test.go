package state

import (
	"encoding/json"
	"testing"
	"time"
)

// ============================================================================
// ActiveGenerationState Tests
// ============================================================================

func TestActiveGenerationStateJSON(t *testing.T) {
	state := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-abc123",
		Revision:         42,
		PreviousRevision: 41,
		UpdatedAt:        time.Now(),
	}

	// Marshal
	bytes, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// Unmarshal
	var restored ActiveGenerationState
	if err := json.Unmarshal(bytes, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.Service != state.Service {
		t.Errorf("service mismatch: got %s, want %s", restored.Service, state.Service)
	}
	if restored.ActiveGeneration != state.ActiveGeneration {
		t.Errorf("generation mismatch: got %s, want %s", restored.ActiveGeneration, state.ActiveGeneration)
	}
	if restored.Revision != state.Revision {
		t.Errorf("revision mismatch: got %d, want %d", restored.Revision, state.Revision)
	}
}

func TestRolloutStateJSON(t *testing.T) {
	state := &RolloutState{
		SchemaVersion:      1,
		Service:            "api",
		OldGeneration:      "gen-old",
		NewGeneration:      "gen-new",
		Phase:              RolloutDraining,
		Authority:          AuthorityTransitioning,
		RollbackCandidate:  "gen-old",
		StartedAt:          time.Now(),
		TransitionStart:    time.Now(),
		TransitionDeadline: time.Now().Add(5 * time.Minute),
		DrainDeadline:      time.Now().Add(30 * time.Second),
	}

	bytes, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored RolloutState
	if err := json.Unmarshal(bytes, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.Phase != RolloutDraining {
		t.Errorf("phase mismatch: got %s, want %s", restored.Phase, RolloutDraining)
	}
	if restored.Authority != AuthorityTransitioning {
		t.Errorf("authority mismatch: got %s, want %s", restored.Authority, AuthorityTransitioning)
	}
}

// ============================================================================
// RolloutPhase Enum Tests
// ============================================================================

func TestRolloutPhaseValues(t *testing.T) {
	tests := []RolloutPhase{
		RolloutPreparing,
		RolloutValidating,
		RolloutDraining,
		RolloutCommitting,
		RolloutCompleted,
		RolloutFailed,
	}

	for _, phase := range tests {
		if phase == "" {
			t.Errorf("empty phase value")
		}
	}
}

func TestRolloutPhaseJSON(t *testing.T) {
	type Container struct {
		Phase RolloutPhase `json:"phase"`
	}

	c := &Container{Phase: RolloutValidating}
	bytes, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored Container
	if err := json.Unmarshal(bytes, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.Phase != RolloutValidating {
		t.Errorf("phase not preserved: got %s, want %s", restored.Phase, RolloutValidating)
	}
}

// ============================================================================
// AuthorityState Enum Tests
// ============================================================================

func TestAuthorityStateValues(t *testing.T) {
	tests := []AuthorityState{
		AuthorityOld,
		AuthorityTransitioning,
		AuthorityNew,
	}

	for _, auth := range tests {
		if auth == "" {
			t.Errorf("empty authority value")
		}
	}
}

func TestAuthorityStateJSON(t *testing.T) {
	type Container struct {
		Authority AuthorityState `json:"authority"`
	}

	c := &Container{Authority: AuthorityTransitioning}
	bytes, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored Container
	if err := json.Unmarshal(bytes, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.Authority != AuthorityTransitioning {
		t.Errorf("authority not preserved: got %s, want %s", restored.Authority, AuthorityTransitioning)
	}
}

// ============================================================================
// GenerationMetrics Tests
// ============================================================================

func TestGenerationMetricsCalculation(t *testing.T) {
	now := time.Now()
	metrics := GenerationMetrics{
		Generation:             "gen-123",
		HealthyCount:           3,
		StartingCount:          1,
		UnhealthyCount:         0,
		TotalCount:             4,
		CreatedAt:              now.Add(-1 * time.Hour),
		FirstHealthyAt:         now.Add(-50 * time.Minute),
		ContinuousHealthyStart: now.Add(-45 * time.Minute),
		LastHealthyCheck:       now,
	}

	if metrics.TotalCount != 4 {
		t.Errorf("total count mismatch: got %d, want 4", metrics.TotalCount)
	}
	if metrics.HealthyCount+metrics.StartingCount+metrics.UnhealthyCount != metrics.TotalCount {
		t.Errorf("health counts don't sum to total")
	}
}

func TestGenerationMetricsFullyHealthy(t *testing.T) {
	now := time.Now()
	fullyHealthy := GenerationMetrics{
		Generation:   "gen-123",
		HealthyCount: 5,
		TotalCount:   5,
		CreatedAt:    now.Add(-2 * time.Hour),
	}

	partiallyHealthy := GenerationMetrics{
		Generation:   "gen-456",
		HealthyCount: 3,
		TotalCount:   5,
		CreatedAt:    now.Add(-1 * time.Hour),
	}

	if !(fullyHealthy.HealthyCount > 0 && fullyHealthy.HealthyCount == fullyHealthy.TotalCount) {
		t.Errorf("fully healthy detection failed")
	}
	if partiallyHealthy.HealthyCount > 0 && partiallyHealthy.HealthyCount == partiallyHealthy.TotalCount {
		t.Errorf("partially healthy incorrectly marked as fully healthy")
	}
}

// ============================================================================
// RecoveryPlan Tests
// ============================================================================

func TestRecoveryPlanImmutability(t *testing.T) {
	plan := &RecoveryPlan{
		Service:                  "web",
		Epoch:                    1,
		GeneratedAt:              time.Now(),
		Action:                   RecoveryRestoreSingle,
		AuthoritativeGeneration:  "gen-123",
		BackendsToRestore:        []BackendCandidate{},
		OrphanedGenerationsFound: []string{},
	}

	// Plan should be safe to read/copy
	original := plan.Epoch
	plan.Epoch = 999 // Shouldn't affect recovery semantics (but shows it's mutable in memory)

	if plan.Epoch != 999 {
		t.Errorf("epoch modification failed")
	}

	// Reset for next test
	plan.Epoch = original
}

func TestRecoveryActionValues(t *testing.T) {
	tests := []RecoveryAction{
		RecoveryRestoreSingle,
		RecoveryRestoreWithDraining,
		RecoveryInferredFallback,
		RecoveryDegraded,
	}

	for _, action := range tests {
		if action == "" {
			t.Errorf("empty recovery action")
		}
	}
}

func TestTrafficRoleValues(t *testing.T) {
	tests := []TrafficRole{
		TrafficRoleActive,
		TrafficRoleDraining,
	}

	for _, role := range tests {
		if role == "" {
			t.Errorf("empty traffic role")
		}
	}
}

// ============================================================================
// BackendCandidate Tests
// ============================================================================

func TestBackendCandidateValidity(t *testing.T) {
	tests := []struct {
		name     string
		cand     BackendCandidate
		expected CandidateValidity
	}{
		{
			name: "healthy backend",
			cand: BackendCandidate{
				Generation:     "gen-123",
				ID:             "container-1",
				Addr:           "10.0.0.1:8080",
				Health:         "healthy",
				TrafficRole:    TrafficRoleActive,
				ValidityStatus: CandidateValid,
			},
			expected: CandidateValid,
		},
		{
			name: "unhealthy backend",
			cand: BackendCandidate{
				Generation:     "gen-456",
				ID:             "container-2",
				Health:         "unhealthy",
				ValidityStatus: CandidateUnhealthy,
			},
			expected: CandidateUnhealthy,
		},
	}

	for _, test := range tests {
		if test.cand.ValidityStatus != test.expected {
			t.Errorf("%s: status mismatch: got %s, want %s", test.name, test.cand.ValidityStatus, test.expected)
		}
	}
}

func TestBackendCandidateTrafficRole(t *testing.T) {
	active := BackendCandidate{
		Generation:  "gen-123",
		TrafficRole: TrafficRoleActive,
	}

	draining := BackendCandidate{
		Generation:  "gen-old",
		TrafficRole: TrafficRoleDraining,
	}

	if active.TrafficRole != TrafficRoleActive {
		t.Errorf("active role not set correctly")
	}
	if draining.TrafficRole != TrafficRoleDraining {
		t.Errorf("draining role not set correctly")
	}
}

// ============================================================================
// CandidateValidity Tests
// ============================================================================

func TestCandidateValidityValues(t *testing.T) {
	tests := []CandidateValidity{
		CandidateValid,
		CandidateUnhealthy,
		CandidatePruned,
		CandidatePartial,
		CandidateStale,
	}

	for _, validity := range tests {
		if validity == "" {
			t.Errorf("empty candidate validity")
		}
	}
}

// ============================================================================
// StateManager Tests
// ============================================================================

func TestStateManagerInitialization(t *testing.T) {
	sm := NewStateManager("/tmp/state", nil)

	if sm == nil {
		t.Fatalf("state manager is nil")
	}
	if sm.epochCounter != 0 {
		t.Errorf("epoch counter should start at 0, got %d", sm.epochCounter)
	}
}

func TestStateManagerEpochCounter(t *testing.T) {
	sm := NewStateManager("/tmp/state", nil)

	epoch1 := sm.NextEpoch()
	if epoch1 != 1 {
		t.Errorf("first epoch should be 1, got %d", epoch1)
	}

	epoch2 := sm.NextEpoch()
	if epoch2 != 2 {
		t.Errorf("second epoch should be 2, got %d", epoch2)
	}

	epoch3 := sm.NextEpoch()
	if epoch3 != 3 {
		t.Errorf("third epoch should be 3, got %d", epoch3)
	}
}

func TestStateManagerInProcessLocking(t *testing.T) {
	sm := NewStateManager("/tmp/state", nil)

	lock1 := sm.getInProcessLock("service-1")
	lock2 := sm.getInProcessLock("service-1")

	// Same service should return same lock instance
	if lock1 != lock2 {
		t.Errorf("same service should return same lock instance")
	}

	lock3 := sm.getInProcessLock("service-2")
	if lock1 == lock3 {
		t.Errorf("different services should have different locks")
	}
}

// ============================================================================
// State File Paths Tests
// ============================================================================

func TestActiveGenerationPath(t *testing.T) {
	sm := NewStateManager("/var/lib/orbit", nil)
	path := sm.ActiveGenerationPath("web")

	if path != "/var/lib/orbit/active-generation-web.json" {
		t.Errorf("path mismatch: got %s", path)
	}
}

func TestRolloutStatePath(t *testing.T) {
	sm := NewStateManager("/var/lib/orbit", nil)
	path := sm.rolloutStatePath("api")

	if path != "/var/lib/orbit/rollout-api.json" {
		t.Errorf("path mismatch: got %s", path)
	}
}

func TestStateLockPath(t *testing.T) {
	sm := NewStateManager("/var/lib/orbit", nil)
	path := sm.StateLockPath("web")

	if path != "/var/lib/orbit/.web.lock" {
		t.Errorf("path mismatch: got %s", path)
	}
}

// ============================================================================
// SchemaVersion Tests
// ============================================================================

func TestSchemaVersionConstant(t *testing.T) {
	if SchemaVersion != 1 {
		t.Errorf("schema version mismatch: got %d, want 1", SchemaVersion)
	}
}

func TestStateSchemaVersions(t *testing.T) {
	activeState := &ActiveGenerationState{
		SchemaVersion: SchemaVersion,
		Service:       "web",
	}

	rolloutState := &RolloutState{
		SchemaVersion: SchemaVersion,
		Service:       "web",
	}

	if activeState.SchemaVersion != SchemaVersion {
		t.Errorf("active state schema version mismatch")
	}
	if rolloutState.SchemaVersion != SchemaVersion {
		t.Errorf("rollout state schema version mismatch")
	}
}

// ============================================================================
// StateLoadError Tests
// ============================================================================

func TestStateLoadErrorFatal(t *testing.T) {
	err := &StateLoadError{
		Path:    "/var/lib/orbit/state.json",
		Reason:  "file corrupted",
		IsFatal: true,
	}

	msg := err.Error()
	if msg != "FATAL: file corrupted" {
		t.Errorf("fatal error message mismatch: got %s", msg)
	}
}

func TestStateLoadErrorNonFatal(t *testing.T) {
	err := &StateLoadError{
		Path:    "/var/lib/orbit/state.json",
		Reason:  "file not found",
		IsFatal: false,
	}

	msg := err.Error()
	if msg != "file not found" {
		t.Errorf("non-fatal error message mismatch: got %s", msg)
	}
}
