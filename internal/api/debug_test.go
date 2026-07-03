package api

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/state"
)

func TestDebugHandler(t *testing.T) {
	sm := state.NewStateManager(t.TempDir(), nil)
	mc := metrics.NewMetricsCollector()
	dh := NewDebugHandler(sm, mc)

	// Record some activity
	done := mc.RecordRecoveryStart()
	time.Sleep(5 * time.Millisecond)
	done()

	mc.RecordAuthorityTransition("gen-old", "gen-new")
	mc.SetCurrentState("gen-new", "ready", "Ready", false)

	// Record recovery plan
	plan := &state.RecoveryPlan{
		Service:                 "web",
		Epoch:                   1,
		GeneratedAt:             time.Now(),
		Action:                  state.RecoveryRestoreSingle,
		AuthoritativeGeneration: "gen-new",
		Reason:                  "test recovery",
		DecisionTrace: []string{
			"loaded state",
			"determined authority: gen-new",
		},
	}
	dh.RecordRecoveryPlan(plan)

	// Test DebugMetrics
	var buf bytes.Buffer
	if err := dh.DebugMetrics(&buf); err != nil {
		t.Fatalf("DebugMetrics failed: %v", err)
	}

	var metricsResp map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &metricsResp); err != nil {
		t.Fatalf("unmarshal metrics failed: %v", err)
	}

	if metricsResp["timestamp"] == nil {
		t.Error("timestamp missing from metrics")
	}

	// Test DebugAuthority
	buf.Reset()
	if err := dh.DebugAuthority(&buf); err != nil {
		t.Fatalf("DebugAuthority failed: %v", err)
	}

	var authResp map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &authResp); err != nil {
		t.Fatalf("unmarshal authority failed: %v", err)
	}

	if authResp["current_authority"] != "gen-new" {
		t.Errorf("expected current_authority gen-new, got %v", authResp["current_authority"])
	}

	// Test DebugDecisionTrace
	buf.Reset()
	if err := dh.DebugDecisionTrace(&buf); err != nil {
		t.Fatalf("DebugDecisionTrace failed: %v", err)
	}

	var traceResp map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &traceResp); err != nil {
		t.Fatalf("unmarshal trace failed: %v", err)
	}

	if traceResp["decision_trace"] == nil {
		t.Error("decision_trace missing")
	}

	// Test DebugFullStatus
	buf.Reset()
	if err := dh.DebugFullStatus(&buf); err != nil {
		t.Fatalf("DebugFullStatus failed: %v", err)
	}

	var statusResp map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &statusResp); err != nil {
		t.Fatalf("unmarshal status failed: %v", err)
	}

	if statusResp["authority"] == nil {
		t.Error("authority section missing from status")
	}

	if statusResp["recovery"] == nil {
		t.Error("recovery section missing from status")
	}
}

func TestDebugRecoveryPlanNil(t *testing.T) {
	sm := state.NewStateManager(t.TempDir(), nil)
	mc := metrics.NewMetricsCollector()
	dh := NewDebugHandler(sm, mc)

	var buf bytes.Buffer
	err := dh.DebugRecoveryPlan(&buf)
	if err == nil {
		t.Error("should fail when no recovery plan recorded")
	}
}

func TestDebugInvariants(t *testing.T) {
	sm := state.NewStateManager(t.TempDir(), nil)
	mc := metrics.NewMetricsCollector()
	dh := NewDebugHandler(sm, mc)

	mc.SetCurrentState("gen-test", "committing", "Ready", false)

	var buf bytes.Buffer
	if err := dh.DebugInvariants(&buf); err != nil {
		t.Fatalf("DebugInvariants failed: %v", err)
	}

	var inv map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &inv); err != nil {
		t.Fatalf("unmarshal invariants failed: %v", err)
	}

	if inv["authority_valid"] != true {
		t.Error("authority should be valid")
	}

	if inv["startup_state_valid"] != true {
		t.Error("startup state should be valid")
	}

	if inv["degraded_flag"] != false {
		t.Error("degraded should be false")
	}
}

func TestDebugRolloutState(t *testing.T) {
	sm := state.NewStateManager(t.TempDir(), nil)
	mc := metrics.NewMetricsCollector()
	dh := NewDebugHandler(sm, mc)

	rollout := &state.RolloutState{
		OldGeneration: "gen-old",
		NewGeneration: "gen-new",
		Phase:         state.RolloutDraining,
		Authority:     state.AuthorityTransitioning,
	}
	dh.RecordRolloutState(rollout)

	var buf bytes.Buffer
	if err := dh.DebugRolloutState(&buf); err != nil {
		t.Fatalf("DebugRolloutState failed: %v", err)
	}

	var rolloutResp map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &rolloutResp); err != nil {
		t.Fatalf("unmarshal rollout failed: %v", err)
	}

	if rolloutResp["rollout_state"] == nil {
		t.Error("rollout_state missing")
	}
}

func TestDebugGenerations(t *testing.T) {
	sm := state.NewStateManager(t.TempDir(), nil)
	mc := metrics.NewMetricsCollector()
	dh := NewDebugHandler(sm, mc)

	mc.RecordAuthorityTransition("gen-a", "gen-b")
	mc.RecordAuthorityTransition("gen-b", "gen-c")

	var buf bytes.Buffer
	if err := dh.DebugGenerations(&buf); err != nil {
		t.Fatalf("DebugGenerations failed: %v", err)
	}

	var genResp map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &genResp); err != nil {
		t.Fatalf("unmarshal generations failed: %v", err)
	}

	if genResp["generation_switches"] != float64(2) {
		t.Errorf("expected 2 switches, got %v", genResp["generation_switches"])
	}
}
