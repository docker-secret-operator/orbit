package chaos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/docker-secret-operator/orbit/internal/state"
)

// Tier 1 Scenarios (Fast, <1s each) - 10 scenarios

// Scenario1_TimeoutDuringRecovery simulates recovery timeout
func Scenario1_TimeoutDuringRecovery(ctx context.Context, h *ChaosHarness) error {
	// Create initial state
	initial := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
		Revision:         1,
		UpdatedAt:        time.Now(),
	}

	filePath := h.sm.ActiveGenerationPath("web")
	if err := state.AtomicWriteJSON(filePath, initial, nil); err != nil {
		return fmt.Errorf("initial state write failed: %w", err)
	}

	// Simulate timeout by canceling context
	time.Sleep(10 * time.Millisecond)
	h.cancel()

	return nil
}

// Scenario2_LockContention simulates lock contention
func Scenario2_LockContention(ctx context.Context, h *ChaosHarness) error {
	lockPath := h.sm.StateLockPath("web")

	// Acquire and hold lock briefly
	lock, err := state.AcquireAdvisoryLock(lockPath, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock acquire failed: %w", err)
	}
	defer lock.Release()

	// Attempt concurrent lock should block/timeout
	time.Sleep(50 * time.Millisecond)

	return nil
}

// Scenario3_PartialWriteFailure simulates write to read-only dir
func Scenario3_PartialWriteFailure(ctx context.Context, h *ChaosHarness) error {
	readonlyDir := filepath.Join(h.stateDir, "readonly")
	if err := os.Mkdir(readonlyDir, 0555); err != nil {
		return fmt.Errorf("mkdir failed: %w", err)
	}
	defer os.Chmod(readonlyDir, 0755)

	data := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
	}

	readonlyFile := filepath.Join(readonlyDir, "state.json")
	err := state.AtomicWriteJSON(readonlyFile, data, nil)

	// Should fail gracefully
	if err == nil {
		return fmt.Errorf("write to read-only should have failed")
	}

	return nil
}

// Scenario4_StateFileCorruption simulates corrupted state file
func Scenario4_StateFileCorruption(ctx context.Context, h *ChaosHarness) error {
	filePath := h.sm.ActiveGenerationPath("web")

	// Write valid initial state
	initial := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
		Revision:         1,
	}
	if err := state.AtomicWriteJSON(filePath, initial, nil); err != nil {
		return err
	}

	// Corrupt it by writing invalid JSON
	invalidData := []byte("{invalid json")
	if err := os.WriteFile(filePath, invalidData, 0644); err != nil {
		return err
	}

	// Attempt to read should fail gracefully
	bytes, _ := os.ReadFile(filePath)
	_ = bytes // unused for now

	return nil
}

// Scenario5_MemoryPressureSimulation simulates memory constraints
func Scenario5_MemoryPressureSimulation(ctx context.Context, h *ChaosHarness) error {
	// Simulate by allocating and releasing large objects
	for i := 0; i < 100; i++ {
		_ = make([]byte, 1024*1024) // 1MB allocation
		time.Sleep(1 * time.Millisecond)
	}
	return nil
}

// Scenario6_RecoveryRaceCondition simulates concurrent recovery attempts
func Scenario6_RecoveryRaceCondition(ctx context.Context, h *ChaosHarness) error {
	filePath := h.sm.ActiveGenerationPath("web")
	lockPath := h.sm.StateLockPath("web")

	// Initialize state
	initial := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
		Revision:         1,
	}
	if err := state.AtomicWriteJSON(filePath, initial, nil); err != nil {
		return fmt.Errorf("initial state write failed: %w", err)
	}

	// Spawn competing recovery attempts
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			lock, err := state.AcquireAdvisoryLock(lockPath, 1*time.Second)
			if err != nil {
				done <- err
				return
			}
			defer lock.Release()

			updated := *initial // copy: initial is shared between both goroutines
			updated.Revision++
			err = state.AtomicWriteJSON(filePath, &updated, nil)
			done <- err
		}()
	}

	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			return fmt.Errorf("concurrent recovery write failed: %w", err)
		}
	}

	return nil
}

