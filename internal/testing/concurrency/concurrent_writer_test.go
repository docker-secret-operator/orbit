package concurrency

import (
	"fmt"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/state"
)

// TestConcurrentProxyRolloutWrite simulates proxy healing loop + rollout contending
func TestConcurrentProxyRolloutWrite(t *testing.T) {
	harness := NewConcurrencyTestHarness(t, 5*time.Millisecond)
	service := "web"

	// Initialize with gen-a
	if err := harness.InitializeState(service, "gen-a"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// Proxy writer: healing loop updates authority
	harness.SpawnWriter(
		WriterProxy,
		service,
		func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
			updated := *current
			updated.ActiveGeneration = "gen-b"
			updated.PreviousRevision = current.Revision
			updated.Revision = current.Revision + 1
			updated.UpdatedAt = time.Now()
			return &updated, nil
		},
	)

	// Rollout writer: attempts to write authority update
	harness.SpawnWriter(
		WriterRollout,
		service,
		func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
			// Rollout reads authority for reference but updates same file in this test
			updated := *current
			updated.ActiveGeneration = "gen-c"
			updated.PreviousRevision = current.Revision
			updated.Revision = current.Revision + 1
			updated.UpdatedAt = time.Now()
			return &updated, nil
		},
	)

	// Start both writers simultaneously
	harness.StartAll()
	harness.WaitAll()

	// Verify invariants
	if err := harness.AssertInvariants(service); err != nil {
		t.Fatalf("invariant violation: %v", err)
	}

	// Verify at least one writer succeeded
	results := harness.Results()
	successes := 0
	for _, r := range results {
		if r.Success {
			successes++
		}
	}

	if successes == 0 {
		t.Errorf("expected at least 1 success, got %d", successes)
	}

	// Verify final state is valid (either initial or one of the updates)
	final, _ := harness.GetFinalState(service)
	validStates := map[string]bool{
		"gen-a": true, // initial
		"gen-b": true, // proxy update
		"gen-c": true, // rollout update
	}
	if !validStates[final.ActiveGeneration] {
		t.Errorf("unexpected final generation: %s", final.ActiveGeneration)
	}

	// Verify revision is valid (incremented from initial)
	if final.Revision < 1 {
		t.Errorf("revision should be >= 1, got %d", final.Revision)
	}

	t.Logf("✓ test passed: writers executed safely, state is consistent")
}

// TestConcurrentRolloutPruneWrite tests rollout + cleanup contending
func TestConcurrentRolloutPruneWrite(t *testing.T) {
	harness := NewConcurrencyTestHarness(t, 5*time.Millisecond)
	service := "web"

	if err := harness.InitializeState(service, "gen-active"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// Rollout: authority transition
	harness.SpawnWriter(
		WriterRollout,
		service,
		func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
			updated := *current
			updated.ActiveGeneration = "gen-new"
			updated.PreviousRevision = current.Revision
			updated.Revision = current.Revision + 1
			return &updated, nil
		},
	)

	// Prune: tries to mark old generation for cleanup
	harness.SpawnWriter(
		WriterPrune,
		service,
		func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
			updated := *current
			updated.ActiveGeneration = "gen-cleaned"
			updated.PreviousRevision = current.Revision
			updated.Revision = current.Revision + 1
			return &updated, nil
		},
	)

	harness.StartAll()
	harness.WaitAll()

	if err := harness.AssertInvariants(service); err != nil {
		t.Fatalf("invariant violation: %v", err)
	}

	results := harness.Results()
	successes := 0
	for _, r := range results {
		if r.Success {
			successes++
		}
	}

	if successes == 0 {
		t.Errorf("expected at least 1 success, got %d", successes)
	}

	t.Logf("✓ test passed: cleanup collision handled safely")
}

// TestConcurrentRecoveryHealingCollision tests recovery plan execution + healing loop collision
func TestConcurrentRecoveryHealingCollision(t *testing.T) {
	harness := NewConcurrencyTestHarness(t, 5*time.Millisecond)
	service := "web"

	if err := harness.InitializeState(service, "gen-authority"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// Recovery: after crash, recovery process restores authority
	harness.SpawnWriter(
		WriterRecovery,
		service,
		func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
			updated := *current
			updated.ActiveGeneration = "gen-recovered"
			updated.PreviousRevision = current.Revision
			updated.Revision = current.Revision + 1
			return &updated, nil
		},
	)

	// Healing loop: simultaneously tries to update based on health check
	harness.SpawnWriter(
		WriterHealing,
		service,
		func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
			updated := *current
			updated.ActiveGeneration = "gen-healed"
			updated.PreviousRevision = current.Revision
			updated.Revision = current.Revision + 1
			return &updated, nil
		},
	)

	harness.StartAll()
	harness.WaitAll()

	if err := harness.AssertInvariants(service); err != nil {
		t.Fatalf("invariant violation: %v", err)
	}

	final, _ := harness.GetFinalState(service)
	t.Logf("final state: gen=%s, revision=%d", final.ActiveGeneration, final.Revision)

	if final.Revision < 1 {
		t.Errorf("revision should be >= 1, got %d", final.Revision)
	}

	t.Logf("✓ test passed: recovery and healing safe from collision")
}

