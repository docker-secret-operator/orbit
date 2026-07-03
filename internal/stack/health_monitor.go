package stack

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// HealthEvent represents a health status change event.
type HealthEvent struct {
	Service     string
	OldStatus   HealthStatus
	NewStatus   HealthStatus
	Timestamp   time.Time
	Reason      string
	CircuitOpen bool
}

// HealthEventListener is called when a health event occurs.
type HealthEventListener func(event *HealthEvent)

// HealthMonitor monitors container health using events instead of polling.
type HealthMonitor struct {
	rollout   *StackRollout
	client    DockerClient
	log       *zap.Logger
	listeners []HealthEventListener
	mu        sync.RWMutex

	// Event tracking
	activeMonitors map[string]context.CancelFunc
	lastEvents     map[string]*HealthEvent
	eventHistory   map[string][]*HealthEvent
	maxHistorySize int

	// Health check configuration
	config *HealthCheckConfig

	// State
	running bool
	done    chan struct{}
}

// NewHealthMonitor creates a new event-based health monitor.
func NewHealthMonitor(rollout *StackRollout, client DockerClient, log *zap.Logger) *HealthMonitor {
	if log == nil {
		log = zap.NewNop()
	}

	return &HealthMonitor{
		rollout:        rollout,
		client:         client,
		log:            log,
		listeners:      make([]HealthEventListener, 0),
		activeMonitors: make(map[string]context.CancelFunc),
		lastEvents:     make(map[string]*HealthEvent),
		eventHistory:   make(map[string][]*HealthEvent),
		maxHistorySize: 100,
		config:         DefaultHealthCheckConfig(),
		done:           make(chan struct{}),
	}
}

// RegisterListener registers a callback for health events.
func (hm *HealthMonitor) RegisterListener(listener HealthEventListener) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	hm.listeners = append(hm.listeners, listener)
	hm.log.Debug("health event listener registered",
		zap.Int("listener_count", len(hm.listeners)))
}

// StartMonitoring starts monitoring health for a service.
func (hm *HealthMonitor) StartMonitoring(service string) error {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	if _, exists := hm.activeMonitors[service]; exists {
		return fmt.Errorf("already monitoring service %q", service)
	}

	state, ok := hm.rollout.state.ServiceStates[service]
	if !ok {
		return fmt.Errorf("service %q not found", service)
	}

	if state.NewContainer == "" {
		return fmt.Errorf("no container for service %q", service)
	}

	ctx, cancel := context.WithCancel(context.Background())
	hm.activeMonitors[service] = cancel

	go hm.monitorServiceHealth(ctx, service, state.NewContainer)

	hm.log.Info("started health monitoring",
		zap.String("service", service),
		zap.String("container_id", state.NewContainer))

	return nil
}

// StopMonitoring stops monitoring health for a service.
func (hm *HealthMonitor) StopMonitoring(service string) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	if cancel, exists := hm.activeMonitors[service]; exists {
		cancel()
		delete(hm.activeMonitors, service)
		hm.log.Info("stopped health monitoring",
			zap.String("service", service))
	}
}

// monitorServiceHealth continuously monitors a container's health.
func (hm *HealthMonitor) monitorServiceHealth(ctx context.Context, service string, containerID string) {
	ticker := time.NewTicker(hm.config.CheckInterval)
	defer ticker.Stop()

	previousStatus := HealthUnknown

	for {
		select {
		case <-ctx.Done():
			hm.log.Debug("health monitoring stopped",
				zap.String("service", service))
			return

		case <-ticker.C:
			currentStatus, err := hm.client.GetContainerHealth(containerID)
			if err != nil {
				hm.log.Warn("failed to get health status",
					zap.String("service", service),
					zap.String("container_id", containerID),
					zap.Error(err))
				currentStatus = HealthUnknown
			}

			// Emit event only if status changed
			if currentStatus != previousStatus {
				hm.emitHealthEvent(service, previousStatus, currentStatus)
				previousStatus = currentStatus
			}
		}
	}
}

