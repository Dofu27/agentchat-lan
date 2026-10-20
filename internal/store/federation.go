package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

type FederationPeer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

type FederationLink struct {
	ID         string    `json:"id"`
	LocalRoom  string    `json:"local_room"`
	PeerID     string    `json:"peer_id"`
	PeerName   string    `json:"peer_name"`
	RemoteRoom string    `json:"remote_room"`
	BridgeID   string    `json:"bridge_agent"`
	CreatedAt  time.Time `json:"created_at"`
}

type FederationOutboxEntry struct {
	ID         string
	LinkID     string
	MessageID  string
	OriginNode string
	OriginID   string
	Payload    []byte
	Attempts   int
	PeerName   string
	PeerURL    string
	PeerSecret string
	RemoteRoom string
}

type FederationPayload struct {
	Room       string            `json:"room"`
	OriginNode string            `json:"origin_node"`
	OriginID   string            `json:"origin_message_id"`
	Sender     protocol.AgentRef `json:"sender"`
	Text       string            `json:"text"`
	ReplyTo    string            `json:"reply_to,omitempty"`
	Meta       json.RawMessage   `json:"meta,omitempty"`
	TS         time.Time         `json:"ts"`
}

func (s *Store) ensureFederationNode(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_node`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	nodeID := "node_" + hex.EncodeToString(b)
	_, err := s.db.ExecContext(ctx, `INSERT INTO federation_node(id,node_id,created_at) VALUES(1,?,?)`, nodeID, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) NodeID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT node_id FROM federation_node WHERE id=1`).Scan(&id)
	return id, err
}

func (s *Store) CreateFederationPeer(ctx context.Context, name, url, secret string) (FederationPeer, error) {
	name = strings.TrimSpace(name)
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	secret = strings.TrimSpace(secret)
	if name == "" || url == "" || secret == "" {
		return FederationPeer{}, errors.New("name, url, and secret are required")
	}
	if len(name) > protocol.MaxNameLength || len(secret) < 16 {
		return FederationPeer{}, errors.New("invalid peer name or secret")
	}
	id := "peer_" + protocol.NewID()
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO federation_peers(id,name,url,secret,enabled,created_at) VALUES(?,?,?,?,1,?)`, id, name, url, secret, now.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return FederationPeer{}, ErrConflict
		}
		return FederationPeer{}, err
	}
	return FederationPeer{ID: id, Name: name, URL: url, Enabled: true, CreatedAt: now}, nil
}

func (s *Store) ListFederationPeers(ctx context.Context) ([]FederationPeer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,url,enabled,created_at FROM federation_peers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FederationPeer{}
	for rows.Next() {
		var p FederationPeer
		var enabled int
		var created string
		if err := rows.Scan(&p.ID, &p.Name, &p.URL, &enabled, &created); err != nil {
			return nil, err
		}
		p.Enabled = enabled == 1
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) FederationPeerBySecret(ctx context.Context, secret string) (FederationPeer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,url,secret,enabled,created_at FROM federation_peers WHERE enabled=1`)
	if err != nil {
		return FederationPeer{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var p FederationPeer
		var stored, created string
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.URL, &stored, &enabled, &created); err != nil {
			return FederationPeer{}, err
		}
		if subtle.ConstantTimeCompare([]byte(stored), []byte(secret)) == 1 {
			p.Enabled = true
			p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
			return p, nil
		}
	}
	return FederationPeer{}, ErrNotFound
}

