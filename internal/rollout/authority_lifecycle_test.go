package rollout

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

// This file covers the Phase 2.6 hardening test matrix items that exercise
// internal/rollout directly: multiple consecutive rollouts/rollbacks,
// interruption at each lifecycle stage, and rollback-after-interrupted-
// deployment. Scenarios that only exercise internal/state's planner
// (interrupted-before/after-commit, stale/corrupted/missing persisted
// state) live in internal/state/recovery_scenarios_test.go and
// internal/api/authority_test.go — see AUTHORITY-LIFECYCLE.md's failure
// matrix for the complete cross-package mapping.

// TestRun_PreStabilityFailures_NeverPersistAuthority is the flip side of
// TestRunWithDeps_HappyPath_PersistsAuthorityAtCorrectPoints: every failure
// mode that occurs before the stability check passes must leave the fake
// control's transitioning/commit call lists empty. Nothing was ever
// supposed to be persisted before that point (see
// AUTHORITY-LIFECYCLE.md §2.2's reasoning for why), so a failed scale-up or
// a healthcheck timeout must never have written anything an operator would
// need to clean up.
func TestRun_PreStabilityFailures_NeverPersistAuthority(t *testing.T) {
	cases := []struct {
		name    string
		runtime *fakeRuntime
	}{
		{
			name:    "scale up fails",
			runtime: &fakeRuntime{replicaCount: 1, scaleErr: errors.New("scale failed")},
		},
		{
			name:    "wait for new container fails",
			runtime: &fakeRuntime{replicaCount: 2, waitErr: errors.New("timeout waiting")},
		},
		{
			name: "stability check itself fails (auto-rollback)",
			runtime: &fakeRuntime{
				replicaCount: 1, waitID: "newcontainer123456", waitAddr: "10.0.0.2:3000",
				oldID: "oldcontainer123456", containerAddr: "10.0.0.1:3000",
				verifyStableErr: errors.New("became unhealthy"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := &fakeControl{}
			st := &fakeStateStore{}
			err := runWithDeps(
				context.Background(),
				Options{Service: "web", ComposeFile: "docker-rollout-compose.yml", ControlAddr: "http://localhost:9900"},
				zap.NewNop(),
				runDeps{runtime: tc.runtime, control: ctrl, state: st},
			)
			if err == nil {
				t.Fatal("expected this case to fail")
			}
			if len(ctrl.transitioningCalls) != 0 {
				t.Errorf("MarkTransitioning called %v, want none — nothing should persist before the stability check passes", ctrl.transitioningCalls)
			}
			if len(ctrl.commitCalls) != 0 {
				t.Errorf("CommitAuthority called %v, want none", ctrl.commitCalls)
			}
		})
	}
}

// TestRun_MultipleConsecutiveRollouts simulates three deploys in a row
// against the same service, each starting from where the last left off
// (the new backend from rollout N becomes the "old" backend rollout N+1
// scales past). Verifies each rollout persists its own correct old/new
// pair — no bleed-through from the previous rollout's IDs — and that the
// final commit always names the very latest generation.
func TestRun_MultipleConsecutiveRollouts(t *testing.T) {
	type step struct {
		oldID, newID     string
		wantOld, wantNew string
	}
	steps := []step{
		{oldID: "seedcontainer0", newID: "gen1container0", wantOld: "web-seedcontaine", wantNew: "web-gen1containe"},
		{oldID: "gen1container0", newID: "gen2container0", wantOld: "web-gen1containe", wantNew: "web-gen2containe"},
		{oldID: "gen2container0", newID: "gen3container0", wantOld: "web-gen2containe", wantNew: "web-gen3containe"},
	}

	for i, s := range steps {
		rt := &fakeRuntime{
			replicaCount: 1, waitID: s.newID, waitAddr: "10.0.0.2:3000",
			oldID: s.oldID, containerAddr: "10.0.0.1:3000",
		}
		ctrl := &fakeControl{}
		st := &fakeStateStore{}
		if err := runWithDeps(
			context.Background(),
			Options{Service: "web", ComposeFile: "docker-rollout-compose.yml", ControlAddr: "http://localhost:9900"},
			zap.NewNop(),
			runDeps{runtime: rt, control: ctrl, state: st},
		); err != nil {
			t.Fatalf("rollout %d: %v", i+1, err)
		}

		if len(ctrl.commitCalls) != 1 {
			t.Fatalf("rollout %d: CommitAuthority called %d times, want 1", i+1, len(ctrl.commitCalls))
		}
		if ctrl.commitCalls[0] != s.wantNew {
			t.Errorf("rollout %d: committed %q, want %q", i+1, ctrl.commitCalls[0], s.wantNew)
		}
		wantTransition := s.wantOld + "->" + s.wantNew
		if len(ctrl.transitioningCalls) != 1 || ctrl.transitioningCalls[0] != wantTransition {
			t.Errorf("rollout %d: transitioning = %v, want [%s]", i+1, ctrl.transitioningCalls, wantTransition)
		}
	}
}

// TestRollback_MultipleConsecutiveRollbacks simulates two independent
// failed-deploy-then-rollback cycles for the same service. Each rollback
// must commit its own old backend as authority — the second rollback must
// not be confused by, or fail to overwrite, whatever the first one
// committed.
func TestRollback_MultipleConsecutiveRollbacks(t *testing.T) {
	withRollbackStateDir(t)

	cycles := []RolloutState{
		{Service: "web", OldBackendID: "web-gen1", OldAddr: "10.0.0.1:3000", NewBackendID: "web-gen2-bad", NewAddr: "10.0.0.2:3000"},
		{Service: "web", OldBackendID: "web-gen2", OldAddr: "10.0.0.3:3000", NewBackendID: "web-gen3-bad", NewAddr: "10.0.0.4:3000"},
	}

	for i, state := range cycles {
		srv, fc := newFakeControlServer(t)
		state.ControlAddr = srv.URL
		state.Drain = time.Millisecond

		if err := Rollback(context.Background(), state, zap.NewNop(), nil); err != nil {
			t.Fatalf("rollback %d: %v", i+1, err)
		}

		fc.mu.Lock()
		commits := append([]string(nil), fc.commits...)
		fc.mu.Unlock()
		if len(commits) != 1 || commits[0] != state.OldBackendID {
			t.Errorf("rollback %d: committed %v, want [%s]", i+1, commits, state.OldBackendID)
		}
	}
}

// TestRollbackAfterInterruptedDeployment covers the specific scenario named
// in the hardening test matrix: a forward rollout is interrupted (the
// stability check fails, triggering Run's own automatic rollback path —
// see rollout.go's PhaseRollingBack branch), and — separately — an
// operator-invoked Rollback against a *different*, already-recorded prior
// state is still safe to run afterward. This asserts the two code paths
// (Run's internal auto-rollback and the standalone Rollback function) do
// not interfere with each other's authority bookkeeping: Run's failed
// attempt persists nothing (verified above), so a subsequent Rollback
// against the last *successful* deploy's recorded state is exactly as if
// the failed attempt never happened.
func TestRollbackAfterInterruptedDeployment(t *testing.T) {
	withRollbackStateDir(t)

	// Step 1: a rollout fails its stability check and auto-rolls-back.
	rt := &fakeRuntime{
		replicaCount: 1, waitID: "badcontainer0", waitAddr: "10.0.0.2:3000",
		oldID: "goodcontainer0", containerAddr: "10.0.0.1:3000",
		verifyStableErr: errors.New("crashed during stability window"),
	}
	ctrl := &fakeControl{}
	st := &fakeStateStore{}
	err := runWithDeps(
		context.Background(),
		Options{Service: "web", ComposeFile: "docker-rollout-compose.yml", ControlAddr: "http://localhost:9900"},
		zap.NewNop(),
		runDeps{runtime: rt, control: ctrl, state: st},
	)
	if err == nil {
		t.Fatal("expected the interrupted rollout to return an error")
	}
	if len(ctrl.commitCalls) != 0 {
		t.Fatalf("interrupted rollout must not have committed authority, got %v", ctrl.commitCalls)
	}

	// Step 2: separately, an operator rolls back to a prior recorded good
	// state (e.g. from an earlier, successful deploy's rollback file).
	// This must succeed independent of step 1's failure.
	srv, fc := newFakeControlServer(t)
	priorGood := RolloutState{
		Service: "web", OldBackendID: "web-goodcontainer0", OldAddr: "10.0.0.1:3000",
		ControlAddr: srv.URL,
	}
	if err := Rollback(context.Background(), priorGood, zap.NewNop(), nil); err != nil {
		t.Fatalf("rollback after interrupted deployment: %v", err)
	}
	fc.mu.Lock()
	commits := append([]string(nil), fc.commits...)
	fc.mu.Unlock()
	if len(commits) != 1 || commits[0] != "web-goodcontainer0" {
		t.Errorf("committed %v, want [web-goodcontainer0]", commits)
	}
}