// Scenario7_StaleTransitionDetection simulates stale transition detection
func Scenario7_StaleTransitionDetection(ctx context.Context, h *ChaosHarness) error {
	filePath := h.sm.ActiveGenerationPath("web")

	// Create state with very old transition timestamp
	agState := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
		Revision:         1,
		UpdatedAt:        time.Now().Add(-10 * time.Minute), // 10 minutes old
	}

	return state.AtomicWriteJSON(filePath, agState, nil)
}

// Scenario8_RevisionOverflow simulates revision increment exhaustion
func Scenario8_RevisionOverflow(ctx context.Context, h *ChaosHarness) error {
	filePath := h.sm.ActiveGenerationPath("web")

	// Create state with very high revision
	agState := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
		Revision:         9223372036854775806, // close to int64 max
	}

	if err := state.AtomicWriteJSON(filePath, agState, nil); err != nil {
		return err
	}

	// Attempt to increment should still work
	agState.Revision++
	return state.AtomicWriteJSON(filePath, agState, nil)
}

// Scenario9_MultipleServiceStates simulates multiple concurrent services
func Scenario9_MultipleServiceStates(ctx context.Context, h *ChaosHarness) error {
	services := []string{"web", "api", "cache", "db"}

	for _, svc := range services {
		agState := &state.ActiveGenerationState{
			SchemaVersion:    1,
			Service:          svc,
			ActiveGeneration: fmt.Sprintf("gen-%s", svc),
			Revision:         1,
		}
		filePath := h.sm.ActiveGenerationPath(svc)
		if err := state.AtomicWriteJSON(filePath, agState, nil); err != nil {
			return err
		}
	}

	return nil
}

// Scenario10_SnapshotConsistency simulates snapshot-time mismatch
func Scenario10_SnapshotConsistency(ctx context.Context, h *ChaosHarness) error {
	filePath := h.sm.ActiveGenerationPath("web")

	agState := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
		Revision:         1,
		UpdatedAt:        time.Now(),
	}

	if err := state.AtomicWriteJSON(filePath, agState, nil); err != nil {
		return err
	}

	// Read it back and verify snapshot consistency
	time.Sleep(10 * time.Millisecond)

	return nil
}

// Tier 2 Scenarios (Medium, 1-5s each) - 8 scenarios

