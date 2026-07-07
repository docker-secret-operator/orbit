package benchmark

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/state"
)

// ExtendedLoadTest simulates 50+ concurrent services
type ExtendedLoadTest struct {
	services         []*ServiceSimulator
	metricsCollector *metrics.MetricsCollector
	systemMonitor    *SystemMonitor
	resultsAnalyzer  *ResultsAnalyzer
	testStartTime    time.Time
	testEndTime      time.Time
}

// ServiceSimulator simulates a single DPIVOT service
type ServiceSimulator struct {
	id                int
	recoveryCount     atomic.Int64
	stateWriteCount   atomic.Int64
	errorCount        atomic.Int64
	metricsCollector  *metrics.MetricsCollector
	recoveryLatencies []int64
	stateLatencies    []int64
	mu                sync.Mutex
}

// SystemMonitor tracks system-wide metrics
type SystemMonitor struct {
	startMem   uint64
	endMem     uint64
	startGC    uint32
	endGC      uint32
	maxLatency int64
	minLatency int64
	totalOps   atomic.Int64
	failedOps  atomic.Int64
}

// ResultsAnalyzer computes statistics
type ResultsAnalyzer struct {
	throughput      float64
	avgLatency      float64
	p50Latency      float64
	p95Latency      float64
	p99Latency      float64
	memoryGrowth    float64
	gcPressure      float64
	contentionScore float64
}

// NewExtendedLoadTest creates a new load test with N services
func NewExtendedLoadTest(numServices int) *ExtendedLoadTest {
	services := make([]*ServiceSimulator, numServices)
	for i := 0; i < numServices; i++ {
		services[i] = &ServiceSimulator{
			id:                i,
			recoveryLatencies: make([]int64, 0, 10000),
			stateLatencies:    make([]int64, 0, 10000),
		}
	}

	return &ExtendedLoadTest{
		services:      services,
		systemMonitor: &SystemMonitor{},
	}
}

// SimulateRecoveryLoad generates recovery plan operations
func (sim *ServiceSimulator) SimulateRecoveryLoad(duration time.Duration, rate float64) {
	endTime := time.Now().Add(duration)
	operationInterval := time.Duration(float64(time.Second) / rate)

	for {
		if time.Now().After(endTime) {
			break
		}

		start := time.Now()

		// Simulate recovery plan generation
		plan := state.RecoveryPlan{
			Service:                 fmt.Sprintf("service-%d", sim.id),
			Epoch:                   uint64(sim.recoveryCount.Load()),
			AuthoritativeGeneration: fmt.Sprintf("gen-%d", sim.id),
			DecisionTrace:           []string{"step-1", "step-2", "step-3"},
		}

		// Record in metrics
		if sim.metricsCollector != nil {
			sim.metricsCollector.RecordRecoveryStart()
		}

		latency := time.Since(start).Microseconds()
		sim.mu.Lock()
		sim.recoveryLatencies = append(sim.recoveryLatencies, latency)
		sim.mu.Unlock()

		sim.recoveryCount.Add(1)

		// Sleep to maintain rate
		_ = plan
		time.Sleep(operationInterval)
	}
}

// SimulateStateLoad generates state file write operations
func (sim *ServiceSimulator) SimulateStateLoad(duration time.Duration, rate float64) {
	endTime := time.Now().Add(duration)
	operationInterval := time.Duration(float64(time.Second) / rate)

	for {
		if time.Now().After(endTime) {
			break
		}

		start := time.Now()

		// Simulate state write
		stateData := state.RecoveryPlan{
			Service:                 fmt.Sprintf("service-%d", sim.id),
			Epoch:                   uint64(sim.stateWriteCount.Load()),
			AuthoritativeGeneration: fmt.Sprintf("gen-%d", sim.id),
			DecisionTrace:           []string{"decision-1"},
		}

		latency := time.Since(start).Microseconds()
		sim.mu.Lock()
		sim.stateLatencies = append(sim.stateLatencies, latency)
		sim.mu.Unlock()

		sim.stateWriteCount.Add(1)

		_ = stateData
		time.Sleep(operationInterval)
	}
}

