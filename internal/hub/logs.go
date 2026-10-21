package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/logbroker"
)

type logRequestKey struct{}
type logRequest struct {
	ID, Agent    string
	Chars, Lines *int
}

func requestLogFields(ctx context.Context) *logRequest {
	v, _ := ctx.Value(logRequestKey{}).(*logRequest)
	return v
}
func (s *Server) logQuery(w http.ResponseWriter, r *http.Request) (logbroker.Query, bool) {
	q := logbroker.Query{Component: "hub", Level: r.URL.Query().Get("level"), After: r.URL.Query().Get("after"), Limit: 50}
	if c := r.URL.Query().Get("component"); c != "" && c != "hub" {
		writeError(w, 400, "invalid_request", "hub logs only; use local broker for bridge logs", nil)
		return q, false
	}
	if q.After == "" {
		q.After = r.Header.Get("Last-Event-ID")
	}
	if len(q.After) > 160 {
		writeError(w, 400, "invalid_request", "invalid cursor", nil)
		return q, false
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 100 {
			writeError(w, 400, "invalid_request", "limit must be 1-100", nil)
			return q, false
		}
		q.Limit = n
	}
	if q.Level != "" && !logbroker.ValidLevel(q.Level) {
		writeError(w, 400, "invalid_request", "invalid level", nil)
		return q, false
	}
	return q, true
}
func (s *Server) readLogBatch(w http.ResponseWriter, r *http.Request, q logbroker.Query) (logbroker.Batch, bool) {
	b, err := (logbroker.Reader{Dir: s.opts.LogDir}).Read(q)
	if err == nil {
		return b, true
	}
	if errors.Is(err, logbroker.ErrCursor) {
		writeError(w, 400, "invalid_cursor", "log cursor is invalid or stream was replaced", nil)
	} else {
		writeError(w, 503, "logs_unavailable", "log files unavailable", nil)
	}
	return b, false
}
func (s *Server) handleLogTail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, ok := s.logQuery(w, r)
	if !ok {
		return
	}
	if b, ok := s.readLogBatch(w, r, q); ok {
		if !s.store.IsAdminToken(r.Context(), bearer(r)) {
			writeError(w, 401, "unauthorized", "admin token revoked", nil)
			return
		}
		writeJSON(w, 200, b)
	}
}
func (s *Server) handleLogEvents(w http.ResponseWriter, r *http.Request) {
	q, ok := s.logQuery(w, r)
	if !ok {
		return
	}
	if s.logStreams.Add(1) > 8 {
		s.logStreams.Add(-1)
		writeError(w, 429, "rate_limited", "log stream limit", nil)
		return
	}
	defer s.logStreams.Add(-1)
	first, ok := s.readLogBatch(w, r, q)
	if !ok {
		return
	}
	token := bearer(r)
	if !s.store.IsAdminToken(r.Context(), token) {
		writeError(w, 401, "unauthorized", "admin token revoked", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	defer rc.SetWriteDeadline(time.Time{})
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	batch := first
	for {
		if !s.store.IsAdminToken(r.Context(), token) {
			return
		}
		_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
		for _, entry := range batch.Entries {
			raw, _ := json.Marshal(entry)
			if _, err := fmt.Fprintf(w, "id: %s\nevent: log\ndata: %s\n\n", entry.Cursor, raw); err != nil {
				return
			}
		}
		raw, _ := json.Marshal(map[string]any{"cursor": batch.Cursor, "gap": batch.Gap, "more": batch.More})
		if _, err := fmt.Fprintf(w, "id: %s\nevent: cursor\ndata: %s\n\n", batch.Cursor, raw); err != nil {
			return
		}
		if rc.Flush() != nil {
			return
		}
		q.After = batch.Cursor
		// The same read budget applies to catch-up and live polling; an inactive
		// filtered stream cannot burn CPU scanning arbitrarily large files.
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
		}
		if !s.allow("log-read:"+hashKey(token), 120, 4) {
			return
		}
		var err error
		batch, err = (logbroker.Reader{Dir: s.opts.LogDir}).Read(q)
		if err != nil {
			return
		}
	}
}
func isLogRoute(pattern string) bool { return strings.HasPrefix(pattern, "GET /v1/admin/logs") }

// Typing is ephemeral, including when the separately reviewed F3 routes merge.
// Its per-keystroke request metadata must not become a persistent event trace.
func isTransientLogRoute(pattern string) bool {
	return pattern == "POST /v1/rooms/{room}/typing" || pattern == "POST /federation/v2/ephemeral/session" || pattern == "POST /federation/v2/ephemeral/events"
}
