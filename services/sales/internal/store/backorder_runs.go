package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// Problem codes of backorder runs.
const (
	CodeBackorderRunRefused = "SLS-0027"
	CodeRunCommitted        = "SLS-0041"
	CodeRunStale            = "SLS-0042"
	CodeRunNotFound         = "SLS-0111"

	// MaxBackorderRunLines bounds the lines of one run (one product at one
	// site and warehouse).
	MaxBackorderRunLines = 10000

	backorderRunSource = "BACKORDER_RUN"
)

// BackorderRunLine is one open order line of a run: what it holds now (Before,
// the quantity with a promised date) and what the strategy gives it (After).
// GainDate is the date a gain is promised on.
type BackorderRunLine struct {
	Seq           int             `json:"seq"`
	OrderID       string          `json:"orderId"`
	OrderNumber   string          `json:"orderNumber"`
	OrderVersion  int             `json:"orderVersion"`
	LineID        string          `json:"lineId"`
	LineNo        int             `json:"lineNo"`
	CustomerID    string          `json:"customerId"`
	PriorityClass int             `json:"priorityClass"`
	RequestedDate string          `json:"requestedDate"`
	OrderValue    decimal.Decimal `json:"orderValue"`
	Demand        decimal.Decimal `json:"demand"`
	Before        decimal.Decimal `json:"before"`
	After         decimal.Decimal `json:"after"`
	Changed       bool            `json:"changed"`
	Deconfirmed   bool            `json:"deconfirmed"`
	GainDate      *string         `json:"gainDate,omitempty"`
}

// BackorderRun is a stored backorder processing run (FR-27.17). It is a
// PREVIEW until committed; a committed run is history.
type BackorderRun struct {
	ID               string             `json:"id"`
	LegalEntityID    string             `json:"legalEntityId"`
	ProductID        string             `json:"productId"`
	SiteID           string             `json:"siteId"`
	WarehouseID      string             `json:"warehouseId"`
	Strategy         string             `json:"strategy"`
	Supply           decimal.Decimal    `json:"supply"`
	SupplyDate       string             `json:"supplyDate"`
	LoseThreshold    int                `json:"loseThreshold"`
	DefaultPriority  int                `json:"defaultPriorityClass"`
	Status           string             `json:"status"`
	Allocated        decimal.Decimal    `json:"allocated"`
	DeconfirmedCount int                `json:"deconfirmedCount"`
	ChangedCount     int                `json:"changedCount"`
	Version          int                `json:"version"`
	CreatedBy        string             `json:"createdBy"`
	CreatedAt        time.Time          `json:"createdAt"`
	CommittedBy      *string            `json:"committedBy,omitempty"`
	CommittedAt      *time.Time         `json:"committedAt,omitempty"`
	Lines            []BackorderRunLine `json:"lines,omitempty"`
}

// NewBackorderRun is a validated request to preview a run. Priorities maps a
// customer id to its priority class; other customers get DefaultPriority.
type NewBackorderRun struct {
	LegalEntityID   string
	ProductID       string
	SiteID          string
	WarehouseID     string
	Strategy        string
	Supply          decimal.Decimal
	SupplyDate      string
	LoseThreshold   int
	DefaultPriority int
	Priorities      map[string]int
	IdempotencyKey  string
	RequestHash     string
	Actor           string
}

// BackorderRunFilter narrows the run list. Empty fields match everything.
type BackorderRunFilter struct {
	LegalEntityID string
	ProductID     string
	Status        string
	Limit         int
}

func runNotFound() error {
	return problem(http.StatusNotFound, CodeRunNotFound, "backorder run not found")
}

const runColumns = `id::text, legal_entity_id::text, product_id::text, site_id::text, warehouse_id::text, strategy, supply::text, supply_date::text,
	lose_threshold, default_priority, status, allocated::text, deconfirmed_count, changed_count, version, created_by, created_at, committed_by, committed_at`

func scanRun(row pgx.Row) (BackorderRun, error) {
	var r BackorderRun
	var supply, allocated string
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.ProductID, &r.SiteID, &r.WarehouseID, &r.Strategy, &supply, &r.SupplyDate, &r.LoseThreshold,
		&r.DefaultPriority, &r.Status, &allocated, &r.DeconfirmedCount, &r.ChangedCount, &r.Version, &r.CreatedBy, &r.CreatedAt, &r.CommittedBy, &r.CommittedAt); err != nil {
		return BackorderRun{}, err
	}
	r.Supply, r.Allocated = decimal.RequireFromString(supply), decimal.RequireFromString(allocated)
	return r, nil
}

