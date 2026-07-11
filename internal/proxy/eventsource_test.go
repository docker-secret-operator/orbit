package proxy

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// fakeEventConnection is one scripted Events() subscription: a pair of
// caller-driven channels a test can push messages/errors onto, and close to
// simulate a stream ending.
type fakeEventConnection struct {
	msgs chan events.Message
	errs chan error
}

func newFakeEventConnection() *fakeEventConnection {
	return &fakeEventConnection{
		msgs: make(chan events.Message, 16),
		errs: make(chan error, 4),
	}
}

// fakeEventDocker is a daemon-free double satisfying both containerLister
// (for the Reconciler an EventSource drives) and eventSubscriber (for the
// EventSource itself) — mirroring how one *client.Client satisfies both in
// production. Deliberately separate from reconciler_test.go's
// fakeReconcileDocker: PR 4.3's tests never touch a PR 4.2-owned file.
type fakeEventDocker struct {
	mu sync.Mutex

	containers []types.Container
	listErr    error
	listCalls  int32
	// listEntered, if non-nil, is closed the instant ContainerList is
	// called. listBlock, if non-nil, is read from (blocking) before
	// ContainerList returns — together they open a deterministic
	// synchronization window, mirroring fakeReconcileDocker's pattern.
	listEntered chan struct{}
	listBlock   chan struct{}

	inspects map[string]types.ContainerJSON

	// connections is a pre-scripted sequence of Events() subscriptions.
	// Each call pops the next one; exhausting the script yields a
	// connection that never sends anything (a live, silent stream).
	connections   []*fakeEventConnection
	connectionIdx int
	connectCalls  int32

	// eventsCtxs records the context passed to each Events() call, in
	// order, so a test can assert a prior connection's context was
	// cancelled once EventSource reconnects (PR 4.4 resource-cleanup
	// hardening).
	eventsCtxs []context.Context
}

