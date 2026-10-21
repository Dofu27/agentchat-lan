package store

// F3 has no schema or persistent configuration. These helpers only read the
// existing approved room/DM scopes; events never use SendMessage or an outbox.
import (
	"context"
	"database/sql"
	"errors"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

type FederationTypingLink struct {
	PeerID         string `json:"peer_id,omitempty"`
	Kind           string `json:"kind"`
	LinkID         string `json:"link_id"`
	Epoch          int64  `json:"epoch"`
	LocalRoom      string `json:"local_room,omitempty"`
	RemoteRoom     string `json:"remote_room,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}
type FederationTypingScope struct {
	Kind               string `json:"kind"`
	LinkID             string `json:"link_id"`
	Epoch              int64  `json:"epoch"`
	SourceRoom         string `json:"source_room,omitempty"`
	DestinationRoom    string `json:"destination_room,omitempty"`
	ConversationID     string `json:"conversation_id,omitempty"`
	SourceAgentID      string `json:"source_agent_id,omitempty"`
	DestinationAgentID string `json:"destination_agent_id,omitempty"`
}

func (s *Store) TypingScope(ctx context.Context, peer string, l FederationTypingLink) (FederationTypingScope, string, error) {
	wire := FederationTypingScope{Kind: l.Kind, LinkID: l.LinkID, Epoch: l.Epoch, ConversationID: l.ConversationID}
	if protocol.ValidateAgentName(l.LinkID) != nil || l.Epoch < 1 {
		return wire, "", ErrInvalidTarget
	}
	if l.Kind == "federated_dm" {
		if !s.federationDM.Load() || protocol.ValidateAgentName(l.ConversationID) != nil || l.RemoteRoom != "" {
			return wire, "", ErrForbidden
		}
		var room string
		err := s.db.QueryRowContext(ctx, `SELECT room_id,local_agent_id,remote_agent_id FROM federation_dm_access WHERE peer_id=? AND conversation_id=? AND link_id=? AND epoch=?`, peer, l.ConversationID, l.LinkID, l.Epoch).Scan(&room, &wire.SourceAgentID, &wire.DestinationAgentID)
		if err != nil {
			return wire, "", ErrForbidden
		}
		if l.LocalRoom != "" && l.LocalRoom != room {
			return wire, "", ErrForbidden
		}
		return wire, room, nil
	}
	if l.Kind != "room" || l.ConversationID != "" || protocol.ValidateAgentName(l.LocalRoom) != nil || protocol.ValidateAgentName(l.RemoteRoom) != nil {
		return wire, "", ErrInvalidTarget
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM federation_room_links l JOIN federation_peers p ON p.node_id=l.peer_id JOIN rooms r ON r.id=l.local_room CROSS JOIN federation_node n JOIN agents a ON a.id=n.bridge_agent_id JOIN memberships m ON m.agent_id=a.id AND m.room_id=r.id WHERE l.peer_id=? AND l.local_room=? AND l.remote_room=? AND l.enabled=1 AND p.enabled=1 AND r.kind='room' AND r.archived_at IS NULL AND a.revoked_at IS NULL`, peer, l.LocalRoom, l.RemoteRoom).Scan(&n)
	if err != nil {
		return wire, "", err
	}
	if n != 1 {
		return wire, "", ErrForbidden
	}
	wire.SourceRoom = l.LocalRoom
	wire.DestinationRoom = l.RemoteRoom
	return wire, l.LocalRoom, nil
}
func (s *Store) TypingActor(ctx context.Context, peer string, l FederationTypingLink, actor string) (protocol.AgentRef, error) {
	scope, room, err := s.TypingScope(ctx, peer, l)
	if err != nil {
		return protocol.AgentRef{}, err
	}
	if scope.Kind == "federated_dm" && scope.SourceAgentID != actor {
		return protocol.AgentRef{}, ErrForbidden
	}
	var a protocol.AgentRef
	err = s.db.QueryRowContext(ctx, `SELECT a.id,a.name,a.kind FROM agents a JOIN memberships m ON m.agent_id=a.id WHERE a.id=? AND m.room_id=? AND a.revoked_at IS NULL AND a.kind NOT IN ('federation','bridge','system')`, actor, room).Scan(&a.ID, &a.Name, &a.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrForbidden
	}
	return a, err
}