func (s *Store) CreateFederationLink(ctx context.Context, localRoom, peerID, remoteRoom, actor string) (FederationLink, error) {
	localRoom = strings.TrimSpace(localRoom)
	remoteRoom = strings.TrimSpace(remoteRoom)
	if localRoom == "" || peerID == "" || remoteRoom == "" {
		return FederationLink{}, errors.New("local_room, peer_id, and remote_room are required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FederationLink{}, err
	}
	defer tx.Rollback()
	var peerName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM federation_peers WHERE id=? AND enabled=1`, peerID).Scan(&peerName); errors.Is(err, sql.ErrNoRows) {
		return FederationLink{}, ErrNotFound
	} else if err != nil {
		return FederationLink{}, err
	}
	var roomExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM rooms WHERE id=? AND archived_at IS NULL`, localRoom).Scan(&roomExists); errors.Is(err, sql.ErrNoRows) {
		return FederationLink{}, ErrNotFound
	} else if err != nil {
		return FederationLink{}, err
	}
	bridgeID := "agt_" + protocol.NewID()
	bridgeName := "fed-" + peerName + "-" + bridgeID[len(bridgeID)-6:]
	if len(bridgeName) > protocol.MaxNameLength {
		bridgeName = bridgeName[:protocol.MaxNameLength]
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return FederationLink{}, err
	}
	tokenHash := sha256.Sum256(b)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id,name,kind,token_hash,status,created_at,last_seen) VALUES(?,?,?,?,?,?,?)`, bridgeID, bridgeName, "federation", "fed_"+hex.EncodeToString(tokenHash[:]), protocol.StatusOffline, now, now); err != nil {
		return FederationLink{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memberships(room_id,agent_id,role,joined_at) VALUES(?,?,?,?)`, localRoom, bridgeID, "member", now); err != nil {
		return FederationLink{}, err
	}
	id := "link_" + protocol.NewID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO federation_links(id,local_room,peer_id,remote_room,bridge_agent,created_at) VALUES(?,?,?,?,?,?)`, id, localRoom, peerID, remoteRoom, bridgeID, now); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return FederationLink{}, ErrConflict
		}
		return FederationLink{}, err
	}
	if err := insertAudit(ctx, tx, actor, "federation.link_created", map[string]any{"link": id, "room": localRoom, "peer": peerID}); err != nil {
		return FederationLink{}, err
	}
	if err := tx.Commit(); err != nil {
		return FederationLink{}, err
	}
	return FederationLink{ID: id, LocalRoom: localRoom, PeerID: peerID, PeerName: peerName, RemoteRoom: remoteRoom, BridgeID: bridgeID, CreatedAt: time.Now().UTC()}, nil
}

func (s *Store) FederationLinkForPeer(ctx context.Context, peerID, remoteRoom string) (FederationLink, error) {
	var l FederationLink
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT l.id,l.local_room,l.peer_id,p.name,l.remote_room,l.bridge_agent,l.created_at FROM federation_links l JOIN federation_peers p ON p.id=l.peer_id WHERE l.peer_id=? AND l.remote_room=? AND p.enabled=1`, peerID, remoteRoom).
		Scan(&l.ID, &l.LocalRoom, &l.PeerID, &l.PeerName, &l.RemoteRoom, &l.BridgeID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return FederationLink{}, ErrNotFound
	}
	if err != nil {
		return FederationLink{}, err
	}
	l.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return l, nil
}

func (s *Store) ListFederationLinks(ctx context.Context) ([]FederationLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.id,l.local_room,l.peer_id,p.name,l.remote_room,l.bridge_agent,l.created_at FROM federation_links l JOIN federation_peers p ON p.id=l.peer_id ORDER BY l.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FederationLink{}
	for rows.Next() {
		var l FederationLink
		var created string
		if err := rows.Scan(&l.ID, &l.LocalRoom, &l.PeerID, &l.PeerName, &l.RemoteRoom, &l.BridgeID, &created); err != nil {
			return nil, err
		}
		l.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) EnqueueFederation(ctx context.Context, message protocol.Message) error {
	nodeID, err := s.NodeID(ctx)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,remote_room FROM federation_links WHERE local_room=?`, message.Room)
	if err != nil {
		return err
	}
	type link struct{ id, room string }
	var links []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.id, &l.room); err != nil {
			rows.Close()
			return err
		}
		links = append(links, l)
	}
	rows.Close()
	for _, l := range links {
		metaJSON, _ := json.Marshal(message.Meta)
		payload := FederationPayload{
			Room: l.room, OriginNode: nodeID, OriginID: message.ID,
			Sender: message.From, Text: message.Text, ReplyTo: message.ReplyTo,
			Meta: metaJSON, TS: message.TS,
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO federation_outbox(id,link_id,message_id,origin_node,origin_message_id,payload_json,status,attempts,next_attempt,created_at) VALUES(?,?,?,?,?,?, 'pending',0,?,?)`, "out_"+protocol.NewID(), l.id, message.ID, nodeID, message.ID, string(b), now, now)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) FederationOutbox(ctx context.Context, limit int) ([]FederationOutboxEntry, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT o.id,o.link_id,o.message_id,o.origin_node,o.origin_message_id,o.payload_json,o.attempts,p.name,p.url,p.secret,l.remote_room FROM federation_outbox o JOIN federation_links l ON l.id=o.link_id JOIN federation_peers p ON p.id=l.peer_id WHERE o.status='pending' AND o.next_attempt<=? AND p.enabled=1 ORDER BY o.created_at LIMIT ?`, time.Now().UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FederationOutboxEntry{}
	for rows.Next() {
		var e FederationOutboxEntry
		if err := rows.Scan(&e.ID, &e.LinkID, &e.MessageID, &e.OriginNode, &e.OriginID, &e.Payload, &e.Attempts, &e.PeerName, &e.PeerURL, &e.PeerSecret, &e.RemoteRoom); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) MarkFederationDelivered(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE federation_outbox SET status='delivered',last_error='' WHERE id=?`, id)
	return err
}

