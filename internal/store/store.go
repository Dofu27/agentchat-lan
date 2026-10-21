// Package store implements the durable SQLite state for the production hub.
package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrForbidden     = errors.New("forbidden")
	ErrConflict      = errors.New("conflict")
	ErrInviteExpired = errors.New("invite expired")
	ErrInviteUsed    = errors.New("invite used")
	ErrInviteInvalid = errors.New("invite invalid")
	ErrInvalidTarget = errors.New("message target is not a room member")
	ErrFileNotFound  = errors.New("file not found")
)

// Store owns a SQLite database. All public operations are safe for concurrent use.
type Store struct {
	db              *sql.DB
	writeMu         sync.Mutex
	federationFiles bool
	federationDM    atomic.Bool
	federation      *FederationLimits // runtime opt-in; nil on every ordinary Open
}

// Open opens or creates a database, applies forward-only migrations, and seeds
// the contract's default room. Use ":memory:" for isolated tests.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := path
	if path == ":memory:" {
		dsn = "file:agentchat-memory?mode=memory&cache=shared"
	} else if !strings.HasPrefix(path, "file:") {
		dsn = "file:" + filepath.ToSlash(path)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database path: %w", err)
	}
	q := u.Query()
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	s := &Store{db: db}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.ensureDefaultRoom(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrateFederation(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrateFederationDM(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error                    { return s.db.Close() }
func (s *Store) Ready(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS agents(
 id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, kind TEXT NOT NULL,
 token_hash TEXT NOT NULL UNIQUE, status TEXT NOT NULL, created_at TEXT NOT NULL,
 last_seen TEXT NOT NULL, revoked_at TEXT
);
CREATE TABLE IF NOT EXISTS rooms(
 id TEXT PRIMARY KEY, name TEXT NOT NULL, topic TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL,
 created_at TEXT NOT NULL, archived_at TEXT, next_seq INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS memberships(
 room_id TEXT NOT NULL REFERENCES rooms(id), agent_id TEXT NOT NULL REFERENCES agents(id),
 role TEXT NOT NULL DEFAULT 'member', joined_at TEXT NOT NULL, last_ack_seq INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(room_id,agent_id)
);
CREATE TABLE IF NOT EXISTS messages(
 id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id), seq INTEGER NOT NULL,
 sender_id TEXT NOT NULL REFERENCES agents(id), kind TEXT NOT NULL, text TEXT NOT NULL,
 reply_to TEXT NOT NULL DEFAULT '', meta_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL,
 client_msg_id TEXT, UNIQUE(room_id,seq), UNIQUE(sender_id,room_id,client_msg_id)
);
CREATE TABLE IF NOT EXISTS mentions(
 message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id), PRIMARY KEY(message_id,agent_id)
);
CREATE TABLE IF NOT EXISTS files(
 id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', sha256 TEXT NOT NULL UNIQUE,
 size INTEGER NOT NULL, mime TEXT NOT NULL, storage_path TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS attachments(
 message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 file_id TEXT NOT NULL REFERENCES files(id), display_name TEXT NOT NULL,
 PRIMARY KEY(message_id,file_id)
);
CREATE TABLE IF NOT EXISTS invites(
 code_hash TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id), name TEXT NOT NULL DEFAULT '',
 expires_at TEXT NOT NULL, uses_left INTEGER NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit(
 id TEXT PRIMARY KEY, ts TEXT NOT NULL, actor_id TEXT NOT NULL DEFAULT '',
 action TEXT NOT NULL, detail_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_messages_room_seq ON messages(room_id,seq);
CREATE INDEX IF NOT EXISTS idx_memberships_inbox ON memberships(agent_id,room_id,last_ack_seq);
CREATE INDEX IF NOT EXISTS idx_invites_expiry ON invites(expires_at);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit(ts DESC);
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(1,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("apply migration 1: %w", err)
	}
	if err := s.ensureFileNameColumn(ctx); err != nil {
		return fmt.Errorf("apply file name migration: %w", err)
	}
	return nil
}

// ensureFileNameColumn upgrades databases created before the M2 file service.
func (s *Store) ensureFileNameColumn(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(files)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == "name" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE files ADD COLUMN name TEXT NOT NULL DEFAULT ''`)
	return err
}

func (s *Store) ensureDefaultRoom(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO rooms(id,name,topic,kind,created_at) VALUES(?,?,?,?,?)`, protocol.DefaultRoom, protocol.DefaultRoom, "General agent chat", "room", now)
	if err != nil {
		return fmt.Errorf("create default room: %w", err)
	}
	return nil
}

// EnsureAdminToken returns an existing admin token as unavailable (false), or
// stores only a hash of and returns a newly generated token (true).
func (s *Store) EnsureAdminToken(ctx context.Context) (string, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='admin_token_hash'`).Scan(&existing)
	if err == nil {
		return "", false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	token := protocol.NewToken("adm")
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key,value) VALUES('admin_token_hash',?)`, hashToken(token))
	if err != nil {
		return "", false, err
	}
	if s.IsAdminToken(ctx, token) {
		return token, true, nil
	}
	return "", false, errors.New("admin token was initialized concurrently; retrieve it from the initializer")
}

// RotateAdminToken replaces the admin credential and returns the new plaintext
// token exactly once. The caller is already authenticated with the old token.
func (s *Store) RotateAdminToken(ctx context.Context, actor string) (string, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	token := protocol.NewToken("adm")
	res, err := tx.ExecContext(ctx, `UPDATE settings SET value=? WHERE key='admin_token_hash'`, hashToken(token))
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('admin_token_hash',?)`, hashToken(token)); err != nil {
			return "", err
		}
	}
	if err := insertAudit(ctx, tx, actor, "admin.token_rotated", nil); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// IsAdminToken compares only token hashes; raw tokens are never stored.
func (s *Store) IsAdminToken(ctx context.Context, token string) bool {
	var want string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='admin_token_hash'`).Scan(&want); err != nil {
		return false
	}
	got := hashToken(token)
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// AgentByToken authenticates an active agent and refreshes last_seen.
func (s *Store) AgentByToken(ctx context.Context, token string) (protocol.Agent, error) {
	a, err := s.AgentByTokenReadOnly(ctx, token)
	if err != nil {
		return a, err
	}
	a.Status = protocol.StatusOnline
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `UPDATE agents SET last_seen=?,status=? WHERE id=?`, now, protocol.StatusOnline, a.ID)
	return a, err
}

// AgentByTokenReadOnly checks revocation without turning ephemeral wakes into writes.
func (s *Store) AgentByTokenReadOnly(ctx context.Context, token string) (protocol.Agent, error) {
	var a protocol.Agent
	var created, seen string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,kind,status,created_at,last_seen FROM agents WHERE token_hash=? AND revoked_at IS NULL`, hashToken(token)).Scan(&a.ID, &a.Name, &a.Kind, &a.Status, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.LastSeen, _ = time.Parse(time.RFC3339Nano, seen)
	return a, nil
}

// Invite is a persisted, hashed, room-scoped one-time or limited-use code.
type Invite struct {
	Room string
	Name string
	TTL  time.Duration
	Uses int
}
type InviteInfo struct {
	Room      string    `json:"room"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
	Uses      int       `json:"uses"`
}

// StoredFile is durable file metadata and its store-relative blob path.
type StoredFile struct {
	Ref          protocol.FileRef
	RelativePath string
}

// RegisterFile inserts metadata for an atomically installed blob. Content is
// unique by SHA-256; concurrent duplicate uploads receive the existing row.
func (s *Store) RegisterFile(ctx context.Context, ref protocol.FileRef, relativePath string) (StoredFile, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if ref.ID == "" || ref.Size < 0 || len(ref.SHA256) != 64 || relativePath == "" {
		return StoredFile{}, false, errors.New("invalid file metadata")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StoredFile{}, false, err
	}
	defer tx.Rollback()
	var existing StoredFile
	err = tx.QueryRowContext(ctx, `SELECT id,name,size,sha256,mime,storage_path FROM files WHERE sha256=?`, ref.SHA256).Scan(&existing.Ref.ID, &existing.Ref.Name, &existing.Ref.Size, &existing.Ref.SHA256, &existing.Ref.Mime, &existing.RelativePath)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return StoredFile{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO files(id,name,sha256,size,mime,storage_path,created_at) VALUES(?,?,?,?,?,?,?)`, ref.ID, ref.Name, ref.SHA256, ref.Size, ref.Mime, filepath.ToSlash(relativePath), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return StoredFile{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return StoredFile{}, false, err
	}
	return StoredFile{Ref: ref, RelativePath: filepath.ToSlash(relativePath)}, true, nil
}

// FileByID returns metadata without exposing its storage path to the API.
func (s *Store) FileByID(ctx context.Context, id string) (StoredFile, error) {
	var f StoredFile
	err := s.db.QueryRowContext(ctx, `SELECT id,name,size,sha256,mime,storage_path FROM files WHERE id=?`, id).Scan(&f.Ref.ID, &f.Ref.Name, &f.Ref.Size, &f.Ref.SHA256, &f.Ref.Mime, &f.RelativePath)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredFile{}, ErrFileNotFound
	}
	return f, err
}

// TotalFileBytes returns the sum of stored file sizes. It is used for the
// optional hub-wide storage quota.
func (s *Store) TotalFileBytes(ctx context.Context) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM files`).Scan(&total)
	return total, err
}

// FileSHAExists reports whether the content-addressed file already exists, so
// a duplicate upload does not count against the storage quota.
func (s *Store) FileSHAExists(ctx context.Context, sha string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM files WHERE sha256=?`, sha).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// CanAccessFile restricts downloads to files attached to a room the agent can
// access, preserving the hub's room-scoped authorization model.
func (s *Store) CanAccessFile(ctx context.Context, agentID, fileID string) (bool, error) {
	var allowed int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM attachments x JOIN messages m ON m.id=x.message_id JOIN memberships mb ON mb.room_id=m.room_id WHERE x.file_id=? AND mb.agent_id=?`+dmReadPredicate("m.room_id")+` LIMIT 1`, fileID, agentID, s.federationDM.Load(), agentID).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) CreateInvite(ctx context.Context, code string, in Invite, actor string) (time.Time, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if in.Uses < 1 {
		return time.Time{}, errors.New("uses must be positive")
	}
	expires := time.Now().UTC().Add(in.TTL)
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	var archived sql.NullString
	var inviteKind string
	if err = tx.QueryRowContext(ctx, `SELECT kind,archived_at FROM rooms WHERE id=?`, in.Room).Scan(&inviteKind, &archived); errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	} else if err != nil {
		return time.Time{}, err
	}
	if inviteKind == "federated_dm" {
		return time.Time{}, ErrForbidden
	}
	if archived.Valid {
		return time.Time{}, ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO invites(code_hash,room_id,name,expires_at,uses_left,created_at) VALUES(?,?,?,?,?,?)`, hashToken(code), in.Room, in.Name, expires.Format(time.RFC3339Nano), in.Uses, now.Format(time.RFC3339Nano)); err != nil {
		return time.Time{}, err
	}
	if err = insertAudit(ctx, tx, actor, "invite.created", map[string]any{"room": in.Room, "name": in.Name, "uses": in.Uses, "expires_at": expires}); err != nil {
		return time.Time{}, err
	}
	return expires, tx.Commit()
}

// ListInvites returns active invite metadata without exposing codes or hashes.
func (s *Store) ListInvites(ctx context.Context) ([]InviteInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT room_id,name,expires_at,uses_left FROM invites WHERE uses_left>0 AND expires_at>? ORDER BY expires_at`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InviteInfo{}
	for rows.Next() {
		var i InviteInfo
		var e string
		if err = rows.Scan(&i.Room, &i.Name, &e, &i.Uses); err != nil {
			return nil, err
		}
		i.ExpiresAt, _ = time.Parse(time.RFC3339Nano, e)
		out = append(out, i)
	}
	return out, rows.Err()
}

