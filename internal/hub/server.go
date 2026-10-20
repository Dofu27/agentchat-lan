// Package hub implements the production HTTP API over the durable store.
package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
	"github.com/Dofu27/agentchat-lan/web"
)

const Version = "0.1.0"

type agentContextKey struct{}

// Options controls limits and operator logging. Zero values select contract defaults.
type Options struct {
	Version               string
	RequestsPerMinute     int
	Burst                 int
	JoinRequestsPerMinute int
	FileUploadsPerMinute  int
	MaxFileSize           int64
	MaxStorageBytes       int64
	FileDir               string
	Logger                *log.Logger
}

type Server struct {
	store    *store.Store
	files    *fileService
	opts     Options
	notifyMu sync.Mutex
	notify   chan struct{}
	limitMu  sync.Mutex
	buckets  map[string]bucket
	typingMu sync.Mutex
	typing   map[string]map[string]typingEntry
}

type bucket struct {
	tokens  float64
	updated time.Time
}

type typingEntry struct {
	name    string
	expires time.Time
}

// Init creates the database and returns the admin token exactly once. Only its
// SHA-256 hash is retained in SQLite.
func Init(ctx context.Context, dataPath string) (string, error) {
	s, err := store.Open(ctx, dataPath)
	if err != nil {
		return "", err
	}
	defer s.Close()
	token, created, err := s.EnsureAdminToken(ctx)
	if err != nil {
		return "", err
	}
	if !created {
		return "", errors.New("hub is already initialized; admin token is not recoverable")
	}
	return token, nil
}

// New constructs an HTTP server around an opened store.
func New(st *store.Store, opts Options) *Server {
	if opts.Version == "" {
		opts.Version = Version
	}
	if opts.RequestsPerMinute <= 0 {
		opts.RequestsPerMinute = 60
	}
	if opts.Burst <= 0 {
		opts.Burst = 20
	}
	if opts.JoinRequestsPerMinute <= 0 {
		opts.JoinRequestsPerMinute = 20
	}
	if opts.FileUploadsPerMinute <= 0 {
		opts.FileUploadsPerMinute = 10
	}
	if opts.MaxFileSize <= 0 {
		opts.MaxFileSize = protocol.MaxFileSize
	}
	if opts.FileDir == "" {
		opts.FileDir = filepath.Join("data", "files")
	}
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	return &Server{store: st, files: &fileService{root: opts.FileDir, maxSize: opts.MaxFileSize}, opts: opts, notify: make(chan struct{}), buckets: map[string]bucket{}, typing: map[string]map[string]typingEntry{}}
}

// Handler returns the contract HTTP handler. It listens on no socket itself.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", s.uiSecurity(http.HandlerFunc(s.handleUI)))
	mux.Handle("GET /admin", s.uiSecurity(http.HandlerFunc(s.handleAdminUI)))
	mux.Handle("GET /static/", s.uiSecurity(http.StripPrefix("/static/", http.FileServer(http.FS(web.FS)))))
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.Handle("POST /v1/join", s.joinRate(http.HandlerFunc(s.handleJoin)))
	mux.Handle("GET /v1/me", s.agentAuth(http.HandlerFunc(s.handleMe)))
	mux.Handle("GET /v1/rooms", s.agentAuth(http.HandlerFunc(s.handleRooms)))
	mux.Handle("GET /v1/rooms/{room}/messages", s.agentAuth(http.HandlerFunc(s.handleHistory)))
	mux.Handle("GET /v1/rooms/{room}/events", s.agentAuth(http.HandlerFunc(s.handleEvents)))
	mux.Handle("POST /v1/rooms/{room}/messages", s.agentAuth(http.HandlerFunc(s.handleSend)))
	mux.Handle("POST /v1/rooms/{room}/typing", s.agentAuth(http.HandlerFunc(s.handleTyping)))
	mux.Handle("GET /v1/inbox", s.agentAuth(http.HandlerFunc(s.handleInbox)))
	mux.Handle("POST /v1/ack", s.agentAuth(http.HandlerFunc(s.handleAck)))
	mux.Handle("GET /v1/agents", s.agentAuth(http.HandlerFunc(s.handleAgents)))
	mux.Handle("POST /v1/files", s.agentAuth(s.fileUploadRate(http.HandlerFunc(s.handleUpload))))
	mux.Handle("GET /v1/files/{id}", s.agentAuth(http.HandlerFunc(s.handleDownload)))
	mux.Handle("HEAD /v1/files/{id}", s.agentAuth(http.HandlerFunc(s.handleDownload)))
	mux.Handle("GET /v1/admin/agents", s.adminAuth(http.HandlerFunc(s.handleAgents)))
	mux.Handle("POST /v1/admin/invites", s.adminAuth(http.HandlerFunc(s.handleCreateInvite)))
	mux.Handle("GET /v1/admin/invites", s.adminAuth(http.HandlerFunc(s.handleListInvites)))
	mux.Handle("DELETE /v1/admin/agents/{id}", s.adminAuth(http.HandlerFunc(s.handleRevokeAgent)))
	mux.Handle("GET /v1/admin/audit", s.adminAuth(http.HandlerFunc(s.handleAudit)))
	mux.Handle("POST /v1/admin/rooms", s.adminAuth(http.HandlerFunc(s.handleCreateRoom)))
	mux.Handle("DELETE /v1/admin/rooms/{id}", s.adminAuth(http.HandlerFunc(s.handleArchiveRoom)))
	return s.requestLog(mux)
}

