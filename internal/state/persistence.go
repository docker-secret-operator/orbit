package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// ============================================================================
// Advisory Locking (Cross-Process Safety)
// ============================================================================

// AdvisoryLock represents a lock held via flock(2)/fcntl(2).
// Automatically released on process exit by the OS.
type AdvisoryLock struct {
	file *os.File
	path string
}

// AcquireAdvisoryLock acquires an exclusive advisory lock with timeout.
// Uses flock(2) for POSIX systems (Linux, macOS, BSD, Solaris).
func AcquireAdvisoryLock(lockPath string, timeout time.Duration) (*AdvisoryLock, error) {
	// Create/open lock file
	f, err := os.OpenFile(lockPath,
		os.O_CREATE|os.O_WRONLY,
		0644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)

	for {
		// Try non-blocking lock (LOCK_EX | LOCK_NB)
		// Returns EWOULDBLOCK if already locked
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)

		if err == nil {
			// Lock acquired successfully
			return &AdvisoryLock{
				file: f,
				path: lockPath,
			}, nil
		}

		// Check if real error (not just "already locked")
		if err != unix.EWOULDBLOCK {
			f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}

		// Lock is held by another process: check timeout
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("lock acquisition timeout")
		}

		// Sleep briefly and retry
		// OS will wake us when lock becomes available
		time.Sleep(10 * time.Millisecond)
	}
}

// AcquireAdvisoryReadLock acquires a shared advisory lock with timeout.
// Multiple readers can hold shared lock simultaneously.
func AcquireAdvisoryReadLock(lockPath string, timeout time.Duration) (*AdvisoryLock, error) {
	f, err := os.OpenFile(lockPath,
		os.O_CREATE|os.O_RDONLY,
		0644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)

	for {
		// Try shared lock (LOCK_SH | LOCK_NB)
		err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB)

		if err == nil {
			return &AdvisoryLock{
				file: f,
				path: lockPath,
			}, nil
		}

		if err != unix.EWOULDBLOCK {
			f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}

		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("lock acquisition timeout")
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// Release unlocks and closes the lock file.
func (l *AdvisoryLock) Release() error {
	if l.file == nil {
		return nil
	}

	// Explicit unlock (automatic on close, but explicit is clearer)
	unix.Flock(int(l.file.Fd()), unix.LOCK_UN) //nolint:errcheck

	err := l.file.Close()
	l.file = nil
	return err
}

// ============================================================================
// Atomic JSON Persistence (Crash-Safe)
// ============================================================================

// AtomicWriteJSON writes data to file atomically with fsync.
// Guarantees: partial/corrupted files never visible to readers on success.
//
// Algorithm:
// 1. Marshal to JSON
// 2. Write to temp file in same directory
// 3. fsync(temp file) — persist to disk
// 4. Atomic rename(temp, target) — all-or-nothing
// 5. fsync(parent directory) — durability across power loss
func AtomicWriteJSON(path string, data interface{}, log interface{}) error {
	// Step 1: Marshal to JSON
	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}

	// Clean up any stale temp file left by a previously-interrupted write to
	// this same path (crash recovery). This is the legacy, deterministic temp
	// name; it is safe to remove because no live writer uses it anymore — each
	// concurrent writer now creates its own unique temp (below), so this remove
	// can never race a writer that is mid-flight. Best-effort: absence is fine.
	_ = os.Remove(path + ".tmp")

	// Step 2: Write to a UNIQUE temp file in the same directory.
	// A per-writer unique name (not a fixed path+".tmp") is required for
	// correctness under concurrent writers to the same path: with a shared temp
	// name, one writer's rename removes the temp file another writer is about to
	// open/rename, producing a spurious "no such file" error. os.CreateTemp
	// gives each writer its own temp in the same directory (same filesystem, so
	// the final rename stays atomic — all-or-nothing, last writer wins, never a
	// torn file). Production callers already serialize per-service via advisory
	// + in-process locks; this makes the primitive itself safe regardless.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpFile := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpFile)
		}
	}()

	if _, err := tmp.Write(bytes); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	// os.CreateTemp creates with 0600, but set it explicitly: state files can
	// carry an API token and must never be group/world readable.
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}

	// Step 3: fsync temp file (persist contents to disk before the rename)
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Step 4: Atomic rename (POSIX: all-or-nothing)
	if err := os.Rename(tmpFile, path); err != nil {
		return fmt.Errorf("atomic rename: %w", err)
	}
	committed = true

	// Step 5: fsync parent directory (ensures rename durability on power loss)
	// This is required for POSIX systems to survive unclean shutdown
	parentDir := filepath.Dir(path)
	parent, err := os.Open(parentDir)
	if err != nil {
		// Log warning but don't fail
		// (some filesystems/mounts may not support directory fsync)
		if log != nil {
			fmt.Fprintf(os.Stderr, "warning: could not open parent directory for fsync: %v\n", err)
		}
		return nil
	}
	defer parent.Close()

	if err := parent.Sync(); err != nil {
		// Log warning but don't fail
		if log != nil {
			fmt.Fprintf(os.Stderr, "warning: parent directory fsync failed: %v\n", err)
		}
	}

	return nil
}

