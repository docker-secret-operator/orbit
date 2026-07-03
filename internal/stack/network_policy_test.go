package stack

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNewBlastRadiusAnalyzer(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		Name:                 "test",
		MaxFailedServices:    2,
		MaxFailurePercentage: 25.0,
	}
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)

	if bra == nil {
		t.Fatal("NewBlastRadiusAnalyzer returned nil")
	}
	if bra.policy != policy {
		t.Error("policy not set correctly")
	}
}

func TestNewBlastRadiusAnalyzerDefaultPolicy(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	bra := NewBlastRadiusAnalyzer(nil, rollout, log)

	if bra.policy == nil {
		t.Fatal("default policy not created")
	}
	if bra.policy.MaxFailedServices != 2 {
		t.Errorf("max failed services = %d, want 2", bra.policy.MaxFailedServices)
	}
}

func TestRecordFailure(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		MaxFailedServices:    2,
		MaxFailurePercentage: 50.0,
		IsolationMode:        IsolationModeNone,
	}
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	rollout.state.ServiceStates["service1"] = &ServiceRolloutState{Status: StatusCompleted}
	rollout.state.ServiceStates["service2"] = &ServiceRolloutState{Status: StatusCompleted}

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)

	result, err := bra.RecordFailure("service1")
	if err != nil {
		t.Fatalf("RecordFailure failed: %v", err)
	}

	if result.FailedService != "service1" {
		t.Errorf("failed service = %s, want service1", result.FailedService)
	}
}

func TestAnalyzeBlastRadius(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		MaxFailedServices:    1,
		MaxFailurePercentage: 25.0,
	}
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	rollout.state.ServiceStates["service1"] = &ServiceRolloutState{Status: StatusCompleted}
	rollout.state.ServiceStates["service2"] = &ServiceRolloutState{Status: StatusCompleted}
	rollout.state.ServiceStates["service3"] = &ServiceRolloutState{Status: StatusCompleted}
	rollout.state.ServiceStates["service4"] = &ServiceRolloutState{Status: StatusCompleted}

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)
	bra.failedServices["service1"] = time.Now()

	result := bra.analyzeBlastRadius("service1")

	if result.FailedService != "service1" {
		t.Errorf("failed service = %s, want service1", result.FailedService)
	}
	if result.FailurePercentage <= 0 {
		t.Error("failure percentage not calculated")
	}
}

func TestBlastRadiusThresholdExceeded(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		MaxFailedServices:    1,
		MaxFailurePercentage: 20.0,
		IsolationMode:        IsolationModeNone,
	}
	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: map[string]*ServiceRolloutState{
				"service1": {Status: StatusCompleted},
				"service2": {Status: StatusCompleted},
				"service3": {Status: StatusCompleted},
				"service4": {Status: StatusCompleted},
				"service5": {Status: StatusCompleted},
			},
			Graph: nil,
		},
	}

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)

	bra.failedServices["service1"] = time.Now()
	bra.failedServices["service2"] = time.Now()

	result := bra.analyzeBlastRadius("service2")

	if !result.ExceedsThreshold {
		t.Error("blast radius should exceed threshold")
	}
}

func TestGetAllDependentsNoGraph(t *testing.T) {
	log := zap.NewNop()
	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: make(map[string]*ServiceRolloutState),
			Graph:         nil,
		},
	}

	bra := NewBlastRadiusAnalyzer(nil, rollout, log)

	dependents := bra.getAllDependents("service1", 3)
	if len(dependents) != 0 {
		t.Errorf("dependents = %v, want empty", dependents)
	}
}

func TestGetAllDependentsZeroDepth(t *testing.T) {
	log := zap.NewNop()
	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: make(map[string]*ServiceRolloutState),
			Graph: &DependencyGraph{
				Services: make(map[string]*ServiceDependency),
			},
		},
	}

	bra := NewBlastRadiusAnalyzer(nil, rollout, log)

	dependents := bra.getAllDependents("service1", 0)
	if len(dependents) != 0 {
		t.Errorf("dependents = %v, want empty", dependents)
	}
}