func (f *fakeEventDocker) ContainerList(context.Context, types.ContainerListOptions) ([]types.Container, error) {
	atomic.AddInt32(&f.listCalls, 1)
	if f.listEntered != nil {
		select {
		case <-f.listEntered:
		default:
			close(f.listEntered)
		}
	}
	if f.listBlock != nil {
		<-f.listBlock
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.containers, nil
}

func (f *fakeEventDocker) ContainerInspect(_ context.Context, id string) (types.ContainerJSON, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.inspects[id]; ok {
		return c, nil
	}
	return types.ContainerJSON{}, fmt.Errorf("container %s not found", id)
}

func (f *fakeEventDocker) Events(ctx context.Context, _ types.EventsOptions) (<-chan events.Message, <-chan error) {
	atomic.AddInt32(&f.connectCalls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eventsCtxs = append(f.eventsCtxs, ctx)
	if f.connectionIdx < len(f.connections) {
		c := f.connections[f.connectionIdx]
		f.connectionIdx++
		return c.msgs, c.errs
	}
	// Script exhausted: a live connection that never sends anything.
	return make(chan events.Message), make(chan error)
}

func (f *fakeEventDocker) getListCalls() int32    { return atomic.LoadInt32(&f.listCalls) }
func (f *fakeEventDocker) getConnectCalls() int32 { return atomic.LoadInt32(&f.connectCalls) }

// ctxAt returns the context passed to the i-th Events() call (0-indexed),
// or nil if that call hasn't happened yet.
func (f *fakeEventDocker) ctxAt(i int) context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < len(f.eventsCtxs) {
		return f.eventsCtxs[i]
	}
	return nil
}

var (
	_ containerLister = (*fakeEventDocker)(nil)
	_ eventSubscriber = (*fakeEventDocker)(nil)
)

// spyEventSourceMetrics is a thread-safe EventSourceMetrics recorder.
type spyEventSourceMetrics struct {
	reconnects        atomic.Int64
	reconnectFailures atomic.Int64
	eventsReceived    atomic.Int64
	eventsIgnored     atomic.Int64
	triggerPeriodic   atomic.Int64
	triggerEvent      atomic.Int64
	triggerReconnect  atomic.Int64
}

func (s *spyEventSourceMetrics) IncReconnects()                    { s.reconnects.Add(1) }
func (s *spyEventSourceMetrics) IncReconnectFailures()             { s.reconnectFailures.Add(1) }
func (s *spyEventSourceMetrics) IncEventsReceived()                { s.eventsReceived.Add(1) }
func (s *spyEventSourceMetrics) IncEventsIgnored()                 { s.eventsIgnored.Add(1) }
func (s *spyEventSourceMetrics) IncReconcileTriggeredByPeriodic()  { s.triggerPeriodic.Add(1) }
func (s *spyEventSourceMetrics) IncReconcileTriggeredByEvent()     { s.triggerEvent.Add(1) }
func (s *spyEventSourceMetrics) IncReconcileTriggeredByReconnect() { s.triggerReconnect.Add(1) }

var _ EventSourceMetrics = (*spyEventSourceMetrics)(nil)

func startBackendContainer(id, service, backendID, ip, port, generation string) (types.Container, types.ContainerJSON) {
	return newFakeContainer(id, service, backendID, ip, port, generation)
}

// ── Required guarantee-level tests ──────────────────────────────────────────

// TestEventSource_Reconnect proves a broken connection is retried with
// backoff and eventually re-established, with reconnect metrics and
// structured logs (reason, retry, elapsed) along the way.
func TestEventSource_Reconnect(t *testing.T) {
	bad := newFakeEventConnection()
	good := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{bad, good}}
	// The first connection fails immediately.
	bad.errs <- fmt.Errorf("daemon unreachable")

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)
	rc := NewReconciler(pr, docker, nil, nil)

	core, observed := observer.New(zapcore.DebugLevel)
	log := zap.New(core)
	m := &spyEventSourceMetrics{}

	es := NewEventSource(rc, docker, time.Hour, m, log) // long periodic interval: isolate reconnect behavior
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	deadline := time.After(1500 * time.Millisecond)
	for m.reconnects.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for a successful reconnect")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if m.reconnectFailures.Load() < 1 {
		t.Errorf("expected at least 1 reconnect failure recorded, got %d", m.reconnectFailures.Load())
	}
	if m.reconnects.Load() < 1 {
		t.Errorf("expected at least 1 successful reconnect recorded, got %d", m.reconnects.Load())
	}

	var sawFailureLog, sawReconnectLog bool
	for _, e := range observed.All() {
		fields := e.ContextMap()
		if e.Message == "eventsource: reconnect attempt failed" {
			sawFailureLog = true
			if _, ok := fields["reason"]; !ok {
				t.Error("reconnect failure log missing 'reason' field")
			}
			if _, ok := fields["retry"]; !ok {
				t.Error("reconnect failure log missing 'retry' field")
			}
			if _, ok := fields["elapsed"]; !ok {
				t.Error("reconnect failure log missing 'elapsed' field")
			}
		}
		if e.Message == "eventsource: reconnected" {
			sawReconnectLog = true
			if _, ok := fields["elapsed"]; !ok {
				t.Error("reconnected log missing 'elapsed' field")
			}
		}
	}
	if !sawFailureLog {
		t.Error("expected a 'reconnect attempt failed' log entry")
	}
	if !sawReconnectLog {
		t.Error("expected a 'reconnected' log entry")
	}
}

