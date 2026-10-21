package hub_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func startDMNodes(t *testing.T, typing ...bool) (*federationNode, *federationNode, *client.Client, *client.Client, protocol.Room, protocol.Room) {
	t.Helper()
	a, b := newFederationNode(t), newFederationNode(t)
	ctx := context.Background()
	for _, pair := range []struct {
		n         *federationNode
		id, token string
	}{{a, "person-a", "synthetic-a-token"}, {b, "person-b", "synthetic-b-token"}} {
		if _, err := pair.n.st.CreateInvite(ctx, "invite", store.Invite{Room: "general", TTL: time.Hour, Uses: 1}, "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := pair.n.st.Join(ctx, "invite", protocol.Agent{ID: pair.id, Name: pair.id, Kind: "human"}, pair.token); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range []struct {
		n, peer       *federationNode
		local, remote string
	}{{a, b, "person-a", "person-b"}, {b, a, "person-b", "person-a"}} {
		pair.n.opts = hub.FederationOptions{Peer: hub.FederationPeerOptions{NodeID: pair.peer.id, BaseURL: pair.peer.url, Secret: federationTestSecret}, DMConversations: []store.FederationDMConversation{{Typing: len(typing) > 0 && typing[0], ConversationID: "private-chat", LinkID: "private-link", Epoch: 1, LocalAgentID: pair.local, RemoteAgentID: pair.remote}}, MaxOutbox: 100, MaxAttempts: 5, RetryBase: 50 * time.Millisecond, PollInterval: 20 * time.Millisecond, RequestTimeout: 300 * time.Millisecond, LeaseDuration: time.Second}
		pair.n.startWithOptions(t)
	}
	roomA, err := a.st.OpenFederatedDM(ctx, "person-a", "private-chat", 1)
	if err != nil {
		t.Fatal(err)
	}
	roomB, err := b.st.OpenFederatedDM(ctx, "person-b", "private-chat", 1)
	if err != nil {
		t.Fatal(err)
	}
	return a, b, client.New(a.url, "synthetic-a-token"), client.New(b.url, "synthetic-b-token"), roomA, roomB
}
func dmHTTPRequest(t *testing.T, n *federationNode, method, path, token string, body []byte) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, n.url+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}
func dmPeerRequest(t *testing.T, n *federationNode, origin string, e store.FederationDMEnvelope, secret string) (int, store.FederationReceipt) {
	t.Helper()
	body, _ := json.Marshal(e)
	req, _ := http.NewRequest("POST", n.url+"/federation/v2/dm/messages", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("X-AgentChat-Node", origin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r store.FederationReceipt
	json.NewDecoder(resp.Body).Decode(&r)
	return resp.StatusCode, r
}

func TestFederatedDMTwoNodesBidirectionalLostACKAndNoLoop(t *testing.T) {
	a, b, ca, cb, ra, rb := startDMNodes(t)
	ctx := context.Background()
	original := *b.handler.Load()
	var lost atomic.Bool
	b.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/federation/v2/dm/messages" {
			rec := httptest.NewRecorder()
			original.ServeHTTP(rec, r)
			if rec.Code == 200 && lost.CompareAndSwap(false, true) {
				w.WriteHeader(500)
				return
			}
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			w.Write(rec.Body.Bytes())
			return
		}
		original.ServeHTTP(w, r)
	}))
	m, err := ca.SendMessage(ctx, ra.ID, protocol.SendMessageRequest{Text: "private A to B", ClientMsgID: "a-once"})
	if err != nil {
		t.Fatal(err)
	}
	a.run()
	b.run()
	waitFederation(t, func() bool {
		status, err := a.st.FederationOperatorStatus(ctx)
		return err == nil && status.DirectMessages.Delivered == 1
	})
	messages, _, err := b.st.History(ctx, "person-b", rb.ID, 0, 0, 20)
	if err != nil || len(messages) != 1 || messages[0].Text != m.Text || messages[0].Meta.Federation.ConversationID != "private-chat" {
		t.Fatal(messages, err)
	}
	if !lost.Load() {
		t.Fatal("lost ACK test not reached")
	}
	if _, err = cb.SendMessage(ctx, rb.ID, protocol.SendMessageRequest{Text: "private B to A"}); err != nil {
		t.Fatal(err)
	}
	waitFederation(t, func() bool {
		status, _ := b.st.FederationOperatorStatus(ctx)
		return status.DirectMessages.Delivered == 1
	})
	messages, _, err = a.st.History(ctx, "person-a", ra.ID, 0, 0, 20)
	if err != nil || len(messages) != 2 {
		t.Fatal(messages, err)
	}
	for _, n := range []*federationNode{a, b} {
		stats, _ := n.st.FederationStats(ctx)
		if stats.Delivered+stats.Pending != 0 {
			t.Fatal("DM exported through ordinary text queue")
		}
		status, _ := n.st.FederationOperatorStatus(ctx)
		if status.DirectMessages.Delivered != 1 {
			t.Fatal("remote DM looped", status)
		}
	}
	code, body := dmHTTPRequest(t, a, "GET", "/v1/dm/remote", "synthetic-a-token", nil)
	if code != 200 || !bytes.Contains(body, []byte("person-b")) {
		t.Fatal(code, string(body))
	}
	code, _ = dmHTTPRequest(t, a, "POST", "/v1/dm/remote", "synthetic-a-token", []byte(`{"conversation_id":"private-chat","epoch":1}`))
	if code != 200 {
		t.Fatal(code)
	}
}

