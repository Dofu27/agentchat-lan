package hub_test

import (
	"bufio"
	"context"
	"io"
	"log"
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

func TestRoomEventsStream(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "agentchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	code := "event-test"
	if _, err := st.CreateInvite(ctx, code, store.Invite{Room: "general", TTL: time.Hour, Uses: 2}, "test"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(hub.New(st, hub.Options{Logger: log.New(io.Discard, "", 0), FileDir: filepath.Join(dir, "files")}).Handler())
	defer ts.Close()

	a, err := client.New(ts.URL, "").Join(ctx, protocol.JoinRequest{InviteCode: code, Name: "event-a", Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.New(ts.URL, "").Join(ctx, protocol.JoinRequest{InviteCode: code, Name: "event-b", Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, ts.URL+"/v1/rooms/general/events?after_seq=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type = %q", got)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = client.New(ts.URL, a.Token).SendMessage(ctx, protocol.DefaultRoom, protocol.SendMessageRequest{Text: "event payload"})
	}()

	scanner := bufio.NewScanner(resp.Body)
	seen := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, `"text":"event payload"`) {
			seen = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("message event not received")
	}
}
