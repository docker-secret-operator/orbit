package stack

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNewMetrics(t *testing.T) {
	m := NewMetrics()

	if m == nil {
		t.Fatal("NewMetrics returned nil")
	}
	if m.maxEvents != 1000 {
		t.Errorf("maxEvents = %d, want 1000", m.maxEvents)
	}
}

func TestNewObservabilityHooks(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	if oh == nil {
		t.Fatal("NewObservabilityHooks returned nil")
	}
	if oh.metrics == nil {
		t.Fatal("metrics not initialized")
	}
}

func TestRegisterHook(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	hook := func(event *MetricEvent) error {
		return nil
	}

	oh.RegisterHook("test_hook", hook)

	if len(oh.hooks) != 1 {
		t.Errorf("hooks count = %d, want 1", len(oh.hooks))
	}
}

func TestUnregisterHook(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	oh.RegisterHook("hook1", func(event *MetricEvent) error { return nil })
	if len(oh.hooks) != 1 {
		t.Fatal("hook not registered")
	}

	oh.UnregisterHook("hook1")
	if len(oh.hooks) != 0 {
		t.Error("hook not unregistered")
	}
}

func TestEmitEvent(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	called := make(chan struct{}, 1)
	oh.RegisterHook("test", func(event *MetricEvent) error {
		called <- struct{}{}
		return nil
	})

	event := &MetricEvent{
		Type:    "rollout_start",
		Service: "service1",
		Status:  "pending",
	}

	oh.EmitEvent(event)

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Error("hook not called")
	}
}

func TestEmitEventRecorded(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	event := &MetricEvent{
		Type:     "rollout_success",
		Service:  "service1",
		Status:   "success",
		Duration: 5 * time.Second,
	}

	oh.EmitEvent(event)

	oh.mu.RLock()
	if len(oh.metrics.Events) != 1 {
		t.Errorf("events recorded = %d, want 1", len(oh.metrics.Events))
	}
	oh.mu.RUnlock()
}

func TestEmitEventUpdateCounters(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	tests := []struct {
		eventType string
		checkFunc func() int64
	}{
		{"rollout_start", func() int64 { return oh.metrics.TotalRollouts.Load() }},
		{"rollout_success", func() int64 { return oh.metrics.SuccessfulRollouts.Load() }},
		{"rollout_failure", func() int64 { return oh.metrics.FailedRollouts.Load() }},
		{"rollout_rollback", func() int64 { return oh.metrics.RolledBackRollouts.Load() }},
		{"health_check_passed", func() int64 { return oh.metrics.HealthChecksPassed.Load() }},
		{"health_check_failed", func() int64 { return oh.metrics.HealthChecksFailed.Load() }},
		{"health_check_timeout", func() int64 { return oh.metrics.HealthChecksTimeout.Load() }},
		{"circuit_opened", func() int64 { return oh.metrics.CircuitsOpened.Load() }},
		{"circuit_closed", func() int64 { return oh.metrics.CircuitsClosed.Load() }},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			oh.metrics.TotalRollouts.Store(0)
			oh.metrics.SuccessfulRollouts.Store(0)
			oh.metrics.FailedRollouts.Store(0)
			oh.metrics.RolledBackRollouts.Store(0)
			oh.metrics.HealthChecksPassed.Store(0)
			oh.metrics.HealthChecksFailed.Store(0)
			oh.metrics.HealthChecksTimeout.Store(0)
			oh.metrics.CircuitsOpened.Store(0)
			oh.metrics.CircuitsClosed.Store(0)
			event := &MetricEvent{Type: tt.eventType, Service: "s1", Status: "ok"}
			oh.EmitEvent(event)

			if tt.checkFunc() != 1 {
				t.Errorf("counter not incremented for %s", tt.eventType)
			}
		})
	}
}

func TestGetMetricsSnapshot(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	oh.metrics.TotalRollouts.Store(10)
	oh.metrics.SuccessfulRollouts.Store(8)
	oh.metrics.FailedRollouts.Store(2)

	snapshot := oh.GetMetricsSnapshot()

	if snapshot.TotalRollouts != 10 {
		t.Errorf("total rollouts = %d, want 10", snapshot.TotalRollouts)
	}
	if snapshot.SuccessfulRollouts != 8 {
		t.Errorf("successful rollouts = %d, want 8", snapshot.SuccessfulRollouts)
	}
}

