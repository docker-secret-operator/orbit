package stack

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// RealDockerClient implements DockerClient interface using Docker SDK.
type RealDockerClient struct {
	log *zap.Logger
}

// NewRealDockerClient creates a new Docker client.
func NewRealDockerClient(log *zap.Logger) *RealDockerClient {
	if log == nil {
		log = zap.NewNop()
	}
	return &RealDockerClient{log: log}
}

// CreateContainer creates a new container from options.
func (dc *RealDockerClient) CreateContainer(opts *RunOptions) (string, error) {
	if opts == nil {
		return "", fmt.Errorf("run options required")
	}

	dc.log.Debug("creating container",
		zap.String("service", opts.Name),
		zap.String("image", opts.Image))

	// Placeholder: actual Docker SDK integration would go here
	// Returns a mock container ID for now
	containerID := "mock-" + opts.Name + "-" + fmt.Sprintf("%d", time.Now().Unix())

	dc.log.Info("container created",
		zap.String("container_id", containerID),
		zap.String("service", opts.Name))

	return containerID, nil
}

// StartContainer starts an existing container.
func (dc *RealDockerClient) StartContainer(containerID string) error {
	if containerID == "" {
		return fmt.Errorf("container ID required")
	}

	dc.log.Debug("starting container",
		zap.String("container_id", containerID))

	// Placeholder: actual Docker SDK integration would go here
	dc.log.Info("container started",
		zap.String("container_id", containerID))

	return nil
}

// StopContainer stops a running container.
func (dc *RealDockerClient) StopContainer(containerID string, timeout time.Duration) error {
	if containerID == "" {
		return fmt.Errorf("container ID required")
	}

	dc.log.Debug("stopping container",
		zap.String("container_id", containerID),
		zap.Duration("timeout", timeout))

	// Placeholder: actual Docker SDK integration would go here
	dc.log.Info("container stopped",
		zap.String("container_id", containerID))

	return nil
}

// RemoveContainer removes a container.
func (dc *RealDockerClient) RemoveContainer(containerID string, force bool) error {
	if containerID == "" {
		return fmt.Errorf("container ID required")
	}

	dc.log.Debug("removing container",
		zap.String("container_id", containerID),
		zap.Bool("force", force))

	// Placeholder: actual Docker SDK integration would go here
	dc.log.Info("container removed",
		zap.String("container_id", containerID))

	return nil
}

// InspectContainer returns detailed info about a container.
func (dc *RealDockerClient) InspectContainer(containerID string) (*ContainerInfo, error) {
	if containerID == "" {
		return nil, fmt.Errorf("container ID required")
	}

	dc.log.Debug("inspecting container",
		zap.String("container_id", containerID))

	// Placeholder: return mock container info
	return &ContainerInfo{
		ID:        containerID,
		Status:    ContainerRunning,
		Health:    HealthHealthy,
		CreatedAt: time.Now(),
		StartedAt: time.Now(),
	}, nil
}

// ListContainers returns containers matching filters.
func (dc *RealDockerClient) ListContainers(filters map[string][]string) ([]*ContainerInfo, error) {
	dc.log.Debug("listing containers",
		zap.Int("filter_count", len(filters)))

	// Placeholder: return empty list
	return make([]*ContainerInfo, 0), nil
}

// GetContainerHealth returns the health status of a container.
func (dc *RealDockerClient) GetContainerHealth(containerID string) (HealthStatus, error) {
	if containerID == "" {
		return HealthUnknown, fmt.Errorf("container ID required")
	}

	dc.log.Debug("checking container health",
		zap.String("container_id", containerID))

	// Placeholder: return healthy
	return HealthHealthy, nil
}

// PullImage pulls an image from registry.
func (dc *RealDockerClient) PullImage(imageName string) error {
	if imageName == "" {
		return fmt.Errorf("image name required")
	}

	dc.log.Debug("pulling image",
		zap.String("image", imageName))

	// Placeholder: actual Docker SDK integration would go here
	dc.log.Info("image pulled",
		zap.String("image", imageName))

	return nil
}

// GetLogs returns recent logs from a container.
func (dc *RealDockerClient) GetLogs(containerID string, lines int) (string, error) {
	if containerID == "" {
		return "", fmt.Errorf("container ID required")
	}

	dc.log.Debug("retrieving logs",
		zap.String("container_id", containerID),
		zap.Int("lines", lines))

	// Placeholder: return empty logs
	return "", nil
}

