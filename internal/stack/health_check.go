package stack

import (
	"fmt"
	"time"

	"go.uber.org/zap"
)

// HealthCheckConfig holds configuration for health checking behavior.
type HealthCheckConfig struct {
	FailureThreshold int           // Failures before circuit opens
	SuccessThreshold int           // Successes before circuit closes from half-open
	OpenTimeout      time.Duration // Time in open state before attempting half-open
	HalfOpenTimeout  time.Duration // Time to wait for recovery in half-open state
	CheckInterval    time.Duration // Interval between health checks
}

// DefaultHealthCheckConfig returns sensible defaults for health checking.
func DefaultHealthCheckConfig() *HealthCheckConfig {
	return &HealthCheckConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout:      30 * time.Second,
		HalfOpenTimeout:  10 * time.Second,
		CheckInterval:    5 * time.Second,
	}
}

// CheckServiceHealth evaluates if a service is healthy, including recursive dependency checks.
func (sr *StackRollout) CheckServiceHealth(service string, config *HealthCheckConfig) (bool, error) {
	if config == nil {
		config = DefaultHealthCheckConfig()
	}

	state, ok := sr.state.ServiceStates[service]
	if !ok {
		return false, fmt.Errorf("service %q not found", service)
	}

	// If service health check itself failed, it's not healthy
	if !state.HealthCheckPassed {
		sr.log.Debug("service health check failed",
			zap.String("service", service))
		return false, nil
	}

	// Check all dependencies are healthy (recursive validation)
	if sr.state.Graph != nil {
		deps := sr.state.Graph.GetDependencies(service)
		for _, dep := range deps {
			_, ok := sr.state.ServiceStates[dep]
			if !ok {
				sr.log.Warn("dependency not found",
					zap.String("service", service),
					zap.String("dependency", dep))
				return false, fmt.Errorf("dependency %q of %q not found", dep, service)
			}

			// Check if dependency's circuit is open
			if sr.IsCircuitOpen(dep) {
				sr.log.Debug("dependency circuit open",
					zap.String("service", service),
					zap.String("dependency", dep))
				return false, nil
			}

			// Recursively check dependency health
			depHealthy, err := sr.CheckServiceHealth(dep, config)
			if err != nil {
				return false, err
			}
			if !depHealthy {
				sr.log.Debug("dependency unhealthy",
					zap.String("service", service),
					zap.String("dependency", dep))
				return false, nil
			}
		}
	}

	return true, nil
}

// RecordHealthCheckFailure records a health check failure and updates circuit breaker state.
func (sr *StackRollout) RecordHealthCheckFailure(service string, config *HealthCheckConfig) {
	if config == nil {
		config = DefaultHealthCheckConfig()
	}

	state, ok := sr.state.ServiceStates[service]
	if !ok {
		return
	}

	if state.CircuitBreaker == nil {
		state.CircuitBreaker = &CircuitBreakerState{
			State:        CircuitClosed,
			FailureCount: 0,
		}
	}

	state.CircuitBreaker.FailureCount++
	state.CircuitBreaker.LastFailureTime = time.Now()

	sr.log.Debug("health check failure recorded",
		zap.String("service", service),
		zap.Int("failure_count", state.CircuitBreaker.FailureCount))

	// Open circuit if threshold reached
	if state.CircuitBreaker.FailureCount >= config.FailureThreshold &&
		state.CircuitBreaker.State == CircuitClosed {
		state.CircuitBreaker.State = CircuitOpen
		state.CircuitBreaker.OpenedAt = time.Now()
		sr.log.Warn("circuit opened",
			zap.String("service", service))
	}
}

// RecordHealthCheckSuccess records a successful health check and updates circuit breaker state.
func (sr *StackRollout) RecordHealthCheckSuccess(service string, config *HealthCheckConfig) {
	if config == nil {
		config = DefaultHealthCheckConfig()
	}

	state, ok := sr.state.ServiceStates[service]
	if !ok {
		return
	}

	if state.CircuitBreaker == nil {
		state.CircuitBreaker = &CircuitBreakerState{
			State:        CircuitClosed,
			FailureCount: 0,
		}
	}

	state.CircuitBreaker.FailureCount = 0
	state.CircuitBreaker.LastSuccessTime = time.Now()

	// Transition from open/half-open to closed
	if state.CircuitBreaker.State != CircuitClosed {
		state.CircuitBreaker.State = CircuitClosed
		sr.log.Info("circuit closed",
			zap.String("service", service))
	}

	sr.log.Debug("health check success recorded",
		zap.String("service", service))
}

