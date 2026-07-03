package rollout

import (
	"context"

	"github.com/docker/docker/client"
	"go.uber.org/zap"

	"github.com/docker-secret-operator/orbit/internal/volumes"
)

// VolumeManager handles volume operations during rollouts.
// This is used to discover and track volumes for stateful services.
type VolumeManager interface {
	ListVolumesForService(ctx context.Context, service string) ([]volumes.VolumeInfo, error)
	DetectVolumeMode(ctx context.Context, containerID, mountPath string) (bool, error)
	TrackVolumeState(ctx context.Context, service string) (*volumes.VolumeInventory, error)
	ValidateVolumeTransition(ctx context.Context, oldContainerID, newContainerID string, vols []volumes.VolumeInfo) (bool, string, error)
}

// NewVolumeManager creates a new volume manager from a Docker client.
// Returns nil if the Docker client cannot be created.
func NewVolumeManager(log *zap.Logger) VolumeManager {
	client, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		if log != nil {
			log.Warn("failed to create Docker client for volume management", zap.Error(err))
		}
		return nil
	}
	return volumes.NewVolumeManager(client, log)
}

// DiscoverServiceVolumes discovers all volumes mounted on containers of a service.
// This is called during rollout planning to understand what volumes need protection.
func DiscoverServiceVolumes(ctx context.Context, service string, mgr VolumeManager) ([]volumes.VolumeInfo, error) {
	if mgr == nil {
		return nil, nil
	}
	return mgr.ListVolumesForService(ctx, service)
}
