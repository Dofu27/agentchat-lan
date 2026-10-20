package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func TestFederationInboundAuthAndReplay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "agentchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateInvite(ctx, "fed-hub-invite", store.Invite{Room: "general", TTL: time.Hour, Uses: 1}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Join(ctx, "fed-hub-invite", protocol.Agent{ID: "agt_local", Name: "local", Kind: "dsh"}, "token"); err != nil {
		t.Fatal(err)
	}
	peer, err := st.ConfigureFederationPeer(ctx, "peer-b", "http://127.0.0.1:1", "0123456789abcdef", "node_b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFederationLink(ctx, "general", peer.ID, "general", "test"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(hub.New(st, hub.Options{Logger: log.New(io.Discard, "", 0), FileDir: filepath.Join(dir, "files")}).Handler())
	defer ts.Close()

	payload := map[string]any{
		"room": "general", "origin_node": "node_b", "origin_message_id": "remote-1",
		"text": "inbound hello", "sender": map[string]string{"id": "agt_remote", "name": "remote", "kind": "dsh"},
		"ts": time.Now().UTC(),
	}
	body, _ := json.Marshal(payload)
	post := func(secret, origin string) *http.Response {
		p := payload
		p["origin_node"] = origin
		body, _ = json.Marshal(p)
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/federation/messages", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := post("wrong-secret", "node_b")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong secret status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = post("0123456789abcdef", "node_b")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid secret status = %d", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out["replayed"] != false {
		t.Fatalf("first inbound replayed = %v", out["replayed"])
	}
	resp = post("0123456789abcdef", "node_b")
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out["replayed"] != true {
		t.Fatalf("second inbound replayed = %v", out["replayed"])
	}
	resp = post("0123456789abcdef", "node_other")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong origin status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}
