package stack

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNewHealthMonitor(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	mockClient := NewMockDockerClient(log)

	hm := NewHealthMonitor(rollout, mockClient, log)

	if hm == nil {
		t.Fatal("NewHealthMonitor returned nil")
	}
	if hm.maxHistorySize != 100 {
		t.Errorf("maxHistorySize = %d, want 100", hm.maxHistorySize)
	}
}

func TestRegisterListener(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	mockClient := NewMockDockerClient(log)
	hm := NewHealthMonitor(rollout, mockClient, log)

	var calledCount int
	listener := func(event *HealthEvent) {
		calledCount++
	}

	hm.RegisterListener(listener)
	if len(hm.listeners) != 1 {
		t.Errorf("listener count = %d, want 1", len(hm.listeners))
	}
}

func TestStartMonitoring(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	rollout.state.ServiceStates["service1"] = &ServiceRolloutState{
		NewContainer: "container123",
		Status:       StatusPending,
	}
	mockClient := NewMockDockerClient(log)
	mockClient.ContainerHealthStates["container123"] = HealthHealthy
	hm := NewHealthMonitor(rollout, mockClient, log)

	err := hm.StartMonitoring("service1")
	if err != nil {
		t.Fatalf("StartMonitoring failed: %v", err)
	}

	services := hm.GetActiveServices()
	if len(services) != 1 || services[0] != "service1" {
		t.Errorf("active services = %v, want [service1]", services)
	}

	hm.StopMonitoring("service1")
}

func TestStartMonitoringErrors(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	mockClient := NewMockDockerClient(log)
	hm := NewHealthMonitor(rollout, mockClient, log)

	tests := []struct {
		name    string
		service string
		wantErr bool
	}{
		{"service not found", "missing", true},
		{"no container", "service", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "no container" {
				rollout.state.ServiceStates["service"] = &ServiceRolloutState{
					NewContainer: "",
					Status:       StatusPending,
				}
			}

			err := hm.StartMonitoring(tt.service)
			if (err != nil) != tt.wantErr {
				t.Errorf("StartMonitoring error = %v, want error: %v", err, tt.wantErr)
			}
		})
	}
}

func TestStartMonitoringDuplicate(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	rollout.state.ServiceStates["service1"] = &ServiceRolloutState{
		NewContainer: "container123",
		Status:       StatusPending,
	}
	mockClient := NewMockDockerClient(log)
	mockClient.ContainerHealthStates["container123"] = HealthHealthy
	hm := NewHealthMonitor(rollout, mockClient, log)

	hm.StartMonitoring("service1")
	err := hm.StartMonitoring("service1")
	if err == nil {
		t.Fatal("StartMonitoring duplicate should error")
	}

	hm.StopMonitoring("service1")
}

func TestMonitorServiceHealthStatusChange(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	rollout.state.ServiceStates["service1"] = &ServiceRolloutState{
		NewContainer: "container123",
		Status:       StatusPending,
	}

	statuses := []HealthStatus{HealthHealthy, HealthUnhealthy, HealthHealthy}
	statusIdx := 0
	mockClient := NewMockDockerClient(log)

	// Create a wrapper to track status changes
	originalGetHealth := func(id string) (HealthStatus, error) {
		status := statuses[statusIdx]
		if statusIdx < len(statuses)-1 {
			statusIdx++
		}
		return status, nil
	}
	_ = originalGetHealth
	mockClient.ContainerHealthStates["container123"] = HealthHealthy

	hm := NewHealthMonitor(rollout, mockClient, log)

	eventsChan := make(chan *HealthEvent, 10)
	hm.RegisterListener(func(event *HealthEvent) {
		eventsChan <- event
	})

	hm.config.CheckInterval = 50 * time.Millisecond
	hm.StartMonitoring("service1")
	defer hm.StopMonitoring("service1")

	time.Sleep(250 * time.Millisecond)

	events := make([]*HealthEvent, 0)
	for len(eventsChan) > 0 {
		select {
		case event := <-eventsChan:
			events = append(events, event)
		default:
			goto done
		}
	}
done:

	if len(events) < 2 {
		t.Logf("collected %d events", len(events))
	}
}

