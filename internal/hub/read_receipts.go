package hub

import "net/http"

func (s *Server) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	if !s.opts.ReadReceipts {
		writeError(w, http.StatusNotFound, "not_found", "read receipts are disabled", nil)
		return
	}
	var req struct {
		Seq *int64 `json:"seq"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Seq == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "seq is required", nil)
		return
	}
	receipt, err := s.store.MarkRead(r.Context(), agentFrom(r.Context()).ID, r.PathValue("room"), *req.Seq)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}
