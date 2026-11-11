package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// PortalProduct is a product a customer has bought before, with the terms of the last time: the customer portal reorders from
// this and nothing else, so a portal order never carries a price, site or warehouse the customer typed.
type PortalProduct struct {
	ProductID        string
	Unit             string
	UnitPrice        decimal.Decimal
	DiscPct          decimal.Decimal
	SiteID           string
	WarehouseID      string
	Currency         string
	InvoiceAccountID string
	LastOrderedAt    time.Time
}

// PortalProductFilter narrows the history. ProductIDs empty means every product of the customer, newest first.
type PortalProductFilter struct {
	LegalEntityID string
	CustomerID    string
	ProductIDs    []string
	Limit         int
}

// PortalProducts reads, per product and unit, the most recent line of the customer's own committed orders (no draft, no cancelled
// order or line, no journal or returned order). It is the only price source of a portal order: the sales service holds no price
// list of its own.
func (s *Postgres) PortalProducts(ctx context.Context, f PortalProductFilter) ([]PortalProduct, error) {
	p, _ := tenant.FromContext(ctx)
	out := []PortalProduct{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select product_id::text, unit, unit_price::text, disc_pct::text, site_id::text, warehouse_id::text, currency, invoice_account_id::text, created_at
			from (select distinct on (l.product_id, l.unit) l.product_id, l.unit, l.unit_price, l.disc_pct, l.site_id, l.warehouse_id, o.currency, o.invoice_account_id, o.created_at
				from sales_order_line l
				join sales_order o on o.tenant_id = l.tenant_id and o.id = l.order_id
				where o.tenant_id = $1 and o.legal_entity_id = $2::uuid and o.customer_id = $3::uuid
					and o.status not in ('DRAFT', 'CANCELLED') and o.order_type in ('ORDER', 'SUBSCRIPTION') and l.line_status <> 'CANCELLED'
					and (cardinality($4::uuid[]) = 0 or l.product_id = any($4::uuid[]))
				order by l.product_id, l.unit, o.created_at desc, o.id desc) latest
			order by created_at desc, product_id limit $5`, p.TenantID, f.LegalEntityID, f.CustomerID, nonNil(f.ProductIDs), f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pp PortalProduct
			var price, disc string
			if err := rows.Scan(&pp.ProductID, &pp.Unit, &price, &disc, &pp.SiteID, &pp.WarehouseID, &pp.Currency, &pp.InvoiceAccountID, &pp.LastOrderedAt); err != nil {
				return err
			}
			pp.UnitPrice, pp.DiscPct = decOrZero(&price), decOrZero(&disc)
			out = append(out, pp)
		}
		return rows.Err()
	})
	return out, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
