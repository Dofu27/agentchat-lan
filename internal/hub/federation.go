package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func (s *Server) handleFederationStatus(w http.ResponseWriter, r *http.Request) {
	nodeID, err := s.store.NodeID(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	peers, err := s.store.ListFederationPeers(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	links, err := s.store.ListFederationLinks(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	counts, err := s.store.FederationOutboxCounts(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id": nodeID,
		"peers":   peers,
		"links":   links,
		"outbox":  counts,
	})
}

func (s *Server) handleCreateFederationPeer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		URL          string `json:"url"`
		Secret       string `json:"secret"`
		RemoteNodeID string `json:"remote_node_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.URL = strings.TrimRight(strings.TrimSpace(req.URL), "/")
	parsed, err := url.Parse(req.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "url must be an http(s) URL", nil)
		return
	}
	peer, err := s.store.CreateFederationPeer(r.Context(), req.Name, req.URL, req.Secret, req.RemoteNodeID)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, peer)
}

func (s *Server) handleCreateFederationLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LocalRoom  string `json:"local_room"`
		PeerID     string `json:"peer_id"`
		RemoteRoom string `json:"remote_room"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	link, err := s.store.CreateFederationLink(r.Context(), req.LocalRoom, req.PeerID, req.RemoteRoom, "admin")
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (s *Server) handleFederationInbound(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "federation bearer token required", nil)
		return
	}
	peer, err := s.store.FederationPeerBySecret(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unknown federation peer", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var payload store.FederationPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid federation payload", nil)
		return
	}
	if payload.OriginNode == "" || payload.OriginID == "" || payload.Room == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "origin_node, origin_message_id, and room are required", nil)
		return
	}
	ownNode, err := s.store.NodeID(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	if payload.OriginNode == ownNode {
		writeError(w, http.StatusBadRequest, "invalid_request", "federation loop rejected", nil)
		return
	}
	if peer.RemoteNodeID != "" && payload.OriginNode != peer.RemoteNodeID {
		writeError(w, http.StatusForbidden, "forbidden", "payload origin does not match registered peer", nil)
		return
	}
	if len(payload.Text) > protocol.MaxTextBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "federated text is too large", nil)
		return
	}
	link, err := s.store.FederationLinkForPeer(r.Context(), peer.ID, payload.Room)
	if err != nil {
		s.storeError(w, err)
		return
	}
	message, replayed, err := s.store.InsertFederatedMessage(r.Context(), link.ID, payload)
	if err != nil {
		s.internalError(w, err)
		return
	}
	if !replayed {
		s.broadcast()
	}
	writeJSON(w, http.StatusOK, map[string]any{"replayed": replayed, "message_id": message.ID})
}

func (s *Server) runFederation(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.deliverFederation(ctx)
		}
	}
}

func (s *Server) deliverFederation(ctx context.Context) {
	entries, err := s.store.FederationOutbox(ctx, 20)
	if err != nil {
		if s.opts.Logger != nil {
			s.opts.Logger.Printf("federation outbox: %v", err)
		}
		return
	}
	for _, entry := range entries {
		if err := s.deliverFederationEntry(ctx, entry); err != nil && s.opts.Logger != nil {
			s.opts.Logger.Printf("federation deliver %s: %v", entry.ID, err)
		}
	}
}

func (s *Server) deliverFederationEntry(ctx context.Context, entry store.FederationOutboxEntry) error {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, strings.TrimRight(entry.PeerURL, "/")+"/v1/federation/messages", bytes.NewReader(entry.Payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	secret := s.opts.FederationSecrets[entry.PeerName]
	if secret == "" {
		return s.retryFederation(ctx, entry, fmt.Errorf("no configured secret for peer %s", entry.PeerName))
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return s.retryFederation(ctx, entry, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return s.store.MarkFederationDelivered(ctx, entry.ID)
	}
	return s.retryFederation(ctx, entry, fmt.Errorf("peer %s returned HTTP %d", entry.PeerName, resp.StatusCode))
}

func (s *Server) retryFederation(ctx context.Context, entry store.FederationOutboxEntry, cause error) error {
	attempts := entry.Attempts + 1
	delay := time.Duration(1<<uint(min(attempts, 6))) * time.Second
	if delay > 60*time.Second {
		delay = 60 * time.Second
	}
	_ = s.store.MarkFederationFailed(ctx, entry.ID, attempts, time.Now().Add(delay), cause.Error())
	return cause
}
