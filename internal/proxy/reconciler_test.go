package proxy

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// fakeReconcileDocker is a daemon-free containerLister for Reconciler tests.
// Distinct from docker_seam_test.go's fakeDocker (same package, different
// name required) — this one adds per-ID inspect-error injection and
// call-order recording, which the guarantee-level tests below need and the
// PR 4.1 seam test does not.
type fakeReconcileDocker struct {
	containers []types.Container
	listErr    error
	// entered is closed the instant ContainerList is called, before any
	// blocking — lets a test synchronize precisely instead of sleeping.
	entered chan struct{}
	// listBlock, if non-nil, is read from (and so blocks) before
	// ContainerList returns — used to open a deterministic race window.
	listBlock chan struct{}

	inspects    map[string]types.ContainerJSON
	inspectErrs map[string]error

	mu           sync.Mutex
	inspectOrder []string
}

func (f *fakeReconcileDocker) ContainerList(context.Context, types.ContainerListOptions) ([]types.Container, error) {
	if f.entered != nil {
		close(f.entered)
	}
	if f.listBlock != nil {
		<-f.listBlock
	}
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.containers, nil
}

func (f *fakeReconcileDocker) ContainerInspect(_ context.Context, id string) (types.ContainerJSON, error) {
	f.mu.Lock()
	f.inspectOrder = append(f.inspectOrder, id)
	f.mu.Unlock()
	if err, ok := f.inspectErrs[id]; ok {
		return types.ContainerJSON{}, err
	}
	return f.inspects[id], nil
}

func (f *fakeReconcileDocker) order() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.inspectOrder))
	copy(out, f.inspectOrder)
	return out
}

var _ containerLister = (*fakeReconcileDocker)(nil)

// spyReconcilerMetrics is a thread-safe ReconcilerMetrics recorder.
type spyReconcilerMetrics struct {
	runs      atomic.Int64
	failures  atomic.Int64
	added     atomic.Int64
	removed   atomic.Int64
	durations atomic.Int64
	rejected  atomic.Int64
}

func (s *spyReconcilerMetrics) IncReconciliationRuns()     { s.runs.Add(1) }
func (s *spyReconcilerMetrics) IncReconciliationFailures() { s.failures.Add(1) }
func (s *spyReconcilerMetrics) IncContainersAdded(n int)   { s.added.Add(int64(n)) }
func (s *spyReconcilerMetrics) IncContainersRemoved(n int) { s.removed.Add(int64(n)) }
func (s *spyReconcilerMetrics) ObserveReconciliationDuration(time.Duration) {
	s.durations.Add(1)
}
func (s *spyReconcilerMetrics) IncReconciliationRejected() { s.rejected.Add(1) }

var _ ReconcilerMetrics = (*spyReconcilerMetrics)(nil)

// newFakeContainer builds a matching (list-summary, inspect) pair for one
// Orbit-managed backend container, mirroring the shape extractBackend
// already parses in recovery.go: ORBIT_BACKEND_ID env for the backend ID,
// the docker_rollout_mesh network's IP, ORBIT_BACKEND's port suffix, and
// the orbit.io/generation label.
func newFakeContainer(containerID, service, backendID, ip, port, generation string) (types.Container, types.ContainerJSON) {
	summary := types.Container{
		ID: containerID,
		Labels: map[string]string{
			"orbit.io/managed": "true",
			"orbit.io/service": service,
			"orbit.io/proxy":   "false",
		},
		State: "running",
	}
	inspect := types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{ID: containerID},
		Config: &container.Config{
			Labels: map[string]string{"orbit.io/generation": generation},
			Env:    []string{"ORBIT_BACKEND_ID=" + backendID, "ORBIT_BACKEND=" + service + ":" + port},
		},
		NetworkSettings: &types.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"docker_rollout_mesh": {IPAddress: ip},
			},
		},
	}
	return summary, inspect
}

// newFakeProxyContainer builds the shared proxy's own container — same
// orbit.io/service label as its backends, orbit.io/proxy=true, no
// ORBIT_BACKEND_ID — proving Reconciler must never mistake it for a backend.
func newFakeProxyContainer(containerID, service string) types.Container {
	return types.Container{
		ID: containerID,
		Labels: map[string]string{
			"orbit.io/managed": "true",
			"orbit.io/service": service,
			"orbit.io/proxy":   "true",
		},
		State: "running",
	}
}

