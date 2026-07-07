package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNewStatePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()

	sp, err := NewStatePersistence(tmpDir, log)
	if err != nil {
		t.Fatalf("NewStatePersistence failed: %v", err)
	}

	if sp == nil {
		t.Fatal("NewStatePersistence returned nil")
	}

	if !pathExists(tmpDir) {
		t.Error("state directory not created")
	}
}

func TestStatePersistenceInvalidDir(t *testing.T) {
	tmpDir := t.TempDir()
	invalidPath := filepath.Join(tmpDir, "nonexistent", "dir", "state")
	log := zap.NewNop()

	_, err := NewStatePersistence(invalidPath, log)
	if err != nil {
		t.Errorf("NewStatePersistence failed on invalid path: %v", err)
	}
}

func TestLogOperation(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	err := sp.LogOperation("start_container", "service1", map[string]string{"container": "abc123"})
	if err != nil {
		t.Fatalf("LogOperation failed: %v", err)
	}

	if len(sp.walEntries) != 1 {
		t.Errorf("WAL entries count = %d, want 1", len(sp.walEntries))
	}

	entry := sp.walEntries[0]
	if entry.Operation != "start_container" || entry.Service != "service1" {
		t.Error("WAL entry not recorded correctly")
	}
}

func TestLogOperationMultiple(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	operations := []struct {
		op      string
		service string
	}{
		{"create", "service1"},
		{"start", "service1"},
		{"health_check", "service1"},
		{"switch_traffic", "service1"},
	}

	for _, op := range operations {
		sp.LogOperation(op.op, op.service, nil)
	}

	if len(sp.walEntries) != len(operations) {
		t.Errorf("WAL entries = %d, want %d", len(sp.walEntries), len(operations))
	}
}

func TestSaveState(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())
	rollout.state.ServiceStates["service1"] = &ServiceRolloutState{
		Status:       StatusRolling,
		OldContainer: "old123",
		NewContainer: "new456",
	}

	err := sp.SaveState(rollout, TxnCompleted)
	if err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	stateFile := filepath.Join(tmpDir, "state-service1.json")
	if !pathExists(stateFile) {
		t.Errorf("state file not created at %s", stateFile)
	}
}

func TestSaveState_MultipleServices_PicksDeterministically(t *testing.T) {
	log := zap.NewNop()

	buildRollout := func() *StackRollout {
		config := &StackRolloutConfig{}
		rollout := NewStackRollout(config, log)
		rollout.state.ServiceStates["zeta"] = &ServiceRolloutState{Status: StatusRolling}
		rollout.state.ServiceStates["alpha"] = &ServiceRolloutState{Status: StatusCompleted}
		rollout.state.ServiceStates["mid"] = &ServiceRolloutState{Status: StatusFailed}
		return rollout
	}

	var chosenFiles []string
	for i := 0; i < 10; i++ {
		tmpDir := t.TempDir()
		sp, _ := NewStatePersistence(tmpDir, log)

		if err := sp.SaveState(buildRollout(), TxnCompleted); err != nil {
			t.Fatalf("SaveState failed: %v", err)
		}
		sp.Close()

		entries, err := os.ReadDir(tmpDir)
		if err != nil {
			t.Fatalf("failed to read state dir: %v", err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "state-") {
				chosenFiles = append(chosenFiles, e.Name())
			}
		}
	}

	first := chosenFiles[0]
	for _, name := range chosenFiles {
		if name != first {
			t.Fatalf("SaveState picked a different service across runs: got %v", chosenFiles)
		}
	}

	if first != "state-alpha.json" {
		t.Fatalf("expected deterministic pick of lexicographically-first service (alpha), got %s", first)
	}
}

