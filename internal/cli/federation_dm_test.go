package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func TestFederatedDMCLIConfigAndReadOnlyStatus(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), hubDBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	st.CreateInvite(ctx, "invite", store.Invite{Room: "general", Uses: 1, TTL: time.Hour}, "test")
	if _, err = st.Join(ctx, "invite", protocol.Agent{ID: "person", Name: "person", Kind: "human"}, "synthetic-person-token"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "peer.secret"), []byte("dedicated-test-only-dm-secret-123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "federation.json")
	cfg := `{"peer":{"node_id":"remote","base_url":"http://127.0.0.1:1","secret_file":"peer.secret"},"dm_conversations":[{"conversation_id":"approved","link_id":"link","epoch":1,"local_agent_id":"person","remote_agent_id":"remote-person"}]}`
	os.WriteFile(path, []byte(cfg), 0600)
	if _, err = loadFederation(ctx, st, path); err != nil {
		t.Fatal(err)
	}
	room, err := st.OpenFederatedDM(ctx, "person", "approved", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SendMessage(ctx, "person", room.ID, protocol.SendMessageRequest{Text: "private must not appear in status"}, nil); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	app := &App{Stdout: &out, Stderr: &errs}
	if app.Run([]string{"hub", "federation", "status", "--data", dir}) != 0 {
		t.Fatal(errs.String())
	}
	var result struct {
		DirectMessages store.FederationQueueStats `json:"direct_messages"`
	}
	if err = json.Unmarshal(out.Bytes(), &result); err != nil || result.DirectMessages.Pending != 1 {
		t.Fatal(out.String(), err)
	}
	for _, s := range []string{"private must not", "dedicated-test", "remote-person", "local_agent_id"} {
		if bytes.Contains(out.Bytes(), []byte(s)) {
			t.Fatal("status leaked private config/payload")
		}
	}
	os.WriteFile(path, []byte(`{"peer":{"node_id":"remote","base_url":"http://127.0.0.1:1","secret_file":"peer.secret"},"dm_conversations":[{"conversation_id":"bad","link_id":"link","epoch":1,"local_agent_id":"not-approved","remote_agent_id":"remote-person"}]}`), 0600)
	if _, err = loadFederation(ctx, st, path); err == nil {
		t.Fatal("unregistered local person configured")
	}
	// Retention must not delete a private source message awaiting delivery.
	db, err := sql.Open("sqlite", hubDBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE messages SET created_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	out.Reset()
	errs.Reset()
	if app.Run([]string{"hub", "gc", "--data", dir, "--days", "1", "--apply", "--json"}) != 0 {
		t.Fatal(errs.String())
	}
	var gc gcResult
	json.Unmarshal(out.Bytes(), &gc)
	if gc.Messages != 0 {
		t.Fatal("pending private source message removed", out.String())
	}
}