// ── Guarantee-level tests ───────────────────────────────────────────────────

func TestReconciler_EmptyRegistry_ContainersAdded(t *testing.T) {
	c1, i1 := newFakeContainer("cid1", "web", "web-b1", "10.0.0.1", "3000", "gen-1")
	docker := &fakeReconcileDocker{
		containers: []types.Container{c1},
		inspects:   map[string]types.ContainerJSON{"cid1": i1},
	}
	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	rc := NewReconciler(pr, docker, nil, nil)
	rc.ReconcileOnce(context.Background())

	b, ok := reg.Get("web-b1")
	if !ok {
		t.Fatal("expected web-b1 to be added from an empty registry")
	}
	if b.Addr != "10.0.0.1:3000" {
		t.Fatalf("unexpected addr %q", b.Addr)
	}
	if st, _ := reg.State("web-b1"); st != StateActive {
		t.Fatalf("expected StateActive, got %s", st)
	}
}

func TestReconciler_StaleRegistry_ContainersRemoved(t *testing.T) {
	docker := &fakeReconcileDocker{} // no live containers anywhere
	reg := NewRegistry()
	addBackend(t, reg, "stale-1")
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	rc := NewReconciler(pr, docker, nil, nil)
	rc.ReconcileOnce(context.Background())

	if _, ok := reg.Get("stale-1"); ok {
		t.Fatal("expected stale-1 to be removed — it no longer exists in Docker")
	}
}

func TestReconciler_MixedState_Converges(t *testing.T) {
	c1, i1 := newFakeContainer("cid1", "web", "keep-1", "10.0.0.1", "3000", "gen-1")
	c2, i2 := newFakeContainer("cid2", "web", "new-1", "10.0.0.2", "3000", "gen-1")
	docker := &fakeReconcileDocker{
		containers: []types.Container{c1, c2},
		inspects:   map[string]types.ContainerJSON{"cid1": i1, "cid2": i2},
	}
	reg := NewRegistry()
	addBackend(t, reg, "keep-1") // present in both Docker and Registry
	addBackend(t, reg, "gone-1") // present only in Registry — stale

	pr := NewProjectRegistry()
	pr.Register("web", reg)

	rc := NewReconciler(pr, docker, nil, nil)
	rc.ReconcileOnce(context.Background())

	if _, ok := reg.Get("keep-1"); !ok {
		t.Error("keep-1 should remain untouched")
	}
	if _, ok := reg.Get("new-1"); !ok {
		t.Error("new-1 should be added")
	}
	if _, ok := reg.Get("gone-1"); ok {
		t.Error("gone-1 should be removed")
	}
	if reg.Len() != 2 {
		t.Fatalf("expected exactly 2 backends after convergence, got %d", reg.Len())
	}
}

func TestReconciler_OneServiceFailure_OthersContinue(t *testing.T) {
	cWeb, _ := newFakeContainer("cid-web", "web", "web-b1", "10.0.0.1", "3000", "gen-1")
	cAPI, iAPI := newFakeContainer("cid-api", "api", "api-b1", "10.0.0.2", "3000", "gen-1")

	docker := &fakeReconcileDocker{
		containers:  []types.Container{cWeb, cAPI},
		inspects:    map[string]types.ContainerJSON{"cid-api": iAPI},
		inspectErrs: map[string]error{"cid-web": errors.New("inspect failed")},
	}

	regWeb := NewRegistry()
	regAPI := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", regWeb)
	pr.Register("api", regAPI)

	m := &spyReconcilerMetrics{}
	rc := NewReconciler(pr, docker, m, nil)
	rc.ReconcileOnce(context.Background())

	if _, ok := regWeb.Get("web-b1"); ok {
		t.Error("web-b1 must not be added — its container failed inspection")
	}
	if _, ok := regAPI.Get("api-b1"); !ok {
		t.Error("api-b1 should be added — web's failure must not block api")
	}
	if m.failures.Load() == 0 {
		t.Error("expected at least one reconciliation failure recorded for web")
	}
}

