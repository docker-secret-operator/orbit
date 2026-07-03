package state

import (
	"testing"
	"time"
)

// ============================================================================
// Generation Selection Tests
// ============================================================================

func TestSelectGenerationFromMetricsExplicitAuthority(t *testing.T) {
	now := time.Now()
	metrics := []GenerationMetrics{
		{
			Generation:             "gen-old",
			HealthyCount:           0,
			TotalCount:             3,
			CreatedAt:              now.Add(-2 * time.Hour),
			ContinuousHealthyStart: now.Add(-10 * time.Minute),
		},
		{
			Generation:             "gen-new",
			HealthyCount:           3,
			TotalCount:             3,
			CreatedAt:              now.Add(-30 * time.Minute),
			ContinuousHealthyStart: now.Add(-20 * time.Minute),
		},
	}

	// Explicit authority should win even if not oldest
	gen, reason := SelectGenerationFromMetrics(metrics, "gen-new", nil)

	if gen != "gen-new" {
		t.Errorf("should select explicit authority, got %s", gen)
	}
	if reason == "" {
		t.Errorf("reason should not be empty")
	}
}

func TestSelectGenerationFromMetricsExplicitAuthorityUnhealthy(t *testing.T) {
	now := time.Now()
	metrics := []GenerationMetrics{
		{
			Generation:   "gen-auth",
			HealthyCount: 0, // Unhealthy
			TotalCount:   3,
			CreatedAt:    now.Add(-2 * time.Hour),
		},
		{
			Generation:             "gen-healthy",
			HealthyCount:           5,
			TotalCount:             5,
			CreatedAt:              now.Add(-1 * time.Hour),
			ContinuousHealthyStart: now.Add(-50 * time.Minute),
		},
	}

	// Authority is unhealthy, should fallback to longest uptime
	gen, reason := SelectGenerationFromMetrics(metrics, "gen-auth", nil)

	if gen != "gen-healthy" {
		t.Errorf("should fallback to healthy gen, got %s", gen)
	}
	if reason == "" {
		t.Errorf("reason should not be empty")
	}
}

func TestSelectGenerationByLongestUptimeFullyHealthy(t *testing.T) {
	now := time.Now()
	metrics := []GenerationMetrics{
		{
			Generation:             "gen-recent",
			HealthyCount:           3,
			TotalCount:             3,
			CreatedAt:              now.Add(-30 * time.Minute),
			ContinuousHealthyStart: now.Add(-15 * time.Minute), // Only 15min healthy
		},
		{
			Generation:             "gen-stable",
			HealthyCount:           3,
			TotalCount:             3,
			CreatedAt:              now.Add(-2 * time.Hour),
			ContinuousHealthyStart: now.Add(-100 * time.Minute), // 100min healthy
		},
	}

	gen, reason := SelectGenerationFromMetrics(metrics, "", nil)

	if gen != "gen-stable" {
		t.Errorf("should select longest uptime, got %s", gen)
	}
	if reason == "" {
		t.Errorf("reason should not be empty")
	}
}

func TestSelectGenerationFallbackToOldest(t *testing.T) {
	now := time.Now()
	metrics := []GenerationMetrics{
		{
			Generation:   "gen-old",
			HealthyCount: 1,
			TotalCount:   3,
			CreatedAt:    now.Add(-2 * time.Hour),
		},
		{
			Generation:   "gen-new",
			HealthyCount: 1,
			TotalCount:   3,
			CreatedAt:    now.Add(-1 * time.Hour),
		},
	}

	// Neither is fully healthy, should select oldest
	gen, reason := SelectGenerationFromMetrics(metrics, "", nil)

	if gen != "gen-old" {
		t.Errorf("should select oldest, got %s", gen)
	}
	if reason == "" {
		t.Errorf("reason should not be empty")
	}
}

