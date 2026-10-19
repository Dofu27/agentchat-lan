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
	home := strings.TrimSpace(codexHome)
	if home == "" {
		home = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(userHome, ".codex")
	}
	dbPath := filepath.Join(home, "thread_history_1.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		return "", fmt.Errorf("read Codex thread history: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(dbPath) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", fmt.Errorf("open Codex thread history: %w", err)
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
