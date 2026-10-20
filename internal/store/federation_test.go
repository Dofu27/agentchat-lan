package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func federationTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	_, err = s.CreateInvite(context.Background(), "local-code", Invite{Room: "general", TTL: time.Hour, Uses: 1}, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Join(context.Background(), "local-code", protocol.Agent{ID: "local", Name: "local", Kind: "test"}, "test-local-token")
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}
func enableFederationTest(t *testing.T, s *Store, cap, attempts int) string {
	t.Helper()
	sum := sha256.Sum256([]byte("dedicated-test-federation-secret-32"))
	id, err := s.ConfigureFederation(context.Background(), []FederationPeer{{NodeID: "remote-node", BaseURL: "http://127.0.0.1:1", SecretHash: hex.EncodeToString(sum[:])}}, []FederationLink{{PeerID: "remote-node", LocalRoom: "general", RemoteRoom: "general"}}, FederationLimits{MaxOutbox: cap, MaxAttempts: attempts})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func TestFederationDisabledMigrationAndDurableIdentity(t *testing.T) {
	ctx := context.Background()
	s, path := federationTestStore(t)
	if _, err := s.SendMessage(ctx, "local", "general", protocol.SendMessageRequest{Text: "ordinary"}, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM federation_node").Scan(&count); err != nil || count != 0 {
		t.Fatalf("implicit identity: %d %v", count, err)
	}
	stats, err := s.FederationStats(ctx)
	if err != nil || stats != (FederationQueueStats{}) {
		t.Fatalf("disabled outbox changed: %v %v", stats, err)
	}
	id := enableFederationTest(t, s, 3, 2)
	s.Close()
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	id2, err := reopened.FederationIdentity(ctx)
	if err != nil || id != id2 {
		t.Fatal("node identity changed")
	}
	if _, err := reopened.SendMessage(ctx, "local", "general", protocol.SendMessageRequest{Text: "default off after restart"}, nil); err != nil {
		t.Fatal(err)
	}
	stats, err = reopened.FederationStats(ctx)
	if err != nil || stats.Pending != 0 {
		t.Fatal("persisted config implicitly enabled forwarding")
	}
}
func TestFederationOutboxAtomicCapAndLeaseRecovery(t *testing.T) {
	ctx := context.Background()
	s, path := federationTestStore(t)
	id := enableFederationTest(t, s, 1, 2)
	first, err := s.SendMessage(ctx, "local", "general", protocol.SendMessageRequest{Text: "durable", ClientMsgID: "once"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.SendMessage(ctx, "local", "general", protocol.SendMessageRequest{Text: "durable", ClientMsgID: "once"}, nil)
	if err != nil || !replay.Replayed || first.Message.ID != replay.Message.ID {
		t.Fatal("local idempotency changed")
	}
	if _, err := s.SendMessage(ctx, "local", "general", protocol.SendMessageRequest{Text: "overflow"}, nil); !errors.Is(err, ErrFederationFull) {
		t.Fatalf("expected backpressure: %v", err)
	}
	history, _, err := s.History(ctx, "local", "general", 0, 0, 20)
	if err != nil || len(history) != 1 {
		t.Fatalf("outbox failure did not roll back message: %v %v", history, err)
	}
	now := time.Now().Add(time.Second)
	job, err := s.ClaimFederationJob(ctx, now, time.Second)
	if err != nil || job == nil || job.Attempts != 1 {
		t.Fatalf("claim: %v %v", job, err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := enableFederationTest(t, s, 1, 2); got != id {
		t.Fatal("identity lost")
	}
	if early, err := s.ClaimFederationJob(ctx, now.Add(500*time.Millisecond), time.Second); err != nil || early != nil {
		t.Fatal("unexpired lease stolen")
	}
	second, err := s.ClaimFederationJob(ctx, now.Add(2*time.Second), time.Second)
	if err != nil || second == nil || second.Attempts != 2 || second.Lease == job.Lease {
		t.Fatal("expired lease not recovered")
	}
	if err := s.FinishFederationJob(ctx, *job, true, false, now, ""); err != nil {
		t.Fatal(err)
	}
	stats, _ := s.FederationStats(ctx)
	if stats.Inflight != 1 {
		t.Fatal("stale worker completed newer attempt")
	}
	if err := s.FinishFederationJob(ctx, *second, false, false, now, "network failure"); err != nil {
		t.Fatal(err)
	}
	stats, _ = s.FederationStats(ctx)
	if stats.Dead != 1 {
		t.Fatal("retry limit not enforced")
	}
	if _, err := s.SendMessage(ctx, "local", "general", protocol.SendMessageRequest{Text: "terminal eviction"}, nil); err != nil {
		t.Fatal(err)
	}
	stats, _ = s.FederationStats(ctx)
	if stats.Pending != 1 || stats.Dead != 0 {
		t.Fatal("terminal history not bounded")
	}
}
func TestFederationInboundConcurrentIdempotencyAndNoLoop(t *testing.T) {
	ctx := context.Background()
	s, path := federationTestStore(t)
	enableFederationTest(t, s, 10, 3)
	e := FederationEnvelope{Version: 1, OriginNodeID: "remote-node", OriginMessageID: "remote-message", SourceRoom: "general", DestinationRoom: "general", Sender: protocol.AgentRef{ID: "foreign-agent", Name: "remote-sender", Kind: "test"}, Text: "remote text", SentAt: time.Now().UTC()}
	const n = 12
	receipts := make(chan FederationReceipt, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { r, err := s.ImportFederation(ctx, "remote-node", e); receipts <- r; errs <- err })
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var localID string
	fresh := 0
	for r := range receipts {
		if localID != "" && r.LocalMessageID != localID {
			t.Fatal("duplicate inserted twice")
		}
		localID = r.LocalMessageID
		if !r.Replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh inserts=%d", fresh)
	}
	history, _, err := s.History(ctx, "local", "general", 0, 0, 20)
	if err != nil || len(history) != 1 {
		t.Fatal("duplicate message rows")
	}
	m := history[0]
	if m.From.Kind != "federation" || m.From.ID == e.Sender.ID || m.Meta.Federation == nil || m.Meta.Federation.Sender != e.Sender {
		t.Fatal("remote sender forged local identity")
	}
	stats, _ := s.FederationStats(ctx)
	if stats != (FederationQueueStats{}) {
		t.Fatal("import was forwarded (loop)")
	}
	changed := e
	changed.Text = "conflict"
	if _, err := s.ImportFederation(ctx, "remote-node", changed); !errors.Is(err, ErrFederationConflict) {
		t.Fatalf("conflicting duplicate accepted: %v", err)
	}
	if _, err := s.ImportFederation(ctx, "other-peer", e); err == nil {
		t.Fatal("third-node origin accepted")
	}
	changed = e
	changed.DestinationRoom = "not-linked"
	if _, err := s.ImportFederation(ctx, "remote-node", changed); err == nil {
		t.Fatal("unlinked destination accepted")
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	enableFederationTest(t, s, 10, 3)
	if r, err := s.ImportFederation(ctx, "remote-node", e); err != nil || !r.Replayed || r.LocalMessageID != localID {
		t.Fatalf("dedupe lost at restart: %v %v", r, err)
	}
	// A GC'd message must not be re-imported when its origin retries later.
	if _, err := s.db.Exec("DELETE FROM messages WHERE id=?", localID); err != nil {
		t.Fatal(err)
	}
	if r, err := s.ImportFederation(ctx, "remote-node", e); err != nil || !r.Replayed || r.LocalMessageID != localID {
		t.Fatal("receipt did not survive local retention")
	}
}
func TestFederationBridgeMessagesAndDisabledLinks(t *testing.T) {
	ctx := context.Background()
	s, _ := federationTestStore(t)
	enableFederationTest(t, s, 10, 3)
	if _, err := s.db.Exec("UPDATE agents SET kind='bridge' WHERE id='local'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendMessage(ctx, "local", "general", protocol.SendMessageRequest{Text: "bridge text"}, nil); err != nil {
		t.Fatal(err)
	}
	stats, _ := s.FederationStats(ctx)
	if stats.Pending != 0 {
		t.Fatal("bridge message reforwarded")
	}
	if _, err := s.db.Exec("UPDATE agents SET kind='test' WHERE id='local'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendMessage(ctx, "local", "general", protocol.SendMessageRequest{Text: "ordinary"}, nil); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimFederationJob(ctx, time.Now().Add(time.Second), time.Second)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	var e FederationEnvelope
	// A changed link is not allowed to retarget the immutable queued envelope.
	e.SourceRoom = "general"
	e.DestinationRoom = "general"
	if _, err := s.db.Exec("UPDATE federation_room_links SET remote_room='other'"); err != nil {
		t.Fatal(err)
	}
	allowed, err := s.FederationJobAuthorized(ctx, *job, e)
	if err != nil || allowed {
		t.Fatal("queued message escaped its original link")
	}
}

func TestFederationRejectsReusedAgentCredential(t *testing.T) {
	s, _ := federationTestStore(t)
	sum := sha256.Sum256([]byte("test-local-token"))
	_, err := s.ConfigureFederation(context.Background(), []FederationPeer{{NodeID: "remote-node", BaseURL: "http://127.0.0.1:1", SecretHash: hex.EncodeToString(sum[:])}}, nil, FederationLimits{MaxOutbox: 10, MaxAttempts: 2})
	if err == nil {
		t.Fatal("agent token reused as peer secret")
	}
}
