package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// LineProduction links a configured order line to the production order built for it.
type LineProduction struct {
	OrderID           string
	LineID            string
	ConfigurationID   string
	ProductionOrderID string
	Actor             string
}

// RecordLineProduction stores the link once; a repeat for the line keeps the first.
func (s *Postgres) RecordLineProduction(ctx context.Context, in LineProduction) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into order_line_production (tenant_id, line_id, legal_entity_id, order_id, configuration_id, production_order_id, requested_by)
			select l.tenant_id, l.id, o.legal_entity_id, o.id, $4::uuid, $5::uuid, $6
			from sales_order_line l join sales_order o on o.tenant_id = l.tenant_id and o.id = l.order_id
			where l.tenant_id = $1 and l.id = $2 and o.id = $3 and l.configuration_id = $4::uuid
			on conflict do nothing`, p.TenantID, in.LineID, in.OrderID, in.ConfigurationID, in.ProductionOrderID, in.Actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var linked bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from order_line_production where tenant_id = $1 and line_id = $2)`, p.TenantID, in.LineID).Scan(&linked); err != nil {
				return err
			}
			if !linked {
				return problem(http.StatusNotFound, CodeOrderNotFound, "configured order line not found")
			}
		}
		return nil
	})
}