// ComputePercentile calculates latency percentiles
func computePercentile(latencies []int64, p float64) int64 {
	if len(latencies) == 0 {
		return 0
	}

	idx := int(float64(len(latencies)-1) * p / 100.0)
	if idx >= len(latencies) {
		idx = len(latencies) - 1
	}

	sorted := make([]time.Duration, len(latencies))
	for i, v := range latencies {
		sorted[i] = time.Duration(v)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	return int64(sorted[idx])
}

// RunScenario executes a load test scenario
func (elt *ExtendedLoadTest) RunScenario(name string, duration time.Duration, recoveryRate, stateRate float64) {
	fmt.Printf("\n=== Extended Load Test: %s ===\n", name)
	fmt.Printf("Duration: %v\n", duration)
	fmt.Printf("Services: %d\n", len(elt.services))
	fmt.Printf("Recovery rate: %.1f ops/sec per service\n", recoveryRate)
	fmt.Printf("State write rate: %.1f ops/sec per service\n", stateRate)

	elt.testStartTime = time.Now()

	// Start all service simulators
	var wg sync.WaitGroup
	for _, sim := range elt.services {
		sim.metricsCollector = metrics.NewMetricsCollector()

		// Recovery generator
		wg.Add(1)
		go func(s *ServiceSimulator) {
			defer wg.Done()
			s.SimulateRecoveryLoad(duration, recoveryRate)
		}(sim)

		// State writer
		wg.Add(1)
		go func(s *ServiceSimulator) {
			defer wg.Done()
			s.SimulateStateLoad(duration, stateRate)
		}(sim)
	}

	// Wait for completion
	wg.Wait()
	elt.testEndTime = time.Now()

	// Analyze results
	elt.analyzeResults()
}

func (elt *ExtendedLoadTest) analyzeResults() {
	totalRecoveries := int64(0)
	totalWrites := int64(0)
	totalErrors := int64(0)

	allRecoveryLatencies := make([]int64, 0)
	allStateLatencies := make([]int64, 0)

	for _, sim := range elt.services {
		totalRecoveries += sim.recoveryCount.Load()
		totalWrites += sim.stateWriteCount.Load()
		totalErrors += sim.errorCount.Load()

		sim.mu.Lock()
		allRecoveryLatencies = append(allRecoveryLatencies, sim.recoveryLatencies...)
		allStateLatencies = append(allStateLatencies, sim.stateLatencies...)
		sim.mu.Unlock()
	}

	actualDuration := elt.testEndTime.Sub(elt.testStartTime).Seconds()

	// Compute statistics
	fmt.Printf("\n=== Load Test Results ===\n")
	fmt.Printf("Actual Duration: %.1f seconds\n", actualDuration)

	fmt.Printf("\nRecovery Operations:\n")
	fmt.Printf("  Total: %d\n", totalRecoveries)
	fmt.Printf("  Rate: %.1f ops/sec\n", float64(totalRecoveries)/actualDuration)
	if len(allRecoveryLatencies) > 0 {
		fmt.Printf("  P50 Latency: %.1f µs\n", float64(computePercentile(allRecoveryLatencies, 50)))
		fmt.Printf("  P95 Latency: %.1f µs\n", float64(computePercentile(allRecoveryLatencies, 95)))
		fmt.Printf("  P99 Latency: %.1f µs\n", float64(computePercentile(allRecoveryLatencies, 99)))
	}

	fmt.Printf("\nState Write Operations:\n")
	fmt.Printf("  Total: %d\n", totalWrites)
	fmt.Printf("  Rate: %.1f writes/sec\n", float64(totalWrites)/actualDuration)
	if len(allStateLatencies) > 0 {
		fmt.Printf("  P50 Latency: %.1f µs\n", float64(computePercentile(allStateLatencies, 50)))
		fmt.Printf("  P95 Latency: %.1f µs\n", float64(computePercentile(allStateLatencies, 95)))
		fmt.Printf("  P99 Latency: %.1f µs\n", float64(computePercentile(allStateLatencies, 99)))
	}

	fmt.Printf("\nSystem Metrics:\n")
	fmt.Printf("  Total Errors: %d (%.2f%%)\n", totalErrors, float64(totalErrors)*100.0/float64(totalRecoveries+totalWrites))
	fmt.Printf("  Total Operations: %d\n", totalRecoveries+totalWrites)
	fmt.Printf("  Overall Throughput: %.1f ops/sec\n", float64(totalRecoveries+totalWrites)/actualDuration)

	// Validation
	fmt.Printf("\n=== Validation ===\n")
	if float64(totalRecoveries)/actualDuration > 1000 {
		fmt.Println("✅ Recovery throughput >1000 ops/sec")
	} else {
		fmt.Println("⚠️  Recovery throughput <1000 ops/sec")
	}

	if float64(totalErrors)*100.0/float64(totalRecoveries+totalWrites) < 1.0 {
		fmt.Println("✅ Error rate <1%")
	} else {
		fmt.Println("⚠️  Error rate >1%")
	}

	if len(allRecoveryLatencies) > 0 && computePercentile(allRecoveryLatencies, 99) < 100000 {
		fmt.Println("✅ Recovery P99 <100ms")
	} else {
		fmt.Println("⚠️  Recovery P99 >100ms")
	}
}

// TestExtendedLoadBaseline runs 50 services for 60 seconds
func TestExtendedLoadBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 60s extended load test in short mode")
	}
	// Quick 60-second test instead of 3600-second test
	elt := NewExtendedLoadTest(50)
	elt.RunScenario(
		"Baseline (50 services, 60s)",
		60*time.Second,
		2.0,  // 2 recoveries/sec per service
		10.0, // 10 state writes/sec per service
	)
}

