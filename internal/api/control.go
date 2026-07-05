// Package api implements the Orbit HTTP control plane.
//
// The control API runs on the docker-rollout-proxy container at ORBIT_CONTROL_PORT
// (default: 9900). It is reachable only from within the docker_rollout_mesh bridge
// network — not from the Docker host.
//
// Optional bearer-token authentication is enabled when the ORBIT_API_TOKEN
// environment variable is set on the proxy container. If unset, a warning is
// logged at startup (the API still works).
package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/docker-secret-operator/orbit/internal/metrics"
	"github.com/docker-secret-operator/orbit/internal/proxy"
	"go.uber.org/zap"
)

// ControlServer exposes the Orbit backend management API over HTTP.
type ControlServer struct {
	reg            *proxy.Registry
	srv            *proxy.Server
	log            *zap.Logger
	metrics        *metrics.Proxy
	startTime      time.Time
	token          string // empty → unauthenticated (warning at startup)
	rateLimiter    *RateLimiter
	ln             net.Listener
	mux            *http.ServeMux
	startupState   proxy.StartupState // Current startup state
	startupStateMu sync.RWMutex       // Protects startupState
	debug          *DebugHandler      // Nil until SetDebugHandler is called.
	service        string             // Proxy instance identifier, for StatusReport.
	version        string             // Runtime version, for StatusReport.
	recoveryState                     // POST /recover trigger + in-flight guard (recovery.go)
}

// NewControlServer creates a control server backed by reg, srv, and m.
// apiToken provides optional bearer token authentication.
func NewControlServer(reg *proxy.Registry, srv *proxy.Server, log *zap.Logger, m *metrics.Proxy, apiToken string) *ControlServer {
	if apiToken == "" {
		log.Warn("control API: unauthenticated (set ORBIT_API_TOKEN to secure)")
	}

	cs := &ControlServer{
		reg:          reg,
		srv:          srv,
		log:          log,
		metrics:      m,
		startTime:    time.Now(),
		token:        apiToken,
		rateLimiter:  NewRateLimiter(100),
		mux:          http.NewServeMux(),
		startupState: proxy.StartupReady, // Caller overrides during actual proxy startup
	}
	cs.registerRoutes()
	return cs
}

// SetDebugHandler attaches the debug/status data source. Must be called
// before ListenAndServe for GET /status to return generation and recovery
// data; without it, /status still works but omits those fields (backend
// health and traffic target are always available, since they come from the
// registry directly).
func (cs *ControlServer) SetDebugHandler(dh *DebugHandler, service, version string) {
	cs.debug = dh
	cs.service = service
	cs.version = version
}

// SetStartupState sets the current startup state (called after recovery completes).
func (cs *ControlServer) SetStartupState(state proxy.StartupState) {
	cs.startupStateMu.Lock()
	defer cs.startupStateMu.Unlock()
	cs.startupState = state
}

// GetStartupState returns the current startup state.
func (cs *ControlServer) GetStartupState() proxy.StartupState {
	cs.startupStateMu.RLock()
	defer cs.startupStateMu.RUnlock()
	return cs.startupState
}

// ListenAndServe binds to addr (e.g. ":9900") and serves requests.
// It blocks until the server is stopped via Close().
func (cs *ControlServer) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("control: listen %s: %w", addr, err)
	}
	cs.ln = ln
	cs.log.Info("control API listening", zap.String("addr", addr))

	// Wrap mux with security middleware.
	handler := cs.middleware(cs.mux)
	return http.Serve(ln, handler) //nolint:wrapcheck
}

// Close shuts down the HTTP listener and rate limiter.
func (cs *ControlServer) Close() error {
	cs.rateLimiter.Close() //nolint:errcheck // rate-limiter close on shutdown; error not actionable
	if cs.ln != nil {
		return cs.ln.Close()
	}
	return nil
}

// Handler returns the underlying http.Handler for use with httptest.NewServer.
func (cs *ControlServer) Handler() http.Handler { return cs.mux }

// ── Route registration ────────────────────────────────────────────────────────

func (cs *ControlServer) registerRoutes() {
	// Legacy health (kept for backward compat with existing compose healthchecks).
	cs.mux.HandleFunc("/health", cs.handleHealth)

	// Split health: liveness (is the process up?) and readiness (can it serve traffic?).
	cs.mux.HandleFunc("/health/live", cs.handleLive)
	cs.mux.HandleFunc("/health/ready", cs.handleReady)

	// Prometheus text metrics — no auth, safe to scrape from internal network.
	cs.mux.HandleFunc("/metrics", cs.handleMetrics)

	// Backend management — auth-protected.
	cs.mux.HandleFunc("/backends", cs.auth(cs.handleBackends))
	cs.mux.HandleFunc("/backends/", cs.auth(cs.handleBackendByID))

	// Consolidated status — read-only, no auth (same trust level as /metrics).
	cs.mux.HandleFunc("/status", cs.handleStatus)

	// On-demand recovery trigger — mutates the registry, auth-protected like
	// backend management.
	cs.mux.HandleFunc("/recover", cs.auth(cs.handleRecover))
}

