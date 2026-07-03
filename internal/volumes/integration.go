package volumes

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// RolloutVolumeCoordinator orchestrates volume management during a rollout operation.
// It handles the full lifecycle: discovery, safeguards, state persistence, and recovery.
type RolloutVolumeCoordinator struct {
	vm    *VolumeManager
	log   *zap.Logger
	state *VolumeTransitionState
}

// VolumeTransitionState tracks the current state of a volume transition.
type VolumeTransitionState struct {
	Service           string
	OldContainerID    string
	NewContainerID    string
	Volumes           []VolumeInfo
	Snapshots         map[string]*VolumeSnapshot
	TransitionStarted time.Time
	PreventedWrites   bool            // Whether we successfully prevented concurrent writes
	RWModeRestored    map[string]bool // Which volumes had RW mode restored
}

// NewRolloutVolumeCoordinator creates a new volume coordinator for a rollout.
func (vm *VolumeManager) NewRolloutVolumeCoordinator(service string) *RolloutVolumeCoordinator {
	return &RolloutVolumeCoordinator{
		vm:  vm,
		log: vm.log,
		state: &VolumeTransitionState{
			Service:        service,
			Snapshots:      make(map[string]*VolumeSnapshot),
			RWModeRestored: make(map[string]bool),
		},
	}
}

// PrepareForRollout executes the pre-rollout volume phase.
// This includes discovery, snapshot capture, and safeguards setup.
func (rvc *RolloutVolumeCoordinator) PrepareForRollout(ctx context.Context, oldContainerID string) error {
	rvc.log.Info("preparing volumes for rollout",
		zap.String("service", rvc.state.Service),
		zap.String("old_container", oldContainerID))

	rvc.state.OldContainerID = oldContainerID
	rvc.state.TransitionStarted = time.Now()

	// Step 1: Discover volumes
	volumes, err := rvc.vm.ListVolumesForService(ctx, rvc.state.Service)
	if err != nil {
		return fmt.Errorf("failed to discover volumes: %w", err)
	}

	rvc.state.Volumes = volumes
	rvc.log.Info("volumes discovered",
		zap.String("service", rvc.state.Service),
		zap.Int("count", len(volumes)))

	if len(volumes) == 0 {
		rvc.log.Info("no stateful volumes, proceeding with stateless rollout")
		return nil
	}

	// Step 2: Capture snapshots for recovery
	builder := rvc.vm.NewSnapshotBuilder(rvc.state.Service)
	snapshots, err := builder.CaptureVolumes(ctx)
	if err != nil {
		return fmt.Errorf("failed to capture volume snapshots: %w", err)
	}

	rvc.state.Snapshots = snapshots

	// Step 3: Validate snapshots before proceeding
	if err := ValidateSnapshots(snapshots); err != nil {
		return fmt.Errorf("invalid snapshot state: %w", err)
	}

	// Step 4: Prevent concurrent writes by mounting old container read-only
	for _, vol := range volumes {
		wasRW, err := rvc.vm.PreventConcurrentAccess(ctx, oldContainerID, vol.MountPath)
		if err != nil {
			rvc.log.Warn("failed to prevent concurrent access",
				zap.String("container", oldContainerID),
				zap.String("volume", vol.Name),
				zap.Error(err))
			// Don't fail the rollout if we can't prevent writes on one volume
			continue
		}

		// Track which volumes we modified
		if wasRW {
			rvc.state.RWModeRestored[vol.Name] = true
		}
		rvc.state.PreventedWrites = true
	}

	rvc.log.Info("volumes prepared for rollout",
		zap.String("service", rvc.state.Service),
		zap.Int("snapshots", len(snapshots)),
		zap.Bool("writes_prevented", rvc.state.PreventedWrites))

	return nil
}

// ValidateNewContainer validates that the new container is ready to receive volumes.
func (rvc *RolloutVolumeCoordinator) ValidateNewContainer(ctx context.Context, newContainerID string) error {
	rvc.log.Info("validating new container for volumes",
		zap.String("service", rvc.state.Service),
		zap.String("new_container", newContainerID))

	rvc.state.NewContainerID = newContainerID

	if len(rvc.state.Volumes) == 0 {
		return nil // No volumes to validate
	}

	// Validate that the new container exists and is running
	for _, vol := range rvc.state.Volumes {
		if err := rvc.vm.StageVolumeMount(ctx, newContainerID, vol.MountPath); err != nil {
			return fmt.Errorf("new container not ready for volume %s: %w", vol.Name, err)
		}
	}

	// Plan the volume transition to verify everything is safe
	_, err := rvc.vm.PlanVolumeTransition(ctx, rvc.state.OldContainerID, newContainerID, rvc.state.Volumes)
	if err != nil {
		return fmt.Errorf("volume transition not safe: %w", err)
	}

	rvc.log.Info("new container validated for volumes",
		zap.String("service", rvc.state.Service),
		zap.String("new_container", newContainerID),
		zap.Int("volumes", len(rvc.state.Volumes)))

	return nil
}