func TestSelectGenerationNoHealthy(t *testing.T) {
	metrics := []GenerationMetrics{
		{
			Generation:   "gen-1",
			HealthyCount: 0,
			TotalCount:   3,
		},
		{
			Generation:   "gen-2",
			HealthyCount: 0,
			TotalCount:   2,
		},
	}

	gen, reason := SelectGenerationFromMetrics(metrics, "", nil)

	if gen != "" {
		t.Errorf("should return empty for no healthy, got %s", gen)
	}
	if reason != "no healthy generations found" {
		t.Errorf("reason mismatch: got %s", reason)
	}
}

func TestSelectByLongestHealthyUptimeEmpty(t *testing.T) {
	metrics := []GenerationMetrics{}
	result := selectByLongestHealthyUptime(metrics)

	if result != nil {
		t.Errorf("should return nil for empty list")
	}
}

func TestSelectByOldestHealthyEmpty(t *testing.T) {
	metrics := []GenerationMetrics{}
	result := selectByOldestHealthy(metrics)

	if result != nil {
		t.Errorf("should return nil for empty list")
	}
}

func TestSelectByOldestHealthyMultiple(t *testing.T) {
	now := time.Now()
	metrics := []GenerationMetrics{
		{
			Generation:   "gen-newest",
			HealthyCount: 1,
			CreatedAt:    now,
		},
		{
			Generation:   "gen-oldest",
			HealthyCount: 1,
			CreatedAt:    now.Add(-3 * time.Hour),
		},
		{
			Generation:   "gen-middle",
			HealthyCount: 1,
			CreatedAt:    now.Add(-1 * time.Hour),
		},
	}

	result := selectByOldestHealthy(metrics)

	if result == nil {
		t.Fatalf("result should not be nil")
	}
	if result.Generation != "gen-oldest" {
		t.Errorf("should select oldest, got %s", result.Generation)
	}
}

// ============================================================================
// Rollback Candidate Validation Tests
// ============================================================================

func TestValidateRollbackCandidateEmpty(t *testing.T) {
	validity := ValidateRollbackCandidate("", &GenerationInventory{})

	if validity != CandidateValid {
		t.Errorf("empty candidate should be valid, got %s", validity)
	}
}

func TestValidateRollbackCandidateMissing(t *testing.T) {
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{},
	}

	validity := ValidateRollbackCandidate("gen-gone", inventory)

	if validity != CandidatePruned {
		t.Errorf("missing candidate should be pruned, got %s", validity)
	}
}

func TestValidateRollbackCandidateUnhealthy(t *testing.T) {
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-broken": {
				Generation:   "gen-broken",
				HealthyCount: 0,
				TotalCount:   3,
			},
		},
	}

	validity := ValidateRollbackCandidate("gen-broken", inventory)

	if validity != CandidateUnhealthy {
		t.Errorf("unhealthy candidate should be invalid, got %s", validity)
	}
}

func TestValidateRollbackCandidateStale(t *testing.T) {
	now := time.Now()
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-old": {
				Generation:   "gen-old",
				HealthyCount: 1,
				TotalCount:   1,
				CreatedAt:    now.Add(-48 * time.Hour), // Too old
			},
		},
	}

	validity := ValidateRollbackCandidate("gen-old", inventory)

	if validity != CandidateStale {
		t.Errorf("stale candidate should be invalid, got %s", validity)
	}
}

func TestValidateRollbackCandidatePartial(t *testing.T) {
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-partial": {
				Generation:   "gen-partial",
				HealthyCount: 1,
				TotalCount:   1, // Only 1 container (below minimum)
				CreatedAt:    time.Now(),
			},
		},
	}

	// Note: with expectedCount=1, this should be valid
	validity := ValidateRollbackCandidate("gen-partial", inventory)

	if validity != CandidateValid {
		t.Errorf("generation with 1 container should be valid, got %s", validity)
	}
}

func TestValidateRollbackCandidateValid(t *testing.T) {
	now := time.Now()
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-good": {
				Generation:   "gen-good",
				HealthyCount: 3,
				TotalCount:   3,
				CreatedAt:    now.Add(-12 * time.Hour), // Recent
			},
		},
	}

	validity := ValidateRollbackCandidate("gen-good", inventory)

	if validity != CandidateValid {
		t.Errorf("valid candidate rejected, got %s", validity)
	}
}

