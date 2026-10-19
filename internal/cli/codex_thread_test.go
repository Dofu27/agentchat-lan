package cli

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCurrentCodexThread(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "thread_history_1.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE thread_turns (thread_id TEXT NOT NULL, started_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO thread_turns(thread_id, started_at) VALUES ('old', 1), ('current', 2)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := currentCodexThread(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "current" {
		t.Fatalf("currentCodexThread() = %q, want current", got)
	}
	started, err := latestTurnStarted(dir, "current")
	if err != nil {
		t.Fatal(err)
	}
	if started != 2 {
		t.Fatalf("latestTurnStarted() = %d, want 2", started)
	}
}