func TestReconciler_ContainerListFailure_RegistryUntouched(t *testing.T) {
	docker := &fakeReconcileDocker{listErr: errors.New("daemon unavailable")}
	reg := NewRegistry()
	addBackend(t, reg, "b1")
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	m := &spyReconcilerMetrics{}
	rc := NewReconciler(pr, docker, m, nil)
	rc.ReconcileOnce(context.Background())

	if _, ok := reg.Get("b1"); !ok {
		t.Fatal("registry must be untouched when discovery fails entirely — never remove on uncertain data")
	}
	if m.failures.Load() != 1 {
		t.Fatalf("expected 1 failure recorded, got %d", m.failures.Load())
	}
	if m.runs.Load() != 1 {
		t.Fatalf("expected 1 run recorded even on failure, got %d", m.runs.Load())
	}
}

func TestReconciler_RegistryReplacement(t *testing.T) {
	docker := &fakeReconcileDocker{} // no live containers for the first pass

	regOld := NewRegistry()
	addBackend(t, regOld, "old-backend")
	pr := NewProjectRegistry()
	pr.Register("web", regOld)

	rc := NewReconciler(pr, docker, nil, nil)
	rc.ReconcileOnce(context.Background())

	if _, ok := regOld.Get("old-backend"); ok {
		t.Fatal("old-backend should have been removed — absent from Docker truth")
	}

	// A new container appears, and the service's registry is replaced at the
	// same time (e.g. a hot-swap during testing/ops tooling).
	c1, i1 := newFakeContainer("cid1", "web", "new-backend", "10.0.0.1", "3000", "gen-1")
	docker.containers = []types.Container{c1}
	docker.inspects = map[string]types.ContainerJSON{"cid1": i1}

	regNew := NewRegistry()
	pr.Register("web", regNew) // replace

	rc.ReconcileOnce(context.Background())

	if _, ok := regNew.Get("new-backend"); !ok {
		t.Fatal("new-backend should be added to the replacement registry")
	}
	if regOld.Len() != 0 {
		t.Fatal("old registry must never be touched again after replacement")
	}
}

// TestReconciler_MissingRegistrySkippedSafely proves that a service removed
// from the ProjectRegistry between Reconciler capturing the service list and
// looking each one up is skipped, not treated as an error — the deterministic
// version of the same race ProjectHealthController and executeRecoveryForProject
// already tolerate. Synchronized precisely via fakeReconcileDocker.entered/
// listBlock instead of a sleep.
func TestReconciler_MissingRegistrySkippedSafely(t *testing.T) {
	entered := make(chan struct{})
	block := make(chan struct{})
	docker := &fakeReconcileDocker{entered: entered, listBlock: block}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("ghost", reg)

	rc := NewReconciler(pr, docker, nil, nil)

	done := make(chan struct{})
	go func() {
		rc.ReconcileOnce(context.Background())
		close(done)
	}()

	<-entered          // Services() has already been captured; ContainerList is now blocked
	pr.Remove("ghost") // race window: service vanishes before the per-service loop reaches it
	close(block)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ReconcileOnce did not complete — did the missing-registry branch panic or deadlock?")
	}
}

func TestReconciler_DuplicateReconciliation_Idempotent(t *testing.T) {
	c1, i1 := newFakeContainer("cid1", "web", "b1", "10.0.0.1", "3000", "gen-1")
	docker := &fakeReconcileDocker{
		containers: []types.Container{c1},
		inspects:   map[string]types.ContainerJSON{"cid1": i1},
	}
	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	m := &spyReconcilerMetrics{}
	rc := NewReconciler(pr, docker, m, nil)
	rc.ReconcileOnce(context.Background())
	rc.ReconcileOnce(context.Background())

	if reg.Len() != 1 {
		t.Fatalf("expected exactly 1 backend after two identical passes, got %d", reg.Len())
	}
	if m.added.Load() != 1 {
		t.Fatalf("expected exactly 1 add total across both passes, got %d", m.added.Load())
	}
	if m.removed.Load() != 0 {
		t.Fatalf("expected 0 removes, got %d", m.removed.Load())
	}
}

