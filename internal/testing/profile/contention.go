package profile

import (
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"
)

// LockContentionAnalyzer measures lock contention and wait times
type LockContentionAnalyzer struct {
	name         string
	measurements []float64
	mu           sync.Mutex
}

// NewLockContentionAnalyzer creates a new contention analyzer
func NewLockContentionAnalyzer(name string) *LockContentionAnalyzer {
	return &LockContentionAnalyzer{
		name:         name,
		measurements: make([]float64, 0, 10000),
	}
}

// MeasureLockWait measures time spent waiting for a lock
func (lca *LockContentionAnalyzer) MeasureLockWait(fn func()) {
	start := time.Now()
	fn()
	waitTime := time.Since(start).Microseconds()

	lca.mu.Lock()
	lca.measurements = append(lca.measurements, float64(waitTime))
	lca.mu.Unlock()
}

// ContentionResult represents contention analysis results
type ContentionResult struct {
	Name              string
	Measurements      int
	AvgWaitTime       float64 // microseconds
	MinWaitTime       float64
	MaxWaitTime       float64
	P50WaitTime       float64
	P95WaitTime       float64
	P99WaitTime       float64
	ContentionScore   float64 // 0-100: higher = more contention
	ScalabilityFactor float64 // throughput ratio under contention
}

// Analyze analyzes collected contention data
func (lca *LockContentionAnalyzer) Analyze() *ContentionResult {
	lca.mu.Lock()
	defer lca.mu.Unlock()

	if len(lca.measurements) == 0 {
		return &ContentionResult{Name: lca.name}
	}

	// Sort for percentile calculation
	sorted := make([]float64, len(lca.measurements))
	copy(sorted, lca.measurements)
	sort.Float64s(sorted)

	result := &ContentionResult{
		Name:         lca.name,
		Measurements: len(sorted),
		MinWaitTime:  sorted[0],
		MaxWaitTime:  sorted[len(sorted)-1],
	}

	// Calculate average
	sum := 0.0
	for _, m := range sorted {
		sum += m
	}
	result.AvgWaitTime = sum / float64(len(sorted))

	// Calculate percentiles
	result.P50WaitTime = sorted[len(sorted)*50/100]
	result.P95WaitTime = sorted[len(sorted)*95/100]
	result.P99WaitTime = sorted[len(sorted)*99/100]

	// Calculate contention score (0-100)
	// Higher max wait time relative to average indicates high contention
	if result.AvgWaitTime > 0 {
		ratio := result.MaxWaitTime / result.AvgWaitTime
		result.ContentionScore = min(100, ratio*10)
	}

	return result
}

// AnalyzeConcurrentScalability measures how throughput scales with goroutine count
type ScalabilityTest struct {
	name      string
	operation func()
	baseline  float64 // throughput at 1 goroutine
	results   map[int]float64
	mu        sync.Mutex
}

// NewScalabilityTest creates a scalability test
func NewScalabilityTest(name string, operation func()) *ScalabilityTest {
	return &ScalabilityTest{
		name:      name,
		operation: operation,
		results:   make(map[int]float64),
	}
}

// Run measures throughput with N concurrent goroutines
func (st *ScalabilityTest) Run(concurrency int, duration time.Duration) float64 {
	var wg sync.WaitGroup
	done := make(chan struct{})
	var ops int64
	var mu sync.Mutex

	start := time.Now()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					st.operation()
					mu.Lock()
					ops++
					mu.Unlock()
				}
			}
		}()
	}

	time.Sleep(duration)
	close(done)
	wg.Wait()

	elapsed := time.Since(start)
	throughput := float64(ops) / elapsed.Seconds()

	st.mu.Lock()
	st.results[concurrency] = throughput
	st.mu.Unlock()

	return throughput
}

// ScalabilityResult measures how well throughput scales
type ScalabilityResult struct {
	Operation     string
	Measurements  map[int]float64 // concurrency → throughput
	Linear1To4    float64         // should be ~4x for linear scaling
	Linear1To8    float64         // should be ~8x for linear scaling
	OptimalCores  int             // concurrency with best throughput
	MaxThroughput float64
	Scalability   string // "Excellent", "Good", "Fair", "Poor"
}

