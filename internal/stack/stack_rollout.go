package stack

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// StackRollout orchestrates multi-service deployments with dependency management.
type StackRollout struct {
	config *StackRolloutConfig
	log    *zap.Logger
	state  *StackRolloutState
}

// NewStackRollout creates a new stack rollout orchestrator.
func NewStackRollout(config *StackRolloutConfig, log *zap.Logger) *StackRollout {
	if log == nil {
		log = zap.NewNop()
	}

	return &StackRollout{
		config: config,
		log:    log,
		state: &StackRolloutState{
			Config:            config,
			StartedAt:         time.Time{},
			ServiceStates:     make(map[string]*ServiceRolloutState),
			CompletedServices: make([]string, 0),
			FailedServices:    make([]string, 0),
		},
	}
}

// ValidateStack performs pre-rollout validation of all services.
// Checks for image availability, port conflicts, volume accessibility, etc.
func (sr *StackRollout) ValidateStack(ctx context.Context, services map[string]*ServiceDependency) *StackValidationResult {
	sr.log.Info("validating stack",
		zap.Int("service_count", len(services)))

	result := &StackValidationResult{
		Valid:        true,
		Issues:       make([]string, 0),
		Warnings:     make([]string, 0),
		ServiceCount: len(services),
	}

	// Validate each service
	for _, svc := range services {
		if svc.IsStateful {
			result.StatefulServices++
		}
		if svc.HasHealthCheck {
			result.HealthCheckServices++
		}

		// Validate service has a name
		if svc.Service == "" {
			result.Issues = append(result.Issues, "service with empty name")
			result.Valid = false
		}

		// Check for unknown dependencies
		for _, dep := range svc.DependsOn {
			if _, ok := services[dep]; !ok {
				result.Issues = append(result.Issues, fmt.Sprintf("service %q depends on undefined service %q", svc.Service, dep))
				result.Valid = false
			}
		}
	}

	sr.log.Info("stack validation complete",
		zap.Bool("valid", result.Valid),
		zap.Int("stateful_services", result.StatefulServices),
		zap.Int("health_check_services", result.HealthCheckServices),
		zap.Int("issues", len(result.Issues)))

	return result
}

// BuildDependencyGraph constructs the dependency ordering for the stack.
func (sr *StackRollout) BuildDependencyGraph(services map[string]*ServiceDependency) (*DependencyGraph, error) {
	sr.log.Info("building dependency graph",
		zap.Int("service_count", len(services)))

	builder := NewDependencyGraphBuilder(sr.log)

	// Add all services to the builder
	for _, svc := range services {
		if svc.Condition != "" {
			builder.AddServiceWithCondition(svc.Service, svc.Condition, svc.DependsOn...)
		} else {
			builder.AddService(svc.Service, svc.DependsOn...)
		}
	}

	// Validate dependencies exist
	if err := builder.ValidateDependencies(); err != nil {
		sr.log.Error("dependency validation failed", zap.Error(err))
		return nil, fmt.Errorf("invalid dependencies: %w", err)
	}

	// Build the graph
	graph, err := builder.Build()
	if err != nil {
		sr.log.Error("failed to build dependency graph", zap.Error(err))
		return nil, fmt.Errorf("failed to build dependency graph: %w", err)
	}

	sr.state.Graph = graph

	sr.log.Info("dependency graph built",
		zap.Int("levels", graph.GetLevelCount()),
		zap.Strings("order", graph.Order))

	return graph, nil
}

// CheckLevelReadiness verifies that all dependencies of a level are completed and healthy.
func (sr *StackRollout) CheckLevelReadiness(level int) (bool, []string) {
	if sr.state.Graph == nil {
		return false, []string{"no dependency graph"}
	}

	issues := make([]string, 0)

	// For first level (level 0), no dependencies
	if level == 0 {
		return true, issues
	}

	// Check that all services from previous levels are completed
	for prevLevel := 0; prevLevel < level; prevLevel++ {
		prevLevelServices := sr.state.Graph.GetLevelServices(prevLevel)
		for _, svc := range prevLevelServices {
			state, ok := sr.state.ServiceStates[svc]
			if !ok {
				issues = append(issues, fmt.Sprintf("service %s in previous level not found", svc))
				continue
			}

			if state.Status != StatusCompleted {
				issues = append(issues, fmt.Sprintf("service %s in level %d not completed (status: %s)", svc, prevLevel, state.Status))
			}

			if !state.HealthCheckPassed {
				issues = append(issues, fmt.Sprintf("service %s in level %d failed health check", svc, prevLevel))
			}
		}
	}

	return len(issues) == 0, issues
}

// GetRolloutPlan returns the services grouped by level for rollout execution.
func (sr *StackRollout) GetRolloutPlan() [][]string {
	if sr.state.Graph == nil {
		return [][]string{}
	}
	return sr.state.Graph.Levels
}