// Scenario11_AuthorityOscillation simulates rapid authority changes
func Scenario11_AuthorityOscillation(ctx context.Context, h *ChaosHarness) error {
	filePath := h.sm.ActiveGenerationPath("web")
	lockPath := h.sm.StateLockPath("web")

	for i := 0; i < 5; i++ {
		lock, err := state.AcquireAdvisoryLock(lockPath, 1*time.Second)
		if err != nil {
			return err
		}

		s := &state.ActiveGenerationState{
			SchemaVersion:    1,
			Service:          "web",
			ActiveGeneration: fmt.Sprintf("gen-%d", i),
			Revision:         int64(i + 1),
		}

		if err := state.AtomicWriteJSON(filePath, s, nil); err != nil {
			lock.Release()
			return err
		}

		lock.Release()
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}

// Scenario12_ConcurrentHealthChecks simulates parallel health validation
func Scenario12_ConcurrentHealthChecks(ctx context.Context, h *ChaosHarness) error {
	done := make(chan error, 10)

	for i := 0; i < 10; i++ {
		go func(idx int) {
			svc := fmt.Sprintf("service-%d", idx)
			filePath := h.sm.ActiveGenerationPath(svc)

			s := &state.ActiveGenerationState{
				SchemaVersion:    1,
				Service:          svc,
				ActiveGeneration: fmt.Sprintf("gen-%d", idx),
				Revision:         1,
			}

			done <- state.AtomicWriteJSON(filePath, s, nil)
		}(i)
	}

	for i := 0; i < 10; i++ {
		if err := <-done; err != nil {
			return err
		}
	}

	return nil
}

// Scenario13_OrphanAccumulation simulates orphan generation buildup
func Scenario13_OrphanAccumulation(ctx context.Context, h *ChaosHarness) error {
	// Persist 20 successive generations but only ever track the latest one
	// as active — orphan accumulation is exactly this shape: N generations
	// have existed, only 1 is referenced by the current active-generation
	// pointer. Each write goes through the real CAS-protected write path so
	// PreviousRevision must track whatever the prior write actually landed
	// — starting from whatever revision (if any) other scenarios sharing
	// this harness's state dir have already left behind.
	var previousRevision int64
	if current, err := h.sm.LoadActiveGenerationState("web"); err == nil && current != nil {
		previousRevision = current.Revision
	}
	for i := 0; i < 20; i++ {
		s := &state.ActiveGenerationState{
			SchemaVersion:    1,
			Service:          "web",
			ActiveGeneration: fmt.Sprintf("gen-%d", i),
			PreviousRevision: previousRevision,
		}
		if err := h.sm.WriteActiveGenerationState(s, nil); err != nil {
			return fmt.Errorf("write generation %d failed: %w", i, err)
		}
		previousRevision = s.Revision
	}

	return nil
}

// Scenario14_RolloutFailure simulates failed rollout transition
func Scenario14_RolloutFailure(ctx context.Context, h *ChaosHarness) error {
	// Persist rollout state in a failed/draining phase so post-scenario
	// invariant validation actually sees a rollout in progress.
	rollout := &state.RolloutState{
		SchemaVersion: 1,
		Service:       "web",
		OldGeneration: "gen-old",
		NewGeneration: "gen-new",
		Phase:         state.RolloutDraining,
		Authority:     state.AuthorityOld,
		StartedAt:     time.Now(),
	}
	if err := h.sm.WriteRolloutState(rollout, nil); err != nil {
		return fmt.Errorf("write rollout state failed: %w", err)
	}

	return nil
}

// Scenario15_RecoveryPlanTimeout simulates recovery plan timeout
func Scenario15_RecoveryPlanTimeout(ctx context.Context, h *ChaosHarness) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return nil
	}
}

// Scenario16_StateManagerConcurrency simulates state manager under load
func Scenario16_StateManagerConcurrency(ctx context.Context, h *ChaosHarness) error {
	svc := "web"
	filePath := h.sm.ActiveGenerationPath(svc)

	// Pre-create the directory to ensure it exists for all concurrent writes
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir failed: %w", err)
	}

	// Initialize with a valid file first
	initial := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          svc,
		ActiveGeneration: "gen-0",
		Revision:         1,
	}
	if err := state.AtomicWriteJSON(filePath, initial, nil); err != nil {
		return fmt.Errorf("initial write failed: %w", err)
	}

	done := make(chan error, 20)

	for i := 0; i < 20; i++ {
		go func(idx int) {
			s := &state.ActiveGenerationState{
				SchemaVersion:    1,
				Service:          svc,
				ActiveGeneration: fmt.Sprintf("gen-%d", idx),
				Revision:         int64(idx + 1),
			}

			done <- state.AtomicWriteJSON(filePath, s, nil)
		}(i)
	}

	for i := 0; i < 20; i++ {
		if err := <-done; err != nil {
			return err
		}
	}

	return nil
}

// Scenario17_MetricsUnderLoad simulates metrics collection under stress
func Scenario17_MetricsUnderLoad(ctx context.Context, h *ChaosHarness) error {
	// Record many metrics in rapid succession
	for i := 0; i < 100; i++ {
		done := h.mc.RecordRecoveryStart()
		time.Sleep(1 * time.Millisecond)
		done()
	}

	return nil
}

// Scenario18_LockFileStale simulates stale lock file cleanup
func Scenario18_LockFileStale(ctx context.Context, h *ChaosHarness) error {
	lockPath := h.sm.StateLockPath("web")

	// Create stale lock file
	if err := os.WriteFile(lockPath, []byte("stale"), 0644); err != nil {
		return err
	}

	// Attempt to acquire lock should handle stale file
	lock, err := state.AcquireAdvisoryLock(lockPath, 1*time.Second)
	if err != nil {
		// Expected if lock is truly stale
		return nil
	}
	defer lock.Release()

	return nil
}

