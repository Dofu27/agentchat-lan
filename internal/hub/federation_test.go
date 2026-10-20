package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

const federationTestSecret = "independent-test-only-peer-secret-123456789"

type federationNode struct {
	dbPath, id, url string
	st              *store.Store
	server          *httptest.Server
	fed             *hub.Federation
	cancel          context.CancelFunc
	done            chan error
	dropAck         atomic.Bool
	requests        atomic.Int32
	opts            hub.FederationOptions
}

func newFederationNode(t *testing.T) *federationNode {
	t.Helper()
	n := &federationNode{dbPath: filepath.Join(t.TempDir(), "agentchat.db")}
	var err error
	n.st, err = store.Open(context.Background(), n.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	n.id, err = n.st.FederationIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n.server = httptest.NewUnstartedServer(nil)
	n.url = "http://" + n.server.Listener.Addr().String()
	t.Cleanup(func() { n.close(t) })
	return n
}
func (n *federationNode) start(t *testing.T, peer *federationNode) {
	t.Helper()
	n.opts = hub.FederationOptions{Peer: hub.FederationPeerOptions{NodeID: peer.id, BaseURL: peer.url, Secret: federationTestSecret}, Links: []store.FederationLink{{PeerID: peer.id, LocalRoom: "general", RemoteRoom: "general"}}, MaxOutbox: 100, MaxAttempts: 10, RetryBase: 50 * time.Millisecond, PollInterval: 20 * time.Millisecond, RequestTimeout: 100 * time.Millisecond, LeaseDuration: 300 * time.Millisecond}
	n.startWithOptions(t)
}
func (n *federationNode) startWithOptions(t *testing.T) {
	t.Helper()
	var err error
	n.fed, err = hub.NewFederation(context.Background(), n.st, n.opts)
	if err != nil {
		t.Fatal(err)
	}
	handler := hub.New(n.st, hub.Options{Federation: n.fed, RequestsPerMinute: 1000, Burst: 100, Logger: log.New(io.Discard, "", 0), FileDir: filepath.Join(filepath.Dir(n.dbPath), "files")}).Handler()
	n.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/federation/v1/messages" {
			n.requests.Add(1)
			if n.dropAck.CompareAndSwap(true, false) {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, r)
				if recorder.Code == 200 {
					w.WriteHeader(500)
					return
				} // commit succeeded; ACK lost
				for key, values := range recorder.Header() {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				w.WriteHeader(recorder.Code)
				w.Write(recorder.Body.Bytes())
				return
			}
		}
		handler.ServeHTTP(w, r)
	})
	n.server.Start()
}
func (n *federationNode) run() {
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	n.done = make(chan error, 1)
	go func() { n.done <- n.fed.Run(ctx) }()
}
func (n *federationNode) stopWorker(t *testing.T) {
	t.Helper()
	if n.cancel != nil {
		n.cancel()
		select {
		case err := <-n.done:
			if err != nil {
				t.Errorf("worker: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("worker failed to stop")
		}
		n.cancel = nil
	}
}
func (n *federationNode) close(t *testing.T) {
	n.stopWorker(t)
	if n.server != nil {
		n.server.Close()
	}
	if n.st != nil {
		n.st.Close()
	}
}
func waitFederation(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("federation condition timed out")
}
func nodeReader(t *testing.T, n *federationNode, name string) *client.Client {
	t.Helper()
	ctx := context.Background()
	code := "invite-" + name
	if _, err := n.st.CreateInvite(ctx, code, store.Invite{Room: "general", TTL: time.Hour, Uses: 1}, "test"); err != nil {
		t.Fatal(err)
	}
	joined, err := client.New(n.url, "").Join(ctx, protocol.JoinRequest{InviteCode: code, Name: name, Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return client.New(n.url, joined.Token)
}
func fedRequest(t *testing.T, n *federationNode, origin, secret string, e store.FederationEnvelope) (int, store.FederationReceipt) {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", n.url+"/federation/v1/messages", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-AgentChat-Node", origin)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var receipt store.FederationReceipt
	if resp.StatusCode == 200 {
		if err = json.NewDecoder(resp.Body).Decode(&receipt); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, receipt
}

func TestFederationTwoNodesRetryIdempotencyAndLoopBlock(t *testing.T) {
	a, b := newFederationNode(t), newFederationNode(t)
	a.start(t, b)
	b.start(t, a)
	senderA, senderB := nodeReader(t, a, "alice"), nodeReader(t, b, "bob")
	if _, err := senderA.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "forged metadata", Meta: protocol.MessageMeta{Federation: &protocol.FederationMeta{OriginNodeID: b.id}}}); err == nil {
		t.Fatal("local agent forged federation attribution")
	}
	b.dropAck.Store(true)
	a.run()
	b.run()
	first, err := senderA.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "from A"})
	if err != nil {
		t.Fatal(err)
	}
	waitFederation(t, func() bool { stats, _ := a.st.FederationStats(context.Background()); return stats.Delivered == 1 })
	if b.requests.Load() < 2 {
		t.Fatal("lost ACK was not retried")
	}
	history, _, err := b.st.History(context.Background(), senderBIdentity(t, senderB), "general", 0, 0, 100)
	if err != nil || len(history) != 1 {
		t.Fatalf("duplicate remote insert: %v %v", history, err)
	}
	if history[0].From.Kind != "federation" || history[0].Meta.Federation.OriginMessageID != first.ID {
		t.Fatal("remote attribution not retained")
	}
	if _, err := senderB.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "from B"}); err != nil {
		t.Fatal(err)
	}
	waitFederation(t, func() bool { stats, _ := b.st.FederationStats(context.Background()); return stats.Delivered == 1 })
	for _, n := range []*federationNode{a, b} {
		stats, _ := n.st.FederationStats(context.Background())
		if stats.Pending != 0 || stats.Inflight != 0 || stats.Delivered != 1 {
			t.Fatalf("loop created extra outbox work: %+v", stats)
		}
	}

	envelope := store.FederationEnvelope{Version: 1, OriginNodeID: a.id, OriginMessageID: "concurrent-message", SourceRoom: "general", DestinationRoom: "general", Text: "concurrent retries", Sender: protocol.AgentRef{ID: "remote-id", Name: "alice", Kind: "test"}, SentAt: time.Now().UTC()}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			status, r := fedRequest(t, b, a.id, federationTestSecret, envelope)
			if status != 200 {
				t.Errorf("concurrent HTTP status %d", status)
			}
			ids <- r.LocalMessageID
		})
	}
	wg.Wait()
	close(ids)
	var unique string
	for id := range ids {
		if unique != "" && id != unique {
			t.Fatal("concurrent duplicate inserted twice")
		}
		unique = id
	}
	altered := envelope
	altered.Text = "changed"
	if status, _ := fedRequest(t, b, a.id, federationTestSecret, altered); status != 409 {
		t.Fatalf("conflicting duplicate=%d", status)
	}
	for _, tc := range []struct {
		node, secret string
		status       int
	}{{"unknown-node", federationTestSecret, 401}, {a.id, "wrong", 401}} {
		if status, _ := fedRequest(t, b, tc.node, tc.secret, envelope); status != tc.status {
			t.Fatalf("peer auth=%d", status)
		}
	}
	altered = envelope
	altered.OriginNodeID = "third-node"
	if status, _ := fedRequest(t, b, a.id, federationTestSecret, altered); status != 403 {
		t.Fatalf("relayed origin accepted: %d", status)
	}
	altered = envelope
	altered.DestinationRoom = "not-linked"
	if status, _ := fedRequest(t, b, a.id, federationTestSecret, altered); status != 403 {
		t.Fatalf("unlinked import=%d", status)
	}
	// Imports still have not enqueued anything, even with reverse links enabled.
	stats, _ := b.st.FederationStats(context.Background())
	if stats.Delivered != 1 || stats.Pending != 0 {
		t.Fatal("inbound relay loop")
	}
}

