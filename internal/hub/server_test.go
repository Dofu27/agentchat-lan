package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
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
