package chaos

import (
	"context"
	"testing"
	"time"
)

// TestChaosFramework runs the full 25-scenario chaos test suite
func TestChaosFramework(t *testing.T) {
	harness := NewChaosHarness(t)
	defer harness.Close()

	scenarios := AllScenarios()

	// Run all scenarios
	start := time.Now()
	for _, scenario := range scenarios {
		result := harness.RunScenario(scenario)

		// Log result
		status := "✓"
		if !result.Success {
			status = "✗"
		}

		t.Logf("%s Scenario %d: %s (%s) - %v",
			status,
			scenario.Tier,
			scenario.Name,
			result.Duration,
			result.Error)

		// Fail test if scenario failed
		if !result.Success {
			t.Errorf("Scenario failed: %s - %v", scenario.Name, result.Error)
		}

		// Warn if invariants violated
		if len(result.InvariantsViolated) > 0 {
			t.Logf("  WARNING: Invariants violated: %v", result.InvariantsViolated)
		}
	}

	totalTime := time.Since(start)

	// Summary
	passed, failed, _ := harness.Summary()
	totalScenarios := len(scenarios)

	t.Logf("\n=== CHAOS TEST SUMMARY ===")
	t.Logf("Total Scenarios: %d", totalScenarios)
	t.Logf("Passed: %d", passed)
	t.Logf("Failed: %d", failed)
	t.Logf("Total Time: %v", totalTime)
	t.Logf("Average Time: %v", totalTime/time.Duration(totalScenarios))

	// Breakdown by tier
	tier1Count := 0
	tier2Count := 0
	tier3Count := 0

	for _, scenario := range scenarios {
		switch scenario.Tier {
		case 1:
			tier1Count++
		case 2:
			tier2Count++
		case 3:
			tier3Count++
		}
	}

	t.Logf("\nScenarios by Tier:")
	t.Logf("  Tier 1 (fast):   %d scenarios", tier1Count)
	t.Logf("  Tier 2 (medium): %d scenarios", tier2Count)
	t.Logf("  Tier 3 (long):   %d scenarios", tier3Count)

	// Detailed results
	t.Logf("\n=== DETAILED RESULTS ===")
	results := harness.Results()
	for _, result := range results {
		t.Logf("\n%s (Tier %d):", result.Scenario, result.Tier)
		t.Logf("  Status: %v", result.Success)
		t.Logf("  Duration: %v", result.Duration)
		t.Logf("  Metrics Before: %s", result.MetricsBeforeStr)
		t.Logf("  Metrics After:  %s", result.MetricsAfterStr)
		if result.Error != "" {
			t.Logf("  Error: %s", result.Error)
		}
		if len(result.InvariantsViolated) > 0 {
			t.Logf("  Invariants Violated: %v", result.InvariantsViolated)
		}
	}

	// Fail test if any scenario failed
	if failed > 0 {
		t.Fatalf("Chaos test suite failed: %d/%d scenarios failed", failed, totalScenarios)
	}
}

// TestChaosFrameworkTier1 runs only Tier 1 (fast) scenarios
func TestChaosFrameworkTier1(t *testing.T) {
	harness := NewChaosHarness(t)
	defer harness.Close()

	scenarios := AllScenarios()

	// Filter to Tier 1 only
	var tier1Scenarios []*FailureScenario
	for _, s := range scenarios {
		if s.Tier == 1 {
			tier1Scenarios = append(tier1Scenarios, s)
		}
	}

	t.Logf("Running %d Tier 1 (fast) scenarios...", len(tier1Scenarios))

	start := time.Now()
	passed := 0
	failed := 0

	for _, scenario := range tier1Scenarios {
		result := harness.RunScenario(scenario)

		if result.Success {
			passed++
			t.Logf("✓ %s", scenario.Name)
		} else {
			failed++
			t.Logf("✗ %s: %v", scenario.Name, result.Error)
		}
	}

	totalTime := time.Since(start)

	t.Logf("\nTier 1 Results: %d passed, %d failed in %v", passed, failed, totalTime)

	if failed > 0 {
		t.Fatalf("Tier 1 suite failed: %d/%d scenarios failed", failed, len(tier1Scenarios))
	}
}

// TestChaosFrameworkTier2 runs only Tier 2 (medium) scenarios
func TestChaosFrameworkTier2(t *testing.T) {
	harness := NewChaosHarness(t)
	defer harness.Close()

	scenarios := AllScenarios()

	// Filter to Tier 2 only
	var tier2Scenarios []*FailureScenario
	for _, s := range scenarios {
		if s.Tier == 2 {
			tier2Scenarios = append(tier2Scenarios, s)
		}
	}

	t.Logf("Running %d Tier 2 (medium) scenarios...", len(tier2Scenarios))

	start := time.Now()
	passed := 0
	failed := 0

	for _, scenario := range tier2Scenarios {
		result := harness.RunScenario(scenario)

		if result.Success {
			passed++
			t.Logf("✓ %s", scenario.Name)
		} else {
			failed++
			t.Logf("✗ %s: %v", scenario.Name, result.Error)
		}
	}

	totalTime := time.Since(start)

	t.Logf("\nTier 2 Results: %d passed, %d failed in %v", passed, failed, totalTime)

	if failed > 0 {
		t.Fatalf("Tier 2 suite failed: %d/%d scenarios failed", failed, len(tier2Scenarios))
	}
}

// TestChaosFrameworkTier3 runs only Tier 3 (long) scenarios
func TestChaosFrameworkTier3(t *testing.T) {
	harness := NewChaosHarness(t)
	defer harness.Close()

	scenarios := AllScenarios()

	// Filter to Tier 3 only
	var tier3Scenarios []*FailureScenario
	for _, s := range scenarios {
		if s.Tier == 3 {
			tier3Scenarios = append(tier3Scenarios, s)
		}
	}

	t.Logf("Running %d Tier 3 (long) scenarios...", len(tier3Scenarios))

	start := time.Now()
	passed := 0
	failed := 0

	for _, scenario := range tier3Scenarios {
		result := harness.RunScenario(scenario)

		if result.Success {
			passed++
			t.Logf("✓ %s", scenario.Name)
		} else {
			failed++
			t.Logf("✗ %s: %v", scenario.Name, result.Error)
		}
	}

	totalTime := time.Since(start)

	t.Logf("\nTier 3 Results: %d passed, %d failed in %v", passed, failed, totalTime)

	if failed > 0 {
		t.Fatalf("Tier 3 suite failed: %d/%d scenarios failed", failed, len(tier3Scenarios))
	}
}

// BenchmarkChaosFramework measures performance of full suite
func BenchmarkChaosFramework(b *testing.B) {
	for i := 0; i < b.N; i++ {
		t := &testing.T{}
		harness := NewChaosHarness(t)

		scenarios := AllScenarios()
		for _, scenario := range scenarios {
			harness.RunScenario(scenario)
		}

		harness.Close()
	}
}

// Example demonstrates how to run custom scenarios
func Example() {
	// This shows how to extend the framework with custom scenarios
	customScenario := &FailureScenario{
		Name:     "CustomScenario",
		Tier:     1,
		Duration: 500 * time.Millisecond,
		Run: func(ctx context.Context, h *ChaosHarness) error {
			// Custom failure logic here
			return nil
		},
	}

	// Custom scenario would be executed like:
	// result := harness.RunScenario(customScenario)

	_ = customScenario // silence unused
}
