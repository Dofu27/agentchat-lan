package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func TestFederationOutboxAndInboundIdempotency(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "agentchat.db")
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, err := st.CreateInvite(ctx, "fed-invite", store.Invite{Room: "general", TTL: time.Hour, Uses: 2}, "test"); err != nil {
		t.Fatal(err)
	}
	agent := protocol.Agent{ID: "agt_fed_test", Name: "fed-test", Kind: "dsh"}
	if _, err := st.Join(ctx, "fed-invite", agent, "token-federation-test"); err != nil {
		t.Fatal(err)
	}
	peer, err := st.ConfigureFederationPeer(ctx, "peer-b", "http://127.0.0.1:1", "0123456789abcdef", "node_b")
	if err != nil {
		t.Fatal(err)
	}
	link, err := st.CreateFederationLink(ctx, "general", peer.ID, "general", "test")
	if err != nil {
		t.Fatal(err)
	}

	result, err := st.SendMessage(ctx, agent.ID, "general", protocol.SendMessageRequest{Text: "federated hello", ClientMsgID: "fed-client-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed {
		t.Fatal("unexpected replay")
	}
	counts, err := st.FederationOutboxCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["pending"] != 1 {
		t.Fatalf("pending outbox = %d, want 1", counts["pending"])
	}
	entries, err := st.ClaimFederationOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("claimed %d entries, want 1", len(entries))
	}
	if entries[0].OriginID != result.Message.ID {
		t.Fatalf("origin id = %q, want %q", entries[0].OriginID, result.Message.ID)
	}
	if err := st.MarkFederationDelivered(ctx, entries[0].ID); err != nil {
		t.Fatal(err)
	}

	payload := store.FederationPayload{
		Room: "general", OriginNode: "node_b", OriginID: "remote-1",
		Sender: protocol.AgentRef{ID: "agt_remote", Name: "remote", Kind: "dsh"},
		Text:   "remote hello", TS: time.Now().UTC(),
	}
	message, replayed, err := st.InsertFederatedMessage(ctx, link.ID, payload)
	if err != nil {
		t.Fatal(err)
	}
	if replayed || message.Text != "remote hello" {
		t.Fatalf("inbound replay=%v text=%q", replayed, message.Text)
	}
	_, replayed, err = st.InsertFederatedMessage(ctx, link.ID, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed {
		t.Fatal("duplicate inbound message was not treated as replay")
	}
}
