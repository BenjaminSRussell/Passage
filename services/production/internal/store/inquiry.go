package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// WIPFilter narrows the WIP balance report (PRD 4.21.14). AsOf (YYYY-MM-DD) counts
// the WIP rows posted on or before it; empty fields do not filter. Cursor is the
// id of the last order of the previous page.
type WIPFilter struct {
	LegalEntityID string
	SiteID        string
	Status        domain.Status
	AsOf          string
	Cursor        string
	Limit         int
}

// WIPRow is one order of the WIP balance report: posted consumption (material,
// labor and overhead into WIP, net of reversals), the value received by reports
// as finished, what End cleared into variances, and the WIP balance left. The
// balance is Σ debit − Σ credit of the order's WIP rows.
type WIPRow struct {
	OrderID       string
	OrderNumber   string
	LegalEntityID string
	SiteID        string
	ProductID     string
	Status        domain.Status
	Currency      string
	Consumption   decimal.Decimal
	RAFValue      decimal.Decimal
	Settled       decimal.Decimal
	Balance       decimal.Decimal
}

// WIPTotal sums the report per currency over every order the filter selects.
type WIPTotal struct {
	Currency    string
	Orders      int
	Consumption decimal.Decimal
	RAFValue    decimal.Decimal
	Settled     decimal.Decimal
	Balance     decimal.Decimal
}

// WIPReport is a page of the WIP balance report and the totals of the whole filter.
type WIPReport struct {
	Items      []WIPRow
	Totals     []WIPTotal
	NextCursor string
}

// wipByOrder aggregates the WIP account rows of the orders the filter selects.
// Only orders with WIP rows up to the as-of date are listed.
const wipByOrder = `with wip as (
	select o.id, o.order_number, o.legal_entity_id, o.site_id, o.product_id, o.status, o.currency::text as currency,
		coalesce(sum(w.amount_dr - w.amount_cr) filter (where w.posting_type in ('wip-material', 'wip-labor', 'wip-overhead')), 0) as consumption,
		coalesce(sum(w.amount_cr - w.amount_dr) filter (where w.posting_type = 'wip-raf-offset'), 0) as raf_value,
		coalesce(sum(w.amount_cr - w.amount_dr) filter (where w.posting_type = 'wip-settlement'), 0) as settled,
		sum(w.amount_dr - w.amount_cr) as balance
	from production_order o
	join production_wip_posting w on w.tenant_id = o.tenant_id and w.order_id = o.id and w.wip
		and ($5 = '' or w.posting_date <= nullif($5, '')::date)
	where o.tenant_id = $1
	  and ($2 = '' or o.legal_entity_id = nullif($2, '')::uuid)
	  and ($3 = '' or o.site_id = nullif($3, '')::uuid)
	  and ($4 = '' or o.status = $4)
	group by o.id, o.order_number, o.legal_entity_id, o.site_id, o.product_id, o.status, o.currency
)
`

// WIPReport returns the WIP balance by order (PRD 4.21.14) from the WIP subledger,
// one page ordered by order id, with totals per currency over the whole filter.
// RLS limits it to the caller's legal entities.
func (s *Postgres) WIPReport(ctx context.Context, f WIPFilter) (WIPReport, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	out := WIPReport{Items: []WIPRow{}, Totals: []WIPTotal{}}
	args := []any{p.TenantID, f.LegalEntityID, f.SiteID, string(f.Status), f.AsOf}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, wipByOrder+`select id::text, order_number, legal_entity_id::text, site_id::text, product_id::text, status, currency,
				consumption::text, raf_value::text, settled::text, balance::text
			from wip where ($6 = '' or id > nullif($6, '')::uuid) order by id limit $7`, append(args, f.Cursor, f.Limit+1)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r WIPRow
			var status, consumption, raf, settled, balance string
			if err := rows.Scan(&r.OrderID, &r.OrderNumber, &r.LegalEntityID, &r.SiteID, &r.ProductID, &status, &r.Currency,
				&consumption, &raf, &settled, &balance); err != nil {
				rows.Close()
				return err
			}
			r.Status = domain.Status(status)
			r.Consumption, r.RAFValue, r.Settled, r.Balance = dec(consumption), dec(raf), dec(settled), dec(balance)
			out.Items = append(out.Items, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		totals, err := tx.Query(ctx, wipByOrder+`select currency, count(*), sum(consumption)::text, sum(raf_value)::text, sum(settled)::text, sum(balance)::text
			from wip group by currency order by currency`, args...)
		if err != nil {
			return err
		}
		defer totals.Close()
		for totals.Next() {
			var t WIPTotal
			var consumption, raf, settled, balance string
			if err := totals.Scan(&t.Currency, &t.Orders, &consumption, &raf, &settled, &balance); err != nil {
				return err
			}
			t.Consumption, t.RAFValue, t.Settled, t.Balance = dec(consumption), dec(raf), dec(settled), dec(balance)
			out.Totals = append(out.Totals, t)
		}
		return totals.Err()
	})
	if err != nil {
		return WIPReport{}, err
	}
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		out.NextCursor = out.Items[f.Limit-1].OrderID
	}
	return out, nil
}

