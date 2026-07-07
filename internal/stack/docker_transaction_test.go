package stack

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestTransactionBuilder_SwitchAndCleanup_RemovesActualOldContainerNotNewOne(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	mockClient := NewMockDockerClient(logger)

	oldContainer := "old-txn-container"
	newContainer := "new-txn-container"

	mockClient.CreatedContainers[oldContainer] = &ContainerInfo{ID: oldContainer, Status: ContainerRunning}
	mockClient.CreatedContainers[newContainer] = &ContainerInfo{ID: newContainer, Status: ContainerRunning}
	mockClient.ContainerHealthStates[newContainer] = HealthHealthy

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:      "api",
		Status:       StatusHealthCheck,
		OldContainer: oldContainer,
		NewContainer: newContainer,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	txn := NewTransactionBuilder("api", mockClient, sr, logger).
		AddHealthCheck(5 * time.Second).
		AddSwitchTraffic().
		AddDrainConnections(5 * time.Second).
		AddCleanup().
		Build()

	if err := txn.Execute(); err != nil {
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
