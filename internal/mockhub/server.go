// Package mockhub is an in-memory implementation of the AgentChat text
// contract. It exists so the CLI and MCP client can be developed before the
// production hub is ready. It is not durable and not secure.
package mockhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentchat-lan/agentchat/internal/protocol"
)

type contextKey string

const agentContextKey contextKey = "agent-id"

// Server is an in-memory hub.
type Server struct {
	mu          sync.Mutex
	rooms       map[string]protocol.Room
	agents      map[string]protocol.Agent
	tokens      map[string]string // token -> agent ID
	messages    map[string][]protocol.Message
	seq         map[string]int64
	acks        map[string]map[string]int64
	idempotency map[string]protocol.Message
	files       map[string]protocol.FileRef
	fileData    map[string][]byte
	fileByHash  map[string]string
	notify      chan struct{}
}

// New returns a server with the default room.
func New() *Server {
	now := time.Now().UTC()
	s := &Server{
		rooms: map[string]protocol.Room{
			protocol.DefaultRoom: {
				ID:        protocol.DefaultRoom,
				Name:      protocol.DefaultRoom,
				Topic:     "General agent chat",
				Kind:      "room",
				CreatedAt: now,
			},
		},
		agents:      make(map[string]protocol.Agent),
		tokens:      make(map[string]string),
		messages:    make(map[string][]protocol.Message),
		seq:         make(map[string]int64),
		acks:        make(map[string]map[string]int64),
		idempotency: make(map[string]protocol.Message),
		files:       make(map[string]protocol.FileRef),
		fileData:    make(map[string][]byte),
		fileByHash:  make(map[string]string),
		notify:      make(chan struct{}),
	}
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /v1/join", s.handleJoin)
	mux.Handle("GET /v1/me", s.auth(http.HandlerFunc(s.handleMe)))
	mux.Handle("GET /v1/rooms", s.auth(http.HandlerFunc(s.handleRooms)))
	mux.Handle("GET /v1/rooms/{room}/messages", s.auth(http.HandlerFunc(s.handleHistory)))
	mux.Handle("POST /v1/rooms/{room}/messages", s.auth(http.HandlerFunc(s.handleSend)))
	mux.Handle("GET /v1/inbox", s.auth(http.HandlerFunc(s.handleInbox)))
	mux.Handle("POST /v1/ack", s.auth(http.HandlerFunc(s.handleAck)))
	mux.Handle("GET /v1/agents", s.auth(http.HandlerFunc(s.handleAgents)))
	mux.Handle("POST /v1/files", s.auth(http.HandlerFunc(s.handleUpload)))
	mux.Handle("GET /v1/files/{id}", s.auth(http.HandlerFunc(s.handleDownload)))
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, protocol.HealthResponse{Status: "ok", Version: "mock"})
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req protocol.JoinRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := protocol.ValidateAgentName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if req.Kind == "" {
		req.Kind = "generic"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.agents {
		if a.Name == req.Name {
			writeError(w, http.StatusConflict, "conflict", "agent name is already in use", nil)
			return
		}
	}

	now := time.Now().UTC()
	agent := protocol.Agent{
		ID:           "agt_" + protocol.NewID(),
		Name:         req.Name,
		Kind:         req.Kind,
		Status:       protocol.StatusOnline,
		LastSeen:     now,
		Capabilities: req.Capabilities,
	}
	token := protocol.NewToken("mock")
	s.agents[agent.ID] = agent
	s.tokens[token] = agent.ID
	s.acks[agent.ID] = make(map[string]int64)

	rooms := make([]protocol.Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		rooms = append(rooms, room)
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].ID < rooms[j].ID })

	writeJSON(w, http.StatusCreated, protocol.JoinResponse{
		Agent:      agent,
		Token:      token,
		Rooms:      rooms,
		HubVersion: "mock",
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	agentID := agentIDFromContext(r.Context())
	s.mu.Lock()
	agent, ok := s.agents[agentID]
	rooms := make([]string, 0, len(s.rooms))
	for id := range s.rooms {
		rooms = append(rooms, id)
	}
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unknown agent", nil)
		return
	}
	sort.Strings(rooms)
	writeJSON(w, http.StatusOK, protocol.MeResponse{
		Agent:  agent,
		Rooms:  rooms,
		Limits: protocol.DefaultLimits(),
	})
}

