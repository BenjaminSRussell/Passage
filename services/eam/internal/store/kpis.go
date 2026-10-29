package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// AssetCost is the parts, labor and total cost of the work orders an asset
// completed in a window, in one currency (FR-44.13, 8.44.14 Cost per asset).
// Amounts are decimal strings with two places.
type AssetCost struct {
	AssetID     string `json:"assetId"`
	AssetNumber string `json:"assetNumber"`
	Name        string `json:"name"`
	AssetType   string `json:"assetType"`
	Currency    string `json:"currency"`
	WorkOrders  int    `json:"workOrders"`
	PartsCost   string `json:"partsCost"`
	LaborCost   string `json:"laborCost"`
	Total       string `json:"total"`
}

// CostFilter narrows the cost rollup: completion window [From, To) and an
// optional asset type.
type CostFilter struct {
	From, To  time.Time
	AssetType string
}

// CostByAsset sums parts and labor of the work orders completed in the window
// (COMPLETED or CLOSED, by actual end) per asset and currency, highest total
// first. Sums are numeric in SQL and decimal in Go; money is never a float.
func (s *Postgres) CostByAsset(ctx context.Context, f CostFilter) ([]AssetCost, error) {
	out := []AssetCost{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select w.asset_id::text, a.asset_number, a.name, a.asset_type, w.currency, count(*),
		    sum(w.parts_cost)::text, sum(w.labor_cost)::text
		  from work_order w
		  join asset a on a.tenant_id = w.tenant_id and a.id = w.asset_id
		  where w.tenant_id = $1 and w.status in ('COMPLETED', 'CLOSED')
		    and w.actual_end >= $2 and w.actual_end < $3 and ($4::text is null or a.asset_type = $4)
		  group by w.asset_id, a.asset_number, a.name, a.asset_type, w.currency
		  order by sum(w.parts_cost + w.labor_cost) desc, a.asset_number, w.currency`, t, f.From, f.To, optStr(f.AssetType))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c AssetCost
			var parts, labor string
			if err := rows.Scan(&c.AssetID, &c.AssetNumber, &c.Name, &c.AssetType, &c.Currency, &c.WorkOrders, &parts, &labor); err != nil {
				return err
			}
			p, err := decimal.NewFromString(parts)
			if err != nil {
				return err
			}
			l, err := decimal.NewFromString(labor)
			if err != nil {
				return err
			}
			c.PartsCost = p.StringFixed(2)
			c.LaborCost = l.StringFixed(2)
			c.Total = p.Add(l).StringFixed(2)
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// BacklogRow is the open work at one priority (FR-44.13, 8.44.14 Backlog).
// Open means not COMPLETED, CLOSED or CANCELED; overdue means the due date has
// passed. Backlog hours need estimated hours on jobs, which work orders do not
// carry yet (NOT-IMPLEMENTED.md).
type BacklogRow struct {
	Priority     int        `json:"priority"`
	OpenCount    int        `json:"openCount"`
	OverdueCount int        `json:"overdueCount"`
	OldestDueAt  *time.Time `json:"oldestDueAt,omitempty"`
}

// Backlog counts the open work orders per priority, optionally on one site,
// with the overdue count and the oldest due date.
func (s *Postgres) Backlog(ctx context.Context, siteID string) ([]BacklogRow, error) {
	out := []BacklogRow{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select w.priority, count(*), count(*) filter (where w.due_at < $2), min(w.due_at)
		  from work_order w
		  join asset a on a.tenant_id = w.tenant_id and a.id = w.asset_id
		  where w.tenant_id = $1 and w.status not in ('COMPLETED', 'CLOSED', 'CANCELED')
		    and ($3::uuid is null or a.site_id = $3)
		  group by w.priority order by w.priority`, t, s.now(), optStr(siteID))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b BacklogRow
			if err := rows.Scan(&b.Priority, &b.OpenCount, &b.OverdueCount, &b.OldestDueAt); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}
