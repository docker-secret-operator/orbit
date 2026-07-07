// Package history records real deployment events (rollouts, rollbacks) as
// they happen, so `docker orbit history` has something genuine to read.
//
// This is new, additive persistence — the existing engine (internal/state,
// internal/rollout) tracks only *current* generation/rollout state, not a
// timeline of past events. Recording events as they occur, without changing
// how rollout/rollback/recovery decisions are made, is instrumentation, not
// a redesign of the deployment engine: see ADR-0003, which this package
// does not modify or extend the decision logic of.
//
// History starts accumulating from when this package first runs — there is
// no retroactive record of deployments that happened before it existed.
// `docker orbit history` says so explicitly on an empty log rather than
// implying data loss.
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// EventType enumerates the real event kinds this package's callers
// currently produce. Do not add a value here without a corresponding
// Append call from real code — an EventType nothing ever records is exactly
// the kind of placeholder this package exists to avoid.
type EventType string

const (
	EventRolloutStarted   EventType = "rollout_started"
	EventRolloutCompleted EventType = "rollout_completed"
	EventRolloutFailed    EventType = "rollout_failed"
	EventRollback         EventType = "rollback"
)

// Event is one line in a service's history log.
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Service   string    `json:"service"`
	Type      EventType `json:"type"`

	// OldGeneration/NewGeneration are populated when known — rollback events
	// may only know the generation being restored to.
	OldGeneration string `json:"old_generation,omitempty"`
	NewGeneration string `json:"new_generation,omitempty"`

	// DurationMS is set on completed/failed events, computed by the caller
	// from its own start time. Zero on "started" events.
	DurationMS int64 `json:"duration_ms,omitempty"`

	// Trigger records what initiated this event. Today the only real value
	// is "cli" — every rollout/rollback currently goes through the CLI.
	// Do not add other values until something real produces them.
	Trigger string `json:"trigger"`

	// Result is "success" or "failure". Empty on "started" events, which
	// have no result yet.
	Result string `json:"result,omitempty"`

	// Reason is a short human explanation, populated on failure.
	Reason string `json:"reason,omitempty"`
}

// Dir returns the directory history logs are written to:
// $ORBIT_STATE_DIR/history (if set, so it colocates with an explicit
// operator choice), else $XDG_STATE_HOME/orbit, else ~/.local/share/orbit,
// else a /tmp fallback if no home directory is resolvable (e.g. some CI
// environments). This mirrors the precedent BRAND.md sets for host-side CLI
// state: XDG conventions apply here, unlike the proxy's container-internal
// /var/lib/orbit path (see BRAND.md Phase C).
func Dir() string {
	if d := os.Getenv("ORBIT_STATE_DIR"); d != "" {
		return filepath.Join(d, "history")
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "orbit")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "orbit")
	}
	return filepath.Join(os.TempDir(), "orbit-history")
}

// validServiceName matches Compose-style service names: letters, digits,
// dots, dashes, and underscores. No "/" or "\" means service can never
// introduce a path segment, and no "." (checked separately for exactly "."
// or "..") means it can never resolve to a directory traversal.
var validServiceName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateServiceName rejects any service name that isn't safe to
// interpolate directly into a file path — in particular names containing
// path separators or "." / ".." segments, which would otherwise let a
// crafted compose service name read or write files outside the history
// directory.
func validateServiceName(service string) error {
	if service == "" {
		return fmt.Errorf("history: service name is required")
	}
	if service == "." || service == ".." {
		return fmt.Errorf("history: invalid service name %q", service)
	}
	if !validServiceName.MatchString(service) {
		return fmt.Errorf("history: invalid service name %q", service)
	}
	return nil
}

// path returns the JSONL file path for service's history.
func path(service string) string {
	return filepath.Join(Dir(), "history-"+service+".jsonl")
}

// Append records ev to service's history log, creating the directory and
// file as needed. File permissions are 0600 — same convention as
// internal/state's persisted files (CONSTITUTION.md: "state files 0600").
// Append is best-effort by design at call sites (a history-write failure
// should never fail a rollout) but returns the error so callers can log it.
func Append(ev Event) error {
	if err := validateServiceName(ev.Service); err != nil {
		return err
	}
	if ev.Trigger == "" {
		ev.Trigger = "cli"
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}

	dir := Dir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("history: create dir %s: %w", dir, err)
	}

	f, err := os.OpenFile(path(ev.Service), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("history: open %s: %w", path(ev.Service), err)
	}
	defer f.Close() //nolint:errcheck // best-effort: history recording never fails a rollout (see Append's doc comment)

	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("history: encode event: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("history: write event: %w", err)
	}
	return nil
}

// Read returns up to limit most-recent events for service, newest first.
// limit <= 0 means "all events". Returns an empty (non-nil) slice, not an
// error, if the log doesn't exist yet — an empty history is a normal state,
// not a failure.
func Read(service string, limit int) ([]Event, error) {
	if err := validateServiceName(service); err != nil {
		return nil, err
	}

	f, err := os.Open(path(service))
	if os.IsNotExist(err) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("history: open %s: %w", path(service), err)
	}
	defer f.Close() //nolint:errcheck // read-only file, nothing meaningful to do with a close error here

	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			// A single corrupt line shouldn't hide the rest of the history —
			// skip it rather than failing the whole read.
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("history: read %s: %w", path(service), err)
	}

	// Newest first.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	if events == nil {
		events = []Event{}
	}
	return events, nil
}
