package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

var ErrFederationFull = errors.New("federation outbox is full")
var ErrFederationConflict = errors.New("conflicting federation delivery")

type FederationLimits struct{ MaxOutbox, MaxAttempts int }
type FederationPeer struct{ NodeID, BaseURL, SecretHash string }
type FederationLink struct{ PeerID, LocalRoom, RemoteRoom string }
type FederationEnvelope struct {
	Version         int               `json:"version"`
	OriginNodeID    string            `json:"origin_node_id"`
	OriginMessageID string            `json:"origin_message_id"`
	SourceRoom      string            `json:"source_room"`
	DestinationRoom string            `json:"destination_room"`
	Sender          protocol.AgentRef `json:"sender"`
	Text            string            `json:"text"`
	SentAt          time.Time         `json:"sent_at"`
}
type FederationReceipt struct {
	OriginNodeID    string `json:"origin_node_id"`
	OriginMessageID string `json:"origin_message_id"`
	LocalMessageID  string `json:"local_message_id"`
	Replayed        bool   `json:"replayed"`
}
type FederationJob struct {
	ID, PeerID, Payload, Lease string
	Attempts, MaxAttempts      int
}
type FederationQueueStats struct {
	Pending   int `json:"pending"`
	Inflight  int `json:"inflight"`
	Delivered int `json:"delivered"`
	Dead      int `json:"dead"`
	Attempts  int `json:"attempts"`
}

func (s *Store) migrateFederation(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// v0.8 shipped an early federation schema with overlapping table names.
	// It was never enabled on production, but remove those empty legacy tables
	// before creating the durable schema. Non-empty legacy data requires a
	// manual migration instead of silent destruction.
	legacyRows, err := tx.QueryContext(ctx, `PRAGMA table_info(federation_peers)`)
	if err != nil {
		return err
	}
	hasLegacyID, hasNodeID := false, false
	for legacyRows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := legacyRows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			legacyRows.Close()
			return err
		}
		if name == "id" {
			hasLegacyID = true
		}
		if name == "node_id" {
			hasNodeID = true
		}
	}
	legacyRows.Close()
	if hasLegacyID && !hasNodeID {
		var legacyCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_peers`).Scan(&legacyCount); err != nil {
			return err
		}
		if legacyCount != 0 {
			return errors.New("legacy v0.8 federation data requires manual migration")
		}
		for _, table := range []string{"federation_inbox", "federation_outbox", "federation_links", "federation_peers", "federation_node"} {
			if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+table); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS federation_node(singleton INTEGER PRIMARY KEY CHECK(singleton=1), node_id TEXT NOT NULL UNIQUE, bridge_agent_id TEXT NOT NULL REFERENCES agents(id));
CREATE TABLE IF NOT EXISTS federation_peers(node_id TEXT PRIMARY KEY, base_url TEXT NOT NULL, secret_hash TEXT NOT NULL, enabled INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS federation_room_links(peer_id TEXT NOT NULL REFERENCES federation_peers(node_id), local_room TEXT NOT NULL REFERENCES rooms(id), remote_room TEXT NOT NULL, enabled INTEGER NOT NULL, PRIMARY KEY(peer_id,local_room), UNIQUE(peer_id,remote_room));
CREATE TABLE IF NOT EXISTS federation_outbox(id TEXT PRIMARY KEY, peer_id TEXT NOT NULL REFERENCES federation_peers(node_id), origin_node_id TEXT NOT NULL, origin_message_id TEXT NOT NULL, payload TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('pending','inflight','delivered','dead')), attempts INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL, next_attempt INTEGER NOT NULL, lease_until INTEGER NOT NULL DEFAULT 0, lease_token TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, UNIQUE(peer_id,origin_node_id,origin_message_id));
CREATE INDEX IF NOT EXISTS federation_outbox_due ON federation_outbox(state,next_attempt,lease_until);
CREATE TABLE IF NOT EXISTS federation_inbox(origin_node_id TEXT NOT NULL, origin_message_id TEXT NOT NULL, local_message_id TEXT NOT NULL, payload_hash TEXT NOT NULL, received_at INTEGER NOT NULL, PRIMARY KEY(origin_node_id,origin_message_id));
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(2,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
CREATE TABLE IF NOT EXISTS federation_operator_retries(job_id TEXT NOT NULL, key_hash TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(job_id,key_hash));
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(3,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
`)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.migrateFederationFiles(ctx)
}

