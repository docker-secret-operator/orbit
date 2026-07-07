package volumes

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/volume"
	"go.uber.org/zap"
)

// MockDockerClient implements DockerClient interface for testing
type MockDockerClient struct {
	ContainerListFn    func(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error)
	ContainerInspectFn func(ctx context.Context, containerID string) (types.ContainerJSON, error)
	VolumeInspectFn    func(ctx context.Context, name string) (volume.Volume, error)
}

func (m *MockDockerClient) ContainerList(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error) {
	if m.ContainerListFn == nil {
		return nil, fmt.Errorf("ContainerList not mocked")
	}
	return m.ContainerListFn(ctx, options)
}

func (m *MockDockerClient) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	if m.ContainerInspectFn == nil {
		return types.ContainerJSON{}, fmt.Errorf("ContainerInspect not mocked")
	}
	return m.ContainerInspectFn(ctx, containerID)
}

func (m *MockDockerClient) VolumeInspect(ctx context.Context, name string) (volume.Volume, error) {
	if m.VolumeInspectFn == nil {
		return volume.Volume{}, fmt.Errorf("VolumeInspect not mocked")
	}
	return m.VolumeInspectFn(ctx, name)
}

// fakeCommandRunner records invocations and returns configured results instead
// of touching the real Docker daemon.
type fakeCommandRunner struct {
	calls   [][]string
	err     error
	errFor  map[string]error // keyed by strings.Join(args, " ")
	stdout  []byte           // written to the caller's stdout writer, if any
}

func (f *fakeCommandRunner) Run(ctx context.Context, stdout io.Writer, name string, args ...string) error {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)

	if f.errFor != nil {
		if err, ok := f.errFor[strings.Join(args, " ")]; ok && err != nil {
			return err
		}
	}

	if stdout != nil && f.stdout != nil {
		if _, err := stdout.Write(f.stdout); err != nil {
			return err
		}
	}

	return f.err
}

func TestNewVolumeManager(t *testing.T) {
	logger, _ := zap.NewProduction()
	vm := NewVolumeManager(nil, logger)
	if vm == nil {
		t.Fatal("NewVolumeManager returned nil")
	}
	if vm.log == nil {
		t.Fatal("log not initialized")
	}
}

func TestNewVolumeManagerWithNilLogger(t *testing.T) {
	vm := NewVolumeManager(nil, nil)
	if vm == nil {
		t.Fatal("NewVolumeManager returned nil")
	}
	if vm.log == nil {
		t.Fatal("log should be initialized with nop logger")
	}
}

func TestVolumeInfoStructure(t *testing.T) {
	vol := VolumeInfo{
		Name:      "test_vol",
		MountPath: "/data",
		ReadOnly:  false,
	}
	if vol.Name != "test_vol" {
		t.Fatal("Name not set correctly")
	}
	if vol.MountPath != "/data" {
		t.Fatal("MountPath not set correctly")
	}
	if vol.ReadOnly {
		t.Fatal("ReadOnly should be false")
	}
}

func TestVolumeInventoryStructure(t *testing.T) {
	vols := []VolumeInfo{
		{Name: "vol1", MountPath: "/data"},
		{Name: "vol2", MountPath: "/logs"},
	}
	inventory := &VolumeInventory{
		Service: "db",
		Volumes: vols,
	}
	if inventory.Service != "db" {
		t.Fatal("Service not set correctly")
	}
	if len(inventory.Volumes) != 2 {
		t.Fatal("Volumes count incorrect")
	}
}

func TestVolumeStateStructure(t *testing.T) {
	state := &VolumeState{
		VolumeName:     "test_vol",
		OwnerContainer: "container-1",
		Mode:           "rw",
		TransitionSafe: true,
	}
	if state.VolumeName != "test_vol" {
		t.Fatal("VolumeName not set")
	}
	if !state.TransitionSafe {
		t.Fatal("TransitionSafe not set")
	}
}

func TestRolloutVolumeStateStructure(t *testing.T) {
	rvs := &RolloutVolumeState{
		Service:      "postgres",
		OldContainer: "pg-old",
		NewContainer: "pg-new",
		InitialState: make(map[string]*VolumeState),
		CurrentState: make(map[string]*VolumeState),
		Snapshots:    make(map[string]string),
	}
	if rvs.Service != "postgres" {
		t.Fatal("Service not set")
	}
	if rvs.OldContainer != "pg-old" {
		t.Fatal("OldContainer not set")
	}
	if len(rvs.InitialState) != 0 {
		t.Fatal("InitialState should be empty")
	}
}