func (s *Server) handleRooms(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	rooms := make([]protocol.Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		rooms = append(rooms, room)
	}
	s.mu.Unlock()
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].ID < rooms[j].ID })
	writeJSON(w, http.StatusOK, protocol.RoomsResponse{Rooms: rooms})
}

func (s *Server) handleAgents(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	agents := make([]protocol.Agent, 0, len(s.agents))
	for _, agent := range s.agents {
		agents = append(agents, agent)
	}
	s.mu.Unlock()
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	writeJSON(w, http.StatusOK, protocol.AgentsResponse{Agents: agents})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, protocol.MaxFileSize+(16<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "file is too large", map[string]any{"max": protocol.MaxFileSize})
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid multipart upload: "+err.Error(), nil)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "multipart field \"file\" is required", nil)
		return
	}
	defer file.Close()

	tmp, err := os.CreateTemp("", "agentchat-mock-upload-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "cannot create upload temp file", nil)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer tmp.Close()

	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(file, protocol.MaxFileSize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "cannot read upload: "+err.Error(), nil)
		return
	}
	if size > protocol.MaxFileSize {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "file is too large", map[string]any{"max": protocol.MaxFileSize})
		return
	}
	sum := hex.EncodeToString(hash.Sum(nil))

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "cannot rewind upload", nil)
		return
	}
	data, err := io.ReadAll(tmp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "cannot read upload", nil)
		return
	}

	name := sanitizeFilename(header.Filename)
	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" && len(data) > 0 {
		mimeType = http.DetectContentType(data)
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	s.mu.Lock()
	if existingID, ok := s.fileByHash[sum]; ok {
		existing := s.files[existingID]
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, existing)
		return
	}
	ref := protocol.FileRef{
		ID:     "fil_" + protocol.NewID(),
		Name:   name,
		Size:   size,
		SHA256: sum,
		Mime:   mimeType,
	}
	s.files[ref.ID] = ref
	s.fileData[ref.ID] = data
	s.fileByHash[sum] = ref.ID
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, ref)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	ref, ok := s.files[id]
	data := append([]byte(nil), s.fileData[id]...)
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "file not found", map[string]any{"file": id})
		return
	}

	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": ref.Name})
	if disposition == "" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", ref.Mime)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-SHA256", ref.SHA256)
	w.Header().Set("Content-Length", strconv.FormatInt(ref.Size, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		log.Printf("mockhub: write file %s: %v", id, err)
	}
}

