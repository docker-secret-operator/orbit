package profile

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/metrics"
)

// TestMetricsLockContention measures lock contention on metrics updates
func TestMetricsLockContention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping lock contention test in short mode")
	}

	mc := metrics.NewMetricsCollector()

	// Test with increasing concurrency
	concurrencies := []int{1, 2, 4, 8, 13}
	scalabilityTest := NewScalabilityTest("MetricsRecording", func() {
		mc.RecordRecoveryFailure()
		mc.RecordStaleTransition()
		mc.RecordCleanupBlocked()
		mc.RecordReconciliationRetry()
		mc.RecordHealingLoopIteration()
	})

	t.Log("Starting lock contention analysis for metrics recording...")

	for _, conc := range concurrencies {
		throughput := scalabilityTest.Run(conc, 1*time.Second)
		t.Log(fmt.Sprintf("Concurrency: %d ops/sec: %.0f", conc, throughput))
	}

	result := scalabilityTest.Analyze()
	t.Log(result.String())

	// Verify acceptable scaling
	if result.Linear1To8 < 4.0 {
		t.Log(fmt.Sprintf("WARNING: Sublinear scaling detected (1→8: %.1fx, expected >6x for lock-free)", result.Linear1To8))
	}
}

// TestMetricsGetSnapshotContention measures GetSnapshot() contention
func TestMetricsGetSnapshotContention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping snapshot contention test in short mode")
	}

	mc := metrics.NewMetricsCollector()

	// Warm up: record some data
	for i := 0; i < 100; i++ {
		mc.RecordRecoveryFailure()
		mc.RecordAuthorityTransition(fmt.Sprintf("gen-%d", i), fmt.Sprintf("gen-%d", i+1))
	}

	analyzer := NewLockContentionAnalyzer("GetSnapshot")

	// Record wait times for GetSnapshot
	for i := 0; i < 1000; i++ {
		analyzer.MeasureLockWait(func() {
			mc.GetSnapshot()
		})
	}

	result := analyzer.Analyze()
	t.Log(result.String())

	// GetSnapshot should be fast (minimal lock hold time)
	if result.AvgWaitTime > 10.0 {
		t.Log(fmt.Sprintf("WARNING: GetSnapshot taking %.2fµs (expect <10µs)", result.AvgWaitTime))
	}
}

// TestAuthorityTransitionContention measures transition operation contention
func TestAuthorityTransitionContention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping transition contention test in short mode")
	}

	mc := metrics.NewMetricsCollector()

	concurrencies := []int{1, 2, 4, 8, 13}
	scalabilityTest := NewScalabilityTest("AuthorityTransitions", func() {
		mc.RecordAuthorityTransition("gen-old", "gen-new")
	})

	t.Log("Starting lock contention analysis for authority transitions...")

	for _, conc := range concurrencies {
		throughput := scalabilityTest.Run(conc, 1*time.Second)
		t.Log(fmt.Sprintf("Concurrency: %d ops/sec: %.0f", conc, throughput))
	}

	result := scalabilityTest.Analyze()
	t.Log(result.String())

	// Authority transitions must scale well (mostly atomic ops)
	if result.Linear1To8 < 6.0 {
		t.Log(fmt.Sprintf("WARNING: Authority transitions not scaling well (1 to 8): %.1fx", result.Linear1To8))
	}
}

// TestConcurrentStateWrites simulates concurrent state persistence
func TestConcurrentStateWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent state write test in short mode")
	}

	// Simulate concurrent write patterns with atomic counter
	var writeCount int64

	concurrencies := []int{1, 2, 4, 8}
	scalabilityTest := NewScalabilityTest("StateWrites", func() {
		atomic.AddInt64(&writeCount, 1)
		// In real scenario, would call AtomicWriteJSON
		// Simulating I/O with brief operation
		time.Sleep(100 * time.Nanosecond)
	})

	t.Log("Starting concurrent state write analysis...")

	for _, conc := range concurrencies {
		writeCount = 0
		throughput := scalabilityTest.Run(conc, 1*time.Second)
		t.Log(fmt.Sprintf("Concurrency=%d: %.0f writes/sec", conc, throughput))
	}

	result := scalabilityTest.Analyze()
	t.Log(result.String())
}

// BenchmarkMetricsContentionUnderLoad benchmarks metrics with sustained load
func BenchmarkMetricsContentionUnderLoad(b *testing.B) {
	mc := metrics.NewMetricsCollector()

	// Simulate multi-goroutine scenario
	concurrency := 10
	opsPerGoroutine := b.N / concurrency

	b.ResetTimer()

	done := make(chan struct{})
	var wg atomic.Int32

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Add(-1)
			for j := 0; j < opsPerGoroutine; j++ {
				mc.RecordRecoveryFailure()
				mc.RecordStaleTransition()
				mc.RecordCleanupBlocked()
			}
			<-done
		}()
	}

	// Measure GetSnapshot overhead under load
	snapshots := 0
	for wg.Load() > 0 && snapshots < 100 {
		mc.GetSnapshot()
		snapshots++
		time.Sleep(10 * time.Millisecond)
	}

	close(done)
	// Wait for goroutines
	for wg.Load() > 0 {
		time.Sleep(1 * time.Millisecond)
	}

	b.Logf("Completed with %d snapshot calls under concurrent load", snapshots)
}

// BenchmarkMetricsScalability measures how metrics scale with goroutines
func BenchmarkMetricsScalability(b *testing.B) {
	mc := metrics.NewMetricsCollector()

	// Test different concurrency levels
	for _, conc := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("concurrency-%d", conc), func(b *testing.B) {
			var wg atomic.Int32
			done := make(chan struct{})

			b.ResetTimer()

			opsPerGoroutine := b.N / conc
			for i := 0; i < conc; i++ {
				wg.Add(1)
				go func() {
					defer wg.Add(-1)
					for j := 0; j < opsPerGoroutine; j++ {
						mc.RecordRecoveryFailure()
						mc.RecordHealingLoopIteration()
					}
					<-done
				}()
			}

			close(done)
			for wg.Load() > 0 {
				time.Sleep(1 * time.Microsecond)
			}
		})
	}
}
