package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func readReceiptStore(t *testing.T) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reads.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err = s.CreateInvite(ctx, "reads-invite", Invite{Room: protocol.DefaultRoom, TTL: time.Hour, Uses: 2}, "test"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"reader", "other"} {
		if _, err = s.Join(ctx, "reads-invite", protocol.Agent{ID: id, Name: id, Kind: "test"}, "token-"+id); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		if _, err = s.SendMessage(ctx, "reader", protocol.DefaultRoom, protocol.SendMessageRequest{Text: fmt.Sprint(i)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	return s, path
}

func TestMarkReadIndependentMonotonicPersistent(t *testing.T) {
	s, path := readReceiptStore(t)
	ctx := context.Background()
	if err := s.Ack(ctx, "reader", map[string]int64{protocol.DefaultRoom: 1}); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []int64{0, 2, 2, 1} {
		r, err := s.MarkRead(ctx, "reader", protocol.DefaultRoom, seq)
		want := int64(2)
		if seq == 0 {
			want = 0
		}
		if err != nil || r.ReadSeq != want || r.AgentID != "reader" || r.Room != protocol.DefaultRoom {
			t.Fatalf("MarkRead(%d)=%+v, %v", seq, r, err)
		}
	}
	inbox, err := s.Inbox(ctx, "reader", 20, nil, nil)
	if err != nil || len(inbox) != 2 || inbox[0].Seq != 2 {
		t.Fatalf("read changed ACK/inbox: %+v, %v", inbox, err)
	}
	receipts, err := s.RoomReceipts(ctx, protocol.DefaultRoom)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range receipts {
		if r["agent_id"] == "reader" && (r["last_ack_seq"] != int64(1) || r["read_seq"] != int64(2)) {
			t.Fatal("independent receipt fields", r)
		}
	}
	if err = s.Ack(ctx, "reader", map[string]int64{protocol.DefaultRoom: 3}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var ack, read int64
	if err = reopened.db.QueryRow(`SELECT last_ack_seq,read_seq FROM memberships WHERE agent_id='reader' AND room_id='general'`).Scan(&ack, &read); err != nil || ack != 3 || read != 2 {
		t.Fatalf("persistent independent cursors=%d/%d, %v", ack, read, err)
	}
}

func TestMarkReadValidationAndLocalScope(t *testing.T) {
	s, _ := readReceiptStore(t)
	ctx := context.Background()
	for _, seq := range []int64{-1, 4} {
		if _, err := s.MarkRead(ctx, "reader", protocol.DefaultRoom, seq); !errors.Is(err, ErrInvalidReadSeq) {
			t.Fatalf("invalid seq %d: %v", seq, err)
		}
	}
	if _, err := s.MarkRead(ctx, "reader", "missing", 0); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown room", err)
	}
	if err := s.CreateRoom(ctx, protocol.Room{ID: "private", Kind: "dm"}, []string{"reader"}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRead(ctx, "other", "private", 0); !errors.Is(err, ErrForbidden) {
		t.Fatal("nonmember", err)
	}
	if _, err := s.MarkRead(ctx, "reader", "private", 0); err != nil {
		t.Fatal("local DM", err)
	}
	if err := s.ArchiveRoom(ctx, "private", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRead(ctx, "reader", "private", 0); !errors.Is(err, ErrNotFound) {
		t.Fatal("archived room", err)
	}
	if err := s.RevokeAgent(ctx, "other", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRead(ctx, "other", protocol.DefaultRoom, 0); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked store identity", err)
	}
	fdm, _, room := dmStore(t)
	if _, err := fdm.MarkRead(ctx, "local", room.ID, 0); !errors.Is(err, ErrForbidden) {
		t.Fatal("federated DM read contract must remain unavailable", err)
	}
}

func TestMarkReadConcurrentMaximum(t *testing.T) {
	s, _ := readReceiptStore(t)
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			if _, err := s.MarkRead(context.Background(), "reader", protocol.DefaultRoom, int64(i%4)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	r, err := s.MarkRead(context.Background(), "reader", protocol.DefaultRoom, 0)
	if err != nil || r.ReadSeq != 3 {
		t.Fatal(r, err)
	}
}

func TestMarkReadAllocatedSequenceBoundary(t *testing.T) {
	s, _ := readReceiptStore(t)
	ctx := context.Background()
	var max int64
	var sqliteVersion string
	if err := s.db.QueryRow(`SELECT next_seq FROM rooms WHERE id='general'`).Scan(&max); err != nil || max != 3 {
		t.Fatal("highest allocated sequence", max, err)
	}
	if _, err := s.MarkRead(ctx, "reader", "general", max); err != nil {
		t.Fatal("equality must be accepted", err)
	}
	if _, err := s.MarkRead(ctx, "reader", "general", max+1); !errors.Is(err, ErrInvalidReadSeq) {
		t.Fatal("next unused sequence must be rejected", err)
	}
	if err := s.CreateRoom(ctx, protocol.Room{ID: "empty-read"}, []string{"reader"}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRead(ctx, "reader", "empty-read", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRead(ctx, "reader", "empty-read", 1); !errors.Is(err, ErrInvalidReadSeq) {
		t.Fatal("empty room accepts only zero", err)
	}
	if err := s.db.QueryRow(`SELECT sqlite_version()`).Scan(&sqliteVersion); err != nil {
		t.Fatal(err)
	}
	t.Log("SQLite runtime", sqliteVersion, "supports UPDATE RETURNING")
}

func TestMarkReadStaysLocalOnFederationLink(t *testing.T) {
	s, _ := federationTestStore(t)
	enableFederationTest(t, s, 10, 3)
	ctx := context.Background()
	if _, err := s.SendMessage(ctx, "local", protocol.DefaultRoom, protocol.SendMessageRequest{Text: "linked room"}, nil); err != nil {
		t.Fatal(err)
	}
	before, err := s.FederationStats(ctx)
	if err != nil || before.Pending != 1 {
		t.Fatal(before, err)
	}
	if _, err = s.MarkRead(ctx, "local", protocol.DefaultRoom, 1); err != nil {
		t.Fatal(err)
	}
	after, err := s.FederationStats(ctx)
	if err != nil || before != after {
		t.Fatal("local read queued federation work", before, after, err)
	}
	inbox, err := s.Inbox(ctx, "local", 20, nil, nil)
	if err != nil || len(inbox) != 1 {
		t.Fatal("linked read consumed inbox or produced a message", inbox, err)
	}
}

func TestReadReceiptMigrationFromPreF4Database(t *testing.T) {
	s, path := readReceiptStore(t)
	ctx := context.Background()
	if err := s.Ack(ctx, "reader", map[string]int64{protocol.DefaultRoom: 2}); err != nil {
		t.Fatal(err)
	}
	// Recreate the actual pre-F4 membership shape, retaining real agents,
	// messages, foreign keys, and processing cursors for the upgrade test.
	if _, err := s.db.Exec(`ALTER TABLE memberships DROP COLUMN read_seq; DELETE FROM schema_migrations WHERE version=6`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var ack, read int64
	if err = upgraded.db.QueryRow(`SELECT last_ack_seq,read_seq FROM memberships WHERE agent_id='reader' AND room_id='general'`).Scan(&ack, &read); err != nil || ack != 2 || read != 0 {
		t.Fatalf("upgrade changed ACK or copied it into read: %d/%d %v", ack, read, err)
	}
	if _, err = upgraded.MarkRead(ctx, "reader", protocol.DefaultRoom, 3); err != nil {
		t.Fatal(err)
	}
}
