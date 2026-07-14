package benchmark

import (
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"
)

// BenchmarkResult captures performance metrics for a single benchmark
type BenchmarkResult struct {
	Name            string
	Iterations      int64
	Duration        time.Duration
	OpsPerSecond    float64
	AvgLatency      time.Duration
	MinLatency      time.Duration
	MaxLatency      time.Duration
	P50Latency      time.Duration
	P95Latency      time.Duration
	P99Latency      time.Duration
	MemoryAllocated uint64
	MemoryFreed     uint64
	GCRuns          uint32
}

// LatencyRecorder tracks individual operation latencies
type LatencyRecorder struct {
	latencies []time.Duration
	mu        sync.Mutex
}

// Record records a single operation latency
func (lr *LatencyRecorder) Record(duration time.Duration) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.latencies = append(lr.latencies, duration)
}

// GetStats calculates latency statistics
func (lr *LatencyRecorder) GetStats() (avg, min, max, p50, p95, p99 time.Duration) {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	if len(lr.latencies) == 0 {
		return 0, 0, 0, 0, 0, 0
	}

	// Sort for percentiles
	sorted := make([]time.Duration, len(lr.latencies))
	copy(sorted, lr.latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	// Min/Max
	min = sorted[0]
	max = sorted[len(sorted)-1]

	// Average
	sum := int64(0)
	for _, d := range sorted {
		sum += int64(d)
	}
	avg = time.Duration(sum / int64(len(sorted)))

	// Percentiles
	p50 = sorted[len(sorted)*50/100]
	p95 = sorted[len(sorted)*95/100]
	p99 = sorted[len(sorted)*99/100]

	return
}

// BenchmarkRunner manages a benchmark execution
type BenchmarkRunner struct {
	name          string
	iterations    int64
	latencies     *LatencyRecorder
	startMemStats runtime.MemStats
	endMemStats   runtime.MemStats
	startTime     time.Time
}

// NewBenchmarkRunner creates a new benchmark runner
func NewBenchmarkRunner(name string, iterations int64) *BenchmarkRunner {
	runtime.ReadMemStats(&runtime.MemStats{})
	return &BenchmarkRunner{
		name:       name,
		iterations: iterations,
		latencies:  &LatencyRecorder{},
	}
}

// Start begins the benchmark
func (br *BenchmarkRunner) Start() {
	runtime.GC()
	runtime.ReadMemStats(&br.startMemStats)
	br.startTime = time.Now()
}

// RecordOperation records a single operation
func (br *BenchmarkRunner) RecordOperation(duration time.Duration) {
	br.latencies.Record(duration)
}

// Stop ends the benchmark and returns results
func (br *BenchmarkRunner) Stop() *BenchmarkResult {
	duration := time.Since(br.startTime)
	runtime.ReadMemStats(&br.endMemStats)

	avg, min, max, p50, p95, p99 := br.latencies.GetStats()

	allocDiff := br.endMemStats.Alloc - br.startMemStats.Alloc
	freeDiff := br.endMemStats.Frees - br.startMemStats.Frees

	opsPerSec := float64(br.iterations) / duration.Seconds()

	return &BenchmarkResult{
		Name:            br.name,
		Iterations:      br.iterations,
		Duration:        duration,
		OpsPerSecond:    opsPerSec,
		AvgLatency:      avg,
		MinLatency:      min,
		MaxLatency:      max,
		P50Latency:      p50,
		P95Latency:      p95,
		P99Latency:      p99,
		MemoryAllocated: allocDiff,
		MemoryFreed:     freeDiff,
		GCRuns:          br.endMemStats.NumGC - br.startMemStats.NumGC,
	}
}

// LoadTestRunner manages load test execution
type LoadTestRunner struct {
	name            string
	concurrency     int
	duration        time.Duration
	operationsCount int64
	latencies       *LatencyRecorder
	startTime       time.Time
	mu              sync.Mutex
}

// NewLoadTestRunner creates a new load test runner
func NewLoadTestRunner(name string, concurrency int, duration time.Duration) *LoadTestRunner {
	return &LoadTestRunner{
		name:        name,
		concurrency: concurrency,
		duration:    duration,
		latencies:   &LatencyRecorder{},
	}
}

// Start begins the load test
func (ltr *LoadTestRunner) Start() {
	runtime.GC()
	ltr.startTime = time.Now()
}

// RecordOperation records a single operation under load
func (ltr *LoadTestRunner) RecordOperation(duration time.Duration) {
	ltr.mu.Lock()
	ltr.operationsCount++
	ltr.mu.Unlock()
	ltr.latencies.Record(duration)
}

// Stop ends the load test and returns results
func (ltr *LoadTestRunner) Stop() *LoadTestResult {
	elapsed := time.Since(ltr.startTime)
	avg, min, max, p50, p95, p99 := ltr.latencies.GetStats()

	throughput := float64(ltr.operationsCount) / elapsed.Seconds()

	return &LoadTestResult{
		Name:        ltr.name,
		Concurrency: ltr.concurrency,
		Duration:    elapsed,
		Operations:  ltr.operationsCount,
		Throughput:  throughput,
		AvgLatency:  avg,
		MinLatency:  min,
		MaxLatency:  max,
		P50Latency:  p50,
		P95Latency:  p95,
		P99Latency:  p99,
	}
}

// LoadTestResult captures load test metrics
type LoadTestResult struct {
	Name        string
	Concurrency int
	Duration    time.Duration
	Operations  int64
	Throughput  float64
	AvgLatency  time.Duration
	MinLatency  time.Duration
	MaxLatency  time.Duration
	P50Latency  time.Duration
	P95Latency  time.Duration
	P99Latency  time.Duration
}

// String returns formatted results
func (br *BenchmarkResult) String() string {
	return fmt.Sprintf(`
%s:
  Iterations:     %d
  Duration:       %v
  Ops/sec:        %.2f
  Avg Latency:    %v
  Min Latency:    %v
  Max Latency:    %v
  P50 Latency:    %v
  P95 Latency:    %v
  P99 Latency:    %v
  Memory Alloc:   %d bytes
  Memory Freed:   %d bytes
  GC Runs:        %d
`,
		br.Name,
		br.Iterations,
		br.Duration,
		br.OpsPerSecond,
		br.AvgLatency,
		br.MinLatency,
		br.MaxLatency,
		br.P50Latency,
		br.P95Latency,
		br.P99Latency,
		br.MemoryAllocated,
		br.MemoryFreed,
		br.GCRuns,
	)
}

// String returns formatted load test results
func (ltr *LoadTestResult) String() string {
	return fmt.Sprintf(`
%s (Concurrency: %d):
  Duration:       %v
  Operations:     %d
  Throughput:     %.2f ops/sec
  Avg Latency:    %v
  Min Latency:    %v
  Max Latency:    %v
  P50 Latency:    %v
  P95 Latency:    %v
  P99 Latency:    %v
`,
		ltr.Name,
		ltr.Concurrency,
		ltr.Duration,
		ltr.Operations,
		ltr.Throughput,
		ltr.AvgLatency,
		ltr.MinLatency,
		ltr.MaxLatency,
		ltr.P50Latency,
		ltr.P95Latency,
		ltr.P99Latency,
	)
}
