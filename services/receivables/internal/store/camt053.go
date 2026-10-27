package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

// BankStatementImport is one parsed camt.053 file ready to persist.
type BankStatementImport struct {
	LegalEntityID  string
	FileSHA256     string
	StatementCount int
	SkippedCount   int
	Credits        []domain.CAMTCredit
}

// BankStatementBatch is an imported camt.053 file. PaymentCount is the CAMT053
// payments it created; DuplicateCount the credits already imported from an
// earlier file (same statement id and entry reference, BR-30.7); SkippedCount
// the entries that are not booked customer credits. Payments is filled on
// import and omitted from the list.
type BankStatementBatch struct {
	ID             string     `json:"id"`
	LegalEntityID  string     `json:"legalEntityId"`
	FileSHA256     string     `json:"fileSha256"`
	StatementCount int        `json:"statementCount"`
	PaymentCount   int        `json:"paymentCount"`
	DuplicateCount int        `json:"duplicateCount"`
	SkippedCount   int        `json:"skippedCount"`
	ImportedAt     *time.Time `json:"importedAt,omitempty"`
	Payments       []Payment  `json:"payments,omitempty"`
}

// ImportBankStatement stores a camt.053 file and one unidentified CAMT053 payment
// per booked customer credit, each with payment.received.v1 in the same
// transaction. A file whose SHA-256 is already stored is refused with AR-0003
// and nothing is written. A credit whose (statement id, entry reference) was
// imported before, from an overlapping statement, is counted and not created again.
func (s *Postgres) ImportBankStatement(ctx context.Context, in BankStatementImport) (BankStatementBatch, error) {
	p, _ := tenant.FromContext(ctx)
	out := BankStatementBatch{ID: ids.New(), LegalEntityID: in.LegalEntityID, FileSHA256: in.FileSHA256,
		StatementCount: in.StatementCount, SkippedCount: in.SkippedCount, Payments: []Payment{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var seen string
		err := tx.QueryRow(ctx, `select id from bank_statement_import where file_sha256 = $1`, in.FileSHA256).Scan(&seen)
		if err == nil {
			return httpx.NewProblem(http.StatusConflict, CodeLockboxDuplicate, "File already imported ("+in.FileSHA256+")")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		for _, c := range in.Credits {
			saved, created, err := insertCAMTPayment(ctx, tx, p.TenantID, in.LegalEntityID, c)
			if err != nil {
				return err
			}
			if !created {
				out.DuplicateCount++
				continue
			}
			out.Payments = append(out.Payments, saved)
		}
		out.PaymentCount = len(out.Payments)
		// A concurrent import of the same file loses here and rolls back its payments.
		err = tx.QueryRow(ctx, `insert into bank_statement_import (tenant_id, id, legal_entity_id, file_sha256, statement_count,
			payment_count, duplicate_count, skipped_count) values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (tenant_id, file_sha256) do nothing returning imported_at`,
			p.TenantID, out.ID, in.LegalEntityID, in.FileSHA256, in.StatementCount, out.PaymentCount, out.DuplicateCount,
			out.SkippedCount).Scan(&out.ImportedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusConflict, CodeLockboxDuplicate, "File already imported ("+in.FileSHA256+")")
		}
		return err
	})
	if err != nil {
		return BankStatementBatch{}, mapWriteErr(err)
	}
	return out, nil
}

// insertCAMTPayment creates the payment for one credit; created is false when
// the entry already exists (unique source, source_ref).
func insertCAMTPayment(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, c domain.CAMTCredit) (Payment, bool, error) {
	ref := c.StatementID + ":" + c.Reference
	remittance := []domain.RemittanceLine{}
	if c.RemittanceRef != "" {
		remittance = []domain.RemittanceLine{{Reference: c.RemittanceRef, Amount: c.Amount}}
	}
	raw, err := json.Marshal(remittance)
	if err != nil {
		return Payment{}, false, err
	}
	var payer *string
	if c.DebtorName != "" {
		payer = &c.DebtorName
	}
	saved, err := scanPayment(tx.QueryRow(ctx, `insert into payment (tenant_id, id, legal_entity_id, customer_id, payment_mode, source,
		source_ref, amount, unapplied_amount, currency, received_date, value_date, payer_name, remittance, status, idempotency_key)
		values ($1, $2, $3, null, 'WIRE', 'CAMT053', $4, $5, $5, $6, $7, $7, $8, $9, 'RECEIVED', $10)
		on conflict (tenant_id, source, source_ref) do nothing
		returning `+paymentColumns,
		tenantID, ids.New(), legalEntityID, ref, c.Amount.String(), c.Currency, c.ValueDate, payer, raw, "camt053:"+ref))
	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, false, nil
	}
	if err != nil {
		return Payment{}, false, err
	}
	err = enqueue(ctx, tx, "payment", saved.ID, rcvevents.PaymentReceived, tenantID, rcvevents.PaymentReceivedData{
		PaymentID: saved.ID, LegalEntityID: saved.LegalEntityID, CustomerID: saved.CustomerID, Source: saved.Source,
		Amount: saved.Amount, Currency: saved.Currency, Remittance: toEventRemittance(saved.Remittance),
	})
	return saved, true, err
}

// ListBankStatementImports reads imported camt.053 files, newest first.
func (s *Postgres) ListBankStatementImports(ctx context.Context, limit int) ([]BankStatementBatch, error) {
	out := []BankStatementBatch{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, legal_entity_id, file_sha256, statement_count, payment_count, duplicate_count,
			skipped_count, imported_at from bank_statement_import order by imported_at desc, id limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b BankStatementBatch
			if err := rows.Scan(&b.ID, &b.LegalEntityID, &b.FileSHA256, &b.StatementCount, &b.PaymentCount, &b.DuplicateCount,
				&b.SkippedCount, &b.ImportedAt); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}
