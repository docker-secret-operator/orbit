package rollout

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

// fakeVolumeCoordinator records calls in order for a single rollout
// transition, mirroring internal/volumes.RolloutVolumeCoordinator's exported
// lifecycle used by runWithDeps/Rollback.
type fakeVolumeCoordinator struct {
	prepareErr  error
	validateErr error
	completeErr error
	rollbackErr error
	snapshots   map[string]interface{}
	calls       []string
}

func (f *fakeVolumeCoordinator) PrepareForRollout(_ context.Context, oldID string) error {
	f.calls = append(f.calls, "prepare:"+oldID)
	return f.prepareErr
}
func (f *fakeVolumeCoordinator) ValidateNewContainer(_ context.Context, newID string) error {
	f.calls = append(f.calls, "validate:"+newID)
	return f.validateErr
}
func (f *fakeVolumeCoordinator) CompleteTransition(context.Context) error {
	f.calls = append(f.calls, "complete")
	return f.completeErr
}
func (f *fakeVolumeCoordinator) Rollback(context.Context) error {
	f.calls = append(f.calls, "rollback")
	return f.rollbackErr
}
func (f *fakeVolumeCoordinator) GetSnapshotsForPersistence() map[string]interface{} {
	return f.snapshots
}

type fakeVolumeManager struct {
	coord         *fakeVolumeCoordinator
	restoreCalled bool
	restoreErr    error
	restoreData   map[string]interface{}
}

func (f *fakeVolumeManager) NewCoordinator(string) VolumeCoordinator { return f.coord }
func (f *fakeVolumeManager) RestoreFromPersisted(_ context.Context, data map[string]interface{}) error {
	f.restoreCalled = true
	f.restoreData = data
	return f.restoreErr
}

func TestRunWithDeps_VolumeLifecycle_HappyPath(t *testing.T) {
	rt := &fakeRuntime{
		replicaCount:  1,
		waitID:        "newcontainer123456",
		waitAddr:      "10.0.0.2:3000",
		oldID:         "oldcontainer123456",
		containerAddr: "10.0.0.1:3000",
	}
	ctrl := &fakeControl{}
	st := &fakeStateStore{}
	coord := &fakeVolumeCoordinator{snapshots: map[string]interface{}{"/data": "snap"}}
	volMgr := &fakeVolumeManager{coord: coord}

	err := runWithDeps(
		context.Background(),
		Options{Service: "db", ComposeFile: "docker-rollout-compose.yml", ControlAddr: "http://localhost:9900"},
		zap.NewNop(),
		runDeps{runtime: rt, control: ctrl, state: st, volumes: volMgr},
	)
	if err != nil {
		t.Fatalf("runWithDeps error = %v, want nil", err)
	}

	wantCalls := []string{"prepare:oldcontainer123456", "validate:newcontainer123456", "complete"}
	if len(coord.calls) != len(wantCalls) {
		t.Fatalf("volume coordinator calls = %v, want %v", coord.calls, wantCalls)
	}
	for i, want := range wantCalls {
		if coord.calls[i] != want {
			t.Errorf("call[%d] = %q, want %q (full: %v)", i, coord.calls[i], want, coord.calls)
		}
	}

	if len(ctrl.registeredIDs) != 1 {
		t.Fatalf("expected the new backend to be registered, registeredIDs = %v", ctrl.registeredIDs)
	}

	if !st.saved {
		t.Fatal("expected rollout state to be saved")
	}
	if got := st.lastSaved.VolumeSnapshots["/data"]; got != "snap" {
		t.Errorf("saved state VolumeSnapshots[/data] = %v, want %q", got, "snap")
	}
}

func TestRunWithDeps_PrepareForRolloutFails_AbortsBeforeRegisterAndScalesBack(t *testing.T) {
	rt := &fakeRuntime{
		replicaCount:  1,
		waitID:        "newcontainer123456",
		waitAddr:      "10.0.0.2:3000",
		oldID:         "oldcontainer123456",
		containerAddr: "10.0.0.1:3000",
	}
	ctrl := &fakeControl{}
	st := &fakeStateStore{}
	coord := &fakeVolumeCoordinator{prepareErr: errors.New("snapshot failed")}
	volMgr := &fakeVolumeManager{coord: coord}

	err := runWithDeps(
		context.Background(),
		Options{Service: "db", ComposeFile: "docker-rollout-compose.yml", ControlAddr: "http://localhost:9900"},
		zap.NewNop(),
		runDeps{runtime: rt, control: ctrl, state: st, volumes: volMgr},
	)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if len(ctrl.registeredIDs) != 0 {
		t.Errorf("expected no backend registration when volume prepare fails, got %v", ctrl.registeredIDs)
	}
	if st.saved {
		t.Error("expected no rollout state to be saved when volume prepare fails")
	}
	if len(rt.scaleCalls) != 2 || rt.scaleCalls[1] != rt.scaleCalls[0]-1 {
		t.Errorf("expected a scale-up then scale-back-down, got %v", rt.scaleCalls)
	}
}

