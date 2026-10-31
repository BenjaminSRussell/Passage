package store

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// Groupings beyond the dimension names.
const (
	GroupProduct     = "product"
	GroupLegalEntity = "legal_entity"
)

// GroupNames lists every accepted groupBy value.
func GroupNames() []string {
	return append([]string{GroupProduct, GroupLegalEntity}, domain.DimensionNames()...)
}

// MaxOnHandRows is the most groups one inquiry returns (PRD 3.16.19 budgets the
// query for up to 10k rows); a larger result is cut and flagged as truncated.
const MaxOnHandRows = 10000

// OnHandFilter selects projection rows and names the grouping. GroupBy holds
// "product" or dimension names; empty means product. Limit caps the number of
// groups returned (0 or above MaxOnHandRows means MaxOnHandRows).
type OnHandFilter struct {
	ProductID   string
	SiteID      string
	WarehouseID string
	BatchID     string
	// Owner and Segment filter on the owner (a vendor id) and segment
	// dimensions (FR-16.17, FR-16.20).
	Owner   string
	Segment string
	// AsOf (YYYY-MM-DD, empty for now) replays the ledger up to that date:
	// physical quantity, posted value, the blocked quantity and the negative
	// flag are answered. Availability and reservations are not replayed (a
	// reservation record moves reserved stock without a transaction), so
	// AvailPhysical, AvailOrdered and ReservedPhysical are left zero and the
	// API omits them.
	AsOf    string
	GroupBy []string
	Limit   int
}

// OnHandResult is the inquiry answer; Truncated is true when more groups
// matched than the limit allowed.
type OnHandResult struct {
	Rows      []OnHandRow
	Truncated bool
	// AsOf echoes the filter date when the rows are a date replay.
	AsOf string
}

// asOfSource replays inventory_transaction up to the date $8 into the column
// layout of on_hand. Each transaction counts at the status it held on the date:
// a transaction registered or moved through transactions:apply has status
// change rows, and its status on the date is the target of the last change
// dated on or before it (none: registered later, so it is left out); a
// transaction without history (journals, blockings, transfers, consignment)
// counts at its current status from its physical date. A transaction whose
// first recorded change is a move (not CREATE) existed before it, at that
// move's from-status. Physical quantity follows FR-16.3: financial statuses
// count their signed quantity, received and registered add, deducted and
// picked subtract; expected statuses (ordered, arrived, on order, reserved)
// add nothing to it. Posted value counts transactions at a financial status
// whose financial date (physical date when none) is on or before the date.
// Rows are summed per projection row, in the row's currency, so the negative
// flag and the currency split are the live projection's.
const asOfSource = `(select t.tenant_id, t.legal_entity_id, t.product_id, t.dimension_set_hash, oh.currency,
  sum(case when s.status in ('SOLD', 'PURCHASED') then t.qty
           when s.status in ('RECEIVED', 'REGISTERED') then abs(t.qty)
           when s.status in ('DEDUCTED', 'PICKED') then -abs(t.qty)
           else 0 end) as posted_qty,
  sum(case when s.status in ('SOLD', 'PURCHASED') and coalesce(t.financial_date, t.physical_date) <= $8::date
           then coalesce(t.cost_amount_posted, 0) else 0 end) as posted_value,
  0::numeric as received, 0::numeric as registered, 0::numeric as arrived, 0::numeric as ordered, 0::numeric as deducted,
  0::numeric as picked, 0::numeric as reserved_physical, 0::numeric as reserved_ordered, 0::numeric as on_order
  from inventory_transaction t
  join on_hand oh on oh.tenant_id = t.tenant_id and oh.legal_entity_id = t.legal_entity_id
    and oh.product_id = t.product_id and oh.dimension_set_hash = t.dimension_set_hash
  left join lateral (select c.to_status from inventory_transaction_status_change c
    where c.tenant_id = t.tenant_id and c.transaction_id = t.id and c.status_date <= $8::date
    order by c.status_date desc, c.occurred_at desc, c.id desc limit 1) last_change on true
  left join lateral (select c.command, c.from_status from inventory_transaction_status_change c
    where c.tenant_id = t.tenant_id and c.transaction_id = t.id
    order by c.status_date, c.occurred_at, c.id limit 1) first_change on true
  cross join lateral (select case
    when last_change.to_status is not null then last_change.to_status
    when t.physical_date > $8::date then null
    when first_change.command is null then coalesce(t.issue_status, t.receipt_status)
    else first_change.from_status end as status) s
  where t.tenant_id = $1 and s.status is not null
  group by t.tenant_id, t.legal_entity_id, t.product_id, t.dimension_set_hash, oh.currency)`

// OnHandRow is one aggregated line of the on-hand inquiry.
type OnHandRow struct {
	Group            map[string]string
	PhysicalInvent   decimal.Decimal
	AvailPhysical    decimal.Decimal
	AvailOrdered     decimal.Decimal
	PostedValue      decimal.Decimal
	ReservedPhysical decimal.Decimal
	// Blocked is the physical quantity held with inventory status BLOCKED or
	// QUARANTINE; it is part of PhysicalInvent and left out of the avail sums.
	Blocked decimal.Decimal
	// Currency of PostedValue. Every group is split by currency, so value
	// totals never mix currencies (legal entities may keep different ones).
	Currency string
	// Negative is true when any underlying row has negative physical stock, so
	// a negative row is flagged even when the group total is positive (FR-16.4).
	Negative bool
}