func TestGetMetricsSnapshotSuccessRate(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	oh.metrics.TotalRollouts.Store(10)
	oh.metrics.SuccessfulRollouts.Store(8)

	snapshot := oh.GetMetricsSnapshot()

	if snapshot.SuccessRate != 80.0 {
		t.Errorf("success rate = %f, want 80.0", snapshot.SuccessRate)
	}
}

func TestGetMetricsSnapshotAverages(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	oh.mu.Lock()
	oh.metrics.RolloutTimes = []time.Duration{
		2 * time.Second,
		4 * time.Second,
		6 * time.Second,
	}
	oh.mu.Unlock()

	snapshot := oh.GetMetricsSnapshot()

	expected := 4 * time.Second
	if snapshot.AvgRolloutTime != expected {
		t.Errorf("avg rollout time = %v, want %v", snapshot.AvgRolloutTime, expected)
	}
}

func TestRecordRolloutTiming(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	event := &MetricEvent{
		Type:     "rollout_success",
		Service:  "service1",
		Duration: 5 * time.Second,
	}

	oh.EmitEvent(event)

	oh.mu.RLock()
	if len(oh.metrics.RolloutTimes) != 1 {
		t.Errorf("rollout times recorded = %d, want 1", len(oh.metrics.RolloutTimes))
	}
	oh.mu.RUnlock()
}

func TestRecordHealthCheckTiming(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	event := &MetricEvent{
		Type:     "health_check_passed",
		Service:  "service1",
		Duration: 500 * time.Millisecond,
	}

	oh.EmitEvent(event)

	oh.mu.RLock()
	if len(oh.metrics.HealthCheckTimes) != 1 {
		t.Errorf("health check times recorded = %d, want 1", len(oh.metrics.HealthCheckTimes))
	}
	oh.mu.RUnlock()
}

func TestObservabilityGetEventHistory(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	for i := 0; i < 5; i++ {
		oh.EmitEvent(&MetricEvent{Type: "rollout_start", Service: "s1"})
		oh.EmitEvent(&MetricEvent{Type: "rollout_success", Service: "s1"})
	}

	history := oh.GetEventHistory("rollout_start", 10)
	if len(history) != 5 {
		t.Errorf("history length = %d, want 5", len(history))
	}
}

func TestObservabilityGetEventHistoryLimit(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	for i := 0; i < 10; i++ {
		oh.EmitEvent(&MetricEvent{Type: "rollout_start", Service: "s1"})
	}

	history := oh.GetEventHistory("rollout_start", 3)
	if len(history) != 3 {
		t.Errorf("history limited to = %d, want 3", len(history))
	}
}

func TestObservabilityGetEventHistoryEmpty(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	history := oh.GetEventHistory("nonexistent", 10)
	if len(history) != 0 {
		t.Errorf("empty history = %d, want 0", len(history))
	}
}

func TestStartTrace(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	ctx := context.Background()
	newCtx, closer := oh.StartTrace(ctx, "test_op")

	if newCtx == nil {
		t.Fatal("returned context is nil")
	}

	closer()
}

func TestSetTracerFunc(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	traceCalled := false
	oh.SetTracerFunc(func(ctx context.Context, name string) (context.Context, func()) {
		traceCalled = true
		return ctx, func() {}
	})

	ctx := context.Background()
	_, _ = oh.StartTrace(ctx, "test")

	if !traceCalled {
		t.Error("tracer function not called")
	}
}

func TestMetricsSnapshotString(t *testing.T) {
	snapshot := MetricsSnapshot{
		Timestamp:          time.Now(),
		TotalRollouts:      10,
		SuccessfulRollouts: 8,
		FailedRollouts:     2,
		SuccessRate:        80.0,
	}

	str := snapshot.String()
	if str == "" {
		t.Error("MetricsSnapshot.String() returned empty")
	}
	if !strings.Contains(str, "Metrics") {
		t.Error("String should contain 'Metrics'")
	}
}

func TestPrometheusHook(t *testing.T) {
	hook := PrometheusHook()

	event := &MetricEvent{Type: "rollout_success", Service: "s1"}
	err := hook(event)

	if err != nil {
		t.Errorf("PrometheusHook returned error: %v", err)
	}
}