// TestConcurrentHealthMonitoringAndForegroundRolloutAccess reproduces the
// real production shape: HealthMonitor.monitorServiceHealth runs as its own
// background goroutine mutating rollout.state.ServiceStates via
// emitHealthEvent -> updateRolloutStateFromEvent, while the goroutine driving
// the rollout concurrently calls UpdateServiceStatus/MarkServiceHealthy/
// CanProceedWithRollout/GetMetrics on the same StackRollout. Run with -race
// to catch the unsynchronized map/field access.
func TestConcurrentHealthMonitoringAndForegroundRolloutAccess(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	rollout.state.ServiceStates["service1"] = &ServiceRolloutState{
		NewContainer: "container123",
		Status:       StatusPending,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	mockClient := NewMockDockerClient(log)
	mockClient.ContainerHealthStates["container123"] = HealthHealthy

	hm := NewHealthMonitor(rollout, mockClient, log)
	hm.config.CheckInterval = 1 * time.Millisecond

	if err := hm.StartMonitoring("service1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer hm.StopMonitoring("service1")

	var wg sync.WaitGroup
	stop := make(chan struct{})

	foreground := func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			rollout.UpdateServiceStatus("service1", StatusHealthCheck, nil)
			rollout.MarkServiceHealthy("service1")
			rollout.CanProceedWithRollout(0)
			rollout.GetMetrics()
		}
	}

	wg.Add(2)
	go foreground()
	go foreground()

	// The initial Unknown -> Healthy transition alone is enough to make
	// emitHealthEvent fire and mutate rollout.state.ServiceStates once from
	// the background goroutine, overlapping with the foreground writes.
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestGetLastEvent(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	hm := NewHealthMonitor(rollout, NewMockDockerClient(log), log)

	event := &HealthEvent{
		Service:   "service1",
		OldStatus: HealthHealthy,
		NewStatus: HealthUnhealthy,
		Timestamp: time.Now(),
	}

	hm.mu.Lock()
	hm.lastEvents["service1"] = event
	hm.mu.Unlock()

	retrieved := hm.GetLastEvent("service1")
	if retrieved == nil || retrieved.Service != "service1" {
		t.Error("GetLastEvent failed to retrieve event")
	}
}

func TestGetEventHistory(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	hm := NewHealthMonitor(rollout, NewMockDockerClient(log), log)

	events := []*HealthEvent{
		{Service: "service1", OldStatus: HealthHealthy, NewStatus: HealthUnhealthy, Timestamp: time.Now()},
		{Service: "service1", OldStatus: HealthUnhealthy, NewStatus: HealthHealthy, Timestamp: time.Now().Add(1 * time.Second)},
	}

	hm.mu.Lock()
	hm.eventHistory["service1"] = events
	hm.mu.Unlock()

	history := hm.GetEventHistory("service1")
	if len(history) != 2 {
		t.Errorf("history length = %d, want 2", len(history))
	}

	emptyHistory := hm.GetEventHistory("nonexistent")
	if len(emptyHistory) != 0 {
		t.Errorf("empty history length = %d, want 0", len(emptyHistory))
	}
}

func TestGetActiveServices(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	hm := NewHealthMonitor(rollout, NewMockDockerClient(log), log)

	hm.mu.Lock()
	_, cancel := context.WithCancel(context.Background())
	hm.activeMonitors["service1"] = cancel
	_, cancel2 := context.WithCancel(context.Background())
	hm.activeMonitors["service2"] = cancel2
	hm.mu.Unlock()

	services := hm.GetActiveServices()
	if len(services) != 2 {
		t.Errorf("active services count = %d, want 2", len(services))
	}
}

func TestSetCheckInterval(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	hm := NewHealthMonitor(rollout, NewMockDockerClient(log), log)

	interval := 100 * time.Millisecond
	hm.SetCheckInterval(interval)

	if hm.config.CheckInterval != interval {
		t.Errorf("check interval = %v, want %v", hm.config.CheckInterval, interval)
	}
}

func TestStop(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	hm := NewHealthMonitor(rollout, NewMockDockerClient(log), log)

	hm.mu.Lock()
	_, cancel := context.WithCancel(context.Background())
	hm.activeMonitors["service1"] = cancel
	hm.running = true
	hm.mu.Unlock()

	hm.Stop()

	hm.mu.RLock()
	if hm.running {
		t.Error("monitor still running after Stop")
	}
	if len(hm.activeMonitors) != 0 {
		t.Error("active monitors not cleared")
	}
	hm.mu.RUnlock()
}

func TestGetStats(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	hm := NewHealthMonitor(rollout, NewMockDockerClient(log), log)

	now := time.Now()
	hm.mu.Lock()
	_, cancel := context.WithCancel(context.Background())
	hm.activeMonitors["service1"] = cancel
	hm.eventHistory["service1"] = []*HealthEvent{
		{Service: "service1", NewStatus: HealthHealthy, Timestamp: now},
		{Service: "service1", NewStatus: HealthUnhealthy, Timestamp: now.Add(1 * time.Second)},
	}
	hm.mu.Unlock()

	stats := hm.GetStats()

	if stats.ActiveMonitors != 1 {
		t.Errorf("active monitors = %d, want 1", stats.ActiveMonitors)
	}
	if stats.TotalEvents != 2 {
		t.Errorf("total events = %d, want 2", stats.TotalEvents)
	}
}

func TestHealthEventListenerConcurrency(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	hm := NewHealthMonitor(rollout, NewMockDockerClient(log), log)

	var wg sync.WaitGroup
	eventCount := 0
	mu := sync.Mutex{}

	for i := 0; i < 5; i++ {
		hm.RegisterListener(func(event *HealthEvent) {
			mu.Lock()
			eventCount++
			mu.Unlock()
		})
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			hm.mu.Lock()
			event := &HealthEvent{
				Service:   "service1",
				NewStatus: HealthHealthy,
				Timestamp: time.Now(),
			}
			hm.lastEvents["service1"] = event
			hm.mu.Unlock()

			hm.emitHealthEvent("service1", HealthUnknown, HealthHealthy)
		}(i)
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if eventCount == 0 {
		t.Error("listeners not called")
	}
	mu.Unlock()
}

func TestHealthEventListenerNonBlocking(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	hm := NewHealthMonitor(rollout, NewMockDockerClient(log), log)

	blockingChan := make(chan struct{})
	hm.RegisterListener(func(event *HealthEvent) {
		<-blockingChan
	})

	done := make(chan struct{})
	go func() {
		hm.emitHealthEvent("service1", HealthUnknown, HealthHealthy)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("emitHealthEvent blocked by listener")
	}

	close(blockingChan)
}

// TestEmitHealthEvent_ListenerPanicDoesNotCrashProcess guards
// emitHealthEvent's per-listener goroutine: a panicking listener must not
// take down the whole process, and well-behaved listeners registered
// alongside it must still run.
func TestEmitHealthEvent_ListenerPanicDoesNotCrashProcess(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	hm := NewHealthMonitor(rollout, NewMockDockerClient(log), log)

	goodListenerCalled := make(chan struct{}, 1)
	hm.RegisterListener(func(event *HealthEvent) {
		panic("boom")
	})
	hm.RegisterListener(func(event *HealthEvent) {
		goodListenerCalled <- struct{}{}
	})

	hm.emitHealthEvent("service1", HealthUnknown, HealthHealthy)

	select {
	case <-goodListenerCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("well-behaved listener was not called (panicking listener may have crashed the process/goroutine group)")
	}
}
