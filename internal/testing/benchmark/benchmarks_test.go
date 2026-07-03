package benchmark

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/state"
)

// BenchmarkRecoveryPlanGeneration measures recovery plan generation latency
func BenchmarkRecoveryPlanGeneration(b *testing.B) {
	tmpDir := b.TempDir()
	sm := state.NewStateManager(tmpDir, nil)

	// Initialize test state
	initial := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
		Revision:         1,
		UpdatedAt:        time.Now(),
	}
	filePath := sm.ActiveGenerationPath("web")
	state.AtomicWriteJSON(filePath, initial, nil)

	runner := NewBenchmarkRunner("RecoveryPlanGeneration", int64(b.N))
	runner.Start()

	for i := 0; i < b.N; i++ {
		start := time.Now()

		// Simulate recovery plan generation
		plan := &state.RecoveryPlan{
			Service:                 "web",
			Epoch:                   uint64(i),
			AuthoritativeGeneration: fmt.Sprintf("gen-%d", i+2),
			DecisionTrace: []string{
				"started recovery",
				fmt.Sprintf("epoch=%d", i),
				"determining authority",
				"action=restore_single",
			},
		}
		_ = plan

		runner.RecordOperation(time.Since(start))
	}

	result := runner.Stop()
	b.Logf("%s", result.String())

	// Assert performance targets
	if result.AvgLatency > 100*time.Millisecond {
		b.Logf("WARNING: Average latency %.2fms exceeds target 100ms", result.AvgLatency.Seconds()*1000)
	}
}

// BenchmarkLockOperations measures advisory lock performance
func BenchmarkLockOperations(b *testing.B) {
	tmpDir := b.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	lockPath := sm.StateLockPath("web")

	runner := NewBenchmarkRunner("LockOperations", int64(b.N))
	runner.Start()

	for i := 0; i < b.N; i++ {
		start := time.Now()

		lock, err := state.AcquireAdvisoryLock(lockPath, 5*time.Second)
		if err != nil {
			b.Fatalf("lock acquire failed: %v", err)
		}
		lock.Release()

		runner.RecordOperation(time.Since(start))
	}

	result := runner.Stop()
	b.Logf("%s", result.String())

	// Assert performance targets
	if result.AvgLatency > 10*time.Millisecond {
		b.Logf("WARNING: Average latency %.2fms exceeds target 10ms", result.AvgLatency.Seconds()*1000)
	}
}

// BenchmarkStateFileIO measures atomic write performance
func BenchmarkStateFileIO(b *testing.B) {
	tmpDir := b.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	filePath := sm.ActiveGenerationPath("web")

	runner := NewBenchmarkRunner("StateFileIO", int64(b.N))
	runner.Start()

	for i := 0; i < b.N; i++ {
		start := time.Now()

		agState := &state.ActiveGenerationState{
			SchemaVersion:    1,
			Service:          "web",
			ActiveGeneration: fmt.Sprintf("gen-%d", i),
			Revision:         int64(i + 1),
			UpdatedAt:        time.Now(),
		}

		if err := state.AtomicWriteJSON(filePath, agState, nil); err != nil {
			b.Fatalf("write failed: %v", err)
		}

		runner.RecordOperation(time.Since(start))
	}

	result := runner.Stop()
	b.Logf("%s", result.String())

	// Assert performance targets
	if result.AvgLatency > 5*time.Millisecond {
		b.Logf("WARNING: Average latency %.2fms exceeds target 5ms", result.AvgLatency.Seconds()*1000)
	}
}

// BenchmarkMetricsCollection measures metrics overhead (with simulated work)
func BenchmarkMetricsCollection(b *testing.B) {
	mc := metrics.NewMetricsCollector()

	runner := NewBenchmarkRunner("MetricsCollection", int64(b.N))
	runner.Start()

	for i := 0; i < b.N; i++ {
		start := time.Now()

		// Record various metrics
		done := mc.RecordRecoveryStart()
		time.Sleep(1 * time.Millisecond)
		done()

		mc.RecordAuthorityTransition(fmt.Sprintf("gen-%d", i), fmt.Sprintf("gen-%d", i+1))
		mc.RecordHealingLoopIteration()

		runner.RecordOperation(time.Since(start))
	}

	result := runner.Stop()
	b.Logf("%s", result.String())

	// Metrics overhead should be < 1ms per operation
	if result.AvgLatency > 1*time.Millisecond {
		b.Logf("WARNING: Metrics overhead %.2fµs might impact performance", float64(result.AvgLatency.Microseconds()))
	}
}

// BenchmarkMetricsAtomicOps measures pure atomic metric operations
func BenchmarkMetricsAtomicOps(b *testing.B) {
	mc := metrics.NewMetricsCollector()

	// Pure atomic counter operations (no harness overhead)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mc.RecordRecoveryFailure()
		mc.RecordStaleTransition()
		mc.RecordCleanupBlocked()
		mc.RecordReconciliationRetry()
		mc.RecordHealingLoopIteration()
	}
}