func TestReconciler_MultiServiceIsolation(t *testing.T) {
	cWeb, iWeb := newFakeContainer("cid-web", "web", "web-b1", "10.0.0.1", "3000", "gen-1")
	docker := &fakeReconcileDocker{
		containers: []types.Container{cWeb},
		inspects:   map[string]types.ContainerJSON{"cid-web": iWeb},
	}

	regWeb := NewRegistry()
	regAPI := NewRegistry()
	addBackend(t, regAPI, "api-stale") // api has zero live containers

	pr := NewProjectRegistry()
	pr.Register("web", regWeb)
	pr.Register("api", regAPI)

	rc := NewReconciler(pr, docker, nil, nil)
	rc.ReconcileOnce(context.Background())

	if _, ok := regWeb.Get("web-b1"); !ok {
		t.Error("web-b1 should be added to web's registry")
	}
	if regWeb.Len() != 1 {
		t.Error("web registry must not be affected by api's convergence")
	}
	if _, ok := regAPI.Get("api-stale"); ok {
		t.Error("api-stale should be removed — api has zero live containers")
	}
	if regAPI.Len() != 0 {
		t.Error("api registry should end up empty")
	}
}

// TestReconciler_Run_Race exercises the ticker-driven path under -race with
// concurrent Register/Remove churn on the ProjectRegistry, mirroring
// TestProjectHealthController_Run.
func TestReconciler_Run_Race(t *testing.T) {
	docker := &fakeReconcileDocker{}
	pr := NewProjectRegistry()
	rc := NewReconciler(pr, docker, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go rc.Run(ctx, time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg := NewRegistry()
			reg.Add(Backend{ID: "b", Addr: "10.0.0.1:80"}) //nolint:errcheck
			pr.Register("svc", reg)
			time.Sleep(2 * time.Millisecond)
			pr.Remove("svc")
		}()
	}
	wg.Wait()
	cancel()
}

// TestReconciler_DeterministicOrdering proves services are reconciled in
// sorted order regardless of Docker's return order, mirroring
// executeRecoveryForProject/ProjectHealthController's own determinism
// guarantee — verified via the order ContainerInspect was actually called.
func TestReconciler_DeterministicOrdering(t *testing.T) {
	cZ, iZ := newFakeContainer("cid-z", "zebra", "z-1", "10.0.0.3", "3000", "gen-1")
	cA, iA := newFakeContainer("cid-a", "alpha", "a-1", "10.0.0.1", "3000", "gen-1")
	cM, iM := newFakeContainer("cid-m", "mike", "m-1", "10.0.0.2", "3000", "gen-1")

	docker := &fakeReconcileDocker{
		containers: []types.Container{cZ, cA, cM}, // deliberately out-of-order input
		inspects: map[string]types.ContainerJSON{
			"cid-z": iZ, "cid-a": iA, "cid-m": iM,
		},
	}

	pr := NewProjectRegistry()
	pr.Register("zebra", NewRegistry())
	pr.Register("alpha", NewRegistry())
	pr.Register("mike", NewRegistry())

	rc := NewReconciler(pr, docker, nil, nil)
	rc.ReconcileOnce(context.Background())

	got := docker.order()
	want := []string{"cid-a", "cid-m", "cid-z"} // alpha, mike, zebra — sorted service order
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected inspect order %v (sorted by service name), got %v", want, got)
	}
}

func TestReconciler_LogsCarryServiceField(t *testing.T) {
	cWeb, iWeb := newFakeContainer("cid-web", "web", "web-b1", "10.0.0.1", "3000", "gen-1")
	cAPI, iAPI := newFakeContainer("cid-api", "api", "api-b1", "10.0.0.2", "3000", "gen-1")
	docker := &fakeReconcileDocker{
		containers: []types.Container{cWeb, cAPI},
		inspects:   map[string]types.ContainerJSON{"cid-web": iWeb, "cid-api": iAPI},
	}

	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	pr.Register("api", NewRegistry())

	core, observed := observer.New(zapcore.InfoLevel)
	log := zap.New(core)

	rc := NewReconciler(pr, docker, nil, log)
	rc.ReconcileOnce(context.Background())

	var webTagged, apiTagged, untagged int
	for _, entry := range observed.All() {
		if entry.Message != "reconcile: backend added" {
			continue
		}
		fields := entry.ContextMap()
		svc, ok := fields["service"]
		if !ok {
			untagged++
			continue
		}
		switch svc {
		case "web":
			webTagged++
		case "api":
			apiTagged++
		default:
			t.Fatalf("unexpected service field value %q", svc)
		}
	}
	if untagged != 0 {
		t.Errorf("every reconcile log must carry a service field, got %d without one", untagged)
	}
	if webTagged == 0 || apiTagged == 0 {
		t.Error("expected backend-added logs tagged for both services")
	}
}

