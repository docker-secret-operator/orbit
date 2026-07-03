package stack

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// NetworkPolicy defines constraints for service rollouts.
type NetworkPolicy struct {
	Name                    string
	MaxFailedServices       int           // Maximum services allowed to fail simultaneously
	MaxFailurePercentage    float64       // Max failure as % of total services (0-100)
	CircuitBreakerThreshold int           // Failures before circuit opens
	IsolationMode           IsolationMode // How to isolate failing services
	RollbackOnBlastRadius   bool          // Auto-rollback if blast radius exceeded
	QuarantineTimeout       time.Duration // How long to quarantine failing service
	PropagationDepth        int           // How many dependency levels to check
}

// IsolationMode defines how to isolate failing services.
type IsolationMode string

const (
	IsolationModeNone       IsolationMode = "none"       // No isolation
	IsolationModeQuarantine IsolationMode = "quarantine" // Stop accepting connections
	IsolationModeKill       IsolationMode = "kill"       // Forcefully stop service
	IsolationModeRollback   IsolationMode = "rollback"   // Rollback to previous version
)

// BlastRadiusAnalyzer analyzes the impact of service failures.
type BlastRadiusAnalyzer struct {
	policy  *NetworkPolicy
	rollout *StackRollout
	log     *zap.Logger
	mu      sync.RWMutex

	// Failure tracking
	failedServices      map[string]time.Time
	quarantinedServices map[string]time.Time
	failureChain        map[string][]string // Service -> affected services
}

// NewBlastRadiusAnalyzer creates a new blast radius analyzer.
func NewBlastRadiusAnalyzer(policy *NetworkPolicy, rollout *StackRollout, log *zap.Logger) *BlastRadiusAnalyzer {
	if log == nil {
		log = zap.NewNop()
	}

	if policy == nil {
		policy = &NetworkPolicy{
			Name:                    "default",
			MaxFailedServices:       2,
			MaxFailurePercentage:    25.0,
			CircuitBreakerThreshold: 3,
			IsolationMode:           IsolationModeQuarantine,
			RollbackOnBlastRadius:   true,
			QuarantineTimeout:       5 * time.Minute,
			PropagationDepth:        3,
		}
	}

	return &BlastRadiusAnalyzer{
		policy:              policy,
		rollout:             rollout,
		log:                 log,
		failedServices:      make(map[string]time.Time),
		quarantinedServices: make(map[string]time.Time),
		failureChain:        make(map[string][]string),
	}
}

// RecordFailure records a service failure and analyzes blast radius.
func (bra *BlastRadiusAnalyzer) RecordFailure(service string) (BlastRadiusResult, error) {
	bra.mu.Lock()
	defer bra.mu.Unlock()

	bra.failedServices[service] = time.Now()

	bra.log.Warn("service failure recorded",
		zap.String("service", service),
		zap.Int("total_failed", len(bra.failedServices)))

	result := bra.analyzeBlastRadius(service)

	if result.ExceedsThreshold && bra.policy.RollbackOnBlastRadius {
		bra.log.Error("blast radius exceeded, initiating containment",
			zap.String("service", service),
			zap.Int("affected_services", len(result.AffectedServices)),
			zap.String("isolation_mode", string(bra.policy.IsolationMode)))

		if err := bra.containFailure(service, result); err != nil {
			bra.log.Error("containment failed",
				zap.String("service", service),
				zap.Error(err))
			return result, err
		}
	}

	return result, nil
}

// analyzeBlastRadius analyzes the impact of a service failure.
func (bra *BlastRadiusAnalyzer) analyzeBlastRadius(failedService string) BlastRadiusResult {
	result := BlastRadiusResult{
		FailedService:     failedService,
		AnalyzedAt:        time.Now(),
		AffectedServices:  make([]string, 0),
		ExceedsThreshold:  false,
		FailurePercentage: 0,
	}

	// Get all dependents of the failed service
	affected := bra.getAllDependents(failedService, bra.policy.PropagationDepth)
	result.AffectedServices = affected

	// Check if blast radius exceeds policy thresholds
	totalServices := len(bra.rollout.state.ServiceStates)
	failedCount := len(bra.failedServices)

	// Calculate percentage
	if totalServices > 0 {
		result.FailurePercentage = (float64(failedCount) / float64(totalServices)) * 100
	}

	// Check thresholds
	if failedCount > bra.policy.MaxFailedServices {
		result.ExceedsThreshold = true
		result.Reason = fmt.Sprintf("exceeded max failed services: %d > %d",
			failedCount, bra.policy.MaxFailedServices)
		return result
	}

	if result.FailurePercentage > bra.policy.MaxFailurePercentage {
		result.ExceedsThreshold = true
		result.Reason = fmt.Sprintf("exceeded max failure percentage: %.1f%% > %.1f%%",
			result.FailurePercentage, bra.policy.MaxFailurePercentage)
		return result
	}

	// Check if too many services could be affected
	if len(affected) > 0 {
		affectedPercentage := (float64(len(affected)) / float64(totalServices)) * 100
		if affectedPercentage > bra.policy.MaxFailurePercentage {
			result.ExceedsThreshold = true
			result.Reason = fmt.Sprintf("affected services exceed threshold: %.1f%% > %.1f%%",
				affectedPercentage, bra.policy.MaxFailurePercentage)
		}
	}

	return result
}

