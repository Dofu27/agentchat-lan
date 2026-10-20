package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrFederationNotDead = errors.New("federation job is not dead")

// Operator DTOs deliberately exclude credentials, URLs, payloads and leases.
type FederationOperatorPeer struct {
	NodeID  string `json:"node_id"`
	Enabled bool   `json:"enabled"`
}
type FederationOperatorLink struct {
	PeerID     string `json:"peer_id"`
	LocalRoom  string `json:"local_room"`
	RemoteRoom string `json:"remote_room"`
	Enabled    bool   `json:"enabled"`
}
type FederationOperatorStatus struct {
	NodeID string                   `json:"node_id"`
	Peers  []FederationOperatorPeer `json:"peers"`
	Links  []FederationOperatorLink `json:"room_links"`
	Outbox FederationQueueStats     `json:"outbox"`
}
type FederationOutboxItem struct {
	ID              string    `json:"id"`
	PeerID          string    `json:"peer_id"`
	OriginNodeID    string    `json:"origin_node_id"`
	OriginMessageID string    `json:"origin_message_id"`
	State           string    `json:"state"`
	Attempts        int       `json:"attempts"`
	LastError       string    `json:"last_error"`
	CreatedAt       time.Time `json:"created_at"`
}
type FederationRetryResult struct {
	ID       string `json:"id"`
	Replayed bool   `json:"replayed"`
}

// ReadFederationOperatorStatus never creates a database, migrates it, provisions
// an identity or enables forwarding. It reports persisted state, not process state.
func ReadFederationOperatorStatus(ctx context.Context, path string) (FederationOperatorStatus, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return FederationOperatorStatus{}, err
	}
	u, err := url.Parse("file:" + filepath.ToSlash(abs))
	if err != nil {
		return FederationOperatorStatus{}, err
	}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return FederationOperatorStatus{}, err
	}
	defer db.Close()
	return (&Store{db: db}).FederationOperatorStatus(ctx)
}

func (s *Store) FederationOperatorStatus(ctx context.Context) (FederationOperatorStatus, error) {
	out := FederationOperatorStatus{Peers: []FederationOperatorPeer{}, Links: []FederationOperatorLink{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, "SELECT node_id FROM federation_node WHERE singleton=1").Scan(&out.NodeID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT node_id,enabled FROM federation_peers ORDER BY node_id")
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p FederationOperatorPeer
		if err = rows.Scan(&p.NodeID, &p.Enabled); err != nil {
			rows.Close()
			return out, err
		}
		out.Peers = append(out.Peers, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT peer_id,local_room,remote_room,enabled FROM federation_room_links ORDER BY peer_id,local_room")
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var l FederationOperatorLink
		if err = rows.Scan(&l.PeerID, &l.LocalRoom, &l.RemoteRoom, &l.Enabled); err != nil {
			rows.Close()
			return out, err
		}
		out.Links = append(out.Links, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(state='pending'),0),COALESCE(SUM(state='inflight'),0),COALESCE(SUM(state='delivered'),0),COALESCE(SUM(state='dead'),0),COALESCE(SUM(attempts),0) FROM federation_outbox`).Scan(&out.Outbox.Pending, &out.Outbox.Inflight, &out.Outbox.Delivered, &out.Outbox.Dead, &out.Outbox.Attempts)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func ValidFederationState(state string) bool {
	return state == "pending" || state == "inflight" || state == "delivered" || state == "dead"
}

// Allowlist stored error classes: even a manually damaged/legacy row cannot
// disclose arbitrary peer response bodies or credentials through this API.
func federationOperatorError(reason string) string {
	switch reason {
	case "", "invalid stored envelope", "storage unavailable", "peer or room link disabled", "invalid configured endpoint", "peer transport failure", "invalid peer receipt":
		return reason
	}
	if strings.HasPrefix(reason, "peer HTTP ") {
		n, err := strconv.Atoi(strings.TrimPrefix(reason, "peer HTTP "))
		if err == nil && n >= 100 && n <= 599 {
			return "peer HTTP " + strconv.Itoa(n)
		}
	}
	return "delivery failure"
}

func (s *Store) ListFederationOutbox(ctx context.Context, state string, limit int) ([]FederationOutboxItem, error) {
	if !ValidFederationState(state) || limit < 1 || limit > 100 {
		return nil, errors.New("invalid outbox state or limit")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,peer_id,origin_node_id,origin_message_id,state,attempts,last_error,created_at FROM federation_outbox WHERE state=? ORDER BY created_at DESC,id DESC LIMIT ?`, state, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FederationOutboxItem{}
	for rows.Next() {
		var item FederationOutboxItem
		var ts int64
		if err = rows.Scan(&item.ID, &item.PeerID, &item.OriginNodeID, &item.OriginMessageID, &item.State, &item.Attempts, &item.LastError, &ts); err != nil {
			return nil, err
		}
		item.CreatedAt = time.UnixMilli(ts).UTC()
		item.LastError = federationOperatorError(item.LastError)
		out = append(out, item)
	}
	return out, rows.Err()
}

// RetryFederationOutbox atomically retries only dead jobs, with a durable receipt
// and audit event. Reusing a key replays the acknowledgement without touching a
// pending/inflight or subsequently dead job. A new key on a non-dead job conflicts.
func (s *Store) RetryFederationOutbox(ctx context.Context, id, key string) (FederationRetryResult, error) {
	out := FederationRetryResult{ID: id}
	if id == "" || len(key) < 1 || len(key) > 128 || strings.ContainsAny(key, "\r\n\t ") {
		return out, errors.New("invalid retry idempotency key")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.federation == nil {
		return out, ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	sum := sha256.Sum256([]byte(key))
	digest := hex.EncodeToString(sum[:])
	var exists int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM federation_operator_retries WHERE job_id=? AND key_hash=?", id, digest).Scan(&exists)
	if err == nil {
		out.Replayed = true
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	var state string
	err = tx.QueryRowContext(ctx, "SELECT state FROM federation_outbox WHERE id=?", id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if state != "dead" {
		return out, ErrFederationNotDead
	}
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, "UPDATE federation_outbox SET state='pending',attempts=0,lease_token='',lease_until=0,next_attempt=?,last_error='' WHERE id=? AND state='dead'", now.UnixMilli(), id)
	if err != nil {
		return out, err
	}
	if err = insertAudit(ctx, tx, "admin", "federation.outbox_retried", map[string]any{"id": id}); err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO federation_operator_retries(job_id,key_hash,created_at) VALUES(?,?,?)", id, digest, now.UnixMilli())
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
