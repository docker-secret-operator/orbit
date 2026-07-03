package volumes

import "time"

// VolumeInfo represents a Docker volume mounted in a container
type VolumeInfo struct {
	Name       string
	MountPath  string
	ReadOnly   bool
	Containers []string
	Driver     string
	CreatedAt  time.Time
}

// VolumeInventory represents all volumes for a service at a point in time
type VolumeInventory struct {
	Service      string
	Volumes      []VolumeInfo
	SnapshotTime time.Time
	TotalSize    int64
}

// VolumeState represents volume state during rollout
type VolumeState struct {
	VolumeName     string
	OwnerContainer string
	Mode           string
	SizeBytes      int64
	LastModified   time.Time
	SnapshotPath   string
	TransitionSafe bool
	UnsafeReason   string
}

// RolloutVolumeState tracks volume state during a rollout
type RolloutVolumeState struct {
	Service      string
	OldContainer string
	NewContainer string
	InitialState map[string]*VolumeState
	CurrentState map[string]*VolumeState
	Snapshots    map[string]string
	StartTime    time.Time
}

// VolumeSnapshot represents a persisted snapshot of a volume's state at rollout time.
// Stored in the rollout state file for recovery and rollback.
type VolumeSnapshot struct {
	Name           string    `json:"name"`                    // Docker volume name
	MountPath      string    `json:"mount_path"`              // Container mount point
	Mode           string    `json:"mode"`                    // "rw" or "ro"
	OwnerContainer string    `json:"owner_container"`         // Container that owns this volume
	SnapshotPath   string    `json:"snapshot_path,omitempty"` // Path to backup if created
	SizeBytes      int64     `json:"size_bytes"`              // Volume size at snapshot time
	LastModified   time.Time `json:"last_modified"`           // When volume was last written
	SnapshotTime   time.Time `json:"snapshot_time"`           // When snapshot was taken
}
