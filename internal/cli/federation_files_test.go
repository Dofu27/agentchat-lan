package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/blobguard"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func cliFileStore(t *testing.T) (*store.Store, string, store.FederationFileEnvelope) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), hubDBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sum := sha256.Sum256([]byte("synthetic-only-secret-1234567890123456"))
	_, err = st.ConfigureFederation(context.Background(), []store.FederationPeer{{NodeID: "remote", BaseURL: "http://127.0.0.1:1", SecretHash: hex.EncodeToString(sum[:])}}, []store.FederationLink{{PeerID: "remote", LocalRoom: "general", RemoteRoom: "general"}}, store.FederationLimits{MaxOutbox: 100, MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.ConfigureFederationFiles(context.Background(), []store.FederationFileLink{{PeerID: "remote", LocalRoom: "general", RemoteRoom: "general", LinkID: "link", Epoch: 1}}); err != nil {
		t.Fatal(err)
	}
	body := []byte("verified attachment bytes")
	hash := sha256.Sum256(body)
	sha := hex.EncodeToString(hash[:])
	root := filepath.Join(dir, "files")
	if err = os.MkdirAll(filepath.Join(root, sha[:2]), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, sha[:2], sha), body, 0600); err != nil {
		t.Fatal(err)
	}
	e := store.FederationFileEnvelope{Version: 2, OriginNodeID: "remote", OriginMessageID: protocol.NewID(), SourceRoom: "general", DestinationRoom: "general", LinkID: "link", Epoch: 1, Sender: protocol.AgentRef{ID: "remote-agent", Name: "remote-agent", Kind: "test"}, SentAt: time.Now().UTC(), Files: []store.FederationBlob{{SHA256: sha, Size: int64(len(body)), Name: "received.bin"}}}
	if _, err = st.StageFederationFiles(context.Background(), "remote", e); err != nil {
		t.Fatal(err)
	}
	j, err := st.ClaimFederationFileJob(context.Background(), true, time.Minute)
	if err != nil || j == nil {
		t.Fatal(j, err)
	}
	if err = st.FederationFileReady(context.Background(), *j, e.Files[0], filepath.ToSlash(filepath.Join(sha[:2], sha)), 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	return st, dir, e
}
func TestFederationFilesBackupRestoreAndGCPins(t *testing.T) {
	st, dir, e := cliFileStore(t)
	var out, errs bytes.Buffer
	app := &App{Stdout: &out, Stderr: &errs}
	// An installed but not yet published blob has no ordinary attachments. GC
	// must still preserve both its file row and physical bytes.
	if code := app.Run([]string{"hub", "gc", "--data", dir, "--apply", "--json"}); code != 0 {
		t.Fatal(errs.String())
	}
	var gc gcResult
	if json.Unmarshal(out.Bytes(), &gc) != nil || gc.Files != 0 {
		t.Fatal(out.String())
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	out.Reset()
	errs.Reset()
	if code := app.Run([]string{"hub", "backup", "--data", dir, "--out", archive}); code != 0 {
		t.Fatal(errs.String())
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if code := app.Run([]string{"hub", "restore", "--data", restored, "--in", archive}); code != 0 {
		t.Fatal(errs.String())
	}
	if err := validateFileRefs(hubDBPath(restored), filepath.Join(restored, "files")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", hubDBPath(restored))
	if err != nil {
		t.Fatal(err)
	}
	var pins int
	if err = db.QueryRow(`SELECT COUNT(*) FROM federation_transfer_blobs`).Scan(&pins); err != nil || pins != 1 {
		t.Fatal("lost pin", pins, err)
	}
	db.Close()
	release, err := blobguard.Acquire(filepath.Join(dir, "files"))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"hub", "backup", "--data", dir, "--out", filepath.Join(t.TempDir(), "locked.tar.gz")}, {"hub", "gc", "--data", dir, "--apply"}, {"hub", "restore", "--data", dir, "--in", archive, "--force"}} {
		if app.Run(args) == 0 {
			t.Fatal("maintenance ignored attachment runtime lock", args)
		}
	}
	release()
	// Hash, not filename/length, protects backup and restore of pinned blobs.
	blob := filepath.Join(dir, "files", e.Files[0].SHA256[:2], e.Files[0].SHA256)
	if err = os.WriteFile(blob, bytes.Repeat([]byte("x"), int(e.Files[0].Size)), 0600); err != nil {
		t.Fatal(err)
	}
	if err = validateFileRefs(hubDBPath(dir), filepath.Join(dir, "files")); err == nil {
		t.Fatal("corrupt federation blob accepted")
	}
	st.Close()
}
func TestFederationFilesGCReleasesExpiredStaging(t *testing.T) {
	st, dir, _ := cliFileStore(t)
	st.Close()
	db, err := sql.Open("sqlite", hubDBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE federation_file_transfers SET deadline=0`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	var out, errs bytes.Buffer
	app := &App{Stdout: &out, Stderr: &errs}
	if app.Run([]string{"hub", "gc", "--data", dir, "--apply", "--json"}) != 0 {
		t.Fatal(errs.String())
	}
	var gc gcResult
	json.Unmarshal(out.Bytes(), &gc)
	if gc.Files != 1 {
		t.Fatal("expired unreferenced pin retained", out.String())
	}
	db, err = sql.Open("sqlite", hubDBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM federation_received_blobs`).Scan(&n); err != nil || n != 0 {
		t.Fatal("provenance budget not released", n, err)
	}
}
func TestFederationFilesConfigExplicitOptIn(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), hubDBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	os.WriteFile(filepath.Join(dir, "peer.secret"), []byte("independent-config-test-secret-123456789"), 0600)
	path := filepath.Join(dir, "federation.json")
	prefix := `{"peer":{"node_id":"remote","base_url":"http://127.0.0.1:1","secret_file":"peer.secret"},"links":[`
	for _, link := range []string{`{"local_room":"general","remote_room":"general","attachments":true}`, `{"local_room":"general","remote_room":"general","link_id":"link","epoch":1}`} {
		os.WriteFile(path, []byte(prefix+link+` ]}`), 0600)
		if _, err = loadFederation(context.Background(), st, path, filepath.Join(dir, "files")); err == nil {
			t.Fatal("incomplete attachment opt-in accepted")
		}
	}
	os.WriteFile(path, []byte(prefix+`{"local_room":"general","remote_room":"general","attachments":true,"link_id":"link","epoch":1}]}`), 0600)
	if _, err = loadFederation(context.Background(), st, path, filepath.Join(dir, "files")); err != nil {
		t.Fatal(err)
	}
	if protocol.MaxFileSize != 512<<20 {
		t.Fatal("local upload limit changed")
	}
}
