package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func TestInitializeAdminTokenDeterministicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")
	token := protocol.NewToken("adm")
	want := sha256.Sum256([]byte(token))
	for range 2 {
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.InitializeAdminToken(ctx, token); err != nil {
			s.Close()
			t.Fatal(err)
		}
		var stored string
		err = s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='admin_token_hash'").Scan(&stored)
		if err != nil || stored != hex.EncodeToString(want[:]) || !s.IsAdminToken(ctx, token) {
			s.Close()
			t.Fatal("initializer hash is not stable SHA-256 across reopen")
		}
		if err := s.InitializeAdminToken(ctx, protocol.NewToken("adm")); err == nil {
			s.Close()
			t.Fatal("different initializer credential replaced existing hash")
		}
		if !s.IsAdminToken(ctx, token) {
			s.Close()
			t.Fatal("rejected initializer changed existing credential")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
