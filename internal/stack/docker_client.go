package stack

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

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
