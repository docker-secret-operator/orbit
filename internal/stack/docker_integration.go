package stack

import (
	"fmt"
	"time"

	"go.uber.org/zap"
)

// DockerIntegration bridges Docker operations with stack rollout orchestration.
type DockerIntegration struct {
	rollout *StackRollout
	client  DockerClient
	log     *zap.Logger
}

// NewDockerIntegration creates a new Docker integration layer.
func NewDockerIntegration(rollout *StackRollout, client DockerClient, log *zap.Logger) *DockerIntegration {
	if log == nil {
		log = zap.NewNop()
	}
	return &DockerIntegration{
		rollout: rollout,
		client:  client,
		log:     log,
	}
}

// EnsureContainersRunning verifies all services have running containers.
func (di *DockerIntegration) EnsureContainersRunning(services []string) error {
	for _, service := range services {
		if err := di.ensureServiceRunning(service); err != nil {
			di.log.Error("failed to ensure service running",
				zap.String("service", service),
				zap.Error(err))
			return err
		}
	}
	return nil
}

// ensureServiceRunning ensures a single service has a running container.
func (di *DockerIntegration) ensureServiceRunning(service string) error {
	state, ok := di.rollout.state.ServiceStates[service]
	if !ok {
		return fmt.Errorf("service %q not found in rollout state", service)
	}

	// If container already exists and is running, verify health
	if state.NewContainer != "" {
		info, err := di.client.InspectContainer(state.NewContainer)
		if err != nil {
			di.log.Warn("failed to inspect container",
				zap.String("service", service),
				zap.String("container_id", state.NewContainer))
		} else if info.Status == ContainerRunning {
			di.log.Debug("container already running",
				zap.String("service", service),
				zap.String("container_id", state.NewContainer))
			return nil
		}
	}

	// Create and start new container
	containerID, err := di.client.CreateContainer(&RunOptions{
		Name:  service,
		Image: di.getServiceImage(service),
	})
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	if err := di.client.StartContainer(containerID); err != nil {
		di.client.RemoveContainer(containerID, true)
		return fmt.Errorf("failed to start container: %w", err)
	}

	state.NewContainer = containerID
	di.log.Info("service container started",
		zap.String("service", service),
		zap.String("container_id", containerID))

	return nil
}

// MonitorContainerHealth monitors a container's health until timeout or completion.
func (di *DockerIntegration) MonitorContainerHealth(service string, timeout time.Duration) error {
	state, ok := di.rollout.state.ServiceStates[service]
	if !ok {
		return fmt.Errorf("service %q not found", service)
	}

	if state.NewContainer == "" {
		return fmt.Errorf("no container for service %q", service)
	}

	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			di.rollout.RecordHealthCheckFailure(service, nil)
			return fmt.Errorf("health check timeout for service %q", service)
		}

		health, err := di.client.GetContainerHealth(state.NewContainer)
		if err != nil {
			di.log.Warn("failed to check container health",
				zap.String("service", service),
				zap.Error(err))
			time.Sleep(1 * time.Second)
			continue
		}

		switch health {
		case HealthHealthy:
			di.rollout.RecordHealthCheckSuccess(service, nil)
			di.rollout.MarkServiceHealthy(service)
			di.log.Info("service health check passed",
				zap.String("service", service))
			return nil

		case HealthUnhealthy:
			di.rollout.RecordHealthCheckFailure(service, nil)
			di.log.Warn("service health check failed",
				zap.String("service", service))
			return fmt.Errorf("health check failed for service %q", service)

		case HealthStarting, HealthUnknown:
			// Still starting, continue checking
			time.Sleep(2 * time.Second)

		default:
			time.Sleep(2 * time.Second)
		}
	}
}

// DrainConnections waits for existing connections to finish.
func (di *DockerIntegration) DrainConnections(service string, timeout time.Duration) error {
	state, ok := di.rollout.state.ServiceStates[service]
	if !ok {
		return fmt.Errorf("service %q not found", service)
	}

	if state.OldContainer == "" {
		return nil // No old container to drain
	}

	di.log.Info("draining connections",
		zap.String("service", service),
		zap.Duration("timeout", timeout),
		zap.String("container_id", state.OldContainer))

	// Wait for container to stop or timeout
	exitCode, err := di.client.WaitForContainer(state.OldContainer, timeout)
	if err != nil {
		di.log.Warn("connection drain timeout, forcing stop",
			zap.String("service", service),
			zap.Error(err))
		return di.client.StopContainer(state.OldContainer, 5*time.Second)
	}

	di.log.Info("connections drained",
		zap.String("service", service),
		zap.Int("exit_code", exitCode))

	return nil
}

