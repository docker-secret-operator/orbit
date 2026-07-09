package proxy

import (
	"context"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ProjectHealthController runs continuous health evaluation across every
// service currently registered in a ProjectRegistry. It does not replace or
// duplicate HealthController's evaluation logic — each service gets its own
// unmodified *HealthController internally, so hysteresis state (consecutive
// failures/successes) stays correctly scoped per service, exactly as it is
// today for a single-service proxy.
//
// Per implementation invariant II-4, this type drives all of them from a
// single ticker in one goroutine (the caller's, via Run), iterating services
// sequentially each tick — never one goroutine per service. At the ADR's own
// scalability ceiling (100 services) that would mean 100+ long-lived
// goroutines for bookkeeping alone; sequential iteration within one bounded
// loop is simpler to reason about and keeps this a thin fan-out layer.
type ProjectHealthController struct {
	pr      *ProjectRegistry
	prober  HealthProber
	cfg     HealthControllerConfig
	metrics HealthMetrics
	log     *zap.Logger

	mu        sync.Mutex
	byService map[string]*HealthController // lazily created per service; reused across ticks
}

// NewProjectHealthController builds the controller. Defaulting mirrors
// NewHealthController's exactly (prober/log/cfg fallbacks) — duplicated
// rather than factored out, since HealthController itself is intentionally
// left unmodified by this stage and its defaulting is unexported.
func NewProjectHealthController(pr *ProjectRegistry, prober HealthProber, cfg HealthControllerConfig, m HealthMetrics, log *zap.Logger) *ProjectHealthController {
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
	return &ProjectHealthController{
		pr: pr, prober: prober, cfg: cfg, metrics: m, log: log,
		byService: make(map[string]*HealthController),
	}
}

// Run continuously evaluates health for every currently registered service
// until ctx is cancelled. It blocks; run as `go phc.Run(ctx)`.
func (p *ProjectHealthController) Run(ctx context.Context) {
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.CheckOnce(ctx)
		}
	}
}

// CheckOnce evaluates health for every currently registered service,
// sequentially and deterministically (sorted by service name), reusing each
// service's HealthController across calls so its hysteresis state persists
// between ticks. Exposed for deterministic testing, mirroring
// HealthController.CheckOnce.
func (p *ProjectHealthController) CheckOnce(ctx context.Context) {
	services := p.pr.Services()
	sort.Strings(services)
	for _, service := range services {
		reg, ok := p.pr.For(service)
		if !ok {
			continue // removed between listing and lookup — next tick will simply not see it
		}
		p.healthControllerFor(service, reg).CheckOnce(ctx)
	}
}

// healthControllerFor returns the cached HealthController for service,
// creating one if this is the first time service has been seen, or
// replacing it if service's Registry was swapped out from under it (a
// ProjectRegistry.Register replacement) — a stale HealthController would
// otherwise keep evaluating the wrong Registry's backends.
func (p *ProjectHealthController) healthControllerFor(service string, reg *Registry) *HealthController {
	p.mu.Lock()
	defer p.mu.Unlock()
	// hc.reg (unexported) is compared directly rather than through an
	// accessor — legal within this package, but it means this function
	// depends on HealthController's internal field layout, not just its
	// exported Run/CheckOnce contract. A future HealthController refactor
	// that renames or restructures this field must keep this comparison in
	// mind (ADR-0006 governance review 01, §5/§12).
	hc, ok := p.byService[service]
	if !ok || hc.reg != reg {
		// A logger scoped with the service name, not the raw p.log, so
		// every log line HealthController.CheckOnce already emits (e.g.
		// "health: backend transitioned") is attributable to this service
		// with zero changes to HealthController itself — logging remains a
		// presentation concern, injected here rather than added there.
		hc = NewHealthController(reg, p.prober, p.cfg, p.metrics, p.log.With(zap.String("service", service)))
		p.byService[service] = hc
	}
	return hc
}
