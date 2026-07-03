package stack

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

func TestNewDependencyGraphBuilder(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	if builder == nil {
		t.Fatal("expected non-nil builder")
	}
	if len(builder.services) != 0 {
		t.Fatal("expected empty services map")
	}
}

func TestAddService(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("web", "db", "cache")

	if len(builder.services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(builder.services))
	}

	svc := builder.services["web"]
	if svc.Service != "web" {
		t.Fatalf("expected service 'web', got '%s'", svc.Service)
	}
	if len(svc.DependsOn) != 2 {
		t.Fatalf("expected 2 dependencies, got %d", len(svc.DependsOn))
	}
}

func TestAddServiceWithCondition(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddServiceWithCondition("web", "service_healthy", "db")

	svc := builder.services["web"]
	if svc.Condition != "service_healthy" {
		t.Fatalf("expected condition 'service_healthy', got '%s'", svc.Condition)
	}
}

func TestBuildSimpleGraph(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("web", "db")

	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if graph == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(graph.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(graph.Services))
	}
	if len(graph.Order) != 2 {
		t.Fatalf("expected 2 ordered services, got %d", len(graph.Order))
	}

	// DB should come before web
	if graph.Order[0] != "db" {
		t.Fatalf("expected 'db' first, got '%s'", graph.Order[0])
	}
	if graph.Order[1] != "web" {
		t.Fatalf("expected 'web' second, got '%s'", graph.Order[1])
	}
}

func TestTopologicalSort(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("cache")
	builder.AddService("api", "db")
	builder.AddService("web", "api", "cache")

	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	order := graph.Order
	if len(order) != 4 {
		t.Fatalf("expected 4 services, got %d", len(order))
	}

	// Verify ordering constraints
	dbIdx := -1
	cacheIdx := -1
	apiIdx := -1
	webIdx := -1

	for i, svc := range order {
		switch svc {
		case "db":
			dbIdx = i
		case "cache":
			cacheIdx = i
		case "api":
			apiIdx = i
		case "web":
			webIdx = i
		}
	}

	if dbIdx >= apiIdx {
		t.Fatal("db should come before api")
	}
	if apiIdx >= webIdx {
		t.Fatal("api should come before web")
	}
	if cacheIdx >= webIdx {
		t.Fatal("cache should come before web")
	}
}

func TestCircularDependencyDetection(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("a", "b")
	builder.AddService("b", "a")

	_, err := builder.Build()
	if err == nil {
		t.Fatal("expected error for circular dependency")
	}
}

func TestUndefinedDependency(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("web", "undefined_db")

	_, err := builder.Build()
	if err == nil {
		t.Fatal("expected error for undefined dependency")
	}
}

func TestBuildLevels(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("cache")
	builder.AddService("api", "db")
	builder.AddService("web", "api")

	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	levels := graph.Levels
	if len(levels) < 2 {
		t.Fatalf("expected at least 2 levels, got %d", len(levels))
	}

	// Level 0 should have independent services
	if len(levels[0]) == 0 {
		t.Fatal("expected services in level 0")
	}
}

func TestGetDependents(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("api", "db")
	builder.AddService("web", "db")

	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dependents := graph.GetDependents("db")
	if len(dependents) != 2 {
		t.Fatalf("expected 2 dependents, got %d", len(dependents))
	}
}

func TestGetDependencies(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("cache")
	builder.AddService("web", "db", "cache")

	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deps := graph.GetDependencies("web")
	if len(deps) != 2 {
		t.Fatalf("expected 2 dependencies, got %d", len(deps))
	}
}

func TestGetServiceLevel(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("api", "db")
	builder.AddService("web", "api")

	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dbLevel := graph.GetServiceLevel("db")
	apiLevel := graph.GetServiceLevel("api")
	webLevel := graph.GetServiceLevel("web")

	if dbLevel >= apiLevel {
		t.Fatal("db level should be less than api level")
	}
	if apiLevel >= webLevel {
		t.Fatal("api level should be less than web level")
	}
}

func TestIsRootService(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("api", "db")

	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !graph.IsRootService("db") {
		t.Fatal("db should be a root service")
	}
	if graph.IsRootService("api") {
		t.Fatal("api should not be a root service")
	}
}

func TestIsLeafService(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	builder := NewDependencyGraphBuilder(logger)
	builder.AddService("db")
	builder.AddService("api", "db")

	graph, err := builder.Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if graph.IsLeafService("db") {
		t.Fatal("db should not be a leaf service")
	}
	if !graph.IsLeafService("api") {
		t.Fatal("api should be a leaf service")
	}
}

// ============================================================================
// Stack Rollout Tests
// ============================================================================

