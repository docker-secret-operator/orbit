package chaos

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/state"
)

// FailureScenario defines a chaos test scenario
type FailureScenario struct {
	Name     string
	Tier     int // 1, 2, or 3
	Duration time.Duration
	Run      func(ctx context.Context, harness *ChaosHarness) error
}

// FailureResult captures outcome of a single scenario
type FailureResult struct {
	Scenario           string
	Tier               int
	Success            bool
	Error              string
	Duration           time.Duration
	MetricsBeforeStr   string
	MetricsAfterStr    string
	DecisionTraceCount int
	InvariantsViolated []string
}

// ChaosHarness manages chaos test execution
type ChaosHarness struct {
	t        *testing.T
	stateDir string
	sm       *state.StateManager
	mc       *metrics.MetricsCollector
	ctx      context.Context
	cancel   context.CancelFunc

	resultsMu sync.Mutex
	results   []FailureResult

	cleanupBlockedCount int
	orphanGenCount      int
}

// NewChaosHarness creates a chaos test harness
func NewChaosHarness(t *testing.T) *ChaosHarness {
	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	mc := metrics.NewMetricsCollector()

	ctx, cancel := context.WithCancel(context.Background())

	return &ChaosHarness{
		t:        t,
		stateDir: tmpDir,
		sm:       sm,
		mc:       mc,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Close cleans up harness resources
func (h *ChaosHarness) Close() error {
	h.cancel()
	return nil
}

// RunScenario executes a single failure scenario and records metrics
func (h *ChaosHarness) RunScenario(scenario *FailureScenario) *FailureResult {
	result := &FailureResult{
		Scenario: scenario.Name,
		Tier:     scenario.Tier,
	}

	// Snapshot metrics before failure
	beforeSnapshot := h.mc.GetSnapshot()
	result.MetricsBeforeStr = fmt.Sprintf("recoveries=%d failures=%d transitions=%d",
		beforeSnapshot.RecoveryCount,
		beforeSnapshot.RecoveryFailureCount,
		beforeSnapshot.AuthorityTransitions)

	// Create isolated context for each scenario (not tied to harness context)
	// This prevents one scenario's cancellation from affecting others
	ctx, cancel := context.WithTimeout(context.Background(), scenario.Duration*2)
	defer cancel()

	start := time.Now()
	err := scenario.Run(ctx, h)
	result.Duration = time.Since(start)

	if err != nil {
		result.Success = false
		result.Error = err.Error()
	} else {
		result.Success = true
	}

	// Snapshot metrics after failure
	afterSnapshot := h.mc.GetSnapshot()
	result.MetricsAfterStr = fmt.Sprintf("recoveries=%d failures=%d transitions=%d",
		afterSnapshot.RecoveryCount,
		afterSnapshot.RecoveryFailureCount,
		afterSnapshot.AuthorityTransitions)

	// Verify invariants post-failure
	result.InvariantsViolated = h.validateInvariants()

	h.recordResult(result)
	return result
}

// validateInvariants checks system invariants after failure by loading
// whatever active-generation/rollout state scenarios actually persisted to
// disk and running them through state.InvariantValidator — the same
// validator recovery uses in production. Every chaos scenario runs against
// the fixed service name "web" (see scenarios.go). GenerationInventory is
// omitted (nil) since these scenarios don't have a real Docker daemon to
// discover live containers from, so orphan-generation checks are skipped;
// revision monotonicity, authority uniqueness, and rollout consistency are
// still checked against whatever was actually written to state files.
func (h *ChaosHarness) validateInvariants() []string {
	const service = "web"

	activeGen, err := h.sm.LoadActiveGenerationState(service)
	if err != nil {
		return []string{fmt.Sprintf("active generation state unreadable: %v", err)}
	}

	rollout, err := h.sm.LoadRolloutState(service)
	if err != nil {
		return []string{fmt.Sprintf("rollout state unreadable: %v", err)}
	}

	validator := state.NewInvariantValidator(nil, activeGen, rollout)
	if validator.ValidateAll() != nil {
		return validator.Violations()
	}

	return nil
}

// recordResult safely records a scenario result
func (h *ChaosHarness) recordResult(result *FailureResult) {
	h.resultsMu.Lock()
	defer h.resultsMu.Unlock()
	h.results = append(h.results, *result)
}

// Results returns all scenario results
func (h *ChaosHarness) Results() []FailureResult {
	h.resultsMu.Lock()
	defer h.resultsMu.Unlock()

	results := make([]FailureResult, len(h.results))
	copy(results, h.results)
	return results
}

// Summary returns test summary statistics
func (h *ChaosHarness) Summary() (passed int, failed int, totalTime time.Duration) {
	results := h.Results()
	for _, r := range results {
		if r.Success {
			passed++
		} else {
			failed++
		}
		totalTime += r.Duration
	}
	return
}