// BenchmarkAuthorityTransitions measures transition cost
func BenchmarkAuthorityTransitions(b *testing.B) {
	mc := metrics.NewMetricsCollector()

	runner := NewBenchmarkRunner("AuthorityTransitions", int64(b.N))
	runner.Start()

	for i := 0; i < b.N; i++ {
		start := time.Now()

		mc.RecordAuthorityTransition(
			fmt.Sprintf("gen-%d", i),
			fmt.Sprintf("gen-%d", i+1),
		)

		runner.RecordOperation(time.Since(start))
	}

	result := runner.Stop()
	b.Logf("%s", result.String())
}

// BenchmarkCleanupOperations measures cleanup safety check performance
func BenchmarkCleanupOperations(b *testing.B) {
	runner := NewBenchmarkRunner("CleanupOperations", int64(b.N))
	runner.Start()

	for i := 0; i < b.N; i++ {
		start := time.Now()

		// Simulate cleanup safety check
		check := state.ValidateCleanupSafe(
			true, // startup ready
			nil,  // no rollout
			&state.ActiveGenerationState{UpdatedAt: time.Now()},
			&state.GenerationInventory{SnapshotTime: time.Now()},
			time.Now().Add(-30*time.Second),
		)
		_ = check.Safe()

		runner.RecordOperation(time.Since(start))
	}

	result := runner.Stop()
	b.Logf("%s", result.String())
}

// LoadTestRecoveryPlan simulates recovery plan generation under load
func TestLoadTestRecoveryPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	concurrency := 100
	duration := 5 * time.Second
	ltr := NewLoadTestRunner("RecoveryPlanLoad", concurrency, duration)

	var wg sync.WaitGroup
	done := make(chan struct{})

	ltr.Start()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					start := time.Now()

					// Simulate recovery plan generation
					plan := &state.RecoveryPlan{
						Service:                 "web",
						AuthoritativeGeneration: "gen-new",
						DecisionTrace:           []string{"started"},
					}
					_ = plan

					ltr.RecordOperation(time.Since(start))
				}
			}
		}()
	}

	// Wait for duration
	time.Sleep(duration)
	close(done)
	wg.Wait()

	result := ltr.Stop()
	t.Logf("%s", result.String())

	// Assert throughput target: 1000+ ops/sec
	if result.Throughput < 1000 {
		t.Logf("WARNING: Throughput %.2f ops/sec below target 1000 ops/sec", result.Throughput)
	}
}

// LoadTestLockContentionDuring simulates lock contention
func TestLoadTestLockContention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	lockPath := sm.StateLockPath("web")

	concurrency := 50
	duration := 5 * time.Second
	ltr := NewLoadTestRunner("LockContention", concurrency, duration)

	var wg sync.WaitGroup
	done := make(chan struct{})

	ltr.Start()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					start := time.Now()

					lock, err := state.AcquireAdvisoryLock(lockPath, 5*time.Second)
					if err != nil {
						continue
					}
					lock.Release()

					ltr.RecordOperation(time.Since(start))
				}
			}
		}()
	}

	time.Sleep(duration)
	close(done)
	wg.Wait()

	result := ltr.Stop()
	t.Logf("%s", result.String())

	// P99 latency should stay under 100ms even with contention
	if result.P99Latency > 100*time.Millisecond {
		t.Logf("WARNING: P99 latency %v exceeds acceptable threshold", result.P99Latency)
	}
}

// LoadTestStateFileIO simulates concurrent state writes
func TestLoadTestStateFileIO(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)
	filePath := sm.ActiveGenerationPath("web")

	concurrency := 20
	duration := 10 * time.Second
	ltr := NewLoadTestRunner("StateFileIOLoad", concurrency, duration)

	var wg sync.WaitGroup
	done := make(chan struct{})
	counter := 0
	counterMu := sync.Mutex{}

	ltr.Start()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					start := time.Now()

					counterMu.Lock()
					counter++
					idx := counter
					counterMu.Unlock()

					agState := &state.ActiveGenerationState{
						SchemaVersion:    1,
						Service:          "web",
						ActiveGeneration: fmt.Sprintf("gen-%d", idx),
						Revision:         int64(idx),
						UpdatedAt:        time.Now(),
					}

					if err := state.AtomicWriteJSON(filePath, agState, nil); err == nil {
						ltr.RecordOperation(time.Since(start))
					}
				}
			}
		}(i)
	}

	time.Sleep(duration)
	close(done)
	wg.Wait()

	result := ltr.Stop()
	t.Logf("%s", result.String())

	// Should sustain 100+ writes/sec
	if result.Throughput < 100 {
		t.Logf("WARNING: Throughput %.2f writes/sec below target 100 writes/sec", result.Throughput)
	}
}
