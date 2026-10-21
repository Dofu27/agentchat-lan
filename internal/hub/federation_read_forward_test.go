package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func startReadNode(t *testing.T, n, peer *federationNode) {
	t.Helper()
	n.opts = hub.FederationOptions{
		Peer:         hub.FederationPeerOptions{NodeID: peer.id, BaseURL: peer.url, Secret: federationTestSecret},
		Links:        []store.FederationLink{{PeerID: peer.id, LocalRoom: "general", RemoteRoom: "general"}},
		ReadReceipts: true, MaxOutbox: 100, MaxAttempts: 10,
		RetryBase: 50 * time.Millisecond, PollInterval: 20 * time.Millisecond,
		RequestTimeout: 100 * time.Millisecond, LeaseDuration: 300 * time.Millisecond,
	}
	n.readReceipts = true
	n.startWithOptions(t)
}

func TestFederationReadForwarding(t *testing.T) {
	a, b := newFederationNode(t), newFederationNode(t)
	startReadNode(t, a, b)
	startReadNode(t, b, a)
	a.run()
	b.run()

	b.fed.ForwardRead("general", "agt_forwarded", 0)
	waitFederation(t, func() bool {
		rows, err := a.st.FederationReadReceipts(context.Background(), "general")
		return err == nil && len(rows) == 1 && rows[0].ReadSeq == 0 && rows[0].AgentID == "agt_forwarded"
	})
}