// FederationIdentity explicitly provisions one stable node and an unloggable
// local sender. Its random token is discarded; no agent credential is shared.
func (s *Store) FederationIdentity(ctx context.Context) (string, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, "SELECT node_id FROM federation_node WHERE singleton=1").Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = "node_" + protocol.NewID()
	agentID := "fed_" + protocol.NewID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO agents(id,name,kind,token_hash,status,created_at,last_seen) VALUES(?,?,?,?,?,?,?)`, agentID, "federation-"+id, "federation", hashToken(protocol.NewToken("fed")), protocol.StatusOffline, now, now)
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO federation_node VALUES(1,?,?)", id, agentID); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// ConfigureFederation is explicit startup opt-in. Persisted peer/link rows alone
// never turn on enqueueing after an ordinary process restart.
func (s *Store) ConfigureFederation(ctx context.Context, peers []FederationPeer, links []FederationLink, limits FederationLimits) (string, error) {
	if len(peers) != 1 || limits.MaxOutbox < 1 || limits.MaxOutbox > 10000 || limits.MaxAttempts < 1 || limits.MaxAttempts > 20 {
		return "", errors.New("PoC requires one peer and bounded outbox/attempt limits")
	}
	id, err := s.FederationIdentity(ctx)
	if err != nil {
		return "", err
	}
	p := peers[0]
	if p.NodeID == id || protocol.ValidateAgentName(p.NodeID) != nil || len(p.SecretHash) != 64 {
		return "", errors.New("invalid federation peer identity")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var reusedCredential int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM agents WHERE token_hash=?", p.SecretHash).Scan(&reusedCredential); err != nil {
		return "", err
	}
	if reusedCredential != 0 {
		return "", errors.New("federation secret must not reuse an agent token")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE federation_peers SET enabled=0"); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM federation_room_links"); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO federation_peers VALUES(?,?,?,1) ON CONFLICT(node_id) DO UPDATE SET base_url=excluded.base_url,secret_hash=excluded.secret_hash,enabled=1`, p.NodeID, p.BaseURL, p.SecretHash); err != nil {
		return "", err
	}
	for _, l := range links {
		if l.PeerID != p.NodeID || protocol.ValidateAgentName(l.LocalRoom) != nil || protocol.ValidateAgentName(l.RemoteRoom) != nil {
			return "", errors.New("invalid federation room link")
		}
		var kind string
		if err = tx.QueryRowContext(ctx, "SELECT kind FROM rooms WHERE id=? AND archived_at IS NULL", l.LocalRoom).Scan(&kind); err != nil || kind != "room" {
			return "", errors.New("federation links require an active ordinary local room")
		}
		// Retired mappings must not silently retarget previously queued messages.
		_, err = tx.ExecContext(ctx, `INSERT INTO federation_room_links VALUES(?,?,?,1) ON CONFLICT(peer_id,local_room) DO UPDATE SET remote_room=excluded.remote_room,enabled=1`, l.PeerID, l.LocalRoom, l.RemoteRoom)
		if err != nil {
			return "", err
		}
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO memberships(room_id,agent_id,role,joined_at) SELECT ?,bridge_agent_id,'member',? FROM federation_node WHERE singleton=1`, l.LocalRoom, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	s.federation = &limits
	return id, nil
}

func (s *Store) enqueueFederation(ctx context.Context, tx *sql.Tx, m protocol.Message, kind string) error {
	if s.federation == nil || kind != "room" || m.Meta.Activity != nil || m.Meta.Federation != nil || m.From.Kind == "federation" || m.From.Kind == "bridge" || m.From.Kind == "system" {
		return nil
	}
	if len(m.Files) > 0 {
		return s.enqueueFederationFiles(ctx, tx, m)
	}
	rows, err := tx.QueryContext(ctx, `SELECT l.peer_id,l.remote_room,n.node_id FROM federation_room_links l JOIN federation_peers p ON p.node_id=l.peer_id CROSS JOIN federation_node n WHERE l.local_room=? AND l.enabled=1 AND p.enabled=1`, m.Room)
	if err != nil {
		return err
	}
	type target struct{ peer, room, node string }
	var targets []target
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.peer, &t.room, &t.node); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	// Bound all rows, including history: trim only terminal rows, never pending work.
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM federation_outbox").Scan(&count); err != nil {
		return err
	}
	if count+len(targets) > s.federation.MaxOutbox {
		_, err = tx.ExecContext(ctx, `DELETE FROM federation_outbox WHERE id IN (SELECT id FROM federation_outbox WHERE state IN ('delivered','dead') ORDER BY created_at,id LIMIT ?)`, count+len(targets)-s.federation.MaxOutbox)
		if err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM federation_outbox").Scan(&count); err != nil {
			return err
		}
		if count+len(targets) > s.federation.MaxOutbox {
			return ErrFederationFull
		}
	}
	for _, t := range targets {
		e := FederationEnvelope{Version: 1, OriginNodeID: t.node, OriginMessageID: m.ID, SourceRoom: m.Room, DestinationRoom: t.room, Sender: m.From, Text: m.Text, SentAt: m.TS}
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		_, err = tx.ExecContext(ctx, `INSERT INTO federation_outbox(id,peer_id,origin_node_id,origin_message_id,payload,state,max_attempts,next_attempt,created_at) VALUES(?,?,?,?,?,'pending',?,?,?)`, protocol.NewID(), t.peer, t.node, m.ID, string(raw), s.federation.MaxAttempts, now, now)
		if err != nil {
			return err
		}
	}
	return nil
}

// ImportFederation is a separate transaction path: it NEVER calls SendMessage
// or enqueueFederation, so a remote message cannot be forwarded back out.
func (s *Store) ImportFederation(ctx context.Context, peer string, e FederationEnvelope) (FederationReceipt, error) {
	receipt := FederationReceipt{OriginNodeID: e.OriginNodeID, OriginMessageID: e.OriginMessageID}
	if e.Version != 1 || e.OriginNodeID != peer || protocol.ValidateAgentName(e.OriginNodeID) != nil || protocol.ValidateAgentName(e.OriginMessageID) != nil || protocol.ValidateAgentName(e.SourceRoom) != nil || protocol.ValidateAgentName(e.DestinationRoom) != nil || protocol.ValidateAgentName(e.Sender.Name) != nil || len(e.Sender.ID) < 1 || len(e.Sender.ID) > 128 || len(e.Sender.Kind) > 64 || strings.TrimSpace(e.Text) == "" || len(e.Text) > protocol.MaxTextBytes || e.SentAt.IsZero() {
		return receipt, ErrInvalidTarget
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return receipt, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.federation == nil {
		return receipt, ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	var bridge string
	err = tx.QueryRowContext(ctx, `SELECT n.bridge_agent_id FROM federation_node n JOIN federation_peers p ON p.node_id=? JOIN federation_room_links l ON l.peer_id=p.node_id JOIN rooms r ON r.id=l.local_room JOIN agents a ON a.id=n.bridge_agent_id WHERE n.node_id<>? AND p.enabled=1 AND l.enabled=1 AND l.local_room=? AND l.remote_room=? AND r.archived_at IS NULL AND r.kind='room' AND a.revoked_at IS NULL`, peer, e.OriginNodeID, e.DestinationRoom, e.SourceRoom).Scan(&bridge)
	if errors.Is(err, sql.ErrNoRows) {
		return receipt, ErrForbidden
	}
	if err != nil {
		return receipt, err
	}
	var staged int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_file_transfers WHERE origin_node_id=? AND origin_message_id=?`, e.OriginNodeID, e.OriginMessageID).Scan(&staged); err != nil {
		return receipt, err
	}
	if staged > 0 {
		return receipt, ErrFederationConflict
	}
	var existingHash string
	err = tx.QueryRowContext(ctx, "SELECT local_message_id,payload_hash FROM federation_inbox WHERE origin_node_id=? AND origin_message_id=?", e.OriginNodeID, e.OriginMessageID).Scan(&receipt.LocalMessageID, &existingHash)
	if err == nil {
		if existingHash != digest {
			return receipt, ErrFederationConflict
		}
		receipt.Replayed = true
		return receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return receipt, err
	}
	var seq int64
	if err = tx.QueryRowContext(ctx, "UPDATE rooms SET next_seq=next_seq+1 WHERE id=? RETURNING next_seq", e.DestinationRoom).Scan(&seq); err != nil {
		return receipt, err
	}
	meta, err := json.Marshal(protocol.MessageMeta{Federation: &protocol.FederationMeta{OriginNodeID: e.OriginNodeID, OriginMessageID: e.OriginMessageID, OriginRoom: e.SourceRoom, Sender: e.Sender, OriginTime: e.SentAt}})
	if err != nil {
		return receipt, err
	}
	receipt.LocalMessageID = protocol.NewID()
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO messages(id,room_id,seq,sender_id,kind,text,meta_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, receipt.LocalMessageID, e.DestinationRoom, seq, bridge, protocol.KindChat, e.Text, string(meta), now.Format(time.RFC3339Nano))
	if err != nil {
		return receipt, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO federation_inbox VALUES(?,?,?,?,?)", e.OriginNodeID, e.OriginMessageID, receipt.LocalMessageID, digest, now.UnixMilli())
	if err != nil {
		return receipt, err
	}
	return receipt, tx.Commit()
}

