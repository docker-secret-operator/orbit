package volumes

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/volume"
	"go.uber.org/zap"
)

// DockerClient defines the Docker API operations we use
type DockerClient interface {
	ContainerList(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error)
	ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error)
	VolumeInspect(ctx context.Context, name string) (volume.Volume, error)
}

// VolumeManager handles volume operations during rollouts
type VolumeManager struct {
	docker DockerClient
	log    *zap.Logger
	runner commandRunner
}

// NewVolumeManager creates a new volume manager
func NewVolumeManager(docker DockerClient, log *zap.Logger) *VolumeManager {
	if log == nil {
		log = zap.NewNop()
	}
	return &VolumeManager{
		docker: docker,
		log:    log,
		runner: execCommandRunner{},
	}
}

// ListVolumesForService returns all volumes mounted on containers of a service
func (vm *VolumeManager) ListVolumesForService(ctx context.Context, service string) ([]VolumeInfo, error) {
	vm.log.Info("discovering volumes", zap.String("service", service))

	if vm.docker == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}

	containers, err := vm.getServiceContainers(ctx, service)
	if err != nil {
		return nil, fmt.Errorf("failed to get containers: %w", err)
	}

	if len(containers) == 0 {
		vm.log.Info("no containers found for service", zap.String("service", service))
		return []VolumeInfo{}, nil
	}

	volumeMap := make(map[string]*VolumeInfo)

	for _, container := range containers {
		containerVolumes, err := vm.getContainerVolumes(ctx, container.ID)
		if err != nil {
			vm.log.Warn("failed to get container volumes",
				zap.String("container", container.ID),
				zap.Error(err))
			continue
		}

		for _, vol := range containerVolumes {
			if _, exists := volumeMap[vol.MountPath]; !exists {
				volumeMap[vol.MountPath] = &vol
				volumeMap[vol.MountPath].Containers = []string{container.ID}
			} else {
				volumeMap[vol.MountPath].Containers = append(
					volumeMap[vol.MountPath].Containers,
					container.ID,
				)
			}
		}
	}

	volumes := make([]VolumeInfo, 0, len(volumeMap))
	for _, vol := range volumeMap {
		volumes = append(volumes, *vol)
	}

	vm.log.Info("discovered volumes",
		zap.String("service", service),
		zap.Int("count", len(volumes)))

	return volumes, nil
}

// getServiceContainers returns all containers for a service
func (vm *VolumeManager) getServiceContainers(ctx context.Context, service string) ([]types.Container, error) {
	opts := types.ContainerListOptions{}
	containers, err := vm.docker.ContainerList(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var serviceContainers []types.Container
	for _, c := range containers {
		if label, ok := c.Labels["com.docker.compose.service"]; ok && label == service {
			serviceContainers = append(serviceContainers, c)
		}
	}

	return serviceContainers, nil
}

// getContainerVolumes returns volumes mounted on a specific container
func (vm *VolumeManager) getContainerVolumes(ctx context.Context, containerID string) ([]VolumeInfo, error) {
	inspect, err := vm.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	var volumes []VolumeInfo
	for _, mount := range inspect.Mounts {
		vol := VolumeInfo{
			Name:       mount.Name,
			MountPath:  mount.Destination,
			ReadOnly:   !mount.RW,
			Driver:     mount.Driver,
			Containers: []string{containerID},
		}
		volumes = append(volumes, vol)
	}

	return volumes, nil
}

// DetectVolumeMode checks if a volume is mounted read-only
func (vm *VolumeManager) DetectVolumeMode(ctx context.Context, containerID, mountPath string) (bool, error) {
	vm.log.Debug("detecting volume mode",
		zap.String("container", containerID),
		zap.String("mount_path", mountPath))

	if vm.docker == nil {
		return false, fmt.Errorf("docker client not initialized")
	}

	inspect, err := vm.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, fmt.Errorf("failed to inspect container: %w", err)
	}

	for _, mount := range inspect.Mounts {
		if mount.Destination == mountPath {
			return !mount.RW, nil
		}
	}

	return false, fmt.Errorf("volume not found: %s", mountPath)
}

// TrackVolumeState captures the current state of all volumes for a service
func (vm *VolumeManager) TrackVolumeState(ctx context.Context, service string) (*VolumeInventory, error) {
	vm.log.Info("tracking volume state", zap.String("service", service))

	if vm.docker == nil {
		return nil, fmt.Errorf("docker client not initialized")
	}

	volumes, err := vm.ListVolumesForService(ctx, service)
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}

	inventory := &VolumeInventory{
		Service:      service,
		Volumes:      volumes,
		SnapshotTime: time.Now(),
		TotalSize:    0,
	}

	vm.log.Info("volume state tracked",
		zap.String("service", service),
		zap.Int("volume_count", len(volumes)))

	return inventory, nil
}

// ValidateVolumeTransition checks if safe to transition volumes from old to new container
func (vm *VolumeManager) ValidateVolumeTransition(ctx context.Context,
	oldContainerID, newContainerID string, volumes []VolumeInfo) (bool, string, error) {

	vm.log.Info("validating volume transition",
		zap.String("old_container", oldContainerID),
		zap.String("new_container", newContainerID),
		zap.Int("volume_count", len(volumes)))

	newInspect, err := vm.docker.ContainerInspect(ctx, newContainerID)
	if err != nil {
		reason := fmt.Sprintf("new container not found: %s", newContainerID)
		return false, reason, nil
	}

	if !newInspect.State.Running {
		reason := "new container is not running"
		return false, reason, nil
	}

	oldInspect, err := vm.docker.ContainerInspect(ctx, oldContainerID)
	if err != nil {
		reason := fmt.Sprintf("old container not found: %s", oldContainerID)
		return false, reason, nil
	}

	_ = oldInspect

	for _, vol := range volumes {
		_, err := vm.docker.VolumeInspect(ctx, vol.Name)
		if err != nil {
			reason := fmt.Sprintf("volume not found: %s", vol.Name)
			return false, reason, nil
		}
	}

	if newInspect.State.Error != "" {
		reason := fmt.Sprintf("new container has error: %s", newInspect.State.Error)
		return false, reason, nil
	}

	vm.log.Info("volume transition valid",
		zap.String("old", oldContainerID),
		zap.String("new", newContainerID))

	return true, "", nil
}
