package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// NetRequirement is one day of the MD04-style view of an item in the current
// version of a plan (FR-22.12). Quantities are decimal strings in the
// inventory unit; PAB(t) = PAB(t−1) + SR + PR − GR.
type NetRequirement struct {
	ItemID            string `json:"itemId"`
	Date              string `json:"date"`
	GrossRequirements string `json:"grossRequirements"`
	ScheduledReceipts string `json:"scheduledReceipts"`
	PlannedReceipts   string `json:"plannedReceipts"`
	PAB               string `json:"projectedAvailableBalance"`
	NetRequirement    string `json:"netRequirement"`
}

// ListNetRequirements reads the time-phased net requirements of one item in
// the current version of a plan, in date order. A plan without a current
// version, or an item it did not plan, has no rows.
func (s *Postgres) ListNetRequirements(ctx context.Context, planCode, itemID string, limit int) ([]NetRequirement, error) {
	p, _ := tenant.FromContext(ctx)
	out := []NetRequirement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select n.item_id::text, to_char(n.day, 'YYYY-MM-DD'), n.gross_requirements::text,
			n.scheduled_receipts::text, n.planned_receipts::text, n.pab::text, n.net_requirement::text
			from net_requirement n join plan_version v on v.tenant_id = n.tenant_id and v.id = n.plan_version_id
			join plan p on p.tenant_id = v.tenant_id and p.id = v.plan_id
			where n.tenant_id = $1 and p.code = $2 and v.status = 'current' and n.item_id = $3::uuid
			order by n.day limit $4`, p.TenantID, planCode, itemID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n NetRequirement
			if err := rows.Scan(&n.ItemID, &n.Date, &n.GrossRequirements, &n.ScheduledReceipts, &n.PlannedReceipts, &n.PAB, &n.NetRequirement); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}
