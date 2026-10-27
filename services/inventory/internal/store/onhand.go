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
	GroupBy []string
	Limit   int
}

// OnHandResult is the inquiry answer; Truncated is true when more groups
// matched than the limit allowed.
type OnHandResult struct {
	Rows      []OnHandRow
	Truncated bool
}

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
	q := fmt.Sprintf(`select %s from on_hand o
		join dimension_set d on d.tenant_id = o.tenant_id and d.hash = o.dimension_set_hash
		where o.tenant_id = $1 and ($2 = '' or o.product_id::text = $2) and ($3 = '' or d.dims->>'site' = $3)
		and ($4 = '' or d.dims->>'warehouse' = $4) and ($5 = '' or d.dims->>'batch' = $5)
		and ($6 = '' or d.dims->>'owner' = $6) and ($7 = '' or d.dims->>'segment' = $7)
		group by %s order by %s limit %d`, strings.Join(selects, ", "), strings.Join(positions, ", "), strings.Join(positions, ", "), limit+1)

	p, _ := tenant.FromContext(ctx)
	out := []OnHandRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, p.TenantID, f.ProductID, f.SiteID, f.WarehouseID, f.BatchID, f.Owner, f.Segment)
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
	res := OnHandResult{Rows: out}
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
