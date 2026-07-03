package stack

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestCheckServiceHealth_SingleService(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["db"] = &ServiceRolloutState{
		Service:           "db",
		Status:            StatusCompleted,
		HealthCheckPassed: true,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	healthy, err := sr.CheckServiceHealth("db", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !healthy {
		t.Fatal("expected service to be healthy")
	}
}

func TestCheckServiceHealth_WithUnhealthyDependency(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("api", "db")
	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr.state.Graph = graph

	sr.state.ServiceStates["db"] = &ServiceRolloutState{
		Service:           "db",
		Status:            StatusCompleted,
		HealthCheckPassed: false,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:           "api",
		Status:            StatusCompleted,
		HealthCheckPassed: true,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	healthy, err := sr.CheckServiceHealth("api", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if healthy {
		t.Fatal("expected api to be unhealthy due to failed dependency")
	}
}

func TestCheckServiceHealth_WithOpenCircuit(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("api", "db")
	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr.state.Graph = graph

	sr.state.ServiceStates["db"] = &ServiceRolloutState{
		Service:           "db",
		Status:            StatusCompleted,
		HealthCheckPassed: true,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitOpen,
		},
	}

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:           "api",
		Status:            StatusCompleted,
		HealthCheckPassed: true,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	healthy, err := sr.CheckServiceHealth("api", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if healthy {
		t.Fatal("expected api to be unhealthy due to open circuit on dependency")
	}
}

func TestRecordHealthCheckFailure_OpenCircuit(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State:        CircuitClosed,
			FailureCount: 0,
		},
	}

	hcConfig := &HealthCheckConfig{
		FailureThreshold: 2,
	}

	sr.RecordHealthCheckFailure("api", hcConfig)
	if sr.state.ServiceStates["api"].CircuitBreaker.State != CircuitClosed {
		t.Fatal("circuit should still be closed after 1 failure")
	}

	sr.RecordHealthCheckFailure("api", hcConfig)
	cb := sr.state.ServiceStates["api"].CircuitBreaker
	if cb.State != CircuitOpen {
		t.Fatalf("expected circuit open, got %s", cb.State)
	}
	if cb.FailureCount != 2 {
		t.Fatalf("expected failure count 2, got %d", cb.FailureCount)
	}
}

func TestRecordHealthCheckSuccess_ResetFailureCount(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State:        CircuitClosed,
			FailureCount: 5,
		},
	}

	sr.RecordHealthCheckSuccess("api", nil)

	cb := sr.state.ServiceStates["api"].CircuitBreaker
	if cb.FailureCount != 0 {
		t.Fatalf("expected failure count 0, got %d", cb.FailureCount)
	}
	if cb.State != CircuitClosed {
		t.Fatalf("expected circuit closed, got %s", cb.State)
	}
}

func TestRecordHealthCheckSuccess_CloseFromOpen(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitOpen,
		},
	}

	sr.RecordHealthCheckSuccess("api", nil)

	cb := sr.state.ServiceStates["api"].CircuitBreaker
	if cb.State != CircuitClosed {
		t.Fatalf("expected circuit closed, got %s", cb.State)
	}
}

func TestIsCircuitOpen(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	if sr.IsCircuitOpen("api") {
		t.Fatal("circuit should be closed")
	}

	sr.state.ServiceStates["api"].CircuitBreaker.State = CircuitOpen
	if !sr.IsCircuitOpen("api") {
		t.Fatal("circuit should be open")
	}
}

func TestCanAttemptHealthCheck_Closed(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	if !sr.CanAttemptHealthCheck("api", nil) {
		t.Fatal("should allow health check when circuit closed")
	}
}

func TestCanAttemptHealthCheck_OpenNotExpired(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State:    CircuitOpen,
			OpenedAt: time.Now(),
		},
	}

	hcConfig := &HealthCheckConfig{
		OpenTimeout: 30 * time.Second,
	}

	if sr.CanAttemptHealthCheck("api", hcConfig) {
		t.Fatal("should not allow health check when circuit just opened")
	}
}

func TestCanAttemptHealthCheck_OpenExpired(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State:    CircuitOpen,
			OpenedAt: time.Now().Add(-31 * time.Second),
		},
	}

	hcConfig := &HealthCheckConfig{
		OpenTimeout: 30 * time.Second,
	}

	if !sr.CanAttemptHealthCheck("api", hcConfig) {
		t.Fatal("should allow health check when open timeout expired")
	}

	// Verify circuit transitioned to half-open
	if sr.state.ServiceStates["api"].CircuitBreaker.State != CircuitHalfOpen {
		t.Fatal("circuit should transition to half-open")
	}
}

