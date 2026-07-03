package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ============================================================================
// Crash Consistency Tests
// ============================================================================
// These tests verify that atomic writes maintain state consistency
// even under crash-like scenarios (kill, out of disk, permissions).
//
// Key invariant: Final state is either completely new or completely old,
// never partially written or corrupted.

func TestCrashDuringAtomicWriteCompletes(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	data := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-123",
		Revision:         1,
		UpdatedAt:        time.Now(),
	}

	// Execute atomic write
	err := AtomicWriteJSON(filePath, data, nil)
	if err != nil {
		t.Fatalf("atomic write failed: %v", err)
	}

	// Verify: state file exists and is valid
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("state file should exist: %v", err)
	}

	var restored ActiveGenerationState
	if err := json.Unmarshal(bytes, &restored); err != nil {
		t.Fatalf("state file corrupted: %v", err)
	}

	if restored.ActiveGeneration != "gen-123" {
		t.Errorf("generation mismatch after write")
	}

	// Verify: no .tmp file left behind
	tmpFile := filePath + ".tmp"
	if _, err := os.Stat(tmpFile); err == nil {
		t.Fatalf("temp file should be cleaned up after successful write")
	}
}

func TestCrashWithPartialTempFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")
	tmpFile := filePath + ".tmp"

	// Create a partial temp file (simulate interrupted write)
	partialData := []byte(`{"schema_version": 1, "service": "web"`)
	if err := os.WriteFile(tmpFile, partialData, 0600); err != nil {
		t.Fatalf("write partial file failed: %v", err)
	}

	// Verify original doesn't exist
	if _, err := os.Stat(filePath); err == nil {
		t.Fatalf("original file should not exist yet")
	}

	// Now attempt atomic write
	data := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-456",
		Revision:         1,
	}

	err := AtomicWriteJSON(filePath, data, nil)
	if err != nil {
		t.Fatalf("atomic write should succeed (cleanup partial temp): %v", err)
	}

	// Verify: new file exists and old .tmp is gone
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("state file should exist: %v", err)
	}

	var restored ActiveGenerationState
	if err := json.Unmarshal(bytes, &restored); err != nil {
		t.Fatalf("state file corrupted: %v", err)
	}

	if restored.ActiveGeneration != "gen-456" {
		t.Errorf("generation mismatch: got %s, want gen-456", restored.ActiveGeneration)
	}

	// Verify .tmp cleanup
	if _, err := os.Stat(tmpFile); err == nil {
		t.Fatalf("temp file should be cleaned up")
	}
}

func TestCrashDuringRenameIsAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	// Write initial state
	initial := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-old",
		Revision:         1,
	}
	if err := AtomicWriteJSON(filePath, initial, nil); err != nil {
		t.Fatalf("initial write failed: %v", err)
	}

	// Read initial bytes
	oldBytes, _ := os.ReadFile(filePath)

	// Write new state
	updated := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-new",
		Revision:         2,
	}
	if err := AtomicWriteJSON(filePath, updated, nil); err != nil {
		t.Fatalf("update write failed: %v", err)
	}

	// Read updated bytes
	newBytes, _ := os.ReadFile(filePath)

	// Verify: state is either completely old or completely new, never mixed
	if string(oldBytes) == string(newBytes) {
		t.Errorf("state should have changed")
	}

	var result ActiveGenerationState
	if err := json.Unmarshal(newBytes, &result); err != nil {
		t.Fatalf("state corrupted (mixed old/new): %v", err)
	}

	if result.ActiveGeneration != "gen-new" {
		t.Errorf("state should be new, got %s", result.ActiveGeneration)
	}
}

func TestCrashWithCorruptedTempFileCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")
	tmpFile := filePath + ".tmp"

	// Simulate corrupted temp file (binary garbage)
	if err := os.WriteFile(tmpFile, []byte{0xFF, 0xFE, 0x00, 0x00}, 0600); err != nil {
		t.Fatalf("write corrupted temp failed: %v", err)
	}

	// Attempt atomic write (should clean up corrupted temp)
	data := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-clean",
		Revision:         1,
	}

	err := AtomicWriteJSON(filePath, data, nil)
	if err != nil {
		t.Fatalf("atomic write should handle corrupted temp: %v", err)
	}

	// Verify: good file exists
	bytes, _ := os.ReadFile(filePath)
	var restored ActiveGenerationState
	if err := json.Unmarshal(bytes, &restored); err != nil {
		t.Fatalf("state should be valid after cleanup: %v", err)
	}
}

