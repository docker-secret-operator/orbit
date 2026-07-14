package main

import (
	"testing"
	"time"

	"github.com/docker-secret-operator/orbit/internal/proxy"
)

// TestBuildGenerationInventory_UsesRealContainerCreationTime reproduces the
// go-live audit's finding H4: buildGenerationInventory stamped every
// generation's CreatedAt/ContinuousHealthyStart with the same process-local
// time.Now() instead of the backend's real Docker creation time. Whenever
// recovery must infer authority among 2+ simultaneously-healthy generations
// (no persisted state), the "longest healthy uptime" tie-break had nothing
// real to compare and fell through to Go's randomized map iteration order.
// A real, older creation time must produce a real, older CreatedAt.
func TestBuildGenerationInventory_UsesRealContainerCreationTime(t *testing.T) {
	older := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	newer := time.Now().Add(-2 * time.Minute).Truncate(time.Second)

	result := &proxy.RecoveryResult{
		Backends: []proxy.BackendHealth{
			{ID: "old-1", Addr: "10.0.0.1:80", Generation: "gen-old", Status: proxy.HealthHealthy},
			{ID: "new-1", Addr: "10.0.0.2:80", Generation: "gen-new", Status: proxy.HealthHealthy},
		},
		BackendCreatedAt: map[string]time.Time{
			"old-1": older,
			"new-1": newer,
		},
	}

	inv := buildGenerationInventory("web", result, nil)

	got := inv.GenerationStates["gen-old"].CreatedAt
	if !got.Equal(older) {
		t.Errorf("gen-old CreatedAt = %v, want real creation time %v (not time.Now())", got, older)
	}
	gotNew := inv.GenerationStates["gen-new"].CreatedAt
	if !gotNew.Equal(newer) {
		t.Errorf("gen-new CreatedAt = %v, want real creation time %v (not time.Now())", gotNew, newer)
	}
	if !got.Before(gotNew) {
		t.Errorf("gen-old (%v) should be provably older than gen-new (%v) for deterministic tie-breaking, but wasn't", got, gotNew)
	}

	oldCHS := inv.GenerationStates["gen-old"].ContinuousHealthyStart
	if !oldCHS.Equal(older) {
		t.Errorf("gen-old ContinuousHealthyStart = %v, want %v", oldCHS, older)
	}
}