func TestGetDependencyHealthStatus(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("cache")
	builder.AddService("api", "db", "cache")
	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr.state.Graph = graph

	sr.state.ServiceStates["db"] = &ServiceRolloutState{
		Service:           "db",
		HealthCheckPassed: true,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	sr.state.ServiceStates["cache"] = &ServiceRolloutState{
		Service:           "cache",
		HealthCheckPassed: false,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	status := sr.GetDependencyHealthStatus("api")

	if status["db"] != true {
		t.Fatal("db should be healthy")
	}
	if status["cache"] != false {
		t.Fatal("cache should be unhealthy")
	}
}

func TestValidateDependencyHealth(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("api", "db")
	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr.state.Graph = graph

	sr.state.ServiceStates["db"] = &ServiceRolloutState{
		Service:           "db",
		HealthCheckPassed: true,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:           "api",
		HealthCheckPassed: true,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	valid, issues := sr.ValidateDependencyHealth("api")
	if !valid {
		t.Fatalf("validation should pass, got issues: %v", issues)
	}
	if len(issues) != 0 {
		t.Fatalf("expected no issues, got %d", len(issues))
	}
}

func TestValidateDependencyHealth_WithIssues(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("api", "db")
	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sr.state.Graph = graph

	sr.state.ServiceStates["db"] = &ServiceRolloutState{
		Service:           "db",
		HealthCheckPassed: false,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitOpen,
		},
	}

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:           "api",
		HealthCheckPassed: true,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	valid, issues := sr.ValidateDependencyHealth("api")
	if valid {
		t.Fatal("validation should fail")
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
}

func TestGetUnhealthyServices(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["db"] = &ServiceRolloutState{
		Service:           "db",
		HealthCheckPassed: true,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service:           "api",
		HealthCheckPassed: false,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitClosed,
		},
	}

	sr.state.ServiceStates["cache"] = &ServiceRolloutState{
		Service:           "cache",
		HealthCheckPassed: true,
		CircuitBreaker: &CircuitBreakerState{
			State: CircuitOpen,
		},
	}

	unhealthy := sr.GetUnhealthyServices()
	if len(unhealthy) != 2 {
		t.Fatalf("expected 2 unhealthy services, got %d", len(unhealthy))
	}
}

func TestCircuitBreakerRecoveryFlow(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["api"] = &ServiceRolloutState{
		Service: "api",
		CircuitBreaker: &CircuitBreakerState{
			State:        CircuitClosed,
			FailureCount: 0,
		},
	}

	hcConfig := &HealthCheckConfig{
		FailureThreshold: 2,
		OpenTimeout:      1 * time.Millisecond,
	}

	// Trigger 2 failures to open circuit
	sr.RecordHealthCheckFailure("api", hcConfig)
	sr.RecordHealthCheckFailure("api", hcConfig)

	cb := sr.state.ServiceStates["api"].CircuitBreaker
	if cb.State != CircuitOpen {
		t.Fatalf("expected circuit open, got %s", cb.State)
	}

	// Wait for timeout and attempt check
	time.Sleep(2 * time.Millisecond)
	if !sr.CanAttemptHealthCheck("api", hcConfig) {
		t.Fatal("should allow check after open timeout")
	}

	// Verify transitioned to half-open
	if sr.state.ServiceStates["api"].CircuitBreaker.State != CircuitHalfOpen {
		t.Fatal("should transition to half-open")
	}

	// Record success to close
	sr.RecordHealthCheckSuccess("api", hcConfig)
	if sr.state.ServiceStates["api"].CircuitBreaker.State != CircuitClosed {
		t.Fatal("should close on success from half-open")
	}
}

func TestDefaultHealthCheckConfig(t *testing.T) {
	config := DefaultHealthCheckConfig()

	if config.FailureThreshold != 3 {
		t.Fatalf("expected failure threshold 3, got %d", config.FailureThreshold)
	}
	if config.SuccessThreshold != 2 {
		t.Fatalf("expected success threshold 2, got %d", config.SuccessThreshold)
	}
	if config.OpenTimeout != 30*time.Second {
		t.Fatalf("expected open timeout 30s, got %v", config.OpenTimeout)
	}
}