func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "upload.bin"
	}
	if len(name) > 255 {
		name = name[len(name)-255:]
	}
	return name
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("room")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	limit := parseLimit(r.URL.Query().Get("limit"), 50, 200)

	s.mu.Lock()
	_, ok := s.rooms[roomID]
	messages := append([]protocol.Message(nil), s.messages[roomID]...)
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "room not found", map[string]any{"room": roomID})
		return
	}

	out := make([]protocol.Message, 0, limit)
	for _, m := range messages {
		if m.Seq > after {
			out = append(out, m)
			if len(out) == limit {
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": out,
		"has_more": len(out) == limit,
	})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("room")
	agentID := agentIDFromContext(r.Context())

	var req protocol.SendMessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Files) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "text or files are required", nil)
		return
	}
	if len(req.Text) > protocol.MaxTextBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "message text is too large", map[string]any{"max_bytes": protocol.MaxTextBytes})
		return
	}
	if len(req.Files) > protocol.MaxFilesPerMessage {
		writeError(w, http.StatusBadRequest, "invalid_request", "too many files", map[string]any{"max": protocol.MaxFilesPerMessage})
		return
	}
	if len(req.To) > protocol.MaxMentions {
		writeError(w, http.StatusBadRequest, "invalid_request", "too many recipients", map[string]any{"max": protocol.MaxMentions})
		return
	}
	if req.Meta.Hops > protocol.MaxHops {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "hop limit exceeded", map[string]any{"max_hops": protocol.MaxHops})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	room, ok := s.rooms[roomID]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "room not found", map[string]any{"room": roomID})
		return
	}
	agent, ok := s.agents[agentID]
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unknown agent", nil)
		return
	}

	files := make([]protocol.FileRef, 0, len(req.Files))
	for _, fileID := range req.Files {
		ref, ok := s.files[fileID]
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown file", map[string]any{"file": fileID})
			return
		}
		files = append(files, ref)
	}

	if req.ClientMsgID != "" {
		key := agentID + "\x00" + roomID + "\x00" + req.ClientMsgID
		if existing, ok := s.idempotency[key]; ok {
			w.Header().Set("Idempotent-Replayed", "true")
			writeJSON(w, http.StatusOK, existing)
			return
		}
	}

	s.seq[roomID]++
	now := time.Now().UTC()
	mentions := protocol.MentionNames(req.Text)
	seen := make(map[string]struct{}, len(mentions)+len(req.To))
	for _, name := range mentions {
		seen[name] = struct{}{}
	}
	for _, name := range req.To {
		if _, ok := seen[name]; !ok {
			mentions = append(mentions, name)
			seen[name] = struct{}{}
		}
	}

	msg := protocol.Message{
		ID:       protocol.NewID(),
		Room:     room.ID,
		Seq:      s.seq[roomID],
		From:     protocol.AgentRef{ID: agent.ID, Name: agent.Name, Kind: agent.Kind},
		Kind:     protocol.KindChat,
		Text:     req.Text,
		ReplyTo:  req.ReplyTo,
		Mentions: mentions,
		Files:    files,
		Meta:     req.Meta,
		TS:       now,
	}
	s.messages[roomID] = append(s.messages[roomID], msg)
	if req.ClientMsgID != "" {
		s.idempotency[agentID+"\x00"+roomID+"\x00"+req.ClientMsgID] = msg
	}
	s.broadcastLocked()

	writeJSON(w, http.StatusCreated, msg)
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	agentID := agentIDFromContext(r.Context())
	wait := parseWait(r.URL.Query().Get("wait"))
	limit := parseLimit(r.URL.Query().Get("limit"), 50, 200)
	deadline := time.Now().Add(time.Duration(wait) * time.Second)

	for {
		s.mu.Lock()
		notify := s.notify
		messages := s.collectLocked(agentID, limit)
		s.mu.Unlock()

		if len(messages) > 0 || wait == 0 {
			writeJSON(w, http.StatusOK, protocol.InboxResponse{
				Messages:   messages,
				ServerTime: time.Now().UTC(),
			})
			return
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			writeJSON(w, http.StatusOK, protocol.InboxResponse{
				Messages:   []protocol.Message{},
				ServerTime: time.Now().UTC(),
			})
			return
		}

		timer := time.NewTimer(remaining)
		select {
		case <-notify:
			timer.Stop()
		case <-timer.C:
			writeJSON(w, http.StatusOK, protocol.InboxResponse{
				Messages:   []protocol.Message{},
				ServerTime: time.Now().UTC(),
			})
			return
		case <-r.Context().Done():
			timer.Stop()
			return
		}
	}
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	agentID := agentIDFromContext(r.Context())
	var req protocol.AckRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acks[agentID] == nil {
		s.acks[agentID] = make(map[string]int64)
	}
	for room, seq := range req.Acks {
		if _, ok := s.rooms[room]; !ok {
			continue
		}
		if seq > s.acks[agentID][room] {
			s.acks[agentID][room] = seq
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) collectLocked(agentID string, limit int) []protocol.Message {
	acks := s.acks[agentID]
	var out []protocol.Message
	for roomID, messages := range s.messages {
		for _, m := range messages {
			if m.Seq > acks[roomID] {
				out = append(out, m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TS.Equal(out[j].TS) {
			if out[i].Room == out[j].Room {
				return out[i].Seq < out[j].Seq
			}
			return out[i].Room < out[j].Room
		}
		return out[i].TS.Before(out[j].TS)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []protocol.Message{}
	}
	return out
}

func (s *Server) broadcastLocked() {
	close(s.notify)
	s.notify = make(chan struct{})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token", nil)
			return
		}
		s.mu.Lock()
		agentID, ok := s.tokens[token]
		if ok {
			agent := s.agents[agentID]
			agent.LastSeen = time.Now().UTC()
			agent.Status = protocol.StatusOnline
			s.agents[agentID] = agent
		}
		s.mu.Unlock()
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or revoked token", nil)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), agentContextKey, agentID)))
	})
}

func agentIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(agentContextKey).(string)
	return id
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix))
}

func parseWait(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	if n > 60 {
		return 60
	}
	return n
}

func parseLimit(raw string, def, max int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_request", "request body is required", nil)
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON: "+err.Error(), nil)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("mockhub: encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSON(w, status, protocol.ErrorResponse{
		Error: protocol.ErrorBody{Code: code, Message: message, Details: details},
	})
}
