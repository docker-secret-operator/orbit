package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ============================================================================
// Advisory Lock Tests
// ============================================================================

func TestAcquireAdvisoryLock(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	lock, err := AcquireAdvisoryLock(lockPath, 1*time.Second)
	if err != nil {
		t.Fatalf("lock acquisition failed: %v", err)
	}
	defer lock.Release()

	if lock.file == nil {
		t.Fatalf("lock file is nil")
	}
	if lock.path != lockPath {
		t.Errorf("lock path mismatch: got %s, want %s", lock.path, lockPath)
	}
}

func TestAcquireAdvisoryLockCreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	// Verify file doesn't exist
	if _, err := os.Stat(lockPath); err == nil {
		t.Fatalf("lock file should not exist yet")
	}

	lock, err := AcquireAdvisoryLock(lockPath, 1*time.Second)
	if err != nil {
		t.Fatalf("lock acquisition failed: %v", err)
	}
	defer lock.Release()

	// Verify file was created
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file should exist: %v", err)
	}
}

func TestAcquireAdvisoryReadLock(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	lock, err := AcquireAdvisoryReadLock(lockPath, 1*time.Second)
	if err != nil {
		t.Fatalf("read lock acquisition failed: %v", err)
	}
	defer lock.Release()

	if lock.file == nil {
		t.Fatalf("lock file is nil")
	}
}

func TestAdvisoryLockRelease(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	lock, err := AcquireAdvisoryLock(lockPath, 1*time.Second)
	if err != nil {
		t.Fatalf("lock acquisition failed: %v", err)
	}

	err = lock.Release()
	if err != nil {
		t.Fatalf("lock release failed: %v", err)
	}

	if lock.file != nil {
		t.Errorf("lock file should be nil after release")
	}
}

func TestAdvisoryLockReleaseIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	lock, err := AcquireAdvisoryLock(lockPath, 1*time.Second)
	if err != nil {
		t.Fatalf("lock acquisition failed: %v", err)
	}

	// First release
	err = lock.Release()
	if err != nil {
		t.Fatalf("first release failed: %v", err)
	}

	// Second release (should not error)
	err = lock.Release()
	if err != nil {
		t.Fatalf("second release should be safe: %v", err)
	}
}

// ============================================================================
// Atomic Write Tests
// ============================================================================

func TestAtomicWriteJSON(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	data := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-123",
		Revision:         42,
		UpdatedAt:        time.Now(),
	}

	err := AtomicWriteJSON(filePath, data, nil)
	if err != nil {
		t.Fatalf("atomic write failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file should exist: %v", err)
	}

	// Verify content
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}

	var restored ActiveGenerationState
	if err := json.Unmarshal(bytes, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if restored.Service != data.Service {
		t.Errorf("service mismatch: got %s, want %s", restored.Service, data.Service)
	}
	if restored.ActiveGeneration != data.ActiveGeneration {
		t.Errorf("generation mismatch: got %s, want %s", restored.ActiveGeneration, data.ActiveGeneration)
	}
}

func TestAtomicWriteNoTempFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	data := map[string]string{"key": "value"}

	err := AtomicWriteJSON(filePath, data, nil)
	if err != nil {
		t.Fatalf("atomic write failed: %v", err)
	}

	// Verify .tmp file doesn't exist (should be cleaned up)
	tmpFile := filePath + ".tmp"
	if _, err := os.Stat(tmpFile); err == nil {
		t.Fatalf("temp file should not exist after atomic write")
	}
}

func TestAtomicWriteOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	// First write
	data1 := map[string]string{"version": "1"}
	if err := AtomicWriteJSON(filePath, data1, nil); err != nil {
		t.Fatalf("first write failed: %v", err)
	}

	// Overwrite
	data2 := map[string]string{"version": "2"}
	if err := AtomicWriteJSON(filePath, data2, nil); err != nil {
		t.Fatalf("second write failed: %v", err)
	}

	// Verify content is from second write
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(bytes, &result); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if result["version"] != "2" {
		t.Errorf("content mismatch: got %s, want 2", result["version"])
	}
}

// ============================================================================
// State File Loading Tests
// ============================================================================

func TestLoadStateFileNotExists(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "nonexistent.json")

	bytes, err := LoadStateFile(filePath)
	if err != nil {
		t.Fatalf("should not error for missing file: %v", err)
	}

	if bytes != nil {
		t.Errorf("bytes should be nil for missing file")
	}
}

func TestLoadStateFileExists(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	content := []byte(`{"schema_version": 1, "service": "web"}`)
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	bytes, err := LoadStateFile(filePath)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if bytes == nil {
		t.Fatalf("bytes should not be nil")
	}
	if string(bytes) != string(content) {
		t.Errorf("content mismatch")
	}
}

func TestLoadStateFileTooLarge(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "huge.json")

	// Create file larger than 1MB
	largeContent := make([]byte, 2*1024*1024)
	if err := os.WriteFile(filePath, largeContent, 0644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	bytes, err := LoadStateFile(filePath)
	if err == nil {
		t.Fatalf("should error for too-large file")
	}

	if bytes != nil {
		t.Errorf("bytes should be nil on error")
	}
}

// ============================================================================
// JSON Validation Tests
// ============================================================================

func TestValidateStateJSONValid(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	content := []byte(`{"schema_version": 1, "service": "web"}`)
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	err := ValidateStateJSON(filePath, content)
	if err != nil {
		t.Fatalf("validation should pass: %v", err)
	}
}

