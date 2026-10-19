package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func openTestHub(t *testing.T, dbPath string, opts hub.Options) (*store.Store, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if opts.Logger == nil {
		opts.Logger = log.New(io.Discard, "", 0)
	}
	if opts.FileDir == "" {
		opts.FileDir = filepath.Join(filepath.Dir(dbPath), "files")
	}
	hs := hub.New(st, opts)
	ts := httptest.NewServer(hs.Handler())
	t.Cleanup(ts.Close)
	return st, ts
}

func adminRequest(t *testing.T, base, token, method, path string, body any, out any) int {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err = json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return resp.StatusCode
}

func createInvite(t *testing.T, base, admin string, uses int) string {
	t.Helper()
	var out struct {
		Code string `json:"code"`
	}
	status := adminRequest(t, base, admin, http.MethodPost, "/v1/admin/invites", map[string]any{"room": "general", "ttl_seconds": 3600, "uses": uses}, &out)
	if status != http.StatusCreated || out.Code == "" {
		t.Fatalf("create invite status=%d code=%q", status, out.Code)
	}
	return out.Code
}

func joinAgent(t *testing.T, base, code, name string) *client.Client {
	t.Helper()
	c := client.New(base, "")
	resp, err := c.Join(context.Background(), protocol.JoinRequest{InviteCode: code, Name: name, Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return client.New(base, resp.Token)
}

func uploadFile(t *testing.T, base, token, name string, content []byte) (int, protocol.FileRef) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err = mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/v1/files", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ref protocol.FileRef
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		if err = json.NewDecoder(resp.Body).Decode(&ref); err != nil {
			t.Fatal(err)
		}
	} else {
		errorBody, _ := io.ReadAll(resp.Body)
		t.Logf("upload returned %d: %s", resp.StatusCode, errorBody)
	}
	return resp.StatusCode, ref
}

func TestEmbeddedUIAndCSP(t *testing.T) {
	db := filepath.Join(t.TempDir(), "ui.sqlite")
	if _, err := hub.Init(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	_, ts := openTestHub(t, db, hub.Options{})

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status=%d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "AgentChat LAN") {
		t.Fatalf("UI body does not look like the app: %q", body[:min(len(body), 120)])
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("missing CSP: %q", csp)
	}

	static, err := http.Get(ts.URL + "/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	static.Body.Close()
	if static.StatusCode != http.StatusOK {
		t.Fatalf("GET /static/app.js status=%d", static.StatusCode)
	}
	if got := static.Header.Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Fatalf("app.js content-type=%q", got)
	}

	missing, err := http.Get(ts.URL + "/not-a-page")
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing page status=%d", missing.StatusCode)
	}
}

func TestFileUploadAttachmentDedupRangeAndAuthorization(t *testing.T) {
	db := filepath.Join(t.TempDir(), "files.sqlite")
	admin, err := hub.Init(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	_, ts := openTestHub(t, db, hub.Options{})
	code := createInvite(t, ts.URL, admin, 2)
	a := joinAgent(t, ts.URL, code, "file-sender")
	b := joinAgent(t, ts.URL, code, "file-recipient")
	data := []byte("0123456789 file payload\n")
	status, first := uploadFile(t, ts.URL, a.Token, `..\\..\\outside.txt`, data)
	if status != http.StatusCreated || first.ID == "" || first.Name != "outside.txt" {
		t.Fatalf("upload status=%d ref=%#v", status, first)
	}
	status, duplicate := uploadFile(t, ts.URL, a.Token, "renamed.txt", data)
	if status != http.StatusCreated || duplicate.ID != first.ID || duplicate.SHA256 != first.SHA256 {
		t.Fatalf("duplicate upload status=%d first=%#v duplicate=%#v", status, first, duplicate)
	}
	message, err := a.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "attached", Files: []string{first.ID}})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := b.Inbox(context.Background(), 0, 20)
	if err != nil || len(inbox.Messages) != 1 || len(inbox.Messages[0].Files) != 1 {
		t.Fatalf("attachment inbox=%#v err=%v", inbox, err)
	}
	if inbox.Messages[0].ID != message.ID || inbox.Messages[0].Files[0].Name != "outside.txt" || inbox.Messages[0].Files[0].SHA256 != first.SHA256 {
		t.Fatalf("attachment metadata=%#v", inbox.Messages[0].Files)
	}

	get := func(method, rangeHeader string, authenticated bool) *http.Response {
		t.Helper()
		req, reqErr := http.NewRequest(method, ts.URL+"/v1/files/"+first.ID, nil)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+b.Token)
		}
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		resp, reqErr := http.DefaultClient.Do(req)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		return resp
	}
	full := get(http.MethodGet, "", true)
	fullBody, err := io.ReadAll(full.Body)
	full.Body.Close()
	if err != nil || full.StatusCode != http.StatusOK || !bytes.Equal(fullBody, data) || full.Header.Get("X-Content-SHA256") != first.SHA256 || full.Header.Get("Content-Disposition") == "" {
		t.Fatalf("full download status=%d body=%q hash=%q disposition=%q err=%v", full.StatusCode, fullBody, full.Header.Get("X-Content-SHA256"), full.Header.Get("Content-Disposition"), err)
	}
	ranged := get(http.MethodGet, "bytes=2-7", true)
	rangeBody, err := io.ReadAll(ranged.Body)
	ranged.Body.Close()
	if err != nil || ranged.StatusCode != http.StatusPartialContent || string(rangeBody) != string(data[2:8]) || ranged.Header.Get("Content-Range") != "bytes 2-7/24" {
		t.Fatalf("range status=%d body=%q content-range=%q err=%v", ranged.StatusCode, rangeBody, ranged.Header.Get("Content-Range"), err)
	}
	head := get(http.MethodHead, "", true)
	headBody, err := io.ReadAll(head.Body)
	head.Body.Close()
	if err != nil || head.StatusCode != http.StatusOK || len(headBody) != 0 || head.Header.Get("Content-Length") != "24" {
		t.Fatalf("HEAD status=%d body=%q length=%q err=%v", head.StatusCode, headBody, head.Header.Get("Content-Length"), err)
	}
	unauth := get(http.MethodGet, "", false)
	unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized download status=%d", unauth.StatusCode)
	}

	roomStatus := adminRequest(t, ts.URL, admin, http.MethodPost, "/v1/admin/rooms", map[string]any{"id": "isolated", "name": "Isolated"}, nil)
	if roomStatus != http.StatusCreated {
		t.Fatalf("create isolated room status=%d", roomStatus)
	}
	var invite struct {
		Code string `json:"code"`
	}
	if status = adminRequest(t, ts.URL, admin, http.MethodPost, "/v1/admin/invites", map[string]any{"room": "isolated", "ttl_seconds": 3600, "uses": 1}, &invite); status != http.StatusCreated {
		t.Fatalf("create isolated invite status=%d", status)
	}
	outsider := joinAgent(t, ts.URL, invite.Code, "unrelated-agent")
	blockedReq, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/files/"+first.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	blockedReq.Header.Set("Authorization", "Bearer "+outsider.Token)
	blocked, err := http.DefaultClient.Do(blockedReq)
	if err != nil {
		t.Fatal(err)
	}
	blocked.Body.Close()
	if blocked.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-room download status=%d", blocked.StatusCode)
	}
}