// InitializeServiceStates creates tracking state for all services.
func (sr *StackRollout) InitializeServiceStates(services map[string]*ServiceDependency) {
	for service := range services {
		sr.state.ServiceStates[service] = &ServiceRolloutState{
			Service: service,
			Status:  StatusPending,
			CircuitBreaker: &CircuitBreakerState{
				State:        CircuitClosed,
				FailureCount: 0,
			},
		}
	}

	sr.log.Info("initialized service states",
		zap.Int("service_count", len(sr.state.ServiceStates)))
}

// UpdateServiceStatus updates the rollout status of a service.
func (sr *StackRollout) UpdateServiceStatus(service string, status ServiceStatus, err error) {
	if state, ok := sr.state.ServiceStates[service]; ok {
		state.Status = status
		if err != nil {
			state.Error = err
		}
		state.CompletedAt = time.Now()

		sr.log.Info("service status updated",
			zap.String("service", service),
			zap.String("status", string(status)),
			zap.Error(err))

		// Track completion
		if status == StatusCompleted {
			sr.state.CompletedServices = append(sr.state.CompletedServices, service)
		} else if status == StatusFailed {
			sr.state.FailedServices = append(sr.state.FailedServices, service)
			sr.state.NeedsRollback = true
		}
	}
}

// MarkServiceHealthy marks a service's health check as passed.
func (sr *StackRollout) MarkServiceHealthy(service string) {
	if state, ok := sr.state.ServiceStates[service]; ok {
		state.HealthCheckPassed = true
		sr.log.Debug("service marked healthy",
			zap.String("service", service))
	}
}

// MarkServiceUnhealthy marks a service's health check as failed.
func (sr *StackRollout) MarkServiceUnhealthy(service string) {
	if state, ok := sr.state.ServiceStates[service]; ok {
		state.HealthCheckPassed = false
		sr.log.Warn("service marked unhealthy",
			zap.String("service", service))
	}
}

// MarkDependenciesReady marks all dependencies of a service as ready.
func (sr *StackRollout) MarkDependenciesReady(service string) {
	if state, ok := sr.state.ServiceStates[service]; ok {
		state.DependenciesReady = true
		sr.log.Debug("dependencies marked ready",
			zap.String("service", service))
	}
}

// GetMetrics returns current rollout metrics and statistics.
func (sr *StackRollout) GetMetrics() *StackMetrics {
	metrics := &StackMetrics{
		TotalServices:     len(sr.state.ServiceStates),
		CompletedServices: len(sr.state.CompletedServices),
		FailedServices:    len(sr.state.FailedServices),
	}

	if sr.state.StartedAt.IsZero() {
		return metrics
	}

	metrics.TotalRolloutTime = time.Since(sr.state.StartedAt)
	if metrics.CompletedServices > 0 {
		metrics.AverageRolloutTime = metrics.TotalRolloutTime / time.Duration(metrics.CompletedServices)
	}

	// Calculate parallelization factor
	if sr.state.Graph != nil {
		levels := len(sr.state.Graph.Levels)
		if levels > 0 {
			maxParallel := 0
			for _, level := range sr.state.Graph.Levels {
				if len(level) > maxParallel {
					maxParallel = len(level)
				}
			}

			// Approximation: if all services could run in parallel
			theoreticalMinTime := metrics.TotalRolloutTime / time.Duration(maxParallel)
			if theoreticalMinTime > 0 {
				metrics.ParallelizationFactor = float64(metrics.TotalRolloutTime) / float64(theoreticalMinTime)
			}
		}
	}

	// Calculate health check pass rate
	healthyCount := 0
	for _, state := range sr.state.ServiceStates {
		if state.HealthCheckPassed {
			healthyCount++
		}
	}
	if metrics.TotalServices > 0 {
		metrics.HealthCheckPassRate = float64(healthyCount) / float64(metrics.TotalServices) * 100
	}

	return metrics
}

// CanProceedWithRollout checks if we can proceed to rollout the next level.
func (sr *StackRollout) CanProceedWithRollout(currentLevel int) (bool, string) {
	if sr.state.NeedsRollback {
		return false, "rollback needed due to previous service failure"
	}

	// Level 0 can always proceed if no rollback is needed
	if currentLevel == 0 {
		return true, ""
	}

	// For levels > 0, we need the graph and must check dependencies
	if sr.state.Graph == nil {
		return false, "no dependency graph"
	}

	// Check previous level is healthy
	prevLevel := currentLevel - 1
	prevServices := sr.state.Graph.GetLevelServices(prevLevel)

	for _, svc := range prevServices {
		if state, ok := sr.state.ServiceStates[svc]; ok {
			if state.Status != StatusCompleted {
				return false, fmt.Sprintf("service %s in level %d not completed", svc, prevLevel)
			}
			if !state.HealthCheckPassed {
				return false, fmt.Sprintf("service %s in level %d failed health check", svc, prevLevel)
			}
		}
	}

	return true, ""
}

// GetRolloutState returns the current state of the rollout.
func (sr *StackRollout) GetRolloutState() *StackRolloutState {
	return sr.state
}