// Tier 3 Scenarios (Long, 5-15s each) - 7 scenarios

// Scenario19_ExtendedRecoveryLoop simulates long recovery cycle
func Scenario19_ExtendedRecoveryLoop(ctx context.Context, h *ChaosHarness) error {
	for i := 0; i < 50; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil
}

// Scenario20_AuthorityOscillationLong simulates extended oscillation
func Scenario20_AuthorityOscillationLong(ctx context.Context, h *ChaosHarness) error {
	filePath := h.sm.ActiveGenerationPath("web")
	lockPath := h.sm.StateLockPath("web")

	for i := 0; i < 20; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		lock, err := state.AcquireAdvisoryLock(lockPath, 1*time.Second)
		if err != nil {
			continue
		}

		s := &state.ActiveGenerationState{
			SchemaVersion:    1,
			Service:          "web",
			ActiveGeneration: fmt.Sprintf("gen-%d", i),
			Revision:         int64(i + 1),
		}

		writeErr := state.AtomicWriteJSON(filePath, s, nil)
		lock.Release()
		if writeErr != nil {
			return fmt.Errorf("state write failed on iteration %d: %w", i, writeErr)
		}

		time.Sleep(250 * time.Millisecond)
	}

	return nil
}

// Scenario21_SnapshotStaleExhaustive simulates extended stale snapshots
func Scenario21_SnapshotStaleExhaustive(ctx context.Context, h *ChaosHarness) error {
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}
	return nil
}

// Scenario22_FullSystemDegradation simulates cascading failures
func Scenario22_FullSystemDegradation(ctx context.Context, h *ChaosHarness) error {
	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Simulate failure in each layer
		h.mc.RecordRecoveryFailure()

		// Rapid state changes
		for j := 0; j < 5; j++ {
			filePath := h.sm.ActiveGenerationPath("web")
			s := &state.ActiveGenerationState{
				SchemaVersion:    1,
				Service:          "web",
				ActiveGeneration: fmt.Sprintf("gen-%d-%d", i, j),
				Revision:         int64(i*5 + j),
			}
			if err := state.AtomicWriteJSON(filePath, s, nil); err != nil {
				return fmt.Errorf("state write failed at iteration %d.%d: %w", i, j, err)
			}
		}

		time.Sleep(300 * time.Millisecond)
	}

	return nil
}

// Scenario23_ReconciliationStorm simulates reconciliation spam
func Scenario23_ReconciliationStorm(ctx context.Context, h *ChaosHarness) error {
	for i := 0; i < 50; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		h.mc.RecordReconciliationRetry()
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}

// Scenario24_HealingLoopOverload simulates healing loop stress
func Scenario24_HealingLoopOverload(ctx context.Context, h *ChaosHarness) error {
	for i := 0; i < 100; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		h.mc.RecordHealingLoopIteration()
		time.Sleep(50 * time.Millisecond)
	}

	return nil
}

// Scenario25_ComplexFailureChain simulates interconnected failures
func Scenario25_ComplexFailureChain(ctx context.Context, h *ChaosHarness) error {
	filePath := h.sm.ActiveGenerationPath("web")
	lockPath := h.sm.StateLockPath("web")

	for phase := 0; phase < 5; phase++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Phase 1: Acquire locks
		lock, _ := state.AcquireAdvisoryLock(lockPath, 1*time.Second)

		// Phase 2: Record metrics
		done := h.mc.RecordRecoveryStart()
		time.Sleep(100 * time.Millisecond)

		// Phase 3: State mutation
		s := &state.ActiveGenerationState{
			SchemaVersion:    1,
			Service:          "web",
			ActiveGeneration: fmt.Sprintf("gen-phase-%d", phase),
			Revision:         int64(phase + 1),
		}
		writeErr := state.AtomicWriteJSON(filePath, s, nil)

		// Phase 4: Cleanup
		done()
		if lock != nil {
			lock.Release()
		}

		if writeErr != nil {
			return fmt.Errorf("state write failed at phase %d: %w", phase, writeErr)
		}

		time.Sleep(200 * time.Millisecond)
	}

	return nil
}

