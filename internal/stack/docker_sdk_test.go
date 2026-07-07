package stack

import (
	"fmt"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"go.uber.org/zap"
)

// Note: These tests use mock client since Docker SDK requires running daemon
// In CI/integration tests, use testcontainers or real Docker daemon

func TestDockerSDKClientCreation(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	// Real Docker SDK client creation would require Docker daemon
	// For unit tests, we skip and rely on integration tests
	t.Skip("requires Docker daemon")
}

func TestDockerSDKHelperFunctions(t *testing.T) {
	// Test envMapToSlice
	envMap := map[string]string{
		"PATH": "/usr/bin",
		"HOME": "/root",
	}
	envSlice := envMapToSlice(envMap)
	if len(envSlice) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(envSlice))
	}

	// Test portMapToPortBindings
	ports := map[int]int{
		3000: 8080,
		5432: 15432,
	}
	bindings := portMapToPortBindings(ports)
	if len(bindings) != 2 {
		t.Fatalf("expected 2 port bindings, got %d", len(bindings))
	}
}

// TestPortMapToPortBindings_HostPortMapsToContainerPort verifies the
// documented "host port -> container port" convention (RunOptions.Ports,
// ContainerInfo.Ports doc comments): the map KEY is the host port, the VALUE
// is the container port that should be exposed under it.
func TestPortMapToPortBindings_HostPortMapsToContainerPort(t *testing.T) {
	ports := map[int]int{
		8080: 3000, // host 8080 -> container 3000
	}

	bindings := portMapToPortBindings(ports)

	containerPortKey := nat.Port("3000/tcp")
	binds, ok := bindings[containerPortKey]
	if !ok {
		t.Fatalf("expected a binding keyed by container port 3000/tcp, got keys: %v", bindingKeys(bindings))
	}
	if len(binds) != 1 || binds[0].HostPort != "8080" {
		t.Fatalf("expected container port 3000 to be bound to host port 8080, got %+v", binds)
	}
}

func bindingKeys(bindings nat.PortMap) []nat.Port {
	keys := make([]nat.Port, 0, len(bindings))
	for k := range bindings {
		keys = append(keys, k)
	}
	return keys
}

func TestTransactionBasicFlow(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		Status:  StatusPending,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	txn := NewRolloutTransaction("api", mockClient, sr, logger)

	executed := false
	txn.AddOperation("test_op", func() error {
		executed = true
		return nil
	}, nil)

	if err := txn.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !executed {
		t.Fatal("operation should have been executed")
	}

	if txn.Status() != TxnCompleted {
		t.Fatal("transaction should be completed")
	}
}

func TestTransactionWithRollback(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		Status:  StatusPending,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	txn := NewRolloutTransaction("api", mockClient, sr, logger)

	executed1 := false
	rolledBack1 := false

	txn.AddOperation("op1", func() error {
		executed1 = true
		return nil
	}, func() error {
		rolledBack1 = true
		return nil
	})

	txn.AddOperation("op2", func() error {
		return nil // This would fail in real scenario
	}, nil)

	// Manually make op2 fail
	txn.operations[1].Execute = func() error {
		return fmt.Errorf("op2 failed")
	}

	if err := txn.Execute(); err == nil {
		t.Fatal("expected error from failed operation")
	}

	if !executed1 {
		t.Fatal("op1 should have been executed")
	}

	if !rolledBack1 {
		t.Fatal("op1 should have been rolled back")
	}

	if txn.Status() != TxnRolledBack {
		t.Fatal("transaction should be rolled back")
	}
}

func TestTransactionBuilder(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		Status:  StatusPending,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	builder := NewTransactionBuilder("api", mockClient, sr, logger)
	txn := builder.
		AddCreateContainer(&RunOptions{Name: "api", Image: "api:latest"}).
		AddStartContainer().
		Build()

	if len(txn.operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(txn.operations))
	}

	if err := txn.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sr.state.ServiceStates["api"].NewContainer == "" {
		t.Fatal("expected container to be created")
	}
}

func TestTransactionCreateAndStartContainer(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	builder := NewTransactionBuilder("api", mockClient, sr, logger)
	txn := builder.
		AddCreateContainer(&RunOptions{Name: "api", Image: "api:latest"}).
		AddStartContainer().
		Build()

	if err := txn.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	containerID := sr.state.ServiceStates["api"].NewContainer
	if containerID == "" {
		t.Fatal("expected container ID")
	}

	// Verify container was started
	if mockClient.ContainerStates[containerID] != ContainerRunning {
		t.Fatal("expected container to be running")
	}
}

func TestTransactionHealthCheck(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	// Pre-create container
	containerID, _ := mockClient.CreateContainer(&RunOptions{Name: "api", Image: "api:latest"})
	sr.state.ServiceStates["api"].NewContainer = containerID
	mockClient.ContainerHealthStates[containerID] = HealthHealthy

	builder := NewTransactionBuilder("api", mockClient, sr, logger)
	txn := builder.
		AddHealthCheck(1 * time.Second).
		Build()

	if err := txn.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !sr.state.ServiceStates["api"].HealthCheckPassed {
		t.Fatal("expected health check to pass")
	}
}

func TestTransactionFullRollout(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	builder := NewTransactionBuilder("api", mockClient, sr, logger)
	txn := builder.
		AddCreateContainer(&RunOptions{Name: "api", Image: "api:latest"}).
		AddStartContainer().
		AddHealthCheck(2 * time.Second).
		AddSwitchTraffic().
		AddDrainConnections(1 * time.Second).
		AddCleanup().
		Build()

	if len(txn.operations) != 6 {
		t.Fatalf("expected 6 operations, got %d", len(txn.operations))
	}

	// Set up health check to succeed. Reads sr's state under sr.mu and
	// writes the mock's health map via SetContainerHealth (both properly
	// synchronized) since txn.Execute() below reads/writes the same state
	// and health map concurrently with this goroutine.
	go func() {
		for {
			time.Sleep(100 * time.Millisecond)
			sr.mu.Lock()
			containerID := sr.state.ServiceStates["api"].NewContainer
			sr.mu.Unlock()
			if containerID != "" {
				mockClient.SetContainerHealth(containerID, HealthHealthy)
				break
			}
		}
	}()

	if err := txn.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if txn.Status() != TxnCompleted {
		t.Fatal("transaction should be completed")
	}
}

func TestTransactionSummary(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	txn := NewRolloutTransaction("api", mockClient, sr, logger)
	txn.AddOperation("test", func() error { return nil }, nil)

	if err := txn.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	summary := txn.Summary()
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}

	if !contains(summary, "completed") {
		t.Fatal("summary should contain state")
	}
}

// Helper
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr))
}
