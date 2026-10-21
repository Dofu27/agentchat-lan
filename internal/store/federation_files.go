package store

// The attachment protocol deliberately uses separate durable tables and wire
// types. A v1 job can never be re-encoded as v2 (or silently lose attachments).
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

const (
	FederationFileSize      int64 = 32 << 20
	FederationMessageSize   int64 = 64 << 20
	FederationReceiveBudget int64 = 512 << 20
	FederationStageLimit          = 100
	FederationChunkSize     int64 = 4 << 20
)

var ErrFederationExpired = errors.New("federation attachment expired")
var ErrFederationQuota = errors.New("federation attachment budget exhausted")

type FederationFileLink struct {
	PeerID, LocalRoom, RemoteRoom, LinkID string
	Epoch                                 int64
}
type FederationBlob struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Name   string `json:"name"`
	Mime   string `json:"mime,omitempty"`
}
type FederationFileEnvelope struct {
	Version         int               `json:"version"`
	OriginNodeID    string            `json:"origin_node_id"`
	OriginMessageID string            `json:"origin_message_id"`
	SourceRoom      string            `json:"source_room"`
	DestinationRoom string            `json:"destination_room"`
	LinkID          string            `json:"link_id"`
	Epoch           int64             `json:"epoch"`
	Sender          protocol.AgentRef `json:"sender"`
	Text            string            `json:"text"`
	SentAt          time.Time         `json:"sent_at"`
	Files           []FederationBlob  `json:"files"`
}
type FederationFileReceipt struct {
	OriginNodeID    string `json:"origin_node_id"`
	OriginMessageID string `json:"origin_message_id"`
	TransferID      string `json:"transfer_id"`
	State           string `json:"state"`
	LocalMessageID  string `json:"local_message_id,omitempty"`
}
type FederationFileJob struct {
	ID, PeerID, Payload, Lease string
	Attempts                   int
	Deadline                   int64
	Generation                 int64
}
type FederationFileProgress struct {
	Index  int
	Offset int64
	Temp   string
}

// Remote display names are presentation metadata, never paths. Normalize both
// separators identically on every host; preserve the original wire manifest for
// canonical replay/conflict detection.
func federationDisplayName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name))
	if name == "" || name == "." || name == ".." || name == "/" {
		return "attachment.bin"
	}
	return name
}