func (s *Store) MarkFederationFailed(ctx context.Context, id string, attempts int, next time.Time, lastErr string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE federation_outbox SET attempts=?,next_attempt=?,last_error=? WHERE id=?`, attempts, next.UTC().Format(time.RFC3339Nano), lastErr, id)
	return err
}

func (s *Store) InsertFederatedMessage(ctx context.Context, linkID string, payload FederationPayload) (protocol.Message, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Message{}, false, err
	}
	defer tx.Rollback()
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT message_id FROM federation_inbox WHERE origin_node=? AND origin_message_id=?`, payload.OriginNode, payload.OriginID).Scan(&existingID)
	if err == nil {
		return protocol.Message{}, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.Message{}, false, err
	}
	var localRoom, bridgeID string
	if err := tx.QueryRowContext(ctx, `SELECT local_room,bridge_agent FROM federation_links WHERE id=?`, linkID).Scan(&localRoom, &bridgeID); errors.Is(err, sql.ErrNoRows) {
		return protocol.Message{}, false, ErrNotFound
	} else if err != nil {
		return protocol.Message{}, false, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, `UPDATE rooms SET next_seq=next_seq+1 WHERE id=? AND archived_at IS NULL RETURNING next_seq`, localRoom).Scan(&seq); err != nil {
		return protocol.Message{}, false, err
	}
	meta := map[string]any{
		"federation": map[string]any{
			"origin_node": payload.OriginNode, "origin_message_id": payload.OriginID,
			"sender": payload.Sender, "remote_room": payload.Room,
		},
	}
	if len(payload.Meta) > 0 {
		var original map[string]any
		if json.Unmarshal(payload.Meta, &original) == nil {
			for k, v := range original {
				if k != "federation" {
					meta[k] = v
				}
			}
		}
	}
	metaJSON, _ := json.Marshal(meta)
	now := time.Now().UTC()
	id := protocol.NewID()
	clientID := "fed:" + payload.OriginNode + ":" + payload.OriginID
	_, err = tx.ExecContext(ctx, `INSERT INTO messages(id,room_id,seq,sender_id,kind,text,reply_to,meta_json,created_at,client_msg_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, localRoom, seq, bridgeID, protocol.KindChat, payload.Text, payload.ReplyTo, string(metaJSON), now.Format(time.RFC3339Nano), clientID)
	if err != nil {
		return protocol.Message{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO federation_inbox(origin_node,origin_message_id,link_id,message_id,received_at) VALUES(?,?,?,?,?)`, payload.OriginNode, payload.OriginID, linkID, id, now.Format(time.RFC3339Nano)); err != nil {
		return protocol.Message{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Message{}, false, err
	}
	m, err := messageByClientID(ctx, s.db, bridgeID, localRoom, clientID)
	if err != nil {
		return protocol.Message{}, false, fmt.Errorf("read federated message: %w", err)
	}
	return m, false, nil
}

func (s *Store) FederationOutboxCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status,COUNT(*) FROM federation_outbox GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
	}
	return out, rows.Err()
}
