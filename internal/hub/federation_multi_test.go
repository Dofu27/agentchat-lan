package hub_test

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func (n *federationNode) startMultiFiles(t *testing.T, peers ...*federationNode) {
	t.Helper()
	opts := hub.FederationOptions{FileDir: filepath.Join(filepath.Dir(n.dbPath), "files"), MaxOutbox: 100, MaxAttempts: 10, RetryBase: 50 * time.Millisecond, PollInterval: 20 * time.Millisecond, RequestTimeout: 500 * time.Millisecond, LeaseDuration: 2 * time.Second}
	for _, peer := range peers {
		opts.Peers = append(opts.Peers, hub.FederationPeerOptions{NodeID: peer.id, BaseURL: peer.url, Secret: federationTestSecret})
		opts.Links = append(opts.Links, store.FederationLink{PeerID: peer.id, LocalRoom: "general", RemoteRoom: "general"})
		opts.FileLinks = append(opts.FileLinks, store.FederationFileLink{PeerID: peer.id, LocalRoom: "general", RemoteRoom: "general", LinkID: "test-link", Epoch: 1})
	}
	n.opts = opts
	n.startWithOptions(t)
}

func (n *federationNode) startMulti(t *testing.T, peers ...*federationNode) {
	t.Helper()
	opts := hub.FederationOptions{MaxOutbox: 100, MaxAttempts: 10, RetryBase: 50 * time.Millisecond, PollInterval: 20 * time.Millisecond, RequestTimeout: 100 * time.Millisecond, LeaseDuration: 300 * time.Millisecond}
	for _, peer := range peers {
		opts.Peers = append(opts.Peers, hub.FederationPeerOptions{NodeID: peer.id, BaseURL: peer.url, Secret: federationTestSecret})
		opts.Links = append(opts.Links, store.FederationLink{PeerID: peer.id, LocalRoom: "general", RemoteRoom: "general"})
	}
	n.opts = opts
	n.startWithOptions(t)
}

func TestFederationMultiPeerFanout(t *testing.T) {
	a, b, c := newFederationNode(t), newFederationNode(t), newFederationNode(t)
	a.startMulti(t, b, c)
	b.start(t, a)
	c.start(t, a)
	a.run()
	b.run()
	c.run()

	sender := nodeReader(t, a, "alice")
	bReader := nodeReader(t, b, "bob")
	cReader := nodeReader(t, c, "carol")
	if _, err := sender.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "fanout"}); err != nil {
		t.Fatal(err)
	}
	waitFederation(t, func() bool {
		stats, _ := a.st.FederationStats(context.Background())
		return stats.Delivered == 2
	})
	for _, nodeReader := range []struct {
		node   *federationNode
		reader *client.Client
	}{{b, bReader}, {c, cReader}} {
		history, _, err := nodeReader.node.st.History(context.Background(), senderBIdentity(t, nodeReader.reader), "general", 0, 0, 100)
		if err != nil || len(history) != 1 || history[0].Text != "fanout" {
			t.Fatalf("multi-peer delivery missing: %v %v", history, err)
		}
	}
}

func TestFederationMultiPeerFileFanout(t *testing.T) {
	a, b, c := newFederationNode(t), newFederationNode(t), newFederationNode(t)
	a.startMultiFiles(t, b, c)
	startFileNode(t, b, a)
	startFileNode(t, c, a)
	a.run()
	b.run()
	c.run()

	bReader := nodeReader(t, b, "bob-file")
	cReader := nodeReader(t, c, "carol-file")
	data := bytes.Repeat([]byte("multi-peer attachment "), 1000)
	fileEnvelope(t, a, data)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		bh, _, _ := b.st.History(context.Background(), senderBIdentity(t, bReader), "general", 0, 0, 20)
		ch, _, _ := c.st.History(context.Background(), senderBIdentity(t, cReader), "general", 0, 0, 20)
		if len(bh) == 1 && len(ch) == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("multi-peer file fanout timed out")
}

func TestFederationMultiPeerOfflinePeerDoesNotBlock(t *testing.T) {
	a, b, c := newFederationNode(t), newFederationNode(t), newFederationNode(t)
	a.startMulti(t, b, c)
	c.start(t, a)
	a.run()
	c.run()

	sender := nodeReader(t, a, "alice")
	cReader := nodeReader(t, c, "carol")
	if _, err := sender.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "isolated"}); err != nil {
		t.Fatal(err)
	}
	waitFederation(t, func() bool {
		stats, _ := a.st.FederationStats(context.Background())
		return stats.Delivered == 1
	})
	history, _, err := c.st.History(context.Background(), senderBIdentity(t, cReader), "general", 0, 0, 100)
	if err != nil || len(history) != 1 || history[0].Text != "isolated" {
		t.Fatalf("online peer was blocked by offline peer: %v %v", history, err)
	}
}

func TestFederationMultiPeerSlowPeerDoesNotBlock(t *testing.T) {
	a, b, c := newFederationNode(t), newFederationNode(t), newFederationNode(t)
	a.startMulti(t, b, c)
	b.start(t, a)
	c.start(t, a)
	baseHandler := *b.handler.Load()
	b.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/federation/v1/messages" {
			time.Sleep(2 * time.Second)
		}
		baseHandler.ServeHTTP(w, r)
	}))
	a.run()
	b.run()
	c.run()

	sender := nodeReader(t, a, "alice")
	cReader := nodeReader(t, c, "carol")
	start := time.Now()
	if _, err := sender.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "slow peer"}); err != nil {
		t.Fatal(err)
	}
	waitFederation(t, func() bool {
		history, _, _ := c.st.History(context.Background(), senderBIdentity(t, cReader), "general", 0, 0, 20)
		return len(history) == 1
	})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("slow peer blocked C for %v", elapsed)
	}
}

func TestFederationMultiPeerDoesNotForwardThirdParty(t *testing.T) {
	a, b, c := newFederationNode(t), newFederationNode(t), newFederationNode(t)
	a.startMulti(t, b, c)
	b.start(t, a)
	c.start(t, a)
	a.run()
	b.run()
	c.run()

	envelope := store.FederationEnvelope{
		Version: 1, OriginNodeID: b.id, OriginMessageID: "third-party",
		SourceRoom: "general", DestinationRoom: "general",
		Text: "do not forward", Sender: protocol.AgentRef{ID: "bob", Name: "bob", Kind: "test"},
		SentAt: time.Now().UTC(),
	}
	if status, _ := fedRequest(t, a, b.id, federationTestSecret, envelope); status != 200 {
		t.Fatalf("inbound status %d", status)
	}
	time.Sleep(200 * time.Millisecond)
	if stats, _ := a.st.FederationStats(context.Background()); stats.Delivered != 0 || stats.Pending != 0 || stats.Inflight != 0 {
		t.Fatalf("A forwarded a third-party message: %+v", stats)
	}
	if history, _, err := c.st.History(context.Background(), senderBIdentity(t, nodeReader(t, c, "carol")), "general", 0, 0, 100); err != nil || len(history) != 0 {
		t.Fatalf("C received a forwarded third-party message: %v %v", history, err)
	}
}