// Join consumes one invite use and creates an agent with a hashed bearer token.
func (s *Store) Join(ctx context.Context, code string, agent protocol.Agent, token string) ([]protocol.Room, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var room, name, expiry string
	var uses int
	err = tx.QueryRowContext(ctx, `SELECT room_id,name,expires_at,uses_left FROM invites WHERE code_hash=?`, hashToken(code)).Scan(&room, &name, &expiry, &uses)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInviteInvalid
	}
	if err != nil {
		return nil, err
	}
	deadline, e := time.Parse(time.RFC3339Nano, expiry)
	if e != nil {
		return nil, e
	}
	if !time.Now().UTC().Before(deadline) {
		return nil, ErrInviteExpired
	}
	if uses < 1 {
		return nil, ErrInviteUsed
	}
	if name != "" && name != agent.Name {
		return nil, ErrInviteInvalid
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `UPDATE invites SET uses_left=uses_left-1 WHERE code_hash=? AND uses_left>0`, hashToken(code)); err != nil {
		return nil, err
	}
	var joinKind string
	if err = tx.QueryRowContext(ctx, `SELECT kind FROM rooms WHERE id=?`, room).Scan(&joinKind); err != nil {
		return nil, err
	}
	if joinKind == "federated_dm" {
		return nil, ErrForbidden
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agents(id,name,kind,token_hash,status,created_at,last_seen) VALUES(?,?,?,?,?,?,?)`, agent.ID, agent.Name, agent.Kind, hashToken(token), protocol.StatusOnline, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrConflict
		}
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO memberships(room_id,agent_id,role,joined_at) VALUES(?,?,?,?)`, room, agent.ID, "member", now.Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}
	if err = insertAudit(ctx, tx, agent.ID, "agent.joined", map[string]any{"agent": agent.ID, "name": agent.Name, "room": room}); err != nil {
		return nil, err
	}
	rooms, err := roomsForAgent(ctx, tx, agent.ID)
	if err != nil {
		return nil, err
	}
	return rooms, tx.Commit()
}