func TestReconciler_UnlabeledContainerIgnored(t *testing.T) {
	c := types.Container{ID: "cid-unlabeled", Labels: map[string]string{}}
	docker := &fakeReconcileDocker{containers: []types.Container{c}}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	rc := NewReconciler(pr, docker, nil, nil)
	rc.ReconcileOnce(context.Background()) // must not panic

	if reg.Len() != 0 {
		t.Fatalf("expected 0 backends, got %d", reg.Len())
	}
}

// TestReconciler_ProxyContainerIgnored proves the shared proxy's own
// container (orbit.io/service set, orbit.io/proxy=true, no
// ORBIT_BACKEND_ID) is never mistaken for one of its own backends, even
// though it carries the same service label its backends do.
func TestReconciler_ProxyContainerIgnored(t *testing.T) {
	proxyContainer := newFakeProxyContainer("cid-proxy", "web")
	docker := &fakeReconcileDocker{containers: []types.Container{proxyContainer}}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	rc := NewReconciler(pr, docker, nil, nil)
	rc.ReconcileOnce(context.Background())

	if reg.Len() != 0 {
		t.Fatalf("proxy's own container must never be registered as a backend, got %d backends", reg.Len())
	}
	if len(docker.order()) != 0 {
		t.Fatalf("proxy's own container should never even be inspected, got %d inspect calls", len(docker.order()))
	}
}

func TestReconciler_RecordsMetrics(t *testing.T) {
	c1, i1 := newFakeContainer("cid1", "web", "b1", "10.0.0.1", "3000", "gen-1")
	docker := &fakeReconcileDocker{
		containers: []types.Container{c1},
		inspects:   map[string]types.ContainerJSON{"cid1": i1},
	}
	reg := NewRegistry()
	addBackend(t, reg, "stale-1")
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	m := &spyReconcilerMetrics{}
	rc := NewReconciler(pr, docker, m, nil)
	rc.ReconcileOnce(context.Background())

	if m.runs.Load() != 1 {
		t.Errorf("expected 1 run, got %d", m.runs.Load())
	}
	if m.added.Load() != 1 {
		t.Errorf("expected 1 added, got %d", m.added.Load())
	}
	if m.removed.Load() != 1 {
		t.Errorf("expected 1 removed, got %d", m.removed.Load())
	}
	if m.failures.Load() != 0 {
		t.Errorf("expected 0 failures, got %d", m.failures.Load())
	}
	if m.durations.Load() != 1 {
		t.Errorf("expected 1 duration observation, got %d", m.durations.Load())
	}
}

func TestReconciler_NilMetricsSafe(t *testing.T) {
	docker := &fakeReconcileDocker{}
	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)
	rc.ReconcileOnce(context.Background()) // must not panic with nil metrics/logger
}

func TestReconciler_EmptyProjectRegistry(t *testing.T) {
	docker := &fakeReconcileDocker{}
	pr := NewProjectRegistry()
	rc := NewReconciler(pr, docker, nil, nil)
	rc.ReconcileOnce(context.Background()) // must not panic
}

// ── Grouping exclusion visibility (PR 4.2 final hardening, Issue 1) ────────

