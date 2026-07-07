package stack

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Metrics tracks rollout statistics and performance metrics.
type Metrics struct {
	// Operation counters
	TotalRollouts      atomic.Int64
	SuccessfulRollouts atomic.Int64
	FailedRollouts     atomic.Int64
	RolledBackRollouts atomic.Int64

	// Health check metrics
	HealthChecksPassed  atomic.Int64
	HealthChecksFailed  atomic.Int64
	HealthChecksTimeout atomic.Int64

	// Circuit breaker metrics
	CircuitsOpened atomic.Int64
	CircuitsClosed atomic.Int64

	// Timing metrics
	RolloutTimes     []time.Duration
	HealthCheckTimes []time.Duration
	mu               sync.RWMutex

	// Event tracking
	Events    []MetricEvent
	maxEvents int
}

// MetricEvent represents a single observable event.
type MetricEvent struct {
	Timestamp time.Time
	Type      string // "rollout_start", "health_check", "circuit_open", etc.
	Service   string
	Status    string // "success", "failure", "timeout"
	Duration  time.Duration
	Details   map[string]interface{}
}

// ObservabilityHooks provides hooks for external observability systems.
type ObservabilityHooks struct {
	mu      sync.RWMutex
	hooks   map[string]ObservabilityHook
	metrics *Metrics
	log     *zap.Logger

	// Trace context
	traceContext context.Context
	tracerFunc   func(ctx context.Context, name string) (context.Context, func())
}

// ObservabilityHook is a callback for observable events.
type ObservabilityHook func(event *MetricEvent) error

// NewMetrics creates a new metrics collector.
func NewMetrics() *Metrics {
	return &Metrics{
		RolloutTimes:     make([]time.Duration, 0),
		HealthCheckTimes: make([]time.Duration, 0),
		Events:           make([]MetricEvent, 0),
		maxEvents:        1000,
	}
}

// NewObservabilityHooks creates a new observability hooks manager.
func NewObservabilityHooks(log *zap.Logger) *ObservabilityHooks {
	if log == nil {
		log = zap.NewNop()
	}

	return &ObservabilityHooks{
		hooks:        make(map[string]ObservabilityHook),
		metrics:      NewMetrics(),
		log:          log,
		traceContext: context.Background(),
	}
}

// RegisterHook registers a hook for observability events.
func (oh *ObservabilityHooks) RegisterHook(name string, hook ObservabilityHook) {
	oh.mu.Lock()
	defer oh.mu.Unlock()

	oh.hooks[name] = hook
	oh.log.Debug("observability hook registered",
		zap.String("hook_name", name))
}

// UnregisterHook unregisters a hook.
func (oh *ObservabilityHooks) UnregisterHook(name string) {
	oh.mu.Lock()
	defer oh.mu.Unlock()

	delete(oh.hooks, name)
	oh.log.Debug("observability hook unregistered",
		zap.String("hook_name", name))
}

// EmitEvent emits an observable event to all registered hooks.
func (oh *ObservabilityHooks) EmitEvent(event *MetricEvent) {
	oh.mu.RLock()
	hooks := make(map[string]ObservabilityHook)
	for name, hook := range oh.hooks {
		hooks[name] = hook
	}
	oh.mu.RUnlock()

	// Record event in metrics
	oh.recordEvent(event)

	// Call hooks asynchronously
	for name, hook := range hooks {
		go func(hookName string, hookFunc ObservabilityHook) {
			defer func() {
				if r := recover(); r != nil {
					oh.log.Error("hook panicked",
						zap.String("hook", hookName),
						zap.Any("panic", r))
				}
			}()
			if err := hookFunc(event); err != nil {
				oh.log.Warn("hook execution failed",
					zap.String("hook", hookName),
					zap.Error(err))
			}
		}(name, hook)
	}
}

// recordEvent records an event in metrics history.
func (oh *ObservabilityHooks) recordEvent(event *MetricEvent) {
	oh.mu.Lock()
	defer oh.mu.Unlock()

	oh.metrics.Events = append(oh.metrics.Events, *event)

	// Trim if too many events
	if len(oh.metrics.Events) > oh.metrics.maxEvents {
		oh.metrics.Events = oh.metrics.Events[1:]
	}

	// Update counters based on event type
	switch event.Type {
	case "rollout_start":
		oh.metrics.TotalRollouts.Add(1)

	case "rollout_success":
		oh.metrics.SuccessfulRollouts.Add(1)
		if event.Duration > 0 {
			oh.metrics.RolloutTimes = append(oh.metrics.RolloutTimes, event.Duration)
		}

	case "rollout_failure":
		oh.metrics.FailedRollouts.Add(1)

	case "rollout_rollback":
		oh.metrics.RolledBackRollouts.Add(1)

	case "health_check_passed":
		oh.metrics.HealthChecksPassed.Add(1)
		if event.Duration > 0 {
			oh.metrics.HealthCheckTimes = append(oh.metrics.HealthCheckTimes, event.Duration)
		}

	case "health_check_failed":
		oh.metrics.HealthChecksFailed.Add(1)

	case "health_check_timeout":
		oh.metrics.HealthChecksTimeout.Add(1)

	case "circuit_opened":
		oh.metrics.CircuitsOpened.Add(1)

	case "circuit_closed":
		oh.metrics.CircuitsClosed.Add(1)
	}
}

