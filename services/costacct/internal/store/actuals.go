package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// MaxActualRows bounds one read of cost actuals.
const MaxActualRows = 5000

// ActualRow is the cost ledger's actual cost of one cost object in one period and currency: what the journals booked on it
// (origin Journal) and what other cost objects' allocations, settlements and distributions moved onto or off it. The cost
// ledger holds both, so the total is the cost object's cost after allocation.
type ActualRow struct {
	CostObjectID string          `json:"costObjectId"`
	Kind         string          `json:"kind"`
	Code         string          `json:"code"`
	Name         string          `json:"name"`
	Period       string          `json:"period"`
	Currency     string          `json:"currency"`
	Primary      decimal.Decimal `json:"primary"`
	Allocated    decimal.Decimal `json:"allocated"`
	Total        decimal.Decimal `json:"total"`
}

// Actuals reads the cost ledger per cost object, period and currency of a legal entity for the periods from to to (YYYY-MM,
// both inclusive), optionally of one kind of cost object. Rows are ordered by code, then period.
func (s *Postgres) Actuals(ctx context.Context, legalEntityID, from, to, kind string) ([]ActualRow, error) {
	p, _ := tenant.FromContext(ctx)
	out := []ActualRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id::text, o.kind, o.code, o.name, e.period_key, e.currency,
				coalesce(sum(e.amount) filter (where e.origin = 'Journal'), 0)::text,
				coalesce(sum(e.amount) filter (where e.origin <> 'Journal'), 0)::text,
				sum(e.amount)::text
			from cost_entry e join cost_object o on o.tenant_id = e.tenant_id and o.id = e.cost_object_id
			where e.tenant_id = $1 and e.legal_entity_id = $2 and e.period_key >= $3 and e.period_key <= $4 and ($5 = '' or o.kind = $5)
			group by o.id, o.kind, o.code, o.name, e.period_key, e.currency
			order by o.code, e.period_key, e.currency limit $6`, p.TenantID, legalEntityID, from, to, kind, MaxActualRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ActualRow
			var primary, allocated, total string
			if err := rows.Scan(&r.CostObjectID, &r.Kind, &r.Code, &r.Name, &r.Period, &r.Currency, &primary, &allocated, &total); err != nil {
				return err
			}
			r.Primary, r.Allocated, r.Total = parseDec(primary), parseDec(allocated), parseDec(total)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