// ============================================================================
// Stale Transition Detection Tests
// ============================================================================

func TestIsTransitionStaleNotTransitioning(t *testing.T) {
	rollout := &RolloutState{
		Authority: AuthorityOld,
	}

	isStale, reason := IsTransitionStale(rollout, 5*time.Minute)

	if isStale {
		t.Errorf("non-transitioning state should not be stale")
	}
	if reason != "" {
		t.Errorf("reason should be empty for non-transitioning")
	}
}

func TestIsTransitionStaleTooLong(t *testing.T) {
	now := time.Now()
	rollout := &RolloutState{
		Authority:          AuthorityTransitioning,
		TransitionStart:    now.Add(-10 * time.Minute), // 10 minutes ago
		TransitionDeadline: now.Add(5 * time.Minute),   // 5 minutes from now (already exceeded)
	}

	isStale, reason := IsTransitionStale(rollout, 5*time.Minute)

	if !isStale {
		t.Errorf("transition > 5 min should be stale")
	}
	if reason == "" {
		t.Errorf("reason should not be empty")
	}
}

func TestIsTransitionStaleDeadlineExceeded(t *testing.T) {
	now := time.Now()
	rollout := &RolloutState{
		Authority:          AuthorityTransitioning,
		TransitionStart:    now.Add(-2 * time.Minute),
		TransitionDeadline: now.Add(-1 * time.Second), // Deadline in the past
	}

	isStale, reason := IsTransitionStale(rollout, 5*time.Minute)

	if !isStale {
		t.Errorf("exceeded deadline should be stale")
	}
	if reason == "" {
		t.Errorf("reason should not be empty")
	}
}

func TestIsTransitionStaleWithinBounds(t *testing.T) {
	now := time.Now()
	rollout := &RolloutState{
		Authority:          AuthorityTransitioning,
		TransitionStart:    now.Add(-2 * time.Minute),
		TransitionDeadline: now.Add(10 * time.Minute), // Far future
	}

	isStale, reason := IsTransitionStale(rollout, 5*time.Minute)

	if isStale {
		t.Errorf("recent transition should not be stale")
	}
	if reason != "" {
		t.Errorf("reason should be empty for non-stale")
	}
}

func TestIsTransitionStaleWithProgress(t *testing.T) {
	now := time.Now()
	rollout := &RolloutState{
		Authority:          AuthorityTransitioning,
		Phase:              RolloutDraining,
		DrainStartedAt:     now.Add(-10 * time.Minute), // Drain for 10 min (> 5 min timeout)
		LastProgressAt:     now.Add(-30 * time.Second), // But progress within last 30s
		TransitionDeadline: now.Add(30 * time.Minute),
	}

	isStale, _ := IsTransitionStale(rollout, 5*time.Minute)

	if isStale {
		t.Errorf("slow-but-healthy drain with recent progress should not be stale")
	}
}

func TestIsTransitionStaleNoProgress(t *testing.T) {
	now := time.Now()
	rollout := &RolloutState{
		Authority:          AuthorityTransitioning,
		Phase:              RolloutDraining,
		DrainStartedAt:     now.Add(-10 * time.Minute), // Drain for 10 min
		LastProgressAt:     now.Add(-8 * time.Minute),  // No progress for 8 min (> 5 min timeout)
		TransitionDeadline: now.Add(30 * time.Minute),
	}

	isStale, reason := IsTransitionStale(rollout, 5*time.Minute)

	if !isStale {
		t.Errorf("drain with no progress should be stale")
	}
	if reason == "" {
		t.Errorf("reason should explain why stale")
	}
}

// ============================================================================
// Determine Authority Tests
// ============================================================================

func TestDetermineAuthorityFromRolloutOld(t *testing.T) {
	rollout := &RolloutState{
		OldGeneration: "gen-old",
		NewGeneration: "gen-new",
		Authority:     AuthorityOld,
	}

	gen, reason := determineAuthority(rollout, nil)

	if gen != "gen-old" {
		t.Errorf("should return old generation, got %s", gen)
	}
	if reason == "" {
		t.Errorf("reason should not be empty")
	}
}