func TestLoadState(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	original := &PersistentState{
		Version:          1,
		Service:          "service1",
		Status:           StatusRolling,
		TransactionState: TxnCompleted,
		ServiceStates: map[string]*ServiceRolloutState{
			"service1": {
				Status:       StatusRolling,
				OldContainer: "old123",
				NewContainer: "new456",
			},
		},
	}

	stateFile := filepath.Join(tmpDir, "state-service1.json")
	data, _ := json.MarshalIndent(original, "", "  ")
	os.WriteFile(stateFile, data, 0640)

	loaded, err := sp.LoadState("service1")
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	if loaded.Service != "service1" || loaded.Status != StatusRolling {
		t.Error("LoadState returned incorrect data")
	}
}

func TestLoadStateNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	_, err := sp.LoadState("nonexistent")
	if err == nil {
		t.Fatal("LoadState should error on nonexistent service")
	}
}

func TestRecoverFromCrashInProgress(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	persistent := &PersistentState{
		Service:          "service1",
		TransactionState: TxnInProgress,
		ServiceStates: map[string]*ServiceRolloutState{
			"service1": {
				Status: StatusRolling,
			},
		},
	}

	stateFile := filepath.Join(tmpDir, "state-service1.json")
	data, _ := json.MarshalIndent(persistent, "", "  ")
	os.WriteFile(stateFile, data, 0640)

	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	recovered, err := sp.RecoverFromCrash("service1", rollout)
	if err != nil {
		t.Fatalf("RecoverFromCrash failed: %v", err)
	}

	if recovered.TransactionState != TxnFailed {
		t.Errorf("transaction state = %s, want TxnFailed", recovered.TransactionState)
	}
}

func TestRecoverFromCrashCompleted(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	persistent := &PersistentState{
		Service:          "service1",
		TransactionState: TxnCompleted,
		ServiceStates: map[string]*ServiceRolloutState{
			"service1": {
				Status: StatusCompleted,
			},
		},
	}

	stateFile := filepath.Join(tmpDir, "state-service1.json")
	data, _ := json.MarshalIndent(persistent, "", "  ")
	os.WriteFile(stateFile, data, 0640)

	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	recovered, err := sp.RecoverFromCrash("service1", rollout)
	if err != nil {
		t.Fatalf("RecoverFromCrash failed: %v", err)
	}

	if recovered.TransactionState != TxnCompleted {
		t.Errorf("transaction state = %s, want TxnCompleted", recovered.TransactionState)
	}
}

