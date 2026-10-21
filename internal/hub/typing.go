package hub

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

func isBroadcastMention(name string) bool {
	return strings.EqualFold(name, "all") || strings.EqualFold(name, "everyone")
}

type typingRequest struct {
	Typing bool `json:"typing"`
}

func (s *Server) handleTyping(w http.ResponseWriter, r *http.Request) {
	if s.federatedDMControlDisabled(w, r) {
		return
	}
	var req typingRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	agent := agentFrom(r.Context())
	room := r.PathValue("room")
	if _, _, err := s.store.History(r.Context(), agent.ID, room, 0, 0, 1); err != nil {
		s.storeError(w, err)
		return
	}

	s.typingMu.Lock()
	if req.Typing {
		if s.typing[room] == nil {
			s.typing[room] = map[string]typingEntry{}
		}
		s.typing[room][agent.ID] = typingEntry{name: agent.Name, expires: time.Now().Add(5 * time.Second)}
	} else if s.typing[room] != nil {
		delete(s.typing[room], agent.ID)
		if len(s.typing[room]) == 0 {
			delete(s.typing, room)
		}
	}
	s.typingMu.Unlock()
	s.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) typingSnapshot(room string) []string {
	now := time.Now()
	s.typingMu.Lock()
	defer s.typingMu.Unlock()
	entries := s.typing[room]
	names := make([]string, 0, len(entries))
	for id, entry := range entries {
		if now.After(entry.expires) {
			delete(entries, id)
			continue
		}
		names = append(names, entry.name)
	}
	if len(entries) == 0 {
		delete(s.typing, room)
	}
	sort.Strings(names)
	return names
}