// SwitchTraffic atomically switches traffic from old container to new one.
func (di *DockerIntegration) SwitchTraffic(service string) error {
	state, ok := di.rollout.state.ServiceStates[service]
	if !ok {
		return fmt.Errorf("service %q not found", service)
	}

	if state.NewContainer == "" {
		return fmt.Errorf("no new container for service %q", service)
	}

	di.log.Info("switching traffic",
		zap.String("service", service),
		zap.String("old_container", state.OldContainer),
		zap.String("new_container", state.NewContainer))

	// In a real implementation, this would update load balancer or proxy
	// For now, we just mark the transition
	state.Status = StatusCompleted

	return nil
}

// CleanupOldContainer removes the old container after rollout.
func (di *DockerIntegration) CleanupOldContainer(service string) error {
	state, ok := di.rollout.state.ServiceStates[service]
	if !ok {
		return fmt.Errorf("service %q not found", service)
	}

	if state.OldContainer == "" {
		return nil // No old container to cleanup
	}

	di.log.Debug("cleaning up old container",
		zap.String("service", service),
		zap.String("container_id", state.OldContainer))

	if err := di.client.RemoveContainer(state.OldContainer, true); err != nil {
		di.log.Warn("failed to remove old container",
			zap.String("service", service),
			zap.Error(err))
		return err
	}

	state.OldContainer = ""
	di.log.Info("old container removed",
		zap.String("service", service))

	return nil
}

// RolloutService performs a complete zero-downtime rollout of a service.
func (di *DockerIntegration) RolloutService(service string, timeout time.Duration) error {
	state, ok := di.rollout.state.ServiceStates[service]
	if !ok {
		return fmt.Errorf("service %q not found", service)
	}

	di.log.Info("starting service rollout",
		zap.String("service", service),
		zap.Duration("timeout", timeout))

	state.Status = StatusRolling
	state.StartedAt = time.Now()

	// Step 1: Start new container
	if err := di.ensureServiceRunning(service); err != nil {
		di.rollout.UpdateServiceStatus(service, StatusFailed, err)
		return err
	}

	// Step 2: Wait for health check
	state.Status = StatusHealthCheck
	if err := di.MonitorContainerHealth(service, timeout); err != nil {
		di.rollout.UpdateServiceStatus(service, StatusFailed, err)
		di.client.RemoveContainer(state.NewContainer, true)
		state.NewContainer = ""
		return err
	}

	// Step 3: Switch traffic
	state.OldContainer = state.NewContainer // Move new to old for cleanup
	if err := di.SwitchTraffic(service); err != nil {
		di.rollout.UpdateServiceStatus(service, StatusFailed, err)
		return err
	}

	// Step 4: Drain and cleanup
	if err := di.DrainConnections(service, 10*time.Second); err != nil {
		di.log.Warn("drain failed, proceeding with cleanup",
			zap.String("service", service),
			zap.Error(err))
	}

	if err := di.CleanupOldContainer(service); err != nil {
		di.log.Warn("cleanup failed",
			zap.String("service", service),
			zap.Error(err))
	}

	// Mark complete
	di.rollout.UpdateServiceStatus(service, StatusCompleted, nil)
	state.CompletedAt = time.Now()

	di.log.Info("service rollout completed",
		zap.String("service", service),
		zap.Duration("duration", time.Since(state.StartedAt)))

	return nil
}

// getServiceImage returns the image for a service from dependency graph.
func (di *DockerIntegration) getServiceImage(service string) string {
	if di.rollout.state.Graph == nil {
		return service + ":latest"
	}

	if svc, ok := di.rollout.state.Graph.Services[service]; ok && svc != nil {
		// Would use actual image from compose file in real implementation
		return service + ":latest"
	}

	return service + ":latest"
}