func (s *Store) Rooms(ctx context.Context, agentID string) ([]protocol.Room, error) {
	return roomsForAgent(ctx, s.db, agentID, s.federationDM.Load())
}
func roomsForAgent(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, agentID string, dmEnabled ...bool) ([]protocol.Room, error) {
	enabled := len(dmEnabled) > 0 && dmEnabled[0]
	rows, err := q.QueryContext(ctx, `SELECT r.id,r.name,r.topic,r.kind,r.created_at FROM rooms r JOIN memberships m ON m.room_id=r.id WHERE m.agent_id=? AND r.archived_at IS NULL`+dmReadPredicate("m.room_id")+` ORDER BY r.id`, agentID, enabled, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Room{}
	for rows.Next() {
		var r protocol.Room
		var created string
		if err = rows.Scan(&r.ID, &r.Name, &r.Topic, &r.Kind, &created); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRooms returns non-archived rooms for admin and agent-directory views.
func (s *Store) ListRooms(ctx context.Context) ([]protocol.Room, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,topic,kind,created_at FROM rooms WHERE archived_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Room{}
	for rows.Next() {
		var r protocol.Room
		var c string
		if err = rows.Scan(&r.ID, &r.Name, &r.Topic, &r.Kind, &c); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) AgentByName(ctx context.Context, name string) (protocol.Agent, error) {
	var a protocol.Agent
	var seen string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,kind,status,last_seen FROM agents WHERE name=? AND revoked_at IS NULL`, name).
		Scan(&a.ID, &a.Name, &a.Kind, &a.Status, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Agent{}, ErrNotFound
	}
	if err != nil {
		return protocol.Agent{}, err
	}
	a.LastSeen, _ = time.Parse(time.RFC3339Nano, seen)
	return a, nil
}

func (s *Store) RoomByID(ctx context.Context, id string) (protocol.Room, error) {
	var r protocol.Room
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,topic,kind,created_at FROM rooms WHERE id=? AND archived_at IS NULL`, id).
		Scan(&r.ID, &r.Name, &r.Topic, &r.Kind, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Room{}, ErrNotFound
	}
	if err != nil {
		return protocol.Room{}, err
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return r, nil
}

// RoomReceipts returns each member's monotonic read cursor for a room.
func (s *Store) RoomReceipts(ctx context.Context, roomID string) ([]map[string]any, error) {
	var private int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms WHERE id=? AND kind='federated_dm'`, roomID).Scan(&private); err != nil {
		return nil, err
	}
	if private != 0 {
		return nil, ErrForbidden
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.agent_id,a.name,m.last_ack_seq FROM memberships m JOIN agents a ON a.id=m.agent_id WHERE m.room_id=? ORDER BY a.name`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name string
		var seq int64
		if err := rows.Scan(&id, &name, &seq); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"agent_id": id, "name": name, "last_ack_seq": seq})
	}
	return out, rows.Err()
}

func (s *Store) ListAgents(ctx context.Context) ([]protocol.Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,kind,status,last_seen FROM agents WHERE revoked_at IS NULL AND id NOT IN (SELECT bridge_agent_id FROM federation_dm_conversations) ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Agent{}
	now := time.Now().UTC()
	for rows.Next() {
		var a protocol.Agent
		var seen string
		if err = rows.Scan(&a.ID, &a.Name, &a.Kind, &a.Status, &seen); err != nil {
			return nil, err
		}
		a.LastSeen, _ = time.Parse(time.RFC3339Nano, seen)
		if now.Sub(a.LastSeen) > 5*time.Minute {
			a.Status = protocol.StatusOffline
		} else if now.Sub(a.LastSeen) > time.Minute {
			a.Status = protocol.StatusIdle
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateUserAgent creates an ordinary agent and returns its token once. This is
// the one-step admin path for onboarding a human user.
func (s *Store) CreateUserAgent(ctx context.Context, name, kind, room, actor string) (protocol.Agent, string, error) {
	name = strings.TrimSpace(name)
	kind = strings.TrimSpace(kind)
	room = strings.TrimSpace(room)
	if err := protocol.ValidateAgentName(name); err != nil {
		return protocol.Agent{}, "", err
	}
	if kind == "" {
		kind = "human"
	}
	if len(kind) > protocol.MaxNameLength {
		return protocol.Agent{}, "", errors.New("agent kind is too long")
	}
	if room == "" {
		room = protocol.DefaultRoom
	}
	if err := protocol.ValidateAgentName(room); err != nil {
		return protocol.Agent{}, "", err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Agent{}, "", err
	}
	defer tx.Rollback()
	var archived sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT archived_at FROM rooms WHERE id=?`, room).Scan(&archived); errors.Is(err, sql.ErrNoRows) || archived.Valid {
		return protocol.Agent{}, "", ErrNotFound
	} else if err != nil {
		return protocol.Agent{}, "", err
	}
	token := protocol.NewToken("agt")
	now := time.Now().UTC()
	agent := protocol.Agent{ID: "agt_" + protocol.NewID(), Name: name, Kind: kind, Status: protocol.StatusOffline, LastSeen: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id,name,kind,token_hash,status,created_at,last_seen) VALUES(?,?,?,?,?,?,?)`,
		agent.ID, agent.Name, agent.Kind, hashToken(token), agent.Status, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return protocol.Agent{}, "", ErrConflict
		}
		return protocol.Agent{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memberships(room_id,agent_id,role,joined_at) VALUES(?,?,?,?)`, room, agent.ID, "member", now.Format(time.RFC3339Nano)); err != nil {
		return protocol.Agent{}, "", err
	}
	if err := insertAudit(ctx, tx, actor, "agent.created_by_admin", map[string]any{"agent": agent.ID, "name": agent.Name, "room": room}); err != nil {
		return protocol.Agent{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Agent{}, "", err
	}
	return agent, token, nil
}

// RotateAgentToken issues a new agent token and invalidates the previous one.
// The plaintext token is returned exactly once; only its hash is stored.
func (s *Store) RotateAgentToken(ctx context.Context, id, actor string) (string, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var kind string
	var revoked sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT kind,revoked_at FROM agents WHERE id=?`, id).Scan(&kind, &revoked)
	if errors.Is(err, sql.ErrNoRows) || revoked.Valid {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if kind == "federation" || kind == "system" || kind == "bridge" {
		return "", ErrForbidden
	}
	token := protocol.NewToken("agt")
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET token_hash=? WHERE id=?`, hashToken(token), id); err != nil {
		return "", err
	}
	if err := insertAudit(ctx, tx, actor, "agent.token_rotated", map[string]any{"agent": id}); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) RevokeAgent(ctx context.Context, id, actor string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE agents SET revoked_at=?,status=? WHERE id=? AND revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano), protocol.StatusOffline, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if err = insertAudit(ctx, tx, actor, "agent.revoked", map[string]any{"agent": id}); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateRoom creates a named room and optionally adds initial members.
func (s *Store) CreateRoom(ctx context.Context, r protocol.Room, members []string, actor string) error {
	if r.Kind == "federated_dm" {
		return ErrForbidden
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range members {
		var scoped int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_dm_conversations WHERE bridge_agent_id=?`, id).Scan(&scoped); err != nil {
			return err
		}
		if scoped != 0 {
			return ErrForbidden
		}
	}
	if r.ID == "" {
		return errors.New("room id required")
	}
	if r.Name == "" {
		r.Name = r.ID
	}
	if r.Kind == "" {
		r.Kind = "room"
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO rooms(id,name,topic,kind,created_at) VALUES(?,?,?,?,?)`, r.ID, r.Name, r.Topic, r.Kind, r.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrConflict
		}
		return err
	}
	for _, id := range members {
		if _, err = tx.ExecContext(ctx, `INSERT INTO memberships(room_id,agent_id,role,joined_at) VALUES(?,?,?,?)`, r.ID, id, "member", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if err = insertAudit(ctx, tx, actor, "room.created", map[string]any{"room": r.ID}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ArchiveRoom(ctx context.Context, id, actor string) error {
	if id == protocol.DefaultRoom {
		return ErrForbidden
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE rooms SET archived_at=? WHERE id=? AND archived_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if err = insertAudit(ctx, tx, actor, "room.archived", map[string]any{"room": id}); err != nil {
		return err
	}
	return tx.Commit()
}

// SendResult includes whether this request replayed an idempotent message.
type SendResult struct {
	Message  protocol.Message
	Replayed bool
}

func (s *Store) SendMessage(ctx context.Context, agentID, roomID string, req protocol.SendMessageRequest, mentions []string) (SendResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SendResult{}, err
	}
	defer tx.Rollback()
	var roomName, roomTopic, roomKind, roomCreated string
	var archived sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT name,topic,kind,created_at,archived_at FROM rooms WHERE id=?`, roomID).Scan(&roomName, &roomTopic, &roomKind, &roomCreated, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return SendResult{}, ErrNotFound
	}
	if err != nil {
		return SendResult{}, err
	}
	if archived.Valid {
		return SendResult{}, ErrNotFound
	}
	if roomKind == "federated_dm" {
		if err = authorizeDMRoom(ctx, tx, s.federationDM.Load(), agentID, roomID); err != nil {
			return SendResult{}, err
		}
		if len(req.Files) > 0 || req.Meta.Activity != nil || req.Meta.Federation != nil || req.ReplyTo != "" || len(req.To) > 0 || strings.TrimSpace(req.Text) == "" || len(req.Text) > protocol.MaxTextBytes {
			return SendResult{}, ErrInvalidTarget
		}
		mentions = nil
	}
	var joined int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM memberships WHERE room_id=? AND agent_id=?`, roomID, agentID).Scan(&joined); errors.Is(err, sql.ErrNoRows) {
		return SendResult{}, ErrForbidden
	} else if err != nil {
		return SendResult{}, err
	}
	var sender protocol.AgentRef
	if err = tx.QueryRowContext(ctx, `SELECT id,name,kind FROM agents WHERE id=? AND revoked_at IS NULL`, agentID).Scan(&sender.ID, &sender.Name, &sender.Kind); errors.Is(err, sql.ErrNoRows) {
		return SendResult{}, ErrForbidden
	} else if err != nil {
		return SendResult{}, err
	}
	if req.ClientMsgID != "" {
		m, e := messageByClientID(ctx, tx, agentID, roomID, req.ClientMsgID)
		if e == nil {
			return SendResult{Message: m, Replayed: true}, nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return SendResult{}, e
		}
	}
	files := make([]protocol.FileRef, 0, len(req.Files))
	for _, fileID := range req.Files {
		var ref protocol.FileRef
		err = tx.QueryRowContext(ctx, `SELECT id,name,size,sha256,mime FROM files WHERE id=?`, fileID).Scan(&ref.ID, &ref.Name, &ref.Size, &ref.SHA256, &ref.Mime)
		if errors.Is(err, sql.ErrNoRows) {
			return SendResult{}, ErrFileNotFound
		}
		if err != nil {
			return SendResult{}, err
		}
		files = append(files, ref)
	}
	mentionIDs := make([]string, 0, len(mentions))
	seen := map[string]bool{}
	for _, name := range mentions {
		if seen[name] {
			continue
		}
		seen[name] = true
		var id string
		e := tx.QueryRowContext(ctx, `SELECT a.id FROM agents a JOIN memberships m ON m.agent_id=a.id WHERE m.room_id=? AND a.name=? AND a.revoked_at IS NULL`, roomID, name).Scan(&id)
		if errors.Is(e, sql.ErrNoRows) {
			return SendResult{}, ErrInvalidTarget
		}
		if e != nil {
			return SendResult{}, e
		}
		mentionIDs = append(mentionIDs, id)
	}
	var seq int64
	if err = tx.QueryRowContext(ctx, `UPDATE rooms SET next_seq=next_seq+1 WHERE id=? RETURNING next_seq`, roomID).Scan(&seq); err != nil {
		return SendResult{}, err
	}
	now := time.Now().UTC()
	meta, _ := json.Marshal(req.Meta)
	id := protocol.NewID()
	kind := protocol.KindChat
	_, err = tx.ExecContext(ctx, `INSERT INTO messages(id,room_id,seq,sender_id,kind,text,reply_to,meta_json,created_at,client_msg_id) VALUES(?,?,?,?,?,?,?,?,?,NULLIF(?,''))`, id, roomID, seq, agentID, kind, req.Text, req.ReplyTo, string(meta), now.Format(time.RFC3339Nano), req.ClientMsgID)
	if err != nil {
		return SendResult{}, err
	}
	for _, mid := range mentionIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO mentions(message_id,agent_id) VALUES(?,?)`, id, mid); err != nil {
			return SendResult{}, err
		}
	}
	for _, file := range files {
		if _, err = tx.ExecContext(ctx, `INSERT INTO attachments(message_id,file_id,display_name) VALUES(?,?,?)`, id, file.ID, file.Name); err != nil {
			return SendResult{}, err
		}
	}
	m := protocol.Message{ID: id, Room: roomID, Seq: seq, From: sender, Kind: kind, Text: req.Text, ReplyTo: req.ReplyTo, Mentions: append([]string(nil), mentions...), Files: files, Meta: req.Meta, TS: now}
	if err = s.enqueueFederation(ctx, tx, m, roomKind); err != nil {
		return SendResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return SendResult{}, err
	}
	return SendResult{Message: m}, nil
}

