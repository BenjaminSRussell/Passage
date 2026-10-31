package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// FirmedSupply is a firmed planned order that still supplies a plan: the
// target order it became has not been delivered yet (FR-22.1, FR-22.10). An
// approved order of the current version counts too, since the next run firms
// it (FR-22.11).
type FirmedSupply struct {
	OrderID      string
	ItemID       string
	OrderType    string
	Qty          decimal.Decimal
	DeliveryDate time.Time
}

// FirmedSupply lists the firmed planned orders of every operational version of
// a plan at a site whose delivery date is on or after from, in item and
// delivery order. A run replaces the whole version, so the orders firmed in
// earlier versions would otherwise drop out of the next run's supply and be
// planned a second time. The approved orders of the current version are
// listed as well: the run that reads them firms them (FR-22.11), so they are
// supply of that run and must not be planned again. Simulation versions never
// hold firmed orders (BR-22.14). An order whose delivery date has passed is
// taken as received and is part of the on-hand the snapshot carries.
func (s *Postgres) FirmedSupply(ctx context.Context, planCode, siteID string, from time.Time) ([]FirmedSupply, error) {
	p, _ := tenant.FromContext(ctx)
	out := []FirmedSupply{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id::text, o.item_id::text, o.order_type, o.qty::text, o.delivery_date
			from planned_order o join plan_version v on v.tenant_id = o.tenant_id and v.id = o.plan_version_id
			join plan p on p.tenant_id = v.tenant_id and p.id = v.plan_id
			where o.tenant_id = $1 and p.code = $2 and v.site_id = $3::uuid and v.kind <> 'simulation'
			and (o.status = 'firmed' or (o.status = 'approved' and v.status = 'current')) and o.delivery_date >= $4::date
			order by o.item_id, o.delivery_date, o.number, o.id`, p.TenantID, planCode, siteID, from.Format(dateLayout))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f FirmedSupply
			var qty string
			if err := rows.Scan(&f.OrderID, &f.ItemID, &f.OrderType, &qty, &f.DeliveryDate); err != nil {
				return err
			}
			if f.Qty, err = decimal.NewFromString(qty); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}