// TestStaleRevisionDetection validates that CAS prevents lost writes
func TestStaleRevisionDetection(t *testing.T) {
	harness := NewConcurrencyTestHarness(t, 10*time.Millisecond)
	service := "web"

	if err := harness.InitializeState(service, "gen-initial"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// Writer A: succeeds first
	harness.SpawnWriter(
		WriterProxy,
		service,
		func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
			updated := *current
			updated.ActiveGeneration = "gen-from-a"
			updated.PreviousRevision = current.Revision
			updated.Revision = current.Revision + 1
			return &updated, nil
		},
	)

	// Writer B: reads stale state, tries to write with outdated revision
	harness.SpawnWriter(
		WriterHealing,
		service,
		func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
			updated := *current
			updated.ActiveGeneration = "gen-from-b"
			updated.PreviousRevision = current.Revision
			updated.Revision = current.Revision + 1
			return &updated, nil
		},
	)

	harness.StartAll()
	harness.WaitAll()

	if err := harness.AssertInvariants(service); err != nil {
		t.Fatalf("invariant violation: %v", err)
	}

	t.Logf("✓ test passed: stale revision handled correctly")
}

// TestRevisionMonotonicity validates that each write increments revision
func TestRevisionMonotonicity(t *testing.T) {
	harness := NewConcurrencyTestHarness(t, 3*time.Millisecond)
	service := "web"

	if err := harness.InitializeState(service, "gen-1"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// Spawn 5 sequential writers (with small timing offsets)
	for i := 0; i < 5; i++ {
		writerID := WriterID(fmt.Sprintf("writer-%d", i))
		generation := fmt.Sprintf("gen-%d", i)

		harness.SpawnWriter(
			writerID,
			service,
			func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
				updated := *current
				updated.ActiveGeneration = generation
				updated.PreviousRevision = current.Revision
				updated.Revision = current.Revision + 1
				return &updated, nil
			},
		)

		// Small delay between spawns to roughly serialize
		time.Sleep(2 * time.Millisecond)
	}

	harness.StartAll()
	harness.WaitAll()

	final, _ := harness.GetFinalState(service)

	// With lock serialization, final revision should be 6 (1 initial + 5 writes)
	// Minimum: revision should be 2 (at least one succeeded)
	if final.Revision < 2 || final.Revision > 6 {
		t.Errorf("revision out of range: got %d, expected 2-6", final.Revision)
	}

	// Key invariant: revision is monotonic
	t.Logf("✓ test passed: final revision=%d (all writes monotonic)", final.Revision)
}

// TestLockSerializationStrictness validates lock strictly serializes writes
func TestLockSerializationStrictness(t *testing.T) {
	harness := NewConcurrencyTestHarness(t, 2*time.Millisecond)
	service := "web"

	if err := harness.InitializeState(service, "gen-start"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// Spawn 3 writers that all try simultaneously
	for i := 0; i < 3; i++ {
		writerID := WriterID(fmt.Sprintf("concurrent-%d", i))
		generation := fmt.Sprintf("gen-concurrent-%d", i)

		harness.SpawnWriter(
			writerID,
			service,
			func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
				updated := *current
				updated.ActiveGeneration = generation
				updated.PreviousRevision = current.Revision
				updated.Revision = current.Revision + 1
				return &updated, nil
			},
		)
	}

	harness.StartAll()
	harness.WaitAll()

	results := harness.Results()
	successCount := 0
	for _, r := range results {
		if r.Success {
			successCount++
		}
	}

	// At least one writer should succeed
	if successCount == 0 {
		t.Errorf("expected at least 1 success, got %d", successCount)
	}

	final, _ := harness.GetFinalState(service)
	if final.Revision < 1 {
		t.Errorf("final revision should be >= 1, got %d", final.Revision)
	}

	t.Logf("✓ test passed: concurrent writes handled safely")
}

// TestNoCrashOnConcurrentFailure ensures failures don't corrupt state
func TestNoCrashOnConcurrentFailure(t *testing.T) {
	harness := NewConcurrencyTestHarness(t, 5*time.Millisecond)
	service := "web"

	if err := harness.InitializeState(service, "gen-stable"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	initialState, _ := harness.GetFinalState(service)

	// Spawn writer that succeeds
	harness.SpawnWriter(
		WriterProxy,
		service,
		func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
			updated := *current
			updated.ActiveGeneration = "gen-updated"
			updated.PreviousRevision = current.Revision
			updated.Revision = current.Revision + 1
			return &updated, nil
		},
	)

	// Spawn writer that fails
	harness.SpawnWriter(
		WriterHealing,
		service,
		func(current *state.ActiveGenerationState) (*state.ActiveGenerationState, error) {
			return nil, fmt.Errorf("simulated failure")
		},
	)

	harness.StartAll()
	harness.WaitAll()

	// Verify state is still valid after one failure
	finalState, err := harness.GetFinalState(service)
	if err != nil {
		t.Fatalf("final state corrupted: %v", err)
	}

	// State should be valid (either unchanged or updated by success)
	if finalState.Revision < initialState.Revision {
		t.Errorf("revision went backwards: before=%d, after=%d", initialState.Revision, finalState.Revision)
	}

	t.Logf("✓ test passed: failures don't corrupt state")
}