// getAllDependents recursively gets all services dependent on a service.
func (bra *BlastRadiusAnalyzer) getAllDependents(service string, depth int) []string {
	if depth <= 0 || bra.rollout.state.Graph == nil {
		return make([]string, 0)
	}

	affected := make(map[string]bool)
	directDependents := bra.rollout.state.Graph.GetDependents(service)

	for _, dependent := range directDependents {
		affected[dependent] = true

		// Recursively get dependents of dependents
		for _, transitive := range bra.getAllDependents(dependent, depth-1) {
			affected[transitive] = true
		}
	}

	result := make([]string, 0, len(affected))
	for service := range affected {
		result = append(result, service)
	}

	return result
}

// containFailure applies isolation to contain the failure.
func (bra *BlastRadiusAnalyzer) containFailure(service string, result BlastRadiusResult) error {
	switch bra.policy.IsolationMode {
	case IsolationModeQuarantine:
		return bra.quarantineServiceLocked(service)

	case IsolationModeKill:
		return bra.killService(service)

	case IsolationModeRollback:
		return bra.rollbackService(service)

	case IsolationModeNone:
		bra.log.Warn("isolation mode is none, blast radius not contained",
			zap.String("service", service))
		return nil

	default:
		return fmt.Errorf("unknown isolation mode: %s", bra.policy.IsolationMode)
	}
}

// quarantineService stops accepting new connections to a service.
func (bra *BlastRadiusAnalyzer) quarantineService(service string) error {
	bra.mu.Lock()
	defer bra.mu.Unlock()
	return bra.quarantineServiceLocked(service)
}

func (bra *BlastRadiusAnalyzer) quarantineServiceLocked(service string) error {
	bra.quarantinedServices[service] = time.Now()

	bra.log.Info("service quarantined",
		zap.String("service", service),
		zap.Duration("timeout", bra.policy.QuarantineTimeout))

	// In real implementation, would update load balancer/proxy
	// to stop routing new connections to this service

	return nil
}

// killService forcefully stops a service.
func (bra *BlastRadiusAnalyzer) killService(service string) error {
	state, ok := bra.rollout.state.ServiceStates[service]
	if !ok {
		return fmt.Errorf("service %q not found", service)
	}

	bra.log.Warn("killing service",
		zap.String("service", service),
		zap.String("container_id", state.NewContainer))

	// In real implementation, would stop the container
	// This would be done through DockerClient

	return nil
}

// rollbackService rolls back a failed service to previous version.
func (bra *BlastRadiusAnalyzer) rollbackService(service string) error {
	state, ok := bra.rollout.state.ServiceStates[service]
	if !ok {
		return fmt.Errorf("service %q not found", service)
	}

	bra.log.Warn("rolling back service",
		zap.String("service", service),
		zap.String("old_container", state.OldContainer),
		zap.String("new_container", state.NewContainer))

	// Swap containers: make old container active again
	state.NewContainer, state.OldContainer = state.OldContainer, state.NewContainer

	return nil
}

// IsServiceQuarantined returns true if service is quarantined.
func (bra *BlastRadiusAnalyzer) IsServiceQuarantined(service string) bool {
	bra.mu.RLock()
	defer bra.mu.RUnlock()

	quarantineTime, exists := bra.quarantinedServices[service]
	if !exists {
		return false
	}

	// Check if quarantine has expired
	if time.Since(quarantineTime) > bra.policy.QuarantineTimeout {
		return false
	}

	return true
}

// ReleaseQuarantine releases a service from quarantine.
func (bra *BlastRadiusAnalyzer) ReleaseQuarantine(service string) {
	bra.mu.Lock()
	defer bra.mu.Unlock()

	delete(bra.quarantinedServices, service)

	bra.log.Info("service released from quarantine",
		zap.String("service", service))
}

// GetStatus returns current status of failures and quarantines.
func (bra *BlastRadiusAnalyzer) GetStatus() NetworkPolicyStatus {
	bra.mu.RLock()
	defer bra.mu.RUnlock()

	status := NetworkPolicyStatus{
		FailedServices:      make([]string, 0),
		QuarantinedServices: make([]string, 0),
		Timestamp:           time.Now(),
	}

	for service := range bra.failedServices {
		status.FailedServices = append(status.FailedServices, service)
	}

	for service := range bra.quarantinedServices {
		status.QuarantinedServices = append(status.QuarantinedServices, service)
	}

	status.TotalFailed = len(status.FailedServices)
	status.TotalQuarantined = len(status.QuarantinedServices)

	return status
}

// BlastRadiusResult contains analysis of a failure's impact.
type BlastRadiusResult struct {
	FailedService     string
	AffectedServices  []string
	AnalyzedAt        time.Time
	ExceedsThreshold  bool
	FailurePercentage float64
	Reason            string
}

// NetworkPolicyStatus represents current network policy status.
type NetworkPolicyStatus struct {
	FailedServices      []string
	QuarantinedServices []string
	TotalFailed         int
	TotalQuarantined    int
	Timestamp           time.Time
}
