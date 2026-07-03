package state

import (
	"os"
	"path/filepath"
	"testing"
)

// Phase 3.0 (Production Reliability) Security: persisted state files can carry
// sensitive data — RolloutState embeds an API token (api_token) and
// ActiveGenerationState records deployment topology. They must never be written
// group/world-readable. This regression guard pins the 0600 mode that
// AtomicWriteJSON is responsible for, including across the unique-temp-file
// rewrite done in this phase (a common atomic-write bug is a temp file created
// with default 0644 whose mode survives the rename).
func TestAtomicWriteJSONProduces0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	data := &ActiveGenerationState{SchemaVersion: 1, Service: "web", ActiveGeneration: "gen-1"}
	if err := AtomicWriteJSON(path, data, nil); err != nil {
		t.Fatalf("write: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("state file permissions = %o, want 0600 (state files can carry an API token)", perm)
	}
}

// TestAtomicWriteJSONOverwritePreserves0600 verifies the mode is 0600 even when
// overwriting an existing file (the rename path), not only on first create.
func TestAtomicWriteJSONOverwritePreserves0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	first := &ActiveGenerationState{SchemaVersion: 1, Service: "web", ActiveGeneration: "gen-1", Revision: 1}
	if err := AtomicWriteJSON(path, first, nil); err != nil {
		t.Fatalf("first write: %v", err)
	}
	second := &ActiveGenerationState{SchemaVersion: 1, Service: "web", ActiveGeneration: "gen-2", Revision: 2}
	if err := AtomicWriteJSON(path, second, nil); err != nil {
		t.Fatalf("second write: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("overwritten state file permissions = %o, want 0600", perm)
	}
}
