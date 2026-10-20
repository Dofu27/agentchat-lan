package hub_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func startFileNode(t *testing.T, n, peer *federationNode) {
	t.Helper()
	n.opts = hub.FederationOptions{Peer: hub.FederationPeerOptions{NodeID: peer.id, BaseURL: peer.url, Secret: federationTestSecret}, Links: []store.FederationLink{{PeerID: peer.id, LocalRoom: "general", RemoteRoom: "general"}}, FileLinks: []store.FederationFileLink{{PeerID: peer.id, LocalRoom: "general", RemoteRoom: "general", LinkID: "test-link", Epoch: 1}}, FileDir: filepath.Join(filepath.Dir(n.dbPath), "files"), MaxOutbox: 100, MaxAttempts: 10, RetryBase: 50 * time.Millisecond, PollInterval: 30 * time.Millisecond, RequestTimeout: time.Second, LeaseDuration: 3 * time.Second}
	n.startWithOptions(t)
}
func localBlob(t *testing.T, n *federationNode, data []byte) protocol.FileRef {
	t.Helper()
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	dir := filepath.Join(filepath.Dir(n.dbPath), "files", sha[:2])
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sha), data, 0600); err != nil {
		t.Fatal(err)
	}
	ref := protocol.FileRef{ID: "fil_" + protocol.NewID(), Name: "test.bin", SHA256: sha, Size: int64(len(data)), Mime: "application/octet-stream"}
	saved, _, err := n.st.RegisterFile(context.Background(), ref, filepath.ToSlash(filepath.Join(sha[:2], sha)))
	if err != nil {
		t.Fatal(err)
	}
	return saved.Ref
}
func fileEnvelope(t *testing.T, n *federationNode, data []byte) store.FederationFileEnvelope {
	t.Helper()
	ref := localBlob(t, n, data)
	c := nodeReader(t, n, "writer"+protocol.NewID())
	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sent, err := n.st.SendMessage(context.Background(), me.Agent.ID, "general", protocol.SendMessageRequest{Text: "caption", Files: []string{ref.ID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store.FederationFileEnvelope{Version: 2, OriginNodeID: n.id, OriginMessageID: sent.Message.ID, SourceRoom: "general", DestinationRoom: "general", LinkID: "test-link", Epoch: 1, Sender: sent.Message.From, Text: "caption", SentAt: sent.Message.TS, Files: []store.FederationBlob{{SHA256: ref.SHA256, Size: ref.Size, Name: ref.Name, Mime: ref.Mime}}}
}
func fileRequest(t *testing.T, n *federationNode, e store.FederationFileEnvelope) (int, store.FederationFileReceipt) {
	t.Helper()
	raw, _ := json.Marshal(e)
	req, _ := http.NewRequest("POST", n.url+"/federation/v2/messages", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+federationTestSecret)
	req.Header.Set("X-AgentChat-Node", e.OriginNodeID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r store.FederationFileReceipt
	_ = json.NewDecoder(resp.Body).Decode(&r)
	return resp.StatusCode, r
}
func TestFederationFilesTwoNodesAtomicReplayAndNoLoop(t *testing.T) {
	a, b := newFederationNode(t), newFederationNode(t)
	startFileNode(t, a, b)
	startFileNode(t, b, a)
	reader := nodeReader(t, b, "reader")
	data := bytes.Repeat([]byte("blob data "), 600000)
	e := fileEnvelope(t, a, data)
	code, r := fileRequest(t, b, e)
	if code != 202 || r.TransferID == "" || r.LocalMessageID != "" {
		t.Fatalf("stage: %d %+v", code, r)
	}
	history, _, err := b.st.History(context.Background(), senderBIdentity(t, reader), "general", 0, 0, 20)
	if err != nil || len(history) != 0 {
		t.Fatalf("premature visibility: %+v %v", history, err)
	}
	if code, r2 := fileRequest(t, b, e); code != 202 || r2.TransferID != r.TransferID {
		t.Fatal("staging duplicate changed identity")
	}
	changed := e
	changed.Text = "changed"
	if code, _ := fileRequest(t, b, changed); code != 409 {
		t.Fatalf("conflict=%d", code)
	}
	b.run()
	a.run()
	waitFederation(t, func() bool {
		receipt, err := b.st.StageFederationFiles(context.Background(), a.id, e)
		return err == nil && receipt.State == "committed" && receipt.LocalMessageID != ""
	})
	history, _, err = b.st.History(context.Background(), senderBIdentity(t, reader), "general", 0, 0, 20)
	if err != nil || len(history) != 1 || len(history[0].Files) != 1 {
		t.Fatalf("final message: %+v %v", history, err)
	}
	got, err := os.ReadFile(filepath.Join(b.opts.FileDir, e.Files[0].SHA256[:2], e.Files[0].SHA256))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("blob differs", err)
	}
	if history[0].Meta.Federation == nil || history[0].From.Kind != "federation" {
		t.Fatal("remote sender spoofed local identity")
	}
	waitFederation(t, func() bool {
		status, err := a.st.FederationOperatorStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return status.Attachments.Delivered == 1
	})
	if j, err := b.st.ClaimFederationFileJob(context.Background(), false, time.Second); err != nil || j != nil {
		t.Fatal("remote attachment reforwarded", j, err)
	}
	if stats, _ := a.st.FederationStats(context.Background()); stats.Pending+stats.Delivered != 0 {
		t.Fatal("attachments leaked into v1")
	}
}

func TestFederationFilesGrantAndStrictProtocol(t *testing.T) {
	a, b := newFederationNode(t), newFederationNode(t)
	startFileNode(t, a, b)
	startFileNode(t, b, a)
	e := fileEnvelope(t, a, []byte("private blob"))
	q := url.Values{"origin_node_id": {e.OriginNodeID}, "origin_message_id": {e.OriginMessageID}, "link_id": {e.LinkID}, "epoch": {"1"}}
	pull := func(secret, origin, rangeValue string, query url.Values) int {
		req, _ := http.NewRequest("GET", a.url+"/federation/v2/blobs/"+e.Files[0].SHA256+"?"+query.Encode(), nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("X-AgentChat-Node", origin)
		req.Header.Set("Range", rangeValue)
		req.Header.Set("If-Range", "\""+e.Files[0].SHA256+"\"")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := pull("agent-token", b.id, "bytes=0-3", q); code != 401 {
		t.Fatal(code)
	}
	if code := pull(federationTestSecret, b.id, "bytes=0-3", q); code != 206 {
		t.Fatal(code)
	}
	q.Set("origin_message_id", "guessed")
	if code := pull(federationTestSecret, b.id, "bytes=0-3", q); code != 404 {
		t.Fatal(code)
	}
	q.Set("origin_message_id", e.OriginMessageID)
	q.Set("epoch", "2")
	if code := pull(federationTestSecret, b.id, "bytes=0-3", q); code != 404 {
		t.Fatal(code)
	}
	q.Set("epoch", "1")
	if code := pull(federationTestSecret, b.id, "bytes=0-1,3-4", q); code != 416 {
		t.Fatal(code)
	}
	changed := e
	changed.Epoch = 2
	if code, _ := fileRequest(t, b, changed); code != 403 {
		t.Fatal(code)
	}
	changed = e
	changed.Files = append(append([]store.FederationBlob{}, e.Files...), e.Files[0])
	if code, _ := fileRequest(t, b, changed); code != 400 {
		t.Fatal("duplicate hashes", code)
	}
	raw, _ := json.Marshal(e)
	raw = bytes.Replace(raw, []byte(`"version":2`), []byte(`"unknown":true,"version":2`), 1)
	req, _ := http.NewRequest("POST", b.url+"/federation/v2/messages", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+federationTestSecret)
	req.Header.Set("X-AgentChat-Node", a.id)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("unknown field accepted")
	}
}

func TestFederationFilesResumeRestartAndHashFailure(t *testing.T) {
	a, b := newFederationNode(t), newFederationNode(t)
	startFileNode(t, a, b)
	startFileNode(t, b, a)
	data := bytes.Repeat([]byte("resume!"), 800000)
	e := fileEnvelope(t, a, data)
	reader := nodeReader(t, b, "reader")
	original := *a.handler.Load()
	var failed atomic.Bool
	var sawResume atomic.Bool
	var allow atomic.Bool
	var corruptSHA atomic.Value
	corruptSHA.Store("")
	a.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sha := corruptSHA.Load().(string); sha != "" && strings.Contains(r.URL.Path, "/blobs/"+sha) {
			rec := httptest.NewRecorder()
			original.ServeHTTP(rec, r)
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			w.Write(bytes.Repeat([]byte("x"), rec.Body.Len()))
			return
		}
		if strings.Contains(r.URL.Path, "/blobs/") {
			if strings.HasPrefix(r.Header.Get("Range"), "bytes=4194304-") {
				sawResume.Store(true)
				if !allow.Load() {
					failed.Store(true)
					http.Error(w, "offline", 503)
					return
				}
			}
		}
		original.ServeHTTP(w, r)
	}))
	if code, _ := fileRequest(t, b, e); code != 202 {
		t.Fatal(code)
	}
	b.run()
	waitFederation(t, failed.Load)
	b.stopWorker(t)
	history, _, _ := b.st.History(context.Background(), senderBIdentity(t, reader), "general", 0, 0, 20)
	if len(history) != 0 {
		t.Fatal("partial message published")
	}
	// Reopen SQLite and construct the runtime again; no in-memory checkpoint.
	b.st.Close()
	var err error
	b.st, err = store.Open(context.Background(), b.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	b.fed, err = hub.NewFederation(context.Background(), b.st, b.opts)
	if err != nil {
		t.Fatal(err)
	}
	b.setHandler(hub.New(b.st, hub.Options{Federation: b.fed, FileDir: b.opts.FileDir}).Handler())
	allow.Store(true)
	b.run()
	waitFederation(t, func() bool {
		r, err := b.st.StageFederationFiles(context.Background(), a.id, e)
		return err == nil && r.State == "committed"
	})
	if !sawResume.Load() {
		t.Fatal("range resume absent")
	}
	// New file is corrupted on wire; a cached unrelated/local copy is not proof
	// that this peer owns the bytes. No message may appear on hash mismatch.
	e2 := fileEnvelope(t, a, []byte("hash must match"))
	corruptSHA.Store(e2.Files[0].SHA256)
	if code, _ := fileRequest(t, b, e2); code != 202 {
		t.Fatal(code)
	}
	time.Sleep(200 * time.Millisecond)
	history, _, _ = b.st.History(context.Background(), senderBIdentity(t, reader), "general", 0, 0, 20)
	if len(history) != 1 {
		t.Fatal("corrupt file published")
	}
}

func TestFederationFilesDisabledDefault(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := hub.New(st, hub.Options{FileDir: t.TempDir()}).Handler()
	for _, path := range []string{"/federation/v2/capabilities", "/federation/v2/messages", "/federation/v2/blobs/abc"} {
		for _, method := range []string{"GET", "POST"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			if rec.Code != 404 {
				t.Fatalf("default route %s %s = %d", method, path, rec.Code)
			}
		}
	}
}

func TestFederationFilesBidirectionalManifestOnlyAndMissingNoDowngrade(t *testing.T) {
	a, b := newFederationNode(t), newFederationNode(t)
	startFileNode(t, a, b)
	startFileNode(t, b, a)
	readerA := nodeReader(t, a, "reader-a")
	readerB := nodeReader(t, b, "reader-b")
	file1 := localBlob(t, b, []byte("one"))
	file2 := localBlob(t, b, []byte("two"))
	me, _ := readerB.Me(context.Background())
	if _, err := b.st.SendMessage(context.Background(), me.Agent.ID, "general", protocol.SendMessageRequest{Files: []string{file1.ID, file2.ID}}, nil); err != nil {
		t.Fatal(err)
	}
	e := fileEnvelope(t, a, []byte("must not become caption-only"))
	// Matching bytes in an unrelated local upload are not peer possession proof.
	localBlob(t, b, []byte("must not become caption-only"))
	if err := os.Remove(filepath.Join(a.opts.FileDir, e.Files[0].SHA256[:2], e.Files[0].SHA256)); err != nil {
		t.Fatal(err)
	}
	original := *a.handler.Load()
	var missing atomic.Bool
	a.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/blobs/") {
			missing.Store(true)
		}
		original.ServeHTTP(w, r)
	}))
	a.run()
	b.run()
	waitFederation(t, func() bool {
		messages, _, err := a.st.History(context.Background(), senderBIdentity(t, readerA), "general", 0, 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			if m.Meta.Federation != nil {
				return m.Text == "" && len(m.Files) == 2
			}
		}
		return false
	})
	waitFederation(t, missing.Load)
	messages, _, err := b.st.History(context.Background(), me.Agent.ID, "general", 0, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if m.Meta.Federation != nil {
			t.Fatal("missing attachment degraded to visible text")
		}
	}
}

func TestFederationFilesV1OnlyPeerDoesNotRegisterV2AttachmentRoutes(t *testing.T) {
	a, b := newFederationNode(t), newFederationNode(t)
	a.start(t, b)
	b.start(t, a)
	for _, path := range []string{"/federation/v2/messages", "/federation/v2/blobs/abc"} {
		resp, err := http.Post(a.url+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("v2 disabled route=%d", resp.StatusCode)
		}
	}
}
