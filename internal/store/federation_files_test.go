package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func fileStore(t *testing.T) (*Store, string) {
	s, path := federationTestStore(t)
	enableFederationTest(t, s, 1000, 3)
	if err := s.ConfigureFederationFiles(context.Background(), []FederationFileLink{{PeerID: "remote-node", LocalRoom: "general", RemoteRoom: "general", LinkID: "link", Epoch: 1}}); err != nil {
		t.Fatal(err)
	}
	return s, path
}
func manifest(size int64) FederationFileEnvelope {
	sum := sha256.Sum256([]byte("content"))
	return FederationFileEnvelope{Version: 2, OriginNodeID: "remote-node", OriginMessageID: protocol.NewID(), SourceRoom: "general", DestinationRoom: "general", LinkID: "link", Epoch: 1, Sender: protocol.AgentRef{ID: "foreign", Name: "foreign", Kind: "test"}, SentAt: time.Now().UTC(), Files: []FederationBlob{{SHA256: hex.EncodeToString(sum[:]), Size: size, Name: "file.bin"}}}
}
func TestFederationFilesAtomicSourceBackpressureAndLimits(t *testing.T) {
	s, _ := fileStore(t)
	ctx := context.Background()
	s.federation.MaxOutbox = 1
	e := manifest(1)
	ref, _, err := s.RegisterFile(ctx, protocol.FileRef{ID: "test-file", SHA256: e.Files[0].SHA256, Size: 1, Name: "file.bin"}, "ab/blob")
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.SendMessageRequest{Files: []string{ref.Ref.ID}, ClientMsgID: "one"}
	sent, err := s.SendMessage(ctx, "local", "general", req, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.ClientMsgID = "two"
	if _, err = s.SendMessage(ctx, "local", "general", req, nil); !errors.Is(err, ErrFederationFull) {
		t.Fatal("missing atomic backpressure", err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n)
	if n != 1 {
		t.Fatal("local message survived failed outbox")
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM federation_blob_grants`).Scan(&n)
	if n != 1 {
		t.Fatal("missing atomic grant")
	}
	var payload string
	s.db.QueryRow(`SELECT payload FROM federation_file_outbox`).Scan(&payload)
	var out FederationFileEnvelope
	json.Unmarshal([]byte(payload), &out)
	if out.OriginMessageID != sent.Message.ID || len(out.Files) != 1 {
		t.Fatal("bad wire snapshot")
	}
	for _, size := range []int64{-1, FederationFileSize + 1} {
		bad := manifest(size)
		if _, err = s.StageFederationFiles(ctx, "remote-node", bad); !errors.Is(err, ErrInvalidTarget) {
			t.Fatal("oversize accepted", err)
		}
	}
	bad := manifest(FederationFileSize)
	b := bad.Files[0]
	b.SHA256 = hex.EncodeToString(make([]byte, 32))
	bad.Files = append(bad.Files, b)
	b.SHA256 = hex.EncodeToString(bytes32(1))
	bad.Files = append(bad.Files, b)
	if bad.Validate("remote-node") == nil {
		t.Fatal("aggregate >64MiB")
	}
}
func bytes32(b byte) []byte { v := make([]byte, 32); v[0] = b; return v }
func TestFederationFilesConcurrentQuotaReplayAndCrossVersion(t *testing.T) {
	s, _ := fileStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			_, err := s.StageFederationFiles(ctx, "remote-node", manifest(FederationFileSize))
			if err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			} else if !errors.Is(err, ErrFederationQuota) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if accepted != 8 {
		t.Fatalf("512MiB conservative reservation: accepted=%d", accepted)
	}
	var reserved int64
	s.db.QueryRow(`SELECT SUM(reserved) FROM federation_file_transfers`).Scan(&reserved)
	if reserved > FederationReceiveBudget {
		t.Fatal("over budget")
	}
	s2, _ := fileStore(t)
	e := manifest(10)
	r, err := s2.StageFederationFiles(ctx, "remote-node", e)
	if err != nil {
		t.Fatal(err)
	}
	var receipts sync.Map
	for i := 0; i < 10; i++ {
		wg.Go(func() {
			r, err := s2.StageFederationFiles(ctx, "remote-node", e)
			if err != nil {
				t.Error(err)
			}
			receipts.Store(r.TransferID, true)
		})
	}
	wg.Wait()
	n := 0
	receipts.Range(func(_, _ any) bool { n++; return true })
	if n != 1 {
		t.Fatal("duplicate staged twice")
	}
	v1 := FederationEnvelope{Version: 1, OriginNodeID: e.OriginNodeID, OriginMessageID: e.OriginMessageID, SourceRoom: e.SourceRoom, DestinationRoom: e.DestinationRoom, Sender: e.Sender, Text: "text", SentAt: e.SentAt}
	if _, err = s2.ImportFederation(ctx, "remote-node", v1); !errors.Is(err, ErrFederationConflict) {
		t.Fatal("v1/v2 collision", err)
	}
	e.Text = "conflict"
	if _, err = s2.StageFederationFiles(ctx, "remote-node", e); !errors.Is(err, ErrFederationConflict) {
		t.Fatal("collision accepted", err)
	}
	s2.db.Exec(`UPDATE federation_file_transfers SET state='dead' WHERE id=?`, r.TransferID)
	e.Text = ""
	if _, err = s2.StageFederationFiles(ctx, "remote-node", e, 2); err != nil {
		t.Fatal("retry generation", err)
	}
	if _, err = s2.StageFederationFiles(ctx, "remote-node", e, 1); !errors.Is(err, ErrFederationConflict) {
		t.Fatal("old generation revived", err)
	}
}
func TestFederationFilesLeaseEpochExpiryAndRestart(t *testing.T) {
	s, path := fileStore(t)
	ctx := context.Background()
	e := manifest(10)
	r, err := s.StageFederationFiles(ctx, "remote-node", e)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.ClaimFederationFileJob(ctx, true, time.Second)
	if err != nil || j == nil {
		t.Fatal(j, err)
	}
	s.db.Exec(`UPDATE federation_file_transfers SET lease_until=0 WHERE id=?`, j.ID)
	replacement, err := s.ClaimFederationFileJob(ctx, true, time.Second)
	if err != nil || replacement == nil || replacement.Lease == j.Lease {
		t.Fatal("claim not fenced", err)
	}
	if err = s.FederationFileCheckpoint(ctx, *j, &FederationFileProgress{Index: 1}, time.Second); !errors.Is(err, ErrFederationExpired) {
		t.Fatal("stale lease checkpoint", err)
	}
	s.db.Exec(`UPDATE federation_file_transfers SET deadline=0 WHERE id=?`, r.TransferID)
	if _, err = s.StageFederationFiles(ctx, "remote-node", e, 2); !errors.Is(err, ErrFederationExpired) {
		t.Fatal("deadline renewed", err)
	}
	if err = s.ConfigureFederationFiles(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureFederationFiles(ctx, []FederationFileLink{{PeerID: "remote-node", LocalRoom: "general", RemoteRoom: "general", LinkID: "link", Epoch: 1}}); !errors.Is(err, ErrFederationConflict) {
		t.Fatal("retired epoch revived", err)
	}
	s.Close()
	reopen, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopen.Close()
	if _, err = reopen.StageFederationFiles(ctx, "remote-node", e); !errors.Is(err, ErrForbidden) {
		t.Fatal("files implicitly enabled after restart", err)
	}
}
func TestFederationFilesStageCountAndV1FirstConflict(t *testing.T) {
	s, _ := fileStore(t)
	ctx := context.Background()
	e := manifest(0)
	v1 := FederationEnvelope{Version: 1, OriginNodeID: e.OriginNodeID, OriginMessageID: e.OriginMessageID, SourceRoom: e.SourceRoom, DestinationRoom: e.DestinationRoom, Sender: e.Sender, Text: "text", SentAt: e.SentAt}
	if _, err := s.ImportFederation(ctx, "remote-node", v1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageFederationFiles(ctx, "remote-node", e); !errors.Is(err, ErrFederationConflict) {
		t.Fatal(err)
	}
	for i := 0; i < FederationStageLimit; i++ {
		if _, err := s.StageFederationFiles(ctx, "remote-node", manifest(0)); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.StageFederationFiles(ctx, "remote-node", manifest(0)); !errors.Is(err, ErrFederationQuota) {
		t.Fatal("101st staging accepted", err)
	}
}

func TestFederationFilesOperatorRetryIsAuditedAndDoesNotRenewTTL(t *testing.T) {
	s, _ := fileStore(t)
	ctx := context.Background()
	e := manifest(1)
	ref, _, err := s.RegisterFile(ctx, protocol.FileRef{ID: "file", Name: "file.bin", Size: 1, SHA256: e.Files[0].SHA256}, "ab/blob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SendMessage(ctx, "local", "general", protocol.SendMessageRequest{Files: []string{ref.Ref.ID}}, nil); err != nil {
		t.Fatal(err)
	}
	j, err := s.ClaimFederationFileJob(ctx, false, time.Second)
	if err != nil || j == nil {
		t.Fatal(j, err)
	}
	if err = s.FinishFederationFileJob(ctx, *j, false, "dead", "fixed error", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryFederationOutbox(ctx, j.ID, "operator-retry"); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.RetryFederationOutbox(ctx, j.ID, "operator-retry"); err != nil || !replay.Replayed {
		t.Fatal(replay, err)
	}
	if _, err = s.RetryFederationOutbox(ctx, j.ID, "other"); !errors.Is(err, ErrFederationNotDead) {
		t.Fatal(err)
	}
	var generation, deadline int64
	s.db.QueryRow(`SELECT generation,deadline FROM federation_file_outbox WHERE id=?`, j.ID).Scan(&generation, &deadline)
	if generation != 2 || deadline != j.Deadline {
		t.Fatal("retry mutated semantic TTL")
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE action='federation.outbox_retried'`).Scan(&count)
	if count != 1 {
		t.Fatal("retry duplicate audited twice", count)
	}
	s.db.Exec(`UPDATE federation_file_outbox SET state='dead',deadline=0 WHERE id=?`, j.ID)
	if _, err = s.RetryFederationOutbox(ctx, j.ID, "expired"); !errors.Is(err, ErrFederationExpired) {
		t.Fatal("expired retry renewed", err)
	}
	status, err := s.FederationOperatorStatus(ctx)
	if err != nil || status.Attachments.Dead != 1 {
		t.Fatal(status, err)
	}
	items, err := s.ListFederationOutbox(ctx, "dead", 10)
	if err != nil || len(items) != 1 || items[0].ID != j.ID {
		t.Fatal(items, err)
	}
}

func TestFederationFilesTransferPathsUseOnlyGeneratedIDs(t *testing.T) {
	if !ValidFederationTransferID(protocol.NewID()) {
		t.Fatal("generated transfer id rejected")
	}
	for _, v := range []string{"..", ".", "general", "../general", `..\general`, strings.Repeat(".", 26), strings.Repeat("0", 25) + "/"} {
		if ValidFederationTransferID(v) {
			t.Fatal("unsafe transfer id", v)
		}
	}
}