// CompleteTransition finalizes the volume transition after new container is healthy.
// Called after the new container is confirmed healthy and ready to receive traffic.
func (rvc *RolloutVolumeCoordinator) CompleteTransition(ctx context.Context) error {
	rvc.log.Info("completing volume transition",
		zap.String("service", rvc.state.Service),
		zap.String("old_container", rvc.state.OldContainerID),
		zap.String("new_container", rvc.state.NewContainerID))

	if len(rvc.state.Volumes) == 0 {
		return nil
	}

	// Volumes are already properly mounted:
	// - Old container is read-only (prevents further writes)
	// - New container sees the data (via volume mounts)
	// - We have snapshots for rollback if needed

	// Cleanup temporary snapshots if they were created
	for path, snapshot := range rvc.state.Snapshots {
		if snapshot.SnapshotPath != "" {
			if err := rvc.vm.CleanupSnapshot(ctx, snapshot.SnapshotPath); err != nil {
				rvc.log.Warn("failed to cleanup snapshot",
					zap.String("path", path),
					zap.Error(err))
			}
		}
	}

	rvc.log.Info("volume transition completed successfully",
		zap.String("service", rvc.state.Service),
		zap.Int("volumes", len(rvc.state.Volumes)))

	return nil
}

// Rollback restores the system to its pre-rollout state.
// Called when the new container fails and we need to return to the old version.
func (rvc *RolloutVolumeCoordinator) Rollback(ctx context.Context) error {
	rvc.log.Info("rolling back volume changes",
		zap.String("service", rvc.state.Service),
		zap.String("old_container", rvc.state.OldContainerID))

	if len(rvc.state.Volumes) == 0 {
		return nil
	}

	// Step 1: Restore old container's volume state (RW mode)
	if err := rvc.vm.RestoreVolumeState(ctx, rvc.state.OldContainerID, rvc.state.Volumes); err != nil {
		rvc.log.Warn("failed to restore old container volumes",
			zap.Error(err))
		// Don't fail rollback on restoration issues
	}

	// Step 2: Restore from snapshots if needed
	if err := rvc.vm.RestoreFromSnapshots(ctx, rvc.state.Snapshots); err != nil {
		rvc.log.Warn("failed to restore from snapshots",
			zap.Error(err))
	}

	// Step 3: Cleanup any snapshots
	for path, snapshot := range rvc.state.Snapshots {
		if snapshot.SnapshotPath != "" {
			if err := rvc.vm.CleanupSnapshot(ctx, snapshot.SnapshotPath); err != nil {
				rvc.log.Warn("failed to cleanup snapshot during rollback",
					zap.String("path", path),
					zap.Error(err))
			}
		}
	}

	rvc.log.Info("volume rollback completed",
		zap.String("service", rvc.state.Service),
		zap.Int("volumes", len(rvc.state.Volumes)))

	return nil
}

// GetTransitionState returns the current state of the volume transition.
// Used for logging, monitoring, and state persistence.
func (rvc *RolloutVolumeCoordinator) GetTransitionState() *VolumeTransitionState {
	return rvc.state
}

// GetSnapshotsForPersistence returns the snapshots in a format suitable for persisting to state file.
func (rvc *RolloutVolumeCoordinator) GetSnapshotsForPersistence() map[string]interface{} {
	return PersistSnapshots(rvc.state.Snapshots)
}

// VolumeReadinessCheck performs a comprehensive readiness check before rollout.
// Returns detailed feedback about what's ready and what needs attention.
type VolumeReadinessCheck struct {
	Service              string
	VolumeCount          int
	HasStatefulVolumes   bool
	AllVolumesAccessible bool
	AvailableSpace       int64
	Issues               []string
	Warnings             []string
	ReadyToRollout       bool
}

// CheckReadiness performs a full readiness check for volume handling.
func (rvc *RolloutVolumeCoordinator) CheckReadiness(ctx context.Context) *VolumeReadinessCheck {
	check := &VolumeReadinessCheck{
		Service:              rvc.state.Service,
		Issues:               make([]string, 0),
		Warnings:             make([]string, 0),
		AllVolumesAccessible: true,
	}

	// Check if service has volumes
	if len(rvc.state.Volumes) == 0 {
		check.HasStatefulVolumes = false
		check.ReadyToRollout = true
		return check
	}

	check.HasStatefulVolumes = true
	check.VolumeCount = len(rvc.state.Volumes)

	// Validate each volume
	for _, vol := range rvc.state.Volumes {
		if err := rvc.vm.ValidateVolumeAccessible(ctx, vol.Name); err != nil {
			check.AllVolumesAccessible = false
			check.Issues = append(check.Issues, fmt.Sprintf("volume %s not accessible: %v", vol.Name, err))
		}
	}

	// Validate snapshots if they exist
	if len(rvc.state.Snapshots) > 0 {
		if err := ValidateSnapshots(rvc.state.Snapshots); err != nil {
			check.Warnings = append(check.Warnings, fmt.Sprintf("snapshot validation warning: %v", err))
		}
	}

	// Determine readiness
	check.ReadyToRollout = len(check.Issues) == 0

	return check
}
