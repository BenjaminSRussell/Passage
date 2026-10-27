package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
)

// ListSPCObservations returns the first-attempt results (retest 0) of one test on
// the newest maxOrders quality orders of one item that have such results, oldest
// order first (FR-33.11). Whole orders are returned, so no subgroup is cut short;
// the domain takes the control limits from the last 25 of them.
func (s *Postgres) ListSPCObservations(ctx context.Context, itemID, testID string, maxOrders int) ([]domain.Observation, error) {
	out := []domain.Observation{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with recent as (
				select r.quality_order_id, min(r.recorded_at) as first_at
				from quality_result r
				join quality_order o on o.tenant_id = r.tenant_id and o.id = r.quality_order_id
				where r.`+ownTenant+` and o.item_id = $1 and r.test_id = $2 and r.retest_no = 0
				group by r.quality_order_id
				order by first_at desc, r.quality_order_id desc
				limit $3)
			select r.quality_order_id::text, r.sample_no, coalesce(r.value_numeric::text, ''), r.passed, r.recorded_at
			from quality_result r
			join recent on recent.quality_order_id = r.quality_order_id
			where r.`+ownTenant+` and r.test_id = $2 and r.retest_no = 0
			order by recent.first_at, r.quality_order_id, r.sample_no`, itemID, testID, maxOrders)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o domain.Observation
			var value string
			if err := rows.Scan(&o.QualityOrderID, &o.SampleNo, &value, &o.Passed, &o.RecordedAt); err != nil {
				return err
			}
			if value != "" {
				v, err := decimal.NewFromString(value)
				if err != nil {
					return err
				}
				o.Value, o.HasValue = v, true
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}
