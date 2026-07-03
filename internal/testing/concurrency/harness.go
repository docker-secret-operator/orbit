package concurrency

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/state"
)

// WriterID identifies which process is writing
type WriterID string

const (
	WriterProxy    WriterID = "proxy"
	WriterRollout  WriterID = "rollout"
	WriterPrune    WriterID = "prune"
	WriterRecovery WriterID = "recovery"
	WriterHealing  WriterID = "healing"
)

// WriteAttempt records one attempt to write state
type WriteAttempt struct {
	WriterID      WriterID
	StartTime     time.Time
	LockAcquired  time.Time
	WriteComplete time.Time
	EndTime       time.Time
	Success       bool
	Error         error
	FinalRevision int64
	Reason        string
}

// ConcurrencyTestHarness simulates multiple writers contending for state
type ConcurrencyTestHarness struct {
	stateDir string
	t        *testing.T
	sm       *state.StateManager

	// Coordination
	barrier     sync.WaitGroup
	startSignal chan struct{}

	// Results
	resultsMu sync.Mutex
	results   []WriteAttempt

	// Contention control
	contentionDelay time.Duration
	lockTimeout     time.Duration

	// Determinism
	seed int64
}

// NewConcurrencyTestHarness creates a harness for concurrent write testing
func NewConcurrencyTestHarness(t *testing.T, contentionDelay time.Duration) *ConcurrencyTestHarness {
	tmpDir := t.TempDir()
	sm := state.NewStateManager(tmpDir, nil)

	return &ConcurrencyTestHarness{
		stateDir:        tmpDir,
		t:               t,
		sm:              sm,
		startSignal:     make(chan struct{}),
		contentionDelay: contentionDelay,
		lockTimeout:     5 * time.Second,
		seed:            time.Now().UnixNano(),
	}
}

// InitializeState writes initial state that all writers will contend over
func (h *ConcurrencyTestHarness) InitializeState(service string, initialGen string) error {
	initial := &state.ActiveGenerationState{
		SchemaVersion:    1,
		Service:          service,
		ActiveGeneration: initialGen,
		Revision:         1,
		UpdatedAt:        time.Now(),
	}

	filePath := h.sm.ActiveGenerationPath(service)
	return state.AtomicWriteJSON(filePath, initial, nil)
}

// SpawnWriter starts a writer goroutine that will attempt state mutation
func (h *ConcurrencyTestHarness) SpawnWriter(
	writerID WriterID,
	service string,
	mutationFn func(*state.ActiveGenerationState) (*state.ActiveGenerationState, error),
) {
	h.barrier.Add(1)

	go func() {
		defer h.barrier.Done()

		// Wait for start signal (deterministic coordination)
		<-h.startSignal

		// Add jitter to force real contention
		time.Sleep(time.Duration(simpleHash(string(writerID))%3) * h.contentionDelay)

		attempt := WriteAttempt{
			WriterID:  writerID,
			StartTime: time.Now(),
		}
		defer func() {
			attempt.EndTime = time.Now()
			h.recordResult(attempt)
		}()

		// Acquire lock
		lockPath := h.sm.StateLockPath(service)
		lock, err := state.AcquireAdvisoryLock(lockPath, h.lockTimeout)

		attempt.LockAcquired = time.Now()
		if err != nil {
			attempt.Error = fmt.Errorf("lock failed: %w", err)
			return
		}
		defer lock.Release()

		// Read current state
		filePath := h.sm.ActiveGenerationPath(service)
		bytes, err := os.ReadFile(filePath)
		if err != nil {
			attempt.Error = fmt.Errorf("read failed: %w", err)
			return
		}

		var current state.ActiveGenerationState
		if err := json.Unmarshal(bytes, &current); err != nil {
			attempt.Error = fmt.Errorf("unmarshal failed: %w", err)
			return
		}

		// Simulate race: healing loop sleeps longer, forcing stale reads
		if writerID == WriterHealing {
			time.Sleep(20 * time.Millisecond)
		}

		// Apply mutation
		updated, err := mutationFn(&current)
		if err != nil {
			attempt.Error = fmt.Errorf("mutation failed: %w", err)
			return
		}

		// Write
		err = state.AtomicWriteJSON(filePath, updated, nil)
		attempt.WriteComplete = time.Now()

		if err != nil {
			attempt.Error = fmt.Errorf("write failed: %w", err)
			return
		}

		attempt.Success = true
		attempt.FinalRevision = updated.Revision
		attempt.Reason = "successfully wrote state"
	}()
}

// StartAll signals all waiting writers to begin (for deterministic coordination)
func (h *ConcurrencyTestHarness) StartAll() {
	close(h.startSignal)
}

// WaitAll blocks until all writers complete
func (h *ConcurrencyTestHarness) WaitAll() {
	h.barrier.Wait()
}

// Results returns all write attempts in order
func (h *ConcurrencyTestHarness) Results() []WriteAttempt {
	h.resultsMu.Lock()
	defer h.resultsMu.Unlock()

	// Return copy
	results := make([]WriteAttempt, len(h.results))
	copy(results, h.results)
	return results
}

// recordResult safely records a write attempt
func (h *ConcurrencyTestHarness) recordResult(attempt WriteAttempt) {
	h.resultsMu.Lock()
	defer h.resultsMu.Unlock()
	h.results = append(h.results, attempt)
}

// GetFinalState reads the final state after all writers complete
func (h *ConcurrencyTestHarness) GetFinalState(service string) (*state.ActiveGenerationState, error) {
	filePath := h.sm.ActiveGenerationPath(service)
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var final state.ActiveGenerationState
	if err := json.Unmarshal(bytes, &final); err != nil {
		return nil, fmt.Errorf("final state corrupted: %w", err)
	}

	return &final, nil
}

// AssertInvariants validates system invariants after completion
func (h *ConcurrencyTestHarness) AssertInvariants(service string) error {
	final, err := h.GetFinalState(service)
	if err != nil {
		return err
	}

	// Check invariants
	if final.SchemaVersion != 1 {
		return fmt.Errorf("schema version corrupted: got %d, want 1", final.SchemaVersion)
	}

	if final.Service != service {
		return fmt.Errorf("service mismatch: got %s, want %s", final.Service, service)
	}

	if final.ActiveGeneration == "" {
		return fmt.Errorf("active generation empty")
	}

	// Revision should be strictly greater than 1 (at least one write succeeded)
	if final.Revision <= 1 {
		return fmt.Errorf("no successful writes: revision still %d", final.Revision)
	}

	return nil
}

// simpleHash is a deterministic hash for jitter
func simpleHash(s string) int64 {
	h := int64(5381)
	for _, c := range s {
		h = ((h << 5) + h) + int64(c)
	}
	if h < 0 {
		h = -h
	}
	return h
}
