package benchmark

import (
	"runtime"
	"testing"
	"time"
)

// TestGetStats_LargeUniformLatencies_DoesNotStackOverflow reproduces a real
// crash: LatencyRecorder.GetStats sorts recorded latencies with a hand-rolled
// quickSort using a last-element pivot. On a large uniform (all-equal)
// input — realistic for a load test hammering a fast, consistent operation —
// every partition call puts every element on one side, so recursion depth is
// O(n) instead of O(log n), overflowing the goroutine stack.
// Skipped in -short mode: it allocates ~12M durations and takes a few
// seconds even with the fix; that's the point (proving the fix scales),
// not something to run on every quick iteration.
func TestGetStats_LargeUniformLatencies_DoesNotStackOverflow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-N sort stress test in short mode")
	}

	lr := &LatencyRecorder{}
	const n = 12_000_000
	for i := 0; i < n; i++ {
		lr.Record(time.Millisecond)
	}

	avg, min, max, p50, p95, p99 := lr.GetStats()

	if avg != time.Millisecond || min != time.Millisecond || max != time.Millisecond ||
		p50 != time.Millisecond || p95 != time.Millisecond || p99 != time.Millisecond {
		t.Fatalf("expected all stats to equal 1ms for uniform input, got avg=%v min=%v max=%v p50=%v p95=%v p99=%v",
			avg, min, max, p50, p95, p99)
	}
}

// TestBenchmarkRunner_MemoryFreed_IsNotUnderflowed reproduces the reported
// bug: runtime.MemStats.Frees is monotonically increasing for the life of
// the process, so end.Frees >= start.Frees always holds. Computing
// start.Frees - end.Frees (as opposed to end.Frees - start.Frees) on these
// uint64 values underflows to a value near math.MaxUint64 whenever any
// frees happen during the run.
func TestBenchmarkRunner_MemoryFreed_IsNotUnderflowed(t *testing.T) {
	br := NewBenchmarkRunner("test", 1)
	br.Start()

	// Generate garbage and force a collection so Frees measurably advances
	// between start and stop.
	for i := 0; i < 10000; i++ {
		_ = make([]byte, 1024)
	}
	runtime.GC()

	br.RecordOperation(time.Millisecond)
	result := br.Stop()

	// A correct end-start diff over this short window is at most in the
	// thousands to low millions of objects; an underflowed uint64 would be
	// within a hair of 2^64 (~1.8e19) — nowhere close to this threshold.
	const maxPlausibleFrees = 100_000_000
	if result.MemoryFreed > maxPlausibleFrees {
		t.Fatalf("MemoryFreed = %d, looks like an underflowed uint64 (computed as start.Frees - end.Frees instead of end.Frees - start.Frees)", result.MemoryFreed)
	}
}