// ============================================================================
// State File Loading (Crash-Safe + Corruption Detection)
// ============================================================================

// LoadStateFile loads and validates a state file.
// Returns nil if file doesn't exist (expected, not an error).
// Returns error if file exists but corrupted (fatal).
func LoadStateFile(path string) ([]byte, error) {
	// Check if file exists
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil // File doesn't exist: OK
	}
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}

	// Sanity check: file too large (corruption indicator)
	const maxStateFileSize = 1 * 1024 * 1024 // 1MB
	if info.Size() > maxStateFileSize {
		return nil, fmt.Errorf("file too large: %d bytes", info.Size())
	}

	// Read file
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	return bytes, nil
}

// ValidateStateJSON validates that bytes can be unmarshaled as state.
// On corruption, moves file to .corrupted for investigation.
func ValidateStateJSON(path string, bytes []byte) error {
	// Try parse as generic JSON to check validity
	var data interface{}
	if err := json.Unmarshal(bytes, &data); err != nil {
		// Corrupted state detected
		// Move aside for investigation
		corruptedPath := path + ".corrupted"
		if moveErr := os.Rename(path, corruptedPath); moveErr == nil {
			fmt.Fprintf(os.Stderr, "FATAL: state file corrupted, moved to %s\n", corruptedPath)
		}
		return fmt.Errorf("JSON decode failed: %w", err)
	}

	// Check schema version
	var schemaWrapper struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(bytes, &schemaWrapper); err != nil {
		return fmt.Errorf("schema version check failed: %w", err)
	}

	if schemaWrapper.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema version: %d (expected %d)",
			schemaWrapper.SchemaVersion, SchemaVersion)
	}

	return nil
}

// ============================================================================
// State Manager Read/Write Operations
// ============================================================================

// LoadActiveGenerationState loads active generation state with locking.
// Returns nil if state doesn't exist (no state file yet).
// Returns error only on corruption or real I/O failure.
func (sm *StateManager) LoadActiveGenerationState(service string) (*ActiveGenerationState, error) {
	lockPath := sm.StateLockPath(service)

	// Acquire shared read lock (multiple readers OK)
	lock, err := AcquireAdvisoryReadLock(lockPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("cannot acquire read lock: %w", err)
	}
	defer lock.Release()

	// Also use in-process RWMutex for same-process safety
	inProcessLock := sm.getInProcessLock(service)
	inProcessLock.RLock()
	defer inProcessLock.RUnlock()

	// Load and validate file
	stateFile := sm.ActiveGenerationPath(service)
	bytes, err := LoadStateFile(stateFile)
	if err != nil {
		return nil, fmt.Errorf("load file: %w", err)
	}

	if bytes == nil {
		return nil, nil // No state file
	}

	// Validate JSON
	if err := ValidateStateJSON(stateFile, bytes); err != nil {
		return nil, &StateLoadError{
			Path:    stateFile,
			Reason:  fmt.Sprintf("JSON validation failed: %v", err),
			IsFatal: true,
		}
	}

	// Unmarshal
	var state ActiveGenerationState
	if err := json.Unmarshal(bytes, &state); err != nil {
		return nil, &StateLoadError{
			Path:    stateFile,
			Reason:  fmt.Sprintf("unmarshal failed: %v", err),
			IsFatal: true,
		}
	}

	return &state, nil
}

