package rollout

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

type fakeRuntime struct {
	pullErr          error
	replicaCount     int
	replicaCountErr  error
	scaleErr         error
	waitID           string
	waitAddr         string
	waitErr          error
	oldID            string
	findOldErr       error
	containerAddr    string
	containerAddrErr error
	removeErr        error
	verifyStableErr  error
	scaleCalls       []int
	removedIDs       []string
}

func (f *fakeRuntime) Pull(context.Context, string, string) error { return f.pullErr }
func (f *fakeRuntime) ServiceReplicaCount(context.Context, string) (int, error) {
	return f.replicaCount, f.replicaCountErr
}
func (f *fakeRuntime) ScaleService(_ context.Context, _ string, _ string, replicas int) error {
	f.scaleCalls = append(f.scaleCalls, replicas)
	return f.scaleErr
}
func (f *fakeRuntime) WaitForNewContainer(context.Context, Options, *zap.Logger) (string, string, error) {
	return f.waitID, f.waitAddr, f.waitErr
}
func (f *fakeRuntime) FindOldContainer(context.Context, string, string) (string, error) {
	return f.oldID, f.findOldErr
}
func (f *fakeRuntime) ContainerAddr(context.Context, string) (string, error) {
	return f.containerAddr, f.containerAddrErr
}
func (f *fakeRuntime) RemoveContainer(_ context.Context, id string) error {
	f.removedIDs = append(f.removedIDs, id)
	return f.removeErr
}
func (f *fakeRuntime) VerifyStable(context.Context, string, time.Duration) error {
	return f.verifyStableErr
}

type fakeControl struct {
	registerErr   error
	drainErr      error
	deregisterErr error

	registeredIDs   []string
	drainedIDs      []string
	deregisteredIDs []string
}

func (f *fakeControl) RegisterBackend(_ context.Context, _ Options, id, _ string, _ *zap.Logger) error {
	f.registeredIDs = append(f.registeredIDs, id)
	return f.registerErr
}
func (f *fakeControl) DrainBackend(_ context.Context, _ Options, id string, _ *zap.Logger) error {
	f.drainedIDs = append(f.drainedIDs, id)
	return f.drainErr
}
func (f *fakeControl) DeregisterBackend(_ context.Context, _ Options, id string, _ *zap.Logger) error {
	f.deregisteredIDs = append(f.deregisteredIDs, id)
	return f.deregisterErr
}

type fakeStateStore struct {
	saved   bool
	cleared bool
}

func (f *fakeStateStore) Save(RolloutState) error {
	f.saved = true
	return nil
}
func (f *fakeStateStore) Clear(string) { f.cleared = true }

func TestRunWithDepsFailurePaths(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		runtime          *fakeRuntime
		control          *fakeControl
		wantErrSubstring string
		wantScaleCalls   []int
		wantStateSaved   bool
	}{
		{
			name: "scale up fails",
			runtime: &fakeRuntime{
				replicaCount: 1,
				scaleErr:     errors.New("scale failed"),
			},
			control:          &fakeControl{},
			wantErrSubstring: "rollout: scale up:",
			wantScaleCalls:   []int{2},
		},
		{
			name: "wait for new container fails and scales back",
			runtime: &fakeRuntime{
				replicaCount: 2,
				waitErr:      errors.New("timeout waiting"),
			},
			control:          &fakeControl{},
			wantErrSubstring: "rollout: wait for healthy container:",
			wantScaleCalls:   []int{3, 2},
		},
		{
			name: "drain old backend fails",
			runtime: &fakeRuntime{
				replicaCount:  1,
				waitID:        "newcontainer123456",
				waitAddr:      "10.0.0.2:3000",
				oldID:         "oldcontainer123456",
				containerAddr: "10.0.0.1:3000",
			},
			control: &fakeControl{
				drainErr: errors.New("drain rejected"),
			},
			wantErrSubstring: "rollout: drain old backend",
			wantScaleCalls:   []int{2},
			wantStateSaved:   true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state := &fakeStateStore{}
			err := runWithDeps(
				context.Background(),
				Options{
					Service:     "api",
					ComposeFile: "docker-rollout-compose.yml",
					ControlAddr: "http://localhost:9900",
					Drain:       0,
				},
				zap.NewNop(),
				runDeps{
					runtime: tc.runtime,
					control: tc.control,
					state:   state,
				},
			)

			if err == nil || !strings.Contains(err.Error(), tc.wantErrSubstring) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErrSubstring)
			}
			if len(tc.runtime.scaleCalls) != len(tc.wantScaleCalls) {
				t.Fatalf("scale calls = %v, want %v", tc.runtime.scaleCalls, tc.wantScaleCalls)
			}
			for i := range tc.wantScaleCalls {
				if tc.runtime.scaleCalls[i] != tc.wantScaleCalls[i] {
					t.Fatalf("scale calls = %v, want %v", tc.runtime.scaleCalls, tc.wantScaleCalls)
				}
			}
			if state.saved != tc.wantStateSaved {
				t.Fatalf("state saved = %v, want %v", state.saved, tc.wantStateSaved)
			}
		})
	}
}
