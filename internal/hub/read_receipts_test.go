package hub_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func TestExplicitReadReceiptsHTTP(t *testing.T) {
	ctx := context.Background()
	st, ts := openTestHub(t, filepath.Join(t.TempDir(), "reads.sqlite"), hub.Options{Burst: 200, RequestsPerMinute: 10000})
	if _, err := st.CreateInvite(ctx, "read-invite", store.Invite{Room: protocol.DefaultRoom, TTL: time.Hour, Uses: 2}, "test"); err != nil {
		t.Fatal(err)
	}
	a := joinAgent(t, ts.URL, "read-invite", "read-a")
	b := joinAgent(t, ts.URL, "read-invite", "read-b")
	identity, err := st.AgentByToken(ctx, a.Token)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := a.SendMessage(ctx, protocol.DefaultRoom, protocol.SendMessageRequest{Text: "read fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Ack(ctx, map[string]int64{protocol.DefaultRoom: 1}); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []int64{2, 1, 2} {
		r, err := a.MarkRead(ctx, protocol.DefaultRoom, seq)
		if err != nil || r.ReadSeq != 2 || r.AgentID != identity.ID || r.Room != protocol.DefaultRoom {
			t.Fatal(r, err)
		}
	}
	inbox, err := a.Inbox(ctx, 0, 20)
	if err != nil || len(inbox.Messages) != 2 {
		t.Fatal("read must not ACK", inbox, err)
	}
	var receipts struct {
		Receipts []struct {
			AgentID string `json:"agent_id"`
			Ack     int64  `json:"last_ack_seq"`
			Read    *int64 `json:"read_seq"`
		} `json:"receipts"`
	}
	if status := adminRequest(t, ts.URL, a.Token, "GET", "/v1/rooms/general/receipts", nil, &receipts); status != 200 {
		t.Fatal(status)
	}
	found := false
	for _, r := range receipts.Receipts {
		if r.Read == nil {
			t.Fatal("missing additive read_seq", r)
		}
		if r.AgentID == identity.ID {
			found = true
			if r.Ack != 1 || *r.Read != 2 {
				t.Fatal("cursors conflated", r)
			}
		} else if *r.Read != 0 {
			t.Fatal("another agent's read state changed", r)
		}
	}
	if !found {
		t.Fatal("reader missing from receipts")
	}
	for _, seq := range []int64{-1, 4} {
		_, err := a.MarkRead(ctx, protocol.DefaultRoom, seq)
		var api *client.APIError
		if !errors.As(err, &api) || api.StatusCode != 400 {
			t.Fatal(seq, err)
		}
	}
	if err := st.CreateRoom(ctx, protocol.Room{ID: "private-read", Kind: "room"}, []string{identity.ID}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRoom(ctx, protocol.Room{ID: "read/slash", Kind: "room"}, []string{identity.ID}, "test"); err != nil {
		t.Fatal(err)
	}
	if receipt, err := a.MarkRead(ctx, "read/slash", 0); err != nil || receipt.Room != "read/slash" || receipt.ReadSeq != 0 {
		t.Fatal("escaped slash must remain one mux wildcard value", receipt, err)
	}
	for _, tc := range []struct {
		room string
		want int
	}{{"private-read", 403}, {"unknown-read", 404}} {
		if got := adminRequest(t, ts.URL, b.Token, "POST", "/v1/rooms/"+tc.room+"/read", map[string]int64{"seq": 0}, nil); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	if err := st.ArchiveRoom(ctx, "private-read", "test"); err != nil {
		t.Fatal(err)
	}
	if got := adminRequest(t, ts.URL, a.Token, "POST", "/v1/rooms/private-read/read", map[string]int64{"seq": 0}, nil); got != 404 {
		t.Fatal("archived room", got)
	}
	for _, body := range []string{`{}`, `{"seq":null}`, `{"seq":"1"}`, `{"seq":1.5}`, `{"seq":9223372036854775808}`, `{"seq":0} {"seq":1}`} {
		req, _ := http.NewRequest("POST", ts.URL+"/v1/rooms/general/read", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+a.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("invalid read body %s => %d", body, resp.StatusCode)
		}
	}
	if got := adminRequest(t, ts.URL, "", "POST", "/v1/rooms/general/read?token="+a.Token, map[string]int64{"seq": 0}, nil); got != 401 {
		t.Fatal("query token accepted", got)
	}
	if err := st.RevokeAgent(ctx, identity.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.MarkRead(ctx, protocol.DefaultRoom, 3); !client.IsUnauthorized(err) {
		t.Fatal("revoked token accepted", err)
	}
}