func TestFederationRejectsBadACKAndRedirect(t *testing.T) {
	for _, mode := range []string{"bad-ack", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var received atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
			defer target.Close()
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"origin_node_id":"wrong","origin_message_id":"wrong","local_message_id":"fake"}`)
			}))
			defer remote.Close()
			n := newFederationNode(t)
			n.opts = hub.FederationOptions{Peer: hub.FederationPeerOptions{NodeID: "remote-peer", BaseURL: remote.URL, Secret: federationTestSecret}, Links: []store.FederationLink{{PeerID: "remote-peer", LocalRoom: "general", RemoteRoom: "general"}}, MaxOutbox: 10, MaxAttempts: 2, RetryBase: 20 * time.Millisecond, PollInterval: 10 * time.Millisecond, RequestTimeout: 100 * time.Millisecond, LeaseDuration: 300 * time.Millisecond}
			n.startWithOptions(t)
			sender := nodeReader(t, n, "ack-check")
			if _, err := sender.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "must not be falsely acknowledged"}); err != nil {
				t.Fatal(err)
			}
			n.run()
			waitFederation(t, func() bool { stats, _ := n.st.FederationStats(context.Background()); return stats.Dead == 1 })
			stats, _ := n.st.FederationStats(context.Background())
			if stats.Delivered != 0 {
				t.Fatal("bad response marked delivered")
			}
			if received.Load() != 0 {
				t.Fatal("redirect target received peer credential")
			}
		})
	}
}
func senderBIdentity(t *testing.T, c *client.Client) string {
	t.Helper()
	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return me.Agent.ID
}

func TestFederationUnreachableRestartAndDisabledDefault(t *testing.T) {
	a, b := newFederationNode(t), newFederationNode(t)
	a.start(t, b)
	// B has a stable identity/data directory, but no HTTP listener is available.
	b.server.Close()
	b.server = nil
	sender := nodeReader(t, a, "restart-sender")
	if _, err := sender.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "persist across restart"}); err != nil {
		t.Fatal(err)
	}
	a.run()
	waitFederation(t, func() bool {
		stats, _ := a.st.FederationStats(context.Background())
		return stats.Pending == 1 && stats.Attempts > 0
	})
	a.stopWorker(t)
	a.server.Close()
	a.st.Close()
	var err error
	a.st, err = store.Open(context.Background(), a.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.st.FederationIdentity(context.Background())
	if err != nil || id != a.id {
		t.Fatal("node ID changed at restart")
	}
	a.server = httptest.NewUnstartedServer(nil)
	a.server.Listener.Close()
	a.server.Listener, err = net.Listen("tcp", strings.TrimPrefix(a.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	a.startWithOptions(t)
	b.server = httptest.NewUnstartedServer(nil)
	b.server.Listener.Close()
	b.server.Listener, err = net.Listen("tcp", strings.TrimPrefix(b.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	b.start(t, a)
	readerB := nodeReader(t, b, "restart-reader")
	a.run()
	b.run()
	waitFederation(t, func() bool { stats, _ := a.st.FederationStats(context.Background()); return stats.Delivered == 1 })
	rows, _, err := b.st.History(context.Background(), senderBIdentity(t, readerB), "general", 0, 0, 100)
	if err != nil || len(rows) != 1 || rows[0].Text != "persist across restart" {
		t.Fatalf("durable outbox lost: %v %v", rows, err)
	}

	// Re-open with the default constructor: persisted rows cannot expose routes.
	a.stopWorker(t)
	a.server.Close()
	a.st.Close()
	a.st, err = store.Open(context.Background(), a.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	disabled := httptest.NewServer(hub.New(a.st, hub.Options{Logger: log.New(io.Discard, "", 0)}).Handler())
	defer disabled.Close()
	response, err := http.Post(disabled.URL+"/federation/v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 && response.StatusCode != 405 {
		t.Fatalf("federation exposed by default: %d", response.StatusCode)
	}
}

func TestFederationRejectsUnsafeConfigAndStopsAtRetryLimit(t *testing.T) {
	n := newFederationNode(t)
	for _, p := range []hub.FederationPeerOptions{{NodeID: n.id, BaseURL: "http://127.0.0.1:1", Secret: federationTestSecret}, {NodeID: "peer", BaseURL: "http://192.0.2.1:1", Secret: federationTestSecret}, {NodeID: "peer", BaseURL: "http://127.0.0.1:1", Secret: "agt_" + federationTestSecret}} {
		if _, err := hub.NewFederation(context.Background(), n.st, hub.FederationOptions{Peer: p}); err == nil {
			t.Fatal("unsafe federation config accepted")
		}
	}
	peer := newFederationNode(t)
	peer.server.Close()
	peer.server = nil
	n.start(t, peer)
	n.opts.MaxAttempts = 2
	n.server.Close()
	n.server = httptest.NewUnstartedServer(nil)
	n.server.Listener.Close()
	var err error
	n.server.Listener, err = net.Listen("tcp", strings.TrimPrefix(n.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n.startWithOptions(t)
	sender := nodeReader(t, n, "limit-sender")
	if _, err := sender.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "unreachable"}); err != nil {
		t.Fatal(err)
	}
	n.run()
	waitFederation(t, func() bool { stats, _ := n.st.FederationStats(context.Background()); return stats.Dead == 1 })
}