func messageByClientID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, agentID, roomID, clientID string) (protocol.Message, error) {
	var m protocol.Message
	var meta, created string
	err := q.QueryRowContext(ctx, `SELECT m.id,m.room_id,m.seq,a.id,a.name,a.kind,m.kind,m.text,m.reply_to,m.meta_json,m.created_at FROM messages m JOIN agents a ON a.id=m.sender_id WHERE m.sender_id=? AND m.room_id=? AND m.client_msg_id=?`, agentID, roomID, clientID).Scan(&m.ID, &m.Room, &m.Seq, &m.From.ID, &m.From.Name, &m.From.Kind, &m.Kind, &m.Text, &m.ReplyTo, &meta, &created)
	if err != nil {
		return m, err
	}
	_ = json.Unmarshal([]byte(meta), &m.Meta)
	m.TS, _ = time.Parse(time.RFC3339Nano, created)
	m.Files = []protocol.FileRef{}
	rows, err := q.QueryContext(ctx, `SELECT a.name FROM mentions x JOIN agents a ON a.id=x.agent_id WHERE x.message_id=? ORDER BY a.name`, m.ID)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			return m, err
		}
		m.Mentions = append(m.Mentions, n)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return m, err
	}
	if err = rows.Close(); err != nil {
		return m, err
	}
	if err = loadAttachments(ctx, q, &m); err != nil {
		return m, err
	}
	return m, nil
}

