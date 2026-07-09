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
	registerErr      error
	drainErr         error
	deregisterErr    error
	transitioningErr error
	commitErr        error

	registeredIDs      []string
	drainedIDs         []string
	deregisteredIDs    []string
	transitioningCalls []string // "old->new" pairs
	commitCalls        []string
	callOrder          []string // every call above, in the order it happened — for sequencing assertions
}

func (f *fakeControl) RegisterBackend(_ context.Context, _ Options, id, _ string, _ *zap.Logger) error {
	f.registeredIDs = append(f.registeredIDs, id)
	f.callOrder = append(f.callOrder, "register:"+id)
	return f.registerErr
}
func (f *fakeControl) DrainBackend(_ context.Context, _ Options, id string, _ *zap.Logger) error {
	f.drainedIDs = append(f.drainedIDs, id)
	f.callOrder = append(f.callOrder, "drain:"+id)
	return f.drainErr
}
func (f *fakeControl) MarkTransitioning(_ context.Context, _ Options, oldGen, newGen string, _ *zap.Logger) error {
	f.transitioningCalls = append(f.transitioningCalls, oldGen+"->"+newGen)
	f.callOrder = append(f.callOrder, "transitioning:"+oldGen+"->"+newGen)
	return f.transitioningErr
}
func (f *fakeControl) CommitAuthority(_ context.Context, _ Options, generation string, _ *zap.Logger) error {
	f.commitCalls = append(f.commitCalls, generation)
	f.callOrder = append(f.callOrder, "commit:"+generation)
	return f.commitErr
}
func (f *fakeControl) DeregisterBackend(_ context.Context, _ Options, id string, _ *zap.Logger) error {
	f.deregisteredIDs = append(f.deregisteredIDs, id)
	f.callOrder = append(f.callOrder, "deregister:"+id)
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

// TestRunWithDeps_HappyPath_PersistsAuthorityAtCorrectPoints verifies
// MarkTransitioning/CommitAuthority are called exactly once each, with the
// right generation IDs, in the right order relative to drain/deregister —
// see docs/governance/AUTHORITY-LIFECYCLE.md §2.2 for why the ordering
// itself (transitioning before drain, commit after the old backend is
// fully gone) is the point of this design, not an implementation detail.
func TestRunWithDeps_HappyPath_PersistsAuthorityAtCorrectPoints(t *testing.T) {
	rt := &fakeRuntime{
		replicaCount:  1,
		waitID:        "newcontainer123456",
		waitAddr:      "10.0.0.2:3000",
		oldID:         "oldcontainer123456",
		containerAddr: "10.0.0.1:3000",
	}
	ctrl := &fakeControl{}
	st := &fakeStateStore{}

	err := runWithDeps(
		context.Background(),
		Options{
			Service:     "web",
			ComposeFile: "docker-rollout-compose.yml",
			ControlAddr: "http://localhost:9900",
			Drain:       0,
		},
		zap.NewNop(),
		runDeps{runtime: rt, control: ctrl, state: st},
	)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	const wantOld = "web-oldcontainer" // opts.Service + "-" + shortID(oldID)
	const wantNew = "web-newcontainer" // opts.Service + "-" + shortID(waitID)

	if len(ctrl.transitioningCalls) != 1 {
		t.Fatalf("MarkTransitioning called %d times, want 1: %v", len(ctrl.transitioningCalls), ctrl.transitioningCalls)
	}
	if want := wantOld + "->" + wantNew; ctrl.transitioningCalls[0] != want {
		t.Errorf("MarkTransitioning call = %q, want %q", ctrl.transitioningCalls[0], want)
	}
	if len(ctrl.commitCalls) != 1 {
		t.Fatalf("CommitAuthority called %d times, want 1: %v", len(ctrl.commitCalls), ctrl.commitCalls)
	}
	if ctrl.commitCalls[0] != wantNew {
		t.Errorf("CommitAuthority call = %q, want %q", ctrl.commitCalls[0], wantNew)
	}

	// Ordering: transitioning must happen before drain (not after — nothing
	// should be persisted before the stability check that could pass has
	// passed, and nothing should be left un-persisted once it has).
	// commit must happen after the old backend's deregister.
	idx := func(prefix string) int {
		for i, c := range ctrl.callOrder {
			if strings.HasPrefix(c, prefix) {
				return i
			}
		}
		t.Fatalf("no call with prefix %q in %v", prefix, ctrl.callOrder)
		return -1
	}
	transitioningIdx := idx("transitioning:")
	drainIdx := idx("drain:")
	deregisterIdx := idx("deregister:")
	commitIdx := idx("commit:")

	if transitioningIdx > drainIdx {
		t.Errorf("call order = %v; transitioning (%d) must come before drain (%d)", ctrl.callOrder, transitioningIdx, drainIdx)
	}
	if commitIdx < deregisterIdx {
		t.Errorf("call order = %v; commit (%d) must come after deregister (%d)", ctrl.callOrder, commitIdx, deregisterIdx)
	}
}
