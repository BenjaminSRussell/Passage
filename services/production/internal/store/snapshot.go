package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// CostSnapshotRun is one run of the order cost snapshot (PRD 4.21.16 `order-cost-snapshot`).
type CostSnapshotRun struct {
	LegalEntityID string
	// Date is the snapshot day (YYYY-MM-DD); empty is today, UTC.
	Date  string
	Actor string
}

// CostSnapshotResult says what a run wrote.
type CostSnapshotResult struct {
	LegalEntityID string `json:"legalEntityId"`
	SnapshotDate  string `json:"snapshotDate"`
	Orders        int    `json:"orders"`
}

// CostSnapshot is the planned and actual cost of one open order on one day.
type CostSnapshot struct {
	OrderID       string          `json:"orderId"`
	OrderNumber   string          `json:"orderNumber"`
	LegalEntityID string          `json:"legalEntityId"`
	SiteID        string          `json:"siteId"`
	SnapshotDate  string          `json:"snapshotDate"`
	Status        string          `json:"status"`
	Currency      string          `json:"currency"`
	QtyOrdered    decimal.Decimal `json:"qtyOrdered"`
	QtyReported   decimal.Decimal `json:"qtyReported"`
	PlannedCost   decimal.Decimal `json:"plannedCost"`
	ActualCost    decimal.Decimal `json:"actualCost"`
	WIPBalance    decimal.Decimal `json:"wipBalance"`
}

// CostSnapshotFilter narrows the snapshot list. From and To bound the snapshot date
// (inclusive); Cursor is "<date>|<orderId>" of the last row of the previous page.
type CostSnapshotFilter struct {
	LegalEntityID, OrderID, From, To, Cursor string
	Limit                                    int
}

// CostSnapshotPage is one page, newest day first.
type CostSnapshotPage struct {
	Items      []CostSnapshot `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// RunCostSnapshot writes the day's snapshot of every open order (Estimated to
// ReportedAsFinished) of the legal entity: planned cost from the estimate, actual cost from the
// posted issue and hours lines dated up to the day (reversals net out) and the WIP balance as of
// the day. A second run on the same day overwrites the day's row of each order (upsert by order
// and date, PRD 4.21.16), so it is idempotent; a row of an order that has left the open statuses
// since keeps the figures of the earlier run. A run is one statement in one transaction with one
// costsnapshot.taken event; runs of the same legal entity and day queue on a transaction-level
// advisory lock, so two concurrent runs never interleave their upserts. RLS limits it to the
// caller's legal entities.
func (s *Postgres) RunCostSnapshot(ctx context.Context, in CostSnapshotRun) (CostSnapshotResult, error) {
	p, _ := tenant.FromContext(ctx)
	if in.Date == "" {
		in.Date = today()
	}
	out := CostSnapshotResult{LegalEntityID: in.LegalEntityID, SnapshotDate: in.Date}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('production_cost_snapshot:' || $1 || ':' || $2 || ':' || $3, 0))`,
			p.TenantID, in.LegalEntityID, in.Date); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `insert into production_cost_snapshot (tenant_id, legal_entity_id, order_id, snapshot_date, order_number, site_id,
				status, currency, qty_ordered, qty_reported, planned_cost, actual_cost, wip_balance, created_by)
			select o.tenant_id, o.legal_entity_id, o.id, $3::date, o.order_number, o.site_id, o.status, o.currency, o.quantity, o.qty_raf,
				coalesce((select sum(c.planned_amount) from production_cost_estimate c where c.tenant_id = o.tenant_id and c.order_id = o.id), 0),
				coalesce((select sum(l.amount + l.overhead_amount) from production_journal_line l
					join production_journal j on j.tenant_id = l.tenant_id and j.id = l.journal_id
					where l.tenant_id = o.tenant_id and j.order_id = o.id and j.posted and j.posting_date <= $3::date
					  and l.kind in ('issue', 'hours')), 0),
				coalesce((select sum(w.amount_dr - w.amount_cr) from production_wip_posting w
					where w.tenant_id = o.tenant_id and w.order_id = o.id and w.wip and w.posting_date <= $3::date), 0),
				$4
			from production_order o
			where o.tenant_id = $1 and o.legal_entity_id = $2::uuid
			  and o.status in ('Estimated', 'Scheduled', 'Released', 'Started', 'ReportedAsFinished')
			on conflict (tenant_id, order_id, snapshot_date) do update set order_number = excluded.order_number, status = excluded.status,
				qty_ordered = excluded.qty_ordered, qty_reported = excluded.qty_reported, planned_cost = excluded.planned_cost,
				actual_cost = excluded.actual_cost, wip_balance = excluded.wip_balance, created_by = excluded.created_by, created_at = now()`,
			p.TenantID, in.LegalEntityID, in.Date, in.Actor)
		if err != nil {
			return err
		}
		out.Orders = int(tag.RowsAffected())
		return enqueue(ctx, tx, in.LegalEntityID, prodevents.CostSnapshotTaken, p.TenantID, in.Actor, prodevents.CostSnapshotTakenData{
			LegalEntityID: in.LegalEntityID, SnapshotDate: in.Date, Orders: out.Orders, Actor: in.Actor,
			OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)})
	})
	return out, err
}

// CostSnapshots lists the snapshots, newest day first then by order id, for the trend of the
// order KPIs. RLS limits the rows to the caller's legal entities.
func (s *Postgres) CostSnapshots(ctx context.Context, f CostSnapshotFilter) (CostSnapshotPage, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	cursorDate, cursorOrder := "", ""
	if f.Cursor != "" {
		for i := 0; i < len(f.Cursor); i++ {
			if f.Cursor[i] == '|' {
				cursorDate, cursorOrder = f.Cursor[:i], f.Cursor[i+1:]
				break
			}
		}
	}
	out := CostSnapshotPage{Items: []CostSnapshot{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select order_id::text, order_number, legal_entity_id::text, site_id::text, snapshot_date::text, status,
				currency::text, qty_ordered::text, qty_reported::text, planned_cost::text, actual_cost::text, wip_balance::text
			from production_cost_snapshot
			where tenant_id = $1 and ($2 = '' or legal_entity_id = nullif($2, '')::uuid) and ($3 = '' or order_id = nullif($3, '')::uuid)
			  and ($4 = '' or snapshot_date >= nullif($4, '')::date) and ($5 = '' or snapshot_date <= nullif($5, '')::date)
			  and ($6 = '' or (snapshot_date, order_id) < (nullif($6, '')::date, nullif($7, '')::uuid))
			order by snapshot_date desc, order_id desc limit $8`,
			p.TenantID, f.LegalEntityID, f.OrderID, f.From, f.To, cursorDate, cursorOrder, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c CostSnapshot
			var qo, qr, pc, ac, wb string
			if err := rows.Scan(&c.OrderID, &c.OrderNumber, &c.LegalEntityID, &c.SiteID, &c.SnapshotDate, &c.Status, &c.Currency, &qo, &qr, &pc, &ac, &wb); err != nil {
				return err
			}
			c.QtyOrdered, c.QtyReported, c.PlannedCost, c.ActualCost, c.WIPBalance = dec(qo), dec(qr), dec(pc), dec(ac), dec(wb)
			out.Items = append(out.Items, c)
		}
		return rows.Err()
	})
	if err != nil {
		return CostSnapshotPage{}, err
	}
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		last := out.Items[f.Limit-1]
		out.NextCursor = last.SnapshotDate + "|" + last.OrderID
	}
	return out, nil
}