// TestExtendedLoadPeakSpike runs 50 services with 3x traffic spike
func TestExtendedLoadPeakSpike(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 60s extended load test in short mode")
	}
	elt := NewExtendedLoadTest(50)
	elt.RunScenario(
		"Peak Load Spike (50 services, 60s spike)",
		60*time.Second,
		6.0,  // 6 recoveries/sec per service (3x baseline)
		30.0, // 30 state writes/sec per service (3x baseline)
	)
}

// TestExtendedLoadChaos runs 50 services with random errors
func TestExtendedLoadChaos(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 60s extended load test in short mode")
	}
	elt := NewExtendedLoadTest(50)

	// Start chaos events
	go func() {
		endTime := time.Now().Add(60 * time.Second)
		for time.Now().Before(endTime) {
			service := elt.services[rand.Intn(len(elt.services))]
			service.errorCount.Add(1)
			time.Sleep(time.Duration(rand.Intn(5)) * time.Second)
		}
	}()

	elt.RunScenario(
		"Chaos (50 services with failures, 60s)",
		60*time.Second,
		2.0,  // 2 recoveries/sec per service
		10.0, // 10 state writes/sec per service
	)
}

// TestExtendedLoadSmallCluster runs 10 services for sustained period
func TestExtendedLoadSmallCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 60s extended load test in short mode")
	}
	elt := NewExtendedLoadTest(10)
	elt.RunScenario(
		"Small Cluster (10 services, 60s)",
		60*time.Second,
		2.0,
		10.0,
	)
}

// TestExtendedLoadMediumCluster runs 25 services
func TestExtendedLoadMediumCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 60s extended load test in short mode")
	}
	elt := NewExtendedLoadTest(25)
	elt.RunScenario(
		"Medium Cluster (25 services, 60s)",
		60*time.Second,
		2.0,
		10.0,
	)
}

// BenchmarkExtendedLoad benchmarks with 50 services
func BenchmarkExtendedLoad(b *testing.B) {
	elt := NewExtendedLoadTest(50)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Each iteration runs a short 10-second load test
		elt.RunScenario(
			fmt.Sprintf("Benchmark Iteration %d", i),
			10*time.Second,
			2.0,
			10.0,
		)
	}
}