func (s *Store) migrateFederationFiles(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
 CREATE TABLE IF NOT EXISTS federation_file_links(peer_id TEXT NOT NULL,link_id TEXT NOT NULL,epoch INTEGER NOT NULL CHECK(epoch>0),local_room TEXT NOT NULL,remote_room TEXT NOT NULL,active INTEGER NOT NULL,PRIMARY KEY(peer_id,link_id,epoch));
 CREATE UNIQUE INDEX IF NOT EXISTS federation_file_active_room ON federation_file_links(peer_id,local_room) WHERE active=1;
 CREATE TABLE IF NOT EXISTS federation_file_outbox(id TEXT PRIMARY KEY,peer_id TEXT NOT NULL,origin_node_id TEXT NOT NULL,origin_message_id TEXT NOT NULL,payload TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('pending','inflight','waiting_receipt','delivered','dead')),attempts INTEGER NOT NULL DEFAULT 0,next_attempt INTEGER NOT NULL,lease_until INTEGER NOT NULL DEFAULT 0,lease_token TEXT NOT NULL DEFAULT '',deadline INTEGER NOT NULL,last_error TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,generation INTEGER NOT NULL DEFAULT 1,UNIQUE(origin_node_id,origin_message_id));
 CREATE TABLE IF NOT EXISTS federation_blob_grants(job_id TEXT NOT NULL REFERENCES federation_file_outbox(id) ON DELETE CASCADE,sha256 TEXT NOT NULL,file_id TEXT NOT NULL REFERENCES files(id),size INTEGER NOT NULL,PRIMARY KEY(job_id,sha256));
 CREATE TABLE IF NOT EXISTS federation_file_transfers(id TEXT PRIMARY KEY,peer_id TEXT NOT NULL,origin_node_id TEXT NOT NULL,origin_message_id TEXT NOT NULL,payload TEXT NOT NULL,payload_hash TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('pending','inflight','committed','dead')),local_message_id TEXT NOT NULL DEFAULT '',reserved INTEGER NOT NULL,deadline INTEGER NOT NULL,next_attempt INTEGER NOT NULL,lease_until INTEGER NOT NULL DEFAULT 0,lease_token TEXT NOT NULL DEFAULT '',attempts INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',file_index INTEGER NOT NULL DEFAULT 0,file_offset INTEGER NOT NULL DEFAULT 0,temp_path TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,generation INTEGER NOT NULL DEFAULT 1,UNIQUE(origin_node_id,origin_message_id));
 CREATE TABLE IF NOT EXISTS federation_transfer_blobs(transfer_id TEXT NOT NULL REFERENCES federation_file_transfers(id),sha256 TEXT NOT NULL,file_id TEXT NOT NULL REFERENCES files(id),PRIMARY KEY(transfer_id,sha256));
 CREATE TABLE IF NOT EXISTS federation_received_blobs(peer_id TEXT NOT NULL,sha256 TEXT NOT NULL,file_id TEXT NOT NULL REFERENCES files(id) ON DELETE CASCADE,size INTEGER NOT NULL,PRIMARY KEY(peer_id,sha256));
 CREATE INDEX IF NOT EXISTS federation_file_outbox_due ON federation_file_outbox(state,next_attempt,lease_until);
 CREATE INDEX IF NOT EXISTS federation_file_transfer_due ON federation_file_transfers(state,next_attempt,lease_until);
 INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(4,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
 `)
	return err
}

// Epochs are immutable and retired epochs cannot be reactivated. Configuration
// is an operator action; inbound payloads cannot create links or memberships.
func (s *Store) ConfigureFederationFiles(ctx context.Context, links []FederationFileLink) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	desired := map[string]bool{}
	for _, l := range links {
		if protocol.ValidateAgentName(l.LinkID) != nil || l.Epoch < 1 || desired[l.LocalRoom] {
			return ErrInvalidTarget
		}
		desired[l.LocalRoom] = true
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_room_links l JOIN rooms r ON r.id=l.local_room JOIN federation_peers p ON p.node_id=l.peer_id WHERE l.peer_id=? AND l.local_room=? AND l.remote_room=? AND l.enabled=1 AND p.enabled=1 AND r.kind='room' AND r.archived_at IS NULL`, l.PeerID, l.LocalRoom, l.RemoteRoom).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return ErrForbidden
		}
		var local, remote string
		var active int
		err = tx.QueryRowContext(ctx, `SELECT local_room,remote_room,active FROM federation_file_links WHERE peer_id=? AND link_id=? AND epoch=?`, l.PeerID, l.LinkID, l.Epoch).Scan(&local, &remote, &active)
		if err == nil && (local != l.LocalRoom || remote != l.RemoteRoom || active != 1) {
			return ErrFederationConflict
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	// Retire omitted rows without temporarily violating the active-room index.
	rows, err := tx.QueryContext(ctx, `SELECT peer_id,local_room,remote_room,link_id,epoch FROM federation_file_links WHERE active=1`)
	if err != nil {
		return err
	}
	var old []FederationFileLink
	for rows.Next() {
		var l FederationFileLink
		if err = rows.Scan(&l.PeerID, &l.LocalRoom, &l.RemoteRoom, &l.LinkID, &l.Epoch); err != nil {
			rows.Close()
			return err
		}
		old = append(old, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, l := range old {
		keep := false
		for _, d := range links {
			if d == l {
				keep = true
			}
		}
		if !keep {
			if _, err = tx.ExecContext(ctx, `UPDATE federation_file_links SET active=0 WHERE peer_id=? AND link_id=? AND epoch=?`, l.PeerID, l.LinkID, l.Epoch); err != nil {
				return err
			}
		}
	}
	for _, l := range links {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO federation_file_links VALUES(?,?,?,?,?,1)`, l.PeerID, l.LinkID, l.Epoch, l.LocalRoom, l.RemoteRoom); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err == nil {
		s.federationFiles = len(links) > 0
	}
	return err
}
func ValidFederationSHA(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && v == strings.ToLower(v)
}
func ValidFederationTransferID(v string) bool {
	if len(v) != 26 {
		return false
	}
	for _, r := range v {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", r) {
			return false
		}
	}
	return true
}
func (e FederationFileEnvelope) Validate(peer string) error {
	if e.Version != 2 || e.OriginNodeID != peer || protocol.ValidateAgentName(peer) != nil || protocol.ValidateAgentName(e.OriginMessageID) != nil || protocol.ValidateAgentName(e.SourceRoom) != nil || protocol.ValidateAgentName(e.DestinationRoom) != nil || protocol.ValidateAgentName(e.LinkID) != nil || e.Epoch < 1 || protocol.ValidateAgentName(e.Sender.Name) != nil || len(e.Sender.ID) < 1 || len(e.Sender.ID) > 128 || len(e.Sender.Kind) > 64 || e.SentAt.IsZero() || len(e.Text) > protocol.MaxTextBytes || len(e.Files) < 1 || len(e.Files) > 8 {
		return ErrInvalidTarget
	}
	var total int64
	seen := map[string]bool{}
	for _, b := range e.Files {
		if !ValidFederationSHA(b.SHA256) || b.Size < 0 || b.Size > FederationFileSize || len(b.Name) > 255 || strings.ContainsAny(b.Name, "\x00\r\n") || len(b.Mime) > 255 || strings.ContainsAny(b.Mime, "\x00\r\n") || seen[b.SHA256] {
			return ErrInvalidTarget
		}
		seen[b.SHA256] = true
		total += b.Size
	}
	if total > FederationMessageSize {
		return ErrInvalidTarget
	}
	return nil
}
func (s *Store) enqueueFederationFiles(ctx context.Context, tx *sql.Tx, m protocol.Message) error {
	if !s.federationFiles {
		return nil
	}
	var l FederationFileLink
	var node string
	err := tx.QueryRowContext(ctx, `SELECT f.peer_id,f.local_room,f.remote_room,f.link_id,f.epoch,n.node_id FROM federation_file_links f JOIN federation_room_links l ON l.peer_id=f.peer_id AND l.local_room=f.local_room AND l.remote_room=f.remote_room JOIN federation_peers p ON p.node_id=l.peer_id CROSS JOIN federation_node n WHERE f.active=1 AND l.enabled=1 AND p.enabled=1 AND f.local_room=?`, m.Room).Scan(&l.PeerID, &l.LocalRoom, &l.RemoteRoom, &l.LinkID, &l.Epoch, &node)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	e := FederationFileEnvelope{Version: 2, OriginNodeID: node, OriginMessageID: m.ID, SourceRoom: m.Room, DestinationRoom: l.RemoteRoom, LinkID: l.LinkID, Epoch: l.Epoch, Sender: m.From, Text: m.Text, SentAt: m.TS}
	for _, f := range m.Files {
		e.Files = append(e.Files, FederationBlob{f.SHA256, f.Size, f.Name, f.Mime})
	}
	if err = e.Validate(node); err != nil {
		return err
	}
	var n int
	// Reclaim only expired terminal history. Active grants and retryable dead
	// jobs remain pinned; a full live queue rejects the local send atomically.
	if _, err = tx.ExecContext(ctx, `DELETE FROM federation_file_outbox WHERE state IN ('delivered','dead') AND deadline<=?`, time.Now().UnixMilli()); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_file_outbox`).Scan(&n); err != nil {
		return err
	}
	if n >= s.federation.MaxOutbox {
		return ErrFederationFull
	}
	raw, _ := json.Marshal(e)
	now := time.Now()
	id := protocol.NewID()
	if _, err = tx.ExecContext(ctx, `INSERT INTO federation_file_outbox(id,peer_id,origin_node_id,origin_message_id,payload,state,next_attempt,deadline,created_at) VALUES(?,?,?,?,?,'pending',?,?,?)`, id, l.PeerID, node, m.ID, string(raw), now.UnixMilli(), now.Add(24*time.Hour).UnixMilli(), now.UnixMilli()); err != nil {
		return err
	}
	for _, f := range m.Files {
		if _, err = tx.ExecContext(ctx, `INSERT INTO federation_blob_grants VALUES(?,?,?,?)`, id, f.SHA256, f.ID, f.Size); err != nil {
			return err
		}
	}
	return nil
}

func fileLinkAuthorized(ctx context.Context, tx *sql.Tx, peer string, e FederationFileEnvelope, inbound bool) error {
	local, remote := e.SourceRoom, e.DestinationRoom
	if inbound {
		local, remote = remote, local
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_file_links f JOIN federation_room_links l ON l.peer_id=f.peer_id AND l.local_room=f.local_room AND l.remote_room=f.remote_room JOIN rooms r ON r.id=l.local_room JOIN federation_peers p ON p.node_id=l.peer_id CROSS JOIN federation_node n JOIN agents a ON a.id=n.bridge_agent_id WHERE f.peer_id=? AND f.link_id=? AND f.epoch=? AND f.local_room=? AND f.remote_room=? AND f.active=1 AND l.enabled=1 AND p.enabled=1 AND r.kind='room' AND r.archived_at IS NULL AND a.revoked_at IS NULL`, peer, e.LinkID, e.Epoch, local, remote).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrForbidden
	}
	return nil
}
func (s *Store) StageFederationFiles(ctx context.Context, peer string, e FederationFileEnvelope, generations ...int64) (FederationFileReceipt, error) {
	generation := int64(1)
	if len(generations) > 0 {
		generation = generations[0]
	}
	if generation < 1 || generation > 1000000000 {
		return FederationFileReceipt{}, ErrInvalidTarget
	}
	r := FederationFileReceipt{OriginNodeID: e.OriginNodeID, OriginMessageID: e.OriginMessageID}
	if err := e.Validate(peer); err != nil {
		return r, err
	}
	raw, _ := json.Marshal(e)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if !s.federationFiles {
		return r, ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	if err = fileLinkAuthorized(ctx, tx, peer, e, true); err != nil {
		return r, err
	}
	var oldHash string
	var oldGeneration, deadline int64
	err = tx.QueryRowContext(ctx, `SELECT id,state,local_message_id,payload_hash,generation,deadline FROM federation_file_transfers WHERE origin_node_id=? AND origin_message_id=?`, peer, e.OriginMessageID).Scan(&r.TransferID, &r.State, &r.LocalMessageID, &oldHash, &oldGeneration, &deadline)
	if err == nil {
		if oldHash != hash || (generation < oldGeneration && r.State != "committed") {
			return r, ErrFederationConflict
		}
		if generation > oldGeneration && r.State != "committed" {
			if deadline <= time.Now().UnixMilli() {
				return r, ErrFederationExpired
			}
			state := r.State
			if state == "dead" {
				state = "pending"
			}
			if _, err = tx.ExecContext(ctx, `UPDATE federation_file_transfers SET generation=?,state=?,attempts=0,next_attempt=? WHERE id=?`, generation, state, time.Now().UnixMilli(), r.TransferID); err != nil {
				return r, err
			}
			r.State = state
			return r, tx.Commit()
		}
		return r, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_inbox WHERE origin_node_id=? AND origin_message_id=?`, peer, e.OriginMessageID).Scan(&n); err != nil {
		return r, err
	}
	if n != 0 {
		return r, ErrFederationConflict
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_file_transfers WHERE peer_id=? AND state IN ('pending','inflight')`, peer).Scan(&n); err != nil {
		return r, err
	}
	if n >= FederationStageLimit {
		return r, ErrFederationQuota
	}
	// Conservative reservation includes a second copy for crash-safe range resume.
	// Verified provenance remains charged until ordinary file GC removes its blob.
	var size, used int64
	for _, b := range e.Files {
		size += b.Size
	}
	size *= 2
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT COALESCE(SUM(reserved),0) FROM federation_file_transfers)+(SELECT COALESCE(SUM(size),0) FROM federation_received_blobs)`).Scan(&used); err != nil {
		return r, err
	}
	if used+size > FederationReceiveBudget {
		return r, ErrFederationQuota
	}
	now := time.Now()
	r.TransferID = protocol.NewID()
	r.State = "pending"
	_, err = tx.ExecContext(ctx, `INSERT INTO federation_file_transfers(id,peer_id,origin_node_id,origin_message_id,payload,payload_hash,state,reserved,deadline,next_attempt,created_at,generation) VALUES(?,?,?,?,?,?,'pending',?,?,?,?,?)`, r.TransferID, peer, peer, e.OriginMessageID, string(raw), hash, size, now.Add(24*time.Hour).UnixMilli(), now.UnixMilli(), now.UnixMilli(), generation)
	if err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func (s *Store) ClaimFederationFileJob(ctx context.Context, inbound bool, lease time.Duration) (*FederationFileJob, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if !s.federationFiles {
		return nil, nil
	}
	table := "federation_file_outbox"
	if inbound {
		table = "federation_file_transfers"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET state='dead',lease_token='',lease_until=0,last_error='expired' WHERE state NOT IN ('delivered','committed','dead') AND deadline<=?`, now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET state='pending',lease_token='',lease_until=0 WHERE state='inflight' AND lease_until<=?`, now); err != nil {
		return nil, err
	}
	var j FederationFileJob
	err = tx.QueryRowContext(ctx, `SELECT id,peer_id,payload,attempts,deadline,generation FROM `+table+` WHERE state IN ('pending','waiting_receipt') AND next_attempt<=? ORDER BY created_at,id LIMIT 1`, now).Scan(&j.ID, &j.PeerID, &j.Payload, &j.Attempts, &j.Deadline, &j.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	j.Lease = protocol.NewID()
	if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET state='inflight',lease_token=?,lease_until=? WHERE id=?`, j.Lease, now+lease.Milliseconds(), j.ID); err != nil {
		return nil, err
	}
	return &j, tx.Commit()
}

// A healthy 202 is waiting_receipt, not success and not a failed attempt.
func (s *Store) FinishFederationFileJob(ctx context.Context, j FederationFileJob, inbound bool, state, reason string, next time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	table := "federation_file_outbox"
	if inbound {
		table = "federation_file_transfers"
	}
	if state != "pending" && state != "waiting_receipt" && state != "delivered" && state != "dead" {
		return ErrInvalidTarget
	}
	if inbound && (state == "waiting_receipt" || state == "delivered") {
		return ErrInvalidTarget
	}
	attempts := 0
	if reason != "" {
		attempts = j.Attempts + 1
	}
	if attempts >= s.federation.MaxAttempts || time.Now().UnixMilli() >= j.Deadline {
		state = "dead"
	}
	// reason comes only from fixed worker vocabulary, never a remote body/path.
	_, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET state=?,attempts=?,next_attempt=?,lease_until=0,lease_token='',last_error=? WHERE id=? AND state='inflight' AND lease_token=? AND lease_until>?`, state, attempts, next.UnixMilli(), reason, j.ID, j.Lease, time.Now().UnixMilli())
	return err
}
func (s *Store) FederationFileAuthorized(ctx context.Context, peer string, e FederationFileEnvelope, inbound bool) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if !s.federationFiles {
		return ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fileLinkAuthorized(ctx, tx, peer, e, inbound)
}

// The grant names an exact source message, link epoch, hash AND size. Knowing a
// content hash or possessing a normal agent/admin token never authorizes pulls.
func (s *Store) FederationBlobGrant(ctx context.Context, peer, origin, message, link string, epoch int64, sha string, generation int64) (StoredFile, error) {
	var f StoredFile
	if !s.federationFiles || !ValidFederationSHA(sha) {
		return f, ErrForbidden
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT o.payload,f.id,f.name,f.sha256,f.size,f.mime,f.storage_path FROM federation_file_outbox o JOIN federation_blob_grants g ON g.job_id=o.id JOIN files f ON f.id=g.file_id WHERE o.peer_id=? AND o.origin_node_id=? AND o.origin_message_id=? AND g.sha256=? AND o.state IN ('pending','inflight','waiting_receipt') AND o.deadline>? AND o.generation=?`, peer, origin, message, sha, time.Now().UnixMilli(), generation).Scan(&payload, &f.Ref.ID, &f.Ref.Name, &f.Ref.SHA256, &f.Ref.Size, &f.Ref.Mime, &f.RelativePath)
	if err != nil {
		return f, ErrForbidden
	}
	var e FederationFileEnvelope
	if json.Unmarshal([]byte(payload), &e) != nil || e.LinkID != link || e.Epoch != epoch {
		return f, ErrForbidden
	}
	if err = s.FederationFileAuthorized(ctx, peer, e, false); err != nil {
		return f, err
	}
	return f, nil
}

func (s *Store) FederationFileCheckpoint(ctx context.Context, j FederationFileJob, p *FederationFileProgress, lease time.Duration) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if p == nil {
		return ErrInvalidTarget
	}
	result, err := s.db.ExecContext(ctx, `UPDATE federation_file_transfers SET file_index=?,file_offset=?,temp_path=?,lease_until=? WHERE id=? AND state='inflight' AND lease_token=? AND lease_until>? AND deadline>?`, p.Index, p.Offset, p.Temp, time.Now().Add(lease).UnixMilli(), j.ID, j.Lease, time.Now().UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrFederationExpired
	}
	return nil
}
func (s *Store) FederationFileProgress(ctx context.Context, j FederationFileJob) (FederationFileProgress, error) {
	var p FederationFileProgress
	err := s.db.QueryRowContext(ctx, `SELECT file_index,file_offset,temp_path FROM federation_file_transfers WHERE id=? AND state='inflight' AND lease_token=?`, j.ID, j.Lease).Scan(&p.Index, &p.Offset, &p.Temp)
	return p, err
}

// Registering a verified blob and pinning it are one transaction. A concurrent
// GC can never observe it as an unreferenced ordinary upload.
func (s *Store) FederationFileReady(ctx context.Context, j FederationFileJob, b FederationBlob, path string, index int, lease time.Duration) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_file_transfers WHERE id=? AND state='inflight' AND lease_token=? AND lease_until>? AND deadline>?`, j.ID, j.Lease, time.Now().UnixMilli(), time.Now().UnixMilli()).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrFederationExpired
	}
	id := "fil_" + protocol.NewID()
	_, err = tx.ExecContext(ctx, `INSERT INTO files(id,name,sha256,size,mime,storage_path,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(sha256) DO NOTHING`, id, federationDisplayName(b.Name), b.SHA256, b.Size, "application/octet-stream", path, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	var size int64
	if err = tx.QueryRowContext(ctx, `SELECT id,size FROM files WHERE sha256=?`, b.SHA256).Scan(&id, &size); err != nil {
		return err
	}
	if size != b.Size {
		return ErrFederationConflict
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO federation_transfer_blobs VALUES(?,?,?)`, j.ID, b.SHA256, id)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO federation_received_blobs VALUES(?,?,?,?)`, j.PeerID, b.SHA256, id, b.Size); err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	discount := int64(0)
	if affected == 1 {
		discount = b.Size
	}
	if _, err = tx.ExecContext(ctx, `UPDATE federation_file_transfers SET file_index=?,file_offset=0,temp_path='',lease_until=?,reserved=MAX(0,reserved-?) WHERE id=?`, index+1, time.Now().Add(lease).UnixMilli(), discount, j.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) CommitFederationFiles(ctx context.Context, j FederationFileJob, e FederationFileEnvelope) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !s.federationFiles {
		return ErrForbidden
	}
	if err = fileLinkAuthorized(ctx, tx, j.PeerID, e, true); err != nil {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_file_transfers WHERE id=? AND state='inflight' AND lease_token=? AND lease_until>? AND deadline>? AND file_index=?`, j.ID, j.Lease, time.Now().UnixMilli(), time.Now().UnixMilli(), len(e.Files)).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrFederationExpired
	}
	var bridge string
	if err = tx.QueryRowContext(ctx, `SELECT bridge_agent_id FROM federation_node`).Scan(&bridge); err != nil {
		return err
	}
	var seq int64
	if err = tx.QueryRowContext(ctx, `UPDATE rooms SET next_seq=next_seq+1 WHERE id=? RETURNING next_seq`, e.DestinationRoom).Scan(&seq); err != nil {
		return err
	}
	meta, _ := json.Marshal(protocol.MessageMeta{Federation: &protocol.FederationMeta{OriginNodeID: e.OriginNodeID, OriginMessageID: e.OriginMessageID, OriginRoom: e.SourceRoom, Sender: e.Sender, OriginTime: e.SentAt}})
	id := protocol.NewID()
	now := time.Now()
	if _, err = tx.ExecContext(ctx, `INSERT INTO messages(id,room_id,seq,sender_id,kind,text,meta_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, id, e.DestinationRoom, seq, bridge, protocol.KindChat, e.Text, string(meta), now.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	for _, b := range e.Files {
		var fileID string
		if err = tx.QueryRowContext(ctx, `SELECT t.file_id FROM federation_transfer_blobs t JOIN files f ON f.id=t.file_id WHERE t.transfer_id=? AND t.sha256=? AND f.size=?`, j.ID, b.SHA256, b.Size).Scan(&fileID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO attachments VALUES(?,?,?)`, id, fileID, federationDisplayName(b.Name)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO federation_inbox SELECT origin_node_id,origin_message_id,?,payload_hash,? FROM federation_file_transfers WHERE id=?`, id, now.UnixMilli(), j.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE federation_file_transfers SET state='committed',local_message_id=?,lease_until=0,lease_token='',reserved=0 WHERE id=?`, id, j.ID); err != nil {
		return err
	}
	return tx.Commit()
}