// TestReconciler_ProxyContainerExclusion_Logged proves the shared proxy's own
// container — excluded from grouping because it never belongs in a
// service's backend set — now produces a structured Warn, where it
// previously produced none. Behavior (never reconciled) is unchanged; only
// observability is added. Field values are asserted directly, never the
// free-text message.
func TestReconciler_ProxyContainerExclusion_Logged(t *testing.T) {
	proxyContainer := newFakeProxyContainer("cid-proxy", "web")
	docker := &fakeReconcileDocker{containers: []types.Container{proxyContainer}}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	core, observed := observer.New(zapcore.WarnLevel)
	log := zap.New(core)

	rc := NewReconciler(pr, docker, nil, log)
	rc.ReconcileOnce(context.Background())

	// Behavior unchanged: still never reconciled, still never inspected.
	if reg.Len() != 0 {
		t.Fatalf("proxy container must never be reconciled as a backend, got %d", reg.Len())
	}
	if len(docker.order()) != 0 {
		t.Fatalf("proxy container should never be inspected, got %d inspect calls", len(docker.order()))
	}

	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 warning, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if got := fields["container"]; got != shortContainerID("cid-proxy") {
		t.Errorf("container field = %v, want %q", got, shortContainerID("cid-proxy"))
	}
	if got := fields["service"]; got != "web" {
		t.Errorf("service field = %v, want %q", got, "web")
	}
	reason, ok := fields["reason"].(string)
	if !ok || reason == "" {
		t.Errorf("expected a non-empty reason field, got %v", fields["reason"])
	}
}

// TestReconciler_EmptyServiceLabelExclusion_Logged proves a container with no
// orbit.io/service label — unattributable to any service — now produces a
// structured Warn. No service field is asserted (there is none to report).
func TestReconciler_EmptyServiceLabelExclusion_Logged(t *testing.T) {
	c := types.Container{ID: "cid-unlabeled", Labels: map[string]string{"orbit.io/managed": "true"}}
	docker := &fakeReconcileDocker{containers: []types.Container{c}}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	core, observed := observer.New(zapcore.WarnLevel)
	log := zap.New(core)

	rc := NewReconciler(pr, docker, nil, log)
	rc.ReconcileOnce(context.Background())

	if reg.Len() != 0 {
		t.Fatalf("expected 0 backends, got %d", reg.Len())
	}

	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 warning, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if got := fields["container"]; got != shortContainerID("cid-unlabeled") {
		t.Errorf("container field = %v, want %q", got, shortContainerID("cid-unlabeled"))
	}
	reason, ok := fields["reason"].(string)
	if !ok || reason == "" {
		t.Errorf("expected a non-empty reason field, got %v", fields["reason"])
	}
}

// TestReconciler_GroupingExclusions_BehaviorUnchanged pins down that adding
// the two Warn calls above changed nothing about what gets reconciled:
// exclusion + inclusion in the same pass, same outcome as before hardening.
func TestReconciler_GroupingExclusions_BehaviorUnchanged(t *testing.T) {
	proxyContainer := newFakeProxyContainer("cid-proxy", "web")
	unlabeled := types.Container{ID: "cid-unlabeled", Labels: map[string]string{"orbit.io/managed": "true"}}
	cWeb, iWeb := newFakeContainer("cid-web", "web", "web-b1", "10.0.0.1", "3000", "gen-1")

	docker := &fakeReconcileDocker{
		containers: []types.Container{proxyContainer, unlabeled, cWeb},
		inspects:   map[string]types.ContainerJSON{"cid-web": iWeb},
	}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	m := &spyReconcilerMetrics{}
	rc := NewReconciler(pr, docker, m, nil)
	rc.ReconcileOnce(context.Background())

	if reg.Len() != 1 {
		t.Fatalf("expected exactly 1 backend (web-b1), got %d", reg.Len())
	}
	if _, ok := reg.Get("web-b1"); !ok {
		t.Error("web-b1 should be the only backend added")
	}
	if m.added.Load() != 1 {
		t.Errorf("expected 1 added, got %d", m.added.Load())
	}
	if m.failures.Load() != 0 {
		t.Errorf("grouping exclusions must not count as reconciliation failures, got %d", m.failures.Load())
	}
}

// ── Backend ID collision visibility (PR 4.2 final hardening, Issue 2) ──────

