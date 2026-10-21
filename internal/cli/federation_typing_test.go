package cli

import (
	"context"
	"github.com/Dofu27/agentchat-lan/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func TestFederatedTypingConfigExplicitOptIn(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, hubDBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = os.WriteFile(filepath.Join(dir, "peer.secret"), []byte("synthetic-typing-config-secret-123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "federation.json")
	prefix := `{"peer":{"node_id":"remote","base_url":"http://127.0.0.1:1","secret_file":"peer.secret"},"links":[`
	for _, link := range []string{`{"local_room":"general","remote_room":"general","typing":true}`, `{"local_room":"general","remote_room":"general","typing":true,"link_id":"typing","epoch":0}`, `{"local_room":"general","remote_room":"general","typing":false,"link_id":"typing","epoch":1}`} {
		os.WriteFile(path, []byte(prefix+link+`]}`), 0600)
		if _, err = loadFederation(ctx, st, path); err == nil {
			t.Fatal("incomplete typing opt-in accepted", link)
		}
	}
	os.WriteFile(path, []byte(prefix+`{"local_room":"general","remote_room":"general","typing":true,"link_id":"typing","epoch":1}]}`), 0600)
	if _, err = loadFederation(ctx, st, path); err != nil {
		t.Fatal(err)
	}
}
