package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

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