// Integration tests (require Docker)
func TestListVolumesForService_NilClient(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	vm := NewVolumeManager(nil, logger)
	ctx := context.Background()

	_, err := vm.ListVolumesForService(ctx, "test")
	if err == nil {
		t.Fatal("expected error with nil docker client")
	}
}

func TestDetectVolumeMode_NilClient(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	vm := NewVolumeManager(nil, logger)
	ctx := context.Background()

	_, err := vm.DetectVolumeMode(ctx, "container-id", "/data")
	if err == nil {
		t.Fatal("expected error with nil docker client")
	}
}

func TestTrackVolumeState_NilClient(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	vm := NewVolumeManager(nil, logger)
	ctx := context.Background()

	_, err := vm.TrackVolumeState(ctx, "service")
	if err == nil {
		t.Fatal("expected error with nil docker client")
	}
}

// ListVolumesForService tests with mock
func TestListVolumesForService_NoContainers(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerListFn: func(ctx context.Context, opts types.ContainerListOptions) ([]types.Container, error) {
			return []types.Container{}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	vols, err := vm.ListVolumesForService(ctx, "db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vols) != 0 {
		t.Fatalf("expected 0 volumes, got %d", len(vols))
	}
}

func TestListVolumesForService_SingleContainer(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerListFn: func(ctx context.Context, opts types.ContainerListOptions) ([]types.Container, error) {
			return []types.Container{
				{
					ID:    "container-1",
					Names: []string{"/db"},
					Labels: map[string]string{
						"com.docker.compose.service": "db",
					},
				},
			}, nil
		},
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					ID: "container-1",
					State: &types.ContainerState{
						Running: true,
					},
				},
				Mounts: []types.MountPoint{
					{
						Name:        "db_data",
						Destination: "/var/lib/postgresql/data",
						Driver:      "local",
						RW:          true,
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	vols, err := vm.ListVolumesForService(ctx, "db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vols) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(vols))
	}
	if vols[0].Name != "db_data" {
		t.Fatalf("expected volume name 'db_data', got '%s'", vols[0].Name)
	}
	if vols[0].MountPath != "/var/lib/postgresql/data" {
		t.Fatalf("expected mount path '/var/lib/postgresql/data', got '%s'", vols[0].MountPath)
	}
	if vols[0].ReadOnly {
		t.Fatal("expected volume to be read-write")
	}
}

func TestListVolumesForService_MultipleContainers(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerListFn: func(ctx context.Context, opts types.ContainerListOptions) ([]types.Container, error) {
			return []types.Container{
				{
					ID:     "container-1",
					Labels: map[string]string{"com.docker.compose.service": "db"},
				},
				{
					ID:     "container-2",
					Labels: map[string]string{"com.docker.compose.service": "db"},
				},
			}, nil
		},
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			if containerID == "container-1" {
				return types.ContainerJSON{
					ContainerJSONBase: &types.ContainerJSONBase{ID: "container-1"},
					Mounts: []types.MountPoint{
						{
							Name:        "shared_vol",
							Destination: "/data",
							Driver:      "local",
							RW:          true,
						},
					},
				}, nil
			}
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{ID: "container-2"},
				Mounts: []types.MountPoint{
					{
						Name:        "shared_vol",
						Destination: "/data",
						Driver:      "local",
						RW:          true,
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	vols, err := vm.ListVolumesForService(ctx, "db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vols) != 1 {
		t.Fatalf("expected 1 volume (shared), got %d", len(vols))
	}
	if len(vols[0].Containers) != 2 {
		t.Fatalf("expected volume to be mounted on 2 containers, got %d", len(vols[0].Containers))
	}
}

func TestDetectVolumeMode_RW(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{ID: "container-1"},
				Mounts: []types.MountPoint{
					{
						Name:        "data",
						Destination: "/data",
						RW:          true,
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	isRO, err := vm.DetectVolumeMode(ctx, "container-1", "/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isRO {
		t.Fatal("expected volume to be RW (isRO=false)")
	}
}

func TestDetectVolumeMode_RO(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{ID: "container-1"},
				Mounts: []types.MountPoint{
					{
						Name:        "data",
						Destination: "/data",
						RW:          false,
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	isRO, err := vm.DetectVolumeMode(ctx, "container-1", "/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isRO {
		t.Fatal("expected volume to be RO (isRO=true)")
	}
}

func TestDetectVolumeMode_NotFound(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{ID: "container-1"},
				Mounts:            []types.MountPoint{},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	_, err := vm.DetectVolumeMode(ctx, "container-1", "/nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent volume")
	}
}

func TestTrackVolumeState_HappyPath(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerListFn: func(ctx context.Context, opts types.ContainerListOptions) ([]types.Container, error) {
			return []types.Container{
				{
					ID:     "container-1",
					Labels: map[string]string{"com.docker.compose.service": "db"},
				},
			}, nil
		},
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{ID: "container-1"},
				Mounts: []types.MountPoint{
					{
						Name:        "data",
						Destination: "/data",
						RW:          true,
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	inv, err := vm.TrackVolumeState(ctx, "db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv.Service != "db" {
		t.Fatalf("expected service 'db', got '%s'", inv.Service)
	}
	if len(inv.Volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(inv.Volumes))
	}
	if inv.SnapshotTime.IsZero() {
		t.Fatal("expected non-zero snapshot time")
	}
}

func TestValidateVolumeTransition_Valid(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					ID: containerID,
					State: &types.ContainerState{
						Running: true,
						Error:   "",
					},
				},
				Mounts: []types.MountPoint{
					{
						Name:        "data",
						Destination: "/data",
						RW:          true,
					},
				},
			}, nil
		},
		VolumeInspectFn: func(ctx context.Context, name string) (volume.Volume, error) {
			return volume.Volume{Name: name, Driver: "local"}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	valid, reason, err := vm.ValidateVolumeTransition(ctx, "old-1", "new-1", []VolumeInfo{
		{Name: "data", MountPath: "/data"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !valid {
		t.Fatalf("expected valid transition, reason: %s", reason)
	}
}

func TestValidateVolumeTransition_NewContainerNotFound(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	callCount := 0
	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			callCount++
			if callCount == 1 {
				// New container not found
				return types.ContainerJSON{}, fmt.Errorf("container not found")
			}
			return types.ContainerJSON{}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	valid, reason, err := vm.ValidateVolumeTransition(ctx, "old-1", "new-1", []VolumeInfo{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Fatal("expected invalid transition")
	}
	if reason == "" {
		t.Fatal("expected reason for invalid transition")
	}
}

func TestValidateVolumeTransition_NewContainerNotRunning(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					ID: containerID,
					State: &types.ContainerState{
						Running: false,
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	valid, reason, err := vm.ValidateVolumeTransition(ctx, "old-1", "new-1", []VolumeInfo{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Fatal("expected invalid transition")
	}
	if reason != "new container is not running" {
		t.Fatalf("expected 'new container is not running', got '%s'", reason)
	}
}

// ============================================================================
// Safeguards Tests
// ============================================================================

func TestPreventConcurrentAccess_AlreadyReadOnly(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{ID: "container-1"},
				Mounts: []types.MountPoint{
					{
						Name:        "data",
						Destination: "/data",
						RW:          false, // Already read-only
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	wasRW, err := vm.PreventConcurrentAccess(ctx, "container-1", "/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wasRW {
		t.Fatal("expected wasRW=false for already read-only volume")
	}
}

func TestPreventConcurrentAccess_WasReadWrite(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{ID: "container-1"},
				Mounts: []types.MountPoint{
					{
						Name:        "data",
						Destination: "/data",
						RW:          true, // Read-write initially
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	fake := &fakeCommandRunner{}
	vm.runner = fake
	ctx := context.Background()

	wasRW, err := vm.PreventConcurrentAccess(ctx, "container-1", "/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wasRW {
		t.Fatal("expected wasRW=true for read-write volume")
	}

	if len(fake.calls) != 1 {
		t.Fatalf("expected exactly one remount command, got %d: %v", len(fake.calls), fake.calls)
	}
	got := fake.calls[0]
	want := []string{"docker", "exec", "container-1", "mount", "-o", "remount,ro", "/data"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("unexpected remount command: got %v, want %v", got, want)
	}
}

func TestPreventConcurrentAccess_RemountFails_ReturnsError(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{ID: "container-1"},
				Mounts: []types.MountPoint{
					{Name: "data", Destination: "/data", RW: true},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	vm.runner = &fakeCommandRunner{err: fmt.Errorf("container has no CAP_SYS_ADMIN")}
	ctx := context.Background()

	_, err := vm.PreventConcurrentAccess(ctx, "container-1", "/data")
	if err == nil {
		t.Fatal("expected error to propagate when remount fails, got nil")
	}
}

func TestTemporarySnapshot_WritesRealFileFromRunnerOutput(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	fake := &fakeCommandRunner{stdout: []byte("fake-tarball-bytes")}
	vm.runner = fake
	ctx := context.Background()

	snapshotPath, err := vm.TemporarySnapshot(ctx, "db_data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(snapshotPath)

	if snapshotPath == "" {
		t.Fatal("expected non-empty snapshot path")
	}
	if !strings.Contains(snapshotPath, "db_data") {
		t.Fatalf("expected snapshot path to contain volume name, got %s", snapshotPath)
	}

	contents, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("expected snapshot file to exist on disk: %v", err)
	}
	if string(contents) != "fake-tarball-bytes" {
		t.Fatalf("expected snapshot file to contain runner output, got %q", string(contents))
	}

	if len(fake.calls) != 1 || fake.calls[0][0] != "docker" || fake.calls[0][1] != "run" {
		t.Fatalf("expected a 'docker run' snapshot command, got %v", fake.calls)
	}
}

func TestTemporarySnapshot_RunnerFails_ReturnsErrorAndNoFile(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	vm.runner = &fakeCommandRunner{err: fmt.Errorf("docker daemon unreachable")}
	ctx := context.Background()

	snapshotPath, err := vm.TemporarySnapshot(ctx, "db_data")
	if err == nil {
		t.Fatal("expected error when snapshot command fails, got nil")
	}
	if snapshotPath != "" {
		t.Fatalf("expected empty snapshot path on failure, got %q", snapshotPath)
	}
}

func TestStageVolumeMount_Valid(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	err := vm.StageVolumeMount(ctx, "container-1", "/var/lib/postgresql/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStageVolumeMount_NoContainer(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	err := vm.StageVolumeMount(ctx, "", "/var/lib/postgresql/data")
	if err == nil {
		t.Fatal("expected error for empty container ID")
	}
}

func TestStageVolumeMount_NoPath(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	err := vm.StageVolumeMount(ctx, "container-1", "")
	if err == nil {
		t.Fatal("expected error for empty volume path")
	}
}

func TestRestoreVolumeState_SingleVolume(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	fake := &fakeCommandRunner{}
	vm.runner = fake
	ctx := context.Background()

	volumes := []VolumeInfo{
		{
			Name:      "db_data",
			MountPath: "/var/lib/postgresql/data",
			ReadOnly:  false,
		},
	}

	err := vm.RestoreVolumeState(ctx, "container-1", volumes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.calls) != 1 {
		t.Fatalf("expected exactly one remount command, got %d: %v", len(fake.calls), fake.calls)
	}
	want := []string{"docker", "exec", "container-1", "mount", "-o", "remount,rw", "/var/lib/postgresql/data"}
	if strings.Join(fake.calls[0], " ") != strings.Join(want, " ") {
		t.Fatalf("unexpected remount command: got %v, want %v", fake.calls[0], want)
	}
}

func TestRestoreVolumeState_MultipleVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	fake := &fakeCommandRunner{}
	vm.runner = fake
	ctx := context.Background()

	volumes := []VolumeInfo{
		{
			Name:      "db_data",
			MountPath: "/var/lib/postgresql/data",
			ReadOnly:  false,
		},
		{
			Name:      "db_logs",
			MountPath: "/var/log/postgresql",
			ReadOnly:  false,
		},
	}

	err := vm.RestoreVolumeState(ctx, "container-1", volumes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.calls) != 2 {
		t.Fatalf("expected a remount command per volume, got %d: %v", len(fake.calls), fake.calls)
	}
}

func TestRestoreVolumeState_PropagatesRemountErrors(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	fake := &fakeCommandRunner{
		errFor: map[string]error{
			"exec container-1 mount -o remount,rw /var/lib/postgresql/data": fmt.Errorf("mount busy"),
		},
	}
	vm.runner = fake
	ctx := context.Background()

	volumes := []VolumeInfo{
		{Name: "db_data", MountPath: "/var/lib/postgresql/data", ReadOnly: false},
		{Name: "db_logs", MountPath: "/var/log/postgresql", ReadOnly: false},
	}

	err := vm.RestoreVolumeState(ctx, "container-1", volumes)
	if err == nil {
		t.Fatal("expected error when a volume fails to restore, got nil")
	}

	// Both volumes should still have been attempted despite the first failing.
	if len(fake.calls) != 2 {
		t.Fatalf("expected both volumes to be attempted, got %d calls: %v", len(fake.calls), fake.calls)
	}
}

func TestRestoreVolumeState_NoContainer(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	err := vm.RestoreVolumeState(ctx, "", []VolumeInfo{})
	if err == nil {
		t.Fatal("expected error for empty container ID")
	}
}

func TestCleanupSnapshot_NoPath(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	// Should not error on empty path
	err := vm.CleanupSnapshot(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCleanupSnapshot_NonexistentFile(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	// Should not error on nonexistent file
	err := vm.CleanupSnapshot(ctx, "/nonexistent/snapshot.tar.gz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVolumeAccessible_Valid(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	err := vm.ValidateVolumeAccessible(ctx, "db_data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVolumeAccessible_NoVolumeName(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	err := vm.ValidateVolumeAccessible(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty volume name")
	}
}

func TestPlanVolumeTransition_Valid(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					ID: containerID,
					State: &types.ContainerState{
						Running: true,
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	volumes := []VolumeInfo{
		{Name: "db_data", MountPath: "/var/lib/postgresql/data", ReadOnly: false},
	}

	plan, err := vm.PlanVolumeTransition(ctx, "old-1", "new-1", volumes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.OldContainerID != "old-1" {
		t.Fatalf("expected old container 'old-1', got '%s'", plan.OldContainerID)
	}
	if plan.NewContainerID != "new-1" {
		t.Fatalf("expected new container 'new-1', got '%s'", plan.NewContainerID)
	}
	if len(plan.Volumes) != 1 {
		t.Fatalf("expected 1 volume in plan, got %d", len(plan.Volumes))
	}
}

func TestPlanVolumeTransition_NewContainerNotRunning(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					ID: containerID,
					State: &types.ContainerState{
						Running: false, // Not running
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	_, err := vm.PlanVolumeTransition(ctx, "old-1", "new-1", []VolumeInfo{})
	if err == nil {
		t.Fatal("expected error for stopped new container")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Fatalf("expected 'not running' in error, got: %v", err)
	}
}

// ============================================================================
// Persistence Tests
// ============================================================================

func TestNewSnapshotBuilder(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)

	builder := vm.NewSnapshotBuilder("postgres")
	if builder == nil {
		t.Fatal("expected non-nil builder")
	}
	if builder.service != "postgres" {
		t.Fatalf("expected service 'postgres', got '%s'", builder.service)
	}
}

func TestCaptureVolumes_NoVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerListFn: func(ctx context.Context, opts types.ContainerListOptions) ([]types.Container, error) {
			return []types.Container{}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	builder := vm.NewSnapshotBuilder("postgres")
	ctx := context.Background()

	snapshots, err := builder.CaptureVolumes(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("expected 0 snapshots, got %d", len(snapshots))
	}
}

func TestCaptureVolumes_SingleVolume(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerListFn: func(ctx context.Context, opts types.ContainerListOptions) ([]types.Container, error) {
			return []types.Container{
				{
					ID:     "postgres-1",
					Labels: map[string]string{"com.docker.compose.service": "postgres"},
				},
			}, nil
		},
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{ID: "postgres-1"},
				Mounts: []types.MountPoint{
					{
						Name:        "postgres_data",
						Destination: "/var/lib/postgresql/data",
						RW:          true,
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	builder := vm.NewSnapshotBuilder("postgres")
	ctx := context.Background()

	snapshots, err := builder.CaptureVolumes(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}

	snapshot, ok := snapshots["/var/lib/postgresql/data"]
	if !ok {
		t.Fatal("expected snapshot for /var/lib/postgresql/data")
	}
	if snapshot.Name != "postgres_data" {
		t.Fatalf("expected name 'postgres_data', got '%s'", snapshot.Name)
	}
	if snapshot.Mode != "rw" {
		t.Fatalf("expected mode 'rw', got '%s'", snapshot.Mode)
	}
	if snapshot.OwnerContainer != "postgres-1" {
		t.Fatalf("expected owner 'postgres-1', got '%s'", snapshot.OwnerContainer)
	}
}

func TestCaptureVolumes_MultipleVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerListFn: func(ctx context.Context, opts types.ContainerListOptions) ([]types.Container, error) {
			return []types.Container{
				{
					ID:     "postgres-1",
					Labels: map[string]string{"com.docker.compose.service": "postgres"},
				},
			}, nil
		},
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{ID: "postgres-1"},
				Mounts: []types.MountPoint{
					{
						Name:        "postgres_data",
						Destination: "/var/lib/postgresql/data",
						RW:          true,
					},
					{
						Name:        "postgres_logs",
						Destination: "/var/log/postgresql",
						RW:          false,
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	builder := vm.NewSnapshotBuilder("postgres")
	ctx := context.Background()

	snapshots, err := builder.CaptureVolumes(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapshots))
	}

	// Check data volume
	dataSnapshot := snapshots["/var/lib/postgresql/data"]
	if dataSnapshot == nil || dataSnapshot.Mode != "rw" {
		t.Fatal("expected data volume with mode 'rw'")
	}

	// Check logs volume
	logsSnapshot := snapshots["/var/log/postgresql"]
	if logsSnapshot == nil || logsSnapshot.Mode != "ro" {
		t.Fatal("expected logs volume with mode 'ro'")
	}
}

func TestSerializeToMap(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	builder := vm.NewSnapshotBuilder("postgres")

	// Manually add snapshots
	builder.snapshots["/data"] = &VolumeSnapshot{
		Name:      "db_data",
		MountPath: "/data",
		Mode:      "rw",
	}

	serialized := builder.SerializeToMap()
	if len(serialized) != 1 {
		t.Fatalf("expected 1 item in serialized map, got %d", len(serialized))
	}

	val, ok := serialized["/data"]
	if !ok {
		t.Fatal("expected /data in serialized map")
	}
	if val == nil {
		t.Fatal("expected non-nil value for /data")
	}
}

func TestDeserializeSnapshots(t *testing.T) {
	data := map[string]interface{}{
		"/data": map[string]interface{}{
			"name":       "db_data",
			"mount_path": "/data",
			"mode":       "rw",
			"size_bytes": int64(1000),
		},
	}

	snapshots, err := DeserializeSnapshots(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}

	snapshot := snapshots["/data"]
	if snapshot == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snapshot.Name != "db_data" {
		t.Fatalf("expected name 'db_data', got '%s'", snapshot.Name)
	}
	if snapshot.Mode != "rw" {
		t.Fatalf("expected mode 'rw', got '%s'", snapshot.Mode)
	}
}

func TestPersistSnapshots(t *testing.T) {
	snapshots := map[string]*VolumeSnapshot{
		"/data": {
			Name:      "db_data",
			MountPath: "/data",
			Mode:      "rw",
		},
	}

	persisted := PersistSnapshots(snapshots)
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted item, got %d", len(persisted))
	}
	if _, ok := persisted["/data"]; !ok {
		t.Fatal("expected /data in persisted map")
	}
}

func TestRestoreFromSnapshots(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	ctx := context.Background()

	snapshots := map[string]*VolumeSnapshot{
		"/data": {
			Name:           "db_data",
			MountPath:      "/data",
			Mode:           "rw",
			OwnerContainer: "postgres-1",
		},
	}

	err := vm.RestoreFromSnapshots(ctx, snapshots)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRestoreFromSnapshots_MultipleOwnerContainers(t *testing.T) {
	snapshots := map[string]*VolumeSnapshot{
		"/data/db": {
			Name:           "db_data",
			MountPath:      "/data/db",
			Mode:           "rw",
			OwnerContainer: "container-a",
		},
		"/data/cache": {
			Name:           "cache_data",
			MountPath:      "/data/cache",
			Mode:           "rw",
			OwnerContainer: "container-b",
		},
	}

	groups := groupVolumesByOwner(snapshots)

	if len(groups) != 2 {
		t.Fatalf("expected 2 owner containers, got %d: %+v", len(groups), groups)
	}

	aVolumes, ok := groups["container-a"]
	if !ok || len(aVolumes) != 1 || aVolumes[0].Name != "db_data" {
		t.Fatalf("expected container-a to own only db_data, got %+v", aVolumes)
	}

	bVolumes, ok := groups["container-b"]
	if !ok || len(bVolumes) != 1 || bVolumes[0].Name != "cache_data" {
		t.Fatalf("expected container-b to own only cache_data, got %+v", bVolumes)
	}
}

func TestGetStats(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	builder := vm.NewSnapshotBuilder("postgres")

	builder.snapshots["/data"] = &VolumeSnapshot{
		Name:      "db_data",
		MountPath: "/data",
		Mode:      "rw",
		SizeBytes: 1000000,
	}
	builder.snapshots["/logs"] = &VolumeSnapshot{
		Name:      "db_logs",
		MountPath: "/logs",
		Mode:      "ro",
		SizeBytes: 500000,
	}

	stats := builder.GetStats()
	if stats.TotalVolumes != 2 {
		t.Fatalf("expected 2 volumes, got %d", stats.TotalVolumes)
	}
	if stats.TotalSizeBytes != 1500000 {
		t.Fatalf("expected 1500000 bytes, got %d", stats.TotalSizeBytes)
	}
	if stats.ReadWriteCount != 1 {
		t.Fatalf("expected 1 rw volume, got %d", stats.ReadWriteCount)
	}
	if stats.ReadOnlyCount != 1 {
		t.Fatalf("expected 1 ro volume, got %d", stats.ReadOnlyCount)
	}
}

func TestValidateSnapshots_Valid(t *testing.T) {
	snapshots := map[string]*VolumeSnapshot{
		"/data": {
			Name:         "db_data",
			MountPath:    "/data",
			Mode:         "rw",
			SnapshotTime: time.Now(),
		},
	}

	err := ValidateSnapshots(snapshots)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSnapshots_InvalidMode(t *testing.T) {
	snapshots := map[string]*VolumeSnapshot{
		"/data": {
			Name:         "db_data",
			Mode:         "invalid",
			SnapshotTime: time.Now(),
		},
	}

	err := ValidateSnapshots(snapshots)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestValidateSnapshots_ZeroTime(t *testing.T) {
	snapshots := map[string]*VolumeSnapshot{
		"/data": {
			Name: "db_data",
			Mode: "rw",
			// SnapshotTime is zero
		},
	}

	err := ValidateSnapshots(snapshots)
	if err == nil {
		t.Fatal("expected error for zero snapshot time")
	}
}

// ============================================================================
// Integration Tests - E2E Volume Rollout Flow
// ============================================================================

func TestRolloutVolumeCoordinator_Creation(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)

	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	if coordinator == nil {
		t.Fatal("expected non-nil coordinator")
	}
	if coordinator.state.Service != "postgres" {
		t.Fatalf("expected service 'postgres', got '%s'", coordinator.state.Service)
	}
}

func TestPrepareForRollout_NoVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerListFn: func(ctx context.Context, opts types.ContainerListOptions) ([]types.Container, error) {
			return []types.Container{}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	ctx := context.Background()

	err := coordinator.PrepareForRollout(ctx, "postgres-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(coordinator.state.Volumes) != 0 {
		t.Fatalf("expected 0 volumes, got %d", len(coordinator.state.Volumes))
	}
}

func TestPrepareForRollout_WithVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerListFn: func(ctx context.Context, opts types.ContainerListOptions) ([]types.Container, error) {
			return []types.Container{
				{
					ID:     "postgres-1",
					Labels: map[string]string{"com.docker.compose.service": "postgres"},
				},
			}, nil
		},
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{ID: "postgres-1"},
				Mounts: []types.MountPoint{
					{
						Name:        "postgres_data",
						Destination: "/var/lib/postgresql/data",
						RW:          true,
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	ctx := context.Background()

	err := coordinator.PrepareForRollout(ctx, "postgres-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(coordinator.state.Volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(coordinator.state.Volumes))
	}
	if len(coordinator.state.Snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(coordinator.state.Snapshots))
	}
}

func TestValidateNewContainer_NoVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	coordinator.state.Volumes = []VolumeInfo{}
	ctx := context.Background()

	err := coordinator.ValidateNewContainer(ctx, "postgres-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateNewContainer_WithVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{
		ContainerInspectFn: func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
			return types.ContainerJSON{
				ContainerJSONBase: &types.ContainerJSONBase{
					ID: containerID,
					State: &types.ContainerState{
						Running: true,
					},
				},
			}, nil
		},
	}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	coordinator.state.OldContainerID = "postgres-1"
	coordinator.state.Volumes = []VolumeInfo{
		{Name: "postgres_data", MountPath: "/data", ReadOnly: false},
	}
	ctx := context.Background()

	err := coordinator.ValidateNewContainer(ctx, "postgres-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if coordinator.state.NewContainerID != "postgres-2" {
		t.Fatalf("expected new container 'postgres-2', got '%s'", coordinator.state.NewContainerID)
	}
}

func TestCompleteTransition_NoVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	coordinator.state.Volumes = []VolumeInfo{}
	ctx := context.Background()

	err := coordinator.CompleteTransition(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompleteTransition_WithVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	coordinator.state.OldContainerID = "postgres-1"
	coordinator.state.NewContainerID = "postgres-2"
	coordinator.state.Volumes = []VolumeInfo{
		{Name: "postgres_data", MountPath: "/data", ReadOnly: false},
	}
	coordinator.state.Snapshots = map[string]*VolumeSnapshot{
		"/data": {
			Name:      "postgres_data",
			MountPath: "/data",
		},
	}
	ctx := context.Background()

	err := coordinator.CompleteTransition(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRollback_NoVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	coordinator.state.Volumes = []VolumeInfo{}
	ctx := context.Background()

	err := coordinator.Rollback(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRollback_WithVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	coordinator.state.OldContainerID = "postgres-1"
	coordinator.state.NewContainerID = "postgres-2"
	coordinator.state.Volumes = []VolumeInfo{
		{Name: "postgres_data", MountPath: "/data", ReadOnly: false},
	}
	coordinator.state.Snapshots = map[string]*VolumeSnapshot{
		"/data": {
			Name:           "postgres_data",
			MountPath:      "/data",
			OwnerContainer: "postgres-1",
		},
	}
	ctx := context.Background()

	err := coordinator.Rollback(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetTransitionState(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	coordinator.state.OldContainerID = "postgres-1"
	coordinator.state.NewContainerID = "postgres-2"

	state := coordinator.GetTransitionState()
	if state.OldContainerID != "postgres-1" {
		t.Fatalf("expected old container 'postgres-1', got '%s'", state.OldContainerID)
	}
	if state.NewContainerID != "postgres-2" {
		t.Fatalf("expected new container 'postgres-2', got '%s'", state.NewContainerID)
	}
}

func TestCheckReadiness_NoVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	ctx := context.Background()

	check := coordinator.CheckReadiness(ctx)
	if check == nil {
		t.Fatal("expected non-nil readiness check")
	}
	if check.HasStatefulVolumes {
		t.Fatal("expected no stateful volumes")
	}
	if !check.ReadyToRollout {
		t.Fatal("expected ready to rollout")
	}
}

func TestCheckReadiness_WithVolumes(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	coordinator.state.Volumes = []VolumeInfo{
		{Name: "postgres_data", MountPath: "/data", ReadOnly: false},
	}
	coordinator.state.Snapshots = map[string]*VolumeSnapshot{
		"/data": {
			Name:         "postgres_data",
			Mode:         "rw",
			SnapshotTime: time.Now(),
		},
	}
	ctx := context.Background()

	check := coordinator.CheckReadiness(ctx)
	if !check.HasStatefulVolumes {
		t.Fatal("expected stateful volumes")
	}
	if check.VolumeCount != 1 {
		t.Fatalf("expected 1 volume, got %d", check.VolumeCount)
	}
	if !check.ReadyToRollout {
		t.Fatal("expected ready to rollout")
	}
	if len(check.Issues) > 0 {
		t.Fatalf("expected no issues, got: %v", check.Issues)
	}
}

func TestGetSnapshotsForPersistence(t *testing.T) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	mock := &MockDockerClient{}
	vm := NewVolumeManager(mock, logger)
	coordinator := vm.NewRolloutVolumeCoordinator("postgres")
	coordinator.state.Snapshots = map[string]*VolumeSnapshot{
		"/data": {
			Name:      "postgres_data",
			MountPath: "/data",
			Mode:      "rw",
		},
	}

	persisted := coordinator.GetSnapshotsForPersistence()
	if len(persisted) != 1 {
		t.Fatalf("expected 1 persisted snapshot, got %d", len(persisted))
	}
	if _, ok := persisted["/data"]; !ok {
		t.Fatal("expected /data in persisted snapshots")
	}
}
