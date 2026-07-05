package proxy

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"go.uber.org/zap"
)

// DockerRecoverySource discovers and validates backends from Docker containers.
type DockerRecoverySource struct {
	client           *client.Client
	proxyInstance    string
	log              *zap.Logger
	healthValidator  *HealthValidator
	tcpDialTimeout   time.Duration
	maxHealthWorkers int
}

// NewDockerRecoverySource creates a recovery source with Docker SDK and health validation.
func NewDockerRecoverySource(proxyInstance string, log *zap.Logger) (*DockerRecoverySource, error) {
	return NewDockerRecoverySourceWithConfig(proxyInstance, log, 2*time.Second, 10)
}

// NewDockerRecoverySourceWithConfig creates a recovery source with custom health config.
func NewDockerRecoverySourceWithConfig(proxyInstance string, log *zap.Logger, tcpTimeout time.Duration, maxWorkers int) (*DockerRecoverySource, error) {
	cl, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	// Validate Docker daemon is available.
	_, err = cl.Ping(context.Background())
	if err != nil {
		cl.Close() //nolint:errcheck // docker client teardown; close error not actionable
		return nil, fmt.Errorf("docker daemon unavailable: %w", err)
	}

	return &DockerRecoverySource{
		client:           cl,
		proxyInstance:    proxyInstance,
		log:              log,
		healthValidator:  NewHealthValidator(cl, log, tcpTimeout, maxWorkers),
		tcpDialTimeout:   tcpTimeout,
		maxHealthWorkers: maxWorkers,
	}, nil
}

// DiscoverBackends finds all Orbit-managed containers (without health validation).
func (d *DockerRecoverySource) DiscoverBackends(ctx context.Context) ([]Backend, error) {
	// Filter for Orbit-managed containers.
	f := filters.NewArgs(
		filters.Arg("label", "orbit.io/managed=true"),
		filters.Arg("status", "running"),
	)

	containers, err := d.client.ContainerList(ctx, types.ContainerListOptions{
		Filters: f,
	})
	if err != nil {
		return nil, fmt.Errorf("container list: %w", err)
	}

	var backends []Backend

	for _, c := range containers {
		backend, err := d.extractBackend(ctx, c)
		if err != nil {
			d.log.Warn("recovery: skip container",
				zap.String("container", c.ID[:12]),
				zap.Error(err))
			continue
		}
		if backend != nil {
			backends = append(backends, *backend)
		}
	}

	return backends, nil
}

// DiscoverAndValidateBackends finds backends and validates health.
// Returns RecoveryResult with detailed health information.
// Handles partial failures gracefully: healthy backends are restored even if some are unhealthy.
func (d *DockerRecoverySource) DiscoverAndValidateBackends(ctx context.Context) (*RecoveryResult, error) {
	start := time.Now()
	result := &RecoveryResult{
		State:       StartupRecovering,
		Backends:    []BackendHealth{},
		RecoveredAt: time.Now(),
	}

	// Discover all containers.
	f := filters.NewArgs(
		filters.Arg("label", "orbit.io/managed=true"),
		filters.Arg("status", "running"),
	)

	containers, err := d.client.ContainerList(ctx, types.ContainerListOptions{
		Filters: f,
	})
	if err != nil {
		result.State = StartupFailed
		result.FailedReason = fmt.Sprintf("container discovery failed: %v", err)
		result.DurationMs = time.Since(start).Milliseconds()
		return result, fmt.Errorf("container list: %w", err)
	}

	result.TotalDiscovered = len(containers)

	// Extract backends.
	containerMap := make(map[string]Backend)
	for _, c := range containers {
		backend, err := d.extractBackend(ctx, c)
		if err != nil {
			d.log.Warn("recovery: skip container",
				zap.String("container", c.ID[:12]),
				zap.Error(err))
			continue
		}
		if backend != nil {
			containerMap[c.ID] = *backend
		}
	}

	// Validate health for all discovered backends.
	healthResults := d.healthValidator.BatchCheck(ctx, containerMap)
	result.Backends = healthResults

	// Count health states.
	for _, h := range healthResults {
		switch h.Status {
		case HealthHealthy:
			result.HealthyCount++
		case HealthStarting:
			result.StartingCount++
		case HealthUnhealthy:
			result.UnhealthyCount++
		case HealthUnknown, HealthDegraded:
			result.UnknownCount++
		}
	}

	// Determine startup state based on health results.
	// CRITICAL: Correct state mapping prevents silent failures.
	result.DurationMs = time.Since(start).Milliseconds()

	// State determination logic (order matters):
	// 1. All unhealthy (no healthy, no starting) → StartupFailed (preserve failure state)
	if result.HealthyCount == 0 && result.StartingCount == 0 && result.UnhealthyCount > 0 {
		result.State = StartupFailed
		result.FailedReason = fmt.Sprintf("all backends unhealthy: %d unhealthy, 0 healthy, 0 starting",
			result.UnhealthyCount)
		d.log.Error("recovery: startup failed - all backends unhealthy",
			zap.Int("unhealthy", result.UnhealthyCount))
		return result, nil
	}

	// 2. Only starting (no healthy, no unhealthy) → StartupRecovering (still bootstrapping)
	if result.HealthyCount == 0 && result.UnhealthyCount == 0 && result.StartingCount > 0 {
		result.State = StartupRecovering
		d.log.Info("recovery: still recovering - backends starting up",
			zap.Int("starting", result.StartingCount))
		return result, nil
	}

	// 3. Healthy + unhealthy (mixed) → StartupDegraded (partial failure OK)
	if result.HealthyCount > 0 && result.UnhealthyCount > 0 {
		result.State = StartupDegraded
		d.log.Warn("recovery: degraded startup - partial failure",
			zap.Int("healthy", result.HealthyCount),
			zap.Int("unhealthy", result.UnhealthyCount),
			zap.Int("starting", result.StartingCount))
		return result, nil
	}

	// 4. Healthy available → StartupReady (can accept traffic)
	if result.HealthyCount > 0 {
		result.State = StartupReady
		d.log.Info("recovery: ready - healthy backends available",
			zap.Int("healthy", result.HealthyCount),
			zap.Int("starting", result.StartingCount),
			zap.Int("unhealthy", result.UnhealthyCount))
		return result, nil
	}

	// 5. Empty (no containers) → StartupRecovering (could be cold start or failure)
	// Future: use ExpectedServices to distinguish.
	result.State = StartupRecovering
	d.log.Info("recovery: no containers discovered",
		zap.Int("expected", result.ExpectedServices))
	return result, nil
}

