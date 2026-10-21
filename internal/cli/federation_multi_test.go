package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dofu27/agentchat-lan/internal/store"
)

func TestLoadMultiPeerFederationConfig(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "hub.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	secretA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	secretB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := os.WriteFile(filepath.Join(dir, "a.secret"), []byte(secretA), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.secret"), []byte(secretB), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := `{
  "peers": [
    {"node_id":"node-a","base_url":"http://127.0.0.1:19001","secret_file":"a.secret"},
    {"node_id":"node-b","base_url":"http://127.0.0.1:19002","secret_file":"b.secret"}
  ],
  "links": [
    {"peer_id":"node-a","local_room":"general","remote_room":"general"},
    {"peer_id":"node-b","local_room":"general","remote_room":"lobby"}
  ],
  "max_outbox": 10,
  "max_attempts": 2
}`
	path := filepath.Join(dir, "federation.json")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFederation(ctx, st, path, filepath.Join(dir, "files")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMultiPeerRejectsUnknownLink(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "hub.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := os.WriteFile(filepath.Join(dir, "a.secret"), []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := `{"peers":[{"node_id":"node-a","base_url":"http://127.0.0.1:19001","secret_file":"a.secret"}],"links":[{"peer_id":"node-x","local_room":"general","remote_room":"general"}]}`
	path := filepath.Join(dir, "federation.json")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFederation(ctx, st, path); err == nil {
		t.Fatal("unknown peer link accepted")
	}
}
