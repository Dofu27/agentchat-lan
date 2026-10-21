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
	me := agentFrom(r.Context())
	receipt, err := s.store.MarkRead(r.Context(), me.ID, r.PathValue("room"), *req.Seq)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if f := s.opts.Federation; f != nil && f.opts.ReadReceipts {
		f.ForwardRead(r.PathValue("room"), me.ID, *req.Seq)
	}
	writeJSON(w, http.StatusOK, receipt)
}