func TestFederatedDMHTTPAuthorizationAndStrictIdentity(t *testing.T) {
	a, b, ca, _, ra, rb := startDMNodes(t)
	ctx := context.Background()
	other := nodeReader(t, b, "outsider")
	me, _ := other.Me(ctx)
	token := other.Token
	for _, path := range []string{"/v1/rooms/" + rb.ID + "/messages", "/v1/rooms/" + rb.ID + "/search?q=private", "/v1/rooms/" + rb.ID + "/events"} {
		if code, _ := dmHTTPRequest(t, b, "GET", path, token, nil); code != 403 {
			t.Fatal("outsider access", path, code)
		}
	}
	if code, body := dmHTTPRequest(t, b, "GET", "/v1/dm/remote", token, nil); code != 200 || bytes.Contains(body, []byte("private-chat")) {
		t.Fatal("contact directory leaked", code, string(body))
	}
	if code, _ := dmHTTPRequest(t, b, "POST", "/v1/dm/remote", token, []byte(`{"conversation_id":"private-chat","epoch":1}`)); code != 403 {
		t.Fatal(code)
	}
	if code, _ := dmHTTPRequest(t, b, "POST", "/v1/rooms/"+rb.ID+"/typing", "synthetic-b-token", []byte(`{"typing":true}`)); code != 403 {
		t.Fatal(code)
	}
	if code, _ := dmHTTPRequest(t, b, "GET", "/v1/rooms/"+rb.ID+"/receipts", "synthetic-b-token", nil); code != 403 {
		t.Fatal(code)
	}
	m, err := ca.SendMessage(ctx, ra.ID, protocol.SendMessageRequest{Text: "private text"})
	if err != nil {
		t.Fatal(err)
	}
	e := store.FederationDMEnvelope{Version: 2, Type: "federated_dm", OriginNodeID: a.id, OriginMessageID: m.ID, ConversationID: "private-chat", LinkID: "private-link", Epoch: 1, RecipientNodeID: b.id, RecipientAgentID: "person-b", Sender: m.From, Text: m.Text, SentAt: m.TS}
	if code, _ := dmPeerRequest(t, b, a.id, e, "bad-secret"); code != 401 {
		t.Fatal(code)
	}
	if code, _ := dmPeerRequest(t, b, "unknown-node", e, federationTestSecret); code != 401 {
		t.Fatal(code)
	}
	if code, r := dmPeerRequest(t, b, a.id, e, federationTestSecret); code != 200 || r.LocalMessageID == "" {
		t.Fatal(code, r)
	}
	bad := e
	bad.Text = "changed"
	if code, _ := dmPeerRequest(t, b, a.id, bad, federationTestSecret); code != 409 {
		t.Fatal("DM conflict", code)
	}
	bad = e
	bad.Sender.ID = me.Agent.ID
	if code, _ := dmPeerRequest(t, b, a.id, bad, federationTestSecret); code != 403 {
		t.Fatal("peer impersonated third/local user", code)
	}
	raw, _ := json.Marshal(e)
	raw = bytes.Replace(raw, []byte(`"version":2`), []byte(`"files":[],"version":2`), 1)
	req, _ := http.NewRequest("POST", b.url+"/federation/v2/dm/messages", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+federationTestSecret)
	req.Header.Set("X-AgentChat-Node", a.id)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("DM attachment wire field accepted")
	}
	// /v1/dm remains a local-only conversation and never exports its history.
	local := nodeReader(t, a, "local-contact")
	_ = local
	code, body := dmHTTPRequest(t, a, "POST", "/v1/dm", "synthetic-a-token", []byte(`{"agent":"local-contact"}`))
	if code != 200 && code != 201 {
		t.Fatal(code, string(body))
	}
	var localRoom protocol.Room
	json.Unmarshal(body, &localRoom)
	if localRoom.Kind != "dm" {
		t.Fatal("local DM kind changed")
	}
	if _, err = ca.SendMessage(ctx, localRoom.ID, protocol.SendMessageRequest{Text: "local only"}); err != nil {
		t.Fatal(err)
	}
	status, _ := a.st.FederationOperatorStatus(ctx)
	if status.DirectMessages.Pending+status.DirectMessages.Inflight+status.DirectMessages.Delivered != 1 {
		t.Fatal("local DM was exported")
	}
}