// Analyze computes scalability metrics
func (st *ScalabilityTest) Analyze() *ScalabilityResult {
	st.mu.Lock()
	defer st.mu.Unlock()

	result := &ScalabilityResult{
		Operation:    st.name,
		Measurements: st.results,
	}

	if len(st.results) == 0 {
		result.Scalability = "No Data"
		return result
	}

	// Find baseline (1 goroutine)
	baseline := st.results[1]
	if baseline == 0 {
		result.Scalability = "Invalid Baseline"
		return result
	}

	// Calculate scaling factors
	if th4, ok := st.results[4]; ok {
		result.Linear1To4 = th4 / baseline
	}
	if th8, ok := st.results[8]; ok {
		result.Linear1To8 = th8 / baseline
	}

	// Find optimal concurrency
	maxTh := 0.0
	for conc, th := range st.results {
		if th > maxTh {
			maxTh = th
			result.OptimalCores = conc
		}
	}
	result.MaxThroughput = maxTh

	// Classify scalability
	if result.Linear1To8 > 7.5 {
		result.Scalability = "Excellent (>7.5x linear)"
	} else if result.Linear1To8 > 6.0 {
		result.Scalability = "Good (6-7.5x)"
	} else if result.Linear1To8 > 4.0 {
		result.Scalability = "Fair (4-6x)"
	} else {
		result.Scalability = "Poor (<4x)"
	}

	return result
}

// String formats the scalability result
func (sr *ScalabilityResult) String() string {
	out := fmt.Sprintf("\n=== Scalability Analysis: %s ===\n", sr.Operation)
	out += fmt.Sprintf("Optimal Concurrency: %d goroutines\n", sr.OptimalCores)
	out += fmt.Sprintf("Max Throughput: %.0f ops/sec\n\n", sr.MaxThroughput)

	out += "Throughput by Concurrency:\n"
	// Sort for display
	var concs []int
	for c := range sr.Measurements {
		concs = append(concs, c)
	}
	sort.Ints(concs)

	for _, c := range concs {
		th := sr.Measurements[c]
		out += fmt.Sprintf("  %2d goroutines: %12.0f ops/sec", c, th)
		if c > 1 {
			baseline := sr.Measurements[1]
			scaling := th / baseline
			out += fmt.Sprintf(" (%.1fx scaling)", scaling)
		}
		out += "\n"
	}

	out += fmt.Sprintf("\nScalability: %s\n", sr.Scalability)
	if sr.Linear1To4 > 0 {
		out += fmt.Sprintf("1→4 cores: %.1fx\n", sr.Linear1To4)
	}
	if sr.Linear1To8 > 0 {
		out += fmt.Sprintf("1→8 cores: %.1fx\n", sr.Linear1To8)
	}

	return out
}

// String formats the contention result
func (cr *ContentionResult) String() string {
	out := fmt.Sprintf("\n=== Lock Contention Analysis: %s ===\n", cr.Name)
	out += fmt.Sprintf("Measurements: %d\n", cr.Measurements)
	out += fmt.Sprintf("Avg Wait Time: %.2f µs\n", cr.AvgWaitTime)
	out += fmt.Sprintf("Min Wait Time: %.2f µs\n", cr.MinWaitTime)
	out += fmt.Sprintf("Max Wait Time: %.2f µs\n", cr.MaxWaitTime)
	out += fmt.Sprintf("P50 Wait Time: %.2f µs\n", cr.P50WaitTime)
	out += fmt.Sprintf("P95 Wait Time: %.2f µs\n", cr.P95WaitTime)
	out += fmt.Sprintf("P99 Wait Time: %.2f µs\n", cr.P99WaitTime)
	out += fmt.Sprintf("Contention Score: %.1f/100\n", cr.ContentionScore)

	if cr.ContentionScore < 20 {
		out += "Status: ✅ Low Contention\n"
	} else if cr.ContentionScore < 50 {
		out += "Status: ⚠️  Moderate Contention\n"
	} else {
		out += "Status: ❌ High Contention\n"
	}

	return out
}

// SystemContentionAnalysis analyzes contention at system level
type SystemContentionAnalysis struct {
	goroutineCountBefore int
	goroutineCountAfter  int
	measurementTime      time.Duration
}

// CaptureSystemMetrics captures system-level contention metrics
func CaptureSystemMetrics() *SystemContentionAnalysis {
	return &SystemContentionAnalysis{
		goroutineCountBefore: runtime.NumGoroutine(),
	}
}

// Finalize captures final metrics and computes differences
func (sca *SystemContentionAnalysis) Finalize() {
	sca.goroutineCountAfter = runtime.NumGoroutine()
}

// Helper function
func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
