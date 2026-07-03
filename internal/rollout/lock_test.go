package rollout

import (
	"os"
	"testing"
	"time"
)

func TestAcquireLockSuccess(t *testing.T) {
	service := "test-service"
	lockPath := LockPath(service)
	defer os.Remove(lockPath)

	lock, err := AcquireLock(service)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer lock.Release()

	// Verify file exists.
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("lock file not created: %v", err)
	}

	// Verify metadata.
	if lock.meta.PID != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), lock.meta.PID)
	}
	if lock.meta.Service != service {
		t.Errorf("expected service %s, got %s", service, lock.meta.Service)
	}
}

func TestAcquireLockBlocked(t *testing.T) {
	service := "test-service-blocked"
	lockPath := LockPath(service)
	defer os.Remove(lockPath)

	lock1, err := AcquireLock(service)
	if err != nil {
		t.Fatalf("first AcquireLock failed: %v", err)
	}
	defer lock1.Release()

	// Try to acquire same lock.
	_, err = AcquireLock(service)
	if err == nil {
		t.Fatal("expected error when lock already held")
	}
}

func TestGetProcessStartTicks(t *testing.T) {
	ticks, err := GetProcessStartTicks(os.Getpid())
	if err != nil {
		t.Fatalf("GetProcessStartTicks failed: %v", err)
	}
	if ticks == 0 {
		t.Error("expected non-zero start ticks")
	}
}

func TestIsProcessAlive(t *testing.T) {
	ticks, err := GetProcessStartTicks(os.Getpid())
	if err != nil {
		t.Fatalf("GetProcessStartTicks failed: %v", err)
	}

	// Current process should be alive.
	if !isProcessAlive(os.Getpid(), ticks) {
		t.Error("expected current process to be alive")
	}

	// Invalid PID should be dead.
	if isProcessAlive(-1, 0) {
		t.Error("expected invalid PID to be dead")
	}
}

func TestLockRelease(t *testing.T) {
	service := "test-release"
	lockPath := LockPath(service)

	lock, err := AcquireLock(service)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}

	err = lock.Release()
	if err != nil {
		t.Errorf("Release failed: %v", err)
	}

	// File should be deleted.
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("lock file not released")
	}
}

func TestLockMetadataPreserved(t *testing.T) {
	service := "test-metadata"
	lockPath := LockPath(service)
	defer os.Remove(lockPath)

	lock, err := AcquireLock(service)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	defer lock.Release()

	// Read back metadata.
	existing, err := readLockMetadata(lockPath)
	if err != nil {
		t.Fatalf("readLockMetadata failed: %v", err)
	}

	if existing.PID != os.Getpid() {
		t.Errorf("PID mismatch: %d vs %d", existing.PID, os.Getpid())
	}

	if existing.Service != service {
		t.Errorf("Service mismatch: %s vs %s", existing.Service, service)
	}

	// CreatedAt should be recent.
	if time.Since(existing.CreatedAt) > 5*time.Second {
		t.Error("CreatedAt is stale")
	}
}

func TestLockPathFormat(t *testing.T) {
	service := "my-service"
	path := LockPath(service)

	expected := "/tmp/orbit-my-service.lock"
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}
