package stack

import "time"

// ServiceDependency represents the dependency relationship between services.
type ServiceDependency struct {
	Service        string   // Name of the service
	DependsOn      []string // Services this depends on
	Condition      string   // Dependency condition (service_started, service_healthy, etc.)
	IsStateful     bool     // Whether the service manages state (volumes)
	HasHealthCheck bool     // Whether the service has a health check
}

// DependencyGraph represents the full dependency topology of a stack.
// Services are organized into levels where services at the same level can be
// rolled out in parallel, and each level depends on the previous level being healthy.
type DependencyGraph struct {
	Services map[string]*ServiceDependency // All services and their dependencies
	Levels   [][]string                    // Services grouped by dependency level
	Order    []string                      // Flat ordering of all services (topological sort)
}

// StackRolloutConfig holds configuration for a stack rollout operation.
type StackRolloutConfig struct {
	ComposeFile        string        // Path to docker-compose.yml
	Service            string        // Specific service to rollout (empty = all)
	TargetImage        string        // Image to deploy (optional override)
	ParallelDegree     int           // How many services can rollout simultaneously
	Timeout            time.Duration // Overall timeout for the rollout
	HealthCheckTimeout time.Duration // Per-service health check timeout
	MaxRetries         int           // Max retries per service
	DryRun             bool          // Preview rollout without executing
	ValidateOnly       bool          // Only validate, don't rollout
}

// StackRolloutState tracks the progress of a multi-service rollout.
type StackRolloutState struct {
	Config            *StackRolloutConfig
	Graph             *DependencyGraph
	StartedAt         time.Time
	ServiceStates     map[string]*ServiceRolloutState
	CompletedServices []string
	FailedServices    []string
	Rolled            bool // Whether any services have been rolled out
	NeedsRollback     bool // Whether rollback is needed
}

// ServiceRolloutState tracks the state of a single service during rollout.
type ServiceRolloutState struct {
	Service           string
	Status            ServiceStatus
	StartedAt         time.Time
	CompletedAt       time.Time
	Error             error
	OldContainer      string
	NewContainer      string
	HealthCheckPassed bool
	VolumesDetected   int
	DependenciesReady bool
	CircuitBreaker    *CircuitBreakerState
}

// CircuitBreakerState tracks health check circuit breaker state for a service.
type CircuitBreakerState struct {
	State             CircuitState // Current state: Closed, Open, or HalfOpen
	FailureCount      int          // Consecutive failures
	LastFailureTime   time.Time    // Time of last failure
	LastSuccessTime   time.Time    // Time of last success
	OpenedAt          time.Time    // When circuit was opened
	HalfOpenCheckedAt time.Time    // When half-open recovery was last attempted
}

// CircuitState represents the state of a service's circuit breaker.
type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"    // Service is healthy, accepting checks
	CircuitOpen     CircuitState = "open"      // Service failed, rejecting new checks
	CircuitHalfOpen CircuitState = "half_open" // Service failed, attempting recovery
)

// ServiceStatus represents the status of a service during rollout.
type ServiceStatus string

const (
	StatusPending     ServiceStatus = "pending"      // Waiting to be rolled out
	StatusValidating  ServiceStatus = "validating"   // Validating prerequisites
	StatusReady       ServiceStatus = "ready"        // Ready to be rolled out
	StatusRolling     ServiceStatus = "rolling"      // Currently rolling out
	StatusHealthCheck ServiceStatus = "health_check" // Waiting for health check
	StatusCompleted   ServiceStatus = "completed"    // Successfully rolled out
	StatusFailed      ServiceStatus = "failed"       // Rollout failed
	StatusRollingBack ServiceStatus = "rolling_back" // Rolling back changes
)

// StackValidationResult contains the results of pre-rollout validation.
type StackValidationResult struct {
	Valid                bool
	Issues               []string
	Warnings             []string
	ServiceCount         int
	StatefulServices     int
	HealthCheckServices  int
	CircularDependencies [][]string // Services with circular deps
}

// RolloutLevel represents services that can be rolled out in parallel.
type RolloutLevel struct {
	Level        int      // 0-based level index
	Services     []string // Services at this level
	Dependencies []int    // Levels this depends on
}

// StackMetrics provides statistics about the stack and rollout progress.
type StackMetrics struct {
	TotalServices         int
	CompletedServices     int
	FailedServices        int
	RolledOutServices     int
	PendingServices       int
	AverageRolloutTime    time.Duration
	TotalRolloutTime      time.Duration
	ParallelizationFactor float64 // Actual vs maximum parallelism
	HealthCheckPassRate   float64 // Percentage of services with passing health checks
}