func TestFederatedDMLeaveClosesSSEAndStopsEpoch(t *testing.T) {
	a, _, _, _, room, _ := startDMNodes(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.url+"/v1/rooms/"+room.ID+"/events", nil)
	req.Header.Set("Authorization", "Bearer synthetic-a-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "data: {}\n" {
			break
		}
	}
	if code, _ := dmHTTPRequest(t, a, "DELETE", "/v1/dm/remote", "synthetic-a-token", []byte(`{"conversation_id":"private-chat","epoch":1}`)); code != 204 {
		t.Fatal(code)
	}
	closed := make(chan bool, 1)
	go func() { _, err := io.ReadAll(reader); closed <- err == nil }()
	select {
	case clean := <-closed:
		if !clean {
			t.Fatal("stream did not close on retirement")
		}
	case <-time.After(time.Second):
		t.Fatal("retired stream remained open")
	}
	if code, _ := dmHTTPRequest(t, a, "GET", "/v1/rooms/"+room.ID+"/messages", "synthetic-a-token", nil); code != 403 {
		t.Fatal(code)
	}
}

func TestFederatedDMUnreachableRestartRecovery(t *testing.T) {
	a, b, ca, _, ra, rb := startDMNodes(t)
	b.server.Close()
	if _, err := ca.SendMessage(context.Background(), ra.ID, protocol.SendMessageRequest{Text: "durable private"}); err != nil {
		t.Fatal(err)
	}
	a.run()
	waitFederation(t, func() bool {
		s, _ := a.st.FederationOperatorStatus(context.Background())
		return s.DirectMessages.Pending == 1 && s.DirectMessages.Attempts > 0
	})
	a.stopWorker(t)
	a.st.Close()
	var err error
	a.st, err = store.Open(context.Background(), a.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	a.fed, err = hub.NewFederation(context.Background(), a.st, a.opts)
	if err != nil {
		t.Fatal(err)
	}
	a.setHandler(hub.New(a.st, hub.Options{Federation: a.fed, FileDir: filepath.Join(filepath.Dir(a.dbPath), "files")}).Handler())
	listener, err := net.Listen("tcp", strings.TrimPrefix(b.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	b.server = httptest.NewUnstartedServer(nil)
	b.server.Listener.Close()
	b.server.Listener = listener
	b.startWithOptions(t)
	a.run()
	waitFederation(t, func() bool {
		s, _ := a.st.FederationOperatorStatus(context.Background())
		return s.DirectMessages.Delivered == 1
	})
	messages, _, err := b.st.History(context.Background(), "person-b", rb.ID, 0, 0, 20)
	if err != nil || len(messages) != 1 {
		t.Fatal(messages, err)
	}
}

func TestFederatedDMRoutesAreExplicitlyDisabled(t *testing.T) {
	a, b := newFederationNode(t), newFederationNode(t)
	a.start(t, b)
	b.start(t, a)
	for _, path := range []string{"/v1/dm/remote", "/federation/v2/dm/messages"} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			if code, _ := dmHTTPRequest(t, a, method, path, "bad", []byte("{}")); code != 404 {
				t.Fatal("disabled DM route", method, path, code)
			}
		}
	}
}
