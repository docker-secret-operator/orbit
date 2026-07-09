package proxy

import "sync"

// ProjectRegistry is a service-to-Registry lookup for proxies fronting more
// than one service (ADR-0006). It owns no backend, health, routing, or
// recovery state of its own — each service's *Registry remains the sole
// owner of that service's backends, exactly as it is today for a
// single-service proxy. ProjectRegistry only answers one question: which
// Registry owns service X?
type ProjectRegistry struct {
	mu       sync.RWMutex
	services map[string]*Registry
}

// NewProjectRegistry creates an empty ProjectRegistry. Services are added
// with Register as their Registry instances are constructed.
func NewProjectRegistry() *ProjectRegistry {
	return &ProjectRegistry{
		services: make(map[string]*Registry),
	}
}

// Register associates service with reg, replacing any existing association
// for that service. reg is stored by reference — ProjectRegistry never
// copies or mirrors backend state out of it.
func (p *ProjectRegistry) Register(service string, reg *Registry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.services[service] = reg
}

// Remove deletes service's association, if any. A no-op if service is not
// registered. Removing one service never affects any other service's
// association.
func (p *ProjectRegistry) Remove(service string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.services, service)
}

// For returns the Registry owning service, and whether it was found.
func (p *ProjectRegistry) For(service string) (*Registry, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	reg, ok := p.services[service]
	return reg, ok
}

// Services returns the names of all currently registered services, in no
// particular order.
func (p *ProjectRegistry) Services() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.services))
	for s := range p.services {
		out = append(out, s)
	}
	return out
}