// GET /status — consolidated deployment/proxy/recovery status. Backing for
// `docker orbit status`. See StatusReport and BuildStatusReport for what
// this actually computes and why.
func (cs *ControlServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r.Method, "GET")
		return
	}
	report := BuildStatusReport(r.Context(), cs.service, cs.version, cs.GetStartupState(), cs.reg, cs.debug)
	writeJSON(w, http.StatusOK, report)
}

// ── Health handlers ───────────────────────────────────────────────────────────

// GET /health — legacy endpoint, kept for backward compatibility.
func (cs *ControlServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r.Method, "GET")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "ok",
		"backends": cs.reg.Len(),
	})
}

// GET /health/live — liveness probe.
// Always returns 200 while the process is running. Use this for Docker
// HEALTHCHECK and Kubernetes livenessProbe. A failing liveness probe triggers
// a container restart.
func (cs *ControlServer) handleLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r.Method, "GET")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "ok",
		"uptime_seconds": time.Since(cs.startTime).Seconds(),
	})
}

// GET /health/ready — readiness probe.
// Returns 200 if in ready/degraded state with at least one active backend.
// Returns 503 if failed, recovering, or no backends registered.
// Use for Docker HEALTHCHECK and Kubernetes readinessProbe.
func (cs *ControlServer) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r.Method, "GET")
		return
	}

	state := cs.GetStartupState()
	active := cs.reg.Active()

	// CRITICAL: Respect startup state, not just backend count.
	// Failed/Recovering states are not ready, even if backends exist.
	switch state {
	case proxy.StartupFailed:
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"status":   "not_ready",
			"reason":   "startup failed - no healthy backends",
			"state":    string(state),
			"backends": len(active),
		})
		return

	case proxy.StartupRecovering, proxy.StartupStarting:
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"status":   "not_ready",
			"reason":   "still starting up",
			"state":    string(state),
			"backends": len(active),
		})
		return

	case proxy.StartupDegraded:
		// Degraded is OK to serve (partial failure accepted).
		if len(active) == 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
				"status": "not_ready",
				"reason": "degraded state with no active backends",
				"state":  string(state),
			})
			return
		}
		// Degraded with backends is ready.
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":          "ready",
			"state":           string(state),
			"active_backends": len(active),
		})
		return

	case proxy.StartupReady:
		// Ready state.
		if len(active) == 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
				"status": "not_ready",
				"reason": "no active backends",
				"state":  string(state),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":          "ready",
			"state":           string(state),
			"active_backends": len(active),
		})
		return

	default:
		// Unknown state.
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"status": "not_ready",
			"reason": "unknown startup state",
			"state":  string(state),
		})
	}
}

// GET /metrics — Prometheus text format.
// Exposes proxy-level counters (connections, errors, uptime) plus per-backend
// request counts. Scrape this from Prometheus or any OpenMetrics collector.
func (cs *ControlServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r.Method, "GET")
		return
	}
	all := cs.reg.Backends()
	active := cs.reg.Active()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	cs.metrics.WritePrometheus(w, len(all), len(active))

	// Per-backend request counts.
	fmt.Fprint(w, "# HELP orbit_backend_requests_total Requests routed to each backend\n")
	fmt.Fprint(w, "# TYPE orbit_backend_requests_total counter\n")
	for _, b := range all {
		fmt.Fprintf(w, "orbit_backend_requests_total{id=%q,addr=%q,draining=%q} %d\n",
			b.ID, b.Addr, fmt.Sprintf("%v", b.Draining), b.MarshalledRequests())
	}
}

// ── Backend CRUD ──────────────────────────────────────────────────────────────

// GET /backends        → list all
// POST /backends       → register a new backend
func (cs *ControlServer) handleBackends(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cs.listBackends(w, r)
	case http.MethodPost:
		cs.addBackend(w, r)
	default:
		methodNotAllowed(w, r.Method, "GET, POST")
	}
}

// DELETE /backends/{id}        → drain + deregister
// PUT    /backends/{id}/drain  → mark draining only
func (cs *ControlServer) handleBackendByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/backends/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]

	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing backend ID in URL", "invalid_path")
		return
	}

	if len(parts) == 2 && parts[1] == "drain" {
		if r.Method != http.MethodPut {
			methodNotAllowed(w, r.Method, "PUT")
			return
		}
		cs.drainBackend(w, id)
		return
	}

	if r.Method != http.MethodDelete {
		methodNotAllowed(w, r.Method, "DELETE")
		return
	}
	cs.removeBackend(w, id)
}

