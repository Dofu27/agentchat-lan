package hub_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func typingPeer(t *testing.T, n *federationNode, origin, secret, path string, body any) (int, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", n.url+"/federation/v2/ephemeral/"+path, bytes.NewReader(raw))
	req.Header.Set("X-AgentChat-Node", origin)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}
func typingScope() map[string]any {
	return map[string]any{"kind": "room", "link_id": "typing-test", "epoch": 1, "source_room": "general", "destination_room": "general"}
}
func typingHandshake(t *testing.T, n *federationNode, origin string, scope any) string {
	t.Helper()
	code, raw := typingPeer(t, n, origin, federationTestSecret, "session", map[string]any{"boot_id": "synthetic-boot-123456", "scope": scope})
	if code != 200 {
		t.Fatalf("handshake %d %s", code, raw)
	}
	var v struct {
		ID string `json:"session_id"`
	}
	json.Unmarshal(raw, &v)
	return v.ID
}
func typingEvent(session string, seq int, ttl int, actor string, active bool) map[string]any {
	return map[string]any{"session_id": session, "seq": seq, "ttl_ms": ttl, "actor": map[string]string{"id": actor, "name": actor, "kind": "human"}, "typing": active}
}
func startTypingNodes(t *testing.T) (*federationNode, *federationNode, *client.Client, *client.Client) {
	t.Helper()
	a, b := newFederationNode(t), newFederationNode(t)
	for _, p := range [][2]*federationNode{{a, b}, {b, a}} {
		n, peer := p[0], p[1]
		n.opts = hub.FederationOptions{Peer: hub.FederationPeerOptions{NodeID: peer.id, BaseURL: peer.url, Secret: federationTestSecret}, Links: []store.FederationLink{{PeerID: peer.id, LocalRoom: "general", RemoteRoom: "general"}}, TypingLinks: []store.FederationTypingLink{{Kind: "room", LinkID: "typing-test", Epoch: 1, LocalRoom: "general", RemoteRoom: "general"}}, RequestTimeout: 100 * time.Millisecond, LeaseDuration: time.Second}
		n.startWithOptions(t)
	}
	return a, b, nodeReader(t, a, "alice"), nodeReader(t, b, "bob")
}
func postTyping(t *testing.T, n *federationNode, c *client.Client, room string, active bool) {
	t.Helper()
	code, body := dmHTTPRequest(t, n, "POST", "/v1/rooms/"+room+"/typing", c.Token, []byte(fmt.Sprintf(`{"typing":%t}`, active)))
	if code != 204 {
		t.Fatalf("typing: %d %s", code, body)
	}
}
func typingStream(t *testing.T, n *federationNode, c *client.Client, room string) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", n.url+"/v1/rooms/"+room+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	ch := make(chan string, 256)
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); resp.Body.Close(); <-done })
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(resp.Body)
		event := ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			}
			if event == "typing" && strings.HasPrefix(line, "data: ") {
				select {
				case ch <- strings.TrimPrefix(line, "data: "):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch
}
func awaitRemote(t *testing.T, ch <-chan string, name string, present bool) string {
	t.Helper()
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	for {
		select {
		case data := <-ch:
			var payload struct {
				Remote []struct {
					Name, NodeID string
					TTL          int `json:"ttl_ms"`
				} `json:"remote_agents"`
			}
			if json.Unmarshal([]byte(data), &payload) != nil {
				continue
			}
			found := false
			for _, a := range payload.Remote {
				if a.Name == name {
					found = true
					if a.TTL > 5000 || a.TTL < 1 {
						t.Fatalf("unbounded TTL %s", data)
					}
				}
			}
			if found == present {
				return data
			}
		case <-timer.C:
			t.Fatalf("remote typing %s present=%t timed out", name, present)
			return ""
		}
	}
}

// Snapshot every persistent table, including agent last_seen and schema/audit.
// F3 traffic and its SSE wakeups must not mutate any of them.
func typingDatabase(t *testing.T, n *federationNode) string {
	t.Helper()
	db, err := sql.Open("sqlite", n.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	var result []string
	for _, name := range tables {
		r, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		var records []string
		for r.Next() {
			values := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range ptr {
				ptr[i] = &values[i]
			}
			if err = r.Scan(ptr...); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(values)
			records = append(records, string(raw))
		}
		r.Close()
		sort.Strings(records)
		result = append(result, name+":"+strings.Join(records, "\n"))
	}
	return strings.Join(result, "\n")
}
func TestFederatedTypingTwoNodesEphemeralNoLoopOrPersistence(t *testing.T) {
	a, b, ca, cb := startTypingNodes(t)
	sa, sb := typingStream(t, a, ca, "general"), typingStream(t, b, cb, "general")
	a.run()
	b.run()
	time.Sleep(80 * time.Millisecond)
	beforeA, beforeB := typingDatabase(t, a), typingDatabase(t, b)
	var forwarded atomic.Int32
	original := *a.handler.Load()
	a.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/federation/v2/ephemeral/events" {
			forwarded.Add(1)
		}
		original.ServeHTTP(w, r)
	}))
	postTyping(t, a, ca, "general", true)
	data := awaitRemote(t, sb, "alice", true)
	if !strings.Contains(data, b.opts.Peer.NodeID) || !strings.Contains(data, `"remote_agents"`) {
		t.Fatal(data)
	}
	time.Sleep(250 * time.Millisecond)
	if forwarded.Load() != 0 {
		t.Fatal("inbound typing forwarded")
	}
	postTyping(t, b, cb, "general", true)
	awaitRemote(t, sa, "bob", true)
	postTyping(t, a, ca, "general", false)
	awaitRemote(t, sb, "alice", false)
	if typingDatabase(t, a) != beforeA || typingDatabase(t, b) != beforeB {
		t.Fatal("ephemeral traffic changed persistent state")
	}
	postTyping(t, a, ca, "general", true)
	awaitRemote(t, sb, "alice", true)
	b.stopWorker(t)
	awaitRemote(t, sb, "alice", false)
}
func TestFederatedTypingSessionTTLSequenceAuthAndRestart(t *testing.T) {
	a, b, _, cb := startTypingNodes(t)
	stream := typingStream(t, b, cb, "general")
	if code, _ := typingPeer(t, b, a.id, "wrong", "session", map[string]any{}); code != 401 {
		t.Fatal(code)
	}
	bad := typingScope()
	bad["epoch"] = 2
	if code, _ := typingPeer(t, b, a.id, federationTestSecret, "session", map[string]any{"boot_id": "synthetic-boot-123456", "scope": bad}); code != 403 {
		t.Fatal(code)
	}
	id := typingHandshake(t, b, a.id, typingScope())
	send := func(ev any, want int) {
		t.Helper()
		code, raw := typingPeer(t, b, a.id, federationTestSecret, "events", ev)
		if code != want {
			t.Fatalf("event %d want %d: %s", code, want, raw)
		}
	}
	send(typingEvent(id, 1, 5001, "remote", true), 400)
	send(typingEvent(id, 1, 80, "remote", true), 204)
	awaitRemote(t, stream, "remote", true)
	send(typingEvent(id, 1, 80, "remote", true), 409)
	send(typingEvent("old-session", 2, 5000, "remote", true), 409)
	time.Sleep(120 * time.Millisecond)
	// A fresh SSE snapshot prunes expired states even if no worker is running.
	fresh := typingStream(t, b, cb, "general")
	awaitRemote(t, fresh, "remote", false)
	// Restart the runtime: all sessions, pending events and remote state disappear.
	b.run()
	time.Sleep(80 * time.Millisecond)
	send(typingEvent(id, 2, 5000, "remote", true), 409)
	b.stopWorker(t)
	id = typingHandshake(t, b, a.id, typingScope())
	time.Sleep(5100 * time.Millisecond)
	send(typingEvent(id, 1, 5000, "remote", true), 409)
}
func TestFederatedTypingDefaultOffAndOfflineDrop(t *testing.T) {
	a, b, ca, cb := startTypingNodes(t)
	stream := typingStream(t, b, cb, "general")
	original := *b.handler.Load()
	var requests atomic.Int32
	var unavailable atomic.Bool
	unavailable.Store(true)
	b.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/federation/v2/ephemeral/") {
			requests.Add(1)
			if unavailable.Load() {
				w.WriteHeader(503)
				return
			}
		}
		original.ServeHTTP(w, r)
	}))
	a.run()
	b.run()
	time.Sleep(80 * time.Millisecond)
	for i := 0; i < 40; i++ {
		postTyping(t, a, ca, "general", true)
	}
	waitFederation(t, func() bool { return requests.Load() > 0 })
	time.Sleep(200 * time.Millisecond)
	count := requests.Load()
	unavailable.Store(false)
	time.Sleep(300 * time.Millisecond)
	if requests.Load() != count {
		t.Fatal("offline input replayed without new local input")
	}
	postTyping(t, a, ca, "general", true)
	awaitRemote(t, stream, "alice", true)
	// Turning the explicit runtime off hides the entire namespace, including
	// misspelled paths, rather than falling through to the Web UI.
	a.stopWorker(t)
	a.opts.TypingLinks = nil
	f, err := hub.NewFederation(context.Background(), a.st, a.opts)
	if err != nil {
		t.Fatal(err)
	}
	a.setHandler(hub.New(a.st, hub.Options{Federation: f}).Handler())
	for _, path := range []string{"session", "events", "unknown"} {
		if code, _ := typingPeer(t, a, b.id, federationTestSecret, path, map[string]any{}); code != 404 {
			t.Fatalf("default off %s: %d", path, code)
		}
	}
}
func TestFederatedTypingDMExplicitScopeAndReadStillDisabled(t *testing.T) {
	a, b, ca, cb, ra, rb := startDMNodes(t, true)
	stream := typingStream(t, b, cb, rb.ID)
	a.run()
	b.run()
	time.Sleep(80 * time.Millisecond)
	postTyping(t, a, ca, ra.ID, true)
	awaitRemote(t, stream, "person-a", true)
	if code, _ := dmHTTPRequest(t, b, "GET", "/v1/rooms/"+rb.ID+"/receipts", cb.Token, nil); code != 403 {
		t.Fatal(code)
	}
	code, body := dmHTTPRequest(t, a, "GET", "/v1/rooms", ca.Token, nil)
	if code != 200 || !bytes.Contains(body, []byte(`"ephemeral_typing":true`)) {
		t.Fatalf("room capability %d %s", code, body)
	}
	// The peer may assert room actors, but cannot substitute a different DM actor.
	b.stopWorker(t)
	scope := map[string]any{"kind": "federated_dm", "link_id": "private-link", "epoch": 1, "conversation_id": "private-chat", "source_agent_id": "person-a", "destination_agent_id": "person-b"}
	id := typingHandshake(t, b, a.id, scope)
	if code, _ := typingPeer(t, b, a.id, federationTestSecret, "events", typingEvent(id, 1, 5000, "intruder", true)); code != 403 {
		t.Fatal(code)
	}
}