func TestReadWAL(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	sp.LogOperation("create", "service1", map[string]string{"image": "img:latest"})
	sp.LogOperation("start", "service1", nil)
	sp.LogOperation("health_check", "service1", nil)

	sp.walFile.Sync()

	entries, err := sp.ReadWAL()
	if err != nil {
		t.Fatalf("ReadWAL failed: %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("WAL entries = %d, want 3", len(entries))
	}

	if entries[0].Operation != "create" {
		t.Errorf("first operation = %s, want create", entries[0].Operation)
	}
}

func TestReadWALEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	entries, err := sp.ReadWAL()
	if err != nil {
		t.Fatalf("ReadWAL failed: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("WAL entries = %d, want 0", len(entries))
	}
}

func TestRotateWAL(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	sp.LogOperation("test", "service1", nil)
	sp.walFile.Sync()

	oldPath := sp.walPath
	err := sp.RotateWAL()
	if err != nil {
		t.Fatalf("RotateWAL failed: %v", err)
	}

	if len(sp.walEntries) != 0 {
		t.Error("WAL entries not cleared after rotation")
	}

	if !pathExists(oldPath) {
		t.Error("new WAL file not created after rotation")
	}
}

func TestWALEntryChecksum(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	sp.LogOperation("op1", "service1", nil)

	if len(sp.walEntries) > 0 {
		entry := sp.walEntries[0]
		if entry.Checksum == "" {
			t.Error("WAL entry checksum not set")
		}
	}
}

func TestWALChecksum_DifferentContentProducesDifferentChecksum(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	sp.LogOperation("create", "service-a", map[string]string{"image": "img:v1"})
	sp.LogOperation("create", "service-b", map[string]string{"image": "img:v2"})

	if sp.walEntries[0].Checksum == sp.walEntries[1].Checksum {
		t.Fatal("expected different content to produce different checksums")
	}

	// A checksum that's just hex(len(data)) would collide whenever the two
	// concatenated strings happen to have the same length, and wouldn't
	// change at all if only the Data payload (not operation+service) changed.
	sp2, _ := NewStatePersistence(t.TempDir(), log)
	defer sp2.Close()
	sp2.LogOperation("create", "service-a", map[string]string{"image": "img:v1"})
	sp2.LogOperation("create", "service-a", map[string]string{"image": "img:DIFFERENT"})

	if sp2.walEntries[0].Checksum == sp2.walEntries[1].Checksum {
		t.Fatal("expected different Data payload to produce different checksums")
	}
}

func TestReadWAL_SkipsEntryWithTamperedChecksum(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	sp.LogOperation("create", "service1", map[string]string{"image": "img:latest"})
	sp.LogOperation("start", "service1", nil)
	sp.walFile.Sync()

	// Tamper with the WAL file on disk: rewrite the first entry's Operation
	// field without updating its Checksum, simulating on-disk corruption.
	walBytes, err := os.ReadFile(sp.walPath)
	if err != nil {
		t.Fatalf("failed to read WAL file: %v", err)
	}
	tampered := []byte(strings.Replace(string(walBytes), `"operation":"create"`, `"operation":"TAMPERED"`, 1))
	if err := os.WriteFile(sp.walPath, tampered, 0640); err != nil {
		t.Fatalf("failed to write tampered WAL file: %v", err)
	}

	entries, err := sp.ReadWAL()
	if err != nil {
		t.Fatalf("ReadWAL failed: %v", err)
	}

	for _, e := range entries {
		if e.Operation == "TAMPERED" {
			t.Fatal("expected tampered entry to be rejected by checksum verification, but it was returned")
		}
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 valid entry (tampered one skipped), got %d", len(entries))
	}
	if entries[0].Operation != "start" {
		t.Fatalf("expected remaining entry to be 'start', got %q", entries[0].Operation)
	}
}

func TestSaveStateEmptyServices(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	err := sp.SaveState(rollout, TxnCompleted)
	if err == nil {
		t.Error("SaveState should error on empty service states")
	}
}

func TestPersistentStateVersion(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: map[string]*ServiceRolloutState{
				"service1": {
					Status: StatusRolling,
				},
			},
		},
	}

	sp.SaveState(rollout, TxnCompleted)

	loaded, _ := sp.LoadState("service1")
	if loaded.Version != 1 {
		t.Errorf("version = %d, want 1", loaded.Version)
	}
}

func TestWALDurability(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)

	sp.LogOperation("op1", "service1", map[string]string{"key": "value"})
	sp.walFile.Sync()
	sp.Close()

	sp2, _ := NewStatePersistence(tmpDir, log)
	defer sp2.Close()

	entries, _ := sp2.ReadWAL()
	if len(entries) != 1 {
		t.Errorf("WAL entries after reload = %d, want 1", len(entries))
	}
}

func TestStatePersistenceTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: map[string]*ServiceRolloutState{
				"service1": {
					Status: StatusRolling,
				},
			},
		},
	}

	before := time.Now()
	sp.SaveState(rollout, TxnCompleted)
	after := time.Now()

	loaded, _ := sp.LoadState("service1")
	if loaded.Timestamp.Before(before) || loaded.Timestamp.After(after.Add(1*time.Second)) {
		t.Error("timestamp not in expected range")
	}
}

func TestLogOperationConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	log := zap.NewNop()
	sp, _ := NewStatePersistence(tmpDir, log)
	defer sp.Close()

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			sp.LogOperation("op", "service1", nil)
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	if len(sp.walEntries) != 10 {
		t.Errorf("WAL entries = %d, want 10", len(sp.walEntries))
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
