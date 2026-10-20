package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleEvents streams new room messages as Server-Sent Events. The browser
// uses fetch() rather than EventSource so the bearer token stays in an
// Authorization header instead of a URL.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	after, err := queryInt(r, "after_seq", 0)
	if err != nil || after < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "after_seq must be a non-negative integer", nil)
		return
	}
	agentID := agentFrom(r.Context()).ID
	roomID := r.PathValue("room")

	// Validate room membership before sending SSE headers so errors remain JSON.
	if _, _, err := s.store.History(r.Context(), agentID, roomID, int64(after), 0, 1); err != nil {
		s.storeError(w, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "streaming is not supported", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 3000\n\nevent: ready\ndata: {}\n\n")
	flusher.Flush()

	last := int64(after)
	for {
		ch := s.eventChannel()
		for {
			messages, more, err := s.store.History(r.Context(), agentID, roomID, last, 0, 200)
			if err != nil {
				return
			}
			for _, message := range messages {
				data, err := json.Marshal(message)
				if err != nil {
					continue
				}
				fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", message.Seq, data)
				last = message.Seq
			}
			flusher.Flush()
			if !more {
				break
			}
		}

		timer := time.NewTimer(15 * time.Second)
		select {
		case <-ch:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func (s *Server) eventChannel() <-chan struct{} {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	return s.notify
}
