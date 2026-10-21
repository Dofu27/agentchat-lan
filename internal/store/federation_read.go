package store

import (
	"context"
	"database/sql"
	"time"
)

type FederationReadReceipt struct {
	PeerID    string    `json:"peer_id"`
	Room      string    `json:"room"`
	AgentID   string    `json:"agent_id"`
	ReadSeq   int64     `json:"read_seq"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RecordFederationRead stores an explicit remote read declaration. It is
// advisory: it never changes local ACK/inbox state and never re-forwarded.
func (s *Store) RecordFederationRead(ctx context.Context, peer, room, agent string, seq int64) error {
	if peer == "" || room == "" || agent == "" || len(agent) > 128 || seq < 0 {
		return ErrInvalidTarget
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_room_links l JOIN federation_peers p ON p.node_id=l.peer_id JOIN rooms r ON r.id=l.local_room WHERE l.peer_id=? AND l.local_room=? AND l.enabled=1 AND p.enabled=1 AND r.kind='room' AND r.archived_at IS NULL`, peer, room).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrForbidden
	}
	var maxSeq int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM messages WHERE room_id=?`, room).Scan(&maxSeq); err != nil {
		return err
	}
	if seq > maxSeq {
		seq = maxSeq
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO federation_read_receipts(peer_id,room,agent_id,read_seq,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(peer_id,room,agent_id) DO UPDATE SET read_seq=MAX(read_seq,excluded.read_seq),updated_at=excluded.updated_at`, peer, room, agent, seq, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FederationReadReceipts(ctx context.Context, room string) ([]FederationReadReceipt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT peer_id,room,agent_id,read_seq,updated_at FROM federation_read_receipts WHERE room=? ORDER BY peer_id,agent_id`, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FederationReadReceipt
	for rows.Next() {
		var r FederationReadReceipt
		var ts string
		if err := rows.Scan(&r.PeerID, &r.Room, &r.AgentID, &r.ReadSeq, &ts); err != nil {
			return nil, err
		}
		r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) FederationReadTargets(ctx context.Context, localRoom string) ([]FederationLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.peer_id,l.local_room,l.remote_room FROM federation_room_links l JOIN federation_peers p ON p.node_id=l.peer_id JOIN rooms r ON r.id=l.local_room WHERE l.local_room=? AND l.enabled=1 AND p.enabled=1 AND r.kind='room' AND r.archived_at IS NULL`, localRoom)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FederationLink
	for rows.Next() {
		var l FederationLink
		if err := rows.Scan(&l.PeerID, &l.LocalRoom, &l.RemoteRoom); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil, sql.ErrNoRows
	}
	return out, rows.Err()
}