// IsCircuitOpen returns true if a service's circuit breaker is open or half-open.
func (sr *StackRollout) IsCircuitOpen(service string) bool {
	state, ok := sr.state.ServiceStates[service]
	if !ok || state.CircuitBreaker == nil {
		return false
	}
	return state.CircuitBreaker.State != CircuitClosed
}

// CanAttemptHealthCheck returns true if a service's circuit breaker allows a health check attempt.
func (sr *StackRollout) CanAttemptHealthCheck(service string, config *HealthCheckConfig) bool {
	if config == nil {
		config = DefaultHealthCheckConfig()
	}

	state, ok := sr.state.ServiceStates[service]
	if !ok || state.CircuitBreaker == nil {
		return true // Circuit doesn't exist yet, can attempt
	}

	cb := state.CircuitBreaker
	switch cb.State {
	case CircuitClosed:
		return true // Always check when closed

	case CircuitOpen:
		// Try to transition to half-open after timeout
		if time.Since(cb.OpenedAt) > config.OpenTimeout {
			cb.State = CircuitHalfOpen
			cb.HalfOpenCheckedAt = time.Now()
			sr.log.Info("circuit transitioning to half-open",
				zap.String("service", service))
			return true
		}
		return false // Still in open state

	case CircuitHalfOpen:
		// In half-open, allow one check attempt
		return true

	default:
		return true
	}
}

// GetDependencyHealthStatus returns health status of all dependencies for a service.
func (sr *StackRollout) GetDependencyHealthStatus(service string) map[string]bool {
	status := make(map[string]bool)

	if sr.state.Graph == nil {
		return status
	}

	deps := sr.state.Graph.GetDependencies(service)
	for _, dep := range deps {
		depState, ok := sr.state.ServiceStates[dep]
		if !ok {
			status[dep] = false
			continue
		}

		// Dependency is healthy if health check passed and no open circuit
		status[dep] = depState.HealthCheckPassed && !sr.IsCircuitOpen(dep)
	}

	return status
}

// ValidateDependencyHealth checks if all dependencies of a service are healthy.
func (sr *StackRollout) ValidateDependencyHealth(service string) (bool, []string) {
	issues := make([]string, 0)

	if sr.state.Graph == nil {
		return true, issues
	}

	deps := sr.state.Graph.GetDependencies(service)
	for _, dep := range deps {
		depState, ok := sr.state.ServiceStates[dep]
		if !ok {
			issues = append(issues, fmt.Sprintf("dependency %q not found", dep))
			continue
		}

		if !depState.HealthCheckPassed {
			issues = append(issues, fmt.Sprintf("dependency %q health check failed", dep))
		}

		if sr.IsCircuitOpen(dep) {
			issues = append(issues, fmt.Sprintf("dependency %q circuit breaker open", dep))
		}
	}

	return len(issues) == 0, issues
}

// GetCircuitBreakerStatus returns the current circuit breaker state for a service.
func (sr *StackRollout) GetCircuitBreakerStatus(service string) *CircuitBreakerState {
	state, ok := sr.state.ServiceStates[service]
	if !ok || state.CircuitBreaker == nil {
		return &CircuitBreakerState{
			State:        CircuitClosed,
			FailureCount: 0,
		}
	}
	return state.CircuitBreaker
}

// GetUnhealthyServices returns list of services with open circuits or failed health checks.
func (sr *StackRollout) GetUnhealthyServices() []string {
	unhealthy := make([]string, 0)

	for service, state := range sr.state.ServiceStates {
		if !state.HealthCheckPassed || sr.IsCircuitOpen(service) {
			unhealthy = append(unhealthy, service)
		}
	}

	return unhealthy
}
