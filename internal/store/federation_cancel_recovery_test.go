package store

import (
	"context"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func TestCancelledClaimExpiresWithoutCounterDrift(t *testing.T) {
	ctx := context.Background()
	s, _ := federationTestStore(t)
	enableFederationTest(t, s, 10, 3)
	if _, err := s.SendMessage(ctx, "local", "general", protocol.SendMessageRequest{Text: "synthetic"}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Second)
	first, err := s.ClaimFederationJob(ctx, now, time.Second)
	if err != nil || first == nil {
		t.Fatalf("first claim: %v %v", first, err)
	}
	// Cancellation after claim leaves the lease for expiry, without finishing it.
	second, err := s.ClaimFederationJob(ctx, now.Add(2*time.Second), time.Second)
	if err != nil || second == nil || first.Lease == second.Lease {
		t.Fatalf("recovery claim: %v %v", second, err)
	}
	if err := s.FinishFederationJob(ctx, *second, true, false, now, ""); err != nil {
		t.Fatal(err)
	}
	// Late success and failure from the cancelled worker must both be inert.
	for _, delivered := range []bool{true, false} {
		if err := s.FinishFederationJob(ctx, *first, delivered, true, now, "stale fixture"); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.FederationStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Delivered != 1 || st.Dead != 0 || st.Pending != 0 || st.Inflight != 0 || st.Attempts != 2 {
		t.Fatalf("recovered lease counters drifted: %+v", st)
	}
}
