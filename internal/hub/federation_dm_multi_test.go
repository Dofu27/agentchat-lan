package hub_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func TestFederatedDMMultiPeerFanout(t *testing.T) {
	ctx := context.Background()
	nodes := []*federationNode{newFederationNode(t), newFederationNode(t), newFederationNode(t)}
	ids := []string{"person-a", "person-b", "person-c"}
	tokens := []string{"synthetic-a-token", "synthetic-b-token", "synthetic-c-token"}
	for i, n := range nodes {
		if _, err := n.st.CreateInvite(ctx, "invite", store.Invite{Room: "general", TTL: time.Hour, Uses: 1}, "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := n.st.Join(ctx, "invite", protocol.Agent{ID: ids[i], Name: ids[i], Kind: "human"}, tokens[i]); err != nil {
			t.Fatal(err)
		}
	}
	conversation := func(i, j int) string { return fmt.Sprintf("dm-%d-%d", min(i, j), max(i, j)) }
	for i, n := range nodes {
		opts := hub.FederationOptions{MaxOutbox: 100, MaxAttempts: 5, RetryBase: 20 * time.Millisecond, PollInterval: 20 * time.Millisecond, RequestTimeout: 300 * time.Millisecond, LeaseDuration: time.Second}
		for j, peer := range nodes {
			if i == j {
				continue
			}
			opts.Peers = append(opts.Peers, hub.FederationPeerOptions{NodeID: peer.id, BaseURL: peer.url, Secret: federationTestSecret})
			opts.DMConversations = append(opts.DMConversations, store.FederationDMConversation{
				PeerID: peer.id, ConversationID: conversation(i, j), LinkID: "link-" + conversation(i, j), Epoch: 1,
				LocalAgentID: ids[i], RemoteAgentID: ids[j],
			})
		}
		n.opts = opts
		n.startWithOptions(t)
	}
	rooms := map[string]map[string]string{}
	for i, n := range nodes {
		rooms[ids[i]] = map[string]string{}
		for j := range nodes {
			if i == j {
				continue
			}
			room, err := n.st.OpenFederatedDM(ctx, ids[i], conversation(i, j), 1)
			if err != nil {
				t.Fatal(err)
			}
			rooms[ids[i]][ids[j]] = room.ID
		}
	}
	for _, n := range nodes {
		n.run()
	}
	ca := client.New(nodes[0].url, tokens[0])
	for _, to := range []string{"person-b", "person-c"} {
		if _, err := ca.SendMessage(ctx, rooms["person-a"][to], protocol.SendMessageRequest{Text: "to " + to}); err != nil {
			t.Fatal(err)
		}
	}
	waitFederation(t, func() bool {
		status, _ := nodes[0].st.FederationOperatorStatus(ctx)
		return status.DirectMessages.Delivered == 2
	})
	for _, pair := range []struct {
		node int
		from string
	}{{1, "person-a"}, {2, "person-a"}} {
		msgs, _, err := nodes[pair.node].st.History(ctx, ids[pair.node], rooms[ids[pair.node]][pair.from], 0, 0, 10)
		if err != nil || len(msgs) != 1 {
			t.Fatalf("node %s history=%d err=%v", ids[pair.node], len(msgs), err)
		}
	}
}