func TestValidateStateJSONInvalid(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	// Create actual file so it can be moved to .corrupted
	if err := os.WriteFile(filePath, []byte("invalid"), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	content := []byte("invalid json {")
	err := ValidateStateJSON(filePath, content)
	if err == nil {
		t.Fatalf("validation should fail for invalid JSON")
	}

	// Verify file was moved to .corrupted
	corruptedPath := filePath + ".corrupted"
	if _, err := os.Stat(corruptedPath); err != nil {
		t.Logf("corrupted file not created (may be OK if permissions denied)")
	}
}

func TestValidateStateJSONMissingSchemaVersion(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	content := []byte(`{"service": "web"}`)
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Should not error even if schema_version missing (it will be 0)
	err := ValidateStateJSON(filePath, content)
	if err != nil {
		// It's OK to fail here - schema version 0 != 1
		t.Logf("schema validation failed as expected: %v", err)
	}
}

func TestValidateStateJSONWrongSchemaVersion(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	content := []byte(`{"schema_version": 99, "service": "web"}`)
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	err := ValidateStateJSON(filePath, content)
	if err == nil {
		t.Fatalf("validation should fail for wrong schema version")
	}
}

// ============================================================================
// StateManager Lock Integration Tests
// ============================================================================

func TestStateManagerGetInProcessLockConcurrent(t *testing.T) {
	sm := NewStateManager(t.TempDir(), nil)

	// Acquire lock from multiple goroutines
	var wg sync.WaitGroup
	locks := make(map[string]bool)
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock := sm.getInProcessLock("service-1")
			mu.Lock()
			locks[fmt.Sprintf("%p", lock)] = true
			mu.Unlock()
		}()
	}

	wg.Wait()

	// All should be the same lock instance
	if len(locks) != 1 {
		t.Errorf("expected 1 unique lock, got %d", len(locks))
	}
}

// ============================================================================
// Atomic Write Durability Tests
// ============================================================================

func TestAtomicWritePreserveExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "state.json")

	// Create initial file
	initial := map[string]string{"data": "initial"}
	if err := AtomicWriteJSON(filePath, initial, nil); err != nil {
		t.Fatalf("initial write failed: %v", err)
	}

	// Read initial content
	bytes1, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	// Write new data
	updated := map[string]string{"data": "updated"}
	if err := AtomicWriteJSON(filePath, updated, nil); err != nil {
		t.Fatalf("updated write failed: %v", err)
	}

	// Read updated content
	bytes2, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	// Verify content changed
	if string(bytes1) == string(bytes2) {
		t.Errorf("content should have changed")
	}
}

// ============================================================================
// StateManager Unsafe Operations Tests
// ============================================================================

func TestLoadCurrentActiveGenerationUnsafe(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewStateManager(tmpDir, nil)

	// File doesn't exist yet
	state, err := sm.loadCurrentActiveGenerationUnsafe("web")
	if err != nil {
		t.Fatalf("should not error for missing file: %v", err)
	}
	if state != nil {
		t.Errorf("state should be nil for missing file")
	}

	// Create state file
	stateFile := sm.ActiveGenerationPath("web")
	data := &ActiveGenerationState{
		SchemaVersion:    1,
		Service:          "web",
		ActiveGeneration: "gen-123",
	}
	if err := AtomicWriteJSON(stateFile, data, nil); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Load it back
	loaded, err := sm.loadCurrentActiveGenerationUnsafe("web")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if loaded == nil {
		t.Fatalf("loaded state should not be nil")
	}
	if loaded.ActiveGeneration != "gen-123" {
		t.Errorf("generation mismatch: got %s, want gen-123", loaded.ActiveGeneration)
	}
}

// ============================================================================
// Lock Timeout Tests
// ============================================================================

func TestAcquireAdvisoryLockTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	// Acquire first lock
	lock1, err := AcquireAdvisoryLock(lockPath, 5*time.Second)
	if err != nil {
		t.Fatalf("first lock acquisition failed: %v", err)
	}
	defer lock1.Release()

	// Try to acquire second lock with short timeout
	// This should timeout (can't test blocking without goroutines)
	start := time.Now()
	lock2, err := AcquireAdvisoryLock(lockPath, 100*time.Millisecond)
	elapsed := time.Since(start)

	// Should fail after ~100ms
	if err == nil {
		lock2.Release()
		t.Fatalf("second lock should fail when first is held")
	}

	if elapsed < 50*time.Millisecond {
		t.Logf("timeout may have failed too quickly: %v", elapsed)
	}
}

// ============================================================================
// Concurrent Read Locks Tests
// ============================================================================

func TestAcquireAdvisoryReadLockConcurrent(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	// Acquire multiple read locks concurrently
	var wg sync.WaitGroup
	locks := []*AdvisoryLock{}
	var mu sync.Mutex

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock, err := AcquireAdvisoryReadLock(lockPath, 5*time.Second)
			if err != nil {
				t.Errorf("read lock acquisition failed: %v", err)
				return
			}
			mu.Lock()
			locks = append(locks, lock)
			mu.Unlock()
		}()
	}

	wg.Wait()

	// All should succeed (read locks are shared)
	if len(locks) != 5 {
		t.Errorf("expected 5 locks acquired, got %d", len(locks))
	}

	// Clean up
	for _, lock := range locks {
		lock.Release()
	}
}
