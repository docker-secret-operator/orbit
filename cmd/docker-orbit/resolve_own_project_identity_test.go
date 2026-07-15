package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"go.uber.org/zap"
)

// fakeInspector is the minimal dockerInspector fake for resolveOwnProjectIdentity
// tests — no real Docker daemon involved.
type fakeInspector struct {
	inspect types.ContainerJSON
	err     error
	calls   int
}

func (f *fakeInspector) ContainerInspect(_ context.Context, _ string) (types.ContainerJSON, error) {
	f.calls++
	return f.inspect, f.err
}

func containerJSONWithProjectLabel(project string) types.ContainerJSON {
	return types.ContainerJSON{
		Config: &container.Config{Labels: map[string]string{"com.docker.compose.project": project}},
	}
}

func TestResolveOwnProjectIdentity_SuccessfulSelfInspection(t *testing.T) {
	fi := &fakeInspector{inspect: containerJSONWithProjectLabel("orbit-demo")}

	project, err := resolveOwnProjectIdentity(context.Background(), fi)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if project != "orbit-demo" {
		t.Errorf("project = %q, want %q", project, "orbit-demo")
	}
}

func TestResolveOwnProjectIdentity_MissingProjectLabel(t *testing.T) {
	fi := &fakeInspector{inspect: types.ContainerJSON{Config: &container.Config{Labels: map[string]string{}}}}

	_, err := resolveOwnProjectIdentity(context.Background(), fi)
	if err == nil {
		t.Fatal("expected an error when com.docker.compose.project label is absent, got nil")
	}
}

func TestResolveOwnProjectIdentity_EmptyProjectLabel(t *testing.T) {
	fi := &fakeInspector{inspect: containerJSONWithProjectLabel("")}

	_, err := resolveOwnProjectIdentity(context.Background(), fi)
	if err == nil {
		t.Fatal("expected an error when com.docker.compose.project label is empty, got nil")
	}
}

func TestResolveOwnProjectIdentity_InspectFailure(t *testing.T) {
	fi := &fakeInspector{err: errors.New("no such container: web-abc123")}

	_, err := resolveOwnProjectIdentity(context.Background(), fi)
	if err == nil {
		t.Fatal("expected an error when ContainerInspect fails, got nil")
	}
}

func TestResolveOwnProjectIdentity_DockerUnavailable(t *testing.T) {
	fi := &fakeInspector{err: errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock")}

	_, err := resolveOwnProjectIdentity(context.Background(), fi)
	if err == nil {
		t.Fatal("expected an error when the Docker daemon is unavailable, got nil")
	}
}

// ── Bounded-retry wrapper (ADR-0007 §15: fail closed only after the startup budget) ──

func TestResolveOwnProjectIdentityWithRetry_SucceedsImmediately(t *testing.T) {
	fi := &fakeInspector{inspect: containerJSONWithProjectLabel("orbit-demo")}

	project, err := resolveOwnProjectIdentityWithRetry(context.Background(), fi, zap.NewNop(), time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if project != "orbit-demo" {
		t.Errorf("project = %q, want %q", project, "orbit-demo")
	}
	if fi.calls != 1 {
		t.Errorf("expected exactly 1 inspect call on immediate success, got %d", fi.calls)
	}
}

// flakyInspector fails the first failTimes calls, then succeeds — simulates a
// transiently-restarting Docker daemon within the startup retry budget.
type flakyInspector struct {
	failTimes int
	project   string
	calls     int
}

func (f *flakyInspector) ContainerInspect(_ context.Context, _ string) (types.ContainerJSON, error) {
	f.calls++
	if f.calls <= f.failTimes {
		return types.ContainerJSON{}, errors.New("daemon transiently unavailable")
	}
	return containerJSONWithProjectLabel(f.project), nil
}

func TestResolveOwnProjectIdentityWithRetry_RetriesWithinBudgetThenSucceeds(t *testing.T) {
	fi := &flakyInspector{failTimes: 2, project: "orbit-demo"}

	project, err := resolveOwnProjectIdentityWithRetry(context.Background(), fi, zap.NewNop(), time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if project != "orbit-demo" {
		t.Errorf("project = %q, want %q", project, "orbit-demo")
	}
	if fi.calls != 3 {
		t.Errorf("expected 3 inspect calls (2 failures + 1 success), got %d", fi.calls)
	}
}

func TestResolveOwnProjectIdentityWithRetry_FailsClosedAfterBudget_ReturnsClearError(t *testing.T) {
	fi := &fakeInspector{err: errors.New("daemon unreachable")}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := resolveOwnProjectIdentityWithRetry(ctx, fi, zap.NewNop(), time.Millisecond)
	if err == nil {
		t.Fatal("expected startup to fail closed once the retry budget is exhausted, got nil error")
	}
	if !strings.Contains(err.Error(), "project identity") {
		t.Errorf("error should clearly name the failure as project-identity resolution, got: %v", err)
	}
}
