package hub_test

import (
	"context"
	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestFederationAdmissionSlowPeerRecovery(t *testing.T) {
	a, b, c := newFederationNode(t), newFederationNode(t), newFederationNode(t)
	if e := a.st.CreateRoom(context.Background(), protocol.Room{ID: "b-only"}, nil, "fixture"); e != nil {
		t.Fatal(e)
	}
	a.opts = hub.FederationOptions{Peers: []hub.FederationPeerOptions{{NodeID: b.id, BaseURL: b.url, Secret: federationTestSecret}, {NodeID: c.id, BaseURL: c.url, Secret: federationTestSecret}}, Links: []store.FederationLink{{PeerID: b.id, LocalRoom: "b-only", RemoteRoom: "general"}, {PeerID: c.id, LocalRoom: "general", RemoteRoom: "general"}}, MaxOutbox: 100, MaxAttempts: 10, RetryBase: 50 * time.Millisecond, PollInterval: 20 * time.Millisecond, RequestTimeout: 2 * time.Second, LeaseDuration: 5 * time.Second}
	a.startWithOptions(t)
	b.opts = hub.FederationOptions{Peer: hub.FederationPeerOptions{NodeID: a.id, BaseURL: a.url, Secret: federationTestSecret}, Links: []store.FederationLink{{PeerID: a.id, LocalRoom: "general", RemoteRoom: "b-only"}}, MaxOutbox: 100, MaxAttempts: 10, RetryBase: 50 * time.Millisecond, PollInterval: 20 * time.Millisecond, RequestTimeout: 100 * time.Millisecond, LeaseDuration: 300 * time.Millisecond}
	b.startWithOptions(t)
	c.start(t, a)
	sender := nodeReader(t, a, "admission-reader")
	if _, e := a.st.CreateInvite(context.Background(), "b-only-fixture", store.Invite{Room: "b-only", TTL: time.Hour, Uses: 1}, "fixture"); e != nil {
		t.Fatal(e)
	}
	joined, e := client.New(a.url, "").Join(context.Background(), protocol.JoinRequest{InviteCode: "b-only-fixture", Name: "b-only-reader", Kind: "test"})
	if e != nil {
		t.Fatal(e)
	}
	bSender := client.New(a.url, joined.Token)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	baseHandler := *b.handler.Load()
	b.setHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		baseHandler.ServeHTTP(w, r)
	}))
	for range 6 {
		if _, e := bSender.SendMessage(context.Background(), "b-only", protocol.SendMessageRequest{Text: "backlog"}); e != nil {
			t.Fatal(e)
		}
	}
	a.run()
	for range 1 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("both consumers did not enter B")
		}
	}
	baseline := c.requests.Load()
	if _, e := sender.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "healthy peer"}); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(time.Second)
	for c.requests.Load() == baseline && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if c.requests.Load() == baseline {
		t.Fatal("healthy peer failed to progress with B backlog")
	}
	once.Do(func() { close(release) })
	waitFederation(t, func() bool {
		stats, _ := a.st.FederationStats(context.Background())
		return stats.Delivered == 7 && stats.Dead == 0
	})
	t.Log("PASS: healthy peer progresses while B has six queued jobs")
}
