package stack

import (
	"fmt"
	"sort"

	"go.uber.org/zap"
)

// DependencyGraphBuilder constructs a dependency graph from service definitions.
type DependencyGraphBuilder struct {
	log      *zap.Logger
	services map[string]*ServiceDependency
}

// NewDependencyGraphBuilder creates a new builder for dependency graphs.
func NewDependencyGraphBuilder(log *zap.Logger) *DependencyGraphBuilder {
	if log == nil {
		log = zap.NewNop()
	}
	return &DependencyGraphBuilder{
		log:      log,
		services: make(map[string]*ServiceDependency),
	}
}

// AddService registers a service and its dependencies in the graph.
func (dgb *DependencyGraphBuilder) AddService(service string, dependencies ...string) {
	dgb.log.Debug("adding service to dependency graph",
		zap.String("service", service),
		zap.Strings("depends_on", dependencies))

	dgb.services[service] = &ServiceDependency{
		Service:   service,
		DependsOn: dependencies,
		Condition: "service_started", // Default condition
	}
}

// AddServiceWithCondition registers a service with a specific health condition.
func (dgb *DependencyGraphBuilder) AddServiceWithCondition(service, condition string, dependencies ...string) {
	dgb.AddService(service, dependencies...)
	if svc, ok := dgb.services[service]; ok {
		svc.Condition = condition
	}
}

// Build constructs the final dependency graph with topological ordering.
func (dgb *DependencyGraphBuilder) Build() (*DependencyGraph, error) {
	dg := &DependencyGraph{
		Services: dgb.services,
		Levels:   make([][]string, 0),
		Order:    make([]string, 0),
	}

	// Check for circular dependencies
	if err := dgb.detectCircularDependencies(); err != nil {
		return nil, err
	}

	// Perform topological sort
	order, err := dgb.topologicalSort()
	if err != nil {
		return nil, err
	}

	dg.Order = order

	// Group services into levels by dependency depth
	dg.Levels = dgb.buildLevels(order)

	dgb.log.Info("dependency graph built",
		zap.Int("service_count", len(dg.Services)),
		zap.Int("level_count", len(dg.Levels)),
		zap.Strings("order", order))

	return dg, nil
}

// topologicalSort performs Kahn's algorithm to order services by dependencies.
// Returns services in order where each service comes after its dependencies.
func (dgb *DependencyGraphBuilder) topologicalSort() ([]string, error) {
	// Calculate in-degree for each service
	inDegree := make(map[string]int)
	for service := range dgb.services {
		inDegree[service] = 0
	}

	for _, svc := range dgb.services {
		for _, dep := range svc.DependsOn {
			if _, ok := dgb.services[dep]; !ok {
				return nil, fmt.Errorf("service %q depends on undefined service %q", svc.Service, dep)
			}
			inDegree[svc.Service]++
		}
	}

	// Find all services with in-degree 0
	queue := make([]string, 0)
	for service, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, service)
		}
	}

	// Sort for deterministic ordering
	sort.Strings(queue)

	result := make([]string, 0, len(dgb.services))

	for len(queue) > 0 {
		// Dequeue a service
		service := queue[0]
		queue = queue[1:]
		result = append(result, service)

		// For each service that depends on this one
		for _, other := range dgb.services {
			// Check if other depends on service
			for _, dep := range other.DependsOn {
				if dep == service {
					inDegree[other.Service]--
					if inDegree[other.Service] == 0 {
						queue = append(queue, other.Service)
						sort.Strings(queue) // Keep sorted for determinism
					}
				}
			}
		}
	}

	if len(result) != len(dgb.services) {
		return nil, fmt.Errorf("circular dependency detected")
	}

	return result, nil
}

// detectCircularDependencies checks for cycles in the dependency graph.
func (dgb *DependencyGraphBuilder) detectCircularDependencies() error {
	// First check for undefined dependencies
	for _, svc := range dgb.services {
		for _, dep := range svc.DependsOn {
			if _, ok := dgb.services[dep]; !ok {
				return fmt.Errorf("service %q depends on undefined service %q", svc.Service, dep)
			}
		}
	}

	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	for service := range dgb.services {
		if !visited[service] {
			if dgb.hasCycle(service, visited, recStack) {
				return fmt.Errorf("circular dependency detected involving service %q", service)
			}
		}
	}

	return nil
}