// TestReconciler_BackendIDCollision_Logged proves two live containers that
// derive the same backend ID now produce a structured Warn naming both
// containers, both addresses, and the backend ID — where previously the
// second silently overwrote the first with zero signal. The existing
// winner policy (last-seen-in-iteration-order, i.e. input slice order) is
// unchanged and asserted directly.
func TestReconciler_BackendIDCollision_Logged(t *testing.T) {
	c1, i1 := newFakeContainer("cid1", "web", "dup-id", "10.0.0.1", "3000", "gen-1")
	c2, i2 := newFakeContainer("cid2", "web", "dup-id", "10.0.0.2", "3000", "gen-2")
	docker := &fakeReconcileDocker{
		containers: []types.Container{c1, c2},
		inspects:   map[string]types.ContainerJSON{"cid1": i1, "cid2": i2},
	}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	core, observed := observer.New(zapcore.WarnLevel)
	log := zap.New(core)

	m := &spyReconcilerMetrics{}
	rc := NewReconciler(pr, docker, m, log)
	rc.ReconcileOnce(context.Background())

	// Registry remains consistent: exactly one backend, reconciliation
	// completed (not aborted), and the deterministic winner is the second
	// container in input order — c2 — matching the unchanged overwrite policy.
	if reg.Len() != 1 {
		t.Fatalf("expected exactly 1 backend after collision, got %d", reg.Len())
	}
	b, ok := reg.Get("dup-id")
	if !ok {
		t.Fatal("dup-id must still be registered")
	}
	if b.Addr != "10.0.0.2:3000" {
		t.Fatalf("winner policy changed: expected last-seen-in-order (c2, 10.0.0.2:3000), got %s", b.Addr)
	}
	if m.added.Load() != 1 {
		t.Fatalf("expected exactly 1 add (the winner), got %d", m.added.Load())
	}

	var collisionEntries []observer.LoggedEntry
	for _, e := range observed.All() {
		if _, ok := e.ContextMap()["existing_container"]; ok {
			collisionEntries = append(collisionEntries, e)
		}
	}
	if len(collisionEntries) != 1 {
		t.Fatalf("expected exactly 1 collision warning, got %d", len(collisionEntries))
	}
	fields := collisionEntries[0].ContextMap()
	if got := fields["id"]; got != "dup-id" {
		t.Errorf("id field = %v, want %q", got, "dup-id")
	}
	if got := fields["existing_container"]; got != shortContainerID("cid1") {
		t.Errorf("existing_container field = %v, want %q", got, shortContainerID("cid1"))
	}
	if got := fields["existing_addr"]; got != "10.0.0.1:3000" {
		t.Errorf("existing_addr field = %v, want %q", got, "10.0.0.1:3000")
	}
	if got := fields["new_container"]; got != shortContainerID("cid2") {
		t.Errorf("new_container field = %v, want %q", got, shortContainerID("cid2"))
	}
	if got := fields["new_addr"]; got != "10.0.0.2:3000" {
		t.Errorf("new_addr field = %v, want %q", got, "10.0.0.2:3000")
	}
	// service is carried by the pre-scoped logger, not re-added redundantly.
	if got := fields["service"]; got != "web" {
		t.Errorf("service field = %v, want %q (should come from the scoped logger)", got, "web")
	}
}

// TestReconciler_BackendIDCollision_DeterministicAcrossRepeatedPasses proves
// the winner policy is stable: re-running reconciliation against the exact
// same (unchanged) Docker input always picks the same winner, since the
// live-set is built from an ordered slice (ContainerList's own return
// order), never Go's randomized map iteration.
func TestReconciler_BackendIDCollision_DeterministicAcrossRepeatedPasses(t *testing.T) {
	c1, i1 := newFakeContainer("cid1", "web", "dup-id", "10.0.0.1", "3000", "gen-1")
	c2, i2 := newFakeContainer("cid2", "web", "dup-id", "10.0.0.2", "3000", "gen-2")
	docker := &fakeReconcileDocker{
		containers: []types.Container{c1, c2},
		inspects:   map[string]types.ContainerJSON{"cid1": i1, "cid2": i2},
	}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	rc := NewReconciler(pr, docker, nil, nil)
	for i := 0; i < 5; i++ {
		rc.ReconcileOnce(context.Background())
		b, ok := reg.Get("dup-id")
		if !ok || b.Addr != "10.0.0.2:3000" {
			t.Fatalf("pass %d: winner changed or vanished — got %+v, ok=%v", i, b, ok)
		}
	}
}

// ── Re-entrancy guard (PR 4.3 final hardening, Issue 1) ────────────────────