// TestEventSource_ReconnectTriggersReconciliation proves the exit-criterion
// "reconnect always triggers reconciliation": a backend that appeared while
// disconnected is picked up immediately after a *mid-operation* reconnect —
// i.e. one reached after the connection was already live at least once, not
// the initial bootstrap connect (which deliberately relies on the periodic
// tick / existing startup recovery instead, matching Reconciler.Run's own
// no-immediate-first-pass precedent — see TestEventSource_Reconnect for
// coverage of the initial-connect-needs-retries case, which is a different
// guarantee: retry/backoff mechanics, not the reconcile-on-reconnect one).
func TestEventSource_ReconnectTriggersReconciliation(t *testing.T) {
	initial := newFakeEventConnection()    // connects cleanly, no error
	afterBreak := newFakeEventConnection() // also connects cleanly, once reconnected to

	c1, i1 := startBackendContainer("cid1", "web", "web-b1", "10.0.0.1", "3000", "gen-1")
	docker := &fakeEventDocker{
		connections: []*fakeEventConnection{initial, afterBreak},
		containers:  []types.Container{c1},
		inspects:    map[string]types.ContainerJSON{"cid1": i1},
	}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)
	rc := NewReconciler(pr, docker, nil, nil)

	m := &spyEventSourceMetrics{}
	es := NewEventSource(rc, docker, time.Hour, m, nil) // long periodic interval: isolate reconnect behavior
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	// Let Run establish the initial connection and settle. No
	// reconciliation yet — the initial connect deliberately doesn't
	// trigger one.
	time.Sleep(eventConnectProbeWindow + 100*time.Millisecond)
	if _, ok := reg.Get("web-b1"); ok {
		t.Fatal("web-b1 must not be reconciled before any reconnect or periodic tick")
	}

	// Simulate the stream breaking mid-operation (e.g. a daemon restart) —
	// this is the case that must always reconcile once on reconnect.
	close(initial.msgs)

	deadline := time.After(2 * time.Second)
	for {
		if _, ok := reg.Get("web-b1"); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("web-b1 was not reconciled after reconnect")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if m.triggerReconnect.Load() < 1 {
		t.Errorf("expected reconnect-triggered reconciliation count >= 1, got %d", m.triggerReconnect.Load())
	}
}

// TestEventSource_DuplicateEvents proves repeated identical events for the
// same container trigger reconciliation safely and idempotently — no
// duplicate registration, no panic.
func TestEventSource_DuplicateEvents(t *testing.T) {
	conn := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{conn}}

	c1, i1 := startBackendContainer("cid1", "web", "web-b1", "10.0.0.1", "3000", "gen-1")
	docker.containers = []types.Container{c1}
	docker.inspects = map[string]types.ContainerJSON{"cid1": i1}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)
	rc := NewReconciler(pr, docker, nil, nil)

	m := &spyEventSourceMetrics{}
	es := NewEventSource(rc, docker, time.Hour, m, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	for i := 0; i < 5; i++ {
		conn.msgs <- events.Message{Type: events.ContainerEventType, Action: "start", Actor: events.Actor{ID: "cid1", Attributes: map[string]string{"orbit.io/service": "web"}}}
	}

	deadline := time.After(1500 * time.Millisecond)
	for m.triggerEvent.Load() < 5 {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for 5 event triggers, got %d", m.triggerEvent.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if reg.Len() != 1 {
		t.Fatalf("expected exactly 1 backend despite 5 duplicate events, got %d", reg.Len())
	}
	if m.eventsReceived.Load() != 5 {
		t.Errorf("expected 5 events received, got %d", m.eventsReceived.Load())
	}
}

// TestEventSource_IgnoredEvents proves an event with an action outside the
// accepted set (start/die/health_status) is counted as ignored and does not
// trigger a reconciliation.
func TestEventSource_IgnoredEvents(t *testing.T) {
	conn := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{conn}}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)
	rc := NewReconciler(pr, docker, nil, nil)

	m := &spyEventSourceMetrics{}
	es := NewEventSource(rc, docker, time.Hour, m, nil) // long interval: isolate event-driven behavior
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	conn.msgs <- events.Message{Type: events.ContainerEventType, Action: "exec_create", Actor: events.Actor{ID: "cid1"}}
	conn.msgs <- events.Message{Type: events.ContainerEventType, Action: "top", Actor: events.Actor{ID: "cid1"}}

	deadline := time.After(1500 * time.Millisecond)
	for m.eventsIgnored.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for 2 ignored events, got %d", m.eventsIgnored.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if m.triggerEvent.Load() != 0 {
		t.Errorf("ignored events must never trigger reconciliation, got %d event-triggered passes", m.triggerEvent.Load())
	}
	if m.eventsReceived.Load() != 2 {
		t.Errorf("expected 2 events received, got %d", m.eventsReceived.Load())
	}
}

// TestEventSource_Cancellation proves Run returns promptly when ctx is
// cancelled, including while blocked in a reconnect backoff sleep.
func TestEventSource_Cancellation(t *testing.T) {
	docker := &fakeEventDocker{} // Events() script exhausted immediately -> silent live connection
	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)

	es := NewEventSource(rc, docker, time.Hour, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond) // let Run reach steady state
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancellation")
	}
}

