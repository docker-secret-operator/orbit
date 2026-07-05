package rollout

import "testing"

// TestPlannedStepsCoversEveryReportedPhase guards the single-source-of-truth
// property PlannedSteps exists for: every Phase Run can report through
// Options.Progress (except PhaseComplete, which signals the end rather than
// describing a step, and PhaseRollingBack, which is a failure branch, not a
// planned step) must have exactly one entry here, in the order Run executes
// them. If this test needs updating, deploySteps in cmd/docker-orbit/deploy.go
// updates automatically — that's the property this test protects.
func TestPlannedStepsCoversEveryReportedPhase(t *testing.T) {
	want := []Phase{
		PhasePulling,
		PhaseScalingUp,
		PhaseHealthCheck,
		PhaseRegistering,
		PhaseSavingState,
		PhaseVerifying,
		PhaseDraining,
		PhaseDeregistering,
	}
	steps := PlannedSteps()
	if len(steps) != len(want) {
		t.Fatalf("PlannedSteps() has %d entries, want %d: %+v", len(steps), len(want), steps)
	}
	for i, s := range steps {
		if s.Phase != want[i] {
			t.Errorf("step %d: phase = %q, want %q", i, s.Phase, want[i])
		}
		if s.Description == "" {
			t.Errorf("step %d (phase %q): empty description", i, s.Phase)
		}
	}
}

func TestPickBackendPortPrefersDPivotBackendEnv(t *testing.T) {
	t.Parallel()

	port, err := pickBackendPort(
		[]string{"443/tcp", "8080/tcp", "3000/tcp"},
		[]string{"FOO=bar", "ORBIT_BACKEND=api:3000"},
	)
	if err != nil {
		t.Fatalf("pickBackendPort returned error: %v", err)
	}
	if port != "3000" {
		t.Fatalf("expected port 3000, got %s", port)
	}
}

func TestPickBackendPortFallsBackToSmallestExposedPort(t *testing.T) {
	t.Parallel()

	port, err := pickBackendPort([]string{"443/tcp", "8080/tcp", "3000/tcp"}, nil)
	if err != nil {
		t.Fatalf("pickBackendPort returned error: %v", err)
	}
	if port != "443" {
		t.Fatalf("expected port 443, got %s", port)
	}
}

func TestPickBackendPortDefaultsTo80WhenNoPorts(t *testing.T) {
	t.Parallel()

	port, err := pickBackendPort(nil, nil)
	if err != nil {
		t.Fatalf("pickBackendPort returned error: %v", err)
	}
	if port != "80" {
		t.Fatalf("expected default port 80, got %s", port)
	}
}
