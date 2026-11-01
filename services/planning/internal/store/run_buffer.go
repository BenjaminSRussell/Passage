package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

// RunBuffers returns the stored site-scope DDMRP buffers of the given items at
// siteID whose site-scope item coverage marks them as a decoupling point
// (FR-22.16: buffer positioning is set per item coverage), for a plan run to
// replenish them by buffer instead of by netting. Items without a buffer, or
// whose coverage is not a decoupling point, are absent;
// warehouse-scope buffers are never returned. With planEntity set, only buffers
// of that legal entity are read. itemIDs must be UUIDs.
func (s *Postgres) RunBuffers(ctx context.Context, siteID, planEntity string, itemIDs []string) ([]domain.RunBuffer, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	p, _ := tenant.FromContext(ctx)
	var out []domain.RunBuffer
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select b.item_id::text, b.dlt_days::text, b.planned_qty::text, b.priority::text, b.zone
			from ddmrp_buffer b
			join item_coverage c on c.tenant_id = b.tenant_id and c.item_id = b.item_id and c.site_id = b.site_id
				and c.planning_scope = 'site'
			where b.tenant_id = $1 and b.site_id = $2::uuid and b.warehouse_id is null and c.decoupling_point
				and b.item_id = any($3::uuid[]) and ($4::text = '' or b.legal_entity_id::text = $4::text)
			order by b.item_id`, p.TenantID, siteID, itemIDs, planEntity)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b domain.RunBuffer
			var dlt, qty, prio string
			if err := rows.Scan(&b.ItemID, &dlt, &qty, &prio, &b.Zone); err != nil {
				return err
			}
			if b.DLTDays, err = decimal.NewFromString(dlt); err != nil {
				return err
			}
			if b.PlannedQty, err = decimal.NewFromString(qty); err != nil {
				return err
			}
			if b.Priority, err = decimal.NewFromString(prio); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}