// TestReconciler_ReentrancyGuard_RejectsConcurrentInvocation proves a second,
// concurrent ReconcileOnce call is rejected immediately (never blocks, never
// queues) while a first pass is still in flight, and that the rejected call
// never touches the Registry.
func TestReconciler_ReentrancyGuard_RejectsConcurrentInvocation(t *testing.T) {
	entered := make(chan struct{})
	block := make(chan struct{})
	docker := &fakeReconcileDocker{entered: entered, listBlock: block}

	reg := NewRegistry()
	addBackend(t, reg, "existing-1")
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	m := &spyReconcilerMetrics{}
	core, observed := observer.New(zapcore.WarnLevel)
	log := zap.New(core)
	rc := NewReconciler(pr, docker, m, log)

	firstDone := make(chan struct{})
	go func() {
		rc.ReconcileOnce(context.Background())
		close(firstDone)
	}()

	<-entered // first call is now blocked inside ContainerList

	secondDone := make(chan struct{})
	go func() {
		rc.ReconcileOnce(context.Background()) // must reject, not block
		close(secondDone)
	}()

	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second ReconcileOnce call did not return promptly — it must reject a concurrent invocation, never block or queue")
	}

	// The rejected second call must never have touched the Registry while
	// the first pass was still blocked mid-flight.
	if reg.Len() != 1 {
		t.Fatalf("registry must be untouched by the rejected invocation, got %d backends", reg.Len())
	}

	close(block) // release the first pass
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first ReconcileOnce call did not complete after unblocking")
	}

	if m.rejected.Load() != 1 {
		t.Fatalf("expected exactly 1 rejected invocation recorded, got %d", m.rejected.Load())
	}

	var found bool
	for _, e := range observed.All() {
		if e.Message == "reconcile: rejected concurrent invocation" {
			found = true
		}
	}
	if !found {
		t.Error("expected a structured warning logged for the rejected invocation")
	}
}

// TestReconciler_ReentrancyGuard_ResetsAfterCompletion proves the guard is
// not permanently latched: once a pass completes (successfully or not),
// the next sequential call proceeds normally — sequential reconciliation
// remains unaffected by the guard.
func TestReconciler_ReentrancyGuard_ResetsAfterCompletion(t *testing.T) {
	c1, i1 := newFakeContainer("cid1", "web", "b1", "10.0.0.1", "3000", "gen-1")
	docker := &fakeReconcileDocker{
		containers: []types.Container{c1},
		inspects:   map[string]types.ContainerJSON{"cid1": i1},
	}
	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	m := &spyReconcilerMetrics{}
	rc := NewReconciler(pr, docker, m, nil)

	rc.ReconcileOnce(context.Background())
	rc.ReconcileOnce(context.Background())
	rc.ReconcileOnce(context.Background())

	if reg.Len() != 1 {
		t.Fatalf("expected 1 backend after 3 sequential passes, got %d", reg.Len())
	}
	if m.rejected.Load() != 0 {
		t.Fatalf("sequential (non-concurrent) calls must never be rejected, got %d rejections", m.rejected.Load())
	}
	if m.runs.Load() != 3 {
		t.Fatalf("expected 3 runs recorded, got %d", m.runs.Load())
	}
}

// TestReconciler_ReentrancyGuard_Race hammers ReconcileOnce with many
// concurrent goroutines under -race: no data race, no panic, at least one
// call succeeds, Registry converges correctly, and every rejection (if any)
// is accounted for in the metric.
func TestReconciler_ReentrancyGuard_Race(t *testing.T) {
	c1, i1 := newFakeContainer("cid1", "web", "b1", "10.0.0.1", "3000", "gen-1")
	docker := &fakeReconcileDocker{
		containers: []types.Container{c1},
		inspects:   map[string]types.ContainerJSON{"cid1": i1},
	}
	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)

	m := &spyReconcilerMetrics{}
	rc := NewReconciler(pr, docker, m, nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc.ReconcileOnce(context.Background())
		}()
	}
	wg.Wait()

	if reg.Len() != 1 {
		t.Fatalf("expected exactly 1 backend after concurrent storm, got %d", reg.Len())
	}
	if m.runs.Load()+m.rejected.Load() != 20 {
		t.Fatalf("expected runs+rejected to account for all 20 invocations, got runs=%d rejected=%d", m.runs.Load(), m.rejected.Load())
	}
	if m.runs.Load() == 0 {
		t.Fatal("expected at least 1 call to actually run, got 0")
	}
}
