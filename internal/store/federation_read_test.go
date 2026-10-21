package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestFederationReadReceiptsAreMonotonicAndLinked(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "hub.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.ConfigureFederation(ctx, []FederationPeer{{NodeID: "remote-peer", BaseURL: "http://127.0.0.1:1", SecretHash: strings.Repeat("a", 64)}}, []FederationLink{{PeerID: "remote-peer", LocalRoom: "general", RemoteRoom: "general"}}, FederationLimits{MaxOutbox: 10, MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordFederationRead(ctx, "remote-peer", "general", "agt_remote", 3); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordFederationRead(ctx, "remote-peer", "general", "agt_remote", 2); err != nil {
		t.Fatal(err)
	}
	receipts, err := st.FederationReadReceipts(ctx, "general")
	if err != nil || len(receipts) != 1 || receipts[0].ReadSeq != 0 {
		t.Fatalf("receipts=%+v err=%v", receipts, err)
	}
	if err := st.RecordFederationRead(ctx, "unknown-peer", "general", "agt_remote", 4); err == nil {
		t.Fatal("unknown peer accepted")
	}
	if err := st.RecordFederationRead(ctx, "remote-peer", "unknown-room", "agt_remote", 4); err == nil {
		t.Fatal("unknown room accepted")
	}
	targets, err := st.FederationReadTargets(ctx, "general")
	if err != nil || len(targets) != 1 || targets[0].PeerID != "remote-peer" {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
}