// Serve runs an HTTP listener until ctx is cancelled and performs a bounded
// graceful shutdown. The caller retains ownership of the Store.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	httpServer := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = httpServer.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	err := httpServer.Serve(ln)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, protocol.HealthResponse{Status: "ok", Version: s.opts.Version})
}
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.store.Ready(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "internal", "hub storage is unavailable", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
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
	if strings.TrimSpace(req.Kind) == "" {
		req.Kind = "generic"
	}
	req.InviteCode = strings.TrimSpace(req.InviteCode)
	if req.InviteCode == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "invite_code is required", nil)
		return
	}
	now := time.Now().UTC()
	agent := protocol.Agent{ID: "agt_" + protocol.NewID(), Name: req.Name, Kind: req.Kind, Status: protocol.StatusOnline, LastSeen: now, Capabilities: req.Capabilities}
	token := protocol.NewToken("agt")
	rooms, err := s.store.Join(r.Context(), req.InviteCode, agent, token)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInviteInvalid):
			writeError(w, http.StatusBadRequest, "invite_invalid", "invite code is invalid", nil)
		case errors.Is(err, store.ErrInviteExpired):
			writeError(w, http.StatusBadRequest, "invite_expired", "invite code has expired", nil)
		case errors.Is(err, store.ErrInviteUsed):
			writeError(w, http.StatusConflict, "invite_used", "invite code has no uses left", nil)
		case errors.Is(err, store.ErrConflict):
			writeError(w, http.StatusConflict, "conflict", "agent name is already in use", nil)
		default:
			s.internalError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, protocol.JoinResponse{Agent: agent, Token: token, Rooms: rooms, HubVersion: s.opts.Version})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	a := agentFrom(r.Context())
	rooms, err := s.store.Rooms(r.Context(), a.ID)
	if err != nil {
		s.internalError(w, err)
		return
	}
	ids := make([]string, 0, len(rooms))
	for _, room := range rooms {
		ids = append(ids, room.ID)
	}
	writeJSON(w, http.StatusOK, protocol.MeResponse{Agent: a, Rooms: ids, Limits: protocol.DefaultLimits()})
}
func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.store.Rooms(r.Context(), agentFrom(r.Context()).ID)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.RoomsResponse{Rooms: rooms})
}
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgents(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.AgentsResponse{Agents: agents})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	after, err := queryInt64(r, "after_seq", 0)
	if err != nil || after < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "after_seq must be a non-negative integer", nil)
		return
	}
	before, err := queryInt64(r, "before_seq", 0)
	if err != nil || before < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "before_seq must be a non-negative integer", nil)
		return
	}
	limit, err := queryInt(r, "limit", 50)
	if err != nil || limit < 1 {
		writeError(w, http.StatusBadRequest, "invalid_request", "limit must be positive", nil)
		return
	}
	if limit > 200 {
		limit = 200
	}
	messages, more, err := s.store.History(r.Context(), agentFrom(r.Context()).ID, r.PathValue("room"), after, before, limit)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.HistoryResponse{Messages: messages, HasMore: more})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req protocol.SendMessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Files) == 0 && req.Meta.Activity == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "text, files, or activity are required", nil)
		return
	}
	if req.Meta.Activity != nil {
		if err := req.Meta.Activity.Validate(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error(), nil)
			return
		}
	}
	if len(req.Text) > protocol.MaxTextBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "message text is too large", map[string]any{"max_bytes": protocol.MaxTextBytes})
		return
	}
	if len(req.Files) > protocol.MaxFilesPerMessage {
		writeError(w, http.StatusBadRequest, "invalid_request", "too many files", map[string]any{"max": protocol.MaxFilesPerMessage})
		return
	}
	fileIDs := make(map[string]struct{}, len(req.Files))
	for _, id := range req.Files {
		if id == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "file IDs must not be empty", nil)
			return
		}
		if _, exists := fileIDs[id]; exists {
			writeError(w, http.StatusBadRequest, "invalid_request", "duplicate file IDs are not allowed", nil)
			return
		}
		fileIDs[id] = struct{}{}
	}
	if len(req.To) > protocol.MaxMentions {
		writeError(w, http.StatusBadRequest, "invalid_request", "too many recipients", map[string]any{"max": protocol.MaxMentions})
		return
	}
	if req.Meta.Hops < 0 || req.Meta.Hops > protocol.MaxHops {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "hop limit exceeded", map[string]any{"max_hops": protocol.MaxHops})
		return
	}
	mentions := protocol.MentionNames(req.Text)
	seen := map[string]bool{}
	for _, n := range mentions {
		seen[n] = true
	}
	for _, n := range req.To {
		if !seen[n] {
			mentions = append(mentions, n)
			seen[n] = true
		}
	}
	filtered := mentions[:0]
	for _, n := range mentions {
		if !isBroadcastMention(n) {
			filtered = append(filtered, n)
		}
	}
	mentions = filtered
	result, err := s.store.SendMessage(r.Context(), agentFrom(r.Context()).ID, r.PathValue("room"), req, mentions)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !result.Replayed {
		s.broadcast()
	}
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, result.Message)
		return
	}
	writeJSON(w, http.StatusCreated, result.Message)
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	wait, err := queryInt(r, "wait", 0)
	if err != nil || wait < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "wait must be between 0 and 60", nil)
		return
	}
	if wait > 60 {
		wait = 60
	}
	limit, err := queryInt(r, "limit", 50)
	if err != nil || limit < 1 {
		writeError(w, http.StatusBadRequest, "invalid_request", "limit must be positive", nil)
		return
	}
	if limit > 200 {
		limit = 200
	}
	rooms := []string{}
	if raw := r.URL.Query().Get("rooms"); raw != "" {
		for _, v := range strings.Split(raw, ",") {
			v = strings.TrimSpace(v)
			if v != "" {
				rooms = append(rooms, v)
			}
		}
	}
	cursor, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "cursor is invalid", nil)
		return
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		s.notifyMu.Lock()
		ch := s.notify
		s.notifyMu.Unlock()
		messages, e := s.store.Inbox(r.Context(), agentFrom(r.Context()).ID, limit, rooms, cursor)
		if e != nil {
			s.internalError(w, e)
			return
		}
		if len(messages) > 0 || wait == 0 {
			writeJSON(w, http.StatusOK, protocol.InboxResponse{Messages: messages, NextCursor: encodeNextCursor(cursor, messages), ServerTime: time.Now().UTC()})
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			writeJSON(w, http.StatusOK, protocol.InboxResponse{Messages: []protocol.Message{}, NextCursor: encodeNextCursor(cursor, nil), ServerTime: time.Now().UTC()})
			return
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ch:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			writeJSON(w, http.StatusOK, protocol.InboxResponse{Messages: []protocol.Message{}, NextCursor: encodeNextCursor(cursor, nil), ServerTime: time.Now().UTC()})
			return
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
func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	var req protocol.AckRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Acks == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "acks is required", nil)
		return
	}
	for _, seq := range req.Acks {
		if seq < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "ack sequence must be non-negative", nil)
			return
		}
	}
	err := s.store.Ack(r.Context(), agentFrom(r.Context()).ID, req.Acks)
	if err != nil {
		s.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Room       string `json:"room"`
		Name       string `json:"name"`
		TTLSeconds int    `json:"ttl_seconds"`
		Uses       int    `json:"uses"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Room == "" {
		req.Room = protocol.DefaultRoom
	}
	if req.TTLSeconds == 0 {
		req.TTLSeconds = 86400
	}
	if req.TTLSeconds < 60 || req.TTLSeconds > 30*86400 {
		writeError(w, http.StatusBadRequest, "invalid_request", "ttl_seconds must be 60..2592000", nil)
		return
	}
	if req.Uses == 0 {
		req.Uses = 1
	}
	if req.Uses < 1 || req.Uses > 1000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "uses must be 1..1000", nil)
		return
	}
	code, err := newInviteCode()
	if err != nil {
		s.internalError(w, err)
		return
	}
	expiry, err := s.store.CreateInvite(r.Context(), code, store.Invite{Room: req.Room, Name: req.Name, TTL: time.Duration(req.TTLSeconds) * time.Second, Uses: req.Uses}, "admin")
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"code": code, "expires_at": expiry, "uses": req.Uses})
}
func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := s.store.ListInvites(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": invites})
}
func (s *Server) handleRevokeAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RevokeAgent(r.Context(), r.PathValue("id"), "admin"); err != nil {
		s.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 100)
	if err != nil || limit < 1 {
		writeError(w, http.StatusBadRequest, "invalid_request", "limit must be positive", nil)
		return
	}
	events, err := s.store.Audit(r.Context(), limit)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
func (s *Server) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Topic string `json:"topic"`
		Kind  string `json:"kind"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	room := protocol.Room{ID: req.ID, Name: req.Name, Topic: req.Topic, Kind: req.Kind}
	if err := s.store.CreateRoom(r.Context(), room, nil, "admin"); err != nil {
		s.storeError(w, err)
		return
	}
	rooms, err := s.store.ListRooms(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	for _, v := range rooms {
		if v.ID == req.ID {
			writeJSON(w, http.StatusCreated, v)
			return
		}
	}
	s.internalError(w, errors.New("created room not found"))
}
func (s *Server) handleArchiveRoom(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ArchiveRoom(r.Context(), r.PathValue("id"), "admin"); err != nil {
		s.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) agentAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token", nil)
			return
		}
		a, err := s.store.AgentByToken(r.Context(), token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or revoked token", nil)
			return
		}
		if !s.allow("agent:"+hashKey(token), s.opts.RequestsPerMinute, s.opts.Burst) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "slow down", nil)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), agentContextKey{}, a)))
	})
}
func (s *Server) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" || !s.store.IsAdminToken(r.Context(), token) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid admin token", nil)
			return
		}
		if !s.allow("admin:"+hashKey(token), s.opts.RequestsPerMinute, s.opts.Burst) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "slow down", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) joinRate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allow("join:"+clientIP(r), s.opts.JoinRequestsPerMinute, 5) {
			w.Header().Set("Retry-After", "3")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many join attempts", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allow(key string, perMinute, burst int) bool {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	now := time.Now()
	b, ok := s.buckets[key]
	if !ok {
		b = bucket{tokens: float64(burst), updated: now}
	} else {
		b.tokens += now.Sub(b.updated).Seconds() * float64(perMinute) / 60
		if b.tokens > float64(burst) {
			b.tokens = float64(burst)
		}
		b.updated = now
	}
	if b.tokens < 1 {
		s.buckets[key] = b
		return false
	}
	b.tokens--
	s.buckets[key] = b
	return true
}
func (s *Server) broadcast() {
	s.notifyMu.Lock()
	close(s.notify)
	s.notify = make(chan struct{})
	s.notifyMu.Unlock()
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := protocol.NewID()
		w.Header().Set("X-Request-Id", id)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		s.opts.Logger.Printf("request_id=%s method=%s path=%q status=%d duration_ms=%d", id, r.Method, r.URL.Path, sw.status, time.Since(start).Milliseconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

// Flush lets streaming handlers (the room event SSE endpoint) work through
// the request logger's response wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.wrote {
			w.WriteHeader(http.StatusOK)
		}
		f.Flush()
	}
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.opts.Logger.Printf("hub internal error: %v", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal server error", nil)
}
func (s *Server) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found", nil)
	case errors.Is(err, store.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "agent is not a member of this room", nil)
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "resource already exists", nil)
	case errors.Is(err, store.ErrInvalidTarget):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "message target is not a room member", nil)
	case errors.Is(err, store.ErrFileNotFound):
		writeError(w, http.StatusBadRequest, "invalid_request", "one or more file IDs are invalid", nil)
	default:
		s.internalError(w, err)
	}
}
func agentFrom(ctx context.Context) protocol.Agent {
	a, _ := ctx.Value(agentContextKey{}).(protocol.Agent)
	return a
}
func bearer(r *http.Request) string {
	const p = "Bearer "
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, p) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(v, p))
}
func hashKey(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func queryInt(r *http.Request, key string, def int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	return strconv.Atoi(v)
}
func queryInt64(r *http.Request, key string, def int64) (int64, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	return strconv.ParseInt(v, 10, 64)
}
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		var me *http.MaxBytesError
		if errors.As(err, &me) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large", nil)
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request", nil)
		}
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "request must contain one JSON value", nil)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSON(w, status, protocol.ErrorResponse{Error: protocol.ErrorBody{Code: code, Message: message, Details: details}})
}
func newInviteCode() (string, error) {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	v := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	return v[:4] + "-" + v[4:], nil
}
func decodeCursor(raw string) (map[string]int64, error) {
	if raw == "" {
		return map[string]int64{}, nil
	}
	if len(raw) > 8192 {
		return nil, errors.New("cursor is too large")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	v := map[string]int64{}
	if err = json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	for _, n := range v {
		if n < 0 {
			return nil, errors.New("negative cursor")
		}
	}
	return v, nil
}
func encodeNextCursor(cur map[string]int64, msgs []protocol.Message) string {
	next := make(map[string]int64, len(cur))
	for k, v := range cur {
		next[k] = v
	}
	for _, m := range msgs {
		if m.Seq > next[m.Room] {
			next[m.Room] = m.Seq
		}
	}
	if len(next) == 0 {
		return ""
	}
	b, err := json.Marshal(next)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Ensure compile-time conformance for status values used in presence updates.
