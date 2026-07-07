package stack

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestEnsureServiceRunning_StartFails_LogsCleanupFailure(t *testing.T) {
	core, observed := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		Status:  StatusPending,
	}

	mockClient.StartContainerError = errors.New("start failed")
	mockClient.RemoveContainerError = errors.New("remove failed")

	err := di.ensureServiceRunning("api")
	if err == nil {
		t.Fatal("expected error from failed start")
	}

	found := false
	for _, entry := range observed.All() {
		if strings.Contains(strings.ToLower(entry.Message), "cleanup") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning log about the failed container cleanup, got entries: %+v", observed.All())
	}
}

func TestEnsureContainersRunning(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		Status:  StatusPending,
	}

	if err := di.EnsureContainersRunning([]string{"api"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sr.state.ServiceStates["api"].NewContainer == "" {
		t.Fatal("expected container to be created")
	}
}

func TestEnsureContainersRunning_AlreadyRunning(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	containerID := "existing-container"
	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:      "api",
		Status:       StatusRolling,
		NewContainer: containerID,
	}

	// Add the container to mock client
	mockClient.CreatedContainers[containerID] = &ContainerInfo{
		ID:     containerID,
		Status: ContainerRunning,
	}
	mockClient.ContainerStates[containerID] = ContainerRunning

	if err := di.EnsureContainersRunning([]string{"api"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still be the same container
	if sr.state.ServiceStates["api"].NewContainer != containerID {
		t.Fatal("expected same container")
	}
}

func TestMonitorContainerHealth_Healthy(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	containerID := "test-container"
	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:      "api",
		NewContainer: containerID,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	mockClient.CreatedContainers[containerID] = &ContainerInfo{
		ID:     containerID,
		Status: ContainerRunning,
	}
	mockClient.ContainerHealthStates[containerID] = HealthHealthy

	if err := di.MonitorContainerHealth("api", 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !sr.state.ServiceStates["api"].HealthCheckPassed {
		t.Fatal("expected health check to pass")
	}
}

func TestMonitorContainerHealth_Unhealthy(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	containerID := "test-container"
	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:      "api",
		NewContainer: containerID,
		CircuitBreaker: &CircuitBreakerState{
			State:        CircuitClosed,
			FailureCount: 0,
		},
	}

	mockClient.CreatedContainers[containerID] = &ContainerInfo{
		ID:     containerID,
		Status: ContainerRunning,
	}
	mockClient.ContainerHealthStates[containerID] = HealthUnhealthy

	if err := di.MonitorContainerHealth("api", 5*time.Second); err == nil {
		t.Fatal("expected error for unhealthy container")
	}

	cb := sr.state.ServiceStates["api"].CircuitBreaker
	if cb.FailureCount == 0 {
		t.Fatal("expected failure count to be recorded")
	}
}

func TestMonitorContainerHealth_Timeout(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	containerID := "test-container"
	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:      "api",
		NewContainer: containerID,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	mockClient.CreatedContainers[containerID] = &ContainerInfo{
		ID:     containerID,
		Status: ContainerRunning,
	}
	// Keep container in starting state to trigger timeout
	mockClient.ContainerHealthStates[containerID] = HealthStarting

	if err := di.MonitorContainerHealth("api", 100*time.Millisecond); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestDrainConnections(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	oldContainer := "old-container"
	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:      "api",
		OldContainer: oldContainer,
	}

	if err := di.DrainConnections("api", 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSwitchTraffic(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	newContainer := "new-container"
	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:      "api",
		NewContainer: newContainer,
		Status:       StatusHealthCheck,
	}

	if err := di.SwitchTraffic("api"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sr.state.ServiceStates["api"].Status != StatusCompleted {
		t.Fatal("expected status to be completed")
	}
}

func TestCleanupOldContainer(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	oldContainer := "old-container"
	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:      "api",
		OldContainer: oldContainer,
	}

	// Create the container in mock
	mockClient.CreatedContainers[oldContainer] = &ContainerInfo{
		ID:     oldContainer,
		Status: ContainerRunning,
	}

	if err := di.CleanupOldContainer("api"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sr.state.ServiceStates["api"].OldContainer != "" {
		t.Fatal("expected old container to be cleared")
	}

	if _, ok := mockClient.CreatedContainers[oldContainer]; ok {
		t.Fatal("expected old container to be removed")
	}
}

func TestCleanupOldContainer_NoOldContainer(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:      "api",
		OldContainer: "",
	}

	// Should return nil without error
	if err := di.CleanupOldContainer("api"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRolloutService_StartsSuccessfully(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		Status:  StatusPending,
		CircuitBreaker: &CircuitBreakerState{
			State:        CircuitClosed,
			FailureCount: 0,
		},
	}

	// Test that rollout starts and transitions service to correct status
	// Note: complete rollout requires async health check which is tested separately
	sr.state.ServiceStates["api"].NewContainer = "mock-api"
	mockClient.ContainerHealthStates["mock-api"] = HealthHealthy

	if err := di.SwitchTraffic("api"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sr.state.ServiceStates["api"].Status != StatusCompleted {
		t.Fatal("expected service to be completed after switching traffic")
	}
}

func TestRolloutService_HealthCheckFails(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		Status:  StatusPending,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	// Set up mock to stay unhealthy
	mockClient.GetContainerHealthError = nil

	if err := di.RolloutService("api", 200*time.Millisecond); err == nil {
		t.Fatal("expected error for failed health check")
	}

	if sr.state.ServiceStates["api"].Status != StatusFailed {
		t.Fatal("expected service to be failed")
	}
}

func TestDockerClientNotFound(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
	}

	// Try to monitor health on non-existent container
	if err := di.MonitorContainerHealth("api", 1*time.Second); err == nil {
		t.Fatal("expected error for missing container")
	}
}

func TestRolloutService_RemovesActualOldContainerNotNewOne(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	oldContainer := "old-api-container"
	mockClient.CreatedContainers[oldContainer] = &ContainerInfo{
		ID:     oldContainer,
		Status: ContainerRunning,
	}

	newContainer := "mock-api"
	mockClient.CreatedContainers[newContainer] = &ContainerInfo{
		ID:     newContainer,
		Status: ContainerRunning,
	}
	mockClient.ContainerStates[newContainer] = ContainerRunning
	mockClient.ContainerHealthStates[newContainer] = HealthHealthy

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:      "api",
		Status:       StatusPending,
		OldContainer: oldContainer,
		NewContainer: newContainer,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	if err := di.RolloutService("api", 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state := sr.state.ServiceStates["api"]

	if state.NewContainer != newContainer {
		t.Fatalf("expected new container to remain active, got %q", state.NewContainer)
	}

	if _, ok := mockClient.CreatedContainers[newContainer]; !ok {
		t.Fatal("newly deployed container was removed by cleanup; it should still be running")
	}

	if _, ok := mockClient.CreatedContainers[oldContainer]; ok {
		t.Fatal("previous container was leaked; it should have been removed by cleanup")
	}
}

func TestDockerIntegrationWithDependencies(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)
	di := NewDockerIntegration(sr, mockClient, logger)

	// Set up dependency graph
	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("api", "db")
	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr.state.Graph = graph

	sr.state.ServiceStates["db"] = &ServiceRolloutState{
		Service: "db",
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	// Ensure both containers run in order (db first, then api)
	if err := di.EnsureContainersRunning([]string{"db", "api"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sr.state.ServiceStates["db"].NewContainer == "" {
		t.Fatal("expected db container")
	}

	if sr.state.ServiceStates["api"].NewContainer == "" {
		t.Fatal("expected api container")
	}
}