// hasCycle performs DFS to detect cycles.
func (dgb *DependencyGraphBuilder) hasCycle(service string, visited, recStack map[string]bool) bool {
	visited[service] = true
	recStack[service] = true

	svc := dgb.services[service]
	for _, dep := range svc.DependsOn {
		if !visited[dep] {
			if dgb.hasCycle(dep, visited, recStack) {
				return true
			}
		} else if recStack[dep] {
			return true
		}
	}

	recStack[service] = false
	return false
}

// buildLevels groups services into rollout levels.
// Services at the same level have no dependencies on each other and can rollout in parallel.
func (dgb *DependencyGraphBuilder) buildLevels(order []string) [][]string {
	levels := make([][]string, 0)
	assignedLevel := make(map[string]int)

	// Assign each service to a level
	for _, service := range order {
		svc := dgb.services[service]
		maxDependencyLevel := -1

		// Find the max level of all dependencies
		for _, dep := range svc.DependsOn {
			if depLevel, ok := assignedLevel[dep]; ok {
				if depLevel > maxDependencyLevel {
					maxDependencyLevel = depLevel
				}
			}
		}

		// This service's level is one more than max dependency level
		serviceLevel := maxDependencyLevel + 1
		assignedLevel[service] = serviceLevel

		// Ensure we have enough levels
		for len(levels) <= serviceLevel {
			levels = append(levels, make([]string, 0))
		}

		// Add service to its level
		levels[serviceLevel] = append(levels[serviceLevel], service)
	}

	return levels
}

// ValidateDependencies checks that all referenced services exist.
func (dgb *DependencyGraphBuilder) ValidateDependencies() error {
	for _, svc := range dgb.services {
		for _, dep := range svc.DependsOn {
			if _, ok := dgb.services[dep]; !ok {
				return fmt.Errorf("service %q depends on undefined service %q", svc.Service, dep)
			}
		}
	}
	return nil
}

// GetDependents returns all services that directly depend on the given service.
func (dg *DependencyGraph) GetDependents(service string) []string {
	dependents := make([]string, 0)

	for _, svc := range dg.Services {
		for _, dep := range svc.DependsOn {
			if dep == service {
				dependents = append(dependents, svc.Service)
				break
			}
		}
	}

	sort.Strings(dependents)
	return dependents
}

// GetDependencies returns the services that the given service depends on.
func (dg *DependencyGraph) GetDependencies(service string) []string {
	if svc, ok := dg.Services[service]; ok {
		deps := make([]string, len(svc.DependsOn))
		copy(deps, svc.DependsOn)
		sort.Strings(deps)
		return deps
	}
	return []string{}
}

// GetServiceLevel returns the rollout level of a service (0-based).
// Services at the same level can be rolled out in parallel.
func (dg *DependencyGraph) GetServiceLevel(service string) int {
	for level, services := range dg.Levels {
		for _, svc := range services {
			if svc == service {
				return level
			}
		}
	}
	return -1
}

// GetLevelServices returns all services at a specific level.
func (dg *DependencyGraph) GetLevelServices(level int) []string {
	if level >= 0 && level < len(dg.Levels) {
		services := make([]string, len(dg.Levels[level]))
		copy(services, dg.Levels[level])
		sort.Strings(services)
		return services
	}
	return []string{}
}

// GetLevelCount returns the number of rollout levels.
func (dg *DependencyGraph) GetLevelCount() int {
	return len(dg.Levels)
}

// IsLeafService returns true if a service has no dependents.
func (dg *DependencyGraph) IsLeafService(service string) bool {
	dependents := dg.GetDependents(service)
	return len(dependents) == 0
}

// IsRootService returns true if a service has no dependencies.
func (dg *DependencyGraph) IsRootService(service string) bool {
	deps := dg.GetDependencies(service)
	return len(deps) == 0
}
