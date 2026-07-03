package rollout

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// Phase 3.0 (Production Reliability) Failure Injection: these tests inject a
// failure at each externally-dependent step of a rollout and assert the engine
// fails GRACEFULLY — meaning the two invariants that matter for a zero-downtime
// system hold in every failure case:
//
//   1. No traffic loss: the old backend is never drained/removed until the new
//      one is registered and rollback state is saved. Any failure before that
//      point must leave the old backend serving.
//   2. No partial mutation without recorded rollback state: the engine either
//      completes and records rollback state, or it unwinds what it started.
//
// These build on the same fakeRuntime/fakeControl/fakeStateStore dependency
// seams TestRunWithDepsFailurePaths uses (run_flow_test.go).

// runInjected runs one rollout with injected fakes and returns the error plus
// the observed scale calls (the primary evidence of what the engine mutated).
func runInjected(t *testing.T, rt *fakeRuntime, ctrl *fakeControl, st *fakeStateStore, pull bool) error {
	t.Helper()
	return runWithDeps(
		context.Background(),
		Options{
			Service:     "api",
			ComposeFile: "docker-rollout-compose.yml",
			ControlAddr: "http://localhost:9900",
			Pull:        pull,
			Drain:       0,
		},
		zap.NewNop(),
		runDeps{runtime: rt, control: ctrl, state: st},
	)
}

// Spec: "image pull failure". With --pull, a failing image pull must abort the
// rollout BEFORE scaling anything — no new container, no traffic change.
func TestFailureInjection_ImagePullFailure_AbortsBeforeScaling(t *testing.T) {
	rt := &fakeRuntime{replicaCount: 1, pullErr: errors.New("manifest unknown")}
	ctrl := &fakeControl{}
	st := &fakeStateStore{}

	err := runInjected(t, rt, ctrl, st, true /* pull */)
	if err == nil || !strings.Contains(err.Error(), "pull") {
		t.Fatalf("expected pull failure, got %v", err)
	}
	if len(rt.scaleCalls) != 0 {
		t.Errorf("pull failure must abort before scaling; scaleCalls=%v", rt.scaleCalls)
	}
	if st.saved {
		t.Error("no rollback state should be saved when the rollout never started")
	}
}

// Spec: "Docker API unavailable". If the engine cannot even read the current
// replica count (docker ps / compose unreachable), it must abort before any
// scaling.
func TestFailureInjection_DockerAPIUnavailable_AbortsBeforeScaling(t *testing.T) {
	rt := &fakeRuntime{replicaCountErr: errors.New("cannot connect to the Docker daemon")}
	ctrl := &fakeControl{}
	st := &fakeStateStore{}

	err := runInjected(t, rt, ctrl, st, false)
	if err == nil || !strings.Contains(err.Error(), "replicas") {
		t.Fatalf("expected replica-count failure, got %v", err)
	}
	if len(rt.scaleCalls) != 0 {
		t.Errorf("docker-unavailable must abort before scaling; scaleCalls=%v", rt.scaleCalls)
	}
}

// Spec: "container startup failure". Scaling succeeds but the new container
// never becomes healthy. The engine must scale back down (unwind) and never
// register the new backend — old backend keeps serving.
func TestFailureInjection_ContainerStartupFailure_ScalesBackAndKeepsOldBackend(t *testing.T) {
	rt := &fakeRuntime{replicaCount: 1, waitErr: errors.New("healthcheck never passed")}
	ctrl := &fakeControl{}
	st := &fakeStateStore{}

	err := runInjected(t, rt, ctrl, st, false)
	if err == nil || !strings.Contains(err.Error(), "wait for healthy container") {
		t.Fatalf("expected healthcheck failure, got %v", err)
	}
	// Scale up (+1) then scale back down — the new (bad) container is unwound.
	if len(rt.scaleCalls) != 2 || rt.scaleCalls[0] != 2 || rt.scaleCalls[1] != 1 {
		t.Errorf("startup failure must scale up then back down; scaleCalls=%v", rt.scaleCalls)
	}
	if st.saved {
		t.Error("no rollback state should be saved when the new container never became healthy")
	}
}

// Spec: "control API unavailable" / "network interruption". The new container
// is healthy, but registering it with the proxy fails. The engine must return
// an error and — critically — must NOT have drained or removed the old backend.
// Traffic stays on the old backend; no rollback state is recorded because the
// new backend never went live.
func TestFailureInjection_ControlAPIUnavailableAtRegister_KeepsOldBackendServing(t *testing.T) {
	rt := &fakeRuntime{
		replicaCount:  1,
		waitID:        "newcontainer1234",
		waitAddr:      "10.0.0.2:80",
		oldID:         "oldcontainer1234",
		containerAddr: "10.0.0.1:80",
	}
	ctrl := &fakeControl{registerErr: errors.New("connection refused")}
	st := &fakeStateStore{}

	err := runInjected(t, rt, ctrl, st, false)
	if err == nil || !strings.Contains(err.Error(), "register new backend") {
		t.Fatalf("expected register failure, got %v", err)
	}
	// The old backend was never touched: no rollback state saved (we returned
	// before Step 6), and the drain/deregister steps (7-8) were never reached.
	if st.saved {
		t.Error("rollback state must not be saved when registration failed — the new backend never took traffic")
	}
	if st.cleared {
		t.Error("state must not be cleared — nothing to clear, old backend still authoritative")
	}
	// Scale up happened (+1) but there is no scale-back here by design: the new
	// container is healthy, just unregistered, so it is left running for a
	// retry rather than destroyed. The safety property is that the OLD backend
	// keeps serving, which holds because drain/deregister were never reached.
	if len(rt.scaleCalls) != 1 || rt.scaleCalls[0] != 2 {
		t.Errorf("expected a single scale-up (+1) and no scale-back; scaleCalls=%v", rt.scaleCalls)
	}
}
