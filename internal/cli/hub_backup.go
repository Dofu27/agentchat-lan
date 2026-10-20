package cli

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/blobguard"
	_ "modernc.org/sqlite"
)

func (a *App) hubBackup(args []string) int {
	fs := a.newFlagSet("hub backup")
	data := fs.String("data", "./data", "data directory")
	out := fs.String("out", "", "destination .tar.gz file")
	force := fs.Bool("force", false, "overwrite an existing destination")
	jsonOut := fs.Bool("json", false, "print JSON")
	if code, ok := a.parse(fs, args); !ok {
		return code
	}
	if strings.TrimSpace(*out) == "" {
		return a.fail(fmt.Errorf("--out is required"))
	}
	absData, err := filepath.Abs(*data)
	if err != nil {
		return a.fail(err)
	}
	dbPath := filepath.Join(absData, "agentchat.db")
	if _, err := os.Stat(dbPath); err != nil {
		return a.fail(fmt.Errorf("open hub data: %w", err))
	}
	absOut, err := filepath.Abs(*out)
	if err != nil {
		return a.fail(err)
	}
	if _, err := os.Stat(absOut); err == nil && !*force {
		return a.fail(fmt.Errorf("%s already exists; use --force", absOut))
	}
	if err := os.MkdirAll(filepath.Dir(absOut), 0o700); err != nil {
		return a.fail(err)
	}

	tmpDir, err := os.MkdirTemp("", "agentchat-backup-*")
	if err != nil {
		return a.fail(err)
	}
	defer os.RemoveAll(tmpDir)
	snapshot := filepath.Join(tmpDir, "agentchat.db")

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=rw")
	if err != nil {
		return a.fail(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return a.fail(err)
	}
	var fileSchema int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='federation_file_outbox' AND type='table'`).Scan(&fileSchema); err != nil {
		return a.fail(err)
	}
	if fileSchema > 0 {
		release, err := blobguard.Acquire(filepath.Join(absData, "files"))
		if err != nil {
			return a.fail(fmt.Errorf("stop attachment hub before backup: %w", err))
		}
		defer release()
	}
	if _, err := db.Exec(`VACUUM INTO '` + strings.ReplaceAll(filepath.ToSlash(snapshot), "'", "''") + `'`); err != nil {
		return a.fail(fmt.Errorf("snapshot database: %w", err))
	}
	if fileSchema > 0 {
		if err = validateFileRefs(snapshot, filepath.Join(absData, "files")); err != nil {
			return a.fail(err)
		}
	}

	tmpOut, err := os.CreateTemp(filepath.Dir(absOut), ".agentchat-backup-*")
	if err != nil {
		return a.fail(err)
	}
	tmpName := tmpOut.Name()
	defer os.Remove(tmpName)
	gz := gzip.NewWriter(tmpOut)
	tw := tar.NewWriter(gz)
	add := func(name, source string, mode os.FileMode) error {
		info, err := os.Stat(source)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", source)
		}
		hdr := &tar.Header{Name: name, Mode: int64(mode.Perm()), Size: info.Size(), ModTime: info.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(source)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}
	if err := add("agentchat.db", snapshot, 0o600); err != nil {
		return a.fail(err)
	}
	filesRoot := filepath.Join(absData, "files")
	if _, err := os.Stat(filesRoot); err == nil {
		if err := filepath.Walk(filesRoot, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(filesRoot, path)
			if err != nil {
				return err
			}
			return add("files/"+filepath.ToSlash(rel), path, info.Mode())
		}); err != nil {
			return a.fail(err)
		}
	}
	manifest := map[string]any{"format": 1, "created_at": time.Now().UTC().Format(time.RFC3339Nano), "version": a.Version}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(raw)), ModTime: time.Now()}); err != nil {
		return a.fail(err)
	}
	if _, err := tw.Write(raw); err != nil {
		return a.fail(err)
	}
	if err := tw.Close(); err != nil {
		return a.fail(err)
	}
	if err := gz.Close(); err != nil {
		return a.fail(err)
	}
	if err := tmpOut.Close(); err != nil {
		return a.fail(err)
	}
	if err := os.Rename(tmpName, absOut); err != nil {
		return a.fail(err)
	}
	outInfo, _ := os.Stat(absOut)
	if *jsonOut {
		return a.printJSON(map[string]any{"path": absOut, "bytes": outInfo.Size()})
	}
	fmt.Fprintf(a.Stdout, "backup: %s (%d bytes)\n", absOut, outInfo.Size())
	return 0
}

