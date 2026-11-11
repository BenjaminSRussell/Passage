package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

// backfillQueued counts, per source, the journals one backfill pass queued for what happened before 30-2 posted them.
type backfillQueued struct {
	DunningFees, Interest, DownPayments, Deductions int
}

type backfillRow struct {
	id, itemID, currency, date, kind, extra string
	amount                                  decimal.Decimal
}

// readBackfillRows runs query and collects its rows (id, currency, date, amount) in memory, so the transaction is free to insert
// the journals while the cursor is closed.
func readBackfillRows(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]backfillRow, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []backfillRow
	for rows.Next() {
		var r backfillRow
		var amount string
		if err := rows.Scan(&r.id, &r.itemID, &r.kind, &r.currency, &r.date, &amount, &r.extra); err != nil {
			return nil, err
		}
		var derr error
		if r.amount, derr = decimal.NewFromString(amount); derr != nil {
			return nil, derr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// backfillCollections queues the journals of the collection-side postings that predate 30-2, each under the key the live path uses
// (so a posting that already has its journal is skipped and a repeat queues nothing twice): issued dunning letters' fees
// (Dr 1200 / Cr 4300), interest notes (Dr 1200 / Cr 4310), received down payments and deposits, their clearings and releases (the
// postings of ReceiptPostings, ClearingPostings and ReleasePostings), and written-off deductions (Dr 6110 / Cr 1200). A clearing
// is queued once per clearing row unless the down payment already has a clearing journal for the item.
func backfillCollections(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) (backfillQueued, error) {
	var out backfillQueued
	missing := func(key string) string {
		return `not exists (select 1 from gl_journal j where j.legal_entity_id = $1 and j.source_key = ` + key + `)`
	}
	fees, err := readBackfillRows(ctx, tx, `select l.id::text, '', '', l.currency::text, coalesce(l.issued_at, now())::date::text, l.fee_amount::text, '0'
		from dunning_letter l where l.legal_entity_id = $1 and l.status = 'ISSUED' and l.fee_amount > 0 and `+missing(`'fee:' || l.id::text`), legalEntityID)
	if err != nil {
		return out, err
	}
	for _, f := range fees {
		if err := enqueueJournal(ctx, tx, tenantID, journalSpec{LegalEntityID: legalEntityID, SourceType: JournalDunningFee, SourceID: f.id,
			SourceKey: "fee:" + f.id, DocumentID: "FEE-" + f.id, PostingDate: f.date, Currency: f.currency,
			Lines: []journalLine{debit(domain.AccountReceivable, f.amount, "Dunning fee"), credit(domain.AccountDunningFee, f.amount, "Dunning fee")}}); err != nil {
			return out, err
		}
		out.DunningFees++
	}
	notes, err := readBackfillRows(ctx, tx, `select n.id::text, '', '', o.currency::text, n.calculation_date::text, n.amount::text, '0'
		from interest_note n join open_item o on o.tenant_id = n.tenant_id and o.id = n.interest_item_id
		where n.legal_entity_id = $1 and `+missing(`'interest:' || n.id::text`), legalEntityID)
	if err != nil {
		return out, err
	}
	for _, n := range notes {
		if err := enqueueJournal(ctx, tx, tenantID, journalSpec{LegalEntityID: legalEntityID, SourceType: JournalInterest, SourceID: n.id,
			SourceKey: "interest:" + n.id, DocumentID: "IN-" + n.id, PostingDate: n.date, Currency: n.currency,
			Lines: []journalLine{debit(domain.AccountReceivable, n.amount, "Interest"), credit(domain.AccountInterestIncome, n.amount, "Interest")}}); err != nil {
			return out, err
		}
		out.Interest++
	}
	dps, err := readBackfillRows(ctx, tx, `select d.id::text, '', d.kind, d.currency::text, d.received_date::text, d.amount::text, ''
		from down_payment d where d.legal_entity_id = $1 and d.status in ('RECEIVED', 'CLEARED', 'RELEASED')
		and `+missing(`'downpayment:' || d.id::text || ':receipt'`), legalEntityID)
	if err != nil {
		return out, err
	}
	for _, d := range dps {
		dp := DownPayment{ID: d.id, LegalEntityID: legalEntityID, Currency: d.currency}
		if err := downPaymentJournal(ctx, tx, tenantID, dp, "receipt", d.date, domain.ReceiptPostings(d.kind, d.amount)); err != nil {
			return out, err
		}
		out.DownPayments++
	}
	clears, err := readBackfillRows(ctx, tx, `select c.id::text, c.open_item_id::text, d.kind, d.currency::text, c.cleared_at::date::text, c.amount::text, d.id::text
		from down_payment_clearing c join down_payment d on d.tenant_id = c.tenant_id and d.id = c.down_payment_id
		where c.legal_entity_id = $1 and not exists (select 1 from gl_journal j where j.tenant_id = c.tenant_id and j.legal_entity_id = c.legal_entity_id
		  and j.source_type = $2 and j.source_id = d.id and j.source_key like 'downpayment:' || d.id::text || ':clear:' || c.open_item_id::text || ':%')`,
		legalEntityID, JournalDownPayment)
	if err != nil {
		return out, err
	}
	for _, c := range clears {
		dp := DownPayment{ID: c.extra, LegalEntityID: legalEntityID, Currency: c.currency}
		if err := downPaymentJournal(ctx, tx, tenantID, dp, "clear:"+c.itemID+":backfill-"+c.id, c.date, domain.ClearingPostings(c.kind, c.amount)); err != nil {
			return out, err
		}
		out.DownPayments++
	}
	rels, err := readBackfillRows(ctx, tx, `select d.id::text, '', d.kind, d.currency::text, current_date::text, (d.amount - d.cleared_amount)::text, '0'
		from down_payment d where d.legal_entity_id = $1 and d.status = 'RELEASED' and d.kind = 'SECURITY_DEPOSIT'
		and `+missing(`'downpayment:' || d.id::text || ':release'`), legalEntityID)
	if err != nil {
		return out, err
	}
	for _, d := range rels {
		dp := DownPayment{ID: d.id, LegalEntityID: legalEntityID, Currency: d.currency}
		if err := downPaymentJournal(ctx, tx, tenantID, dp, "release", d.date, domain.ReleasePostings(d.kind, d.amount)); err != nil {
			return out, err
		}
		out.DownPayments++
	}
	deds, err := readBackfillRows(ctx, tx, `select d.id::text, '', '', d.currency::text, coalesce(d.resolved_at, now())::date::text, d.amount::text, '0'
		from deduction d where d.legal_entity_id = $1 and d.status = 'WRITTEN_OFF' and `+missing(`'deduction:' || d.id::text || ':write-off'`), legalEntityID)
	if err != nil {
		return out, err
	}
	for _, d := range deds {
		if err := enqueueJournal(ctx, tx, tenantID, journalSpec{LegalEntityID: legalEntityID, SourceType: JournalDeduction, SourceID: d.id,
			SourceKey: "deduction:" + d.id + ":write-off", DocumentID: "DED-" + d.id, PostingDate: d.date, Currency: d.currency,
			Lines: postingLines(domain.DeductionWriteOffPostings(d.amount))}); err != nil {
			return out, err
		}
		out.Deductions++
	}
	return out, nil
}
