package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

// Configuration is approval of a stable two-end identity pair, not a directory
// lookup by display name and not permission to export an existing local DM.
type FederationDMConversation struct {
	Typing         bool   `json:"typing,omitempty"`
	ConversationID string `json:"conversation_id"`
	LinkID         string `json:"link_id"`
	Epoch          int64  `json:"epoch"`
	LocalAgentID   string `json:"local_agent_id"`
	RemoteAgentID  string `json:"remote_agent_id"`
}
type FederationDMEnvelope struct {
	Version          int               `json:"version"`
	Type             string            `json:"type"`
	OriginNodeID     string            `json:"origin_node_id"`
	OriginMessageID  string            `json:"origin_message_id"`
	ConversationID   string            `json:"conversation_id"`
	LinkID           string            `json:"link_id"`
	Epoch            int64             `json:"epoch"`
	RecipientNodeID  string            `json:"recipient_node_id"`
	RecipientAgentID string            `json:"recipient_agent_id"`
	Sender           protocol.AgentRef `json:"sender"`
	Text             string            `json:"text"`
	SentAt           time.Time         `json:"sent_at"`
}
type FederationDMContact struct {
	ConversationID string `json:"conversation_id"`
	PeerNodeID     string `json:"peer_node_id"`
	RemoteAgentID  string `json:"remote_agent_id"`
	Epoch          int64  `json:"epoch"`
	RoomID         string `json:"room_id"`
}