func TestFederatedTypingActorBoundAndLatestStop(t *testing.T) {
	a, b, _, cb := startTypingNodes(t)
	b.run()
	time.Sleep(80 * time.Millisecond)
	id := typingHandshake(t, b, a.id, typingScope())
	stream := typingStream(t, b, cb, "general")
	for i := 1; i <= 65; i++ {
		code, raw := typingPeer(t, b, a.id, federationTestSecret, "events", typingEvent(id, i, 5000, fmt.Sprintf("actor-%d", i), true))
		want := 204
		if i == 65 {
			want = 429
		}
		if code != want {
			t.Fatalf("actor %d: %d %s", i, code, raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Multiple updates within the actor window merge into the latest stop,
	// rather than dropping it and leaving the earlier true visible until TTL.
	code, _ := typingPeer(t, b, a.id, federationTestSecret, "events", typingEvent(id, 66, 1000, "actor-64", true))
	if code != 204 {
		t.Fatal(code)
	}
	awaitRemote(t, stream, "actor-64", true)
	code, _ = typingPeer(t, b, a.id, federationTestSecret, "events", typingEvent(id, 67, 1000, "actor-64", false))
	if code != 204 {
		t.Fatal(code)
	}
	awaitRemote(t, stream, "actor-64", false)
}

func TestFederatedTypingSourceCoalescingAndCurrentLink(t *testing.T) {
	a, b, ca, cb := startTypingNodes(t)
	stream := typingStream(t, b, cb, "general")
	var count atomic.Int32
	var latest atomic.Bool
	original := *b.handler.Load()
	b.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/federation/v2/ephemeral/events" {
			raw, _ := io.ReadAll(r.Body)
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var v struct {
				Typing bool `json:"typing"`
			}
			json.Unmarshal(raw, &v)
			latest.Store(v.Typing)
			count.Add(1)
		}
		original.ServeHTTP(w, r)
	}))
	a.run()
	b.run()
	time.Sleep(80 * time.Millisecond)
	postTyping(t, a, ca, "general", true)
	awaitRemote(t, stream, "alice", true)
	for i := 0; i < 30; i++ {
		postTyping(t, a, ca, "general", i%2 == 0)
	}
	awaitRemote(t, stream, "alice", false)
	time.Sleep(250 * time.Millisecond)
	if latest.Load() || count.Load() > 5 {
		t.Fatalf("source did not coalesce latest state: %d requests, latest=%t", count.Load(), latest.Load())
	}
	// Current DB authorization, rather than the session's cached scope, gates
	// subsequent inbound events after a room link is disabled.
	b.stopWorker(t)
	id := typingHandshake(t, b, a.id, typingScope())
	db, err := sql.Open("sqlite", b.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE federation_room_links SET enabled=0`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := typingPeer(t, b, a.id, federationTestSecret, "events", typingEvent(id, 1, 5000, "alice", true)); code != 403 {
		t.Fatal(code)
	}
}