// VarianceFilter narrows the variance report (PRD 4.21.14). From and To bound the
// date the variance was posted at End (UTC); empty fields do not filter. Cursor
// is the id of the last variance of the previous page.
type VarianceFilter struct {
	LegalEntityID string
	SiteID        string
	ItemGroup     string
	OrderID       string
	Type          domain.VarianceType
	From          string
	To            string
	Cursor        string
	Limit         int
}

// VarianceRow is one posted variance with the planned cost of its order's cost
// group and the variance as a percentage of it (nil when nothing was planned).
type VarianceRow struct {
	ID           string
	OrderID      string
	OrderNumber  string
	SiteID       string
	ItemGroup    string
	Currency     string
	Type         domain.VarianceType
	Group        domain.CostGroup
	Amount       decimal.Decimal
	Planned      decimal.Decimal
	PctOfPlanned *decimal.Decimal
	PostedOn     string
}

// VarianceTotal sums the variances of one type in one currency over the filter.
type VarianceTotal struct {
	Currency string
	Type     domain.VarianceType
	Amount   decimal.Decimal
}

// VarianceReport is a page of the variance report and the totals of the whole filter.
type VarianceReport struct {
	Items      []VarianceRow
	Totals     []VarianceTotal
	NextCursor string
}

const variancesSelected = `with v as (
	select v.id, v.order_id, o.order_number, o.site_id, o.item_group, v.currency::text as currency, v.variance_type, v.cost_group, v.amount,
		coalesce((select sum(c.planned_amount) from production_cost_estimate c
		          where c.tenant_id = v.tenant_id and c.order_id = v.order_id and c.cost_group = v.cost_group), 0) as planned,
		(v.created_at at time zone 'UTC')::date as posted_on
	from production_variance v
	join production_order o on o.tenant_id = v.tenant_id and o.id = v.order_id
	where v.tenant_id = $1
	  and ($2 = '' or v.legal_entity_id = nullif($2, '')::uuid)
	  and ($3 = '' or o.site_id = nullif($3, '')::uuid)
	  and ($4 = '' or o.item_group = $4)
	  and ($5 = '' or v.order_id = nullif($5, '')::uuid)
	  and ($6 = '' or v.variance_type = $6)
	  and ($7 = '' or (v.created_at at time zone 'UTC')::date >= nullif($7, '')::date)
	  and ($8 = '' or (v.created_at at time zone 'UTC')::date <= nullif($8, '')::date)
)
`

// VarianceReport lists posted variances (PRD 4.21.14) ordered by id (posting
// order), one page, with totals per currency and variance type over the filter.
func (s *Postgres) VarianceReport(ctx context.Context, f VarianceFilter) (VarianceReport, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	out := VarianceReport{Items: []VarianceRow{}, Totals: []VarianceTotal{}}
	args := []any{p.TenantID, f.LegalEntityID, f.SiteID, f.ItemGroup, f.OrderID, string(f.Type), f.From, f.To}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, variancesSelected+`select id::text, order_id::text, order_number, site_id::text, item_group, currency,
				variance_type, cost_group, amount::text, planned::text, posted_on::text
			from v where ($9 = '' or id > nullif($9, '')::uuid) order by id limit $10`, append(args, f.Cursor, f.Limit+1)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r VarianceRow
			var typ, group, amount, planned string
			if err := rows.Scan(&r.ID, &r.OrderID, &r.OrderNumber, &r.SiteID, &r.ItemGroup, &r.Currency, &typ, &group, &amount, &planned, &r.PostedOn); err != nil {
				rows.Close()
				return err
			}
			r.Type, r.Group, r.Amount, r.Planned = domain.VarianceType(typ), domain.CostGroup(group), dec(amount), dec(planned)
			r.PctOfPlanned = domain.VariancePct(r.Amount, r.Planned)
			out.Items = append(out.Items, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		totals, err := tx.Query(ctx, variancesSelected+`select currency, variance_type, sum(amount)::text from v
			group by currency, variance_type order by currency, variance_type`, args...)
		if err != nil {
			return err
		}
		defer totals.Close()
		for totals.Next() {
			var t VarianceTotal
			var typ, amount string
			if err := totals.Scan(&t.Currency, &typ, &amount); err != nil {
				return err
			}
			t.Type, t.Amount = domain.VarianceType(typ), dec(amount)
			out.Totals = append(out.Totals, t)
		}
		return totals.Err()
	})
	if err != nil {
		return VarianceReport{}, err
	}
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		out.NextCursor = out.Items[f.Limit-1].ID
	}
	return out, nil
}