func TestQuarantineService(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		QuarantineTimeout:    5 * time.Minute,
		IsolationMode:        IsolationModeQuarantine,
		MaxFailedServices:    1,
		MaxFailurePercentage: 25.0,
	}
	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: map[string]*ServiceRolloutState{
				"service1": {Status: StatusCompleted},
			},
		},
	}

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)

	err := bra.quarantineService("service1")
	if err != nil {
		t.Fatalf("quarantineService failed: %v", err)
	}

	if !bra.IsServiceQuarantined("service1") {
		t.Error("service not quarantined")
	}
}

func TestIsServiceQuarantined(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		QuarantineTimeout: 100 * time.Millisecond,
	}
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)

	bra.mu.Lock()
	bra.quarantinedServices["service1"] = time.Now()
	bra.mu.Unlock()

	if !bra.IsServiceQuarantined("service1") {
		t.Error("service should be quarantined")
	}

	time.Sleep(150 * time.Millisecond)
	if bra.IsServiceQuarantined("service1") {
		t.Error("service quarantine should expire")
	}
}

func TestReleaseQuarantine(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	bra := NewBlastRadiusAnalyzer(nil, rollout, log)

	bra.mu.Lock()
	bra.quarantinedServices["service1"] = time.Now()
	bra.mu.Unlock()

	bra.ReleaseQuarantine("service1")

	if bra.IsServiceQuarantined("service1") {
		t.Error("service still quarantined after release")
	}
}

func TestKillService(t *testing.T) {
	log := zap.NewNop()
	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: map[string]*ServiceRolloutState{
				"service1": {
					NewContainer: "container123",
					Status:       StatusCompleted,
				},
			},
		},
	}

	bra := NewBlastRadiusAnalyzer(nil, rollout, log)

	err := bra.killService("service1")
	if err != nil {
		t.Fatalf("killService failed: %v", err)
	}
}

func TestKillServiceNotFound(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	bra := NewBlastRadiusAnalyzer(nil, rollout, log)

	err := bra.killService("missing")
	if err == nil {
		t.Error("killService should error on missing service")
	}
}

func TestRollbackService(t *testing.T) {
	log := zap.NewNop()
	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: map[string]*ServiceRolloutState{
				"service1": {
					NewContainer: "new123",
					OldContainer: "old456",
					Status:       StatusCompleted,
				},
			},
		},
	}

	bra := NewBlastRadiusAnalyzer(nil, rollout, log)

	err := bra.rollbackService("service1")
	if err != nil {
		t.Fatalf("rollbackService failed: %v", err)
	}

	state := rollout.state.ServiceStates["service1"]
	if state.NewContainer != "old456" || state.OldContainer != "new123" {
		t.Error("containers not swapped correctly")
	}
}

func TestRollbackServiceNotFound(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	bra := NewBlastRadiusAnalyzer(nil, rollout, log)

	err := bra.rollbackService("missing")
	if err == nil {
		t.Error("rollbackService should error on missing service")
	}
}

func TestGetStatus(t *testing.T) {
	log := zap.NewNop()
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	bra := NewBlastRadiusAnalyzer(nil, rollout, log)

	bra.mu.Lock()
	bra.failedServices["service1"] = time.Now()
	bra.failedServices["service2"] = time.Now()
	bra.quarantinedServices["service2"] = time.Now()
	bra.mu.Unlock()

	status := bra.GetStatus()

	if status.TotalFailed != 2 {
		t.Errorf("total failed = %d, want 2", status.TotalFailed)
	}
	if status.TotalQuarantined != 1 {
		t.Errorf("total quarantined = %d, want 1", status.TotalQuarantined)
	}
}

