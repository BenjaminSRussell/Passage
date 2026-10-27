package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
)

// DNIFilter selects the delivered-not-invoiced report (section 5.28.14): one
// legal entity as of a date, optionally one customer ("" = all customers).
type DNIFilter struct {
	LegalEntityID string
	CustomerID    string
	AsOf          time.Time
}

// DeliveredNotInvoiced returns every packing slip of the legal entity with an
// open balance on account 1405 as of f.AsOf (FR-28.12, AC-28.8). The balance is
// read from the journals the slips carry: the original's Dr 1405 plus its
// reversal when that reversal is dated on or before f.AsOf, so a slip cancelled
// after the report date still counts on that date. Slips posted with
// cogs_at_shipment carry no 1405 line and never appear. An INVOICED slip is left
// out whatever the date, because billing (wave 29) relieves 1405 and this
// service does not record when (a slip is invoiced as a whole and an invoiced
// slip cannot be cancelled, so it never has a reversal). Rows are ordered by
// posting date and number.
func (s *Postgres) DeliveredNotInvoiced(ctx context.Context, f DNIFilter) ([]domain.DNIRow, error) {
	out := []domain.DNIRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			with balance as (
				select ps.tenant_id, coalesce(ps.reversal_of_id, ps.id) as slip_id, sum((j.line->>'amount')::numeric) as amount
				from packing_slip ps
				cross join lateral jsonb_array_elements(ps.journal) as j(line)
				where ps.legal_entity_id = $1::uuid and ps.status <> 'INVOICED' and ps.posting_date <= $2::date
				  and j.line->>'account' = $3
				group by ps.tenant_id, coalesce(ps.reversal_of_id, ps.id)
			)
			select o.id::text, o.packing_slip_number, o.shipment_id::text, o.order_id::text, sh.customer_id::text,
				o.currency::text, o.posting_date, b.amount::text
			from balance b
			join packing_slip o on o.tenant_id = b.tenant_id and o.id = b.slip_id
			join shipment sh on sh.tenant_id = o.tenant_id and sh.id = o.shipment_id
			where b.amount <> 0
			  and ($4::text = '' or sh.customer_id = nullif($4::text, '')::uuid)
			order by o.posting_date, o.packing_slip_number`,
			f.LegalEntityID, f.AsOf.Format("2006-01-02"), domain.AccountDeliveredNotInvoiced, f.CustomerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.DNIRow
			var amount string
			if err := rows.Scan(&r.PackingSlipID, &r.PackingSlipNumber, &r.ShipmentID, &r.OrderID, &r.CustomerID,
				&r.Currency, &r.PostingDate, &amount); err != nil {
				return err
			}
			r.Amount = dec(amount)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