func loadRun(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (BackorderRun, error) {
	q := `select ` + runColumns + ` from backorder_run where tenant_id = $1 and id = $2`
	if lock {
		q += " for update"
	}
	r, err := scanRun(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return BackorderRun{}, runNotFound()
	}
	if err != nil {
		return BackorderRun{}, err
	}
	rows, err := tx.Query(ctx, `select seq, order_id::text, order_number, order_version, line_id::text, line_no, customer_id::text, priority_class,
			requested_date::text, order_value::text, demand::text, before_qty::text, after_qty::text, changed, deconfirmed, gain_date::text
		from backorder_run_line where tenant_id = $1 and run_id = $2 order by seq`, tenantID, id)
	if err != nil {
		return BackorderRun{}, err
	}
	defer rows.Close()
	r.Lines = []BackorderRunLine{}
	for rows.Next() {
		var l BackorderRunLine
		var value, demand, before, after string
		if err := rows.Scan(&l.Seq, &l.OrderID, &l.OrderNumber, &l.OrderVersion, &l.LineID, &l.LineNo, &l.CustomerID, &l.PriorityClass,
			&l.RequestedDate, &value, &demand, &before, &after, &l.Changed, &l.Deconfirmed, &l.GainDate); err != nil {
			return BackorderRun{}, err
		}
		l.OrderValue, l.Demand = decimal.RequireFromString(value), decimal.RequireFromString(demand)
		l.Before, l.After = decimal.RequireFromString(before), decimal.RequireFromString(after)
		r.Lines = append(r.Lines, l)
	}
	return r, rows.Err()
}

// runLock serialises backorder commits (and previews) per (product, site,
// warehouse) within a tenant, like confirmation (BR-27.11).
func runLock(ctx context.Context, tx pgx.Tx, tenantID, productID, siteID, warehouseID string) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('sales.backorder_run:' || $1 || ':' || $2 || ':' || $3 || ':' || $4, 0))`,
		tenantID, productID, siteID, warehouseID)
	return err
}

// openRunLines reads the open lines of the product at the site and warehouse
// from the CONFIRMED and PARTIALLY_SHIPPED orders (JOURNAL orders,
// supplementary lines and lines past RESERVED, which a delivery has reached,
// are left out), with Before the quantity their schedule lines
// promise a date for, in (order id, line number) order. It reads at most
// MaxBackorderRunLines+1 lines so a caller can refuse a run that is too large.
func openRunLines(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, productID, siteID, warehouseID string) ([]BackorderRunLine, error) {
	rows, err := tx.Query(ctx, `select o.id::text, o.order_number, o.version, o.customer_id::text, l.id::text, l.line_no, l.quantity::text,
			l.requested_ship_date::text, l.line_net::text,
			coalesce((select sum(sl.quantity) from order_schedule_line sl where sl.tenant_id = l.tenant_id and sl.line_id = l.id
				and sl.status in ('CONFIRMED', 'BACKORDER')), 0)::text
		from sales_order_line l join sales_order o on o.tenant_id = l.tenant_id and o.id = l.order_id
		where l.tenant_id = $1 and o.legal_entity_id = $2 and l.product_id = $3 and l.site_id = $4 and l.warehouse_id = $5
			and o.status in ('CONFIRMED', 'PARTIALLY_SHIPPED') and o.order_type <> 'JOURNAL' and l.line_status in ('OPEN', 'RESERVED') and l.supplementary_of is null
		order by o.id, l.line_no limit $6`,
		tenantID, legalEntityID, productID, siteID, warehouseID, MaxBackorderRunLines+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lines []BackorderRunLine
	for rows.Next() {
		var l BackorderRunLine
		var demand, value, dated string
		if err := rows.Scan(&l.OrderID, &l.OrderNumber, &l.OrderVersion, &l.CustomerID, &l.LineID, &l.LineNo, &demand, &l.RequestedDate, &value, &dated); err != nil {
			return nil, err
		}
		l.Demand, l.OrderValue = decimal.RequireFromString(demand), decimal.RequireFromString(value)
		l.Before = decimal.Min(decimal.RequireFromString(dated), l.Demand)
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

// CreateBackorderRun reads the open lines of the product at the site and
// warehouse from the CONFIRMED and PARTIALLY_SHIPPED orders (JOURNAL orders,
// supplementary lines and lines past RESERVED are left out), plans the strategy over the supply and
// stores the result as a PREVIEW run. A line's confirmed quantity is what its
// schedule lines promise a date for. A repeat of the same Idempotency-Key and
// body returns the stored run with created=false. A line that gains quantity
// is promised on the latest of its requested date, the supply date and the
// start of the quota period it drew from (BR-27.15).
func (s *Postgres) CreateBackorderRun(ctx context.Context, in NewBackorderRun, now time.Time) (BackorderRun, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out BackorderRun
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := runLock(ctx, tx, p.TenantID, in.ProductID, in.SiteID, in.WarehouseID); err != nil {
			return err
		}
		var existing, hash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from backorder_run where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&existing, &hash)
		if err == nil {
			if hash != in.RequestHash {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out, err = loadRun(ctx, tx, p.TenantID, existing, false)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		lines, err := openRunLines(ctx, tx, p.TenantID, in.LegalEntityID, in.ProductID, in.SiteID, in.WarehouseID)
		if err != nil {
			return err
		}
		for i := range lines {
			lines[i].PriorityClass = in.DefaultPriority
			if c, ok := in.Priorities[lines[i].CustomerID]; ok {
				lines[i].PriorityClass = c
			}
		}
		if len(lines) == 0 {
			return problem(http.StatusUnprocessableEntity, CodeBackorderRunRefused, "no open line of a CONFIRMED or PARTIALLY_SHIPPED order is waiting for this product, site and warehouse")
		}
		if len(lines) > MaxBackorderRunLines {
			return problem(http.StatusUnprocessableEntity, CodeBackorderRunRefused, fmt.Sprintf("more than %d open lines; run fewer", MaxBackorderRunLines))
		}
		input := make([]domain.BackorderLine, 0, len(lines))
		for _, l := range lines {
			input = append(input, domain.BackorderLine{ID: l.LineID, Priority: l.PriorityClass, RequestedDate: l.RequestedDate,
				OrderValue: l.OrderValue, Demand: l.Demand, Confirmed: l.Before})
		}
		plan, err := domain.PlanBackorder(in.Strategy, in.Supply, in.LoseThreshold, input)
		if err != nil {
			return problem(http.StatusUnprocessableEntity, CodeBackorderRunRefused, err.Error())
		}
		byID := make(map[string]BackorderRunLine, len(lines))
		for _, l := range lines {
			byID[l.LineID] = l
		}
		ordered := make([]BackorderRunLine, 0, len(plan.Lines))
		changed := 0
		for i, pl := range plan.Lines {
			l := byID[pl.ID]
			l.Seq, l.After, l.Changed, l.Deconfirmed = i+1, pl.After, pl.Changed, pl.Deconfirmed
			if pl.Changed {
				changed++
			}
			if pl.After.GreaterThan(pl.Before) {
				date := l.RequestedDate
				if in.SupplyDate > date {
					date = in.SupplyDate
				}
				floor, err := allocationFloor(ctx, tx, p.TenantID, l.LineID)
				if err != nil {
					return err
				}
				if floor > date {
					date = floor
				}
				l.GainDate = &date
			}
			ordered = append(ordered, l)
		}

		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into backorder_run (tenant_id, id, legal_entity_id, product_id, site_id, warehouse_id, strategy, supply, supply_date,
				lose_threshold, default_priority, status, allocated, deconfirmed_count, changed_count, idempotency_key, request_hash, created_by, created_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::date, $10, $11, 'PREVIEW', $12::numeric, $13, $14, $15, $16, $17, $18)`,
			p.TenantID, id, in.LegalEntityID, in.ProductID, in.SiteID, in.WarehouseID, in.Strategy, in.Supply.String(), in.SupplyDate,
			in.LoseThreshold, in.DefaultPriority, plan.Allocated.String(), plan.DeconfirmedCount, changed, in.IdempotencyKey, in.RequestHash, in.Actor, now); err != nil {
			return err
		}
		for _, l := range ordered {
			if _, err := tx.Exec(ctx, `insert into backorder_run_line (tenant_id, id, legal_entity_id, run_id, seq, order_id, order_number, order_version, line_id,
					line_no, customer_id, priority_class, requested_date, order_value, demand, before_qty, after_qty, changed, deconfirmed, gain_date)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::date, $14::numeric, $15::numeric, $16::numeric, $17::numeric, $18, $19, $20::date)`,
				p.TenantID, ids.New(), in.LegalEntityID, id, l.Seq, l.OrderID, l.OrderNumber, l.OrderVersion, l.LineID, l.LineNo, l.CustomerID, l.PriorityClass,
				l.RequestedDate, l.OrderValue.String(), l.Demand.String(), l.Before.String(), l.After.String(), l.Changed, l.Deconfirmed, l.GainDate); err != nil {
				return err
			}
		}
		created = true
		out, err = loadRun(ctx, tx, p.TenantID, id, false)
		return err
	})
	if err != nil {
		return BackorderRun{}, false, err
	}
	return out, created, nil
}

