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
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	billingevents "github.com/BenjaminSRussell/passage/services/billing/internal/events"
)

const (
	MaxGLAttempts = 5
	// DeferredGL is 2400 contract liability: the deferred part of an allocation and what a contract billed above its
	// allocation (ASC 606-10-45-2, IFRS 15.106).
	DeferredGL = "2400"
	// DefaultContractAssetGL is 1250 contract asset: what a contract recognized above its billings (ASC 606-10-45-3, IFRS 15.107).
	DefaultContractAssetGL = "1250"
	OriginalParkedCode     = "BIL-0206"
)

var ErrGLAlreadyBooked = errors.New("the document already has a ledger entry")

type GLProfile struct {
	TenantID                string   `json:"tenantId,omitempty"`
	LegalEntityID           string   `json:"legalEntityId"`
	LedgerID                string   `json:"ledgerId"`
	ReceivableAccount       string   `json:"receivableAccount"`
	TaxAccount              string   `json:"taxAccount"`
	ChargeAccount           string   `json:"chargeAccount"`
	DeferredRevenueAccounts []string `json:"deferredRevenueAccounts"`
	ContractAssetAccount    string   `json:"contractAssetAccount,omitempty"`
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
	// Held is the booking an earlier attempt sent to the ledger without a verdict; a retry posts it again unchanged, so the
	// ledger key never meets a different entry and later documents of the contract count it as consumed.
	Held *GLHeld
}

// GLHeld is a booking sent to the ledger: the shares per line and the contract position change (Liability: a credit to 2400
// when positive; Asset: a debit to the contract asset account when positive).
type GLHeld struct {
	Shares   map[int]GLShare
	Contract GLContractMove
}

type GLContractMove struct {
	Liability decimal.Decimal `json:"liability"`
	Asset     decimal.Decimal `json:"asset"`
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
			var split, contract []byte
			if err := tx.QueryRow(ctx, `select gl_split, gl_contract from invoice where id = $1::uuid`, id).Scan(&split, &contract); err != nil {
				return err
			}
			if split != nil {
				d.Held = &GLHeld{}
				if err := json.Unmarshal(split, &d.Held.Shares); err != nil {
					return err
				}
				if contract != nil {
					if err := json.Unmarshal(contract, &d.Held.Contract); err != nil {
						return err
					}
				}
			}
			var original map[int]GLShare
			var originalLines []Line
			if d.CreditNote {
				if original, originalLines, err = loadBookedOriginal(ctx, tx, inv.OriginalInvoiceID); err != nil {
					return err
				}
			}
			for _, l := range lines {
				gl := GLLine{No: l.No, Account: l.RevenueAccount, Net: l.Net, OrderID: l.Source.OrderID, ProductID: l.Source.ProductID}
				if i := slices.IndexFunc(originalLines, func(o Line) bool { return o.No == l.OriginalLineNo }); d.CreditNote && i >= 0 {
					// A credit note line has no source of its own: it moves the contract of its original line.
					gl.OrderID, gl.ProductID = originalLines[i].Source.OrderID, originalLines[i].Source.ProductID
					if share, ok := original[l.OriginalLineNo]; ok {
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
	var shares map[int]GLShare
	if raw != nil {
		if err := json.Unmarshal(raw, &shares); err != nil {
			return nil, nil, err
		}
	}
	lines, err := loadLines(ctx, tx, originalID)
	return shares, lines, err
}

// MarkGLBooked stores the ledger entry of a document, and the shares an allocation booked per line number (nil when the document
// was booked at its billed nets), and reports whether this call was the one that did; a second call changes nothing. Booking a
// credit note enqueues creditnote.booked in the same transaction, with the allocated and deferred amounts the journal reversed per
// line, so revrec reverses its schedules by what 2400 was actually debited.
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
		if !done {
			return err
		}
		return enqueueCreditNoteBooked(ctx, tx, id, entryID, shares)
	})
	return done, err
}

// enqueueCreditNoteBooked writes creditnote.booked for a credit note just booked; it does nothing for an invoice. A line without a
// share was booked at its net with nothing deferred.
func enqueueCreditNoteBooked(ctx context.Context, tx pgx.Tx, id, entryID string, shares map[int]GLShare) error {
	cn, err := scanInvoice(tx.QueryRow(ctx, `select `+invoiceColumns+` from invoice where id = $1::uuid`, id))
	if err != nil || cn.Type != domain.TypeCreditNote {
		return err
	}
	lines, err := loadLines(ctx, tx, id)
	if err != nil {
		return err
	}
	originalLines, err := loadLines(ctx, tx, cn.OriginalInvoiceID)
	if err != nil {
		return err
	}
	exp := money.MinorUnits(cn.Currency)
	data := billingevents.CreditNoteBookedData{
		CreditNoteID: cn.ID, Number: cn.Number, OriginalInvoiceID: cn.OriginalInvoiceID, OriginalNumber: cn.OriginalNumber,
		LegalEntityID: cn.LegalEntityID, Currency: cn.Currency, PostingDate: cn.PostingDate.Format(dateLayout), JournalEntryID: entryID,
		Lines: make([]billingevents.CreditNoteBookedLine, 0, len(lines)),
	}
	for _, l := range lines {
		share, ok := shares[l.No]
		if !ok {
			share = GLShare{Allocated: l.Net}
		}
		out := billingevents.CreditNoteBookedLine{LineNo: l.No, OriginalLineNo: l.OriginalLineNo, Net: l.Net.StringFixed(exp),
			Allocated: share.Allocated.StringFixed(exp), Deferred: share.Deferred.StringFixed(exp)}
		if i := slices.IndexFunc(originalLines, func(o Line) bool { return o.No == l.OriginalLineNo }); i >= 0 {
			out.OrderID, out.Product = originalLines[i].Source.OrderID, originalLines[i].Source.ProductID
		}
		data.Lines = append(data.Lines, out)
	}
	p, _ := tenant.FromContext(ctx)
	evt, err := events.New(billingevents.CreditNoteBooked, eventSource, cn.ID, p.TenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "invoice", cn.ID, evt)
}