// WriteActiveGenerationState writes active generation state with locking.
// Uses CAS (Compare-And-Swap) semantics via revision numbers.
func (sm *StateManager) WriteActiveGenerationState(state *ActiveGenerationState, log interface{}) error {
	lockPath := sm.StateLockPath(state.Service)

	// Acquire exclusive write lock
	lock, err := AcquireAdvisoryLock(lockPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("cannot acquire write lock: %w", err)
	}
	defer lock.Release()

	// Also use in-process RWMutex
	inProcessLock := sm.getInProcessLock(state.Service)
	inProcessLock.Lock()
	defer inProcessLock.Unlock()

	// CAS: Verify revision matches expected
	current, err := sm.loadCurrentActiveGenerationUnsafe(state.Service)
	if err == nil && current != nil {
		// Current state exists: verify CAS
		if state.PreviousRevision != current.Revision {
			return fmt.Errorf("revision conflict: expected %d, found %d (write skipped)",
				state.PreviousRevision, current.Revision)
		}
	}

	// Safe to write: increment revision
	state.Revision = time.Now().Unix() // Use timestamp as monotonic counter
	if current != nil {
		state.PreviousRevision = current.Revision
	}
	state.UpdatedAt = time.Now()

	// Atomic write to disk
	stateFile := sm.ActiveGenerationPath(state.Service)
	return AtomicWriteJSON(stateFile, state, log)
}

// LoadRolloutState loads rollout state with locking.
func (sm *StateManager) LoadRolloutState(service string) (*RolloutState, error) {
	lockPath := sm.StateLockPath(service)

	lock, err := AcquireAdvisoryReadLock(lockPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("cannot acquire read lock: %w", err)
	}
	defer lock.Release()

	inProcessLock := sm.getInProcessLock(service)
	inProcessLock.RLock()
	defer inProcessLock.RUnlock()

	stateFile := sm.rolloutStatePath(service)
	bytes, err := LoadStateFile(stateFile)
	if err != nil {
		return nil, fmt.Errorf("load file: %w", err)
	}

	if bytes == nil {
		return nil, nil
	}

	if err := ValidateStateJSON(stateFile, bytes); err != nil {
		return nil, &StateLoadError{
			Path:    stateFile,
			Reason:  fmt.Sprintf("JSON validation failed: %v", err),
			IsFatal: true,
		}
	}

	var state RolloutState
	if err := json.Unmarshal(bytes, &state); err != nil {
		return nil, &StateLoadError{
			Path:    stateFile,
			Reason:  fmt.Sprintf("unmarshal failed: %v", err),
			IsFatal: true,
		}
	}

	return &state, nil
}

// WriteRolloutState writes rollout state with locking.
func (sm *StateManager) WriteRolloutState(state *RolloutState, log interface{}) error {
	lockPath := sm.StateLockPath(state.Service)

	lock, err := AcquireAdvisoryLock(lockPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("cannot acquire write lock: %w", err)
	}
	defer lock.Release()

	inProcessLock := sm.getInProcessLock(state.Service)
	inProcessLock.Lock()
	defer inProcessLock.Unlock()

	stateFile := sm.rolloutStatePath(state.Service)
	return AtomicWriteJSON(stateFile, state, log)
}

// DeleteRolloutState deletes rollout state (on completion).
func (sm *StateManager) DeleteRolloutState(service string) error {
	lockPath := sm.StateLockPath(service)

	lock, err := AcquireAdvisoryLock(lockPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("cannot acquire write lock: %w", err)
	}
	defer lock.Release()

	inProcessLock := sm.getInProcessLock(service)
	inProcessLock.Lock()
	defer inProcessLock.Unlock()

	stateFile := sm.rolloutStatePath(service)
	if err := os.Remove(stateFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete: %w", err)
	}

	return nil
}

// ============================================================================
// Unsafe Internal Operations (Within Lock Only)
// ============================================================================

// loadCurrentActiveGenerationUnsafe loads state WITHOUT acquiring locks.
// MUST ONLY be called while holding exclusive write lock.
func (sm *StateManager) loadCurrentActiveGenerationUnsafe(service string) (*ActiveGenerationState, error) {
	stateFile := sm.ActiveGenerationPath(service)
	bytes, err := LoadStateFile(stateFile)
	if err != nil {
		return nil, err
	}

	if bytes == nil {
		return nil, nil
	}

	var state ActiveGenerationState
	if err := json.Unmarshal(bytes, &state); err != nil {
		return nil, err
	}

	return &state, nil
}
