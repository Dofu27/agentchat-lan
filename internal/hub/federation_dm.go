package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/store"
)

func (s *Server) handleRemoteDM(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	agent := agentFrom(r.Context()).ID
	if r.Method == "GET" {
		contacts, err := s.store.FederatedDMContacts(r.Context(), agent)
		if err != nil {
			s.internalError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"contacts": contacts})
		return
	}
	var req struct {
		ConversationID string `json:"conversation_id"`
		Epoch          int64  `json:"epoch"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if r.Method == "DELETE" {
		if err := s.store.LeaveFederatedDM(r.Context(), agent, req.ConversationID, req.Epoch); err != nil {
			s.storeError(w, err)
			return
		}
		s.broadcast()
		w.WriteHeader(204)
		return
	}
	room, err := s.store.OpenFederatedDM(r.Context(), agent, req.ConversationID, req.Epoch)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, 200, room)
}
func (s *Server) handleFederationDM(w http.ResponseWriter, r *http.Request) {
	node, ok := s.opts.Federation.authenticatedPeer(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	if !s.allow("federation-dm:"+node, 120, 20) {
		http.Error(w, "rate limited", 429)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(s.opts.Federation.opts.RequestTimeout))
	defer controller.SetReadDeadline(time.Time{})
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
	dec.DisallowUnknownFields()
	var e store.FederationDMEnvelope
	if dec.Decode(&e) != nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid DM envelope", 400)
		return
	}
	receipt, err := s.store.ImportFederationDM(r.Context(), node, e)
	if err != nil {
		if errors.Is(err, store.ErrFederationConflict) {
			writeError(w, 409, "conflict", "conflicting federation delivery", nil)
			return
		}
		s.storeError(w, err)
		return
	}
	if !receipt.Replayed {
		s.broadcast()
	}
	writeJSON(w, 200, receipt)
}
func (f *Federation) runDM(ctx context.Context) error {
	timer := time.NewTicker(f.opts.PollInterval)
	defer timer.Stop()
	for ctx.Err() == nil {
		j, err := f.store.ClaimFederationDMJob(ctx, time.Now(), f.opts.LeaseDuration)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if j == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-timer.C:
			}
			continue
		}
		delivered, permanent, reason := f.deliverDM(ctx, *j)
		if ctx.Err() != nil {
			return nil
		}
		delay := min(f.opts.RetryBase*time.Duration(1<<min(j.Attempts-1, 10)), 30*time.Second)
		if err = f.store.FinishFederationDMJob(ctx, *j, delivered, permanent, time.Now().Add(delay), reason); err != nil {
			return err
		}
	}
	return nil
}
func (f *Federation) deliverDM(ctx context.Context, j store.FederationJob) (bool, bool, string) {
	var e store.FederationDMEnvelope
	if json.Unmarshal([]byte(j.Payload), &e) != nil {
		return false, true, "invalid stored envelope"
	}
	if _, ok := f.peer(j.PeerID); !ok || e.OriginNodeID != f.nodeID || !f.store.FederationDMJobAuthorized(ctx, j, e) {
		return false, true, "peer or room link disabled"
	}
	req, cancel, err := f.peerRequest(ctx, j.PeerID, "GET", "/federation/v2/capabilities", nil)
	if err != nil {
		return false, true, "invalid configured endpoint"
	}
	client := f.clients[j.PeerID]
	if client == nil {
		cancel()
		return false, true, "unknown peer client"
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return false, false, "peer transport failure"
	}
	var caps struct {
		Version int  `json:"version"`
		DM      bool `json:"federated_dm"`
	}
	ok := resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&caps) == nil && caps.Version == 2 && caps.DM
	resp.Body.Close()
	cancel()
	if !ok {
		return false, false, "peer capability unavailable"
	}
	req, cancel, err = f.peerRequest(ctx, j.PeerID, "POST", "/federation/v2/dm/messages", bytes.NewBufferString(j.Payload))
	if err != nil {
		return false, true, "invalid configured endpoint"
	}
	defer cancel()
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		return false, false, "peer transport failure"
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 429, "peer rejected DM"
	}
	var receipt store.FederationReceipt
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&receipt) != nil || receipt.OriginNodeID != e.OriginNodeID || receipt.OriginMessageID != e.OriginMessageID || receipt.LocalMessageID == "" {
		return false, false, "invalid peer receipt"
	}
	return true, false, ""
}

func (s *Server) federatedDMControlDisabled(w http.ResponseWriter, r *http.Request) bool {
	room, err := s.store.RoomByID(r.Context(), r.PathValue("room"))
	if err == nil && room.Kind == "federated_dm" {
		writeError(w, 403, "forbidden", "federated DM typing and read receipts require separate opt-in phases", nil)
		return true
	}
	return false
}
