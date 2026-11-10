package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// CodeLockboxDuplicate is returned when the same lockbox file content is imported twice (AR-0003).
const CodeLockboxDuplicate = "AR-0003"

// LockboxImport is one parsed BAI2 file ready to persist.
type LockboxImport struct {
	LegalEntityID string
	FileSHA256    string
	ReceivedDate  string
	AccountCount  int
	Checks        []domain.LockboxCheck
}

// LockboxBatch is an imported lockbox file and the unidentified CHECK payments it
// created. Payments is filled on import and omitted from the batch list.
type LockboxBatch struct {
	ID            string     `json:"id"`
	LegalEntityID string     `json:"legalEntityId"`
	FileSHA256    string     `json:"fileSha256"`
	ReceivedDate  string     `json:"receivedDate"`
	AccountCount  int        `json:"accountCount"`
	PaymentCount  int        `json:"paymentCount"`
	ImportedAt    *time.Time `json:"importedAt,omitempty"`
	Payments      []Payment  `json:"payments,omitempty"`
}

// ImportLockbox stores a lockbox file and one LOCKBOX payment per check, each with
// payment.received.v1 in the same transaction. The payments stay unidentified
// (no customer) until the matcher applies them. A file whose SHA-256 is already
// stored is refused with AR-0003 and nothing is written.
func (s *Postgres) ImportLockbox(ctx context.Context, in LockboxImport) (LockboxBatch, error) {
	p, _ := tenant.FromContext(ctx)
	out := LockboxBatch{LegalEntityID: in.LegalEntityID, FileSHA256: in.FileSHA256, ReceivedDate: in.ReceivedDate,
		AccountCount: in.AccountCount, PaymentCount: len(in.Checks), Payments: []Payment{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out.ID = ids.New()
		var inserted string
		err := tx.QueryRow(ctx, `insert into lockbox_batch (tenant_id, id, legal_entity_id, file_sha256, received_date, account_count, payment_count)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (tenant_id, file_sha256) do nothing returning id`,
			p.TenantID, out.ID, in.LegalEntityID, in.FileSHA256, in.ReceivedDate, in.AccountCount, len(in.Checks)).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusConflict, CodeLockboxDuplicate, "File already imported ("+in.FileSHA256+")")
		}
		if err != nil {
			return err
		}
		for _, c := range in.Checks {
			saved, err := insertLockboxPayment(ctx, tx, p.TenantID, in, c)
			if err != nil {
				return err
			}
			out.Payments = append(out.Payments, saved)
		}
		return nil
	})
	if err != nil {
		return LockboxBatch{}, mapWriteErr(err)
	}
	return out, nil
}

func insertLockboxPayment(ctx context.Context, tx pgx.Tx, tenantID string, in LockboxImport, c domain.LockboxCheck) (Payment, error) {
	ref := lockboxSourceRef(in.FileSHA256, c)
	pay := Payment{ID: ids.New(), LegalEntityID: in.LegalEntityID, PaymentMode: "CHECK", Source: "LOCKBOX",
		SourceRef: &ref, Amount: c.Amount, UnappliedAmount: c.Amount, Currency: c.Currency,
		ReceivedDate: in.ReceivedDate, ValueDate: in.ReceivedDate, Remittance: []domain.RemittanceLine{}}
	if c.CustomerRef != "" {
		pay.Remittance = []domain.RemittanceLine{{Reference: c.CustomerRef, Amount: c.Amount}}
	}
	remittance, err := json.Marshal(pay.Remittance)
	if err != nil {
		return Payment{}, err
	}
	row := tx.QueryRow(ctx, `insert into payment (tenant_id, id, legal_entity_id, customer_id, payment_mode, source, source_ref,
		amount, unapplied_amount, currency, received_date, value_date, payer_name, remittance, status, idempotency_key)
		values ($1, $2, $3, null, $4, $5, $6, $7, $7, $8, $9, $9, null, $10, 'RECEIVED', $11)
		returning `+paymentColumns,
		tenantID, pay.ID, pay.LegalEntityID, pay.PaymentMode, pay.Source, ref, pay.Amount.String(), pay.Currency,
		pay.ReceivedDate, remittance, "lockbox:"+ref)
	saved, err := scanPayment(row)
	if err != nil {
		return Payment{}, err
	}
	if err := receiptJournal(ctx, tx, tenantID, saved); err != nil {
		return Payment{}, err
	}
	err = enqueue(ctx, tx, "payment", saved.ID, rcvevents.PaymentReceived, tenantID, rcvevents.PaymentReceivedData{
		PaymentID: saved.ID, LegalEntityID: saved.LegalEntityID, CustomerID: saved.CustomerID, Source: saved.Source,
		Amount: saved.Amount, Currency: saved.Currency, Remittance: toEventRemittance(saved.Remittance),
	})
	return saved, err
}

// lockboxSourceRef keys a check by the bank's reference when it has one, so a
// re-sent check in a corrected file is refused by (source, source_ref); otherwise
// by file hash and line.
func lockboxSourceRef(fileSHA256 string, c domain.LockboxCheck) string {
	if c.BankRef != "" {
		return c.AccountNo + ":" + c.BankRef
	}
	return fmt.Sprintf("%s:L%d", fileSHA256[:16], c.Line)
}

// ListLockboxBatches reads imported lockbox files, newest first.
func (s *Postgres) ListLockboxBatches(ctx context.Context, limit int) ([]LockboxBatch, error) {
	out := []LockboxBatch{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, legal_entity_id, file_sha256, received_date::text, account_count, payment_count, imported_at
			from lockbox_batch order by imported_at desc, id limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b LockboxBatch
			if err := rows.Scan(&b.ID, &b.LegalEntityID, &b.FileSHA256, &b.ReceivedDate, &b.AccountCount, &b.PaymentCount, &b.ImportedAt); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}
