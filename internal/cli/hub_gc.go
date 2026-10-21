package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/blobguard"
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

	var fileSchema int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='federation_file_outbox' AND type='table'`).Scan(&fileSchema); err != nil {
		return a.fail(err)
	}
	if fileSchema > 0 {
		release, err := blobguard.Acquire(filepath.Join(absData, "files"))
		if err != nil {
			return a.fail(fmt.Errorf("stop attachment hub before GC: %w", err))
		}
		defer release()
	}
	pinnedMessages, pinnedFiles := "", ""
	if fileSchema > 0 {
		pinnedMessages = ` AND id NOT IN (SELECT origin_message_id FROM federation_file_outbox WHERE state IN ('pending','inflight','waiting_receipt'))`
		now := strconv.FormatInt(time.Now().UnixMilli(), 10)
		pinnedFiles = ` AND f.id NOT IN (SELECT g.file_id FROM federation_blob_grants g JOIN federation_file_outbox o ON o.id=g.job_id WHERE o.state<>'delivered' AND o.deadline>` + now + ` UNION SELECT b.file_id FROM federation_transfer_blobs b JOIN federation_file_transfers t ON t.id=b.transfer_id WHERE t.state<>'committed' AND t.deadline>` + now + `)`
	}
	cutoff := time.Now().UTC().Add(-time.Duration(*days) * 24 * time.Hour).Format(time.RFC3339Nano)
	var dmSchema int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='federation_dm_outbox' AND type='table'`).Scan(&dmSchema); err != nil {
		return a.fail(err)
	}
	if dmSchema > 0 {
		pinnedMessages += ` AND id NOT IN (SELECT origin_message_id FROM federation_dm_outbox WHERE state IN ('pending','inflight'))`
	}
	tx, err := db.Begin()
	if err != nil {
		return a.fail(err)
	}
	defer tx.Rollback()
	if fileSchema > 0 && *apply {
		if err = cleanupFederationFiles(tx, filepath.Join(absData, "files")); err != nil {
			return a.fail(err)
		}
	}

	var messageCount int64
	if *days > 0 {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM messages WHERE created_at < ?`+pinnedMessages, cutoff).Scan(&messageCount); err != nil {
			return a.fail(err)
		}
		if *apply && messageCount > 0 {
			if _, err := tx.Exec(`DELETE FROM mentions WHERE message_id IN (SELECT id FROM messages WHERE created_at < ?`+pinnedMessages+`)`, cutoff); err != nil {
				return a.fail(err)
			}
			if _, err := tx.Exec(`DELETE FROM attachments WHERE message_id IN (SELECT id FROM messages WHERE created_at < ?`+pinnedMessages+`)`, cutoff); err != nil {
				return a.fail(err)
			}
			if _, err := tx.Exec(`DELETE FROM messages WHERE created_at < ?`+pinnedMessages, cutoff); err != nil {
				return a.fail(err)
			}
		}
	}
	type orphan struct {
		id   string
		full string
		size int64
	}
	filesRoot := filepath.Join(absData, "files")
	rows, err := tx.Query(`SELECT f.id, f.storage_path, f.size FROM files f LEFT JOIN attachments a ON a.file_id = f.id WHERE a.file_id IS NULL` + pinnedFiles)
	if err != nil {
		return a.fail(err)
	}
	var orphans []orphan
	var fileBytes int64
	for rows.Next() {
		var id, path string
		var size int64
		if err := rows.Scan(&id, &path, &size); err != nil {
			rows.Close()
			return a.fail(err)
		}
		full, ok := safeUnderRoot(filesRoot, path)
		if !ok {
			// Keep the row: its storage path escapes the files root, so it is
			// not safe to delete automatically.
			continue
		}
		orphans = append(orphans, orphan{id: id, full: full, size: size})
		fileBytes += size
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

	var removed int64
	var removedBytes int64
	if *apply {
		for _, o := range orphans {
			if err := os.Remove(o.full); err == nil {
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
