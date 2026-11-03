package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
)

const (
	MaxGLAttempts      = 5
	DeferredGL         = "2400"
	OriginalParkedCode = "BIL-0206"
)

var ErrGLAlreadyBooked = errors.New("the document already has a ledger entry")

type GLProfile struct {
	LegalEntityID           string   `json:"legalEntityId"`
	LedgerID                string   `json:"ledgerId"`
	ReceivableAccount       string   `json:"receivableAccount"`
	TaxAccount              string   `json:"taxAccount"`
	ChargeAccount           string   `json:"chargeAccount"`
	DeferredRevenueAccounts []string `json:"deferredRevenueAccounts"`
}

type GLLine struct {
	Account string
	Net     decimal.Decimal
}

type GLDocument struct {
	ID          string
	Number      string
	CreditNote  bool
	Currency    string
	PostingDate string
	Gross       decimal.Decimal
	Charges     decimal.Decimal
	Tax         decimal.Decimal
	Lines       []GLLine
}

// DueGLDocuments returns up to limit posted invoices and credit notes of the entity that have no ledger entry, are not parked and
// whose next attempt is due, oldest first, with the lines as stored; more says that due documents were left for the next call.
// A credit note is held until its original has its entry, and is parked with OriginalParkedCode while the original is parked, so a
// mirror is never booked against nothing.
func (s *Postgres) DueGLDocuments(ctx context.Context, legalEntityID string, now time.Time, limit int) ([]GLDocument, bool, error) {
	var out []GLDocument
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update invoice cn set gl_parked_code = $2, gl_last_error = $2
			from invoice o
			where cn.tenant_id = o.tenant_id and cn.original_invoice_id = o.id and cn.legal_entity_id = $1::uuid
			  and cn.status in `+postedStatuses+` and cn.journal_posting_id is null and cn.gl_parked_code is null
			  and o.journal_posting_id is null and o.gl_parked_code is not null`, legalEntityID, OriginalParkedCode); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select i.id::text from invoice i
			left join invoice o on o.tenant_id = i.tenant_id and o.id = i.original_invoice_id
			where i.legal_entity_id = $1::uuid and i.status in `+postedStatuses+` and i.journal_posting_id is null and i.gl_parked_code is null
			  and (i.gl_next_attempt_at is null or i.gl_next_attempt_at <= $2) and i.gross_amount > 0
			  and (i.original_invoice_id is null or o.journal_posting_id is not null)
			order by i.posting_date, i.created_at, i.id limit $3`, legalEntityID, now, limit+1)
		if err != nil {
			return err
		}
		docIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range docIDs {
			inv, err := scanInvoice(tx.QueryRow(ctx, `select `+invoiceColumns+` from invoice where id = $1`, id))
			if err != nil {
				return err
			}
			lines, err := loadLines(ctx, tx, id)
			if err != nil {
				return err
			}
			d := GLDocument{ID: inv.ID, Number: inv.Number, CreditNote: inv.Type == domain.TypeCreditNote, Currency: inv.Currency,
				PostingDate: inv.PostingDate.Format(dateLayout), Gross: inv.Totals.Gross, Charges: inv.Totals.Charges, Tax: inv.Totals.Tax}
			for _, l := range lines {
				d.Lines = append(d.Lines, GLLine{Account: l.RevenueAccount, Net: l.Net})
			}
			out = append(out, d)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// MarkGLBooked stores the ledger entry of a document and reports whether this call was the one that did; a second call changes nothing.
func (s *Postgres) MarkGLBooked(ctx context.Context, id, entryID string) (bool, error) {
	var done bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update invoice set journal_posting_id = $2::uuid, gl_last_error = null, gl_next_attempt_at = null
			where id = $1::uuid and journal_posting_id is null`, id, entryID)
		done = err == nil && tag.RowsAffected() > 0
		return err
	})
	return done, err
}

// RecordGLRefusal counts a refused attempt, schedules the next one with exponential backoff (5 minutes, doubling, at most a day) and
// parks the document with code once MaxGLAttempts is reached or when park is set; ErrGLAlreadyBooked says a concurrent call booked it.
func (s *Postgres) RecordGLRefusal(ctx context.Context, id, code string, now time.Time, park bool) (parked bool, err error) {
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `update invoice set gl_attempt_count = gl_attempt_count + 1, gl_last_error = $2,
			gl_next_attempt_at = $3::timestamptz + (5 * least(2 ^ gl_attempt_count, 288)) * interval '1 minute',
			gl_parked_code = case when $4 or gl_attempt_count + 1 >= $5 then $2 end
			where id = $1::uuid and journal_posting_id is null returning gl_parked_code is not null`, id, code, now, park, MaxGLAttempts).Scan(&parked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGLAlreadyBooked
		}
		return err
	})
	return parked, err
}

// UnparkGL offers the parked documents of the entity again and returns how many there were.
func (s *Postgres) UnparkGL(ctx context.Context, legalEntityID string) (int64, error) {
	var n int64
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update invoice set gl_parked_code = null, gl_attempt_count = 0, gl_next_attempt_at = null
			where legal_entity_id = $1::uuid and gl_parked_code is not null and journal_posting_id is null`, legalEntityID)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}
