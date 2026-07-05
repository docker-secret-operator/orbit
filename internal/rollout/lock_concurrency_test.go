//go:build linux

package rollout

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// Phase 3.0 (Production Reliability) Concurrency: stress the deployment lock,
// which is the primitive that makes "concurrent deploy requests" and "multiple
// CLI sessions" safe. The contract is mutual exclusion: for any single service,
// at most one holder may exist at a time. Everyone else must be told the
// rollout is already in progress — clearly, never with a spurious "corrupted"
// error and never with a second successful acquisition.
func TestLockConcurrency_ExactlyOneWinner(t *testing.T) {
	const goroutines = 16
	const iterations = 40

	// Unique per-run nonce so repeated `-count` runs (same PID, same /tmp) never
	// collide on a service name or inherit a leftover lock file.
	nonce := time.Now().UnixNano()

	for iter := 0; iter < iterations; iter++ {
		service := fmt.Sprintf("concurrency-lock-%d-%d", nonce, iter)
		// Guarantee the lock file cannot outlive this iteration, even if an
		// assertion below fails — otherwise a leaked lock (live test PID) would
		// poison later iterations/runs.
		t.Cleanup(func() { _ = os.Remove(LockPath(service)) })

		var wg sync.WaitGroup
		var mu sync.Mutex
		winners := 0
		var inProgress, other int

		start := make(chan struct{})
		locks := make(chan *FileLock, goroutines)

		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start // release all goroutines at once to maximize contention
				lock, err := AcquireLock(service)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					winners++
					locks <- lock
				case containsAny(err.Error(), "already in progress"):
					inProgress++
				default:
					other++
					t.Errorf("iter %d: unexpected lock error (not clean 'in progress'): %v", iter, err)
				}
			}()
		}

		close(start)
		wg.Wait()
		close(locks)

		if winners != 1 {
			t.Fatalf("iter %d: expected exactly 1 lock winner, got %d (in-progress=%d, other=%d)",
				iter, winners, inProgress, other)
		}
		if inProgress != goroutines-1 {
			t.Fatalf("iter %d: expected %d clean 'in progress' rejections, got %d (other=%d)",
				iter, goroutines-1, inProgress, other)
		}

		// Release the single winner; a fresh acquire must now succeed.
		for l := range locks {
			if err := l.Release(); err != nil {
				t.Fatalf("iter %d: release failed: %v", iter, err)
			}
		}
		relock, err := AcquireLock(service)
		if err != nil {
			t.Fatalf("iter %d: acquire after release should succeed, got %v", iter, err)
		}
		if err := relock.Release(); err != nil {
			t.Fatalf("iter %d: final release failed: %v", iter, err)
		}
	}
}

func containsAny(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
