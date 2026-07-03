package proxy

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/docker/docker/client"
	"go.uber.org/zap"
)

// HealthValidator validates backend health using Docker HEALTHCHECK and TCP fallback.
type HealthValidator struct {
	client        *client.Client
	log           *zap.Logger
	tcpTimeout    time.Duration
	maxConcurrent int
	semaphore     chan struct{} // Bounded concurrency control
}

// NewHealthValidator creates a health validator with bounded concurrency.
func NewHealthValidator(cl *client.Client, log *zap.Logger, tcpTimeout time.Duration, maxConcurrent int) *HealthValidator {
	return &HealthValidator{
		client:        cl,
		log:           log,
		tcpTimeout:    tcpTimeout,
		maxConcurrent: maxConcurrent,
		semaphore:     make(chan struct{}, maxConcurrent),
	}
}

// CheckHealth validates a container's health using Docker HEALTHCHECK + TCP fallback.
// Returns BackendHealth with detailed status and reason.
func (hv *HealthValidator) CheckHealth(ctx context.Context, containerID string, backend Backend) BackendHealth {
	// Check cancellation deterministically before touching the semaphore.
	// A plain select on {semaphore, ctx.Done()} would race when both are
	// ready simultaneously, sometimes falling through to a live Docker call
	// on an already-cancelled context.
	if ctx.Err() != nil {
		return BackendHealth{
			ID:         backend.ID,
			Addr:       backend.Addr,
			Generation: backend.Generation,
			Status:     HealthUnknown,
			Reason:     "context cancelled before health check",
			CheckedAt:  time.Now(),
		}
	}

	// Acquire concurrency slot.
	select {
	case hv.semaphore <- struct{}{}:
		defer func() { <-hv.semaphore }()
	case <-ctx.Done():
		return BackendHealth{
			ID:         backend.ID,
			Addr:       backend.Addr,
			Generation: backend.Generation,
			Status:     HealthUnknown,
			Reason:     "context cancelled before health check",
			CheckedAt:  time.Now(),
		}
	}

	// Inspect container state and HEALTHCHECK status.
	inspect, err := hv.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return BackendHealth{
			ID:         backend.ID,
			Addr:       backend.Addr,
			Generation: backend.Generation,
			Status:     HealthUnknown,
			Reason:     fmt.Sprintf("inspect failed: %v", err),
			CheckedAt:  time.Now(),
			LastErr:    err,
		}
	}

	// Check container running state.
	if !inspect.State.Running {
		return BackendHealth{
			ID:         backend.ID,
			Addr:       backend.Addr,
			Generation: backend.Generation,
			Status:     HealthUnhealthy,
			Reason:     fmt.Sprintf("container not running (state: %s)", inspect.State.Status),
			CheckedAt:  time.Now(),
		}
	}

	// Try Docker HEALTHCHECK first.
	if inspect.State.Health != nil {
		switch inspect.State.Health.Status {
		case "healthy":
			return BackendHealth{
				ID:         backend.ID,
				Addr:       backend.Addr,
				Generation: backend.Generation,
				Status:     HealthHealthy,
				Reason:     "Docker HEALTHCHECK healthy",
				CheckedAt:  time.Now(),
				Attempts:   inspect.State.Health.FailingStreak,
			}
		case "unhealthy":
			return BackendHealth{
				ID:         backend.ID,
				Addr:       backend.Addr,
				Generation: backend.Generation,
				Status:     HealthUnhealthy,
				Reason:     fmt.Sprintf("Docker HEALTHCHECK unhealthy (failing streak: %d)", inspect.State.Health.FailingStreak),
				CheckedAt:  time.Now(),
				Attempts:   inspect.State.Health.FailingStreak,
			}
		case "starting":
			return BackendHealth{
				ID:         backend.ID,
				Addr:       backend.Addr,
				Generation: backend.Generation,
				Status:     HealthStarting,
				Reason:     "Docker HEALTHCHECK still starting",
				CheckedAt:  time.Now(),
				Attempts:   len(inspect.State.Health.Log),
			}
		}
	}

	// Fall back to TCP validation if no HEALTHCHECK.
	tcpStatus := hv.validateTCP(ctx, backend.Addr)
	return BackendHealth{
		ID:         backend.ID,
		Addr:       backend.Addr,
		Generation: backend.Generation,
		Status:     tcpStatus,
		Reason:     fmt.Sprintf("TCP fallback: %s (no HEALTHCHECK)", tcpStatus),
		CheckedAt:  time.Now(),
	}
}

// validateTCP performs a quick TCP dial to backend address.
// Returns HealthHealthy if connection succeeds, HealthUnhealthy otherwise.
func (hv *HealthValidator) validateTCP(ctx context.Context, addr string) HealthStatus {
	// Create a short-timeout context for TCP dial.
	dialCtx, cancel := context.WithTimeout(ctx, hv.tcpTimeout)
	defer cancel()

	dialer := net.Dialer{
		Timeout: hv.tcpTimeout,
	}

	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		hv.log.Debug("tcp health check failed",
			zap.String("addr", addr),
			zap.Error(err))
		return HealthUnhealthy
	}
	defer conn.Close()

	hv.log.Debug("tcp health check passed",
		zap.String("addr", addr))
	return HealthHealthy
}

// BatchCheck validates multiple backends concurrently, respecting overall timeout.
// Returns slice of BackendHealth in same order as input backends.
func (hv *HealthValidator) BatchCheck(ctx context.Context, containers map[string]Backend) []BackendHealth {
	results := make([]BackendHealth, 0, len(containers))
	resultChan := make(chan BackendHealth, len(containers))

	// Launch goroutines for each container.
	for containerID, backend := range containers {
		go func(cID string, b Backend) {
			resultChan <- hv.CheckHealth(ctx, cID, b)
		}(containerID, backend)
	}

	// Collect results with timeout awareness.
	for i := 0; i < len(containers); i++ {
		select {
		case result := <-resultChan:
			results = append(results, result)
		case <-ctx.Done():
			// Context expired; partial results only.
			hv.log.Warn("batch health check timeout",
				zap.Int("checked", i),
				zap.Int("total", len(containers)))
			return results
		}
	}

	return results
}

// Close releases resources.
func (hv *HealthValidator) Close() error {
	close(hv.semaphore)
	return nil
}
