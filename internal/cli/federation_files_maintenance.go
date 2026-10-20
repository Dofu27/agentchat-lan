package cli

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/store"
)

// Called with the OS maintenance guard held and the hub stopped. Only known
// expired transfer directories are swept; no arbitrary orphan path is trusted.
func cleanupFederationFiles(tx *sql.Tx, root string) error {
	now := time.Now().UnixMilli()
	rows, err := tx.Query(`SELECT id FROM federation_file_transfers WHERE deadline<=? AND state<>'committed'`, now)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !store.ValidFederationTransferID(id) {
			return errors.New("invalid staging identifier")
		}
		dir, ok := safeUnderRoot(root, filepath.Join(".federation", id))
		if !ok {
			return errors.New("unsafe staging path")
		}
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, v := range entries {
			if v.Type()&os.ModeSymlink != 0 || v.IsDir() {
				return errors.New("unsafe staging entry")
			}
			if err = os.Remove(filepath.Join(dir, v.Name())); err != nil {
				return err
			}
		}
		if err = os.Remove(dir); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE federation_file_transfers SET reserved=0,state='dead',lease_until=0,lease_token='' WHERE deadline<=? AND state<>'committed'`, now); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM federation_blob_grants WHERE job_id IN (SELECT id FROM federation_file_outbox WHERE state='delivered' OR deadline<=?)`, now); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM federation_transfer_blobs WHERE transfer_id IN (SELECT id FROM federation_file_transfers WHERE state='committed' OR deadline<=?)`, now)
	return err
}

func validateFederationSnapshot(db *sql.DB, root string) error {
	var schema int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='federation_received_blobs'`).Scan(&schema); err != nil {
		return err
	}
	if schema == 0 {
		return nil
	}
	check, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	broken := check.Next()
	err = check.Err()
	check.Close()
	if err != nil {
		return err
	}
	if broken {
		return errors.New("backup database has dangling foreign keys")
	}
	rows, err := db.Query(`SELECT f.storage_path,f.sha256,f.size FROM files f WHERE f.id IN (SELECT file_id FROM federation_blob_grants UNION SELECT file_id FROM federation_received_blobs UNION SELECT file_id FROM federation_transfer_blobs)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rel, sha string
		var size int64
		if err = rows.Scan(&rel, &sha, &size); err != nil {
			return err
		}
		full, ok := safeUnderRoot(root, rel)
		if !ok {
			return errors.New("unsafe federation snapshot blob")
		}
		f, err := os.Open(full)
		if err != nil {
			return errors.New("missing federation snapshot blob")
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil || n != size || hex.EncodeToString(h.Sum(nil)) != sha {
			return errors.New("federation snapshot blob integrity failure")
		}
	}
	return rows.Err()
}
