package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/store"
)

type federationReadPayload struct {
	Version      int    `json:"version"`
	OriginNodeID string `json:"origin_node_id"`
	Room         string `json:"room"`
	AgentID      string `json:"agent_id"`
	ReadSeq      int64  `json:"read_seq"`
}

func (s *Server) handleFederationRead(w http.ResponseWriter, r *http.Request) {
	f := s.opts.Federation
	if f == nil || !f.opts.ReadReceipts {
		writeError(w, 404, "not_found", "federation read receipts are disabled", nil)
		return
	}
	node, ok := f.authenticatedPeer(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	var p federationReadPayload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF {
		writeError(w, 400, "invalid_request", "invalid read receipt", nil)
		return
	}
	if p.Version != 1 || p.OriginNodeID != node || p.Room == "" || p.AgentID == "" || len(p.AgentID) > 128 || p.ReadSeq < 0 {
		writeError(w, 400, "invalid_request", "invalid read receipt fields", nil)
		return
	}
	if err := s.store.RecordFederationRead(r.Context(), node, p.Room, p.AgentID, p.ReadSeq); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			writeError(w, 403, "forbidden", "no active room link", nil)
			return
		}
		s.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ForwardRead is best-effort advisory fanout. It never blocks the local
// MarkRead response and never re-forwards an inbound remote receipt.
func (f *Federation) ForwardRead(room, agentID string, seq int64) {
	if f == nil || !f.opts.ReadReceipts || room == "" || agentID == "" || seq < 0 {
		return
	}
	targets, err := f.store.FederationReadTargets(context.Background(), room)
	if err != nil {
		return
	}
	for _, target := range targets {
		select {
		case f.readSlots <- struct{}{}:
		default:
			return
		}
		target := target
		go func() {
			defer func() { <-f.readSlots }()
			peer, ok := f.peer(target.PeerID)
			client := f.clients[target.PeerID]
			if !ok || client == nil {
				return
			}
			p := federationReadPayload{Version: 1, OriginNodeID: f.nodeID, Room: target.RemoteRoom, AgentID: agentID, ReadSeq: seq}
			body, _ := json.Marshal(p)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(peer.BaseURL, "/")+"/federation/v1/read", bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Authorization", "Bearer "+peer.Secret)
			req.Header.Set("X-AgentChat-Node", f.nodeID)
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
				resp.Body.Close()
			}
		}()
	}
}
