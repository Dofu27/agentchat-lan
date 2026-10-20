package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type gcResult struct {
	DryRun        bool   `json:"dry_run"`
	OlderThanDays int    `json:"older_than_days"`
	Messages      int64  `json:"messages"`
	Files         int64  `json:"files"`
	Bytes         int64  `json:"bytes"`
	FilesRoot     string `json:"files_root"`
}

func (a *App) hubGC(args []string) int {
	fs := a.newFlagSet("hub gc")
	data := fs.String("data", "./data", "data directory")
	days := fs.Int("days", 0, "delete messages older than this many days; 0 keeps all messages")
	apply := fs.Bool("apply", false, "actually delete; without this flag only report")
	jsonOut := fs.Bool("json", false, "print JSON")
	if code, ok := a.parse(fs, args); !ok {
		return code
	}
	if *days < 0 {
		return a.fail(fmt.Errorf("--days must be non-negative"))
	}
	absData, err := filepath.Abs(*data)
	if err != nil {
		return a.fail(err)
	}
	dbPath := filepath.Join(absData, "agentchat.db")
	if _, err := os.Stat(dbPath); err != nil {
		return a.fail(fmt.Errorf("open hub data: %w", err))
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=rw")
	if err != nil {
		return a.fail(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return a.fail(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return a.fail(err)
	}

	cutoff := time.Now().UTC().Add(-time.Duration(*days) * 24 * time.Hour).Format(time.RFC3339Nano)
	tx, err := db.Begin()
	if err != nil {
		return a.fail(err)
	}
	defer tx.Rollback()

	var messageCount int64
	if *days > 0 {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM messages WHERE created_at < ?`, cutoff).Scan(&messageCount); err != nil {
			return a.fail(err)
		}
		if *apply && messageCount > 0 {
			if _, err := tx.Exec(`DELETE FROM mentions WHERE message_id IN (SELECT id FROM messages WHERE created_at < ?)`, cutoff); err != nil {
				return a.fail(err)
			}
			if _, err := tx.Exec(`DELETE FROM attachments WHERE message_id IN (SELECT id FROM messages WHERE created_at < ?)`, cutoff); err != nil {
				return a.fail(err)
			}
			if _, err := tx.Exec(`DELETE FROM messages WHERE created_at < ?`, cutoff); err != nil {
				return a.fail(err)
			}
		}
	}
	type orphan struct {
		id   string
		path string
		size int64
	}
	rows, err := tx.Query(`SELECT f.id, f.storage_path, f.size FROM files f LEFT JOIN attachments a ON a.file_id = f.id WHERE a.file_id IS NULL`)
	if err != nil {
		return a.fail(err)
	}
	var orphans []orphan
	var fileBytes int64
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.id, &o.path, &o.size); err != nil {
			rows.Close()
			return a.fail(err)
		}
		orphans = append(orphans, o)
		fileBytes += o.size
	}
	if err := rows.Close(); err != nil {
		return a.fail(err)
	}
	if err := rows.Err(); err != nil {
		return a.fail(err)
	}
	if *apply && len(orphans) > 0 {
		for _, o := range orphans {
			if _, err := tx.Exec(`DELETE FROM files WHERE id=?`, o.id); err != nil {
				return a.fail(err)
			}
		}
	}
	if *apply {
		if err := tx.Commit(); err != nil {
			return a.fail(err)
		}
	} else {
		_ = tx.Rollback()
	}

	filesRoot := filepath.Join(absData, "files")
	var removed int64
	var removedBytes int64
	if *apply {
		for _, o := range orphans {
			rel := filepath.Clean(filepath.FromSlash(o.path))
			if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			full := filepath.Join(filesRoot, rel)
			rootClean := filepath.Clean(filesRoot)
			if !strings.HasPrefix(filepath.Clean(full), rootClean+string(filepath.Separator)) {
				continue
			}
			if err := os.Remove(full); err == nil {
				removed++
				removedBytes += o.size
			}
		}
	}

	result := gcResult{
		DryRun:        !*apply,
		OlderThanDays: *days,
		Messages:      messageCount,
		Files:         int64(len(orphans)),
		Bytes:         fileBytes,
		FilesRoot:     filesRoot,
	}
	if *apply {
		result.Files = removed
		result.Bytes = removedBytes
	}
	if *jsonOut {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Stdout, string(data))
		return 0
	}
	mode := "DRY-RUN"
	if *apply {
		mode = "APPLIED"
	}
	fmt.Fprintf(a.Stdout, "%s: messages=%d files=%d bytes=%d\n", mode, result.Messages, result.Files, result.Bytes)
	if !*apply {
		fmt.Fprintln(a.Stdout, "rerun with --apply to delete")
	}
	return 0
}
