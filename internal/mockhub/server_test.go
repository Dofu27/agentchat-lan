package mockhub_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/mockhub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func newTestHub(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(mockhub.New().Handler())
	t.Cleanup(ts.Close)
	return ts
}

func join(t *testing.T, baseURL, name string) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.New(baseURL, "").Join(ctx, protocol.JoinRequest{Name: name, Kind: "test"})
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	if resp.Token == "" {
		t.Fatal("Join() returned an empty token")
	}
	return client.New(baseURL, resp.Token)
}

func TestTextChatAndInbox(t *testing.T) {
	ts := newTestHub(t)
	c := join(t, ts.URL, "dsh")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msg, err := c.SendMessage(ctx, protocol.DefaultRoom, protocol.SendMessageRequest{Text: "hello @codex"})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if msg.Seq != 1 {
		t.Fatalf("seq = %d, want 1", msg.Seq)
	}
	if len(msg.Mentions) != 1 || msg.Mentions[0] != "codex" {
		t.Fatalf("mentions = %#v, want [codex]", msg.Mentions)
	}

	inbox, err := c.Inbox(ctx, 0, 50)
	if err != nil {
		t.Fatalf("Inbox() error = %v", err)
	}
	if len(inbox.Messages) != 1 || inbox.Messages[0].ID != msg.ID {
		t.Fatalf("Inbox() = %#v, want the sent message", inbox.Messages)
	}
}

func TestUnauthorized(t *testing.T) {
	ts := newTestHub(t)
	resp, err := http.Get(ts.URL + "/v1/rooms")
	if err != nil {
		t.Fatalf("GET /v1/rooms error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestIdempotentSend(t *testing.T) {
	ts := newTestHub(t)
	c := join(t, ts.URL, "dsh")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := protocol.SendMessageRequest{ClientMsgID: "same", Text: "once"}
	first, err := c.SendMessage(ctx, protocol.DefaultRoom, req)
	if err != nil {
		t.Fatalf("first SendMessage() error = %v", err)
	}
	second, err := c.SendMessage(ctx, protocol.DefaultRoom, req)
	if err != nil {
		t.Fatalf("second SendMessage() error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("duplicate send created a new message: %s != %s", first.ID, second.ID)
	}
}

func TestAckAdvancesCursor(t *testing.T) {
	ts := newTestHub(t)
	c := join(t, ts.URL, "dsh")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, err := c.SendMessage(ctx, protocol.DefaultRoom, protocol.SendMessageRequest{Text: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.SendMessage(ctx, protocol.DefaultRoom, protocol.SendMessageRequest{Text: "two"})
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Ack(ctx, map[string]int64{protocol.DefaultRoom: first.Seq}); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	inbox, err := c.Inbox(ctx, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox.Messages) != 1 || inbox.Messages[0].ID != second.ID {
		t.Fatalf("Inbox() after ack = %#v, want only the second message", inbox.Messages)
	}
}

func TestLongPollWakesOnMessage(t *testing.T) {
	ts := newTestHub(t)
	c := join(t, ts.URL, "dsh")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		inbox *protocol.InboxResponse
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		inbox, err := c.Inbox(ctx, 5, 50)
		ch <- result{inbox: inbox, err: err}
	}()

	time.Sleep(100 * time.Millisecond)
	if _, err := c.SendMessage(ctx, protocol.DefaultRoom, protocol.SendMessageRequest{Text: "wake"}); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("Inbox() error = %v", got.err)
		}
		if len(got.inbox.Messages) != 1 || got.inbox.Messages[0].Text != "wake" {
			t.Fatalf("Inbox() = %#v, want the wake message", got.inbox.Messages)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long poll did not wake")
	}
}