// HoldGLBooking stores the booking of a document about to be sent to the ledger (see GLDocument.Held).
func (s *Postgres) HoldGLBooking(ctx context.Context, id string, held GLHeld) error {
	split, err := json.Marshal(held.Shares)
	if err != nil {
		return err
	}
	contract, err := json.Marshal(held.Contract)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update invoice set gl_split = $2::jsonb, gl_contract = $3::jsonb where id = $1::uuid and journal_posting_id is null`,
			id, split, contract)
		return err
	})
}

// RecordGLRefusal schedules the next attempt with exponential backoff (5 minutes, doubling, at most a day) and records code. A
// GLRefused outcome, the ledger's verdict, counts toward MaxGLAttempts and parks the document with code when it is reached; GLPark
// parks at once; GLNoVerdict (an outage, a missing allocation) never parks. A verdict releases the held booking (the ledger
// booked nothing), an outage keeps it for the retry. ErrGLAlreadyBooked says a concurrent call booked it.
func (s *Postgres) RecordGLRefusal(ctx context.Context, id, code string, now time.Time, outcome GLOutcome) (parked bool, err error) {
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `update invoice set gl_attempt_count = gl_attempt_count + 1, gl_last_error = $2,
			gl_split = case when $4::int = 0 then gl_split end, gl_contract = case when $4::int = 0 then gl_contract end,
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

// ContractBalance is what the booked documents of an order hold against its revenue contract: per product the allocated
// amount and its deferred part they booked (Consumed, credit notes subtracted), and the contract position, billed nets less
// allocated amounts (positive: a contract liability in DeferredGL; negative: a contract asset).
type ContractBalance struct {
	Consumed map[string]GLShare
	Position decimal.Decimal
}

// GLContractBalances sums per order the shares that the booked or held invoices and credit notes of the entity stored in gl_split; a
// booked line without a share consumed its billed net. A credit note line counts against the order and product of its original.
func (s *Postgres) GLContractBalances(ctx context.Context, legalEntityID string, orderIDs []string) (map[string]ContractBalance, error) {
	out := map[string]ContractBalance{}
	if len(orderIDs) == 0 {
		return out, nil
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select src.order_id::text, src.product_id::text,
				sum(src.sign * coalesce((i.gl_split -> l.line_no::text ->> 'allocated')::numeric, l.line_net))::text,
				sum(src.sign * coalesce((i.gl_split -> l.line_no::text ->> 'deferred')::numeric, 0))::text,
				sum(src.sign * (l.line_net - coalesce((i.gl_split -> l.line_no::text ->> 'allocated')::numeric, l.line_net)))::text
			from invoice i
			join invoice_line l on l.tenant_id = i.tenant_id and l.invoice_id = i.id
			left join invoice_line ol on i.invoice_type = 'CREDIT_NOTE' and ol.tenant_id = i.tenant_id
			  and ol.invoice_id = i.original_invoice_id and ol.line_no = l.original_line_no
			cross join lateral (select coalesce(ol.order_id, l.order_id) as order_id, coalesce(ol.product_id, l.product_id) as product_id,
			  case when i.invoice_type = 'CREDIT_NOTE' then -1 else 1 end as sign) src
			where i.legal_entity_id = $1::uuid and (i.journal_posting_id is not null or i.gl_split is not null) and src.order_id = any($2::uuid[])
			group by 1, 2`, legalEntityID, orderIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var order, product, allocated, deferred, position string
			if err := rows.Scan(&order, &product, &allocated, &deferred, &position); err != nil {
				return err
			}
			share, err := parseShare(allocated, deferred)
			if err != nil {
				return err
			}
			pos, err := decimal.NewFromString(position)
			if err != nil {
				return err
			}
			b := out[order]
			if b.Consumed == nil {
				b.Consumed = map[string]GLShare{}
			}
			b.Consumed[product], b.Position = share, b.Position.Add(pos)
			out[order] = b
		}
		return rows.Err()
	})
	return out, err
}

func parseShare(allocated, deferred string) (GLShare, error) {
	a, err := decimal.NewFromString(allocated)
	if err != nil {
		return GLShare{}, err
	}
	d, err := decimal.NewFromString(deferred)
	return GLShare{Allocated: a, Deferred: d}, err
}

// LockGLRun takes the posting-run lock of the entity, a session advisory lock on a connection held until unlock is called, so
// two runs never book against the same contract balances; ok is false when another run holds it.
func (s *Postgres) LockGLRun(ctx context.Context, legalEntityID string) (unlock func(), ok bool, err error) {
	p, found := tenant.FromContext(ctx)
	if !found {
		return nil, false, errors.New("store: no principal in context")
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	key := "billing-gl-run:" + p.TenantID + ":" + legalEntityID
	if err := conn.QueryRow(ctx, `select pg_try_advisory_lock(hashtextextended(current_schema() || $1, 0))`, key).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nil, false, err
	}
	return func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), `select pg_advisory_unlock(hashtextextended(current_schema() || $1, 0))`, key); err != nil {
			// A connection that may still hold the lock never goes back to the pool.
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
		}
		conn.Release()
	}, true, nil
}