// emitHealthEvent emits a health status change event.
func (hm *HealthMonitor) emitHealthEvent(service string, oldStatus, newStatus HealthStatus) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	event := &HealthEvent{
		Service:   service,
		OldStatus: oldStatus,
		NewStatus: newStatus,
		Timestamp: time.Now(),
		Reason:    fmt.Sprintf("health status changed from %s to %s", oldStatus, newStatus),
	}

	// Record event in history
	if _, exists := hm.eventHistory[service]; !exists {
		hm.eventHistory[service] = make([]*HealthEvent, 0)
	}

	hm.eventHistory[service] = append(hm.eventHistory[service], event)

	// Trim history if too large
	if len(hm.eventHistory[service]) > hm.maxHistorySize {
		hm.eventHistory[service] = hm.eventHistory[service][1:]
	}

	hm.lastEvents[service] = event

	// Update rollout state based on event
	hm.updateRolloutStateFromEvent(event)

	// Notify listeners
	for _, listener := range hm.listeners {
		go listener(event)
	}

	hm.log.Info("health event emitted",
		zap.String("service", service),
		zap.String("old_status", string(oldStatus)),
		zap.String("new_status", string(newStatus)))
}

// updateRolloutStateFromEvent updates the rollout state based on health events.
func (hm *HealthMonitor) updateRolloutStateFromEvent(event *HealthEvent) {
	state, ok := hm.rollout.state.ServiceStates[event.Service]
	if !ok {
		return
	}

	switch event.NewStatus {
	case HealthHealthy:
		hm.rollout.RecordHealthCheckSuccess(event.Service, hm.config)
		state.HealthCheckPassed = true

	case HealthUnhealthy:
		hm.rollout.RecordHealthCheckFailure(event.Service, hm.config)
		state.HealthCheckPassed = false
		event.CircuitOpen = hm.rollout.IsCircuitOpen(event.Service)

	case HealthStarting:
		// Service is still starting, don't record failure yet
		break
	}
}

// GetLastEvent returns the last health event for a service.
func (hm *HealthMonitor) GetLastEvent(service string) *HealthEvent {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	return hm.lastEvents[service]
}

// GetEventHistory returns the health event history for a service.
func (hm *HealthMonitor) GetEventHistory(service string) []*HealthEvent {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	if history, exists := hm.eventHistory[service]; exists {
		// Return a copy
		result := make([]*HealthEvent, len(history))
		copy(result, history)
		return result
	}

	return make([]*HealthEvent, 0)
}

// GetActiveServices returns all services currently being monitored.
func (hm *HealthMonitor) GetActiveServices() []string {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	services := make([]string, 0, len(hm.activeMonitors))
	for service := range hm.activeMonitors {
		services = append(services, service)
	}

	return services
}

// SetCheckInterval sets the health check interval.
func (hm *HealthMonitor) SetCheckInterval(interval time.Duration) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	hm.config.CheckInterval = interval
	hm.log.Debug("health check interval updated",
		zap.Duration("interval", interval))
}

// Stop stops all health monitoring.
func (hm *HealthMonitor) Stop() {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	for service, cancel := range hm.activeMonitors {
		cancel()
		hm.log.Debug("stopping monitor",
			zap.String("service", service))
	}

	hm.activeMonitors = make(map[string]context.CancelFunc)
	hm.running = false

	hm.log.Info("health monitor stopped")
}

// HealthMonitorStats provides statistics about health monitoring.
type HealthMonitorStats struct {
	ActiveMonitors    int
	TotalEvents       int
	LastEventTime     time.Time
	ServicesHealthy   int
	ServicesUnhealthy int
}

// GetStats returns current monitoring statistics.
func (hm *HealthMonitor) GetStats() HealthMonitorStats {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	stats := HealthMonitorStats{
		ActiveMonitors: len(hm.activeMonitors),
	}

	lastEventTime := time.Time{}
	healthy := 0
	unhealthy := 0

	for _, history := range hm.eventHistory {
		stats.TotalEvents += len(history)

		if len(history) > 0 {
			lastEvent := history[len(history)-1]
			if lastEvent.Timestamp.After(lastEventTime) {
				lastEventTime = lastEvent.Timestamp
			}

			switch lastEvent.NewStatus {
			case HealthHealthy:
				healthy++
			case HealthUnhealthy:
				unhealthy++
			}
		}
	}

	stats.LastEventTime = lastEventTime
	stats.ServicesHealthy = healthy
	stats.ServicesUnhealthy = unhealthy

	return stats
}