// ── Individual actions ────────────────────────────────────────────────────────

func (cs *ControlServer) listBackends(w http.ResponseWriter, _ *http.Request) {
	all := cs.reg.Backends()
	type row struct {
		ID       string `json:"id"`
		Addr     string `json:"addr"`
		Draining bool   `json:"draining"`
		State    string `json:"state"`
		Requests uint64 `json:"requests"`
	}
	rows := make([]row, len(all))
	for i, b := range all {
		rows[i] = row{
			ID:       b.ID,
			Addr:     b.Addr,
			Draining: b.Draining,
			State:    string(b.State),
			Requests: b.MarshalledRequests(),
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"backends": rows,
		"count":    len(rows),
	})
}

func (cs *ControlServer) addBackend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Addr string `json:"addr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_body")
		return
	}
	if req.Addr == "" {
		writeErr(w, http.StatusBadRequest, "addr is required", "missing_field")
		return
	}
	if req.ID == "" {
		req.ID = req.Addr
	}

	b := proxy.Backend{ID: req.ID, Addr: req.Addr}
	if err := cs.reg.Add(b); err != nil {
		code := http.StatusInternalServerError
		errCode := "internal_error"
		if strings.Contains(err.Error(), "already registered") {
			code = http.StatusConflict
			errCode = "conflict"
		}
		writeErr(w, code, err.Error(), errCode)
		return
	}
	cs.log.Info("backend registered", zap.String("id", req.ID), zap.String("addr", req.Addr))

	registered, _ := cs.reg.Get(req.ID)
	writeJSON(w, http.StatusCreated, registered)
}

func (cs *ControlServer) drainBackend(w http.ResponseWriter, id string) {
	if err := cs.reg.SetDraining(id); err != nil {
		code := http.StatusNotFound
		if !strings.Contains(err.Error(), "not found") {
			code = http.StatusInternalServerError
		}
		writeErr(w, code, err.Error(), "not_found")
		return
	}
	cs.log.Info("backend draining", zap.String("id", id))
	w.WriteHeader(http.StatusNoContent)
}

func (cs *ControlServer) removeBackend(w http.ResponseWriter, id string) {
	_ = cs.reg.SetDraining(id) // best-effort pre-drain

	if err := cs.reg.Remove(id); err != nil {
		code := http.StatusNotFound
		if !strings.Contains(err.Error(), "not found") {
			code = http.StatusInternalServerError
		}
		writeErr(w, code, err.Error(), "not_found")
		return
	}
	cs.log.Info("backend removed", zap.String("id", id))
	w.WriteHeader(http.StatusNoContent)
}

// ── Security middleware ───────────────────────────────────────────────────────

// middleware wraps handler with rate limiting and audit logging.
func (cs *ControlServer) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Rate limiting.
		ip := clientIP(r)
		if !cs.rateLimiter.Allow(ip) {
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded", "rate_limit_exceeded")
			cs.log.Warn("rate limit exceeded", zap.String("ip", ip))
			return
		}

		// Audit logging for mutations.
		if r.Method != "GET" && r.Method != "HEAD" {
			cs.log.Info("audit: backend mutation",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.String("ip", ip))
		}

		next.ServeHTTP(w, r)
	})
}

// ── Auth middleware ───────────────────────────────────────────────────────────

func (cs *ControlServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cs.token == "" {
			next(w, r)
			return
		}
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			writeErr(w, http.StatusUnauthorized, "Bearer token required", "unauthorized")
			return
		}
		provided := strings.TrimPrefix(authHeader, "Bearer ")
		// Constant-time compare to avoid leaking the token via timing.
		if subtle.ConstantTimeCompare([]byte(provided), []byte(cs.token)) != 1 {
			writeErr(w, http.StatusForbidden, "invalid token", "forbidden")
			return
		}
		next(w, r)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeErr(w http.ResponseWriter, status int, msg, code string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func methodNotAllowed(w http.ResponseWriter, got, allowed string) {
	w.Header().Set("Allow", allowed)
	writeErr(w, http.StatusMethodNotAllowed,
		fmt.Sprintf("method %s not allowed; use %s", got, allowed),
		"method_not_allowed")
}

// clientIP extracts client IP from request.
func clientIP(r *http.Request) string {
	// Check X-Forwarded-For (from proxy).
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		return strings.TrimSpace(ips[0])
	}
	// Fall back to RemoteAddr. Use SplitHostPort so IPv6 addresses
	// (e.g. "[::1]:54321") yield the real host, not a bare "[".
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