func TestJSONLogHook(t *testing.T) {
	log := zap.NewNop()
	hook := JSONLogHook(log)

	event := &MetricEvent{
		Type:     "rollout_success",
		Service:  "service1",
		Status:   "success",
		Duration: 5 * time.Second,
	}

	err := hook(event)
	if err != nil {
		t.Errorf("JSONLogHook returned error: %v", err)
	}
}

func TestEmitEventEventsTrimmed(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	oh.metrics.maxEvents = 5

	for i := 0; i < 10; i++ {
		oh.EmitEvent(&MetricEvent{Type: "test", Service: "s1"})
	}

	oh.mu.RLock()
	eventsCount := len(oh.metrics.Events)
	oh.mu.RUnlock()

	if eventsCount > 5 {
		t.Errorf("events not trimmed: %d > 5", eventsCount)
	}
}

func TestEmitEventAsynchronous(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	blockChan := make(chan struct{})
	oh.RegisterHook("blocking", func(event *MetricEvent) error {
		<-blockChan
		return nil
	})

	done := make(chan struct{})
	go func() {
		oh.EmitEvent(&MetricEvent{Type: "test", Service: "s1"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("EmitEvent blocked by hook")
	}

	close(blockChan)
}

// TestEmitEvent_HookPanicDoesNotCrashProcess guards EmitEvent's per-hook
// goroutine: a panicking hook must not take down the whole process, and
// well-behaved hooks registered alongside it must still run.
func TestEmitEvent_HookPanicDoesNotCrashProcess(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	goodHookCalled := make(chan struct{}, 1)
	oh.RegisterHook("panicky", func(event *MetricEvent) error {
		panic("boom")
	})
	oh.RegisterHook("good", func(event *MetricEvent) error {
		goodHookCalled <- struct{}{}
		return nil
	})

	oh.EmitEvent(&MetricEvent{Type: "rollout_start", Service: "s1"})

	select {
	case <-goodHookCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("well-behaved hook was not called (panicking hook may have crashed the process/goroutine group)")
	}
}

func TestMultipleHooks(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	var mu sync.Mutex
	var callCount int

	for i := 0; i < 5; i++ {
		oh.RegisterHook("hook"+string(rune(i)), func(event *MetricEvent) error {
			mu.Lock()
			callCount++
			mu.Unlock()
			return nil
		})
	}

	oh.EmitEvent(&MetricEvent{Type: "test", Service: "s1"})

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if callCount != 5 {
		t.Errorf("hooks called = %d, want 5", callCount)
	}
	mu.Unlock()
}

func TestEmitEventConcurrency(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			oh.EmitEvent(&MetricEvent{
				Type:    "rollout_start",
				Service: "s1",
				Status:  "ok",
			})
		}(i)
	}

	wg.Wait()

	oh.mu.RLock()
	if len(oh.metrics.Events) != 20 {
		t.Errorf("events recorded = %d, want 20", len(oh.metrics.Events))
	}
	oh.mu.RUnlock()
}

func TestObservabilityIntegration(t *testing.T) {
	log := zap.NewNop()
	oh := NewObservabilityHooks(log)

	events := []struct {
		eventType string
		service   string
		duration  time.Duration
	}{
		{"rollout_start", "service1", 0},
		{"health_check_passed", "service1", 100 * time.Millisecond},
		{"rollout_success", "service1", 2 * time.Second},
	}

	for _, evt := range events {
		oh.EmitEvent(&MetricEvent{
			Type:     evt.eventType,
			Service:  evt.service,
			Duration: evt.duration,
		})
	}

	snapshot := oh.GetMetricsSnapshot()

	if snapshot.TotalRollouts != 1 {
		t.Errorf("total rollouts = %d, want 1", snapshot.TotalRollouts)
	}
	if snapshot.SuccessfulRollouts != 1 {
		t.Errorf("successful rollouts = %d, want 1", snapshot.SuccessfulRollouts)
	}
	if snapshot.HealthChecksPassed != 1 {
		t.Errorf("health checks passed = %d, want 1", snapshot.HealthChecksPassed)
	}
}
