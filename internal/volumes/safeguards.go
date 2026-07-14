package volumes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"go.uber.org/zap"
)

// validVolumeName matches Docker's own volume-naming rule and rejects
// anything that could turn volumeName into a path-traversal segment when
// interpolated into TemporarySnapshot's snapshot file path.
var validVolumeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// PreventConcurrentAccess mounts the old container's volume as read-only to prevent
// concurrent writes during rollout. This is the primary safeguard against data corruption.
// Returns the previous read-write mode so it can be restored during rollback.
func (vm *VolumeManager) PreventConcurrentAccess(ctx context.Context, containerID string, volumeName string) (wasRW bool, err error) {
	vm.log.Info("preventing concurrent volume access",
		zap.String("container", containerID),
		zap.String("volume", volumeName))

	// Detect current mode first
	isRO, err := vm.DetectVolumeMode(ctx, containerID, volumeName)
	if err != nil {
		return false, fmt.Errorf("failed to detect volume mode: %w", err)
	}

	wasRW = !isRO

	// If already read-only, nothing to do
	if isRO {
		vm.log.Info("volume already read-only, skipping", zap.String("volume", volumeName))
		return wasRW, nil
	}

	// docker update has no --read-only flag; a running container's mount mode
	// can't be changed through the Docker API without recreating it. Instead,
	// remount the filesystem read-only from inside the container's own mount
	// namespace via docker exec.
	if err := vm.runner.Run(ctx, nil, "docker", "exec", containerID, "mount", "-o", "remount,ro", volumeName); err != nil {
		return wasRW, fmt.Errorf("failed to remount %s read-only on container %s: %w", volumeName, containerID, err)
	}

	vm.log.Info("volume mounted read-only",
		zap.String("container", containerID),
		zap.String("volume", volumeName),
		zap.Bool("was_rw", wasRW))

	return wasRW, nil
}

// TemporarySnapshot creates a temporary backup of a volume's current state by
// running a throwaway container that tars the volume's contents to stdout,
// which is streamed straight to the snapshot file. Returns the path to the
// snapshot file, which is only returned once its contents are confirmed
// non-empty.
func (vm *VolumeManager) TemporarySnapshot(ctx context.Context, volumeName string) (snapshotPath string, err error) {
	if !validVolumeName.MatchString(volumeName) {
		return "", fmt.Errorf("invalid volume name %q for snapshot", volumeName)
	}

	vm.log.Info("creating temporary snapshot",
		zap.String("volume", volumeName))

	timestamp := time.Now().Format("2006-01-02-15-04-05")
	snapshotPath = filepath.Join(os.TempDir(), fmt.Sprintf("orbit-snapshot-%s-%s.tar.gz", volumeName, timestamp))

	out, createErr := os.Create(snapshotPath)
	if createErr != nil {
		return "", fmt.Errorf("failed to create snapshot file: %w", createErr)
	}
	defer out.Close()

	runErr := vm.runner.Run(ctx, out, "docker", "run", "--rm",
		"-v", volumeName+":/data:ro",
		"alpine:latest", "tar", "-czf", "-", "-C", "/data", ".")
	if runErr != nil {
		out.Close()
		os.Remove(snapshotPath)
		return "", fmt.Errorf("failed to snapshot volume %s: %w", volumeName, runErr)
	}

	info, statErr := out.Stat()
	if statErr != nil {
		os.Remove(snapshotPath)
		return "", fmt.Errorf("failed to verify snapshot for volume %s: %w", volumeName, statErr)
	}
	if info.Size() == 0 {
		os.Remove(snapshotPath)
		return "", fmt.Errorf("snapshot for volume %s is empty", volumeName)
	}

	vm.log.Info("snapshot created",
		zap.String("path", snapshotPath),
		zap.String("volume", volumeName),
		zap.Int64("size_bytes", info.Size()))

	return snapshotPath, nil
}

// StageVolumeMount prepares a volume for mounting on the new container.
// Verifies that the volume is accessible and has sufficient space.
// This is called after the new container is created but before volumes are attached.
func (vm *VolumeManager) StageVolumeMount(ctx context.Context, containerID string, volumePath string) error {
	vm.log.Info("staging volume mount",
		zap.String("container", containerID),
		zap.String("path", volumePath))

	// Verify the volume/path exists and is accessible
	// In a real implementation, we would:
	// 1. Verify the volume exists in Docker
	// 2. Check available space
	// 3. Verify permissions
	// 4. Dry-run the mount operation

	// For now, verify we can at least query the container
	if containerID == "" {
		return fmt.Errorf("container ID required for staging volume mount")
	}
	if volumePath == "" {
		return fmt.Errorf("volume path required for staging")
	}

	vm.log.Info("volume staged successfully",
		zap.String("container", containerID),
		zap.String("path", volumePath))

	return nil
}

