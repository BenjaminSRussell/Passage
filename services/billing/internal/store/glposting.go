package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

const MaxDeferralsPerRun = 200

type DeferralProfile struct {
	LegalEntityID           string   `json:"legalEntityId"`
	LedgerID                string   `json:"ledgerId"`
	ReceivableAccount       string   `json:"receivableAccount"`
	DeferredAccount         string   `json:"deferredAccount"`
	DeferredRevenueAccounts []string `json:"deferredRevenueAccounts"`
}

type Deferral struct {
	InvoiceID   string
	Number      string
	Currency    string
	PostingDate string
	Amount      decimal.Decimal
}

// UnbookedDeferrals lists the posted invoices of the entity whose lines on a deferred revenue account have no ledger entry yet,
// with the net of those lines as stored. A credit note is not an invoice and is never listed.
func (s *Postgres) UnbookedDeferrals(ctx context.Context, p DeferralProfile) ([]Deferral, error) {
	var out []Deferral
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select i.id::text, i.invoice_number, i.currency::text, i.posting_date::text, sum(l.line_net)::text
			from invoice i join invoice_line l on l.tenant_id = i.tenant_id and l.invoice_id = i.id
			where i.legal_entity_id = $1::uuid and i.invoice_type <> 'CREDIT_NOTE' and i.status in `+postedStatuses+`
			  and i.journal_posting_id is null and l.revenue_account = any($2::text[])
			group by i.id, i.invoice_number, i.currency, i.posting_date, i.created_at
			having sum(l.line_net) > 0
			order by i.posting_date, i.created_at, i.id limit $3`, p.LegalEntityID, p.DeferredRevenueAccounts, MaxDeferralsPerRun)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Deferral
			var amount string
			if err := rows.Scan(&d.InvoiceID, &d.Number, &d.Currency, &d.PostingDate, &amount); err != nil {
				return err
			}
			if d.Amount, err = decimal.NewFromString(amount); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// MarkDeferralBooked records the ledger entry of an invoice's deferral; a second call for the same invoice changes nothing.
func (s *Postgres) MarkDeferralBooked(ctx context.Context, invoiceID, entryID string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update invoice set journal_posting_id = $2::uuid where id = $1::uuid and journal_posting_id is null`, invoiceID, entryID)
		return err
	})
}
