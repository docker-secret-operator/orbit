// Package rollout implements zero-downtime rolling updates.
package rollout

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProcessStartTicks represents boot-relative process creation time (Linux only).
type ProcessStartTicks uint64

// LockMetadata holds lock file information.
type LockMetadata struct {
	PID               int               `json:"pid"`
	ProcessStartTicks ProcessStartTicks `json:"process_start_ticks"`
	Hostname          string            `json:"hostname"`
	CreatedAt         time.Time         `json:"created_at"`
	Operation         string            `json:"operation"`
	Service           string            `json:"service"`
	User              string            `json:"user,omitempty"`
}

// FileLock represents an acquired lock.
type FileLock struct {
	path string
	meta LockMetadata
}

// AcquireLock attempts to acquire an exclusive lock for a service.
// Returns error if lock exists and process is alive.
// Removes lock if process is dead.
func AcquireLock(service string) (*FileLock, error) {
	if err := validateServiceNameForCLIArg(service); err != nil {
		return nil, err
	}
	lockPath := LockPath(service)
	hostname, _ := os.Hostname()

	// Get current process start ticks.
	startTicks, err := GetProcessStartTicks(os.Getpid())
	if err != nil {
		return nil, fmt.Errorf("lock: cannot get process start ticks: %w", err)
	}

	meta := LockMetadata{
		PID:               os.Getpid(),
		ProcessStartTicks: startTicks,
		Hostname:          hostname,
		CreatedAt:         time.Now(),
		Operation:         "rollout",
		Service:           service,
		User:              os.Getenv("USER"),
	}

	// Try exclusive create.
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		// Lock acquired successfully.
		data, _ := json.MarshalIndent(meta, "", "  ")
		f.Write(data) //nolint:errcheck // lock file already created via O_EXCL; metadata write is advisory
		f.Close()
		return &FileLock{path: lockPath, meta: meta}, nil
	}

	// Lock exists — validate it. It may have been created microseconds ago by a
	// concurrent acquirer that has not yet written its metadata (the O_EXCL
	// create and the metadata write are two steps, not one atomic op). In that
	// window the file is empty and unparseable; retry briefly to distinguish a
	// mid-creation lock from a genuinely corrupt one, so concurrent deploys /
	// multiple CLI sessions get a clean "already in progress" rather than a
	// false "corrupted — manual inspection required".
	existing, err := readLockMetadataWithRetry(lockPath)
	if err != nil {
		return nil, fmt.Errorf(
			"lock file corrupted for %q (path: %s) — manual inspection required: %w",
			service, lockPath, err,
		)
	}

	// Check if process is alive with matching start ticks.
	if !isProcessAlive(existing.PID, existing.ProcessStartTicks) {
		// Process dead — safe to remove.
		os.Remove(lockPath)
		// Single tail-call retry (safe, not recursive).
		return AcquireLock(service)
	}

	// Process is alive — BLOCK deployment.
	age := time.Since(existing.CreatedAt).Round(time.Second)
	return nil, fmt.Errorf(
		"rollout for %q is already in progress:\n"+
			"  PID: %d on %s\n"+
			"  Started: %v ago\n"+
			"  Lock: %s\n\n"+
			"Actions:\n"+
			"  1. Wait for deployment to complete\n"+
			"  2. Verify process: ps -p %d\n"+
			"  3. Force if confirmed dead: docker orbit rollout %q --force-unlock\n",
		service, existing.PID, existing.Hostname,
		age, lockPath,
		existing.PID, service,
	)
}

// AcquireLockForce forcefully removes stale lock if process is confirmed dead.
func AcquireLockForce(service string) (*FileLock, error) {
	if err := validateServiceNameForCLIArg(service); err != nil {
		return nil, err
	}
	lockPath := LockPath(service)

	// Read existing lock.
	existing, err := readLockMetadata(lockPath)
	if err != nil {
		// Lock corrupted — safe to remove and retry.
		os.Remove(lockPath)
		return AcquireLock(service)
	}

	// Verify process is actually dead.
	if isProcessAlive(existing.PID, existing.ProcessStartTicks) {
		return nil, fmt.Errorf(
			"cannot force unlock: process %d is still alive on %s",
			existing.PID, existing.Hostname,
		)
	}

	// Process is dead — safe to remove.
	os.Remove(lockPath)
	return AcquireLock(service)
}

// Release removes the lock file.
func (l *FileLock) Release() error {
	if l == nil {
		return nil
	}
	return os.Remove(l.path)
}

// readLockMetadata reads and parses lock metadata.
func readLockMetadata(lockPath string) (LockMetadata, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return LockMetadata{}, err
	}
	var meta LockMetadata
	return meta, json.Unmarshal(data, &meta)
}

// readLockMetadataWithRetry reads and parses the lock file, tolerating the
// brief window in which a concurrent acquirer has created the lock file
// (O_EXCL) but not yet written its metadata — during that window os.ReadFile
// returns an empty file and json.Unmarshal fails with "unexpected end of JSON
// input". Writing the ~200-byte metadata takes microseconds, so a legitimately
// held lock parses within the first retry; a file that stays unreadable across
// the whole budget (~20ms) is treated as genuine corruption by the caller (or a
// creator that died in the window — equally warranting manual attention).
func readLockMetadataWithRetry(lockPath string) (LockMetadata, error) {
	const attempts = 10
	const backoff = 2 * time.Millisecond
	var lastErr error
	for i := 0; i < attempts; i++ {
		meta, err := readLockMetadata(lockPath)
		if err == nil {
			return meta, nil
		}
		lastErr = err
		time.Sleep(backoff)
	}
	return LockMetadata{}, lastErr
}

// GetProcessStartTicks reads process creation time (in boot-relative ticks) from /proc.
// Linux-only. Returns error on non-Linux systems.
func GetProcessStartTicks(pid int) (ProcessStartTicks, error) {
	statFile := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statFile)
	if err != nil {
		return 0, fmt.Errorf("cannot read /proc/[pid]/stat: %w", err)
	}

	// Parse /proc/[pid]/stat format:
	// pid (comm) state ppid pgrp session tty_nr tpgid flags minflt ... starttime
	// starttime is field 21 (1-indexed) or field 20 (0-indexed after closing paren).

	line := string(data)

	// Find closing paren of comm field (handles comm with spaces).
	closingParen := strings.LastIndex(line, ")")
	if closingParen == -1 {
		return 0, fmt.Errorf("invalid /proc/[pid]/stat format: no closing paren")
	}

	// Split remaining fields.
	fields := strings.Fields(line[closingParen+1:])
	if len(fields) < 20 {
		return 0, fmt.Errorf("insufficient fields in /proc/[pid]/stat: got %d, need 20", len(fields))
	}

	// Field 20 (0-indexed from after closing paren) is starttime in ticks.
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse starttime from field 20: %w", err)
	}

	return ProcessStartTicks(startTicks), nil
}

// isProcessAlive verifies process exists and has matching start ticks.
// Protects against PID reuse.
func isProcessAlive(pid int, expectedStartTicks ProcessStartTicks) bool {
	if pid <= 0 {
		return false
	}

	// Check if process exists (signal 0 is no-op but validates PID).
	err := syscall.Kill(pid, 0)
	if err != nil {
		return false
	}

	// Verify start ticks match (PID reuse protection).
	actualTicks, err := GetProcessStartTicks(pid)
	if err != nil {
		// Cannot read /proc — assume dead (conservative).
		return false
	}

	return actualTicks == expectedStartTicks
}

// LockPath returns the lock file path for a service.
func LockPath(service string) string {
	return fmt.Sprintf("/tmp/orbit-%s.lock", service)
}