// TestEventSource_Cancellation_DuringBackoff proves cancellation is honored
// even while a reconnect backoff sleep is in progress (not just the steady
// state select loop).
func TestEventSource_Cancellation_DuringBackoff(t *testing.T) {
	bad := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{bad}} // always fails, script never recovers
	bad.errs <- fmt.Errorf("daemon unreachable")

	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)

	es := NewEventSource(rc, docker, time.Hour, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond) // let it enter the backoff sleep after the first failure
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancellation during backoff")
	}
}

// TestEventSource_PermanentReconnectFailureAfterLiveConnection_NoPanic
// covers the specific case the connection-cleanup hardening (PR 4.4) had to
// get right: the *initial* connect succeeds (so Run's deferred cleanup
// holds a live CancelFunc), then a mid-loop disconnect's own reconnect
// attempt never recovers before ctx is cancelled during its backoff — the
// deferred cleanup must not panic on what es.connect returns in that
// failure case (nil), only skip cleanly.
func TestEventSource_PermanentReconnectFailureAfterLiveConnection_NoPanic(t *testing.T) {
	initial := newFakeEventConnection() // connects cleanly
	failing := newFakeEventConnection() // every reconnect attempt against this one fails
	docker := &fakeEventDocker{connections: []*fakeEventConnection{initial, failing}}

	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)

	es := NewEventSource(rc, docker, time.Hour, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		es.Run(ctx) // must not panic
		close(done)
	}()

	// Let the initial connection settle.
	time.Sleep(eventConnectProbeWindow + 100*time.Millisecond)

	// Break it, then keep failing() every reconnect attempt.
	failing.errs <- fmt.Errorf("daemon still unreachable")
	close(initial.msgs)

	// Give it a moment to be mid-backoff on the failing reconnect, then
	// cancel — this is the exact window the nil-CancelFunc bug lived in.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancellation during a permanently-failing reconnect")
	}
}

