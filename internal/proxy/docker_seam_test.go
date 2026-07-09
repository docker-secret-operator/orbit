package proxy

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
)

// fakeDocker is a daemon-free stand-in for the Stage 4 Docker seam. Its
// existence is the proof PR 4.1 owes: containerLister and eventSubscriber
// can be satisfied by an in-test fake, so the Reconciler (PR 4.2) and
// EventSource (PR 4.3) will be unit- and race-testable without a live
// Docker daemon. The channels are caller-owned, so a test can drive
// reconnect (close errs) and event delivery deterministically.
type fakeDocker struct {
	containers []types.Container
	inspects   map[string]types.ContainerJSON
	msgs       <-chan events.Message
	errs       <-chan error
}

func (f *fakeDocker) ContainerList(context.Context, types.ContainerListOptions) ([]types.Container, error) {
	return f.containers, nil
}

func (f *fakeDocker) ContainerInspect(_ context.Context, id string) (types.ContainerJSON, error) {
	return f.inspects[id], nil
}

func (f *fakeDocker) Events(context.Context, types.EventsOptions) (<-chan events.Message, <-chan error) {
	return f.msgs, f.errs
}

// Compile-time proof the fake satisfies both consumer seams — the same
// interfaces the real *client.Client satisfies (asserted in docker_seam.go).
var (
	_ containerLister = (*fakeDocker)(nil)
	_ eventSubscriber = (*fakeDocker)(nil)
)

// TestDockerSeam_FakeIsUsableWithoutDaemon exercises every seam method on the
// fake, confirming the abstraction is functional (not just type-compatible)
// with no Docker daemon present.
func TestDockerSeam_FakeIsUsableWithoutDaemon(t *testing.T) {
	msgs := make(chan events.Message, 1)
	errs := make(chan error, 1)
	f := &fakeDocker{
		containers: []types.Container{{ID: "abc"}},
		inspects:   map[string]types.ContainerJSON{"abc": {}},
		msgs:       msgs,
		errs:       errs,
	}

	var lister containerLister = f
	got, err := lister.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil || len(got) != 1 || got[0].ID != "abc" {
		t.Fatalf("ContainerList via seam = %v, %v; want one container 'abc'", got, err)
	}
	if _, err := lister.ContainerInspect(context.Background(), "abc"); err != nil {
		t.Fatalf("ContainerInspect via seam: %v", err)
	}

	var sub eventSubscriber = f
	gotMsgs, gotErrs := sub.Events(context.Background(), types.EventsOptions{})
	if gotMsgs == nil || gotErrs == nil {
		t.Fatal("Events via seam returned nil channels")
	}
	// Prove the channels are caller-driven (the property reconnect/event tests rely on).
	msgs <- events.Message{Type: events.ContainerEventType, Action: "start"}
	if m := <-gotMsgs; m.Action != "start" {
		t.Fatalf("expected caller-injected 'start' event, got %q", m.Action)
	}
}