func TestFileUploadLimitAndMissingFileField(t *testing.T) {
	db := filepath.Join(t.TempDir(), "file-limit.sqlite")
	admin, err := hub.Init(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	_, ts := openTestHub(t, db, hub.Options{MaxFileSize: 64})
	agent := joinAgent(t, ts.URL, createInvite(t, ts.URL, admin, 1), "limited-uploader")
	status, _ := uploadFile(t, ts.URL, agent.Token, "too-large.bin", bytes.Repeat([]byte("x"), 65))
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload status=%d", status)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("other", "not a file")
	_ = mw.Close()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/files", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+agent.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing file field status=%d", resp.StatusCode)
	}
}

func TestDurableTwoAgentExchangeAndOfflineBacklog(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "hub.sqlite")
	admin, err := hub.Init(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	st, ts := openTestHub(t, db, hub.Options{})
	code := createInvite(t, ts.URL, admin, 2)
	a := joinAgent(t, ts.URL, code, "codex-a")
	b := joinAgent(t, ts.URL, code, "agent-b")
	first, err := a.SendMessage(ctx, "general", protocol.SendMessageRequest{ClientMsgID: "a-1", Text: "hello @agent-b"})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := b.Inbox(ctx, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox.Messages) != 1 || inbox.Messages[0].ID != first.ID || len(inbox.Messages[0].Mentions) != 1 || inbox.Messages[0].Mentions[0] != "agent-b" {
		t.Fatalf("first inbox = %#v", inbox.Messages)
	}
	if err = b.Ack(ctx, map[string]int64{"general": inbox.Messages[0].Seq}); err != nil {
		t.Fatal(err)
	}
	if err = a.Ack(ctx, map[string]int64{"general": first.Seq}); err != nil {
		t.Fatal(err)
	}
	if again, err := b.Inbox(ctx, 0, 50); err != nil || len(again.Messages) != 0 {
		t.Fatalf("acked inbox = %#v, err=%v", again, err)
	}
	second, err := b.SendMessage(ctx, "general", protocol.SendMessageRequest{ClientMsgID: "b-1", Text: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err = a.Inbox(ctx, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox.Messages) != 1 || inbox.Messages[0].ID != second.ID {
		t.Fatalf("agent A inbox = %#v", inbox.Messages)
	}
	if err = a.Ack(ctx, map[string]int64{"general": second.Seq}); err != nil {
		t.Fatal(err)
	}
	if err = b.Ack(ctx, map[string]int64{"general": second.Seq}); err != nil {
		t.Fatal(err)
	}
	// A duplicate id returns the existing row without consuming another room seq.
	replayed, err := a.SendMessage(ctx, "general", protocol.SendMessageRequest{ClientMsgID: "a-1", Text: "hello @agent-b"})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != first.ID {
		t.Fatalf("replay id=%s want %s", replayed.ID, first.ID)
	}
	history, err := a.History(ctx, "general", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 2 {
		t.Fatalf("history count=%d, want 2", len(history.Messages))
	}

	// Persist an unacknowledged message, restart the HTTP hub and SQLite handle,
	// and prove the offline recipient receives it once at the application layer.
	backlog, err := a.SendMessage(ctx, "general", protocol.SendMessageRequest{ClientMsgID: "a-2", Text: "while agent-b is offline"})
	if err != nil {
		t.Fatal(err)
	}
	ts.Close()
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	_, ts2 := openTestHub(t, db, hub.Options{})
	b2 := client.New(ts2.URL, b.Token)
	inbox, err = b2.Inbox(ctx, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox.Messages) != 1 || inbox.Messages[0].ID != backlog.ID {
		t.Fatalf("post-restart backlog=%#v", inbox.Messages)
	}
	if err = b2.Ack(ctx, map[string]int64{"general": backlog.Seq}); err != nil {
		t.Fatal(err)
	}
	if inbox, err = b2.Inbox(ctx, 0, 50); err != nil || len(inbox.Messages) != 0 {
		t.Fatalf("post-ack inbox=%#v err=%v", inbox, err)
	}
}

func TestAuthRoomBoundaryIdempotencyAndAudit(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.sqlite")
	admin, err := hub.Init(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	_, ts := openTestHub(t, db, hub.Options{})
	code := createInvite(t, ts.URL, admin, 1)
	a := joinAgent(t, ts.URL, code, "agent-a")
	if status := adminRequest(t, ts.URL, "bad", http.MethodGet, "/v1/admin/invites", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("bad admin status=%d", status)
	}
	status := adminRequest(t, ts.URL, admin, http.MethodPost, "/v1/admin/rooms", map[string]any{"id": "private", "name": "Private"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create private status=%d", status)
	}
	_, err = a.SendMessage(context.Background(), "private", protocol.SendMessageRequest{Text: "no access"})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("cross-room send err=%v", err)
	}
	_, err = a.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "hello @not-a-member"})
	if err == nil || !strings.Contains(err.Error(), "validation_failed") {
		t.Fatalf("unknown mention err=%v", err)
	}
	var events struct {
		Events []map[string]any `json:"events"`
	}
	status = adminRequest(t, ts.URL, admin, http.MethodGet, "/v1/admin/audit?limit=20", nil, &events)
	if status != http.StatusOK || len(events.Events) < 3 {
		t.Fatalf("audit status=%d events=%#v", status, events.Events)
	}
}

func TestLongPollWakesAndRateLimitReturnsRetryAfter(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.sqlite")
	admin, err := hub.Init(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	_, ts := openTestHub(t, db, hub.Options{RequestsPerMinute: 1, Burst: 8})
	code := createInvite(t, ts.URL, admin, 2)
	a := joinAgent(t, ts.URL, code, "agent-a")
	b := joinAgent(t, ts.URL, code, "agent-b")
	got := make(chan *protocol.InboxResponse, 1)
	errs := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		v, e := b.Inbox(ctx, 3, 20)
		if e != nil {
			errs <- e
			return
		}
		got <- v
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err = a.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "wake"}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-errs:
		t.Fatal(e)
	case v := <-got:
		if len(v.Messages) != 1 || v.Messages[0].Text != "wake" {
			t.Fatalf("long poll = %#v", v.Messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long poll did not wake")
	}

	// Separate server with a single-token burst to assert 429 and Retry-After.
	db2 := filepath.Join(t.TempDir(), "limited.sqlite")
	admin2, err := hub.Init(context.Background(), db2)
	if err != nil {
		t.Fatal(err)
	}
	_, limited := openTestHub(t, db2, hub.Options{RequestsPerMinute: 1, Burst: 1})
	code2 := createInvite(t, limited.URL, admin2, 1)
	c := joinAgent(t, limited.URL, code2, "limited-agent")
	_, err = c.Rooms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.NewRequest(http.MethodGet, limited.URL+"/v1/rooms", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Header.Set("Authorization", "Bearer "+c.Token)
	reply, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatal(err)
	}
	defer reply.Body.Close()
	if reply.StatusCode != http.StatusTooManyRequests || reply.Header.Get("Retry-After") == "" {
		t.Fatalf("rate limit status=%d retry=%q", reply.StatusCode, reply.Header.Get("Retry-After"))
	}
}

func TestServeGracefulShutdown(t *testing.T) {
	db := filepath.Join(t.TempDir(), "serve.sqlite")
	st, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hs := hub.New(st, hub.Options{Logger: log.New(io.Discard, "", 0)})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- hs.Serve(ctx, ln) }()
	clientHTTP := &http.Client{Timeout: time.Second}
	for i := 0; i < 20; i++ {
		resp, e := clientHTTP.Get("http://" + ln.Addr().String() + "/healthz")
		if e == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
}
