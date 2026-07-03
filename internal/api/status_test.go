package api

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/proxy"
	"github.com/docker-secret-operator/orbit/internal/state"
)

// listenLocal opens a real TCP listener on an ephemeral port and returns its
// address, so probeTCP has a genuinely reachable target to dial — no mocks.
func listenLocal(t *testing.T) (addr string, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestProbeTCPReachable(t *testing.T) {
	addr, closeFn := listenLocal(t)
	defer closeFn()

	if !probeTCP(context.Background(), addr) {
		t.Errorf("probeTCP(%s) = false, want true (listener is up)", addr)
	}
}

func TestProbeTCPUnreachable(t *testing.T) {
	// Bind and immediately close, so nothing listens on this exact port —
	// giving us a real "connection refused" rather than a fabricated one.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	if probeTCP(context.Background(), addr) {
		t.Errorf("probeTCP(%s) = true, want false (nothing listening)", addr)
	}
}

func TestBuildStatusReportEmptyRegistry(t *testing.T) {
	reg := proxy.NewRegistry()
	report := BuildStatusReport(context.Background(), "web", "1.2.3", proxy.StartupReady, reg, nil)

	if report.Service != "web" || report.RuntimeVersion != "1.2.3" {
		t.Errorf("got Service=%q RuntimeVersion=%q, want web/1.2.3", report.Service, report.RuntimeVersion)
	}
	if report.ProxyStatus != "ready" {
		t.Errorf("ProxyStatus = %q, want ready", report.ProxyStatus)
	}
	if len(report.HealthyBackends) != 0 || len(report.UnhealthyBackends) != 0 {
		t.Errorf("expected no backends, got healthy=%d unhealthy=%d", len(report.HealthyBackends), len(report.UnhealthyBackends))
	}
	if report.DeploymentState != "idle" {
		t.Errorf("DeploymentState = %q, want idle (no DebugHandler, no rollout state)", report.DeploymentState)
	}
}

func TestBuildStatusReportClassifiesBackendsByLiveProbe(t *testing.T) {
	reachableAddr, closeFn := listenLocal(t)
	defer closeFn()

	unreachableLn, _ := net.Listen("tcp", "127.0.0.1:0")
	unreachableAddr := unreachableLn.Addr().String()
	_ = unreachableLn.Close()

	reg := proxy.NewRegistry()
	if err := reg.Add(proxy.Backend{ID: "b1", Addr: reachableAddr}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(proxy.Backend{ID: "b2", Addr: unreachableAddr}); err != nil {
		t.Fatal(err)
	}

	report := BuildStatusReport(context.Background(), "web", "1.0", proxy.StartupReady, reg, nil)

	if len(report.HealthyBackends) != 1 || report.HealthyBackends[0].ID != "b1" {
		t.Errorf("HealthyBackends = %+v, want exactly [b1]", report.HealthyBackends)
	}
	if len(report.UnhealthyBackends) != 1 || report.UnhealthyBackends[0].ID != "b2" {
		t.Errorf("UnhealthyBackends = %+v, want exactly [b2]", report.UnhealthyBackends)
	}
}

func TestBuildStatusReportActiveTrafficTargetExcludesDraining(t *testing.T) {
	addr1, close1 := listenLocal(t)
	defer close1()
	addr2, close2 := listenLocal(t)
	defer close2()

	reg := proxy.NewRegistry()
	if err := reg.Add(proxy.Backend{ID: "active", Addr: addr1}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(proxy.Backend{ID: "draining", Addr: addr2}); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetDraining("draining"); err != nil {
		t.Fatal(err)
	}

	report := BuildStatusReport(context.Background(), "web", "1.0", proxy.StartupReady, reg, nil)

	if len(report.ActiveTrafficTarget) != 1 || report.ActiveTrafficTarget[0] != addr1 {
		t.Errorf("ActiveTrafficTarget = %v, want exactly [%s] (draining backend excluded)", report.ActiveTrafficTarget, addr1)
	}
	// Both backends are still reported for health, though — draining doesn't mean gone.
	if len(report.HealthyBackends) != 2 {
		t.Errorf("HealthyBackends = %+v, want 2 (draining backend still health-checked)", report.HealthyBackends)
	}
}

func TestBuildStatusReportUsesDebugHandlerGenerationState(t *testing.T) {
	reg := proxy.NewRegistry()
	mc := metrics.NewMetricsCollector()
	dh := NewDebugHandler(nil, mc)

	dh.RecordActiveGenState(&state.ActiveGenerationState{ActiveGeneration: "web-3"})
	dh.RecordRolloutState(&state.RolloutState{OldGeneration: "web-2", NewGeneration: "web-3", Phase: state.RolloutDraining})

	report := BuildStatusReport(context.Background(), "web", "1.0", proxy.StartupReady, reg, dh)

	if report.CurrentGeneration != "web-3" {
		t.Errorf("CurrentGeneration = %q, want web-3", report.CurrentGeneration)
	}
	if report.PreviousGeneration != "web-2" {
		t.Errorf("PreviousGeneration = %q, want web-2", report.PreviousGeneration)
	}
	if report.DeploymentState != "draining" {
		t.Errorf("DeploymentState = %q, want draining", report.DeploymentState)
	}
}

func TestBuildStatusReportRecoverySnapshotReflectsMetricsCollector(t *testing.T) {
	reg := proxy.NewRegistry()
	mc := metrics.NewMetricsCollector()
	dh := NewDebugHandler(nil, mc)

	done := mc.RecordRecoveryStart()
	time.Sleep(2 * time.Millisecond)
	done()
	mc.RecordRecoveryFailure()

	report := BuildStatusReport(context.Background(), "web", "1.0", proxy.StartupReady, reg, dh)

	if report.Recovery.RecoveryCount != 1 {
		t.Errorf("Recovery.RecoveryCount = %d, want 1", report.Recovery.RecoveryCount)
	}
	if report.Recovery.RecoveryFailureCount != 1 {
		t.Errorf("Recovery.RecoveryFailureCount = %d, want 1", report.Recovery.RecoveryFailureCount)
	}
	if report.Recovery.LastRecoveryTime.IsZero() {
		t.Error("Recovery.LastRecoveryTime is zero, want a real timestamp")
	}
}

func TestBuildStatusReportFallsBackToMetricsAuthorityWhenNoActiveGenState(t *testing.T) {
	reg := proxy.NewRegistry()
	mc := metrics.NewMetricsCollector()
	dh := NewDebugHandler(nil, mc)

	// No RecordActiveGenState call — simulates a proxy that hasn't loaded
	// persisted state yet but has recorded an authority transition.
	mc.RecordAuthorityTransition("", "web-1")

	report := BuildStatusReport(context.Background(), "web", "1.0", proxy.StartupReady, reg, dh)

	if report.CurrentGeneration != "web-1" {
		t.Errorf("CurrentGeneration = %q, want fallback to MetricsCollector's CurrentAuthority (web-1)", report.CurrentGeneration)
	}
}

func TestStartupStateStringMatchesUnderlyingValue(t *testing.T) {
	cases := []struct {
		in   proxy.StartupState
		want string
	}{
		{proxy.StartupReady, "ready"},
		{proxy.StartupDegraded, "degraded"},
		{proxy.StartupFailed, "failed"},
		{proxy.StartupRecovering, "recovering"},
		{proxy.StartupStarting, "starting"},
	}
	for _, c := range cases {
		if got := StartupStateString(c.in); got != c.want {
			t.Errorf("StartupStateString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
