package api

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	maxLimiters     = 10000
	ttl             = 5 * time.Minute
	cleanupInterval = 1 * time.Minute
)

// RateLimiter manages per-IP rate limiting with automatic TTL cleanup.
type RateLimiter struct {
	limiters map[string]*rate.Limiter
	lastSeen map[string]time.Time
	rpsLimit int
	mu       sync.RWMutex
	ticker   *time.Ticker
	done     chan struct{}
}

// NewRateLimiter creates a per-IP rate limiter.
func NewRateLimiter(rpsLimit int) *RateLimiter {
	rl := &RateLimiter{
		limiters: make(map[string]*rate.Limiter),
		lastSeen: make(map[string]time.Time),
		rpsLimit: rpsLimit,
		ticker:   time.NewTicker(cleanupInterval),
		done:     make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

// Allow returns true if the IP is within rate limit.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// DDoS protection: reject if at max capacity.
	if len(rl.limiters) >= maxLimiters {
		rl.evictOldest()
	}

	limiter, exists := rl.limiters[ip]
	if !exists {
		// Burst == rpsLimit (the standard golang.org/x/time/rate idiom):
		// a single client legitimately issues several rapid sequential
		// requests per CLI invocation (doctor: ~4 GETs, deploy: preflight +
		// plan + result, recover: status + recover), all from the same IP
		// within milliseconds. A burst of 1 rate-limited that single
		// invocation against itself; sustained abuse is still capped at
		// rpsLimit/sec either way.
		limiter = rate.NewLimiter(rate.Limit(rl.rpsLimit), rl.rpsLimit)
		rl.limiters[ip] = limiter
	}

	// Update last-seen time.
	rl.lastSeen[ip] = time.Now()

	return limiter.Allow()
}

// evictOldest removes the least-recently-seen IP.
func (rl *RateLimiter) evictOldest() {
	if len(rl.lastSeen) == 0 {
		return
	}

	var oldest string
	oldestTime := time.Now()

	for ip, t := range rl.lastSeen {
		if t.Before(oldestTime) {
			oldest = ip
			oldestTime = t
		}
	}

	delete(rl.limiters, oldest)
	delete(rl.lastSeen, oldest)
}

// cleanupLoop removes old limiters.
func (rl *RateLimiter) cleanupLoop() {
	for {
		select {
		case <-rl.ticker.C:
			rl.cleanup()
		case <-rl.done:
			rl.ticker.Stop()
			return
		}
	}
}

// cleanup removes entries older than TTL.
func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	for ip, lastSeen := range rl.lastSeen {
		if now.Sub(lastSeen) > ttl {
			delete(rl.limiters, ip)
			delete(rl.lastSeen, ip)
		}
	}
}

// Len returns active limiter count.
func (rl *RateLimiter) Len() int {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	return len(rl.limiters)
}

// Close stops the cleanup goroutine.
func (rl *RateLimiter) Close() error {
	close(rl.done)
	return nil
}
