package store

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/ledger/internal/domain"
)

const (
	PostingRequestPosted   = "posted"
	PostingRequestRejected = "rejected"

	CodePostingRequestNotFound = "LEDGER_POSTING_REQUEST_NOT_FOUND"
)

type PostingRequest struct {
	Key            string    `json:"key"`
	Status         string    `json:"status"`
	LedgerID       string    `json:"ledgerId"`
	EntryID        *string   `json:"entryId"`
	DocumentNumber *string   `json:"documentNumber"`
	ViolationCode  *string   `json:"violationCode"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func recordPostedRequest(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, ledgerID, key, entryID, voucher string) error {
	_, err := tx.Exec(ctx, `insert into posting_request (tenant_id, legal_entity_id, ledger_id, idempotency_key, status, journal_entry_id, document_number)
		values ($1, $2, $3, $4, 'posted', $5, $6)
		on conflict (tenant_id, legal_entity_id, idempotency_key) do update
		set status = 'posted', journal_entry_id = excluded.journal_entry_id, document_number = excluded.document_number, ledger_id = excluded.ledger_id, violation_code = null, updated_at = now()`,
		tenantID, legalEntityID, ledgerID, key, entryID, voucher)
	return err
}

func (s *Postgres) recordRejectedPosting(ctx context.Context, tenantID, ledgerID, key string, post error) {
	var rej *domain.RejectedError
	if !errors.As(post, &rej) || len(rej.Violations) == 0 {
		return
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into posting_request (tenant_id, legal_entity_id, ledger_id, idempotency_key, status, violation_code)
			select tenant_id, legal_entity_id, id, $3, 'rejected', $4 from ledger where tenant_id = $1 and id = $2
			on conflict (tenant_id, legal_entity_id, idempotency_key) do update
			set status = 'rejected', violation_code = excluded.violation_code, updated_at = now()
			where posting_request.status <> 'posted'`, tenantID, ledgerID, key, rej.Violations[0].Code)
		return err
	})
	if err != nil {
		slog.Error("posting request rejection not recorded", "err", err, "ledgerId", ledgerID, "key", key)
	}
}

// GetPostingRequest returns what the registry holds for a posting key within the caller's tenant and legal-entity scope.
func (s *Postgres) GetPostingRequest(ctx context.Context, key string) (PostingRequest, error) {
	var out PostingRequest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select idempotency_key, status, ledger_id::text, journal_entry_id::text, document_number, violation_code, created_at, updated_at
			from posting_request where idempotency_key = $1 order by updated_at desc limit 1`, key).
			Scan(&out.Key, &out.Status, &out.LedgerID, &out.EntryID, &out.DocumentNumber, &out.ViolationCode, &out.CreatedAt, &out.UpdatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.NewProblem(http.StatusNotFound, CodePostingRequestNotFound, "no posting request is recorded for this key")
	}
	return out, err
}