func (s *Store) History(ctx context.Context, agentID, roomID string, after, before int64, limit int) ([]protocol.Message, bool, error) {
	if err := authorizeDMRoom(ctx, s.db, s.federationDM.Load(), agentID, roomID); err != nil {
		return nil, false, err
	}
	var joined int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM memberships WHERE room_id=? AND agent_id=?`, roomID, agentID).Scan(&joined); errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrForbidden
	} else if err != nil {
		return nil, false, err
	}
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	query := `SELECT m.id,m.room_id,m.seq,a.id,a.name,a.kind,m.kind,m.text,m.reply_to,m.meta_json,m.created_at FROM messages m JOIN agents a ON a.id=m.sender_id WHERE m.room_id=? AND m.seq>?`
	args := []any{roomID, after}
	if before > 0 {
		query += ` AND m.seq<?`
		args = append(args, before)
	}
	query += dmReadPredicate("m.room_id")
	args = append(args, s.federationDM.Load(), agentID)
	query += ` ORDER BY m.seq LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []protocol.Message{}
	for rows.Next() {
		var m protocol.Message
		var meta, created string
		if err = rows.Scan(&m.ID, &m.Room, &m.Seq, &m.From.ID, &m.From.Name, &m.From.Kind, &m.Kind, &m.Text, &m.ReplyTo, &meta, &created); err != nil {
			return nil, false, err
		}
		_ = json.Unmarshal([]byte(meta), &m.Meta)
		m.TS, _ = time.Parse(time.RFC3339Nano, created)
		m.Files = []protocol.FileRef{}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	rows.Close()
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	for i := range out {
		if err = loadMentions(ctx, s.db, &out[i]); err != nil {
			return nil, false, err
		}
		if err = loadAttachments(ctx, s.db, &out[i]); err != nil {
			return nil, false, err
		}
	}
	return out, more, nil
}

func (s *Store) SearchMessages(ctx context.Context, agentID, roomID, query string, limit int) ([]protocol.Message, error) {
	if err := authorizeDMRoom(ctx, s.db, s.federationDM.Load(), agentID, roomID); err != nil {
		return nil, err
	}
	var joined int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM memberships WHERE room_id=? AND agent_id=?`, roomID, agentID).Scan(&joined); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrForbidden
	} else if err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	rows, err := s.db.QueryContext(ctx, `SELECT m.id,m.room_id,m.seq,a.id,a.name,a.kind,m.kind,m.text,m.reply_to,m.meta_json,m.created_at FROM messages m JOIN agents a ON a.id=m.sender_id WHERE m.room_id=? AND m.text LIKE ? ESCAPE '\' COLLATE NOCASE`+dmReadPredicate("m.room_id")+` ORDER BY m.seq DESC LIMIT ?`, roomID, "%"+escaped+"%", s.federationDM.Load(), agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Message{}
	for rows.Next() {
		var m protocol.Message
		var meta, created string
		if err := rows.Scan(&m.ID, &m.Room, &m.Seq, &m.From.ID, &m.From.Name, &m.From.Kind, &m.Kind, &m.Text, &m.ReplyTo, &meta, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(meta), &m.Meta)
		m.TS, _ = time.Parse(time.RFC3339Nano, created)
		m.Files = []protocol.FileRef{}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		if err := loadMentions(ctx, s.db, &out[i]); err != nil {
			return nil, err
		}
		if err := loadAttachments(ctx, s.db, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) Inbox(ctx context.Context, agentID string, limit int, roomFilter []string, cursor map[string]int64) ([]protocol.Message, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	members, err := s.db.QueryContext(ctx, `SELECT room_id,last_ack_seq FROM memberships mb WHERE agent_id=?`+dmReadPredicate("mb.room_id")+` ORDER BY room_id`, agentID, s.federationDM.Load(), agentID)
	if err != nil {
		return nil, err
	}
	type membership struct {
		room string
		ack  int64
	}
	var ms []membership
	for members.Next() {
		var m membership
		if err = members.Scan(&m.room, &m.ack); err != nil {
			members.Close()
			return nil, err
		}
		ms = append(ms, m)
	}
	err = members.Err()
	members.Close()
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, r := range roomFilter {
		allowed[r] = true
	}
	var clauses []string
	args := []any{agentID}
	for _, m := range ms {
		if len(roomFilter) > 0 && !allowed[m.room] {
			continue
		}
		threshold := m.ack
		if cursor[m.room] > threshold {
			threshold = cursor[m.room]
		}
		clauses = append(clauses, `(m.room_id=? AND m.seq>?)`)
		args = append(args, m.room, threshold)
	}
	if len(clauses) == 0 {
		return []protocol.Message{}, nil
	}
	query := `SELECT m.id,m.room_id,m.seq,a.id,a.name,a.kind,m.kind,m.text,m.reply_to,m.meta_json,m.created_at FROM memberships mb JOIN messages m ON m.room_id=mb.room_id JOIN agents a ON a.id=m.sender_id WHERE mb.agent_id=? AND (` + strings.Join(clauses, ` OR `) + `)` + dmReadPredicate("m.room_id") + ` ORDER BY m.created_at,m.room_id,m.seq LIMIT ?`
	args = append(args, s.federationDM.Load(), agentID)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Message{}
	for rows.Next() {
		var m protocol.Message
		var meta, created string
		if err = rows.Scan(&m.ID, &m.Room, &m.Seq, &m.From.ID, &m.From.Name, &m.From.Kind, &m.Kind, &m.Text, &m.ReplyTo, &meta, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(meta), &m.Meta)
		m.TS, _ = time.Parse(time.RFC3339Nano, created)
		m.Files = []protocol.FileRef{}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		if err = loadMentions(ctx, s.db, &out[i]); err != nil {
			return nil, err
		}
		if err = loadAttachments(ctx, s.db, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func loadMentions(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, m *protocol.Message) error {
	rows, err := q.QueryContext(ctx, `SELECT a.name FROM mentions x JOIN agents a ON a.id=x.agent_id WHERE x.message_id=? ORDER BY a.name`, m.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	m.Mentions = []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return err
		}
		m.Mentions = append(m.Mentions, name)
	}
	return rows.Err()
}

func loadAttachments(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, m *protocol.Message) error {
	rows, err := q.QueryContext(ctx, `SELECT f.id,x.display_name,f.size,f.sha256,f.mime FROM attachments x JOIN files f ON f.id=x.file_id WHERE x.message_id=? ORDER BY f.id`, m.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	m.Files = []protocol.FileRef{}
	for rows.Next() {
		var f protocol.FileRef
		if err = rows.Scan(&f.ID, &f.Name, &f.Size, &f.SHA256, &f.Mime); err != nil {
			return err
		}
		m.Files = append(m.Files, f)
	}
	return rows.Err()
}

func (s *Store) Ack(ctx context.Context, agentID string, acks map[string]int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for room, seq := range acks {
		if err = authorizeDMRoom(ctx, tx, s.federationDM.Load(), agentID, room); err != nil {
			return err
		}
		var max int64
		if err = tx.QueryRowContext(ctx, `SELECT next_seq FROM rooms WHERE id=? AND archived_at IS NULL`, room).Scan(&max); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if seq > max {
			seq = max
		}
		res, e := tx.ExecContext(ctx, `UPDATE memberships SET last_ack_seq=MAX(last_ack_seq,?) WHERE room_id=? AND agent_id=?`, seq, room, agentID)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrForbidden
		}
	}
	return tx.Commit()
}

type AuditEvent struct {
	ID     string         `json:"id"`
	TS     time.Time      `json:"ts"`
	Actor  string         `json:"actor_id"`
	Action string         `json:"action"`
	Detail map[string]any `json:"detail"`
}

func insertAudit(ctx context.Context, tx *sql.Tx, actor, action string, detail any) error {
	b, _ := json.Marshal(detail)
	_, err := tx.ExecContext(ctx, `INSERT INTO audit(id,ts,actor_id,action,detail_json) VALUES(?,?,?,?,?)`, protocol.NewID(), time.Now().UTC().Format(time.RFC3339Nano), actor, action, string(b))
	return err
}
func (s *Store) Audit(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,ts,actor_id,action,detail_json FROM audit ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		var ts, detail string
		if err = rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &detail); err != nil {
			return nil, err
		}
		e.TS, _ = time.Parse(time.RFC3339Nano, ts)
		_ = json.Unmarshal([]byte(detail), &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}
