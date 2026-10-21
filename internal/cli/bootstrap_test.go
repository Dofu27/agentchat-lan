package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

type failingBootstrapFile struct {
	*os.File
	stage string
}

func (f *failingBootstrapFile) Write(p []byte) (int, error) {
	if f.stage == "write" {
		return 0, errors.New("injected write")
	}
	if f.stage == "short" {
		return len(p) - 1, nil
	}
	return f.File.Write(p)
}
func (f *failingBootstrapFile) Sync() error {
	if f.stage == "sync" {
		return errors.New("injected sync")
	}
	return f.File.Sync()
}
func (f *failingBootstrapFile) Close() error {
	err := f.File.Close()
	if f.stage == "close" {
		return errors.New("injected close")
	}
	return err
}

func TestBootstrapIOFailureNeverCommitsDatabase(t *testing.T) {
	for _, stage := range []string{"write", "short", "sync", "close"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			path, db := filepath.Join(dir, "token"), filepath.Join(dir, "data", "agentchat.db")
			called := false
			ops := bootstrapOps{create: func(p string) (bootstrapFile, error) {
				f, e := createBootstrapFile(p)
				if e != nil {
					return nil, e
				}
				return &failingBootstrapFile{f, stage}, nil
			}, initialize: func(context.Context, string, string) error { called = true; return nil }}
			err := bootstrapHub(context.Background(), db, path, false, ops)
			if err == nil || called {
				t.Fatalf("failure committed DB: %v called=%v", err, called)
			}
			if _, e := os.Stat(path); e != nil {
				t.Fatal("credential must be retained, not blindly removed")
			}
			if _, e := os.Stat(db); !os.IsNotExist(e) {
				t.Fatal("DB exists after credential failure")
			}
		})
	}
}

func TestBootstrapRecoverableDeliveryAndIdempotentResume(t *testing.T) {
	dir := t.TempDir()
	path, db := filepath.Join(dir, "token"), filepath.Join(dir, "data", "agentchat.db")
	ops := defaultBootstrapOps()
	ops.initialize = func(context.Context, string, string) error { return errors.New("injected initialization failure") }
	if err := bootstrapHub(context.Background(), db, path, false, ops); err == nil || !strings.Contains(err.Error(), "--resume-init") {
		t.Fatal("missing recovery instruction")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapHub(context.Background(), db, path, true, defaultBootstrapOps()); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapHub(context.Background(), db, path, true, defaultBootstrapOps()); err != nil {
		t.Fatal("same-credential retry not idempotent", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("resume replaced credential")
	}
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.IsAdminToken(context.Background(), strings.TrimSpace(string(before))) {
		t.Fatal("delivered credential does not authenticate")
	}
	otherPath := filepath.Join(dir, "other-token")
	if err := bootstrapHub(context.Background(), db, otherPath, false, defaultBootstrapOps()); err == nil {
		t.Fatal("different credential replaced initializer")
	}
	if !s.IsAdminToken(context.Background(), strings.TrimSpace(string(before))) {
		t.Fatal("existing admin changed")
	}
}

func TestBootstrapExistingFileNeverTouchesDB(t *testing.T) {
	dir := t.TempDir()
	p, db := filepath.Join(dir, "token"), filepath.Join(dir, "data", "agentchat.db")
	if err := os.WriteFile(p, []byte("unowned synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapHub(context.Background(), db, p, false, defaultBootstrapOps()); err == nil {
		t.Fatal("existing token accepted")
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatal("DB was created")
	}
}

func TestBootstrapCommitThenReportedFailureResumes(t *testing.T) {
	dir := t.TempDir()
	p, db := filepath.Join(dir, "token"), filepath.Join(dir, "data", "agentchat.db")
	ops := defaultBootstrapOps()
	ops.initialize = func(ctx context.Context, path, token string) error {
		if e := hub.InitWithToken(ctx, path, token); e != nil {
			return e
		}
		return errors.New("response lost after commit")
	}
	if err := bootstrapHub(context.Background(), db, p, false, ops); err == nil {
		t.Fatal("expected reported failure")
	}
	if err := bootstrapHub(context.Background(), db, p, true, defaultBootstrapOps()); err != nil {
		t.Fatal(err)
	}
}
