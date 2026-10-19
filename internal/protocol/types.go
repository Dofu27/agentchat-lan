// Package protocol contains the wire types shared by the hub, CLI, and MCP
// server. Keep this package free of business logic and third-party imports.
package protocol

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

const (
	// APIVersion is the current wire contract version.
	APIVersion = "v1"

	// DefaultRoom is created on a fresh hub.
	DefaultRoom = "general"

	// MaxTextBytes is the maximum message body size.
	MaxTextBytes = 64 * 1024

	// MaxFilesPerMessage is the maximum number of attachments on one message.
	MaxFilesPerMessage = 8

	// MaxFileSize is the default per-file cap (512 MiB).
	MaxFileSize int64 = 512 << 20

	// MaxHops is the hard limit for agent-to-agent replies.
	MaxHops = 12

	// RecommendedHops is the soft limit clients should enforce.
	RecommendedHops = 6

	// MaxNameLength is the maximum agent or room name length.
	MaxNameLength = 64
)

// Message kinds.
const (
	KindChat   = "chat"
	KindEvent  = "event"
	KindSystem = "system"
)

// Intents describe what the sender expects from the receiver.
const (
	IntentAsk     = "ask"
	IntentAnswer  = "answer"
	IntentNotify  = "notify"
	IntentHandoff = "handoff"
)

// Agent status values.
const (
	StatusOnline  = "online"
	StatusIdle    = "idle"
	StatusOffline = "offline"
)

var (
	agentNameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)
	mentionRE   = regexp.MustCompile(`@([a-zA-Z0-9_-](?:[a-zA-Z0-9._-]{0,62}[a-zA-Z0-9_-])?)`)
)

// AgentRef is the compact sender representation embedded in a message.
type AgentRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Agent is the full agent record returned by presence endpoints.
type Agent struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	LastSeen     time.Time `json:"last_seen"`
	Capabilities []string  `json:"capabilities,omitempty"`
}

// Room is a chat room or a direct-message conversation.
type Room struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Topic     string    `json:"topic,omitempty"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

// FileRef is the metadata for an uploaded file.
type FileRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mime   string `json:"mime,omitempty"`
}

// MessageMeta carries optional conversation-control metadata.
type MessageMeta struct {
	Intent         string `json:"intent,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Hops           int    `json:"hops,omitempty"`
}

// Message is one chat message.
type Message struct {
	ID       string      `json:"id"`
	Room     string      `json:"room"`
	Seq      int64       `json:"seq"`
	From     AgentRef    `json:"from"`
	Kind     string      `json:"kind"`
	Text     string      `json:"text"`
	ReplyTo  string      `json:"reply_to,omitempty"`
	Mentions []string    `json:"mentions,omitempty"`
	Files    []FileRef   `json:"files,omitempty"`
	Meta     MessageMeta `json:"meta,omitempty"`
	TS       time.Time   `json:"ts"`
}

// SendMessageRequest is the body of POST /v1/rooms/{room}/messages.
type SendMessageRequest struct {
	ClientMsgID string      `json:"client_msg_id,omitempty"`
	Text        string      `json:"text"`
	ReplyTo     string      `json:"reply_to,omitempty"`
	Files       []string    `json:"files,omitempty"`
	To          []string    `json:"to,omitempty"`
	Meta        MessageMeta `json:"meta,omitempty"`
}

// JoinRequest is the body of POST /v1/join.
type JoinRequest struct {
	InviteCode   string   `json:"invite_code"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// JoinResponse is returned once, with a token that is never shown again.
type JoinResponse struct {
	Agent      Agent  `json:"agent"`
	Token      string `json:"token"`
	Rooms      []Room `json:"rooms"`
	HubVersion string `json:"hub_version"`
}

// InboxResponse is returned by GET /v1/inbox.
type InboxResponse struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"next_cursor,omitempty"`
	ServerTime time.Time `json:"server_time"`
}

// AckRequest advances an agent's per-room cursor.
type AckRequest struct {
	Acks map[string]int64 `json:"acks"`
}

// RoomsResponse is returned by GET /v1/rooms.
type RoomsResponse struct {
	Rooms []Room `json:"rooms"`
}

// AgentsResponse is returned by GET /v1/agents.
type AgentsResponse struct {
	Agents []Agent `json:"agents"`
}

// MeResponse is returned by GET /v1/me.
type MeResponse struct {
	Agent  Agent    `json:"agent"`
	Rooms  []string `json:"rooms"`
	Limits Limits   `json:"limits"`
}

// Limits describes server-side defaults so clients can display them.
type Limits struct {
	MaxTextBytes       int   `json:"max_text_bytes"`
	MaxFilesPerMessage int   `json:"max_files_per_message"`
	MaxFileSize        int64 `json:"max_file_size"`
	MaxHops            int   `json:"max_hops"`
}

// DefaultLimits returns the contract defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxTextBytes:       MaxTextBytes,
		MaxFilesPerMessage: MaxFilesPerMessage,
		MaxFileSize:        MaxFileSize,
		MaxHops:            MaxHops,
	}
}

// HealthResponse is returned by GET /healthz.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// ErrorBody is the machine-readable part of an error response.
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// ErrorResponse is the envelope for all API errors.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ValidateAgentName checks the portable agent-name rules.
func ValidateAgentName(name string) error {
	if !agentNameRE.MatchString(name) {
		return fmt.Errorf("name must match ^[a-zA-Z0-9._-]{1,%d}$", MaxNameLength)
	}
	return nil
}

// MentionNames extracts unique @mentions from text, preserving order.
func MentionNames(text string) []string {
	matches := mentionRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// NewToken returns a prefixed, cryptographically random token.
func NewToken(prefix string) string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b[:])
}

// NewID returns a ULID-compatible 26-character identifier.
func NewID() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}

	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	v := new(big.Int).SetBytes(b[:])
	v.Lsh(v, 2) // 128 bits -> 130 bits, encoded as 26 base32 chars
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[v.Uint64()&31]
		v.Rsh(v, 5)
	}
	return string(out)
}

// MarshalJSON is a small helper for tests and fixtures.
func MarshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}
