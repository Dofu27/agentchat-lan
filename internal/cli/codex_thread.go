package cli

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// currentCodexThread reads the most recently started thread from the Codex
// Desktop history database. The bridge uses it so a restarted Codex app does
// not leave a stale hard-coded thread ID behind.
func currentCodexThread(codexHome string) (string, error) {
	db, err := openCodexHistory(codexHome)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var threadID string
	if err := db.QueryRow(`SELECT thread_id FROM thread_turns ORDER BY started_at DESC LIMIT 1`).Scan(&threadID); err != nil {
		return "", fmt.Errorf("find current Codex thread: %w", err)
	}
	if strings.TrimSpace(threadID) == "" {
		return "", fmt.Errorf("current Codex thread is empty")
	}
	return threadID, nil
}

// latestTurnStarted returns the newest started_at for a thread. The bridge
// compares this value before and after queueing to distinguish "accepted by the
// queue" from "the Codex app actually started a turn".
func latestTurnStarted(codexHome, threadID string) (int64, error) {
	db, err := openCodexHistory(codexHome)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var started int64
	err = db.QueryRow(
		`SELECT COALESCE(MAX(started_at), 0) FROM thread_turns WHERE thread_id = ?`,
		threadID,
	).Scan(&started)
	if err != nil {
		return 0, fmt.Errorf("read Codex turn time: %w", err)
	}
	return started, nil
}

func openCodexHistory(codexHome string) (*sql.DB, error) {
	home := strings.TrimSpace(codexHome)
	if home == "" {
		home = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".codex")
	}
	dbPath := filepath.Join(home, "thread_history_1.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("read Codex thread history: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open Codex thread history: %w", err)
	}
	return db, nil
}