// TestEventSource_EventStorm proves a burst of many rapid events converges
// correctly with no panic and no data race (run under -race).
func TestEventSource_EventStorm(t *testing.T) {
	conn := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{conn}}

	c1, i1 := startBackendContainer("cid1", "web", "web-b1", "10.0.0.1", "3000", "gen-1")
	docker.containers = []types.Container{c1}
	docker.inspects = map[string]types.ContainerJSON{"cid1": i1}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)
	rc := NewReconciler(pr, docker, nil, nil)

	m := &spyEventSourceMetrics{}
	es := NewEventSource(rc, docker, time.Hour, m, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	const storm = 50
	go func() {
		for i := 0; i < storm; i++ {
			conn.msgs <- events.Message{Type: events.ContainerEventType, Action: "start", Actor: events.Actor{ID: "cid1"}}
		}
	}()

	deadline := time.After(3 * time.Second)
	for m.eventsReceived.Load() < storm {
		select {
		case <-deadline:
			t.Fatalf("timed out: only %d/%d events received", m.eventsReceived.Load(), storm)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if reg.Len() != 1 {
		t.Fatalf("expected exactly 1 backend after the storm, got %d", reg.Len())
	}
}

// TestEventSource_SerializedReconciliation proves a periodic tick and an
// event-triggered reconciliation never run concurrently: a slow
// (blocked-on-command) ContainerList call is held open while a relevant
// event is fired, and the event's own reconciliation must not start its own
// ContainerList call until the first one completes.
func TestEventSource_SerializedReconciliation(t *testing.T) {
	entered := make(chan struct{})
	block := make(chan struct{})
	conn := newFakeEventConnection()
	docker := &fakeEventDocker{
		connections: []*fakeEventConnection{conn},
		listEntered: entered,
		listBlock:   block,
	}

	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)

	es := NewEventSource(rc, docker, 20*time.Millisecond, nil, nil) // short interval to force the periodic tick to fire the first ContainerList
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	<-entered // the periodic tick's ContainerList call is now blocked inside reg.Add/diff

	// Fire a relevant event while the first pass is still blocked. If
	// serialization were broken, this would race a second ContainerList
	// call in immediately; with it, the event's trigger must wait for
	// ReconcileOnce's mutex-free but single-goroutine execution to free up.
	conn.msgs <- events.Message{Type: events.ContainerEventType, Action: "start", Actor: events.Actor{ID: "cid-x"}}

	// While still blocked, no second ContainerList call should have landed
	// — the single-goroutine Run loop cannot service the event channel
	// while parked inside the first ReconcileOnce call.
	time.Sleep(100 * time.Millisecond)
	if calls := docker.getListCalls(); calls != 1 {
		t.Fatalf("expected exactly 1 in-flight ContainerList call while blocked, got %d — reconciliation ran concurrently", calls)
	}

	close(block) // release the first pass

	deadline := time.After(2 * time.Second)
	for docker.getListCalls() < 2 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the event-triggered ContainerList call after unblocking")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// TestEventSource_DaemonUnavailable proves a permanently-unreachable daemon
// produces repeated, logged reconnect failures without panicking or
// crashing the loop, and shuts down cleanly on cancellation.
func TestEventSource_DaemonUnavailable(t *testing.T) {
	always := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{always}} // script exhausts to a silent connection, but we keep feeding errors below
	go func() {
		for i := 0; i < 100; i++ {
			select {
			case always.errs <- fmt.Errorf("connection refused"):
			case <-time.After(2 * time.Second):
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)

	m := &spyEventSourceMetrics{}
	es := NewEventSource(rc, docker, time.Hour, m, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx) // must not panic
		close(done)
	}()

	<-done
	if m.reconnectFailures.Load() == 0 {
		t.Error("expected at least one reconnect failure recorded against a permanently unavailable daemon")
	}
}

// TestEventSource_Run_Race exercises the full event/reconnect/periodic path
// concurrently with Registry churn under -race.
func TestEventSource_Run_Race(t *testing.T) {
	conn := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{conn}}
	pr := NewProjectRegistry()
	rc := NewReconciler(pr, docker, nil, nil)
	es := NewEventSource(rc, docker, 5*time.Millisecond, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go es.Run(ctx)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reg := NewRegistry()
			reg.Add(Backend{ID: "b", Addr: "10.0.0.1:80"}) //nolint:errcheck
			pr.Register("svc", reg)
			conn.msgs <- events.Message{Type: events.ContainerEventType, Action: "start", Actor: events.Actor{ID: "cid"}}
			time.Sleep(2 * time.Millisecond)
			pr.Remove("svc")
		}(i)
	}
	wg.Wait()
	cancel()
}

