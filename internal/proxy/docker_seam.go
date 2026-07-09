package proxy

import (
	"context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"
)

// Docker interaction seam (ADR-0006 Stage 4, PR 4.1 — frozen).
//
// These interfaces are the ONLY Docker capabilities the Stage 4 Reconciler
// and EventSource are permitted to use. They exist so those two components
// can be unit-tested against fakes without a live daemon, and so the exact
// Docker surface Stage 4 depends on is small, explicit, and reviewable.
//
// Design rules (frozen — expanding these requires a new architecture
// review, per the PR 4.1 interface-freeze review):
//
//   - Every method has an immediate Stage 4 consumer. No speculative methods.
//   - The seam models Docker interactions ONLY. It never depends on Registry,
//     Router, ProjectRegistry, HealthController, or recovery, and it returns
//     raw Docker SDK types — turning a types.ContainerJSON into a Backend is
//     the consumer's job (via the existing extractBackend logic), never the
//     seam's, so no business logic leaks in here.
//   - The interfaces are unexported: they cannot be widened from outside this
//     package, and a caller in cmd/docker-orbit passes the concrete
//     *client.Client (which satisfies them structurally) without ever naming
//     them.
//   - Interfaces are segregated per consumer (Reconciler sees the lister,
//     EventSource sees the subscriber) so neither depends on a method it does
//     not call. ContainerInspect is the one genuinely shared capability and
//     lives in the embedded containerInspector.

// containerInspector turns a container ID into full metadata (labels, env,
// network settings) — the inputs extractBackend needs to build a Backend.
// Docker inspection is the sole source of truth (INV-4); an event payload's
// own fields are never trusted in its place.
type containerInspector interface {
	ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error)
}

// containerLister is the Reconciler's seam: one project-wide list pass
// (INV-5 — one ContainerList serves all services, demuxed by label) plus
// per-container inspection to build the live membership set.
type containerLister interface {
	containerInspector
	ContainerList(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error)
}

// eventSubscriber is the EventSource's seam: the Docker event stream plus
// targeted inspection on start/health events.
type eventSubscriber interface {
	containerInspector
	Events(ctx context.Context, options types.EventsOptions) (<-chan events.Message, <-chan error)
}

// Compile-time assertions: the real Docker client satisfies both consumer
// seams with no adapter. If a future SDK bump changes one of these
// signatures, the build breaks here — the single intended place to notice,
// rather than deep inside the Reconciler or EventSource.
var (
	_ containerLister = (*client.Client)(nil)
	_ eventSubscriber = (*client.Client)(nil)
)