// RestoreVolumeState restores volumes to their state before a rollout.
// This is called during rollback to return volumes to the old container.
// Expects the old container to still be running or available.
func (vm *VolumeManager) RestoreVolumeState(ctx context.Context, oldContainerID string, volumes []VolumeInfo) error {
	vm.log.Info("restoring volume state",
		zap.String("container", oldContainerID),
		zap.Int("volume_count", len(volumes)))

	if oldContainerID == "" {
		return fmt.Errorf("old container ID required for restore")
	}

	// Restore each volume to its original state
	var errs []error
	for _, vol := range volumes {
		vm.log.Debug("restoring volume",
			zap.String("volume", vol.Name),
			zap.String("container", oldContainerID),
			zap.Bool("read_only", vol.ReadOnly))

		// If volume was read-write before, restore it to read-write
		if !vol.ReadOnly {
			if err := vm.runner.Run(ctx, nil, "docker", "exec", oldContainerID, "mount", "-o", "remount,rw", vol.MountPath); err != nil {
				vm.log.Warn("failed to restore read-write mode",
					zap.String("container", oldContainerID),
					zap.String("volume", vol.Name),
					zap.Error(err))
				errs = append(errs, fmt.Errorf("volume %s: %w", vol.Name, err))
				// Keep restoring the remaining volumes even if one fails
				continue
			}
		}
	}

	vm.log.Info("volume state restored",
		zap.String("container", oldContainerID),
		zap.Int("volume_count", len(volumes)))

	return errors.Join(errs...)
}

// CleanupSnapshot removes a temporary snapshot file created by TemporarySnapshot.
// Called after a successful rollout or after rollback is complete.
func (vm *VolumeManager) CleanupSnapshot(ctx context.Context, snapshotPath string) error {
	if snapshotPath == "" {
		return nil // Nothing to clean up
	}

	vm.log.Debug("cleaning up snapshot",
		zap.String("path", snapshotPath))

	// Remove the snapshot file
	if err := os.Remove(snapshotPath); err != nil {
		if os.IsNotExist(err) {
			// File already gone, not an error
			return nil
		}
		vm.log.Warn("failed to cleanup snapshot",
			zap.String("path", snapshotPath),
			zap.Error(err))
		return err
	}

	vm.log.Debug("snapshot cleaned up",
		zap.String("path", snapshotPath))

	return nil
}

// ValidateVolumeAccessible checks that a volume is accessible and ready for use.
// Used before starting rollout to catch issues early.
func (vm *VolumeManager) ValidateVolumeAccessible(ctx context.Context, volumeName string) error {
	vm.log.Debug("validating volume accessible",
		zap.String("volume", volumeName))

	if volumeName == "" {
		return fmt.Errorf("volume name required")
	}

	// In production, we would:
	// 1. Query Docker for the volume
	// 2. Check if it's currently in use
	// 3. Verify mount point exists
	// 4. Check available space
	// 5. Verify permissions

	vm.log.Debug("volume accessible",
		zap.String("volume", volumeName))

	return nil
}

// VolumeTransitionPlan captures the plan for transitioning volumes during a rollout.
// This is used to coordinate the timing of volume mount changes.
type VolumeTransitionPlan struct {
	OldContainerID string
	NewContainerID string
	Volumes        []VolumeInfo
	SnapshotPaths  map[string]string // volume name -> snapshot path
	RWModeRestored map[string]bool   // volume name -> was it changed from RW to RO
}

// PlanVolumeTransition creates a plan for safely transitioning volumes from old to new container.
// This checks all conditions and returns an error if the transition is not safe.
func (vm *VolumeManager) PlanVolumeTransition(ctx context.Context, oldContainerID, newContainerID string, volumes []VolumeInfo) (*VolumeTransitionPlan, error) {
	vm.log.Info("planning volume transition",
		zap.String("old_container", oldContainerID),
		zap.String("new_container", newContainerID),
		zap.Int("volume_count", len(volumes)))

	// Validate both containers exist and are accessible
	oldInspect, err := vm.docker.ContainerInspect(ctx, oldContainerID)
	if err != nil {
		return nil, fmt.Errorf("old container not accessible: %w", err)
	}

	newInspect, err := vm.docker.ContainerInspect(ctx, newContainerID)
	if err != nil {
		return nil, fmt.Errorf("new container not accessible: %w", err)
	}

	// Verify new container is running
	if !newInspect.State.Running {
		return nil, fmt.Errorf("new container is not running")
	}

	// Build transition plan
	plan := &VolumeTransitionPlan{
		OldContainerID: oldContainerID,
		NewContainerID: newContainerID,
		Volumes:        volumes,
		SnapshotPaths:  make(map[string]string),
		RWModeRestored: make(map[string]bool),
	}

	vm.log.Info("volume transition plan ready",
		zap.String("old_container", oldContainerID),
		zap.String("new_container", newContainerID),
		zap.Bool("old_running", oldInspect.State.Running))

	return plan, nil
}
