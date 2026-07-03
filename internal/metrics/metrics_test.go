package metrics

import (
	"testing"
	"time"
)

func TestMetricsCollectorRecovery(t *testing.T) {
	mc := NewMetricsCollector()

	// Record a recovery
	done := mc.RecordRecoveryStart()
	time.Sleep(10 * time.Millisecond)
	done()

	snapshot := mc.GetSnapshot()

	if snapshot.RecoveryCount != 1 {
		t.Errorf("expected 1 recovery, got %d", snapshot.RecoveryCount)
	}

	if snapshot.LastRecoveryDuration < 10 {
		t.Errorf("recovery duration should be >= 10ms, got %d", snapshot.LastRecoveryDuration)
	}

	if snapshot.AvgRecoveryDuration < 10 {
		t.Errorf("avg recovery duration should be >= 10ms, got %d", snapshot.AvgRecoveryDuration)
	}
}

func TestMetricsCollectorRecoveryFailure(t *testing.T) {
	mc := NewMetricsCollector()

	done := mc.RecordRecoveryStart()
	done()
	mc.RecordRecoveryFailure()

	snapshot := mc.GetSnapshot()

	if snapshot.RecoveryCount != 1 {
		t.Errorf("expected 1 recovery, got %d", snapshot.RecoveryCount)
	}

	if snapshot.RecoveryFailureCount != 1 {
		t.Errorf("expected 1 failure, got %d", snapshot.RecoveryFailureCount)
	}

	if snapshot.FailureRate() != 100 {
		t.Errorf("expected 100%% failure rate, got %f%%", snapshot.FailureRate())
	}
}

func TestMetricsCollectorAuthorityTransition(t *testing.T) {
	mc := NewMetricsCollector()

	mc.RecordAuthorityTransition("gen-old", "gen-new")
	mc.RecordAuthorityTransition("gen-new", "gen-newer")

	snapshot := mc.GetSnapshot()

	if snapshot.AuthorityTransitions != 2 {
		t.Errorf("expected 2 transitions, got %d", snapshot.AuthorityTransitions)
	}

	if snapshot.GenerationSwitches != 2 {
		t.Errorf("expected 2 switches, got %d", snapshot.GenerationSwitches)
	}

	if snapshot.CurrentAuthority != "gen-newer" {
		t.Errorf("expected current authority gen-newer, got %s", snapshot.CurrentAuthority)
	}
}

func TestMetricsCollectorStaleTransition(t *testing.T) {
	mc := NewMetricsCollector()

	mc.RecordAuthorityTransition("gen-a", "gen-b")
	mc.RecordStaleTransition()
	mc.RecordAuthorityTransition("gen-b", "gen-c")

	snapshot := mc.GetSnapshot()

	if snapshot.TransitionStaleCount != 1 {
		t.Errorf("expected 1 stale transition, got %d", snapshot.TransitionStaleCount)
	}

	if snapshot.AuthorityTransitions != 2 {
		t.Errorf("expected 2 transitions, got %d", snapshot.AuthorityTransitions)
	}

	expectedRate := 50.0 // 1 stale out of 2 transitions
	actualRate := snapshot.StaleTransitionRate()
	if actualRate < expectedRate-0.1 || actualRate > expectedRate+0.1 {
		t.Errorf("expected ~50%% stale rate, got %f%%", actualRate)
	}
}

func TestMetricsCollectorCleanupBlocked(t *testing.T) {
	mc := NewMetricsCollector()

	mc.RecordRecoveryStart()()
	mc.RecordCleanupBlocked()

	mc.RecordRecoveryStart()()
	mc.RecordCleanupBlocked()

	snapshot := mc.GetSnapshot()

	if snapshot.CleanupBlockedCount != 2 {
		t.Errorf("expected 2 blocked cleanups, got %d", snapshot.CleanupBlockedCount)
	}

	if snapshot.CleanupBlockRate() != 100 {
		t.Errorf("expected 100%% block rate, got %f%%", snapshot.CleanupBlockRate())
	}
}

