package proxy

import (
	"context"
	"net"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// HealthProber probes a single backend address and reports reachability.
// Implementations must be safe for concurrent use and honor ctx cancellation.
// The default (tcpProber) mirrors HealthValidator.validateTCP; a Docker
// HEALTHCHECK-based prober can be substituted without touching the controller.
type HealthProber interface {
	Probe(ctx context.Context, addr string) bool
}

// tcpProber reports a backend healthy if a TCP connection can be established
// within the configured timeout.
type tcpProber struct{ timeout time.Duration }

func (p tcpProber) Probe(ctx context.Context, addr string) bool {
	d := net.Dialer{Timeout: p.timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// HealthMetrics is the optional health observability sink (nil-safe), satisfied
// structurally by *metrics.Proxy.
type HealthMetrics interface {
	IncHealthChecks()
	IncHealthFailures()
	IncBackendStateChanges()
	SetUnhealthyBackends(n int)
}

// HealthControllerConfig configures continuous health evaluation. Zero values
// fall back to defaults (see NewHealthController), preserving backward
// compatibility with callers that pass a partially-filled config.
type HealthControllerConfig struct {
	Interval           time.Duration // between evaluation passes
	Timeout            time.Duration // per-probe timeout
	UnhealthyThreshold int           // consecutive failures to go Active → Unhealthy
	HealthyThreshold   int           // consecutive successes to go Unhealthy → Active
	MaxConcurrent      int           // bounded concurrent probes per pass
}

// DefaultHealthControllerConfig returns sensible defaults.
func DefaultHealthControllerConfig() HealthControllerConfig {
	return HealthControllerConfig{
		Interval:           5 * time.Second,
		Timeout:            2 * time.Second,
		UnhealthyThreshold: 2,
		HealthyThreshold:   3,
		MaxConcurrent:      16,
	}
}

// HealthController is the single continuous owner of runtime backend health
// (Runtime Constitution §III Layer 5). It probes backends, applies hysteresis,
// and writes verdicts to the Runtime Registry via SetHealth. It NEVER touches
// the Router, deploys, calls Docker lifecycle, or persists — health reports
// state; the Registry owns state; the Router consumes state.
type HealthController struct {
	reg     *Registry
	prober  HealthProber
	cfg     HealthControllerConfig
	metrics HealthMetrics
	log     *zap.Logger

	// evalMu serializes evaluation passes so the hysteresis counters below are
	// only ever touched by one pass at a time (Run ticks are serial; this also
	// makes concurrent CheckOnce calls safe).
	evalMu sync.Mutex
	fails  map[string]int // consecutive failures per backend
	oks    map[string]int // consecutive successes per backend
}

// NewHealthController builds the controller. A nil prober defaults to a TCP
// prober; a nil logger defaults to no-op; non-positive config fields fall back
// to DefaultHealthControllerConfig.
func NewHealthController(reg *Registry, prober HealthProber, cfg HealthControllerConfig, m HealthMetrics, log *zap.Logger) *HealthController {
	d := DefaultHealthControllerConfig()
	if cfg.Interval <= 0 {
		cfg.Interval = d.Interval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = d.Timeout
	}
	if cfg.UnhealthyThreshold < 1 {
		cfg.UnhealthyThreshold = d.UnhealthyThreshold
	}
	if cfg.HealthyThreshold < 1 {
		cfg.HealthyThreshold = d.HealthyThreshold
	}
	if cfg.MaxConcurrent < 1 {
		cfg.MaxConcurrent = d.MaxConcurrent
	}
	if prober == nil {
		prober = tcpProber{timeout: cfg.Timeout}
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &HealthController{
		reg: reg, prober: prober, cfg: cfg, metrics: m, log: log,
		fails: map[string]int{}, oks: map[string]int{},
	}
}

// Run continuously evaluates backend health until ctx is cancelled. It blocks;
// run as `go hc.Run(ctx)`. No goroutine leaks: the ticker is stopped on return
// and every per-pass probe goroutine completes before the pass returns.
func (hc *HealthController) Run(ctx context.Context) {
	t := time.NewTicker(hc.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hc.CheckOnce(ctx)
		}
	}
}

// CheckOnce performs one evaluation pass: probe every routable backend
// (StateActive or StateUnhealthy — Draining/Failed are owned by the deployment
// and terminal lifecycles), apply hysteresis, and update the Registry. Exposed
// for deterministic testing. Transitions are logged once, on change only.
func (hc *HealthController) CheckOnce(ctx context.Context) {
	hc.evalMu.Lock()
	defer hc.evalMu.Unlock()

	type target struct{ id, addr string }
	var targets []target
	for _, b := range hc.reg.Snapshot() {
		if b.State == StateActive || b.State == StateUnhealthy {
			targets = append(targets, target{b.ID, b.Addr})
		}
	}

	// Bounded-concurrency probing; probes are read-only, results applied serially.
	results := make(map[string]bool, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, hc.cfg.MaxConcurrent)
	for _, tg := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(tg target) {
			defer wg.Done()
			defer func() { <-sem }()
			ok := hc.prober.Probe(ctx, tg.addr)
			mu.Lock()
			results[tg.id] = ok
			mu.Unlock()
		}(tg)
	}
	wg.Wait()

	// Apply deterministically (sorted by ID).
	sort.Slice(targets, func(i, j int) bool { return targets[i].id < targets[j].id })
	unhealthy := 0
	for _, tg := range targets {
		ok := results[tg.id]
		if hc.metrics != nil {
			hc.metrics.IncHealthChecks()
		}
		if ok {
			hc.oks[tg.id]++
			hc.fails[tg.id] = 0
		} else {
			hc.fails[tg.id]++
			hc.oks[tg.id] = 0
			if hc.metrics != nil {
				hc.metrics.IncHealthFailures()
			}
		}

		cur, exists := hc.reg.State(tg.id)
		if !exists {
			continue
		}
		switch {
		case !ok && cur == StateActive && hc.fails[tg.id] >= hc.cfg.UnhealthyThreshold:
			// Guarded demotion preserves zero-backend protection: refused if this
			// is the last active backend (the zero-backend hook logs/meters it).
			if applied, _, gerr := hc.reg.SetHealthGuarded(tg.id, false); gerr == nil && applied {
				hc.log.Info("health: backend transitioned",
					zap.String("id", tg.id),
					zap.String("from", string(StateActive)),
					zap.String("to", string(StateUnhealthy)),
					zap.Int("consecutive_failures", hc.fails[tg.id]))
				if hc.metrics != nil {
					hc.metrics.IncBackendStateChanges()
				}
			}
		case ok && cur == StateUnhealthy && hc.oks[tg.id] >= hc.cfg.HealthyThreshold:
			if applied, _, gerr := hc.reg.SetHealthGuarded(tg.id, true); gerr == nil && applied {
				hc.log.Info("health: backend transitioned",
					zap.String("id", tg.id),
					zap.String("from", string(StateUnhealthy)),
					zap.String("to", string(StateActive)),
					zap.Int("consecutive_successes", hc.oks[tg.id]))
				if hc.metrics != nil {
					hc.metrics.IncBackendStateChanges()
				}
			}
		}

		if st, _ := hc.reg.State(tg.id); st == StateUnhealthy {
			unhealthy++
		}
	}
	if hc.metrics != nil {
		hc.metrics.SetUnhealthyBackends(unhealthy)
	}

	// Forget hysteresis counters for backends no longer present.
	present := make(map[string]bool, len(targets))
	for _, tg := range targets {
		present[tg.id] = true
	}
	for id := range hc.fails {
		if !present[id] {
			delete(hc.fails, id)
			delete(hc.oks, id)
		}
	}
}