// extractBackend extracts Backend from a container.
// Returns nil if validation fails (stale/invalid container).
func (d *DockerRecoverySource) extractBackend(ctx context.Context, c types.Container) (*Backend, error) {
	// Inspect for full metadata.
	inspect, err := d.client.ContainerInspect(ctx, c.ID)
	if err != nil {
		return nil, fmt.Errorf("inspect: %w", err)
	}

	labels := inspect.Config.Labels

	// Validate ownership labels.
	service := labels["orbit.io/service"]
	proxy := labels["orbit.io/proxy"]
	generation := labels["orbit.io/generation"]
	proxyInstance := labels["orbit.io/proxy-instance"]

	if service == "" || proxy == "" || generation == "" {
		return nil, fmt.Errorf("incomplete ownership labels")
	}

	// Verify this backend belongs to this proxy instance.
	if proxyInstance != "" && proxyInstance != d.proxyInstance {
		return nil, fmt.Errorf(
			"ownership mismatch: container owned by instance %q, this is %q",
			proxyInstance, d.proxyInstance,
		)
	}

	// Extract backend ID from env.
	backendID := ""
	for _, env := range inspect.Config.Env {
		if strings.HasPrefix(env, "ORBIT_BACKEND_ID=") {
			backendID = strings.TrimPrefix(env, "ORBIT_BACKEND_ID=")
			break
		}
	}
	if backendID == "" {
		return nil, fmt.Errorf("missing ORBIT_BACKEND_ID env")
	}

	// Extract IP from docker_rollout_mesh network.
	var ip string
	if net := inspect.NetworkSettings.Networks["docker_rollout_mesh"]; net != nil {
		ip = net.IPAddress
	}
	if ip == "" {
		return nil, fmt.Errorf("not on docker_rollout_mesh network")
	}

	// Extract port from ORBIT_BACKEND env (format: "service:port").
	port := "3000" // fallback
	for _, env := range inspect.Config.Env {
		if strings.HasPrefix(env, "ORBIT_BACKEND=") {
			parts := strings.Split(strings.TrimPrefix(env, "ORBIT_BACKEND="), ":")
			if len(parts) == 2 {
				port = parts[1]
			}
			break
		}
	}

	addr := net.JoinHostPort(ip, port)

	return &Backend{
		ID:         backendID,
		Addr:       addr,
		Generation: generation,
	}, nil
}

// Close closes the Docker client and health validator.
func (d *DockerRecoverySource) Close() error {
	if d.healthValidator != nil {
		d.healthValidator.Close() //nolint:errcheck // health validator teardown; close error not actionable
	}
	if d.client != nil {
		return d.client.Close()
	}
	return nil
}

// DeriveHealthStreakStartTime derives ContinuousHealthyStart from container metadata.
// Used on process restart to recover the health streak information that was lost.
//
// Semantics:
//   - Source of truth: container creation time (conservative)
//   - For fully healthy generation: use oldest container creation time
//   - For partially healthy: use current time (pessimistic; reset on any failure)
//   - Never overestimate uptime (round DOWN)
func DeriveHealthStreakStartTime(
	allHealthy bool,
	containers []types.Container,
	log *zap.Logger,
) time.Time {
	if !allHealthy || len(containers) == 0 {
		// No healthy state to recover; start fresh
		log.Debug("health streak: reset to now (not all healthy)")
		return time.Now()
	}

	// All healthy: use oldest container creation
	oldest := time.Now()
	for _, c := range containers {
		if c.Created < oldest.Unix() {
			oldest = time.Unix(c.Created, 0)
		}
	}

	log.Info("health streak: derived from oldest container",
		zap.Time("derived_start", oldest),
		zap.Int("container_count", len(containers)))

	return oldest
}