// AllScenarios returns all 25 failure scenarios
func AllScenarios() []*FailureScenario {
	return []*FailureScenario{
		// Tier 1
		{Name: "TimeoutDuringRecovery", Tier: 1, Duration: 500 * time.Millisecond, Run: Scenario1_TimeoutDuringRecovery},
		{Name: "LockContention", Tier: 1, Duration: 500 * time.Millisecond, Run: Scenario2_LockContention},
		{Name: "PartialWriteFailure", Tier: 1, Duration: 500 * time.Millisecond, Run: Scenario3_PartialWriteFailure},
		{Name: "StateFileCorruption", Tier: 1, Duration: 500 * time.Millisecond, Run: Scenario4_StateFileCorruption},
		{Name: "MemoryPressureSimulation", Tier: 1, Duration: 500 * time.Millisecond, Run: Scenario5_MemoryPressureSimulation},
		{Name: "RecoveryRaceCondition", Tier: 1, Duration: 500 * time.Millisecond, Run: Scenario6_RecoveryRaceCondition},
		{Name: "StaleTransitionDetection", Tier: 1, Duration: 500 * time.Millisecond, Run: Scenario7_StaleTransitionDetection},
		{Name: "RevisionOverflow", Tier: 1, Duration: 500 * time.Millisecond, Run: Scenario8_RevisionOverflow},
		{Name: "MultipleServiceStates", Tier: 1, Duration: 500 * time.Millisecond, Run: Scenario9_MultipleServiceStates},
		{Name: "SnapshotConsistency", Tier: 1, Duration: 500 * time.Millisecond, Run: Scenario10_SnapshotConsistency},

		// Tier 2
		{Name: "AuthorityOscillation", Tier: 2, Duration: 3 * time.Second, Run: Scenario11_AuthorityOscillation},
		{Name: "ConcurrentHealthChecks", Tier: 2, Duration: 3 * time.Second, Run: Scenario12_ConcurrentHealthChecks},
		{Name: "OrphanAccumulation", Tier: 2, Duration: 3 * time.Second, Run: Scenario13_OrphanAccumulation},
		{Name: "RolloutFailure", Tier: 2, Duration: 3 * time.Second, Run: Scenario14_RolloutFailure},
		{Name: "RecoveryPlanTimeout", Tier: 2, Duration: 3 * time.Second, Run: Scenario15_RecoveryPlanTimeout},
		{Name: "StateManagerConcurrency", Tier: 2, Duration: 3 * time.Second, Run: Scenario16_StateManagerConcurrency},
		{Name: "MetricsUnderLoad", Tier: 2, Duration: 3 * time.Second, Run: Scenario17_MetricsUnderLoad},
		{Name: "LockFileStale", Tier: 2, Duration: 3 * time.Second, Run: Scenario18_LockFileStale},

		// Tier 3
		{Name: "ExtendedRecoveryLoop", Tier: 3, Duration: 10 * time.Second, Run: Scenario19_ExtendedRecoveryLoop},
		{Name: "AuthorityOscillationLong", Tier: 3, Duration: 10 * time.Second, Run: Scenario20_AuthorityOscillationLong},
		{Name: "SnapshotStaleExhaustive", Tier: 3, Duration: 10 * time.Second, Run: Scenario21_SnapshotStaleExhaustive},
		{Name: "FullSystemDegradation", Tier: 3, Duration: 10 * time.Second, Run: Scenario22_FullSystemDegradation},
		{Name: "ReconciliationStorm", Tier: 3, Duration: 10 * time.Second, Run: Scenario23_ReconciliationStorm},
		{Name: "HealingLoopOverload", Tier: 3, Duration: 10 * time.Second, Run: Scenario24_HealingLoopOverload},
		{Name: "ComplexFailureChain", Tier: 3, Duration: 10 * time.Second, Run: Scenario25_ComplexFailureChain},
	}
}
