package volumes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// SnapshotBuilder captures the current state of volumes for persistence.
// Used during rollout to create a snapshot that can be restored if rollback is needed.
type SnapshotBuilder struct {
	vm        *VolumeManager
	service   string
	log       *zap.Logger
	snapshots map[string]*VolumeSnapshot
}

// NewSnapshotBuilder creates a builder for capturing volume snapshots.
func (vm *VolumeManager) NewSnapshotBuilder(service string) *SnapshotBuilder {
	return &SnapshotBuilder{
		vm:        vm,
		service:   service,
		log:       vm.log,
		snapshots: make(map[string]*VolumeSnapshot),
	}
}

// CaptureVolumes discovers and snapshots all volumes for the service.
// Returns a map of mount path -> snapshot ready for persistence.
func (sb *SnapshotBuilder) CaptureVolumes(ctx context.Context) (map[string]*VolumeSnapshot, error) {
	sb.log.Info("capturing volume snapshots",
		zap.String("service", sb.service))

	// List all volumes for the service
	volumes, err := sb.vm.ListVolumesForService(ctx, sb.service)
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}

	if len(volumes) == 0 {
		sb.log.Info("no volumes found for service",
			zap.String("service", sb.service))
		return sb.snapshots, nil
	}

	// Capture each volume
	now := time.Now()
	for _, vol := range volumes {
		snapshot := &VolumeSnapshot{
			Name:         vol.Name,
			MountPath:    vol.MountPath,
			Mode:         "rw", // Default to rw, can be updated if ro
			SizeBytes:    0,    // Would need Docker API to get actual size
			LastModified: now,
			SnapshotTime: now,
		}

		// Set owner container (first one if multiple)
		if len(vol.Containers) > 0 {
			snapshot.OwnerContainer = vol.Containers[0]
		}

		// Set mode
		if vol.ReadOnly {
			snapshot.Mode = "ro"
		}

		sb.snapshots[vol.MountPath] = snapshot
		sb.log.Debug("captured volume snapshot",
			zap.String("name", vol.Name),
			zap.String("mount_path", vol.MountPath),
			zap.String("mode", snapshot.Mode))
	}

	sb.log.Info("volume snapshots captured",
		zap.String("service", sb.service),
		zap.Int("count", len(sb.snapshots)))

	return sb.snapshots, nil
}

// SerializeToMap converts snapshots to a JSON-serializable map.
// Used when persisting to rollout state file.
func (sb *SnapshotBuilder) SerializeToMap() map[string]interface{} {
	result := make(map[string]interface{})
	for path, snapshot := range sb.snapshots {
		result[path] = snapshot
	}
	return result
}

// DeserializeSnapshots reconstructs volume snapshots from persisted data.
// Used when loading rollout state for recovery or rollback.
func DeserializeSnapshots(data map[string]interface{}) (map[string]*VolumeSnapshot, error) {
	snapshots := make(map[string]*VolumeSnapshot)

	for path, rawData := range data {
		// Marshal and unmarshal to convert map to struct
		jsonBytes, err := json.Marshal(rawData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal snapshot for %s: %w", path, err)
		}

		var snapshot VolumeSnapshot
		if err := json.Unmarshal(jsonBytes, &snapshot); err != nil {
			return nil, fmt.Errorf("failed to unmarshal snapshot for %s: %w", path, err)
		}

		snapshots[path] = &snapshot
	}

	return snapshots, nil
}

// PersistSnapshots stores volume snapshots in the given map (for state persistence).
// This wraps the snapshots in the proper JSON structure.
func PersistSnapshots(snapshots map[string]*VolumeSnapshot) map[string]interface{} {
	result := make(map[string]interface{})
	for path, snapshot := range snapshots {
		result[path] = snapshot
	}
	return result
}

// groupVolumesByOwner buckets snapshot-derived VolumeInfo by owner container,
// so a multi-container service restores each container's own volumes instead
// of applying one container's volumes to all owners.
func groupVolumesByOwner(snapshots map[string]*VolumeSnapshot) map[string][]VolumeInfo {
	groups := make(map[string][]VolumeInfo)
	for _, snapshot := range snapshots {
		if snapshot.OwnerContainer == "" {
			continue
		}
		vol := VolumeInfo{
			Name:       snapshot.Name,
			MountPath:  snapshot.MountPath,
			ReadOnly:   snapshot.Mode == "ro",
			Containers: []string{snapshot.OwnerContainer},
		}
		groups[snapshot.OwnerContainer] = append(groups[snapshot.OwnerContainer], vol)
	}
	return groups
}

// RestoreFromSnapshots recovers volume state from persisted snapshots.
// Called during rollback to restore volumes to their pre-rollout state.
func (vm *VolumeManager) RestoreFromSnapshots(ctx context.Context, snapshots map[string]*VolumeSnapshot) error {
	vm.log.Info("restoring volumes from snapshots",
		zap.Int("snapshot_count", len(snapshots)))

	if len(snapshots) == 0 {
		return nil
	}

	// Restore each owner container's own volumes independently, since a
	// service can have multiple containers each owning distinct volumes.
	for owner, volumes := range groupVolumesByOwner(snapshots) {
		if err := vm.RestoreVolumeState(ctx, owner, volumes); err != nil {
			vm.log.Warn("failed to restore volume state",
				zap.String("container", owner),
				zap.Error(err))
			// Don't fail the entire restore if one container has issues
		}
	}

	vm.log.Info("volumes restored from snapshots",
		zap.Int("count", len(snapshots)))

	return nil
}

// SnapshotStats provides statistics about captured snapshots.
type SnapshotStats struct {
	TotalVolumes   int
	TotalSizeBytes int64
	ReadWriteCount int
	ReadOnlyCount  int
	CapturedAt     time.Time
}

// GetStats returns statistics about the captured snapshots.
func (sb *SnapshotBuilder) GetStats() SnapshotStats {
	stats := SnapshotStats{
		TotalVolumes: len(sb.snapshots),
		CapturedAt:   time.Now(),
	}

	for _, snapshot := range sb.snapshots {
		stats.TotalSizeBytes += snapshot.SizeBytes
		if snapshot.Mode == "ro" {
			stats.ReadOnlyCount++
		} else {
			stats.ReadWriteCount++
		}
	}

	return stats
}

// ValidateSnapshots checks that all snapshots are valid and consistent.
// Called before using snapshots for recovery.
func ValidateSnapshots(snapshots map[string]*VolumeSnapshot) error {
	if len(snapshots) == 0 {
		return nil // No volumes is valid
	}

	for path, snapshot := range snapshots {
		if snapshot == nil {
			return fmt.Errorf("nil snapshot for path %s", path)
		}
		if snapshot.Name == "" {
			return fmt.Errorf("snapshot for path %s has empty name", path)
		}
		if snapshot.Mode != "rw" && snapshot.Mode != "ro" {
			return fmt.Errorf("invalid mode for volume %s: %s", snapshot.Name, snapshot.Mode)
		}
		if snapshot.SnapshotTime.IsZero() {
			return fmt.Errorf("snapshot for volume %s has zero time", snapshot.Name)
		}
	}

	return nil
}
