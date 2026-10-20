package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dofu27/agentchat-lan/internal/store"
)

func TestFederationOperatorCLIReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := hubDBPath(dir)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	// A v0.9 database can be inspected without applying the new ops migration.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DROP TABLE federation_operator_retries; DELETE FROM schema_migrations WHERE version=3"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	app := &App{Stdout: &out, Stderr: &errs}
	if code := app.Run([]string{"hub", "federation", "status", "--data", dir}); code != 0 {
		t.Fatal(errs.String())
	}
	var result struct {
		Source string                     `json:"source"`
		NodeID string                     `json:"node_id"`
		Outbox store.FederationQueueStats `json:"outbox"`
	}
	if err = json.Unmarshal(out.Bytes(), &result); err != nil || result.Source != "persisted_state_only" || result.NodeID != "" {
		t.Fatalf("bad status %s %v", out.String(), err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("read-only CLI mutated database")
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	out.Reset()
	errs.Reset()
	if code := app.Run([]string{"hub", "federation", "status", "--data", missing}); code == 0 {
		t.Fatal("missing db accepted")
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("status created missing directory")
	}
}