func TestContainFailureQuarantine(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		IsolationMode: IsolationModeQuarantine,
	}
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)

	result := BlastRadiusResult{FailedService: "service1"}
	err := bra.containFailure("service1", result)
	if err != nil {
		t.Errorf("containFailure with quarantine failed: %v", err)
	}
}

func TestContainFailureKill(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		IsolationMode: IsolationModeKill,
	}
	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: map[string]*ServiceRolloutState{
				"service1": {NewContainer: "cont123"},
			},
		},
	}

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)

	result := BlastRadiusResult{FailedService: "service1"}
	err := bra.containFailure("service1", result)
	if err != nil {
		t.Errorf("containFailure with kill failed: %v", err)
	}
}

func TestContainFailureRollback(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		IsolationMode: IsolationModeRollback,
	}
	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: map[string]*ServiceRolloutState{
				"service1": {
					NewContainer: "new123",
					OldContainer: "old456",
				},
			},
		},
	}

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)

	result := BlastRadiusResult{FailedService: "service1"}
	err := bra.containFailure("service1", result)
	if err != nil {
		t.Errorf("containFailure with rollback failed: %v", err)
	}
}

func TestContainFailureNone(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		IsolationMode: IsolationModeNone,
	}
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)

	result := BlastRadiusResult{FailedService: "service1"}
	err := bra.containFailure("service1", result)
	if err != nil {
		t.Errorf("containFailure with none should not error: %v", err)
	}
}

func TestContainFailureUnknownMode(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		IsolationMode: IsolationMode("unknown"),
	}
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, zap.NewNop())

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)

	result := BlastRadiusResult{FailedService: "service1"}
	err := bra.containFailure("service1", result)
	if err == nil {
		t.Error("containFailure should error on unknown mode")
	}
}

func TestRecordFailureWithContainment(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		MaxFailedServices:     1,
		MaxFailurePercentage:  50.0,
		IsolationMode:         IsolationModeNone,
		RollbackOnBlastRadius: true,
	}
	config := &StackRolloutConfig{}
	rollout := NewStackRollout(config, log)
	rollout.state.ServiceStates["service1"] = &ServiceRolloutState{Status: StatusCompleted}
	rollout.state.ServiceStates["service2"] = &ServiceRolloutState{Status: StatusCompleted}

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)

	bra.failedServices["service1"] = time.Now()

	result, err := bra.RecordFailure("service2")
	if err != nil {
		t.Fatalf("RecordFailure failed: %v", err)
	}

	if !result.ExceedsThreshold {
		t.Error("should have exceeded threshold")
	}
}

func TestBlastRadiusResultReason(t *testing.T) {
	log := zap.NewNop()
	policy := &NetworkPolicy{
		MaxFailedServices:    1,
		MaxFailurePercentage: 25.0,
	}
	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: map[string]*ServiceRolloutState{
				"s1": {}, "s2": {}, "s3": {}, "s4": {},
			},
			Graph: nil,
		},
	}

	bra := NewBlastRadiusAnalyzer(policy, rollout, log)
	bra.failedServices["s1"] = time.Now()
	bra.failedServices["s2"] = time.Now()

	result := bra.analyzeBlastRadius("s2")

	if result.ExceedsThreshold && result.Reason == "" {
		t.Error("reason should be set when threshold exceeded")
	}
}

func TestRecordFailureConcurrency(t *testing.T) {
	log := zap.NewNop()
	rollout := &StackRollout{
		state: &StackRolloutState{
			ServiceStates: map[string]*ServiceRolloutState{
				"s1": {}, "s2": {}, "s3": {},
			},
		},
	}

	bra := NewBlastRadiusAnalyzer(nil, rollout, log)

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(idx int) {
			services := []string{"s1", "s2", "s3"}
			bra.RecordFailure(services[idx%3])
			done <- struct{}{}
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	status := bra.GetStatus()
	if status.TotalFailed == 0 {
		t.Error("failures not recorded")
	}
}