func TestDetermineAuthorityFromRolloutNew(t *testing.T) {
	rollout := &RolloutState{
		OldGeneration: "gen-old",
		NewGeneration: "gen-new",
		Authority:     AuthorityNew,
	}

	gen, reason := determineAuthority(rollout, nil)

	if gen != "gen-new" {
		t.Errorf("should return new generation, got %s", gen)
	}
	if reason == "" {
		t.Errorf("reason should not be empty")
	}
}

func TestDetermineAuthorityFromRolloutTransitioning(t *testing.T) {
	rollout := &RolloutState{
		OldGeneration: "gen-old",
		NewGeneration: "gen-new",
		Authority:     AuthorityTransitioning,
	}

	gen, reason := determineAuthority(rollout, nil)

	if gen != "gen-new" {
		t.Errorf("should return new generation during transition, got %s", gen)
	}
	if reason == "" {
		t.Errorf("reason should not be empty")
	}
}

func TestDetermineAuthorityFromActiveGen(t *testing.T) {
	activeGen := &ActiveGenerationState{
		ActiveGeneration: "gen-active",
	}

	gen, reason := determineAuthority(nil, activeGen)

	if gen != "gen-active" {
		t.Errorf("should return active generation, got %s", gen)
	}
	if reason == "" {
		t.Errorf("reason should not be empty")
	}
}

func TestDetermineAuthorityRolloutPriority(t *testing.T) {
	rollout := &RolloutState{
		OldGeneration: "gen-old",
		NewGeneration: "gen-new",
		Authority:     AuthorityOld,
	}

	activeGen := &ActiveGenerationState{
		ActiveGeneration: "gen-active",
	}

	gen, reason := determineAuthority(rollout, activeGen)

	if gen != "gen-old" {
		t.Errorf("rollout should take priority, got %s", gen)
	}
	if reason == "" {
		t.Errorf("reason should not be empty")
	}
}

// ============================================================================
// Identify Orphan Generations Tests
// ============================================================================

func TestIdentifyOrphanGenerationsNone(t *testing.T) {
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-auth": {},
		},
	}

	plan := &RecoveryPlan{}
	identifyOrphanGenerations(inventory, "gen-auth", nil, plan)

	if len(plan.OrphanedGenerationsFound) != 0 {
		t.Errorf("should have no orphans, got %d", len(plan.OrphanedGenerationsFound))
	}
}

func TestIdentifyOrphanGenerationsNoAuthority(t *testing.T) {
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-1": {},
			"gen-2": {},
		},
	}

	plan := &RecoveryPlan{}
	identifyOrphanGenerations(inventory, "", nil, plan)

	// All are orphans if no authority
	if len(plan.OrphanedGenerationsFound) != 2 {
		t.Errorf("should have 2 orphans, got %d", len(plan.OrphanedGenerationsFound))
	}
}

func TestIdentifyOrphanGenerationsWithRollout(t *testing.T) {
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-auth":  {},
			"gen-old":   {},
			"gen-new":   {},
			"gen-stale": {},
		},
	}

	rollout := &RolloutState{
		OldGeneration: "gen-old",
		NewGeneration: "gen-new",
	}

	plan := &RecoveryPlan{}
	identifyOrphanGenerations(inventory, "gen-auth", rollout, plan)

	// Only gen-stale should be orphaned
	if len(plan.OrphanedGenerationsFound) != 1 {
		t.Errorf("should have 1 orphan, got %d", len(plan.OrphanedGenerationsFound))
	}
	if plan.OrphanedGenerationsFound[0] != "gen-stale" {
		t.Errorf("wrong orphan: got %s", plan.OrphanedGenerationsFound[0])
	}
}

// ============================================================================
// Determine Recovery Action Tests
// ============================================================================