// GetBackorderRun returns one run with its lines in processing order.
func (s *Postgres) GetBackorderRun(ctx context.Context, id string) (BackorderRun, error) {
	p, _ := tenant.FromContext(ctx)
	var out BackorderRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadRun(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ListBackorderRuns lists runs without their lines, newest first.
func (s *Postgres) ListBackorderRuns(ctx context.Context, f BackorderRunFilter) ([]BackorderRun, error) {
	p, _ := tenant.FromContext(ctx)
	out := []BackorderRun{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+runColumns+` from backorder_run
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or product_id::text = $3) and ($4 = '' or status = $4)
			order by created_at desc, id limit $5`, p.TenantID, f.LegalEntityID, f.ProductID, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// checkRunCurrent refuses a commit (409 SLS-0042) when the open lines the
// preview shared the supply over no longer hold what the preview saw: a line
// the run leaves unchanged now carries another promised quantity (another
// run or a promise moved it), or a line the preview did not see now carries a
// promised quantity. Either would let the commit promise more than the
// supply (AC-27.12: no cumulative ATP is negative). Every order of the run is
// locked first, in id order, so nothing moves a line between this check and
// the rewrite. Lines that left the run (cancelled, shipped) only free supply;
// a changed line's own order is checked again when it is applied.
func checkRunCurrent(ctx context.Context, tx pgx.Tx, tenantID string, run BackorderRun) error {
	seen := map[string]bool{}
	orderIDs := []string{}
	for _, l := range run.Lines {
		if !seen[l.OrderID] {
			seen[l.OrderID] = true
			orderIDs = append(orderIDs, l.OrderID)
		}
	}
	sort.Strings(orderIDs)
	if _, err := tx.Exec(ctx, `select 1 from sales_order where tenant_id = $1 and id = any($2::uuid[]) order by id for update`, tenantID, orderIDs); err != nil {
		return err
	}
	current, err := openRunLines(ctx, tx, tenantID, run.LegalEntityID, run.ProductID, run.SiteID, run.WarehouseID)
	if err != nil {
		return err
	}
	inRun := make(map[string]BackorderRunLine, len(run.Lines))
	for _, l := range run.Lines {
		inRun[l.LineID] = l
	}
	for _, c := range current {
		rl, ok := inRun[c.LineID]
		switch {
		case !ok && c.Before.IsPositive():
			return problem(http.StatusConflict, CodeRunStale, fmt.Sprintf("order %s line %d was promised %s since the preview; run a new preview", c.OrderNumber, c.LineNo, c.Before))
		case ok && !rl.Changed && !c.Before.Equal(rl.Before):
			return problem(http.StatusConflict, CodeRunStale, fmt.Sprintf("order %s line %d now holds %s promised, not %s, since the preview; run a new preview", c.OrderNumber, c.LineNo, c.Before, rl.Before))
		}
	}
	return nil
}

func entriesOf(lines []ScheduleLine) []domain.ScheduleEntry {
	out := make([]domain.ScheduleEntry, 0, len(lines))
	for _, l := range lines {
		e := domain.ScheduleEntry{Quantity: l.Quantity, Status: l.Status, Source: l.Source}
		if l.DeliveryDate != nil {
			e.Date = *l.DeliveryDate
		}
		if l.BucketDate != nil {
			e.BucketDate = *l.BucketDate
		}
		out = append(out, e)
	}
	return out
}

// CommitBackorderRun applies a PREVIEW run at its ETag in one transaction,
// serialised per (product, site, warehouse) like confirmation (BR-27.11): for
// each changed line the schedule lines are rewritten so that exactly the
// run's After quantity carries a promised date, the line's confirmed ship date
// (and its supplementary lines') follows the first dated schedule line, and
// each order gets a new version with the diff, order.changed, and
// atp.promised for every changed line; backorderrun.committed closes the run.
// An order that changed since the preview (its version moved, it is no longer
// CONFIRMED or PARTIALLY_SHIPPED, or the line is past RESERVED) refuses the whole commit with 409
// SLS-0042 and nothing is applied, so run a new preview.
func (s *Postgres) CommitBackorderRun(ctx context.Context, id string, version int, actor string, now time.Time) (BackorderRun, error) {
	p, _ := tenant.FromContext(ctx)
	var out BackorderRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		run, err := loadRun(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if err := runLock(ctx, tx, p.TenantID, run.ProductID, run.SiteID, run.WarehouseID); err != nil {
			return err
		}
		// A committed run answers a retried commit with SLS-0041, whatever ETag it sends.
		if run.Status != "PREVIEW" {
			return problem(http.StatusConflict, CodeRunCommitted, "backorder run was already committed")
		}
		if run.Version != version {
			return versionConflict()
		}
		if err := checkRunCurrent(ctx, tx, p.TenantID, run); err != nil {
			return err
		}
		byOrder := map[string][]BackorderRunLine{}
		var orderIDs []string
		for _, l := range run.Lines {
			if !l.Changed {
				continue
			}
			if _, ok := byOrder[l.OrderID]; !ok {
				orderIDs = append(orderIDs, l.OrderID)
			}
			byOrder[l.OrderID] = append(byOrder[l.OrderID], l)
		}
		sort.Strings(orderIDs)
		stale := func(l BackorderRunLine, why string) error {
			return problem(http.StatusConflict, CodeRunStale, fmt.Sprintf("order %s line %d %s since the preview; run a new preview", l.OrderNumber, l.LineNo, why))
		}
		var changedLines []salesevents.BackorderRunLineData
		for _, orderID := range orderIDs {
			o, err := loadOrder(ctx, tx, p.TenantID, orderID, true)
			if err != nil {
				return err
			}
			first := byOrder[orderID][0]
			if o.Version != first.OrderVersion || !domain.Promisable(o.Status) {
				return stale(first, "changed")
			}
			active, err := activeSchedule(ctx, tx, p.TenantID, orderID)
			if err != nil {
				return err
			}
			var diff []domain.FieldChange
			type promised struct {
				line    Line
				entries []domain.ScheduleEntry
				date    *string
				after   decimal.Decimal
			}
			var promises []promised
			for _, rl := range byOrder[orderID] {
				var line *Line
				for i := range o.Lines {
					if o.Lines[i].ID == rl.LineID {
						line = &o.Lines[i]
					}
				}
				if line == nil || !domain.LineChangeable(line.LineStatus) {
					return stale(rl, "is no longer open")
				}
				cur := entriesOf(active[line.ID])
				if !domain.DatedQuantity(cur).Equal(rl.Before) {
					return stale(rl, "has another schedule")
				}
				gain := ""
				if rl.GainDate != nil {
					gain = *rl.GainDate
				}
				entries, err := domain.RescheduleLine(cur, line.RequestedShipDate, rl.After, gain, run.SupplyDate)
				if err != nil {
					return problem(http.StatusConflict, CodeRunStale, err.Error())
				}
				if _, err := tx.Exec(ctx, `update order_schedule_line set status = 'SUPERSEDED', updated_at = now()
					where tenant_id = $1 and line_id = $2 and status in ('CONFIRMED', 'BACKORDER', 'UNPROMISED')`, p.TenantID, line.ID); err != nil {
					return err
				}
				seq, err := nextSeq(ctx, tx, p.TenantID, line.ID)
				if err != nil {
					return err
				}
				var newDate *string
				for i, e := range entries {
					if err := insertSchedule(ctx, tx, p.TenantID, o, *line, seq+i, e); err != nil {
						return err
					}
					if newDate == nil && e.Date != "" {
						d := e.Date
						newDate = &d
					}
				}
				diff = append(diff, domain.FieldChange{LineNo: line.LineNo, Field: "promisedQuantity", Old: rl.Before.String(), New: rl.After.String()})
				if !sameDay(newDate, line.ConfirmedShipDate) {
					diff = append(diff, domain.FieldChange{LineNo: line.LineNo, Field: "confirmedShipDate", Old: dayOrEmpty(line.ConfirmedShipDate), New: dayOrEmpty(newDate)})
					if _, err := tx.Exec(ctx, `update sales_order_line set confirmed_ship_date = $4::date where tenant_id = $1 and order_id = $2 and id = $3`,
						p.TenantID, orderID, line.ID, newDate); err != nil {
						return err
					}
					for _, c := range o.Lines {
						if c.SupplementaryOf != nil && *c.SupplementaryOf == line.ID && c.LineStatus != domain.LineCancelled && !sameDay(newDate, c.ConfirmedShipDate) {
							diff = append(diff, domain.FieldChange{LineNo: c.LineNo, Field: "confirmedShipDate", Old: dayOrEmpty(c.ConfirmedShipDate), New: dayOrEmpty(newDate)})
							if _, err := tx.Exec(ctx, `update sales_order_line set confirmed_ship_date = $4::date where tenant_id = $1 and order_id = $2 and id = $3`,
								p.TenantID, orderID, c.ID, newDate); err != nil {
								return err
							}
						}
					}
				}
				promises = append(promises, promised{line: *line, entries: entries, date: newDate, after: rl.After})
				changedLines = append(changedLines, salesevents.BackorderRunLineData{OrderID: orderID, LineID: line.ID, LineNo: line.LineNo,
					Before: rl.Before.String(), After: rl.After.String(), Deconfirmed: rl.Deconfirmed})
			}
			if err := syncScheduleTx(ctx, tx, p.TenantID, orderID); err != nil {
				return err
			}
			next := o.Version + 1
			if _, err := tx.Exec(ctx, `update sales_order set version = $3, updated_at = $4 where tenant_id = $1 and id = $2`, p.TenantID, orderID, next, now); err != nil {
				return err
			}
			reason := "Backorder run " + run.ID
			if err := insertVersion(ctx, tx, p.TenantID, o.LegalEntityID, orderID, next, actor, reason, diff, now); err != nil {
				return err
			}
			repromised := make([]int, 0, len(promises))
			for _, pr := range promises {
				repromised = append(repromised, pr.line.LineNo)
			}
			if err := enqueue(ctx, tx, "order", orderID, salesevents.OrderChanged, p.TenantID, salesevents.OrderChangedData{
				OrderID: orderID, Version: next, Reason: reason, Actor: actor, Diff: salesevents.DiffOf(diff), RepromisedLines: repromised}); err != nil {
				return err
			}
			for _, pr := range promises {
				slices := make([]salesevents.PromisedSlice, 0, len(pr.entries))
				for _, e := range pr.entries {
					slices = append(slices, salesevents.PromisedSlice{Date: e.Date, BucketDate: e.BucketDate, Quantity: e.Quantity.String(), Status: e.Status})
				}
				if err := enqueue(ctx, tx, "order", orderID, salesevents.ATPPromised, p.TenantID, salesevents.ATPPromisedData{
					OrderID: orderID, LineID: pr.line.ID, LineNo: pr.line.LineNo, ProductID: pr.line.ProductID, SiteID: pr.line.SiteID, WarehouseID: pr.line.WarehouseID,
					Quantity: pr.line.Quantity.String(), RequestedShipDate: pr.line.RequestedShipDate, Source: backorderRunSource,
					Complete: pr.after.Equal(pr.line.Quantity), PromisedDate: dayOrEmpty(pr.date), BackorderQty: backorderQty(pr.entries), Schedule: slices,
					Actor: actor, Version: next}); err != nil {
					return err
				}
			}
		}
		next := run.Version + 1
		if _, err := tx.Exec(ctx, `update backorder_run set status = 'COMMITTED', version = $3, committed_by = $4, committed_at = $5 where tenant_id = $1 and id = $2`,
			p.TenantID, id, next, actor, now); err != nil {
			return err
		}
		if changedLines == nil {
			changedLines = []salesevents.BackorderRunLineData{}
		}
		if err := enqueue(ctx, tx, "backorderrun", id, salesevents.BackorderRunCommitted, p.TenantID, salesevents.BackorderRunCommittedData{
			RunID: id, LegalEntityID: run.LegalEntityID, ProductID: run.ProductID, SiteID: run.SiteID, WarehouseID: run.WarehouseID, Strategy: run.Strategy, Supply: run.Supply.String(),
			Allocated: run.Allocated.String(), DeconfirmedCount: run.DeconfirmedCount, Lines: changedLines, Actor: actor, Version: next}); err != nil {
			return err
		}
		out, err = loadRun(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}
