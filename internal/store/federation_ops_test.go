package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func operatorStore(t *testing.T) (*Store, string, FederationJob) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, err = st.ConfigureFederation(ctx, []FederationPeer{{NodeID: "peer-node", BaseURL: "http://127.0.0.1:1", SecretHash: strings.Repeat("a", 64)}}, []FederationLink{{PeerID: "peer-node", LocalRoom: "general", RemoteRoom: "remote"}}, FederationLimits{10, 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateInvite(ctx, "test-invite", Invite{Room: "general", Uses: 1, TTL: time.Hour}, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Join(ctx, "test-invite", protocol.Agent{ID: "agt_test", Name: "tester", Kind: "test"}, "local-test-token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.SendMessage(ctx, "agt_test", "general", protocol.SendMessageRequest{Text: "sensitive message payload"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	j, err := st.ClaimFederationJob(ctx, time.Now(), time.Minute)
	if err != nil || j == nil {
		t.Fatalf("job %v %v", j, err)
	}
	if err = st.FinishFederationJob(ctx, *j, false, true, time.Now(), "peer transport failure"); err != nil {
		t.Fatal(err)
	}
	return st, path, *j
}

func TestFederationOperatorRetryAtomicIdempotentAndDurable(t *testing.T) {
	st, path, old := operatorStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var fresh atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := st.RetryFederationOutbox(ctx, old.ID, "same-request")
			if err != nil {
				t.Error(err)
			} else if !r.Replayed {
				fresh.Add(1)
			}
		}()
	}
	wg.Wait()
	if fresh.Load() != 1 {
		t.Fatalf("fresh retries %d", fresh.Load())
	}
	var state, lease string
	var attempts int
	var until, next int64
	if err := st.db.QueryRow(`SELECT state,attempts,lease_token,lease_until,next_attempt FROM federation_outbox WHERE id=?`, old.ID).Scan(&state, &attempts, &lease, &until, &next); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || attempts != 0 || lease != "" || until != 0 || next > time.Now().UnixMilli() {
		t.Fatalf("reset incomplete %s %d %s %d %d", state, attempts, lease, until, next)
	}
	if _, err := st.RetryFederationOutbox(ctx, old.ID, "new-request"); !errors.Is(err, ErrFederationNotDead) {
		t.Fatalf("nondead %v", err)
	}
	if _, err := st.RetryFederationOutbox(ctx, "unknown", "request"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown %v", err)
	}
	// An old worker cannot finish or undo an operator reset.
	if err := st.FinishFederationJob(ctx, old, true, false, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	stats, err := st.FederationStats(ctx)
	if err != nil || stats.Pending != 1 {
		t.Fatalf("stale worker %v %v", stats, err)
	}
	events, err := st.Audit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Action == "federation.outbox_retried" {
			n++
			if e.Actor != "admin" || e.Detail["id"] != old.ID {
				t.Fatalf("bad audit %+v", e)
			}
		}
	}
	if n != 1 {
		t.Fatalf("audit count %d", n)
	}
	st.Close()
	st2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	// Runtime remains off on restart; even retries with old keys require opt-in.
	if _, err = st2.RetryFederationOutbox(ctx, old.ID, "same-request"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("restart enabled retry: %v", err)
	}
	st2.federation = &FederationLimits{10, 2}
	r, err := st2.RetryFederationOutbox(ctx, old.ID, "same-request")
	if err != nil || !r.Replayed {
		t.Fatalf("receipt not durable %v %v", r, err)
	}
}

func TestFederationOperatorAuditFailureRollsBack(t *testing.T) {
	st, _, job := operatorStore(t)
	ctx := context.Background()
	if _, err := st.db.Exec(`CREATE TRIGGER fail_operator_audit BEFORE INSERT ON audit WHEN NEW.action='federation.outbox_retried' BEGIN SELECT RAISE(ABORT,'test audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RetryFederationOutbox(ctx, job.ID, "request"); err == nil {
		t.Fatal("audit failure ignored")
	}
	stats, err := st.FederationStats(ctx)
	if err != nil || stats.Dead != 1 {
		t.Fatalf("retry escaped rollback %+v %v", stats, err)
	}
	var n int
	if err = st.db.QueryRow("SELECT count(*) FROM federation_operator_retries").Scan(&n); err != nil || n != 0 {
		t.Fatal("receipt escaped rollback")
	}
}

func TestFederationOperatorRetryDoesNotReplayASecondDeath(t *testing.T) {
	st, _, job := operatorStore(t)
	ctx := context.Background()
	if _, err := st.RetryFederationOutbox(ctx, job.ID, "first-request"); err != nil {
		t.Fatal(err)
	}
	j, err := st.ClaimFederationJob(ctx, time.Now(), time.Minute)
	if err != nil || j == nil {
		t.Fatalf("%v %v", j, err)
	}
	if _, err = st.RetryFederationOutbox(ctx, job.ID, "while-inflight"); !errors.Is(err, ErrFederationNotDead) {
		t.Fatalf("inflight retry %v", err)
	}
	if err = st.FinishFederationJob(ctx, *j, false, true, time.Now(), "peer HTTP 403"); err != nil {
		t.Fatal(err)
	}
	r, err := st.RetryFederationOutbox(ctx, job.ID, "first-request")
	if err != nil || !r.Replayed {
		t.Fatalf("receipt replay %v %v", r, err)
	}
	stats, err := st.FederationStats(ctx)
	if err != nil || stats.Dead != 1 {
		t.Fatal("old retry reactivated second death")
	}
	if _, err = st.RetryFederationOutbox(ctx, job.ID, "second-request"); err != nil {
		t.Fatal(err)
	}
	j, err = st.ClaimFederationJob(ctx, time.Now(), time.Minute)
	if err != nil || j == nil {
		t.Fatalf("%v %v", j, err)
	}
	if err = st.FinishFederationJob(ctx, *j, true, false, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err = st.RetryFederationOutbox(ctx, job.ID, "while-delivered"); !errors.Is(err, ErrFederationNotDead) {
		t.Fatalf("delivered retry %v", err)
	}
}

func TestFederationOperatorRedactionAndReadOnly(t *testing.T) {
	st, path, job := operatorStore(t)
	ctx := context.Background()
	// Defense against unexpected arbitrary stored error strings.
	if _, err := st.db.Exec("UPDATE federation_outbox SET last_error=? WHERE id=?", "Bearer secret-and-payload", job.ID); err != nil {
		t.Fatal(err)
	}
	items, err := st.ListFederationOutbox(ctx, "dead", 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("%v %v", items, err)
	}
	if items[0].LastError != "delivery failure" {
		t.Fatal("arbitrary error exposed")
	}
	status, err := ReadFederationOperatorStatus(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(struct {
		Status FederationOperatorStatus
		Items  []FederationOutboxItem
	}{status, items})
	for _, s := range []string{"sensitive message payload", "Bearer secret", strings.Repeat("a", 64), "payload\"", "secret_hash", "lease_token", "http://"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("operator data includes %q", s)
		}
	}
	if status.Outbox.Dead != 1 || status.Outbox.Attempts != 1 || len(status.Links) != 1 {
		t.Fatalf("bad status %+v", status)
	}
	if _, err = ReadFederationOperatorStatus(ctx, filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("missing db created")
	}
}