func TestNewStackRollout(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{
		ComposeFile: "docker-compose.yml",
	}

	sr := NewStackRollout(config, logger)
	if sr == nil {
		t.Fatal("expected non-nil stack rollout")
	}
	if sr.state == nil {
		t.Fatal("expected non-nil state")
	}
}

func TestValidateStack(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	services := map[string]*ServiceDependency{
		"db": {
			Service:    "db",
			DependsOn:  []string{},
			IsStateful: true,
		},
		"api": {
			Service:        "api",
			DependsOn:      []string{"db"},
			HasHealthCheck: true,
		},
	}

	result := sr.ValidateStack(context.Background(), services)
	if !result.Valid {
		t.Fatalf("validation should pass, got issues: %v", result.Issues)
	}
	if result.ServiceCount != 2 {
		t.Fatalf("expected 2 services, got %d", result.ServiceCount)
	}
	if result.StatefulServices != 1 {
		t.Fatalf("expected 1 stateful service, got %d", result.StatefulServices)
	}
	if result.HealthCheckServices != 1 {
		t.Fatalf("expected 1 health check service, got %d", result.HealthCheckServices)
	}
}

func TestValidateStackWithInvalidDependency(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	services := map[string]*ServiceDependency{
		"api": {
			Service:   "api",
			DependsOn: []string{"undefined_db"},
		},
	}

	result := sr.ValidateStack(context.Background(), services)
	if result.Valid {
		t.Fatal("validation should fail for undefined dependency")
	}
	if len(result.Issues) == 0 {
		t.Fatal("expected issues")
	}
}

func TestBuildDependencyGraph(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	services := map[string]*ServiceDependency{
		"db":  {Service: "db", DependsOn: []string{}},
		"api": {Service: "api", DependsOn: []string{"db"}},
	}

	graph, err := sr.BuildDependencyGraph(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if graph == nil {
		t.Fatal("expected non-nil graph")
	}
	if sr.state.Graph == nil {
		t.Fatal("expected graph to be stored in state")
	}
}

func TestInitializeServiceStates(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	services := map[string]*ServiceDependency{
		"db":  {Service: "db", DependsOn: []string{}},
		"api": {Service: "api", DependsOn: []string{"db"}},
	}

	sr.InitializeServiceStates(services)

	if len(sr.state.ServiceStates) != 2 {
		t.Fatalf("expected 2 service states, got %d", len(sr.state.ServiceStates))
	}

	for _, svc := range []string{"db", "api"} {
		state, ok := sr.state.ServiceStates[svc]
		if !ok {
			t.Fatalf("expected service %s in states", svc)
		}
		if state.Status != StatusPending {
			t.Fatalf("expected pending status, got %s", state.Status)
		}
	}
}

func TestUpdateServiceStatus(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["db"] = &ServiceRolloutState{
		Service: "db",
		Status:  StatusPending,
	}

	sr.UpdateServiceStatus("db", StatusCompleted, nil)

	state := sr.state.ServiceStates["db"]
	if state.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %s", state.Status)
	}
	if len(sr.state.CompletedServices) != 1 {
		t.Fatal("expected service in completed list")
	}
}

func TestMarkServiceHealthy(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["db"] = &ServiceRolloutState{
		Service:           "db",
		HealthCheckPassed: false,
	}

	sr.MarkServiceHealthy("db")

	if !sr.state.ServiceStates["db"].HealthCheckPassed {
		t.Fatal("expected service to be marked healthy")
	}
}

func TestGetMetrics(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	sr.state.ServiceStates["db"] = &ServiceRolloutState{Service: "db"}
	sr.state.ServiceStates["api"] = &ServiceRolloutState{Service: "api"}
	sr.state.CompletedServices = []string{"db"}
	sr.state.FailedServices = []string{}

	metrics := sr.GetMetrics()
	if metrics.TotalServices != 2 {
		t.Fatalf("expected 2 total services, got %d", metrics.TotalServices)
	}
	if metrics.CompletedServices != 1 {
		t.Fatalf("expected 1 completed service, got %d", metrics.CompletedServices)
	}
	if metrics.FailedServices != 0 {
		t.Fatalf("expected 0 failed services, got %d", metrics.FailedServices)
	}
}

func TestCanProceedWithRollout(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)

	// Can always proceed with level 0
	canProceed, msg := sr.CanProceedWithRollout(0)
	if !canProceed {
		t.Fatalf("should be able to proceed with level 0: %s", msg)
	}
}

func TestCannotProceedIfRollbackNeeded(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	config := &StackRolloutConfig{}
	sr := NewStackRollout(config, logger)
	sr.state.NeedsRollback = true

	canProceed, msg := sr.CanProceedWithRollout(0)
	if canProceed {
		t.Fatal("should not be able to proceed if rollback needed")
	}
	if msg == "" {
		t.Fatal("expected error message")
	}
}
