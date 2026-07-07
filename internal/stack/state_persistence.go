package stack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// PersistentState holds serializable rollout state.
type PersistentState struct {
	Version          int                             `json:"version"`
	Timestamp        time.Time                       `json:"timestamp"`
	Service          string                          `json:"service"`
	Status           ServiceStatus                   `json:"status"`
	TransactionState TransactionState                `json:"transaction_state"`
	ServiceStates    map[string]*ServiceRolloutState `json:"service_states"`
	Operations       []OperationLog                  `json:"operations"`
	LastCheckpoint   time.Time                       `json:"last_checkpoint"`
}

// OperationLog records an operation for recovery.
type OperationLog struct {
	Name       string    `json:"name"`
	Service    string    `json:"service"`
	Timestamp  time.Time `json:"timestamp"`
	Executed   bool      `json:"executed"`
	RolledBack bool      `json:"rolled_back"`
	Error      string    `json:"error,omitempty"`
}

// StatePersistence handles saving and recovering rollout state.
type StatePersistence struct {
	stateDir string
	log      *zap.Logger
	mu       sync.Mutex

	// WAL (Write-Ahead Log)
	walFile       *os.File
	walPath       string
	walEntries    []WALEntry
	walCheckpoint time.Time
}

// WALEntry is a Write-Ahead Log entry.
type WALEntry struct {
	Timestamp time.Time   `json:"timestamp"`
	Operation string      `json:"operation"`
	Service   string      `json:"service"`
	Data      interface{} `json:"data"`
	Checksum  string      `json:"checksum"`
}

// NewStatePersistence creates a new state persistence manager.
func NewStatePersistence(stateDir string, log *zap.Logger) (*StatePersistence, error) {
	if log == nil {
		log = zap.NewNop()
	}

	// Create state directory if it doesn't exist
	if err := os.MkdirAll(stateDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	walPath := filepath.Join(stateDir, "rollout.wal")

	sp := &StatePersistence{
		stateDir:      stateDir,
		log:           log,
		walPath:       walPath,
		walEntries:    make([]WALEntry, 0),
		walCheckpoint: time.Now(),
	}

	// Initialize WAL
	if err := sp.initWAL(); err != nil {
		return nil, err
	}

	return sp, nil
}

// initWAL initializes the Write-Ahead Log.
func (sp *StatePersistence) initWAL() error {
	file, err := os.OpenFile(sp.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return fmt.Errorf("failed to open WAL file: %w", err)
	}

	sp.walFile = file
	sp.log.Info("WAL initialized",
		zap.String("path", sp.walPath))

	return nil
}

// LogOperation writes an operation to the WAL before execution.
func (sp *StatePersistence) LogOperation(operation, service string, data interface{}) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	entry := WALEntry{
		Timestamp: time.Now(),
		Operation: operation,
		Service:   service,
		Data:      data,
	}
	entry.Checksum = sp.calculateChecksum(entry)

	sp.walEntries = append(sp.walEntries, entry)

	// Write to file immediately for durability
	if err := sp.writeWALEntry(entry); err != nil {
		sp.log.Error("failed to write WAL entry",
			zap.String("operation", operation),
			zap.String("service", service),
			zap.Error(err))
		return err
	}

	sp.log.Debug("operation logged",
		zap.String("operation", operation),
		zap.String("service", service))

	return nil
}

// writeWALEntry writes a single WAL entry to disk.
func (sp *StatePersistence) writeWALEntry(entry WALEntry) error {
	if sp.walFile == nil {
		return fmt.Errorf("WAL file not open")
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal WAL entry: %w", err)
	}

	data = append(data, '\n')

	if _, err := sp.walFile.Write(data); err != nil {
		return fmt.Errorf("failed to write WAL entry: %w", err)
	}

	if err := sp.walFile.Sync(); err != nil {
		return fmt.Errorf("failed to fsync WAL entry: %w", err)
	}
	return nil
}

// SaveState saves the current rollout state to disk.
func (sp *StatePersistence) SaveState(rollout *StackRollout, txnState TransactionState) error {
	rollout.mu.Lock()
	if len(rollout.state.ServiceStates) == 0 {
		rollout.mu.Unlock()
		return fmt.Errorf("no service states to save")
	}

	// Pick the lexicographically-first service name so repeated saves of the
	// same state are deterministic — Go map iteration order is randomized,
	// so "first" via a bare range would otherwise vary from run to run.
	names := make([]string, 0, len(rollout.state.ServiceStates))
	for name := range rollout.state.ServiceStates {
		names = append(names, name)
	}
	sort.Strings(names)
	service := names[0]

	persistent := &PersistentState{
		Version:          1,
		Timestamp:        time.Now(),
		Service:          service,
		Status:           rollout.state.ServiceStates[service].Status,
		TransactionState: txnState,
		ServiceStates:    rollout.state.ServiceStates,
		Operations:       make([]OperationLog, 0),
		LastCheckpoint:   time.Now(),
	}

	// Marshal while still holding the lock: persistent.ServiceStates aliases
	// the live map, and json.Marshal ranges over it plus reads each
	// *ServiceRolloutState's fields.
	data, err := json.MarshalIndent(persistent, "", "  ")
	rollout.mu.Unlock()
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Write to state file
	stateFile := filepath.Join(sp.stateDir, fmt.Sprintf("state-%s.json", service))

	if err := os.WriteFile(stateFile, data, 0640); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	sp.log.Info("state saved",
		zap.String("service", service),
		zap.String("file", stateFile))

	return nil
}