// GetMetricsSnapshot returns a point-in-time snapshot of metrics.
func (oh *ObservabilityHooks) GetMetricsSnapshot() MetricsSnapshot {
	oh.mu.RLock()
	defer oh.mu.RUnlock()

	snapshot := MetricsSnapshot{
		Timestamp:           time.Now(),
		TotalRollouts:       oh.metrics.TotalRollouts.Load(),
		SuccessfulRollouts:  oh.metrics.SuccessfulRollouts.Load(),
		FailedRollouts:      oh.metrics.FailedRollouts.Load(),
		RolledBackRollouts:  oh.metrics.RolledBackRollouts.Load(),
		HealthChecksPassed:  oh.metrics.HealthChecksPassed.Load(),
		HealthChecksFailed:  oh.metrics.HealthChecksFailed.Load(),
		HealthChecksTimeout: oh.metrics.HealthChecksTimeout.Load(),
		CircuitsOpened:      oh.metrics.CircuitsOpened.Load(),
		CircuitsClosed:      oh.metrics.CircuitsClosed.Load(),
	}

	// Calculate averages
	if len(oh.metrics.RolloutTimes) > 0 {
		sum := time.Duration(0)
		for _, d := range oh.metrics.RolloutTimes {
			sum += d
		}
		snapshot.AvgRolloutTime = sum / time.Duration(len(oh.metrics.RolloutTimes))
	}

	if len(oh.metrics.HealthCheckTimes) > 0 {
		sum := time.Duration(0)
		for _, d := range oh.metrics.HealthCheckTimes {
			sum += d
		}
		snapshot.AvgHealthCheckTime = sum / time.Duration(len(oh.metrics.HealthCheckTimes))
	}

	// Calculate success rate
	if snapshot.TotalRollouts > 0 {
		snapshot.SuccessRate = float64(snapshot.SuccessfulRollouts) / float64(snapshot.TotalRollouts) * 100
	}

	return snapshot
}

// StartTrace starts a trace span for an operation.
func (oh *ObservabilityHooks) StartTrace(ctx context.Context, operation string) (context.Context, func()) {
	if oh.tracerFunc != nil {
		return oh.tracerFunc(ctx, operation)
	}

	// Default: just return context and no-op closer
	return ctx, func() {}
}

// SetTracerFunc sets the tracer function for span creation.
func (oh *ObservabilityHooks) SetTracerFunc(tracerFunc func(ctx context.Context, name string) (context.Context, func())) {
	oh.mu.Lock()
	defer oh.mu.Unlock()

	oh.tracerFunc = tracerFunc
}

// GetEventHistory returns recent events matching a filter.
func (oh *ObservabilityHooks) GetEventHistory(eventType string, limit int) []MetricEvent {
	oh.mu.RLock()
	defer oh.mu.RUnlock()

	result := make([]MetricEvent, 0)

	// Iterate from end to start (most recent first)
	for i := len(oh.metrics.Events) - 1; i >= 0 && len(result) < limit; i-- {
		event := oh.metrics.Events[i]
		if eventType == "" || event.Type == eventType {
			result = append(result, event)
		}
	}

	return result
}

// MetricsSnapshot is a point-in-time view of metrics.
type MetricsSnapshot struct {
	Timestamp           time.Time
	TotalRollouts       int64
	SuccessfulRollouts  int64
	FailedRollouts      int64
	RolledBackRollouts  int64
	HealthChecksPassed  int64
	HealthChecksFailed  int64
	HealthChecksTimeout int64
	CircuitsOpened      int64
	CircuitsClosed      int64
	AvgRolloutTime      time.Duration
	AvgHealthCheckTime  time.Duration
	SuccessRate         float64
}

// String returns a human-readable summary of metrics.
func (ms MetricsSnapshot) String() string {
	return fmt.Sprintf(
		"Metrics[time=%s total=%d success=%d failed=%d rollback=%d healthpass=%d healthfail=%d circuits_open=%d success_rate=%.1f%% avg_rollout=%v]",
		ms.Timestamp.Format("15:04:05"),
		ms.TotalRollouts,
		ms.SuccessfulRollouts,
		ms.FailedRollouts,
		ms.RolledBackRollouts,
		ms.HealthChecksPassed,
		ms.HealthChecksFailed,
		ms.CircuitsOpened,
		ms.SuccessRate,
		ms.AvgRolloutTime,
	)
}

// PrometheusHook returns a hook that exports metrics to Prometheus format.
func PrometheusHook() ObservabilityHook {
	return func(event *MetricEvent) error {
		// In real implementation, would increment Prometheus counters
		// e.g., prometheus.ExampleCounter.With(...).Inc()
		return nil
	}
}

// JSONLogHook returns a hook that logs events as JSON.
func JSONLogHook(log *zap.Logger) ObservabilityHook {
	return func(event *MetricEvent) error {
		log.Info("observable event",
			zap.String("type", event.Type),
			zap.String("service", event.Service),
			zap.String("status", event.Status),
			zap.Duration("duration", event.Duration),
		)
		return nil
	}
}