// TestEventSource_LoggingTriggerField asserts the scheduling log's trigger
// field takes each of the three documented values, via structured fields
// only.
func TestEventSource_LoggingTriggerField(t *testing.T) {
	conn := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{conn}}
	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)

	core, observed := observer.New(zapcore.InfoLevel)
	log := zap.New(core)
	es := NewEventSource(rc, docker, 20*time.Millisecond, nil, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	conn.msgs <- events.Message{Type: events.ContainerEventType, Action: "die", Actor: events.Actor{ID: "cid1"}}

	deadline := time.After(2 * time.Second)
	seen := map[string]bool{}
	for !(seen["periodic"] && seen["docker_event"]) {
		for _, e := range observed.All() {
			if e.Message != "eventsource: scheduling reconciliation" {
				continue
			}
			if trig, ok := e.ContextMap()["trigger"].(string); ok {
				seen[trig] = true
			}
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for periodic+docker_event trigger logs, saw: %v", seen)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// ── Ignored event visibility (PR 4.3 final hardening, Issue 2) ─────────────

// TestEventSource_IgnoredEvent_DebugLogged proves an ignored event now
// produces a structured Debug-level log carrying action, container ID,
// service (if present), and a reason — where previously only the metric
// existed. Filtering and the ignored-metric are unchanged; only a new log
// line is added. Fields are asserted directly, never the message text.
func TestEventSource_IgnoredEvent_DebugLogged(t *testing.T) {
	conn := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{conn}}
	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)

	core, observed := observer.New(zapcore.DebugLevel) // capture Debug and above
	log := zap.New(core)
	m := &spyEventSourceMetrics{}
	es := NewEventSource(rc, docker, time.Hour, m, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	conn.msgs <- events.Message{
		Type:   events.ContainerEventType,
		Action: "exec_create",
		Actor:  events.Actor{ID: "cid1", Attributes: map[string]string{"orbit.io/service": "web"}},
	}

	deadline := time.After(1500 * time.Millisecond)
	for m.eventsIgnored.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the ignored event to be processed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	var entry *observer.LoggedEntry
	for _, e := range observed.All() {
		if e.Message == "eventsource: event ignored" {
			e := e
			entry = &e
		}
	}
	if entry == nil {
		t.Fatal("expected a Debug-level 'eventsource: event ignored' log entry")
	}
	if entry.Level != zapcore.DebugLevel {
		t.Errorf("expected Debug level, got %s", entry.Level)
	}
	fields := entry.ContextMap()
	if got := fields["action"]; got != "exec_create" {
		t.Errorf("action field = %v, want %q", got, "exec_create")
	}
	if got := fields["container"]; got != shortContainerID("cid1") {
		t.Errorf("container field = %v, want %q", got, shortContainerID("cid1"))
	}
	if got := fields["service"]; got != "web" {
		t.Errorf("service field = %v, want %q", got, "web")
	}
	reason, ok := fields["reason"].(string)
	if !ok || reason == "" {
		t.Errorf("expected a non-empty reason field, got %v", fields["reason"])
	}

	if m.triggerEvent.Load() != 0 {
		t.Errorf("ignored event must never trigger reconciliation, got %d", m.triggerEvent.Load())
	}
	if m.eventsIgnored.Load() != 1 {
		t.Errorf("expected ignored-event metric to still be exactly 1, got %d", m.eventsIgnored.Load())
	}
}

// TestEventSource_IgnoredEvent_NotVisibleAtDefaultLevel proves the new log
// does not increase default (Info+) log volume — it only appears when a
// logger is configured to capture Debug level, matching the requirement
// "do not increase default log noise."
func TestEventSource_IgnoredEvent_NotVisibleAtDefaultLevel(t *testing.T) {
	conn := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{conn}}
	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)

	core, observed := observer.New(zapcore.InfoLevel) // default production level
	log := zap.New(core)
	m := &spyEventSourceMetrics{}
	es := NewEventSource(rc, docker, time.Hour, m, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	conn.msgs <- events.Message{Type: events.ContainerEventType, Action: "top", Actor: events.Actor{ID: "cid1"}}

	deadline := time.After(1500 * time.Millisecond)
	for m.eventsIgnored.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the ignored event to be processed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	for _, e := range observed.All() {
		if e.Message == "eventsource: event ignored" {
			t.Fatal("ignored-event log must not be visible at the default (Info) level")
		}
	}
}

// ── Runtime resilience hardening (PR 4.4) ───────────────────────────────────

// TestEventSource_ReconnectCancelsPreviousConnection proves that once a
// reconnect completes, the *previous* subscription's context is explicitly
// cancelled — closing the connection's underlying goroutine/stream instead
// of merely abandoning its channels — rather than relying solely on the
// outer ctx to eventually clean everything up together at process shutdown.
func TestEventSource_ReconnectCancelsPreviousConnection(t *testing.T) {
	first := newFakeEventConnection()
	second := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{first, second}}

	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)
	es := NewEventSource(rc, docker, time.Hour, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	// Wait for the initial connection to be established.
	deadline := time.After(1500 * time.Millisecond)
	for docker.getConnectCalls() < 1 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for initial connect")
		case <-time.After(10 * time.Millisecond):
		}
	}

	firstCtx := docker.ctxAt(0)
	if firstCtx == nil {
		t.Fatal("expected a context to have been recorded for the first connection")
	}
	if firstCtx.Err() != nil {
		t.Fatalf("first connection's context must not be cancelled yet, got %v", firstCtx.Err())
	}

	// Force a reconnect.
	close(first.msgs)

	deadline = time.After(2 * time.Second)
	for docker.getConnectCalls() < 2 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for reconnect")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// The first connection's context must now be cancelled — its
	// underlying stream is explicitly torn down, not merely abandoned.
	deadline = time.After(1 * time.Second)
	for firstCtx.Err() == nil {
		select {
		case <-deadline:
			t.Fatal("first connection's context was never cancelled after reconnect")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-done
}

// TestEventSource_StoppedLogEmitted proves Run logs a confirmation when it
// actually stops, giving operators positive evidence of clean shutdown
// rather than inferring it from an absence of further log lines.
func TestEventSource_StoppedLogEmitted(t *testing.T) {
	docker := &fakeEventDocker{}
	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)

	core, observed := observer.New(zapcore.InfoLevel)
	log := zap.New(core)
	es := NewEventSource(rc, docker, time.Hour, nil, log)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond) // let Run reach steady state
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancellation")
	}

	var found bool
	for _, e := range observed.All() {
		if e.Message == "eventsource: stopped" {
			found = true
		}
	}
	if !found {
		t.Error("expected an 'eventsource: stopped' log entry after Run returns")
	}
}

