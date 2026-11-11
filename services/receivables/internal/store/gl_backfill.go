package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

// JournalOpening is the source_type of the journal that books the open items loaded by an opening-item import.
const JournalOpening = "opening_items"

// MaxBackfillBatch bounds the payments one backfill pass queues, so a large history is worked off over several calls.
const MaxBackfillBatch = 1000

// BackfillRequest asks for the journals receivables owes the ledger for what happened before 30-2 (payments) and for the items an
// opening-item import loaded. OpeningAccount and OpeningDate, both needed together, switch on the opening-item journals: Dr 1200
// (or Cr, for a net credit) against OpeningAccount on OpeningDate, one per import source and currency. Leave them out when the
// ledger's opening balance already carries the receivable (the ledger pack's own migration does), or the 1200 balance is doubled.
type BackfillRequest struct {
	LegalEntityID  string
	OpeningAccount string
	OpeningDate    string
}

// BackfillResult counts the journals one pass queued. More is true when the payment batch limit was hit: call again.
type BackfillResult struct {
	Receipts      int  `json:"receipts"`
	Applications  int  `json:"applications"`
	OpeningItems  int  `json:"openingItems"`
	OpeningQueued int  `json:"openingAlreadyQueued"`
	More          bool `json:"more"`
}

// BackfillGLJournals queues the missing journals of a legal entity in one transaction (30-2 backfill): for each payment recorded
// before the journals existed, its cash receipt (Dr bank / Cr 1205), and for each such payment applied before then, one journal for
// its applications (Dr 1205, Dr 4100, Dr 6100 / Cr 1200); and, when asked, the opening-item journals. A payment that already has any
// application journal is left to them, an ADVISED or CONFIRMED payment books no cash, and every journal has its own key, so a
// repeat queues nothing twice. The sweeper posts them like any other owed journal.
func (s *Postgres) BackfillGLJournals(ctx context.Context, req BackfillRequest) (BackfillResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out BackfillResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+paymentColumns+` from payment p
			where p.legal_entity_id = $1 and p.status not in ('REVERSED', 'ADVISED', 'CONFIRMED')
			  and not exists (select 1 from gl_journal j where j.tenant_id = p.tenant_id and j.legal_entity_id = p.legal_entity_id and j.source_key = 'receipt:' || p.id::text)
			order by p.created_at, p.id limit $2`, req.LegalEntityID, MaxBackfillBatch+1)
		if err != nil {
			return err
		}
		var pays []Payment
		for rows.Next() {
			pay, err := scanPayment(rows)
			if err != nil {
				rows.Close()
				return err
			}
			pays = append(pays, pay)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(pays) > MaxBackfillBatch {
			pays, out.More = pays[:MaxBackfillBatch], true
		}
		for _, pay := range pays {
			if err := receiptJournal(ctx, tx, p.TenantID, pay); err != nil {
				return err
			}
			out.Receipts++
		}
		n, err := backfillApplications(ctx, tx, p.TenantID, req.LegalEntityID)
		if err != nil {
			return err
		}
		out.Applications = n
		if req.OpeningAccount != "" {
			queued, existing, err := backfillOpening(ctx, tx, p.TenantID, req)
			if err != nil {
				return err
			}
			out.OpeningItems, out.OpeningQueued = queued, existing
		}
		return nil
	})
	return out, err
}

func backfillApplications(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) (int, error) {
	rows, err := tx.Query(ctx, `select p.id::text, p.currency::text, sum(s.settled_amount)::text, sum(s.cash_discount_taken)::text,
			sum(s.write_off_amount)::text, max(s.settled_at)::date::text
		from payment p
		join settlement s on s.tenant_id = p.tenant_id and s.payment_id = p.id and s.reversal_of_id is null
		where p.legal_entity_id = $1
		  and not exists (select 1 from gl_journal j where j.tenant_id = p.tenant_id and j.legal_entity_id = p.legal_entity_id
		                  and j.source_type = $2 and j.source_id = p.id)
		group by p.id, p.currency`, legalEntityID, JournalPaymentApply)
	if err != nil {
		return 0, err
	}
	type app struct {
		id, currency, date  string
		cash, discount, off decimal.Decimal
	}
	var apps []app
	for rows.Next() {
		var a app
		var cash, discount, off string
		if err := rows.Scan(&a.id, &a.currency, &cash, &discount, &off, &a.date); err != nil {
			rows.Close()
			return 0, err
		}
		for dst, src := range map[*decimal.Decimal]string{&a.cash: cash, &a.discount: discount, &a.off: off} {
			d, err := decimal.NewFromString(src)
			if err != nil {
				rows.Close()
				return 0, err
			}
			*dst = d
		}
		apps = append(apps, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, a := range apps {
		err := enqueueJournal(ctx, tx, tenantID, journalSpec{LegalEntityID: legalEntityID, SourceType: JournalPaymentApply, SourceID: a.id,
			SourceKey: "apply-backfill:" + a.id, DocumentID: "PAY-" + a.id, PostingDate: a.date, Currency: a.currency,
			Lines: []journalLine{
				debit(domain.AccountUnappliedCash, a.cash, "Payment applied"), debit(domain.AccountCashDiscount, a.discount, "Cash discount taken"),
				debit(domain.AccountWriteOff, a.off, "Write-off"), credit(domain.AccountReceivable, a.cash.Add(a.discount).Add(a.off), "Open items settled")}})
		if err != nil {
			return 0, err
		}
	}
	return len(apps), nil
}

// backfillOpening queues one journal per import source and currency for the open amount the imports set, so GL 1200 carries
// the receivable the legacy system handed over.
func backfillOpening(ctx context.Context, tx pgx.Tx, tenantID string, req BackfillRequest) (queued, existing int, err error) {
	rows, err := tx.Query(ctx, `select import_source, currency::text, sum(imported_open_amount)::text from open_item
		where legal_entity_id = $1 and import_source is not null group by import_source, currency
		having sum(imported_open_amount) <> 0 order by import_source, currency`, req.LegalEntityID)
	if err != nil {
		return 0, 0, err
	}
	type group struct{ source, currency, net string }
	var groups []group
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.source, &g.currency, &g.net); err != nil {
			rows.Close()
			return 0, 0, err
		}
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, g := range groups {
		net, err := decimal.NewFromString(g.net)
		if err != nil {
			return 0, 0, err
		}
		key := fmt.Sprintf("opening:%s:%s", g.source, g.currency)
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from gl_journal where tenant_id = $1 and legal_entity_id = $2 and source_key = $3`, tenantID, req.LegalEntityID, key).Scan(&n); err != nil {
			return 0, 0, err
		}
		if n > 0 {
			existing++
			continue
		}
		lines := []journalLine{debit(domain.AccountReceivable, net, "Opening receivables"), credit(req.OpeningAccount, net, "Opening balance")}
		if net.IsNegative() {
			lines = []journalLine{debit(req.OpeningAccount, net.Neg(), "Opening balance"), credit(domain.AccountReceivable, net.Neg(), "Opening receivables")}
		}
		if err := enqueueJournal(ctx, tx, tenantID, journalSpec{LegalEntityID: req.LegalEntityID, SourceType: JournalOpening, SourceID: ids.New(), SourceKey: key,
			DocumentID: "OPEN-" + g.source, PostingDate: req.OpeningDate, Currency: g.currency, Lines: lines}); err != nil {
			return 0, 0, err
		}
		queued++
	}
	return queued, existing, nil
}