func (s *Store) migrateFederationDM(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
 CREATE TABLE IF NOT EXISTS federation_principals(node_id TEXT NOT NULL,agent_id TEXT NOT NULL,PRIMARY KEY(node_id,agent_id));
 CREATE TABLE IF NOT EXISTS federation_dm_conversations(peer_id TEXT NOT NULL REFERENCES federation_peers(node_id),conversation_id TEXT NOT NULL,link_id TEXT NOT NULL,epoch INTEGER NOT NULL CHECK(epoch>0),local_room TEXT NOT NULL UNIQUE REFERENCES rooms(id),bridge_agent_id TEXT NOT NULL UNIQUE REFERENCES agents(id),active INTEGER NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(peer_id,conversation_id,epoch));
 CREATE UNIQUE INDEX IF NOT EXISTS federation_dm_active_conversation ON federation_dm_conversations(peer_id,conversation_id) WHERE active=1;
 CREATE TABLE IF NOT EXISTS federation_dm_members(peer_id TEXT NOT NULL,conversation_id TEXT NOT NULL,epoch INTEGER NOT NULL,side TEXT NOT NULL CHECK(side IN ('local','remote')),node_id TEXT NOT NULL,agent_id TEXT NOT NULL,PRIMARY KEY(peer_id,conversation_id,epoch,side),FOREIGN KEY(peer_id,conversation_id,epoch) REFERENCES federation_dm_conversations(peer_id,conversation_id,epoch),FOREIGN KEY(node_id,agent_id) REFERENCES federation_principals(node_id,agent_id));
 CREATE TABLE IF NOT EXISTS federation_dm_outbox(id TEXT PRIMARY KEY,peer_id TEXT NOT NULL REFERENCES federation_peers(node_id),origin_node_id TEXT NOT NULL,origin_message_id TEXT NOT NULL,payload TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('pending','inflight','delivered','dead')),attempts INTEGER NOT NULL DEFAULT 0,max_attempts INTEGER NOT NULL,next_attempt INTEGER NOT NULL,lease_until INTEGER NOT NULL DEFAULT 0,lease_token TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,UNIQUE(peer_id,origin_node_id,origin_message_id));
 CREATE INDEX IF NOT EXISTS federation_dm_outbox_due ON federation_dm_outbox(state,next_attempt,lease_until);
 CREATE VIEW IF NOT EXISTS federation_dm_access AS
 SELECT c.peer_id,c.conversation_id,c.link_id,c.epoch,c.local_room AS room_id,c.bridge_agent_id,l.agent_id AS local_agent_id,z.agent_id AS remote_agent_id,n.node_id AS local_node_id
 FROM federation_dm_conversations c
 JOIN federation_peers p ON p.node_id=c.peer_id AND p.enabled=1
 JOIN rooms r ON r.id=c.local_room AND r.kind='federated_dm' AND r.archived_at IS NULL
 CROSS JOIN federation_node n
 JOIN federation_dm_members l ON l.peer_id=c.peer_id AND l.conversation_id=c.conversation_id AND l.epoch=c.epoch AND l.side='local' AND l.node_id=n.node_id
 JOIN federation_dm_members z ON z.peer_id=c.peer_id AND z.conversation_id=c.conversation_id AND z.epoch=c.epoch AND z.side='remote' AND z.node_id=c.peer_id
 JOIN agents a ON a.id=l.agent_id AND a.revoked_at IS NULL AND a.kind NOT IN ('federation','system','bridge')
 JOIN agents b ON b.id=c.bridge_agent_id AND b.revoked_at IS NULL AND b.kind='federation'
 JOIN memberships lm ON lm.room_id=r.id AND lm.agent_id=a.id AND lm.role='participant'
 JOIN memberships bm ON bm.room_id=r.id AND bm.agent_id=b.id AND bm.role='bridge'
 WHERE c.active=1 AND (SELECT COUNT(*) FROM memberships WHERE room_id=r.id)=2 AND (SELECT COUNT(*) FROM memberships WHERE agent_id=b.id)=1;
 INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(5,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
 `)
	return err
}

func (s *Store) ConfigureFederationDM(ctx context.Context, peer string, conversations []FederationDMConversation) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.federation == nil {
		return ErrForbidden
	}
	if len(conversations) > 100 {
		return ErrInvalidTarget
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var node string
	if err = tx.QueryRowContext(ctx, `SELECT n.node_id FROM federation_node n JOIN federation_peers p ON p.node_id=? AND p.enabled=1`, peer).Scan(&node); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range conversations {
		if protocol.ValidateAgentName(c.ConversationID) != nil || protocol.ValidateAgentName(c.LinkID) != nil || c.Epoch < 1 || protocol.ValidateAgentName(c.LocalAgentID) != nil || protocol.ValidateAgentName(c.RemoteAgentID) != nil || seen[c.ConversationID] {
			return ErrInvalidTarget
		}
		seen[c.ConversationID] = true
		var valid int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id=? AND revoked_at IS NULL AND kind NOT IN ('federation','system','bridge')`, c.LocalAgentID).Scan(&valid); err != nil {
			return err
		}
		if valid != 1 {
			return ErrForbidden
		}
		var link, local, remote, room string
		var active int
		err = tx.QueryRowContext(ctx, `SELECT c.link_id,c.active,l.agent_id,z.agent_id,c.local_room FROM federation_dm_conversations c JOIN federation_dm_members l ON l.peer_id=c.peer_id AND l.conversation_id=c.conversation_id AND l.epoch=c.epoch AND l.side='local' JOIN federation_dm_members z ON z.peer_id=c.peer_id AND z.conversation_id=c.conversation_id AND z.epoch=c.epoch AND z.side='remote' WHERE c.peer_id=? AND c.conversation_id=? AND c.epoch=?`, peer, c.ConversationID, c.Epoch).Scan(&link, &active, &local, &remote, &room)
		if err == nil {
			if link != c.LinkID || local != c.LocalAgentID || remote != c.RemoteAgentID || active != 1 {
				return ErrFederationConflict
			}
			if err = authorizeDMRoom(ctx, tx, true, c.LocalAgentID, room); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT peer_id,conversation_id,epoch,local_room FROM federation_dm_conversations WHERE active=1`)
	if err != nil {
		return err
	}
	type old struct {
		peer, id, room string
		epoch          int64
	}
	var previous []old
	for rows.Next() {
		var o old
		if err = rows.Scan(&o.peer, &o.id, &o.epoch, &o.room); err != nil {
			rows.Close()
			return err
		}
		previous = append(previous, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, o := range previous {
		keep := false
		for _, c := range conversations {
			if o.peer == peer && o.id == c.ConversationID && o.epoch == c.Epoch {
				keep = true
			}
		}
		if !keep {
			if _, err = tx.ExecContext(ctx, `UPDATE federation_dm_conversations SET active=0 WHERE peer_id=? AND conversation_id=? AND epoch=?`, o.peer, o.id, o.epoch); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE rooms SET archived_at=COALESCE(archived_at,?) WHERE id=?`, now.Format(time.RFC3339Nano), o.room); err != nil {
				return err
			}
			if err = insertAudit(ctx, tx, "operator", "federation.dm_retired", map[string]any{"conversation_id": o.id, "epoch": o.epoch}); err != nil {
				return err
			}
		}
	}
	for _, c := range conversations {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_dm_conversations WHERE peer_id=? AND conversation_id=? AND epoch=?`, peer, c.ConversationID, c.Epoch).Scan(&exists); err != nil {
			return err
		}
		if exists == 1 {
			continue
		}
		digest := sha256.Sum256([]byte(peer + "\x00" + c.ConversationID + "\x00" + strconv.FormatInt(c.Epoch, 10)))
		room := "fdm_" + hex.EncodeToString(digest[:16])
		bridge := "fdm_bridge_" + protocol.NewID()
		token := protocol.NewToken("fed")
		for _, p := range [][2]string{{node, c.LocalAgentID}, {peer, c.RemoteAgentID}} {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO federation_principals VALUES(?,?)`, p[0], p[1]); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO agents(id,name,kind,token_hash,status,created_at,last_seen) VALUES(?,?,'federation',?,'offline',?,?)`, bridge, bridge, hashToken(token), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		topic := "Approved participants: " + c.LocalAgentID + "@" + node + " / " + c.RemoteAgentID + "@" + peer
		if _, err = tx.ExecContext(ctx, `INSERT INTO rooms(id,name,topic,kind,created_at) VALUES(?,?,?,'federated_dm',?)`, room, c.RemoteAgentID+"@"+peer, topic, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO federation_dm_conversations VALUES(?,?,?,?,?,?,1,?)`, peer, c.ConversationID, c.LinkID, c.Epoch, room, bridge, now.UnixMilli()); err != nil {
			return err
		}
		for _, p := range []struct{ side, node, id string }{{"local", node, c.LocalAgentID}, {"remote", peer, c.RemoteAgentID}} {
			if _, err = tx.ExecContext(ctx, `INSERT INTO federation_dm_members VALUES(?,?,?,?,?,?)`, peer, c.ConversationID, c.Epoch, p.side, p.node, p.id); err != nil {
				return err
			}
		}
		for _, p := range [][2]string{{c.LocalAgentID, "participant"}, {bridge, "bridge"}} {
			if _, err = tx.ExecContext(ctx, `INSERT INTO memberships(room_id,agent_id,role,joined_at) VALUES(?,?,?,?)`, room, p[0], p[1], now.Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		if err = insertAudit(ctx, tx, "operator", "federation.dm_approved", map[string]any{"conversation_id": c.ConversationID, "epoch": c.Epoch}); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err == nil {
		s.federationDM.Store(len(conversations) > 0)
	}
	return err
}

type dmQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func authorizeDMRoom(ctx context.Context, q dmQuerier, enabled bool, agent, room string) error {
	var kind string
	if err := q.QueryRowContext(ctx, `SELECT kind FROM rooms WHERE id=?`, room).Scan(&kind); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if kind != "federated_dm" {
		return nil
	}
	if !enabled {
		return ErrForbidden
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_dm_access WHERE room_id=? AND local_agent_id=?`, room, agent).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrForbidden
	}
	return nil
}

// Append to the actual read query too, so retirement between the initial ACL
// check and a read cannot expose private rows. Column names are code constants.
func dmReadPredicate(roomColumn string) string {
	return ` AND (NOT EXISTS(SELECT 1 FROM rooms dr WHERE dr.id=` + roomColumn + ` AND dr.kind='federated_dm') OR (? AND EXISTS(SELECT 1 FROM federation_dm_access da WHERE da.room_id=` + roomColumn + ` AND da.local_agent_id=?)))`
}
func (s *Store) FederatedDMContacts(ctx context.Context, agent string) ([]FederationDMContact, error) {
	out := []FederationDMContact{}
	if !s.federationDM.Load() {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT conversation_id,peer_id,remote_agent_id,epoch,room_id FROM federation_dm_access WHERE local_agent_id=? ORDER BY conversation_id`, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c FederationDMContact
		if err = rows.Scan(&c.ConversationID, &c.PeerNodeID, &c.RemoteAgentID, &c.Epoch, &c.RoomID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) OpenFederatedDM(ctx context.Context, agent, conversation string, epoch int64) (protocol.Room, error) {
	if !s.federationDM.Load() {
		return protocol.Room{}, ErrForbidden
	}
	var room string
	err := s.db.QueryRowContext(ctx, `SELECT room_id FROM federation_dm_access WHERE conversation_id=? AND epoch=? AND local_agent_id=?`, conversation, epoch, agent).Scan(&room)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Room{}, ErrForbidden
	}
	if err != nil {
		return protocol.Room{}, err
	}
	return s.RoomByID(ctx, room)
}
func (s *Store) LeaveFederatedDM(ctx context.Context, agent, conversation string, epoch int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if !s.federationDM.Load() {
		return ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var room string
	err = tx.QueryRowContext(ctx, `SELECT room_id FROM federation_dm_access WHERE conversation_id=? AND epoch=? AND local_agent_id=?`, conversation, epoch, agent).Scan(&room)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE federation_dm_conversations SET active=0 WHERE local_room=?`, room); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE rooms SET archived_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), room); err != nil {
		return err
	}
	if err = insertAudit(ctx, tx, agent, "federation.dm_left", map[string]any{"conversation_id": conversation, "epoch": epoch}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) enqueueFederationDM(ctx context.Context, tx *sql.Tx, m protocol.Message) error {
	if !s.federationDM.Load() || s.federation == nil || m.Meta.Federation != nil {
		return ErrForbidden
	}
	var e FederationDMEnvelope
	if err := tx.QueryRowContext(ctx, `SELECT peer_id,remote_agent_id,conversation_id,link_id,epoch,local_node_id FROM federation_dm_access WHERE room_id=? AND local_agent_id=?`, m.Room, m.From.ID).Scan(&e.RecipientNodeID, &e.RecipientAgentID, &e.ConversationID, &e.LinkID, &e.Epoch, &e.OriginNodeID); err != nil {
		return ErrForbidden
	}
	e.Version = 2
	e.Type = "federated_dm"
	e.OriginMessageID = m.ID
	e.Sender = m.From
	e.Text = m.Text
	e.SentAt = m.TS
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_dm_outbox`).Scan(&count); err != nil {
		return err
	}
	if count >= s.federation.MaxOutbox {
		if _, err := tx.ExecContext(ctx, `DELETE FROM federation_dm_outbox WHERE id IN (SELECT id FROM federation_dm_outbox WHERE state IN ('delivered','dead') ORDER BY created_at,id LIMIT 1)`); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_dm_outbox`).Scan(&count); err != nil {
			return err
		}
		if count >= s.federation.MaxOutbox {
			return ErrFederationFull
		}
	}
	raw, _ := json.Marshal(e)
	now := time.Now().UnixMilli()
	_, err := tx.ExecContext(ctx, `INSERT INTO federation_dm_outbox(id,peer_id,origin_node_id,origin_message_id,payload,state,max_attempts,next_attempt,created_at) VALUES(?,?,?,?,?,'pending',?,?,?)`, protocol.NewID(), e.RecipientNodeID, e.OriginNodeID, m.ID, string(raw), s.federation.MaxAttempts, now, now)
	return err
}
func (e FederationDMEnvelope) Validate(peer string) error {
	if e.Version != 2 || e.Type != "federated_dm" || e.OriginNodeID != peer || e.Epoch < 1 || e.SentAt.IsZero() || strings.TrimSpace(e.Text) == "" || len(e.Text) > protocol.MaxTextBytes || protocol.ValidateAgentName(e.Sender.Name) != nil || len(e.Sender.Kind) > 64 {
		return ErrInvalidTarget
	}
	for _, v := range []string{e.OriginNodeID, e.OriginMessageID, e.ConversationID, e.LinkID, e.RecipientNodeID, e.RecipientAgentID, e.Sender.ID} {
		if protocol.ValidateAgentName(v) != nil {
			return ErrInvalidTarget
		}
	}
	return nil
}
func dmDeliveryScope(ctx context.Context, q dmQuerier, e FederationDMEnvelope, inbound bool) (string, string, error) {
	local, remote, node, peer := e.Sender.ID, e.RecipientAgentID, e.OriginNodeID, e.RecipientNodeID
	if inbound {
		local, remote, node, peer = remote, local, peer, node
	}
	var room, bridge string
	err := q.QueryRowContext(ctx, `SELECT room_id,bridge_agent_id FROM federation_dm_access WHERE peer_id=? AND conversation_id=? AND link_id=? AND epoch=? AND local_node_id=? AND local_agent_id=? AND remote_agent_id=?`, peer, e.ConversationID, e.LinkID, e.Epoch, node, local, remote).Scan(&room, &bridge)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrForbidden
	}
	return room, bridge, err
}
func (s *Store) FederationDMJobAuthorized(ctx context.Context, j FederationJob, e FederationDMEnvelope) bool {
	if !s.federationDM.Load() || e.Validate(e.OriginNodeID) != nil || j.PeerID != e.RecipientNodeID {
		return false
	}
	_, _, err := dmDeliveryScope(ctx, s.db, e, false)
	return err == nil
}
func (s *Store) ImportFederationDM(ctx context.Context, peer string, e FederationDMEnvelope) (FederationReceipt, error) {
	r := FederationReceipt{OriginNodeID: e.OriginNodeID, OriginMessageID: e.OriginMessageID}
	if err := e.Validate(peer); err != nil {
		return r, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if !s.federationDM.Load() {
		return r, ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	room, bridge, err := dmDeliveryScope(ctx, tx, e, true)
	if err != nil {
		return r, err
	}
	raw, _ := json.Marshal(e)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT local_message_id,payload_hash FROM federation_inbox WHERE origin_node_id=? AND origin_message_id=?`, peer, e.OriginMessageID).Scan(&r.LocalMessageID, &existing)
	if err == nil {
		if existing != digest {
			return r, ErrFederationConflict
		}
		r.Replayed = true
		return r, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	var staged int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_file_transfers WHERE origin_node_id=? AND origin_message_id=?`, peer, e.OriginMessageID).Scan(&staged); err != nil {
		return r, err
	}
	if staged != 0 {
		return r, ErrFederationConflict
	}
	var seq int64
	if err = tx.QueryRowContext(ctx, `UPDATE rooms SET next_seq=next_seq+1 WHERE id=? RETURNING next_seq`, room).Scan(&seq); err != nil {
		return r, err
	}
	r.LocalMessageID = protocol.NewID()
	now := time.Now().UTC()
	meta, _ := json.Marshal(protocol.MessageMeta{Federation: &protocol.FederationMeta{OriginNodeID: peer, OriginMessageID: e.OriginMessageID, OriginRoom: e.ConversationID, Sender: e.Sender, OriginTime: e.SentAt, ConversationID: e.ConversationID}})
	if _, err = tx.ExecContext(ctx, `INSERT INTO messages(id,room_id,seq,sender_id,kind,text,meta_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, r.LocalMessageID, room, seq, bridge, protocol.KindChat, e.Text, string(meta), now.Format(time.RFC3339Nano)); err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO federation_inbox VALUES(?,?,?,?,?)`, peer, e.OriginMessageID, r.LocalMessageID, digest, now.UnixMilli()); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func (s *Store) ClaimFederationDMJob(ctx context.Context, now time.Time, lease time.Duration) (*FederationJob, error) {
	if !s.federationDM.Load() {
		return nil, nil
	}
	return s.claimFederationJob(ctx, now, lease, "federation_dm_outbox", "")
}
func (s *Store) FinishFederationDMJob(ctx context.Context, j FederationJob, delivered, permanent bool, next time.Time, reason string) error {
	return s.finishFederationJob(ctx, j, delivered, permanent, next, reason, "federation_dm_outbox")
}