// ListOnHand aggregates the OnHand projection over the finest rows with the
// formulas of FR-16.3. Group names are checked against the dimension whitelist
// before they are placed in SQL.
func (s *Postgres) ListOnHand(ctx context.Context, f OnHandFilter) (OnHandResult, error) {
	var groups []string
	for _, g := range f.GroupBy {
		if !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		groups = []string{GroupProduct}
	}
	limit := f.Limit
	if limit <= 0 || limit > MaxOnHandRows {
		limit = MaxOnHandRows
	}
	selects := make([]string, 0, len(groups)+5)
	positions := make([]string, 0, len(groups))
	for i, g := range groups {
		expr, err := groupExpr(g)
		if err != nil {
			return OnHandResult{}, err
		}
		selects = append(selects, fmt.Sprintf("%s as g%d", expr, i))
		positions = append(positions, fmt.Sprint(i+1))
	}
	selects = append(selects, "coalesce(o.currency::text, '')")
	positions = append(positions, fmt.Sprint(len(groups)+1))
	// Blocked and quarantined rows count in physical stock but not in
	// availability (FR-16.8, BR-16.10); their physical quantity is reported
	// as blocked.
	const phys = "o.posted_qty + o.received + o.registered - o.deducted - o.picked"
	const restricted = "coalesce(d.dims->>'inventory_status', '') in ('BLOCKED', 'QUARANTINE')"
	selects = append(selects,
		"sum("+phys+")::text",
		"coalesce(sum("+phys+" - o.reserved_physical) filter (where not "+restricted+"), 0)::text",
		"coalesce(sum("+phys+" - o.reserved_physical + o.ordered + o.arrived - o.on_order - o.reserved_ordered) filter (where not "+restricted+"), 0)::text",
		"sum(o.posted_value)::text",
		"sum(o.reserved_physical)::text",
		"coalesce(sum("+phys+") filter (where "+restricted+"), 0)::text",
		"bool_or(("+phys+") < 0)",
	)
	source, asOf := "on_hand", f.AsOf
	if asOf != "" {
		source = asOfSource
	}
	q := fmt.Sprintf(`select %s from %s o
		join dimension_set d on d.tenant_id = o.tenant_id and d.hash = o.dimension_set_hash
		where o.tenant_id = $1 and ($2 = '' or o.product_id::text = $2) and ($3 = '' or d.dims->>'site' = $3)
		and ($4 = '' or d.dims->>'warehouse' = $4) and ($5 = '' or d.dims->>'batch' = $5)
		and ($6 = '' or d.dims->>'owner' = $6) and ($7 = '' or d.dims->>'segment' = $7)
		group by %s order by %s limit %d`, strings.Join(selects, ", "), source, strings.Join(positions, ", "), strings.Join(positions, ", "), limit+1)

	p, _ := tenant.FromContext(ctx)
	out := []OnHandRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		args := []any{p.TenantID, f.ProductID, f.SiteID, f.WarehouseID, f.BatchID, f.Owner, f.Segment}
		if asOf != "" {
			args = append(args, asOf)
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			g := make([]string, len(groups))
			var currency, phys, avail, ordered, value, reserved, blocked string
			var negative bool
			dest := make([]any, 0, len(groups)+8)
			for i := range g {
				dest = append(dest, &g[i])
			}
			dest = append(dest, &currency, &phys, &avail, &ordered, &value, &reserved, &blocked, &negative)
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			r := OnHandRow{Group: map[string]string{}, PhysicalInvent: decimal.RequireFromString(phys),
				AvailPhysical: decimal.RequireFromString(avail), AvailOrdered: decimal.RequireFromString(ordered),
				PostedValue: decimal.RequireFromString(value), ReservedPhysical: decimal.RequireFromString(reserved), Blocked: decimal.RequireFromString(blocked),
				Currency: currency, Negative: negative}
			if asOf != "" {
				r.AvailPhysical, r.AvailOrdered, r.ReservedPhysical = decimal.Zero, decimal.Zero, decimal.Zero
			}
			for i, name := range groups {
				r.Group[name] = g[i]
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return OnHandResult{}, err
	}
	res := OnHandResult{Rows: out, AsOf: asOf}
	if len(out) > limit {
		res.Rows, res.Truncated = out[:limit], true
	}
	return res, nil
}

// groupExpr maps a grouping name to a SQL expression. Only "product" and the
// dimension whitelist are accepted, so the name is safe to place in SQL.
func groupExpr(name string) (string, error) {
	switch name {
	case GroupProduct:
		return "o.product_id::text", nil
	case GroupLegalEntity:
		return "o.legal_entity_id::text", nil
	}
	for _, d := range domain.DimensionNames() {
		if d == name {
			return fmt.Sprintf("coalesce(d.dims->>'%s', '')", d), nil
		}
	}
	return "", domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: fmt.Sprintf("cannot group by %q", name)})
}