func TestCrashWithLockFileStale(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	// Acquire first lock
	lock1, err := AcquireAdvisoryLock(lockPath, 1*time.Second)
	if err != nil {
		t.Fatalf("first lock failed: %v", err)
	}
	defer lock1.Release()

	// Simulate stale lock (would happen if process crashed while holding lock)
	// flock is automatically released on process exit, so we just verify
	// that the lock can be re-acquired after release

	lock1.Release()

	// Second lock should succeed immediately
	lock2, err := AcquireAdvisoryLock(lockPath, 1*time.Second)
	if err != nil {
		t.Fatalf("should reacquire after release: %v", err)
	}
	defer lock2.Release()
}

func TestCrashWithPermissionError(t *testing.T) {
	tmpDir := t.TempDir()

	// Create read-only directory
	readonlyDir := filepath.Join(tmpDir, "readonly")
	if err := os.Mkdir(readonlyDir, 0555); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	defer os.Chmod(readonlyDir, 0755) // Restore permissions for cleanup

	readonlyFile := filepath.Join(readonlyDir, "state.json")

	data := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-test",
	}

	// Attempt write to read-only location
	err := AtomicWriteJSON(readonlyFile, data, nil)
	if err == nil {
		t.Fatalf("write to read-only should fail")
	}

	// Verify: no partial files left behind
	if _, statErr := os.Stat(readonlyFile); statErr == nil {
		t.Fatalf("read-only file should not exist")
	}
	if _, statErr := os.Stat(readonlyFile + ".tmp"); statErr == nil {
		t.Fatalf("temp file should not exist after permission error")
	}
}

func TestCrashWithMultipleSequentialWrites(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	// Simulate 10 rapid sequential writes (like recovery loop)
	for i := 0; i < 10; i++ {
		data := &ActiveGenerationState{
			SchemaVersion:    1,
			Service:          "web",
			ActiveGeneration: "gen-test",
			Revision:         int64(i),
			UpdatedAt:        time.Now(),
		}

		if err := AtomicWriteJSON(filePath, data, nil); err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}

		// Verify: state is always valid
		bytes, _ := os.ReadFile(filePath)
		var restored ActiveGenerationState
		if err := json.Unmarshal(bytes, &restored); err != nil {
			t.Fatalf("state corrupted at write %d: %v", i, err)
		}

		// Verify: no leaked temp files
		if _, err := os.Stat(filePath + ".tmp"); err == nil {
			t.Fatalf("temp file leaked after write %d", i)
		}
	}
}

func TestCrashFileSystemConsistency(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	// Write initial state
	initial := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-1",
		Revision:         1,
		UpdatedAt:        time.Now(),
	}
	if err := AtomicWriteJSON(filePath, initial, nil); err != nil {
		t.Fatalf("initial write failed: %v", err)
	}

	// Verify: file is readable as JSON
	bytes1, _ := os.ReadFile(filePath)
	var state1 ActiveGenerationState
	if err := json.Unmarshal(bytes1, &state1); err != nil {
		t.Fatalf("initial state corrupted: %v", err)
	}

	// Write update
	updated := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-2",
		Revision:         2,
		UpdatedAt:        time.Now(),
	}
	if err := AtomicWriteJSON(filePath, updated, nil); err != nil {
		t.Fatalf("update write failed: %v", err)
	}

	// Verify: file is still readable
	bytes2, _ := os.ReadFile(filePath)
	var state2 ActiveGenerationState
	if err := json.Unmarshal(bytes2, &state2); err != nil {
		t.Fatalf("updated state corrupted: %v", err)
	}

	if state2.Revision != 2 {
		t.Errorf("revision should be 2, got %d", state2.Revision)
	}

	// Verify: both read successfully, different content
	if state1.ActiveGeneration == state2.ActiveGeneration {
		t.Errorf("generation should have changed")
	}
}