func (a *App) hubRestore(args []string) int {
	fs := a.newFlagSet("hub restore")
	data := fs.String("data", "./data", "data directory")
	in := fs.String("in", "", "backup .tar.gz file")
	force := fs.Bool("force", false, "replace an existing database and files directory")
	jsonOut := fs.Bool("json", false, "print JSON")
	if code, ok := a.parse(fs, args); !ok {
		return code
	}
	if strings.TrimSpace(*in) == "" {
		return a.fail(fmt.Errorf("--in is required"))
	}
	absData, err := filepath.Abs(*data)
	if err != nil {
		return a.fail(err)
	}
	absIn, err := filepath.Abs(*in)
	if err != nil {
		return a.fail(err)
	}
	tmpDir, err := os.MkdirTemp("", "agentchat-restore-*")
	if err != nil {
		return a.fail(err)
	}
	defer os.RemoveAll(tmpDir)
	if err := extractBackup(absIn, tmpDir); err != nil {
		return a.fail(err)
	}
	stagingDB := filepath.Join(tmpDir, "agentchat.db")
	stagingFiles := filepath.Join(tmpDir, "files")
	if err := validateSQLite(stagingDB); err != nil {
		return a.fail(fmt.Errorf("backup database validation failed: %w", err))
	}
	if err := validateFileRefs(stagingDB, stagingFiles); err != nil {
		return a.fail(fmt.Errorf("backup file validation failed: %w", err))
	}
	_ = os.Remove(stagingDB + "-wal")
	_ = os.Remove(stagingDB + "-shm")

	dbPath := filepath.Join(absData, "agentchat.db")
	filesPath := filepath.Join(absData, "files")
	if _, err := os.Stat(dbPath); err == nil && !*force {
		return a.fail(fmt.Errorf("%s already exists; use --force", dbPath))
	}
	if err := os.MkdirAll(absData, 0o700); err != nil {
		return a.fail(err)
	}
	release, err := blobguard.Acquire(filesPath)
	if err != nil {
		return a.fail(fmt.Errorf("stop attachment hub before restore: %w", err))
	}
	defer release()

	oldDir := filepath.Join(absData, ".restore-old-"+time.Now().UTC().Format("20060102-150405"))
	var oldDB, oldFiles string
	if *force {
		if _, err := os.Stat(dbPath); err == nil {
			if err := os.MkdirAll(oldDir, 0o700); err != nil {
				return a.fail(err)
			}
			oldDB = filepath.Join(oldDir, "agentchat.db")
			if err := movePath(dbPath, oldDB); err != nil {
				return a.fail(err)
			}
		}
		if _, err := os.Stat(filesPath); err == nil {
			if err := os.MkdirAll(oldDir, 0o700); err != nil {
				return a.fail(err)
			}
			oldFiles = filepath.Join(oldDir, "files")
			if err := movePath(filesPath, oldFiles); err != nil {
				return a.fail(err)
			}
		}
	}
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	restoreErr := func() error {
		if err := movePath(stagingDB, dbPath); err != nil {
			return err
		}
		if err := copyTree(stagingFiles, filesPath); err != nil {
			return err
		}
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")
		return nil
	}()
	if restoreErr != nil {
		_ = os.Remove(dbPath)
		_ = os.RemoveAll(filesPath)
		if oldDB != "" {
			_ = movePath(oldDB, dbPath)
		}
		if oldFiles != "" {
			_ = movePath(oldFiles, filesPath)
		}
		return a.fail(restoreErr)
	}
	if *jsonOut {
		return a.printJSON(map[string]any{"path": absData})
	}
	fmt.Fprintf(a.Stdout, "restored: %s\n", absData)
	return 0
}

func validateSQLite(path string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("integrity_check returned %q", integrity)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('agents','rooms','memberships','messages','files','attachments')`).Scan(&tables); err != nil {
		return err
	}
	if tables < 6 {
		return fmt.Errorf("backup database is missing required tables")
	}
	return nil
}

func validateFileRefs(dbPath, filesRoot string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.Query(`SELECT storage_path FROM files`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rel string
		if err := rows.Scan(&rel); err != nil {
			return err
		}
		full, ok := safeUnderRoot(filesRoot, rel)
		if !ok {
			return fmt.Errorf("unsafe file reference %q", rel)
		}
		info, err := os.Stat(full)
		if err != nil {
			return fmt.Errorf("missing file %q: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("file reference %q is not a regular file", rel)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return validateFederationSnapshot(db, filesRoot)
}

func movePath(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyTree(src, dst)
	}
	return copyFile(src, dst, info.Mode())
}

func extractBackup(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read backup gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read backup tar: %w", err)
		}
		name := filepath.ToSlash(hdr.Name)
		if name == "agentchat.db" || name == "manifest.json" {
			// ok
		} else if strings.HasPrefix(name, "files/") && !strings.Contains(name, "..") {
			// ok
		} else {
			return fmt.Errorf("unsafe backup entry %q", hdr.Name)
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsupported backup entry %q", hdr.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "agentchat.db")); err != nil {
		return fmt.Errorf("backup has no agentchat.db")
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyTree(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target, info.Mode())
	})
}
