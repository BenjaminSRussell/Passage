package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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

type GLShare struct {
	Allocated decimal.Decimal `json:"allocated"`
	Deferred  decimal.Decimal `json:"deferred"`
}

type GLOutcome int

const (
	GLNoVerdict GLOutcome = iota
	GLRefused
	GLPark
)

type GLLine struct {
	No        int
	Account   string
	Net       decimal.Decimal
	OrderID   string
	ProductID string
	Original  *GLOriginal
}

type GLOriginal struct {
	Net   decimal.Decimal
	Share GLShare
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
			var original map[int]GLShare
			var originalLines []Line
			if d.CreditNote {
				if original, originalLines, err = loadBookedOriginal(ctx, tx, inv.OriginalInvoiceID); err != nil {
					return err
				}
			}
			for _, l := range lines {
				gl := GLLine{No: l.No, Account: l.RevenueAccount, Net: l.Net, OrderID: l.Source.OrderID, ProductID: l.Source.ProductID}
				if share, ok := original[l.OriginalLineNo]; ok {
					if i := slices.IndexFunc(originalLines, func(o Line) bool { return o.No == l.OriginalLineNo }); i >= 0 {
						gl.Original = &GLOriginal{Net: originalLines[i].Net, Share: share}
					}
				}
				d.Lines = append(d.Lines, gl)
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

func loadBookedOriginal(ctx context.Context, tx pgx.Tx, originalID string) (map[int]GLShare, []Line, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `select gl_split from invoice where id = $1::uuid`, originalID).Scan(&raw); err != nil {
		return nil, nil, err
	}
	if raw == nil {
		return nil, nil, nil
	}
	var shares map[int]GLShare
	if err := json.Unmarshal(raw, &shares); err != nil {
		return nil, nil, err
	}
	lines, err := loadLines(ctx, tx, originalID)
	return shares, lines, err
}

// MarkGLBooked stores the ledger entry of a document, and the shares an allocation booked per line number (nil when the document
// was booked at its billed nets), and reports whether this call was the one that did; a second call changes nothing.
func (s *Postgres) MarkGLBooked(ctx context.Context, id, entryID string, shares map[int]GLShare) (bool, error) {
	var split []byte
	if len(shares) > 0 {
		var err error
		if split, err = json.Marshal(shares); err != nil {
			return false, err
		}
	}
	var done bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update invoice set journal_posting_id = $2::uuid, gl_split = $3::jsonb, gl_last_error = null, gl_next_attempt_at = null
			where id = $1::uuid and journal_posting_id is null`, id, entryID, split)
		done = err == nil && tag.RowsAffected() > 0
		return err
	})
	return done, err
}

// RecordGLRefusal schedules the next attempt with exponential backoff (5 minutes, doubling, at most a day) and records code. A
// GLRefused outcome, the ledger's verdict, counts toward MaxGLAttempts and parks the document with code when it is reached; GLPark
// parks at once; GLNoVerdict (an outage, a missing allocation) never parks. ErrGLAlreadyBooked says a concurrent call booked it.
func (s *Postgres) RecordGLRefusal(ctx context.Context, id, code string, now time.Time, outcome GLOutcome) (parked bool, err error) {
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `update invoice set gl_attempt_count = gl_attempt_count + 1, gl_last_error = $2,
			gl_refusal_count = gl_refusal_count + case when $4::int = 1 then 1 else 0 end,
			gl_next_attempt_at = $3::timestamptz + (5 * least(2 ^ gl_attempt_count, 288)) * interval '1 minute',
			gl_parked_code = case when $4::int = 2 or ($4::int = 1 and gl_refusal_count + 1 >= $5) then $2 end
			where id = $1::uuid and journal_posting_id is null returning gl_parked_code is not null`, id, code, now, int(outcome), MaxGLAttempts).Scan(&parked)
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
		tag, err := tx.Exec(ctx, `update invoice set gl_parked_code = null, gl_attempt_count = 0, gl_refusal_count = 0, gl_next_attempt_at = null
			where legal_entity_id = $1::uuid and gl_parked_code is not null and journal_posting_id is null`, legalEntityID)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}
