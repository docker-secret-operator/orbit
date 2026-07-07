package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withStateDir points ORBIT_STATE_DIR at a temp directory for the duration
// of the test, so history files never touch the real home directory.
func withStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ORBIT_STATE_DIR", dir)
	return dir
}

func TestAppendRequiresService(t *testing.T) {
	withStateDir(t)
	err := Append(Event{Type: EventRolloutStarted})
	if err == nil {
		t.Fatal("Append() with empty Service should error")
	}
}

func TestAppendRejectsPathTraversalServiceName(t *testing.T) {
	stateDir := withStateDir(t)

	err := Append(Event{Service: "../../../../etc/cron.d/evil", Type: EventRolloutStarted})
	if err == nil {
		t.Fatal("Append() with a path-traversal service name should error")
	}

	// Nothing should have been written outside the state dir at all.
	if _, statErr := os.Stat(filepath.Join(stateDir, "etc")); statErr == nil {
		t.Fatal("Append() must not write outside the history directory")
	}
}

func TestAppendRejectsServiceNameWithSlash(t *testing.T) {
	withStateDir(t)

	err := Append(Event{Service: "foo/bar", Type: EventRolloutStarted})
	if err == nil {
		t.Fatal("Append() with a slash in the service name should error")
	}
}

func TestReadRejectsPathTraversalServiceName(t *testing.T) {
	withStateDir(t)

	events, err := Read("../../../../etc/cron.d/evil", 0)
	if err == nil {
		t.Fatalf("Read() with a path-traversal service name should error, got events=%v", events)
	}
}

func TestReadRejectsServiceNameWithSlash(t *testing.T) {
	withStateDir(t)

	if _, err := Read("foo/bar", 0); err == nil {
		t.Fatal("Read() with a slash in the service name should error")
	}
}

func TestAppendAndReadRoundTrip(t *testing.T) {
	withStateDir(t)

	ev := Event{
		Service:       "web",
		Type:          EventRolloutCompleted,
		OldGeneration: "web-1",
		NewGeneration: "web-2",
		DurationMS:    1500,
		Result:        "success",
	}
	if err := Append(ev); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	got, err := Read("web", 0)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Read() returned %d events, want 1", len(got))
	}
	if got[0].Service != "web" || got[0].NewGeneration != "web-2" || got[0].Result != "success" {
		t.Errorf("Read()[0] = %+v, missing expected fields", got[0])
	}
	if got[0].Trigger != "cli" {
		t.Errorf("Trigger defaulted to %q, want %q", got[0].Trigger, "cli")
	}
}

func TestReadOnMissingLogReturnsEmptyNotError(t *testing.T) {
	withStateDir(t)

	got, err := Read("never-deployed", 0)
	if err != nil {
		t.Fatalf("Read() on missing log returned error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Read() on missing log returned %d events, want 0", len(got))
	}
}

func TestReadOrdersNewestFirst(t *testing.T) {
	withStateDir(t)

	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		ev := Event{
			Service:   "api",
			Type:      EventRolloutCompleted,
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Result:    "success",
		}
		if err := Append(ev); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Read("api", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if !got[0].Timestamp.After(got[1].Timestamp) || !got[1].Timestamp.After(got[2].Timestamp) {
		t.Errorf("events not ordered newest-first: %v, %v, %v", got[0].Timestamp, got[1].Timestamp, got[2].Timestamp)
	}
}

func TestReadRespectsLimit(t *testing.T) {
	withStateDir(t)

	for i := 0; i < 5; i++ {
		if err := Append(Event{Service: "api", Type: EventRolloutCompleted, Result: "success"}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Read("api", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("Read(limit=2) returned %d events, want 2", len(got))
	}
}

func TestServicesDoNotShareHistory(t *testing.T) {
	withStateDir(t)

	if err := Append(Event{Service: "web", Type: EventRolloutCompleted, Result: "success"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(Event{Service: "api", Type: EventRolloutCompleted, Result: "success"}); err != nil {
		t.Fatal(err)
	}

	webEvents, _ := Read("web", 0)
	apiEvents, _ := Read("api", 0)

	if len(webEvents) != 1 || len(apiEvents) != 1 {
		t.Fatalf("expected 1 event each, got web=%d api=%d", len(webEvents), len(apiEvents))
	}
}

func TestAppendSetsFilePermissions0600(t *testing.T) {
	dir := withStateDir(t)

	if err := Append(Event{Service: "web", Type: EventRolloutStarted}); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(filepath.Join(dir, "history", "history-web.jsonl"))
	if err != nil {
		t.Fatalf("stat history file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("history file permissions = %o, want 0600", perm)
	}
}

func TestCorruptLineIsSkippedNotFatal(t *testing.T) {
	dir := withStateDir(t)

	if err := Append(Event{Service: "web", Type: EventRolloutStarted}); err != nil {
		t.Fatal(err)
	}

	// Inject a corrupt line directly.
	f, err := os.OpenFile(filepath.Join(dir, "history", "history-web.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not valid json\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Append(Event{Service: "web", Type: EventRolloutCompleted, Result: "success"}); err != nil {
		t.Fatal(err)
	}

	got, err := Read("web", 0)
	if err != nil {
		t.Fatalf("Read() with a corrupt line returned error = %v, want nil (skip and continue)", err)
	}
	if len(got) != 2 {
		t.Errorf("Read() returned %d valid events, want 2 (corrupt line skipped)", len(got))
	}
}

func TestDirPrefersOrbitStateDir(t *testing.T) {
	t.Setenv("ORBIT_STATE_DIR", "/custom/state")
	t.Setenv("XDG_STATE_HOME", "/should/not/be/used")

	got := Dir()
	want := filepath.Join("/custom/state", "history")
	if got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
}
