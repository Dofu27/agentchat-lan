package hub_test

import (
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func TestFederatedTypingMultiPeerFanout(t *testing.T) {
	a, b, c := newFederationNode(t), newFederationNode(t), newFederationNode(t)
	a.opts = hub.FederationOptions{
		MaxAttempts: 2, RetryBase: 20 * time.Millisecond, PollInterval: 20 * time.Millisecond,
		RequestTimeout: 200 * time.Millisecond, LeaseDuration: time.Second,
	}
	a.opts.Peers = []hub.FederationPeerOptions{
		{NodeID: b.id, BaseURL: b.url, Secret: federationTestSecret},
		{NodeID: c.id, BaseURL: c.url, Secret: federationTestSecret},
	}
	for _, n := range []*federationNode{b, c} {
		a.opts.Links = append(a.opts.Links, store.FederationLink{PeerID: n.id, LocalRoom: "general", RemoteRoom: "general"})
		a.opts.TypingLinks = append(a.opts.TypingLinks, store.FederationTypingLink{PeerID: n.id, Kind: "room", LinkID: "typing-test", Epoch: 1, LocalRoom: "general", RemoteRoom: "general"})
	}
	a.startWithOptions(t)
	for _, x := range []*federationNode{b, c} {
		x.opts = hub.FederationOptions{
			Peer:           hub.FederationPeerOptions{NodeID: a.id, BaseURL: a.url, Secret: federationTestSecret},
			Links:          []store.FederationLink{{PeerID: a.id, LocalRoom: "general", RemoteRoom: "general"}},
			TypingLinks:    []store.FederationTypingLink{{Kind: "room", LinkID: "typing-test", Epoch: 1, LocalRoom: "general", RemoteRoom: "general"}},
			RequestTimeout: 200 * time.Millisecond, LeaseDuration: time.Second,
		}
		x.startWithOptions(t)
	}
	ca, cb, cc := nodeReader(t, a, "alice"), nodeReader(t, b, "bob"), nodeReader(t, c, "carol")
	for _, n := range []*federationNode{a, b, c} {
		n.run()
	}
	bch, cch := typingStream(t, b, cb, "general"), typingStream(t, c, cc, "general")
	postTyping(t, a, ca, "general", true)
	awaitRemote(t, bch, "alice", true)
	awaitRemote(t, cch, "alice", true)
	postTyping(t, a, ca, "general", false)
	awaitRemote(t, bch, "alice", false)
	awaitRemote(t, cch, "alice", false)
}