func TestRunWithDeps_ValidateNewContainerFails_AbortsBeforeRegister(t *testing.T) {
	rt := &fakeRuntime{
		replicaCount:  1,
		waitID:        "newcontainer123456",
		waitAddr:      "10.0.0.2:3000",
		oldID:         "oldcontainer123456",
		containerAddr: "10.0.0.1:3000",
	}
	ctrl := &fakeControl{}
	st := &fakeStateStore{}
	coord := &fakeVolumeCoordinator{validateErr: errors.New("new container not ready")}
	volMgr := &fakeVolumeManager{coord: coord}

	err := runWithDeps(
		context.Background(),
		Options{Service: "db", ComposeFile: "docker-rollout-compose.yml", ControlAddr: "http://localhost:9900"},
		zap.NewNop(),
		runDeps{runtime: rt, control: ctrl, state: st, volumes: volMgr},
	)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if len(ctrl.registeredIDs) != 0 {
		t.Errorf("expected no backend registration when volume validation fails, got %v", ctrl.registeredIDs)
	}
}

func TestRunWithDeps_StabilityCheckFails_CallsVolumeRollback(t *testing.T) {
	rt := &fakeRuntime{
		replicaCount:    1,
		waitID:          "newcontainer123456",
		waitAddr:        "10.0.0.2:3000",
		oldID:           "oldcontainer123456",
		containerAddr:   "10.0.0.1:3000",
		verifyStableErr: errors.New("became unhealthy"),
	}
	ctrl := &fakeControl{}
	st := &fakeStateStore{}
	coord := &fakeVolumeCoordinator{}
	volMgr := &fakeVolumeManager{coord: coord}

	err := runWithDeps(
		context.Background(),
		Options{Service: "db", ComposeFile: "docker-rollout-compose.yml", ControlAddr: "http://localhost:9900"},
		zap.NewNop(),
		runDeps{runtime: rt, control: ctrl, state: st, volumes: volMgr},
	)
	if err == nil {
		t.Fatal("expected an error from the failed stability check, got nil")
	}

	foundRollback := false
	for _, c := range coord.calls {
		if c == "rollback" {
			foundRollback = true
		}
		if c == "complete" {
			t.Errorf("CompleteTransition should not be called on auto-rollback, calls = %v", coord.calls)
		}
	}
	if !foundRollback {
		t.Errorf("expected volume coordinator Rollback to be called, calls = %v", coord.calls)
	}
	if !st.cleared {
		t.Error("expected rollout state to be cleared after auto-rollback")
	}
}

func TestRollbackWithVolumeManager_RestoresFromPersistedSnapshots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/backends":
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	volMgr := &fakeVolumeManager{coord: &fakeVolumeCoordinator{}}
	state := RolloutState{
		Service:         "db",
		OldBackendID:    "db-old1",
		OldAddr:         "10.0.0.1:5432",
		ControlAddr:     srv.URL,
		VolumeSnapshots: map[string]interface{}{"/data": map[string]interface{}{"name": "db_data"}},
	}

	if err := rollbackWithVolumeManager(context.Background(), state, zap.NewNop(), nil, volMgr); err != nil {
		t.Fatalf("rollbackWithVolumeManager error = %v, want nil", err)
	}
	if !volMgr.restoreCalled {
		t.Fatal("expected RestoreFromPersisted to be called")
	}
	if len(volMgr.restoreData) != 1 {
		t.Errorf("RestoreFromPersisted data = %v, want the persisted VolumeSnapshots", volMgr.restoreData)
	}
}

func TestRollbackWithVolumeManager_NoSnapshots_SkipsRestore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	volMgr := &fakeVolumeManager{coord: &fakeVolumeCoordinator{}}
	state := RolloutState{
		Service:      "web",
		OldBackendID: "web-old1",
		OldAddr:      "10.0.0.1:3000",
		ControlAddr:  srv.URL,
	}

	if err := rollbackWithVolumeManager(context.Background(), state, zap.NewNop(), nil, volMgr); err != nil {
		t.Fatalf("rollbackWithVolumeManager error = %v, want nil", err)
	}
	if volMgr.restoreCalled {
		t.Error("expected RestoreFromPersisted not to be called when no volume snapshots were recorded")
	}
}
