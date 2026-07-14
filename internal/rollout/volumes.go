package rollout

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"
	"go.uber.org/zap"

	"github.com/docker-secret-operator/orbit/internal/volumes"
)

// VolumeCoordinator protects a single service's Docker volumes across one
// rollout/rollback transition. It mirrors
// internal/volumes.RolloutVolumeCoordinator's exported lifecycle, abstracted
// here so Run/Rollback don't import internal/volumes directly and tests can
// substitute a fake. Every method must be a safe no-op when the service has
// no volumes — internal/volumes.RolloutVolumeCoordinator already guards each
// method on len(Volumes)==0, so callers here invoke this lifecycle
// unconditionally for every rollout, stateful or not.
type VolumeCoordinator interface {
	// PrepareForRollout discovers the service's volumes, snapshots their
	// metadata, and mounts oldContainerID's volumes read-only to prevent the
	// soon-to-start new container from racing the old one on writes. Called
	// once the old container is identified, before the new backend is
	// registered with the proxy.
	PrepareForRollout(ctx context.Context, oldContainerID string) error

	// ValidateNewContainer confirms newContainerID is ready to receive the
	// discovered volumes. Called alongside PrepareForRollout, before the new
	// backend is registered.
	ValidateNewContainer(ctx context.Context, newContainerID string) error

	// CompleteTransition finalizes the transition once the new backend has
	// passed its stability window — cleans up temporary snapshot state.
	// Failure here is non-fatal: traffic has already committed to the new
	// backend by this point.
	CompleteTransition(ctx context.Context) error

	// Rollback restores the old container's volumes to read-write. Called
	// both from Run's automatic rollback (new backend failed its stability
	// window, old backend never touched) and from the standalone Rollback
	// entry point.
	Rollback(ctx context.Context) error

	// GetSnapshotsForPersistence returns this transition's captured snapshot
	// metadata in the form RolloutState.VolumeSnapshots persists to disk, so
	// a later `docker orbit rollback` — potentially a different process —
	// can restore volumes without a live VolumeCoordinator.
	GetSnapshotsForPersistence() map[string]interface{}
}

// VolumeManager creates a VolumeCoordinator for a single rollout and restores
// volumes from a rollout state file's persisted snapshot data. The latter is
// needed because `docker orbit rollback` normally runs as a fresh process,
// with no live VolumeCoordinator from the Run invocation that captured the
// snapshots.
type VolumeManager interface {
	NewCoordinator(service string) VolumeCoordinator
	RestoreFromPersisted(ctx context.Context, data map[string]interface{}) error
}

// dockerVolumeManager is the real, Docker-backed VolumeManager.
type dockerVolumeManager struct {
	vm *volumes.VolumeManager
}

func (d dockerVolumeManager) NewCoordinator(service string) VolumeCoordinator {
	return d.vm.NewRolloutVolumeCoordinator(service)
}

func (d dockerVolumeManager) RestoreFromPersisted(ctx context.Context, data map[string]interface{}) error {
	if len(data) == 0 {
		return nil
	}
	snapshots, err := volumes.DeserializeSnapshots(data)
	if err != nil {
		return fmt.Errorf("deserialize volume snapshots: %w", err)
	}
	return d.vm.RestoreFromSnapshots(ctx, snapshots)
}

// noopVolumeManager is used when a Docker client couldn't be constructed —
// rollout proceeds without volume protection rather than failing every
// deployment (stateful or not) outright. The absence is logged loudly at
// construction time (see newVolumeManager) so it isn't a silent gap.
type noopVolumeManager struct{}

func (noopVolumeManager) NewCoordinator(string) VolumeCoordinator { return noopVolumeCoordinator{} }
func (noopVolumeManager) RestoreFromPersisted(context.Context, map[string]interface{}) error {
	return nil
}

type noopVolumeCoordinator struct{}

func (noopVolumeCoordinator) PrepareForRollout(context.Context, string) error    { return nil }
func (noopVolumeCoordinator) ValidateNewContainer(context.Context, string) error { return nil }
func (noopVolumeCoordinator) CompleteTransition(context.Context) error           { return nil }
func (noopVolumeCoordinator) Rollback(context.Context) error                     { return nil }
func (noopVolumeCoordinator) GetSnapshotsForPersistence() map[string]interface{} { return nil }

// newVolumeManager creates the real Docker-backed VolumeManager, or a no-op
// fallback (with a warning) if a Docker client can't be constructed.
func newVolumeManager(log *zap.Logger) VolumeManager {
	dc, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		if log != nil {
			log.Warn("volume protection disabled: failed to create Docker client", zap.Error(err))
		}
		return noopVolumeManager{}
	}
	return dockerVolumeManager{vm: volumes.NewVolumeManager(dc, log)}
}