// LoadState loads previously saved rollout state.
func (sp *StatePersistence) LoadState(service string) (*PersistentState, error) {
	stateFile := filepath.Join(sp.stateDir, fmt.Sprintf("state-%s.json", service))

	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no saved state for service %q", service)
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var persistent PersistentState
	if err := json.Unmarshal(data, &persistent); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state: %w", err)
	}

	sp.log.Info("state loaded",
		zap.String("service", service),
		zap.String("file", stateFile),
		zap.String("transaction_state", string(persistent.TransactionState)))

	return &persistent, nil
}

// RecoverFromCrash attempts to recover from a crash using saved state.
func (sp *StatePersistence) RecoverFromCrash(service string, rollout *StackRollout) (*PersistentState, error) {
	sp.log.Warn("attempting crash recovery",
		zap.String("service", service))

	// Load saved state
	persistent, err := sp.LoadState(service)
	if err != nil {
		return nil, err
	}

	// Check transaction state to determine recovery action
	switch persistent.TransactionState {
	case TxnInProgress:
		sp.log.Warn("transaction was in progress, needs rollback",
			zap.String("service", service))
		// Mark for rollback
		persistent.TransactionState = TxnFailed

	case TxnRolledBack:
		sp.log.Info("transaction was already rolled back, recovery complete",
			zap.String("service", service))

	case TxnCompleted:
		sp.log.Info("transaction was completed before crash, no recovery needed",
			zap.String("service", service))
	}

	// Restore service states
	rollout.mu.Lock()
	for svcName, state := range persistent.ServiceStates {
		rollout.state.ServiceStates[svcName] = state
	}
	rollout.mu.Unlock()

	return persistent, nil
}

// ReadWAL reads and parses the Write-Ahead Log.
func (sp *StatePersistence) ReadWAL() ([]WALEntry, error) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	data, err := os.ReadFile(sp.walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return make([]WALEntry, 0), nil
		}
		return nil, fmt.Errorf("failed to read WAL: %w", err)
	}

	entries := make([]WALEntry, 0)

	// Parse JSONL format
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var entry WALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			sp.log.Warn("failed to parse WAL entry",
				zap.Error(err))
			continue
		}

		if expected := sp.calculateChecksum(entry); expected != entry.Checksum {
			sp.log.Warn("WAL entry failed checksum verification, discarding",
				zap.String("operation", entry.Operation),
				zap.String("service", entry.Service))
			continue
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// RotateWAL rotates the WAL file after successful checkpoint.
func (sp *StatePersistence) RotateWAL() error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	if sp.walFile != nil {
		sp.walFile.Close()
	}

	// Archive old WAL
	timestamp := time.Now().Format("20060102-150405")
	archivedPath := filepath.Join(sp.stateDir, fmt.Sprintf("rollout-%s.wal", timestamp))

	if err := os.Rename(sp.walPath, archivedPath); err != nil {
		sp.log.Warn("failed to archive WAL",
			zap.Error(err))
		// Don't fail if archive fails
	}

	// Create new WAL
	if err := sp.initWAL(); err != nil {
		return err
	}

	sp.walEntries = make([]WALEntry, 0)
	sp.walCheckpoint = time.Now()

	sp.log.Info("WAL rotated",
		zap.String("archived", archivedPath))

	return nil
}

// Close closes the state persistence manager.
func (sp *StatePersistence) Close() error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	if sp.walFile != nil {
		return sp.walFile.Close()
	}
	return nil
}

// calculateChecksum computes a checksum over an entry's meaningful content
// (everything except the Checksum field itself), so tampering with any of
// Timestamp/Operation/Service/Data is detectable on read.
func (sp *StatePersistence) calculateChecksum(entry WALEntry) string {
	payload, err := json.Marshal(struct {
		Timestamp time.Time   `json:"timestamp"`
		Operation string      `json:"operation"`
		Service   string      `json:"service"`
		Data      interface{} `json:"data"`
	}{entry.Timestamp, entry.Operation, entry.Service, entry.Data})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
