package store

import (
	"context"
	"database/sql"
)

// File status is additive to the existing v1 operator contract. No manifest,
// filenames, URLs, tokens, grant query, temp path or lease is exposed.
type FederationFileOperatorStatus struct {
	Pending        int   `json:"pending"`
	Inflight       int   `json:"inflight"`
	WaitingReceipt int   `json:"waiting_receipt"`
	Delivered      int   `json:"delivered"`
	Dead           int   `json:"dead"`
	Attempts       int   `json:"attempts"`
	Staged         int   `json:"staged"`
	ReservedBytes  int64 `json:"reserved_bytes"`
}

func readFederationFileStatus(ctx context.Context, tx *sql.Tx, status *FederationFileOperatorStatus) error {
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(state='pending'),0),COALESCE(SUM(state='inflight'),0),COALESCE(SUM(state='waiting_receipt'),0),COALESCE(SUM(state='delivered'),0),COALESCE(SUM(state='dead'),0),COALESCE(SUM(attempts),0) FROM federation_file_outbox`).Scan(&status.Pending, &status.Inflight, &status.WaitingReceipt, &status.Delivered, &status.Dead, &status.Attempts); err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(state IN ('pending','inflight')),0),COALESCE(SUM(reserved),0)+(SELECT COALESCE(SUM(size),0) FROM federation_received_blobs) FROM federation_file_transfers`).Scan(&status.Staged, &status.ReservedBytes)
}
