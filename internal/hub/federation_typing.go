package hub

// Ephemeral typing deliberately has no store writer, durable queue or retry.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

const typingTTL = 5 * time.Second
const typingInterval = 200 * time.Millisecond

type typingSessionRequest struct {
	Boot  string                      `json:"boot_id"`
	Scope store.FederationTypingScope `json:"scope"`
}
type typingSessionReply struct {
	ID  string `json:"session_id"`
	TTL int64  `json:"ttl_ms"`
}
type remoteTypingEvent struct {
	Session string            `json:"session_id"`
	Seq     uint64            `json:"seq"`
	Actor   protocol.AgentRef `json:"actor"`
	Typing  bool              `json:"typing"`
	TTL     int64             `json:"ttl_ms"`
}
type remoteTypingAgent struct {
	NodeID   string `json:"node_id"`
	AgentID  string `json:"agent_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	TTL      int64  `json:"ttl_ms"`
	Revision uint64 `json:"revision"`
}
type typingSession struct {
	id, boot string
	link     store.FederationTypingLink
	expires  time.Time
	seq      uint64
}
type typingRemote struct {
	agent         remoteTypingAgent
	room, session string
	expires, next time.Time
	active        bool
	dirty         bool
}
type typingPending struct {
	link     store.FederationTypingLink
	actor    string
	typing   bool
	at       time.Time
	revision uint64
}
type ephemeralTyping struct {
	mu       sync.Mutex
	boot     string
	links    []store.FederationTypingLink // immutable, maximum 64
	inbound  map[string]typingSession     // link key -> receiver-issued capability
	outbound map[string]typingSession
	remote   map[string]typingRemote  // bounded; includes brief rate-limit tombstones
	pending  map[string]typingPending // latest only; never replayed after failure
	next     map[string]time.Time
	revision uint64
	running  bool
	now      func() time.Time
}

func typingLinkKey(l store.FederationTypingLink) string {
	return l.PeerID + "\x00" + l.Kind + ":" + l.LinkID
}
func (f *Federation) initTyping(ctx context.Context) error {
	links := append([]store.FederationTypingLink(nil), f.opts.TypingLinks...)
	for _, d := range f.opts.DMConversations {
		if d.Typing {
			links = append(links, store.FederationTypingLink{PeerID: d.PeerID, Kind: "federated_dm", LinkID: d.LinkID, Epoch: d.Epoch, ConversationID: d.ConversationID})
		}
	}
	if len(links) == 0 {
		return nil
	}
	if len(links) > 64 {
		return errors.New("at most 64 typing links")
	}
	keys, rooms := map[string]bool{}, map[string]bool{}
	for i, l := range links {
		if l.PeerID == "" {
			links[i].PeerID = f.opts.Peer.NodeID
		} else if _, ok := f.peers[l.PeerID]; !ok {
			return errors.New("typing link references an unknown peer")
		}
		l = links[i]
		_, room, err := f.store.TypingScope(ctx, l.PeerID, l)
		if err != nil {
			return errors.New("invalid explicit typing link")
		}
		key := typingLinkKey(l)
		roomKey := l.PeerID + "\x00" + room
		if keys[key] || rooms[roomKey] {
			return errors.New("duplicate typing scope")
		}
		keys[key] = true
		rooms[roomKey] = true
		links[i].LocalRoom = room
	}
	f.ephemeral = &ephemeralTyping{boot: protocol.NewToken("epb"), links: links, now: time.Now}
	f.ephemeral.clearLocked()
	return nil
}
func (e *ephemeralTyping) clearLocked() {
	e.inbound = map[string]typingSession{}
	e.outbound = map[string]typingSession{}
	e.remote = map[string]typingRemote{}
	e.pending = map[string]typingPending{}
	e.next = map[string]time.Time{}
}
func (f *Federation) typingEnabled(ctx context.Context, room string) bool {
	if f == nil || f.ephemeral == nil {
		return false
	}
	for _, l := range f.ephemeral.links {
		if l.LocalRoom == room {
			if _, _, err := f.store.TypingScope(ctx, l.PeerID, l); err == nil {
				return true
			}
		}
	}
	return false
}
func (f *Federation) publishTyping(ctx context.Context, room, actor string, active bool) {
	if f == nil || f.ephemeral == nil {
		return
	}
	e := f.ephemeral
	matched := make([]store.FederationTypingLink, 0, 2)
	for _, l := range e.links {
		if l.LocalRoom == room {
			if _, err := f.store.TypingActor(ctx, l.PeerID, l, actor); err == nil {
				matched = append(matched, l)
			}
		}
	}
	if len(matched) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return
	}
	for _, l := range matched {
		key := typingLinkKey(l) + ":" + actor
		if _, exists := e.pending[key]; !exists && len(e.pending) >= 256 {
			continue
		}
		if _, exists := e.next[key]; !exists && len(e.next) >= 256 {
			continue
		}
		e.revision++
		e.pending[key] = typingPending{link: l, actor: actor, typing: active, at: e.now(), revision: e.revision}
	}
}
func decodeEphemeral(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		writeError(w, 400, "invalid_request", "invalid ephemeral event", nil)
		return false
	}
	return true
}
func (s *Server) handleTypingSession(w http.ResponseWriter, r *http.Request) {
	f := s.opts.Federation
	node, ok := f.authenticatedPeer(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	e := f.ephemeral
	if !s.allow("typing-session:"+node, 120, 10) {
		writeError(w, 429, "rate_limited", "ephemeral session rate limit", nil)
		return
	}
	var req typingSessionRequest
	if !decodeEphemeral(w, r, &req) {
		return
	}
	if len(req.Boot) < 16 || len(req.Boot) > 128 {
		writeError(w, 400, "invalid_request", "invalid boot identity", nil)
		return
	}
	var matched *store.FederationTypingLink
	for _, l := range e.links {
		if l.PeerID != node {
			continue
		}
		scope, _, err := f.store.TypingScope(r.Context(), node, l)
		if err != nil {
			continue
		}
		scope.SourceRoom, scope.DestinationRoom = scope.DestinationRoom, scope.SourceRoom
		scope.SourceAgentID, scope.DestinationAgentID = scope.DestinationAgentID, scope.SourceAgentID
		if req.Scope == scope {
			copy := l
			matched = &copy
			break
		}
	}
	if matched == nil {
		writeError(w, 403, "forbidden", "typing link is not opted in or currently authorized", nil)
		return
	}
	e.mu.Lock()
	key := typingLinkKey(*matched)
	old := e.inbound[key]
	now := e.now()
	// An active session is not renewable or replaceable: its expiry is fixed.
	// This also prevents delayed handshakes from superseding a live boot.
	if now.Before(old.expires) {
		e.mu.Unlock()
		writeError(w, 409, "conflict", "typing session still active", nil)
		return
	}
	id := protocol.NewToken("eps")
	e.inbound[key] = typingSession{id: id, boot: req.Boot, link: *matched, expires: now.Add(typingTTL)}
	e.pruneLocked(now)
	e.mu.Unlock()
	writeJSON(w, 200, typingSessionReply{ID: id, TTL: 5000})
}
func (s *Server) handleRemoteTyping(w http.ResponseWriter, r *http.Request) {
	f := s.opts.Federation
	node, ok := f.authenticatedPeer(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	e := f.ephemeral
	if !s.allow("typing-events:"+node, 1200, 40) {
		writeError(w, 429, "rate_limited", "ephemeral peer rate limit", nil)
		return
	}
	var ev remoteTypingEvent
	if !decodeEphemeral(w, r, &ev) {
		return
	}
	if ev.Seq == 0 || ev.TTL < 1 || ev.TTL > 5000 || protocol.ValidateAgentName(ev.Actor.ID) != nil || protocol.ValidateAgentName(ev.Actor.Name) != nil || protocol.ValidateAgentName(ev.Actor.Kind) != nil {
		writeError(w, 400, "invalid_request", "invalid typing actor, sequence or TTL", nil)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	e.pruneLocked(now)
	key := ""
	var session typingSession
	for k, v := range e.inbound {
		if v.id == ev.Session && now.Before(v.expires) {
			key = k
			session = v
			break
		}
	}
	if key == "" || ev.Seq <= session.seq || session.link.PeerID != node {
		writeError(w, 409, "stale_session", "expired session or stale sequence", nil)
		return
	}
	scope, room, err := f.store.TypingScope(r.Context(), node, session.link)
	if err != nil || scope.Kind == "federated_dm" && ev.Actor.ID != scope.DestinationAgentID {
		delete(e.inbound, key)
		writeError(w, 403, "forbidden", "typing scope or actor no longer authorized", nil)
		return
	}
	if !s.allow("typing-link:"+key, 1200, 20) {
		writeError(w, 429, "rate_limited", "ephemeral link rate limit", nil)
		return
	}
	actorKey := key + ":" + ev.Actor.ID
	old, exists := e.remote[actorKey]
	session.seq = ev.Seq
	e.inbound[key] = session // consume even rate-limited events
	if !exists && len(e.remote) >= 256 {
		writeError(w, 429, "rate_limited", "ephemeral actor capacity", nil)
		return
	}
	count := 0
	for _, a := range e.remote {
		if a.session == session.id {
			count++
		}
	}
	if !exists && count >= 64 {
		writeError(w, 429, "rate_limited", "ephemeral link actor capacity", nil)
		return
	}
	expires := now.Add(time.Duration(ev.TTL) * time.Millisecond)
	if session.expires.Before(expires) {
		expires = session.expires
	}
	e.revision++
	limited := exists && now.Before(old.next)
	next := now.Add(typingInterval)
	if limited {
		next = old.next
	}
	e.remote[actorKey] = typingRemote{agent: remoteTypingAgent{NodeID: node, AgentID: ev.Actor.ID, Name: ev.Actor.Name, Kind: ev.Actor.Kind, Revision: e.revision}, room: room, session: session.id, expires: expires, next: next, active: ev.Typing, dirty: limited}
	if !limited {
		s.broadcast()
	}
	w.WriteHeader(204)
}
func (e *ephemeralTyping) pruneLocked(now time.Time) bool {
	changed := false
	for key, a := range e.remote {
		if a.dirty && !now.Before(a.next) {
			a.dirty = false
			e.remote[key] = a
			changed = true
		}
		if !now.Before(a.expires) {
			delete(e.remote, key)
			changed = true
		}
	}
	for key, t := range e.next {
		if now.Sub(t) > typingTTL {
			delete(e.next, key)
		}
	}
	for key, p := range e.pending {
		if now.Sub(p.at) >= typingTTL {
			delete(e.pending, key)
		}
	}
	return changed
}
func (f *Federation) remoteTyping(ctx context.Context, room string) []remoteTypingAgent {
	result := []remoteTypingAgent{}
	if f == nil || f.ephemeral == nil {
		return result
	}
	e := f.ephemeral
	// Current approved scope is checked on every SSE snapshot, not just handshake.
	if !f.typingEnabled(ctx, room) {
		return result
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	e.pruneLocked(now)
	for _, a := range e.remote {
		if a.room == room && a.active {
			v := a.agent
			v.TTL = max(1, a.expires.Sub(now).Milliseconds())
			result = append(result, v)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AgentID < result[j].AgentID })
	return result
}
func (f *Federation) runTyping(ctx context.Context) error {
	e := f.ephemeral
	e.mu.Lock()
	e.clearLocked()
	e.boot = protocol.NewToken("epb")
	e.running = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.running = false
		e.clearLocked()
		e.mu.Unlock()
		if f.opts.notify != nil {
			f.opts.notify()
		}
	}()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			e.mu.Lock()
			changed := e.pruneLocked(e.now())
			var key string
			var p typingPending
			for k, v := range e.pending {
				if !e.now().Before(e.next[k]) {
					key = k
					p = v
					break
				}
			}
			e.mu.Unlock()
			if changed && f.opts.notify != nil {
				f.opts.notify()
			}
			if key == "" {
				continue
			}
			ok := f.deliverTyping(ctx, p)
			e.mu.Lock()
			if !ok {
				// A failure on one peer must not erase another peer's sessions.
				delete(e.pending, key)
				delete(e.outbound, typingLinkKey(p.link))
			} else {
				if e.pending[key].revision == p.revision {
					delete(e.pending, key)
				}
				e.next[key] = e.now().Add(typingInterval)
			}
			e.mu.Unlock()
			if !ok && f.opts.notify != nil {
				f.opts.notify()
			}
		}
	}
}
func (f *Federation) typingRequest(ctx context.Context, peer, path string, in, out any) bool {
	data, err := json.Marshal(in)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, min(f.opts.RequestTimeout, time.Second))
	defer cancel()
	req, done, err := f.peerRequest(ctx, peer, "POST", "/federation/v2/ephemeral/"+path, bytes.NewReader(data))
	if err != nil {
		return false
	}
	defer done()
	req.Header.Set("Content-Type", "application/json")
	client := f.clients[peer]
	if client == nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if out == nil {
		return resp.StatusCode == 204
	}
	return resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(out) == nil
}
func (f *Federation) deliverTyping(ctx context.Context, p typingPending) bool {
	e := f.ephemeral
	peer := p.link.PeerID
	actor, err := f.store.TypingActor(ctx, peer, p.link, p.actor)
	if err != nil {
		return false
	}
	scope, _, err := f.store.TypingScope(ctx, peer, p.link)
	if err != nil {
		return false
	}
	key := typingLinkKey(p.link)
	e.mu.Lock()
	session := e.outbound[key]
	boot := e.boot
	now := e.now()
	e.mu.Unlock()
	if now.Sub(p.at) >= typingTTL {
		return true
	}
	if !now.Before(session.expires) {
		var reply typingSessionReply
		start := e.now()
		if !f.typingRequest(ctx, peer, "session", typingSessionRequest{Boot: boot, Scope: scope}, &reply) || reply.ID == "" || len(reply.ID) > 128 || reply.TTL < 1 || reply.TTL > 5000 {
			return false
		}
		session = typingSession{id: reply.ID, expires: start.Add(time.Duration(reply.TTL) * time.Millisecond)}
	}
	ttl := min(p.at.Add(typingTTL).Sub(e.now()), session.expires.Sub(e.now())).Milliseconds()
	if ttl < 1 {
		return true
	}
	session.seq++
	ev := remoteTypingEvent{Session: session.id, Seq: session.seq, Actor: actor, Typing: p.typing, TTL: ttl}
	if !f.typingRequest(ctx, peer, "events", ev, nil) {
		return false
	}
	e.mu.Lock()
	e.outbound[key] = session
	e.mu.Unlock()
	return true
}