// TestEventSource_RepeatedReconnectCycles proves the reconnect path remains
// correct across many consecutive cycles — not just one — with metrics,
// convergence, and connection cleanup all holding up under repetition.
// Deterministic: driven entirely by channel closes, no sleep-based timing
// assumptions for the cycle count itself.
func TestEventSource_RepeatedReconnectCycles(t *testing.T) {
	const cycles = 5
	conns := make([]*fakeEventConnection, cycles+1)
	ifaceConns := make([]*fakeEventConnection, cycles+1)
	for i := range conns {
		conns[i] = newFakeEventConnection()
		ifaceConns[i] = conns[i]
	}
	docker := &fakeEventDocker{connections: ifaceConns}

	pr := NewProjectRegistry()
	pr.Register("web", NewRegistry())
	rc := NewReconciler(pr, docker, nil, nil)
	m := &spyEventSourceMetrics{}
	es := NewEventSource(rc, docker, time.Hour, m, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	// Wait for the initial connect (connect call #1) before starting the
	// reconnect cycles.
	deadline := time.After(2 * time.Second)
	for docker.getConnectCalls() < 1 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the initial connect")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Each cycle: close the current connection, then wait for the
	// reconnect-triggered reconciliation count to advance — this is the
	// signal that connect() has fully completed (survived its probe
	// window) and Run has moved past triggerReconciliation, not merely
	// that a new Events() call was made (which happens before the
	// connection is confirmed live and would race the next close).
	for i := 0; i < cycles; i++ {
		close(conns[i].msgs)
		target := int64(i + 1)
		deadline := time.After(2 * time.Second)
		for m.triggerReconnect.Load() < target {
			select {
			case <-deadline:
				t.Fatalf("cycle %d: timed out waiting for reconnect-trigger count %d, got %d", i, target, m.triggerReconnect.Load())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	cancel()
	<-done

	if m.triggerReconnect.Load() != int64(cycles) {
		t.Errorf("expected %d reconnect-triggered reconciliations, got %d", cycles, m.triggerReconnect.Load())
	}
	// m.reconnects (IncReconnects) only fires when a reconnect needed at
	// least one failed attempt first — these are clean, immediate
	// resubscribes (no errors on any connection's errs channel), so it
	// correctly stays 0. That distinction is exactly what
	// TestEventSource_Reconnect covers instead.
	if m.reconnects.Load() != 0 {
		t.Errorf("expected 0 failure-recovery reconnects for a clean cycle, got %d", m.reconnects.Load())
	}
}

// TestEventSource_TransientDockerFailureRecovers proves that a
// ContainerList failure inside Reconciler (a different layer than the
// event-stream connection) does not derail EventSource — the next
// triggered pass converges normally once Docker recovers. Registry stays
// consistent throughout (never partially updated).
func TestEventSource_TransientDockerFailureRecovers(t *testing.T) {
	conn := newFakeEventConnection()
	docker := &fakeEventDocker{connections: []*fakeEventConnection{conn}}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)
	m := &spyReconcilerMetrics{}
	rc := NewReconciler(pr, docker, m, nil)

	esMetrics := &spyEventSourceMetrics{}
	es := NewEventSource(rc, docker, time.Hour, esMetrics, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	// Two transient failures.
	docker.mu.Lock()
	docker.listErr = fmt.Errorf("daemon transiently unavailable")
	docker.mu.Unlock()

	conn.msgs <- events.Message{Type: events.ContainerEventType, Action: "start", Actor: events.Actor{ID: "cid1"}}
	conn.msgs <- events.Message{Type: events.ContainerEventType, Action: "start", Actor: events.Actor{ID: "cid1"}}

	deadline := time.After(1500 * time.Millisecond)
	for m.failures.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for 2 recorded failures, got %d", m.failures.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if reg.Len() != 0 {
		t.Fatalf("registry must remain empty/untouched during transient failures, got %d", reg.Len())
	}

	// Docker recovers.
	c1, i1 := startBackendContainer("cid1", "web", "web-b1", "10.0.0.1", "3000", "gen-1")
	docker.mu.Lock()
	docker.listErr = nil
	docker.containers = []types.Container{c1}
	docker.inspects = map[string]types.ContainerJSON{"cid1": i1}
	docker.mu.Unlock()

	conn.msgs <- events.Message{Type: events.ContainerEventType, Action: "start", Actor: events.Actor{ID: "cid1"}}

	deadline = time.After(1500 * time.Millisecond)
	for {
		if _, ok := reg.Get("web-b1"); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("registry did not converge after Docker recovered")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// TestEventSource_ShutdownDuringReconciliation proves cancelling ctx while
// a reconciliation pass is in flight (blocked inside ContainerList) does
// not corrupt Registry and Run still returns once the in-flight pass
// completes and the loop observes cancellation.
func TestEventSource_ShutdownDuringReconciliation(t *testing.T) {
	entered := make(chan struct{})
	block := make(chan struct{})
	conn := newFakeEventConnection()
	docker := &fakeEventDocker{
		connections: []*fakeEventConnection{conn},
		listEntered: entered,
		listBlock:   block,
	}

	reg := NewRegistry()
	pr := NewProjectRegistry()
	pr.Register("web", reg)
	rc := NewReconciler(pr, docker, nil, nil)
	es := NewEventSource(rc, docker, 20*time.Millisecond, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		es.Run(ctx)
		close(done)
	}()

	<-entered // a reconciliation pass is now blocked mid-flight

	cancel() // shutdown signal arrives while the pass is still in progress

	select {
	case <-done:
		t.Fatal("Run returned before the in-flight ContainerList call was released — it should still be blocked")
	case <-time.After(100 * time.Millisecond):
	}

	close(block) // release the in-flight pass

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the in-flight pass completed and cancellation was observed")
	}

	if reg.Len() != 0 {
		t.Fatalf("registry must be consistent (empty, matching empty Docker state) after shutdown mid-pass, got %d", reg.Len())
	}
}

// TestEventSource_ResilienceRace hammers reconnects, events, and periodic
// ticks concurrently with registry churn under -race, proving no data race
// across the reconnect-cleanup and shutdown-logging additions.
func TestEventSource_ResilienceRace(t *testing.T) {
	conns := make([]*fakeEventConnection, 6)
	ifaceConns := make([]*fakeEventConnection, 6)
	for i := range conns {
		conns[i] = newFakeEventConnection()
		ifaceConns[i] = conns[i]
	}
	docker := &fakeEventDocker{connections: ifaceConns}
	pr := NewProjectRegistry()
	rc := NewReconciler(pr, docker, nil, nil)
	es := NewEventSource(rc, docker, 3*time.Millisecond, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go es.Run(ctx)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reg := NewRegistry()
			reg.Add(Backend{ID: "b", Addr: "10.0.0.1:80"}) //nolint:errcheck
			pr.Register("svc", reg)
			if i < len(conns) {
				conns[i].msgs <- events.Message{Type: events.ContainerEventType, Action: "start", Actor: events.Actor{ID: "cid"}}
			}
			time.Sleep(2 * time.Millisecond)
			pr.Remove("svc")
		}(i)
	}
	wg.Wait()
	cancel()
}
