package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

// migrateReadReceipts leaves existing ACK cursors untouched, including on upgrade.
func (s *Store) migrateReadReceipts(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(memberships)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		if err = rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		found = found || name == "read_seq"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE memberships ADD COLUMN read_seq INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(6,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkRead records a local, explicit declaration. It never acknowledges inbox
// messages, publishes an event, or queues a federation receipt.
func (s *Store) MarkRead(ctx context.Context, agentID, roomID string, seq int64) (protocol.ReadReceipt, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.ReadReceipt{}, err
	}
	defer tx.Rollback()
	var max int64
	var kind string
	err = tx.QueryRowContext(ctx, `SELECT next_seq,kind FROM rooms WHERE id=? AND archived_at IS NULL`, roomID).Scan(&max, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.ReadReceipt{}, ErrNotFound
	}
	if err != nil {
		return protocol.ReadReceipt{}, err
	}
	// Federated DM read state needs a separate privacy/identity contract.
	if kind == "federated_dm" {
		return protocol.ReadReceipt{}, ErrForbidden
	}
	var member int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM memberships m JOIN agents a ON a.id=m.agent_id WHERE m.room_id=? AND m.agent_id=? AND a.revoked_at IS NULL`, roomID, agentID).Scan(&member)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.ReadReceipt{}, ErrForbidden
	}
	if err != nil {
		return protocol.ReadReceipt{}, err
	}
	if seq < 0 || seq > max {
		return protocol.ReadReceipt{}, ErrInvalidReadSeq
	}
	receipt := protocol.ReadReceipt{Room: roomID, AgentID: agentID}
	if err = tx.QueryRowContext(ctx, `UPDATE memberships SET read_seq=MAX(read_seq,?) WHERE room_id=? AND agent_id=? RETURNING read_seq`, seq, roomID, agentID).Scan(&receipt.ReadSeq); err != nil {
		return protocol.ReadReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return protocol.ReadReceipt{}, err
	}
	return receipt, nil
}
