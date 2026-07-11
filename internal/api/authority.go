package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/docker-secret-operator/orbit/internal/state"
	"go.uber.org/zap"
)

// Authority persistence handlers. See docs/governance/AUTHORITY-LIFECYCLE.md
// for why these exist, exactly when the CLI calls them, and why not at other
// points in the rollout sequence. Both are no-ops (200 OK, nothing written)
// when sm is nil, so callers that don't care about persistence (most tests)
// don't need to construct a StateManager.

// POST /authority/transitioning {old, new string}
// Called by internal/rollout.Run after the new backend's stability window
// passes, before the old backend is drained. Persists RolloutState so a
// crash between here and /authority/commit recovers as
// RecoveryRestoreWithDraining — both generations restored, old draining —
// instead of losing track of which container is which.
func (cs *ControlServer) handleAuthorityTransitioning(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r.Method, "POST")
		return
	}
	service, ok := cs.requireProvableService(w)
	if !ok {
		return
	}
	if cs.sm == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_body")
		return
	}
	if req.New == "" {
		writeErr(w, http.StatusBadRequest, "new is required", "missing_field")
		return
	}

	current, loadErr := cs.sm.LoadRolloutState(service)
	if loadErr != nil {
		cs.log.Warn("authority: existing rollout state unreadable, writing over it",
			zap.Error(loadErr))
		current = nil
	}

	now := time.Now()
	rs := &state.RolloutState{
		SchemaVersion:      state.SchemaVersion,
		Service:            service,
		OldGeneration:      req.Old,
		NewGeneration:      req.New,
		Phase:              state.RolloutDraining,
		Authority:          state.AuthorityTransitioning,
		StartedAt:          now,
		TransitionStart:    now,
		TransitionDeadline: now.Add(cs.transitionTimeout),
		LastProgressAt:     now,
	}
	if current != nil {
		rs.PreviousRevision = current.Revision
	}

	if err := cs.sm.WriteRolloutState(rs, cs.log); err != nil {
		cs.log.Error("authority: failed to persist transitioning state", zap.Error(err))
		writeErr(w, http.StatusInternalServerError, "failed to persist state: "+err.Error(), "internal_error")
		return
	}
	cs.log.Info("authority: transition persisted",
		zap.String("old", req.Old), zap.String("new", req.New))
	w.WriteHeader(http.StatusOK)
}

// POST /authority/commit {generation string}
// Called by internal/rollout.Run after the old backend is fully drained and
// removed (rollout complete), by Rollback on its own completion, and by the
// CLI's very first backend registration (the seed) so the second-ever boot
// — not just every boot after the first rollout — gets a trusted restore
// instead of an inferred one. Writes the new single-generation authority and
// clears any in-flight RolloutState, since there is no longer a transition
// to track.
func (cs *ControlServer) handleAuthorityCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r.Method, "POST")
		return
	}
	service, ok := cs.requireProvableService(w)
	if !ok {
		return
	}
	if cs.sm == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	var req struct {
		Generation string `json:"generation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_body")
		return
	}
	if req.Generation == "" {
		writeErr(w, http.StatusBadRequest, "generation is required", "missing_field")
		return
	}

	current, loadErr := cs.sm.LoadActiveGenerationState(service)
	if loadErr != nil {
		cs.log.Warn("authority: existing active-generation state unreadable, writing over it",
			zap.Error(loadErr))
		current = nil
	}

	ags := &state.ActiveGenerationState{
		SchemaVersion:    state.SchemaVersion,
		Service:          service,
		ActiveGeneration: req.Generation,
	}
	if current != nil {
		ags.PreviousRevision = current.Revision
	}

	if err := cs.sm.WriteActiveGenerationState(ags, cs.log); err != nil {
		cs.log.Error("authority: failed to persist committed authority", zap.Error(err))
		writeErr(w, http.StatusInternalServerError, "failed to persist state: "+err.Error(), "internal_error")
		return
	}

	// Best-effort: a rollout that never reached /authority/transitioning
	// (e.g. the very first seed commit) has no RolloutState to clear, and
	// DeleteRolloutState on a missing file is not an error worth failing
	// the request over.
	if err := cs.sm.DeleteRolloutState(service); err != nil {
		cs.log.Warn("authority: could not clear rollout state after commit", zap.Error(err))
	}

	cs.log.Info("authority: committed", zap.String("generation", req.Generation))
	w.WriteHeader(http.StatusOK)
}
