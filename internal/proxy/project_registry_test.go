package proxy_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/docker-secret-operator/orbit/internal/proxy"
)

func TestProjectRegistry_RegisterAndFor(t *testing.T) {
	pr := proxy.NewProjectRegistry()
	reg := proxy.NewRegistry()

	pr.Register("web", reg)

	got, ok := pr.For("web")
	if !ok {
		t.Fatal("expected web to be found")
	}
	if got != reg {
		t.Fatal("For must return the exact *Registry instance originally registered, not a copy")
	}
}

func TestProjectRegistry_ForMissingService(t *testing.T) {
	pr := proxy.NewProjectRegistry()

	got, ok := pr.For("does-not-exist")
	if ok {
		t.Fatal("expected ok=false for an unregistered service")
	}
	if got != nil {
		t.Fatal("expected a nil Registry for an unregistered service")
	}
}

func TestProjectRegistry_Replace(t *testing.T) {
	pr := proxy.NewProjectRegistry()
	regA := proxy.NewRegistry()
	regB := proxy.NewRegistry()

	pr.Register("web", regA)
	pr.Register("web", regB) // re-register — must replace, not merge or error

	got, ok := pr.For("web")
	if !ok {
		t.Fatal("expected web to still be found after replacement")
	}
	if got != regB {
		t.Fatal("For must return the most recently registered Registry after a replacement")
	}
	if got == regA {
		t.Fatal("the old Registry instance must no longer be reachable through ProjectRegistry")
	}
}

func TestProjectRegistry_Remove(t *testing.T) {
	pr := proxy.NewProjectRegistry()
	reg := proxy.NewRegistry()
	pr.Register("web", reg)

	pr.Remove("web")

	if _, ok := pr.For("web"); ok {
		t.Fatal("expected web to be gone after Remove")
	}
}

func TestProjectRegistry_RemoveIsNoopForMissingService(t *testing.T) {
	pr := proxy.NewProjectRegistry()
	pr.Remove("never-registered") // must not panic
}

// TestProjectRegistry_ServicesAreIsolated proves the ownership invariant
// that matters most: two distinct services registered with two distinct
// Registry instances must never resolve to the same Registry, and removing
// one service's association must never affect any other service's.
func TestProjectRegistry_ServicesAreIsolated(t *testing.T) {
	pr := proxy.NewProjectRegistry()
	regWeb := proxy.NewRegistry()
	regAPI := proxy.NewRegistry()

	pr.Register("web", regWeb)
	pr.Register("api", regAPI)

	gotWeb, ok := pr.For("web")
	if !ok || gotWeb != regWeb {
		t.Fatal("web must resolve to its own Registry")
	}
	gotAPI, ok := pr.For("api")
	if !ok || gotAPI != regAPI {
		t.Fatal("api must resolve to its own Registry")
	}
	if gotWeb == gotAPI {
		t.Fatal("two distinct services must never resolve to the same Registry unless explicitly registered that way")
	}

	pr.Remove("web")

	if _, ok := pr.For("web"); ok {
		t.Fatal("web should be gone")
	}
	gotAPI2, ok := pr.For("api")
	if !ok || gotAPI2 != regAPI {
		t.Fatal("removing web must not affect api's association")
	}
}

func TestProjectRegistry_Services(t *testing.T) {
	pr := proxy.NewProjectRegistry()
	pr.Register("web", proxy.NewRegistry())
	pr.Register("api", proxy.NewRegistry())

	names := pr.Services()
	if len(names) != 2 {
		t.Fatalf("want 2 services, got %d: %v", len(names), names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	if !seen["web"] || !seen["api"] {
		t.Fatalf("expected web and api, got %v", names)
	}
}

func TestProjectRegistry_ServicesEmpty(t *testing.T) {
	pr := proxy.NewProjectRegistry()
	names := pr.Services()
	if len(names) != 0 {
		t.Fatalf("want 0 services, got %d: %v", len(names), names)
	}
}

// TestProjectRegistry_ConcurrentAccess exercises Register/For/Remove/Services
// from many goroutines at once. Run with -race: the only thing this test
// asserts structurally is that it doesn't crash or race: correctness of
// concurrent registration semantics (last write wins under concurrent
// Register on the same key) is inherent to a plain map-behind-a-mutex and
// isn't re-verified here beyond "the mutex actually serializes access."
func TestProjectRegistry_ConcurrentAccess(t *testing.T) {
	pr := proxy.NewProjectRegistry()
	const n = 50

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			service := fmt.Sprintf("svc-%d", i%10)
			reg := proxy.NewRegistry()
			pr.Register(service, reg)
			pr.For(service)
			pr.Services()
			if i%3 == 0 {
				pr.Remove(service)
			}
		}(i)
	}
	wg.Wait()
}
