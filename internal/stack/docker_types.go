package stack

import "time"

// ContainerInfo holds information about a Docker container.
type ContainerInfo struct {
	ID         string            // Container ID
	Name       string            // Container name
	Service    string            // Service name from compose
	Image      string            // Image name
	Status     ContainerStatus   // Current status
	Health     HealthStatus      // Health check status
	Ports      map[int]int       // Host port -> container port mapping
	CreatedAt  time.Time         // When container was created
	StartedAt  time.Time         // When container was started
	FinishedAt time.Time         // When container finished (if stopped)
	Labels     map[string]string // Container labels
	ExitCode   int               // Exit code if stopped
}

// ContainerStatus represents the status of a Docker container.
type ContainerStatus string

const (
	ContainerCreated    ContainerStatus = "created"
	ContainerRunning    ContainerStatus = "running"
	ContainerPaused     ContainerStatus = "paused"
	ContainerRestarting ContainerStatus = "restarting"
	ContainerRemoving   ContainerStatus = "removing"
	ContainerRemoved    ContainerStatus = "removed"
	ContainerExited     ContainerStatus = "exited"
	ContainerDead       ContainerStatus = "dead"
	ContainerUnknown    ContainerStatus = "unknown"
)

// HealthStatus represents the health check status of a container.
type HealthStatus string

const (
	HealthUnknown   HealthStatus = "unknown"   // No healthcheck configured
	HealthStarting  HealthStatus = "starting"  // Within start period
	HealthHealthy   HealthStatus = "healthy"   // Healthcheck passing
	HealthUnhealthy HealthStatus = "unhealthy" // Healthcheck failing
	HealthNone      HealthStatus = "none"      // Healthcheck disabled
)

// HealthCheckConfig from docker-compose healthcheck block.
type DockerHealthCheck struct {
	Test        []string      // Command to run
	Interval    time.Duration // Interval between checks
	Timeout     time.Duration // Timeout for each check
	Retries     int           // Retries before unhealthy
	StartPeriod time.Duration // Grace period before checks start
}

// ContainerOperation represents a Docker container operation.
type ContainerOperation string

const (
	OpStart   ContainerOperation = "start"
	OpStop    ContainerOperation = "stop"
	OpCreate  ContainerOperation = "create"
	OpRemove  ContainerOperation = "remove"
	OpInspect ContainerOperation = "inspect"
	OpLogs    ContainerOperation = "logs"
)

// ContainerOperationResult captures result of a container operation.
type ContainerOperationResult struct {
	Operation   ContainerOperation
	Service     string
	ContainerID string
	Success     bool
	Error       error
	Message     string
	ExecutedAt  time.Time
	Duration    time.Duration
}

// RunOptions holds options for starting a container.
type RunOptions struct {
	Image       string            // Image to run
	Name        string            // Container name
	Env         map[string]string // Environment variables
	Ports       map[int]int       // Host port -> container port
	HealthCheck *DockerHealthCheck
	Labels      map[string]string
	Detach      bool
	Remove      bool
	Restart     string
	DependsOn   []string
	Volumes     map[string]string // Host path -> container path
}

// DockerClient interface for container operations.
type DockerClient interface {
	// Lifecycle operations
	CreateContainer(opts *RunOptions) (containerID string, err error)
	StartContainer(containerID string) error
	StopContainer(containerID string, timeout time.Duration) error
	RemoveContainer(containerID string, force bool) error

	// Information operations
	InspectContainer(containerID string) (*ContainerInfo, error)
	ListContainers(filters map[string][]string) ([]*ContainerInfo, error)
	GetContainerHealth(containerID string) (HealthStatus, error)

	// Utility operations
	PullImage(imageName string) error
	GetLogs(containerID string, lines int) (string, error)
	WaitForContainer(containerID string, timeout time.Duration) (exitCode int, err error)
}