func TestDetermineRecoveryActionNoAuthority(t *testing.T) {
	now := time.Now()
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-healthy": {
				Generation:             "gen-healthy",
				HealthyCount:           3,
				TotalCount:             3,
				CreatedAt:              now.Add(-1 * time.Hour),
				ContinuousHealthyStart: now.Add(-50 * time.Minute),
			},
		},
	}

	plan := &RecoveryPlan{}
	determineRecoveryAction(nil, "", inventory, plan)

	if plan.Action != RecoveryInferredFallback {
		t.Errorf("should be inferred fallback, got %s", plan.Action)
	}
	if plan.AuthoritativeGeneration != "gen-healthy" {
		t.Errorf("should infer authority, got %s", plan.AuthoritativeGeneration)
	}
}

func TestDetermineRecoveryActionAuthorityHealthy(t *testing.T) {
	now := time.Now()
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-auth": {
				Generation:   "gen-auth",
				HealthyCount: 3,
				TotalCount:   3,
				CreatedAt:    now.Add(-1 * time.Hour),
			},
		},
	}

	plan := &RecoveryPlan{}
	determineRecoveryAction(nil, "gen-auth", inventory, plan)

	if plan.Action != RecoveryRestoreSingle {
		t.Errorf("should be restore single, got %s", plan.Action)
	}
}

func TestDetermineRecoveryActionAuthorityUnhealthy(t *testing.T) {
	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-auth": {
				Generation:   "gen-auth",
				HealthyCount: 0,
				TotalCount:   3,
			},
		},
	}

	plan := &RecoveryPlan{}
	determineRecoveryAction(nil, "gen-auth", inventory, plan)

	if plan.Action != RecoveryDegraded {
		t.Errorf("should be degraded, got %s", plan.Action)
	}
}

func TestDetermineRecoveryActionInterruptedRollout(t *testing.T) {
	rollout := &RolloutState{
		OldGeneration: "gen-old",
		NewGeneration: "gen-new",
		Authority:     AuthorityTransitioning,
		Phase:         RolloutDraining,
	}

	inventory := &GenerationInventory{
		GenerationStates: map[string]GenerationMetrics{
			"gen-new": {
				Generation:   "gen-new",
				HealthyCount: 3,
				TotalCount:   3,
			},
		},
	}

	plan := &RecoveryPlan{}
	determineRecoveryAction(rollout, "gen-new", inventory, plan)

	if plan.Action != RecoveryRestoreWithDraining {
		t.Errorf("should be restore with draining, got %s", plan.Action)
	}
}

// ============================================================================
// Generation Inventory Tests
// ============================================================================

func TestGenerationInventoryHealthCounts(t *testing.T) {
	inventory := &GenerationInventory{
		Service:             "web",
		ActiveGeneration:    "gen-123",
		GenerationStates:    map[string]GenerationMetrics{},
		HealthyGenerations:  []string{"gen-123"},
		ContainerCount:      5,
		HealthyBackendCount: 5,
	}

	if inventory.Service != "web" {
		t.Errorf("service mismatch")
	}
	if inventory.ContainerCount != 5 {
		t.Errorf("container count mismatch")
	}
}

// ============================================================================
// Convert To Metrics Tests
// ============================================================================

func TestConvertToMetrics(t *testing.T) {
	now := time.Now()
	states := map[string]GenerationMetrics{
		"gen-1": {
			Generation:   "gen-1",
			HealthyCount: 1,
			CreatedAt:    now.Add(-2 * time.Hour),
		},
		"gen-2": {
			Generation:   "gen-2",
			HealthyCount: 2,
			CreatedAt:    now.Add(-1 * time.Hour),
		},
	}

	metrics := convertToMetrics(states)

	if len(metrics) != 2 {
		t.Errorf("should convert 2 states, got %d", len(metrics))
	}
}

func TestConvertToMetricsEmpty(t *testing.T) {
	states := map[string]GenerationMetrics{}
	metrics := convertToMetrics(states)

	if len(metrics) != 0 {
		t.Errorf("should have 0 metrics, got %d", len(metrics))
	}
}
