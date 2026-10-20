package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func TestMigrationsAndAdminTokenAreDurableAndHashed(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "store.sqlite")
	s, err := Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	token, created, err := s.EnsureAdminToken(ctx)
	if err != nil || !created || token == "" {
		t.Fatalf("EnsureAdminToken = %q, %v, %v", token, created, err)
	}
	if !s.IsAdminToken(ctx, token) || s.IsAdminToken(ctx, "wrong") {
		t.Fatal("admin token verification failed")
	}
	var stored string
	if err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='admin_token_hash'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token || len(stored) != 64 {
		t.Fatalf("admin token was not stored as SHA-256: %q", stored)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, created, err = s.EnsureAdminToken(ctx); err != nil || created {
		t.Fatalf("reinitialized admin token: created=%v err=%v", created, err)
	}
	var migrations, defaultRooms int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms WHERE id=?`, protocol.DefaultRoom).Scan(&defaultRooms); err != nil {
		t.Fatal(err)
	}
	if migrations != 4 || defaultRooms != 1 {
		t.Fatalf("migration/default room counts = %d/%d", migrations, defaultRooms)
	}
}

func TestConcurrentPerRoomSequenceAndAckCursor(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "concurrent.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.CreateInvite(ctx, "join-code", Invite{Room: protocol.DefaultRoom, TTL: time.Hour, Uses: 1}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Join(ctx, "join-code", protocol.Agent{ID: "agent-1", Name: "agent-1", Kind: "test"}, "agent-token"); err != nil {
		t.Fatal(err)
	}
	const count = 24
	seqs := make([]int64, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, sendErr := s.SendMessage(ctx, "agent-1", protocol.DefaultRoom, protocol.SendMessageRequest{ClientMsgID: fmt.Sprintf("m-%02d", i), Text: "concurrent"}, nil)
			if sendErr != nil {
				t.Errorf("send %d: %v", i, sendErr)
				return
			}
			seqs[i] = result.Message.Seq
		}(i)
	}
	wg.Wait()
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("room seqs = %v; want contiguous 1..%d", seqs, count)
		}
	}
	if err = s.Ack(ctx, "agent-1", map[string]int64{protocol.DefaultRoom: int64(count + 10)}); err != nil {
		t.Fatal(err)
	}
	var ack int64
	if err = s.db.QueryRowContext(ctx, `SELECT last_ack_seq FROM memberships WHERE agent_id='agent-1' AND room_id=?`, protocol.DefaultRoom).Scan(&ack); err != nil {
		t.Fatal(err)
	}
	if ack != count {
		t.Fatalf("ack=%d, want clamped %d", ack, count)
	}
	if err = s.Ack(ctx, "agent-1", map[string]int64{protocol.DefaultRoom: 2}); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT last_ack_seq FROM memberships WHERE agent_id='agent-1' AND room_id=?`, protocol.DefaultRoom).Scan(&ack); err != nil {
		t.Fatal(err)
	}
	if ack != count {
		t.Fatalf("ack cursor moved backwards to %d", ack)
	}
}