// ClaimFederationJob atomically recovers expired leases and claims at most one
// job. A lease token prevents a late worker from completing a newer attempt.
func (s *Store) ClaimFederationJob(ctx context.Context, now time.Time, lease time.Duration) (*FederationJob, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.federation == nil {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE federation_outbox SET state=CASE WHEN attempts>=max_attempts THEN 'dead' ELSE 'pending' END,lease_token='',lease_until=0 WHERE state='inflight' AND lease_until<=?`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	var job FederationJob
	err = tx.QueryRowContext(ctx, `SELECT id,peer_id,payload,attempts,max_attempts FROM federation_outbox WHERE state='pending' AND next_attempt<=? ORDER BY created_at,id LIMIT 1`, now.UnixMilli()).Scan(&job.ID, &job.PeerID, &job.Payload, &job.Attempts, &job.MaxAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	job.Lease = protocol.NewID()
	job.Attempts++
	_, err = tx.ExecContext(ctx, "UPDATE federation_outbox SET state='inflight',attempts=?,lease_token=?,lease_until=? WHERE id=?", job.Attempts, job.Lease, now.Add(lease).UnixMilli(), job.ID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *Store) FinishFederationJob(ctx context.Context, j FederationJob, delivered, permanent bool, next time.Time, reason string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	state := "pending"
	if delivered {
		state = "delivered"
	} else if permanent || j.Attempts >= j.MaxAttempts {
		state = "dead"
	}
	// Reasons are fixed error classes/statuses, never remote bodies or credentials.
	if len(reason) > 128 {
		reason = reason[:128]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE federation_outbox SET state=?,next_attempt=?,lease_until=0,lease_token='',last_error=? WHERE id=? AND state='inflight' AND lease_token=?`, state, next.UnixMilli(), reason, j.ID, j.Lease)
	return err
}

func (s *Store) FederationJobAuthorized(ctx context.Context, j FederationJob, e FederationEnvelope) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_room_links l JOIN federation_peers p ON p.node_id=l.peer_id JOIN rooms r ON r.id=l.local_room WHERE p.node_id=? AND p.enabled=1 AND l.enabled=1 AND l.local_room=? AND l.remote_room=? AND r.kind='room' AND r.archived_at IS NULL`, j.PeerID, e.SourceRoom, e.DestinationRoom).Scan(&n)
	return n == 1, err
}
func (s *Store) FederationStats(ctx context.Context) (FederationQueueStats, error) {
	var stats FederationQueueStats
	rows, err := s.db.QueryContext(ctx, "SELECT state,COUNT(*),SUM(attempts) FROM federation_outbox GROUP BY state")
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n, attempts int
		if err = rows.Scan(&state, &n, &attempts); err != nil {
			return stats, err
		}
		stats.Attempts += attempts
		switch state {
		case "pending":
			stats.Pending = n
		case "inflight":
			stats.Inflight = n
		case "delivered":
			stats.Delivered = n
		case "dead":
			stats.Dead = n
		default:
			return stats, fmt.Errorf("invalid federation state")
		}
	}
	return stats, rows.Err()
}
