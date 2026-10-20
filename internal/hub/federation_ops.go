package hub

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Dofu27/agentchat-lan/internal/store"
)

func (s *Server) handleFederationOperatorStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.store.FederationOperatorStatus(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, struct {
		RuntimeEnabled bool `json:"runtime_enabled"`
		store.FederationOperatorStatus
	}{true, status})
}
func (s *Server) handleFederationOperatorOutbox(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "dead"
	}
	limit := 50
	if raw, ok := r.URL.Query()["limit"]; ok {
		var err error
		if len(raw) != 1 {
			writeError(w, 400, "invalid_request", "expected one limit", nil)
			return
		}
		limit, err = strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, 400, "invalid_request", "limit must be 1-100", nil)
			return
		}
	}
	if !store.ValidFederationState(state) {
		writeError(w, 400, "invalid_request", "invalid outbox state", nil)
		return
	}
	items, err := s.store.ListFederationOutbox(r.Context(), state, limit)
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) handleFederationOperatorRetry(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) == 0 || len(key) > 128 || invalidRetryKey(key) {
		writeError(w, 400, "invalid_request", "Idempotency-Key of 1-128 printable non-whitespace bytes is required", nil)
		return
	}
	result, err := s.store.RetryFederationOutbox(r.Context(), r.PathValue("id"), key)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, 404, "not_found", "outbox job not found", nil)
		case errors.Is(err, store.ErrFederationNotDead):
			writeError(w, 409, "not_dead", "only dead outbox jobs may be retried", nil)
		case errors.Is(err, store.ErrForbidden):
			writeError(w, 403, "forbidden", "federation is disabled", nil)
		default:
			s.internalError(w, err)
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func invalidRetryKey(key string) bool {
	for _, c := range key {
		if c <= 32 || c >= 127 {
			return true
		}
	}
	return false
}
