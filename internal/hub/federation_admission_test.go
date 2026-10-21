package hub_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFederationAdmissionPreservesLeases(t *testing.T) {
	var active, maxActive atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
		}
		var e store.FederationEnvelope
		_ = json.NewDecoder(r.Body).Decode(&e)
		time.Sleep(150 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(store.FederationReceipt{OriginNodeID: e.OriginNodeID, OriginMessageID: e.OriginMessageID, LocalMessageID: "fixture"})
	}))
	defer remote.Close()
	a := newFederationNode(t)
	opts := hub.FederationOptions{MaxOutbox: 100, MaxAttempts: 1, RetryBase: 20 * time.Millisecond, PollInterval: 10 * time.Millisecond, RequestTimeout: time.Second, LeaseDuration: 2 * time.Second}
	for i := range 32 {
		id := fmt.Sprintf("fixture-peer-%02d", i)
		opts.Peers = append(opts.Peers, hub.FederationPeerOptions{NodeID: id, BaseURL: remote.URL, Secret: federationTestSecret})
		opts.Links = append(opts.Links, store.FederationLink{PeerID: id, LocalRoom: "general", RemoteRoom: "general"})
	}
	a.opts = opts
	a.startWithOptions(t)
	sender := nodeReader(t, a, "fixture-reader")
	if _, e := sender.SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: "synthetic"}); e != nil {
		t.Fatal(e)
	}
	a.run()
	end := time.Now().Add(5 * time.Second)
	var st store.FederationQueueStats
	for time.Now().Before(end) {
		var e error
		st, e = a.st.FederationStats(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if st.Delivered+st.Dead == 32 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if maxActive.Load() > 8 {
		t.Fatalf("global limit exceeded %d", maxActive.Load())
	}
	if st.Dead != 0 || st.Delivered != 32 || st.Attempts != 32 {
		t.Fatalf("successful peers must all be delivered, got %+v", st)
	}
	t.Logf("all peers delivered within lease after admission: %+v; max concurrent=%d", st, maxActive.Load())
}