// WaitForContainer waits for a container to finish.
func (dc *RealDockerClient) WaitForContainer(containerID string, timeout time.Duration) (int, error) {
	if containerID == "" {
		return -1, fmt.Errorf("container ID required")
	}

	dc.log.Debug("waiting for container",
		zap.String("container_id", containerID),
		zap.Duration("timeout", timeout))

	// Placeholder: return exit code 0
	return 0, nil
}

// MockDockerClient is a test double for Docker operations.
//
// mu guards CreatedContainers/ContainerStates/ContainerHealthStates: tests
// that simulate a container becoming healthy from a background goroutine
// (while the main goroutine drives a transaction/rollout that reads the same
// maps) must go through SetContainerHealth rather than writing the map
// field directly, or the access races.
type MockDockerClient struct {
	log                     *zap.Logger
	mu                      sync.Mutex
	CreatedContainers       map[string]*ContainerInfo
	ContainerStates         map[string]ContainerStatus
	ContainerHealthStates   map[string]HealthStatus
	CreateContainerError    error
	StartContainerError     error
	StopContainerError      error
	RemoveContainerError    error
	InspectContainerError   error
	GetContainerHealthError error
}

// NewMockDockerClient creates a test double.
func NewMockDockerClient(log *zap.Logger) *MockDockerClient {
	return &MockDockerClient{
		log:                   log,
		CreatedContainers:     make(map[string]*ContainerInfo),
		ContainerStates:       make(map[string]ContainerStatus),
		ContainerHealthStates: make(map[string]HealthStatus),
	}
}

// CreateContainer mocks container creation.
func (m *MockDockerClient) CreateContainer(opts *RunOptions) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreateContainerError != nil {
		return "", m.CreateContainerError
	}
	containerID := "mock-" + opts.Name
	m.CreatedContainers[containerID] = &ContainerInfo{
		ID:     containerID,
		Name:   opts.Name,
		Status: ContainerCreated,
		Health: HealthStarting,
		Image:  opts.Image,
	}
	m.ContainerStates[containerID] = ContainerCreated
	m.ContainerHealthStates[containerID] = HealthStarting
	return containerID, nil
}

// StartContainer mocks starting a container.
func (m *MockDockerClient) StartContainer(containerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.StartContainerError != nil {
		return m.StartContainerError
	}
	m.ContainerStates[containerID] = ContainerRunning
	return nil
}

// StopContainer mocks stopping a container.
func (m *MockDockerClient) StopContainer(containerID string, timeout time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.StopContainerError != nil {
		return m.StopContainerError
	}
	m.ContainerStates[containerID] = ContainerExited
	return nil
}

// RemoveContainer mocks removing a container.
func (m *MockDockerClient) RemoveContainer(containerID string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.RemoveContainerError != nil {
		return m.RemoveContainerError
	}
	delete(m.CreatedContainers, containerID)
	delete(m.ContainerStates, containerID)
	return nil
}

// InspectContainer mocks inspection.
func (m *MockDockerClient) InspectContainer(containerID string) (*ContainerInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.InspectContainerError != nil {
		return nil, m.InspectContainerError
	}
	info, ok := m.CreatedContainers[containerID]
	if !ok {
		return nil, fmt.Errorf("container not found")
	}
	return info, nil
}

// ListContainers mocks listing containers.
func (m *MockDockerClient) ListContainers(filters map[string][]string) ([]*ContainerInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*ContainerInfo, 0, len(m.CreatedContainers))
	for _, info := range m.CreatedContainers {
		result = append(result, info)
	}
	return result, nil
}

// GetContainerHealth mocks getting health status.
func (m *MockDockerClient) GetContainerHealth(containerID string) (HealthStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetContainerHealthError != nil {
		return HealthUnknown, m.GetContainerHealthError
	}
	status, ok := m.ContainerHealthStates[containerID]
	if !ok {
		return HealthUnknown, nil
	}
	return status, nil
}

// SetContainerHealth safely sets a container's mocked health status. Tests
// that flip a container's health from a background goroutine (to unblock a
// health-check loop running on another goroutine) must use this instead of
// writing ContainerHealthStates directly, since CreateContainer/
// GetContainerHealth also access it under mu.
func (m *MockDockerClient) SetContainerHealth(containerID string, health HealthStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ContainerHealthStates[containerID] = health
}

// PullImage mocks pulling an image.
func (m *MockDockerClient) PullImage(imageName string) error {
	return nil
}

// GetLogs mocks getting logs.
func (m *MockDockerClient) GetLogs(containerID string, lines int) (string, error) {
	return "", nil
}

// WaitForContainer mocks waiting.
func (m *MockDockerClient) WaitForContainer(containerID string, timeout time.Duration) (int, error) {
	return 0, nil
}