func TestMetricsCollectorHealingLoop(t *testing.T) {
	mc := NewMetricsCollector()

	for i := 0; i < 10; i++ {
		mc.RecordHealingLoopIteration()
	}

	snapshot := mc.GetSnapshot()

	if snapshot.HealingLoopIterations != 10 {
		t.Errorf("expected 10 healing loops, got %d", snapshot.HealingLoopIterations)
	}
}

func TestMetricsCollectorReconciliationRetries(t *testing.T) {
	mc := NewMetricsCollector()

	mc.RecordReconciliationRetry()
	mc.RecordReconciliationRetry()
	mc.RecordReconciliationRetry()

	snapshot := mc.GetSnapshot()

	if snapshot.ReconciliationRetries != 3 {
		t.Errorf("expected 3 retries, got %d", snapshot.ReconciliationRetries)
	}
}

func TestMetricsCollectorSetCurrentState(t *testing.T) {
	mc := NewMetricsCollector()

	mc.SetCurrentState("gen-prod", "draining", "Ready", false)

	snapshot := mc.GetSnapshot()

	if snapshot.CurrentAuthority != "gen-prod" {
		t.Errorf("expected gen-prod, got %s", snapshot.CurrentAuthority)
	}

	if snapshot.CurrentRolloutPhase != "draining" {
		t.Errorf("expected draining phase, got %s", snapshot.CurrentRolloutPhase)
	}

	if snapshot.StartupState != "Ready" {
		t.Errorf("expected Ready state, got %s", snapshot.StartupState)
	}

	if snapshot.DegradedFlag {
		t.Error("expected degraded=false")
	}
}

func TestMetricsCollectorSuccessRate(t *testing.T) {
	mc := NewMetricsCollector()

	// 4 total: 3 successes, 1 failure = 75% success rate
	mc.RecordRecoveryStart()()
	mc.RecordRecoveryStart()()
	mc.RecordRecoveryStart()()
	mc.RecordRecoveryStart()()
	mc.RecordRecoveryFailure()

	snapshot := mc.GetSnapshot()

	expectedRate := 75.0 // 3 successes out of 4
	actualRate := snapshot.SuccessRate()
	if actualRate < expectedRate-0.1 || actualRate > expectedRate+0.1 {
		t.Errorf("expected ~75%% success rate, got %f%%", actualRate)
	}
}

func TestMetricsCollectorDurationStats(t *testing.T) {
	mc := NewMetricsCollector()

	// Record recoveries with different durations
	for _, duration := range []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond} {
		done := mc.RecordRecoveryStart()
		time.Sleep(duration)
		done()
	}

	snapshot := mc.GetSnapshot()

	if snapshot.RecoveryCount != 3 {
		t.Errorf("expected 3 recoveries, got %d", snapshot.RecoveryCount)
	}

	if snapshot.MinRecoveryDuration < 10 {
		t.Errorf("min duration should be >= 10ms, got %d", snapshot.MinRecoveryDuration)
	}

	if snapshot.MaxRecoveryDuration < 30 {
		t.Errorf("max duration should be >= 30ms, got %d", snapshot.MaxRecoveryDuration)
	}

	if snapshot.AvgRecoveryDuration == 0 {
		t.Error("avg duration should not be zero")
	}
}

func TestMetricsCollectorConcurrency(t *testing.T) {
	mc := NewMetricsCollector()

	// Simulate concurrent updates
	done := make(chan struct{})

	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			mc.RecordHealingLoopIteration()
			mc.RecordAuthorityTransition("old", "new")
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	snapshot := mc.GetSnapshot()

	if snapshot.HealingLoopIterations != 10 {
		t.Errorf("expected 10 healing loops, got %d", snapshot.HealingLoopIterations)
	}

	if snapshot.AuthorityTransitions != 10 {
		t.Errorf("expected 10 transitions, got %d", snapshot.AuthorityTransitions)
	}
}
