package hub_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/logbroker"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdminLogsMetadataAuthRedactionAndResume(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	db := filepath.Join(dir, "db")
	admin, err := hub.Init(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := logbroker.New(logbroker.Options{Dir: filepath.Join(dir, "logs"), Component: "hub"})
	if err != nil {
		t.Fatal(err)
	}
	st, ts := openTestHub(t, db, hub.Options{Logs: logs, LogDir: filepath.Join(dir, "logs"), RequestsPerMinute: 1000, Burst: 100})
	st.CreateInvite(ctx, "fixture-invite", store.Invite{Room: "general", TTL: time.Hour, Uses: 1}, "test")
	st.Join(ctx, "fixture-invite", protocol.Agent{ID: "person", Name: "person", Kind: "human"}, "agt_fixture")
	for _, token := range []string{"", "agt_fixture", "wrong-peer-secret"} {
		if code := adminRequest(t, ts.URL, token, "GET", "/v1/admin/logs", nil, nil); code != 401 {
			t.Fatal(code)
		}
	}
	secretText := "full-message-body-adm_NEVER_LOG-private-key"
	reqBody := map[string]any{"text": secretText}
	if code := adminRequest(t, ts.URL, "agt_fixture", "POST", "/v1/rooms/general/messages?secret=NEVER_QUERY_LOG", reqBody, nil); code != 201 {
		t.Fatal(code)
	}
	var batch logbroker.Batch
	if code := adminRequest(t, ts.URL, admin, "GET", "/v1/admin/logs?limit=100", nil, &batch); code != 200 {
		t.Fatal(code)
	}
	raw, _ := json.Marshal(batch)
	for _, forbidden := range []string{secretText, admin, "agt_fixture", "NEVER_QUERY_LOG", "fixture-invite"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("leaked %s", forbidden)
		}
	}
	found := false
	for _, e := range batch.Entries {
		if e.Record.Route == "POST /v1/rooms/{room}/messages" {
			found = true
			if e.Record.RequestID == "" || e.Record.Agent != "person" || e.Record.Room != "general" || e.Record.BodyChars != nil {
				t.Fatalf("request fields %+v", e.Record)
			}
		}
	}
	if !found {
		t.Fatal("missing structured request")
	}
	// Ephemeral typing must not become per-keystroke persistent request logs.
	if code := adminRequest(t, ts.URL, "agt_fixture", "POST", "/v1/rooms/general/typing", map[string]bool{"typing": true}, nil); code != 204 {
		t.Fatal(code)
	}
	cursor := batch.Cursor
	transient, err := (logbroker.Reader{Dir: filepath.Join(dir, "logs")}).Read(logbroker.Query{Component: "hub", After: cursor, Limit: 100})
	if err != nil || len(transient.Entries) != 0 {
		t.Fatal("typing was persistently logged", transient, err)
	}
	logs.Write(logbroker.Record{Level: "error", Event: "internal_error"})
	if code := adminRequest(t, ts.URL, admin, "GET", "/v1/admin/logs?after="+cursor+"&level=error", nil, &batch); code != 200 || len(batch.Entries) != 1 {
		t.Fatalf("resume %d %+v", code, batch)
	}
	// Viewer reads must not feed their own request logs into the watched file.
	cursor = batch.Cursor
	if code := adminRequest(t, ts.URL, admin, "GET", "/v1/admin/logs?after="+cursor, nil, &batch); code != 200 || len(batch.Entries) != 0 || batch.Cursor != cursor {
		t.Fatal("log read self-amplified", code, batch)
	}
	for _, query := range []string{"?component=../../secret", "?component=bridge", "?limit=1000", "?after=../../secret", "?level=token"} {
		if code := adminRequest(t, ts.URL, admin, "GET", "/v1/admin/logs"+query, nil, nil); code != 400 {
			t.Fatalf("query %s: %d", query, code)
		}
	}
}
func TestAdminLogEventsReconnectRevocationAndLimits(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "db")
	ctx := context.Background()
	admin, err := hub.Init(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(dir, "logs")
	logs, err := logbroker.New(logbroker.Options{Dir: logDir, Component: "hub"})
	if err != nil {
		t.Fatal(err)
	}
	st, ts := openTestHub(t, db, hub.Options{Logs: logs, LogDir: logDir, RequestsPerMinute: 1000, Burst: 100})
	var batch logbroker.Batch
	adminRequest(t, ts.URL, admin, "GET", "/v1/admin/logs", nil, &batch)
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(streamCtx, "GET", ts.URL+"/v1/admin/logs/events", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Last-Event-ID", batch.Cursor)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal(resp.Status)
	}
	seen := make(chan string, 100)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			select {
			case seen <- scanner.Text():
			case <-streamCtx.Done():
				return
			}
		}
	}()
	logs.Write(logbroker.Record{Level: "error", Event: "queue_failed"})
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	found := false
	for !found {
		select {
		case line := <-seen:
			found = strings.Contains(line, `"event":"queue_failed"`)
		case <-timer.C:
			t.Fatal("no live log event")
		}
	}
	if _, err = st.RotateAdminToken(ctx, "adm_replacement_fixture"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("revoked admin retained stream")
	}
	// A disabled broker is an API 404, never a successful SPA fallback.
	_, disabled := openTestHub(t, filepath.Join(t.TempDir(), "db"), hub.Options{})
	for _, path := range []string{"/v1/admin/logs", "/v1/admin/logs/events", "/v1/admin/logs/unknown"} {
		if code := adminRequest(t, disabled.URL, admin, "GET", path, nil, nil); code != 404 {
			t.Fatal(path, code)
		}
	}
}
func TestAdminLogReadRateLimit(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "db")
	admin, err := hub.Init(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	_, ts := openTestHub(t, db, hub.Options{LogDir: dir, RequestsPerMinute: 1, Burst: 1})
	code := adminRequest(t, ts.URL, admin, "GET", "/v1/admin/logs", nil, nil)
	if code != 200 {
		t.Fatal(code)
	}
	if code = adminRequest(t, ts.URL, admin, "GET", "/v1/admin/logs", nil, nil); code != 429 {
		t.Fatal(code)
	}
	// Consume and close a response to keep this test independent of UI lifetimes.
	resp, err := http.Get(ts.URL + "/healthz")
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func TestAdminLogSSEConcurrentLimitAndSavedCursor(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "db")
	admin, err := hub.Init(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(dir, "logs")
	logs, err := logbroker.New(logbroker.Options{Dir: logDir, Component: "hub"})
	if err != nil {
		t.Fatal(err)
	}
	_, ts := openTestHub(t, db, hub.Options{Logs: logs, LogDir: logDir, RequestsPerMinute: 1000, Burst: 100})
	var batch logbroker.Batch
	adminRequest(t, ts.URL, admin, "GET", "/v1/admin/logs", nil, &batch)
	var responses []*http.Response
	defer func() {
		for _, r := range responses {
			r.Body.Close()
		}
	}()
	for i := 0; i < 9; i++ {
		req, _ := http.NewRequest("GET", ts.URL+"/v1/admin/logs/events?after="+batch.Cursor, nil)
		req.Header.Set("Authorization", "Bearer "+admin)
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		responses = append(responses, resp)
		want := 200
		if i == 8 {
			want = 429
		}
		if resp.StatusCode != want {
			t.Fatal(i, resp.Status)
		}
	}
	// A saved cursor resumes after the viewer/connection is gone. The producer
	// continues writing without an open response or stdout consumer.
	for _, resp := range responses {
		resp.Body.Close()
	}
	logs.Write(logbroker.Record{Event: "request", Level: "warn"})
	var resumed logbroker.Batch
	if code := adminRequest(t, ts.URL, admin, "GET", "/v1/admin/logs?after="+batch.Cursor, nil, &resumed); code != 200 || len(resumed.Entries) != 1 || resumed.Entries[0].Record.Level != "warn" {
		t.Fatal(code, resumed)
	}
}
