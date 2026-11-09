package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// Schedule problem codes.
const (
	CodeNotPromisable = "SLS-0037"
	CodeLineNotFound  = "SLS-0109"
)

// ScheduleLine is one delivery slice of an order line (FR-27.6). DeliveryDate
// is absent on an UNPROMISED slice. Source is LINE when the slice copies the
// line's confirmed ship date and ATP when a promise placed it; BucketDate is
// the ATP bucket a dated ATP slice drew on.
type ScheduleLine struct {
	ID           string          `json:"id"`
	LineNo       int             `json:"lineNo"`
	Seq          int             `json:"seq"`
	Quantity     decimal.Decimal `json:"quantity"`
	DeliveryDate *string         `json:"deliveryDate,omitempty"`
	Status       string          `json:"status"`
	Source       string          `json:"source"`
	BucketDate   *string         `json:"bucketDate,omitempty"`
}

// PromiseRequest places one confirmed line against caller-supplied ATP
// buckets until the projection exists (see NOT-IMPLEMENTED).
type PromiseRequest struct {
	LineNo  int
	Periods []domain.Period
	// CompleteOnly is whether the whole quantity must ship on one date. Nil
	// follows the partial delivery policy of the line's product and the
	// order's customer (COMPLETE_ONLY and ONE_SHIPMENT ask for it).
	CompleteOnly *bool
}

// PromiseResult is the order after a promise with the line's new schedule.
type PromiseResult struct {
	Order   Order          `json:"order"`
	Promise domain.Promise `json:"-"`
	// CompleteOnly is whether the promise had to carry the whole quantity on one
	// date, and PartialDelivery the policy the caller's choice or the default
	// came from ("" when the caller chose and the policy was not read).
	CompleteOnly    bool           `json:"completeOnly"`
	PartialDelivery string         `json:"partialDelivery,omitempty"`
	Schedule        []ScheduleLine `json:"scheduleLines"`
}

// BackorderItem is one schedule line promised after the requested date or not
// promised at all, on a confirmed order (FR-27.6, AC-27.8).
type BackorderItem struct {
	ScheduleLineID    string          `json:"scheduleLineId"`
	LegalEntityID     string          `json:"legalEntityId"`
	OrderID           string          `json:"orderId"`
	OrderNumber       string          `json:"orderNumber"`
	OrderVersion      int             `json:"orderVersion"`
	CustomerID        string          `json:"customerId"`
	LineID            string          `json:"lineId"`
	LineNo            int             `json:"lineNo"`
	ProductID         string          `json:"productId"`
	SiteID            string          `json:"siteId"`
	WarehouseID       string          `json:"warehouseId"`
	Quantity          decimal.Decimal `json:"quantity"`
	RequestedShipDate string          `json:"requestedShipDate"`
	PromisedDate      *string         `json:"promisedDate,omitempty"`
	DaysLate          *int            `json:"daysLate,omitempty"`
	Status            string          `json:"status"`
}

// BackorderFilter narrows the backorder list. Empty fields match everything.
type BackorderFilter struct {
	LegalEntityID string
	ProductID     string
	CustomerID    string
	PromisedTo    string // yyyy-mm-dd; undated lines are always listed
	Limit         int
}

func scanScheduleLines(rows pgx.Rows) ([]ScheduleLine, error) {
	defer rows.Close()
	out := []ScheduleLine{}
	for rows.Next() {
		var l ScheduleLine
		var qty string
		if err := rows.Scan(&l.ID, &l.LineNo, &l.Seq, &qty, &l.DeliveryDate, &l.Status, &l.Source, &l.BucketDate); err != nil {
			return nil, err
		}
		l.Quantity = decimal.RequireFromString(qty)
		out = append(out, l)
	}
	return out, rows.Err()
}

const scheduleCols = `id::text, line_no, seq, quantity::text, delivery_date::text, status, source, bucket_date::text`

func activeSchedule(ctx context.Context, tx pgx.Tx, tenantID, orderID string) (map[string][]ScheduleLine, error) {
	rows, err := tx.Query(ctx, `select line_id::text, `+scheduleCols+` from order_schedule_line
		where tenant_id = $1 and order_id = $2 and status in ('CONFIRMED', 'BACKORDER', 'UNPROMISED') order by line_no, seq`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]ScheduleLine{}
	for rows.Next() {
		var lineID, qty string
		var l ScheduleLine
		if err := rows.Scan(&lineID, &l.ID, &l.LineNo, &l.Seq, &qty, &l.DeliveryDate, &l.Status, &l.Source, &l.BucketDate); err != nil {
			return nil, err
		}
		l.Quantity = decimal.RequireFromString(qty)
		out[lineID] = append(out[lineID], l)
	}
	return out, rows.Err()
}

func insertSchedule(ctx context.Context, tx pgx.Tx, tenantID string, o Order, l Line, seq int, e domain.ScheduleEntry) error {
	var date, bucket *string
	if e.Date != "" {
		date = &e.Date
	}
	if e.BucketDate != "" {
		bucket = &e.BucketDate
	}
	_, err := tx.Exec(ctx, `insert into order_schedule_line (tenant_id, id, legal_entity_id, order_id, line_id, line_no, seq, quantity, delivery_date, status, source, bucket_date)
		values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::date, $10, $11, $12::date)`,
		tenantID, ids.New(), o.LegalEntityID, o.ID, l.ID, l.LineNo, seq, e.Quantity.String(), date, e.Status, e.Source, bucket)
	return err
}

func nextSeq(ctx context.Context, tx pgx.Tx, tenantID, lineID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `select coalesce(max(seq), 0) + 1 from order_schedule_line where tenant_id = $1 and line_id = $2`, tenantID, lineID).Scan(&n)
	return n, err
}

// firstDate is the date of the first dated schedule line, nil when none is dated.
func firstDate(lines []ScheduleLine) *string {
	for _, l := range lines {
		if l.DeliveryDate != nil {
			return l.DeliveryDate
		}
	}
	return nil
}

// syncScheduleTx makes the schedule lines of a confirmed order agree with its
// lines: a cancelled line's schedule is CANCELLED, a line whose schedule no
// longer adds up to its quantity or starts on another date than its confirmed
// ship date gets a single new schedule line (the old ones SUPERSEDED), and a
// schedule that still agrees is kept, so a promise split survives changes that
// do not touch the line's quantity or date. Statuses follow the requested date.
func syncScheduleTx(ctx context.Context, tx pgx.Tx, tenantID, orderID string) error {
	o, err := loadOrder(ctx, tx, tenantID, orderID, false)
	if err != nil {
		return err
	}
	active, err := activeSchedule(ctx, tx, tenantID, orderID)
	if err != nil {
		return err
	}
	for _, l := range o.Lines {
		cur := active[l.ID]
		if l.LineStatus == domain.LineCancelled {
			if len(cur) > 0 {
				if _, err := tx.Exec(ctx, `update order_schedule_line set status = 'CANCELLED', updated_at = now()
					where tenant_id = $1 and line_id = $2 and status in ('CONFIRMED', 'BACKORDER', 'UNPROMISED')`, tenantID, l.ID); err != nil {
					return err
				}
			}
			continue
		}
		sum := decimal.Zero
		for _, c := range cur {
			sum = sum.Add(c.Quantity)
		}
		if len(cur) > 0 && sum.Equal(l.Quantity) && sameDay(firstDate(cur), l.ConfirmedShipDate) {
			for _, c := range cur {
				date := ""
				if c.DeliveryDate != nil {
					date = *c.DeliveryDate
				}
				if want := domain.ScheduleStatus(date, l.RequestedShipDate); want != c.Status {
					if _, err := tx.Exec(ctx, `update order_schedule_line set status = $3, updated_at = now() where tenant_id = $1 and id = $2`,
						tenantID, c.ID, want); err != nil {
						return err
					}
				}
			}
			continue
		}
		if len(cur) > 0 {
			if _, err := tx.Exec(ctx, `update order_schedule_line set status = 'SUPERSEDED', updated_at = now()
				where tenant_id = $1 and line_id = $2 and status in ('CONFIRMED', 'BACKORDER', 'UNPROMISED')`, tenantID, l.ID); err != nil {
				return err
			}
		}
		seq, err := nextSeq(ctx, tx, tenantID, l.ID)
		if err != nil {
			return err
		}
		date := ""
		if l.ConfirmedShipDate != nil {
			date = *l.ConfirmedShipDate
		}
		if err := insertSchedule(ctx, tx, tenantID, o, l, seq, domain.ScheduleEntry{
			Date: date, Quantity: l.Quantity, Status: domain.ScheduleStatus(date, l.RequestedShipDate), Source: domain.SchedSourceLine}); err != nil {
			return err
		}
	}
	return nil
}

// allocationFloor is the first day the line's quota period allows, or ""
// when no allocation constrains it: a promise never lands before it.
func allocationFloor(ctx context.Context, tx pgx.Tx, tenantID, lineID string) (string, error) {
	var floor *string
	err := tx.QueryRow(ctx, `select max(q.period_start)::text from allocation_consumption c
		join allocation_quota q on q.tenant_id = c.tenant_id and q.id = c.quota_id
		where c.tenant_id = $1 and c.line_id = $2 and c.returned_at is null`, tenantID, lineID).Scan(&floor)
	if err != nil || floor == nil {
		return "", err
	}
	return *floor, nil
}

// PromiseLine places one open line of a CONFIRMED or PARTIALLY_SHIPPED order against the given ATP
// buckets (BR-27.1) and stores the result as schedule lines: the part that
// can ship by the requested date stays CONFIRMED, supply that arrives later
// becomes BACKORDER lines and quantity no supply covers an UNPROMISED line
// (FR-27.6, AC-27.8). The line's confirmed ship date becomes the first dated
// schedule line, and a quota period it drew from still holds it back
// (BR-27.15). The order gets a new version with the diff, and
// order.changed.v1 and atp.promised.v1 are written in the same transaction.
func (s *Postgres) PromiseLine(ctx context.Context, orderID string, version int, req PromiseRequest, actor string, now time.Time) (PromiseResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out PromiseResult
	err := s.orderTx(ctx, p.TenantID, &out.Order, func(tx pgx.Tx) error {
		o, err := loadOrder(ctx, tx, p.TenantID, orderID, true)
		if err != nil {
			return err
		}
		if o.Version != version {
			return versionConflict()
		}
		if !domain.Promisable(o.Status) {
			return problem(http.StatusUnprocessableEntity, CodeNotPromisable, fmt.Sprintf("order is %s; only a CONFIRMED or PARTIALLY_SHIPPED order can be promised", o.Status))
		}
		if o.OrderType == domain.OrderTypeJournal {
			return problem(http.StatusUnprocessableEntity, CodeNotPromisable, "a JOURNAL order never consumes ATP and cannot be promised (BR-27.7)")
		}
		var line *Line
		for i := range o.Lines {
			if o.Lines[i].LineNo == req.LineNo {
				line = &o.Lines[i]
			}
		}
		if line == nil {
			return problem(http.StatusNotFound, CodeLineNotFound, "order line not found")
		}
		if !domain.LineChangeable(line.LineStatus) {
			return problem(http.StatusUnprocessableEntity, CodeNotPromisable, fmt.Sprintf("line %d is %s and cannot be promised", line.LineNo, line.LineStatus))
		}
		if line.SupplementaryOf != nil {
			return problem(http.StatusUnprocessableEntity, CodeNotPromisable, fmt.Sprintf("line %d is a supplementary line; promise its parent line instead", line.LineNo))
		}
		requested, err := time.Parse(dayLayout, line.RequestedShipDate)
		if err != nil {
			return err
		}
		earliest, source := requested, domain.SourceATP
		floor, err := allocationFloor(ctx, tx, p.TenantID, line.ID)
		if err != nil {
			return err
		}
		if floor != "" && floor > line.RequestedShipDate {
			if earliest, err = time.Parse(dayLayout, floor); err != nil {
				return err
			}
			source = allocationSource
		}
		completeOnly, policy := false, ""
		if req.CompleteOnly != nil {
			completeOnly = *req.CompleteOnly
		} else {
			tol, err := resolveToleranceTx(ctx, tx, p.TenantID, o.LegalEntityID, line.ProductID, o.CustomerID)
			if err != nil {
				return err
			}
			policy = tol.Tolerance.PartialDelivery
			completeOnly = domain.CompleteOnlyFor(policy)
		}
		promise, err := domain.PromiseDate(req.Periods, earliest, line.Quantity, completeOnly)
		if err != nil {
			return problem(http.StatusBadRequest, "COM-0001", err.Error())
		}
		entries := domain.PlanSchedule(promise, req.Periods, requested)
		var newDate *string
		for _, e := range entries {
			if e.Date != "" {
				d := e.Date
				newDate = &d
				break
			}
		}
		// Replace the line's schedule, then move its confirmed date and that of
		// its supplementary lines, which follow their parent (BR-27.14).
		if _, err := tx.Exec(ctx, `update order_schedule_line set status = 'SUPERSEDED', updated_at = now()
			where tenant_id = $1 and line_id = $2 and status in ('CONFIRMED', 'BACKORDER', 'UNPROMISED')`, p.TenantID, line.ID); err != nil {
			return err
		}
		seq, err := nextSeq(ctx, tx, p.TenantID, line.ID)
		if err != nil {
			return err
		}
		for i, e := range entries {
			if err := insertSchedule(ctx, tx, p.TenantID, o, *line, seq+i, e); err != nil {
				return err
			}
		}
		var diff []domain.FieldChange
		repromised := []int{line.LineNo}
		if !sameDay(newDate, line.ConfirmedShipDate) {
			diff = append(diff, domain.FieldChange{LineNo: line.LineNo, Field: "confirmedShipDate", Old: dayOrEmpty(line.ConfirmedShipDate), New: dayOrEmpty(newDate)})
			if _, err := tx.Exec(ctx, `update sales_order_line set confirmed_ship_date = $4::date where tenant_id = $1 and order_id = $2 and id = $3`,
				p.TenantID, orderID, line.ID, newDate); err != nil {
				return err
			}
			for _, c := range o.Lines {
				if c.SupplementaryOf != nil && *c.SupplementaryOf == line.ID && c.LineStatus != domain.LineCancelled && !sameDay(newDate, c.ConfirmedShipDate) {
					diff = append(diff, domain.FieldChange{LineNo: c.LineNo, Field: "confirmedShipDate", Old: dayOrEmpty(c.ConfirmedShipDate), New: dayOrEmpty(newDate)})
					repromised = append(repromised, c.LineNo)
					if _, err := tx.Exec(ctx, `update sales_order_line set confirmed_ship_date = $4::date where tenant_id = $1 and order_id = $2 and id = $3`,
						p.TenantID, orderID, c.ID, newDate); err != nil {
						return err
					}
				}
			}
		}
		if len(diff) == 0 {
			diff = []domain.FieldChange{{LineNo: line.LineNo, Field: "schedule", Old: "", New: fmt.Sprintf("%d schedule lines", len(entries))}}
		}
		if err := syncScheduleTx(ctx, tx, p.TenantID, orderID); err != nil {
			return err
		}
		next := o.Version + 1
		if _, err := tx.Exec(ctx, `update sales_order set version = $3, updated_at = $4 where tenant_id = $1 and id = $2`, p.TenantID, orderID, next, now); err != nil {
			return err
		}
		const reason = "ATP promise"
		if err := insertVersion(ctx, tx, p.TenantID, o.LegalEntityID, orderID, next, actor, reason, diff, now); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "order", orderID, salesevents.OrderChanged, p.TenantID, salesevents.OrderChangedData{
			OrderID: orderID, Version: next, Reason: reason, Actor: actor, Diff: salesevents.DiffOf(diff), RepromisedLines: repromised}); err != nil {
			return err
		}
		slices := make([]salesevents.PromisedSlice, 0, len(entries))
		for _, e := range entries {
			slices = append(slices, salesevents.PromisedSlice{Date: e.Date, BucketDate: e.BucketDate, Quantity: e.Quantity.String(), Status: e.Status})
		}
		if err := enqueue(ctx, tx, "order", orderID, salesevents.ATPPromised, p.TenantID, salesevents.ATPPromisedData{
			OrderID: orderID, LineID: line.ID, LineNo: line.LineNo, ProductID: line.ProductID, SiteID: line.SiteID, WarehouseID: line.WarehouseID,
			Quantity: line.Quantity.String(), RequestedShipDate: line.RequestedShipDate, Source: source,
			Complete: promise.Complete, PromisedDate: dayOrEmpty(newDate), BackorderQty: backorderQty(entries), Schedule: slices,
			Actor: actor, Version: next}); err != nil {
			return err
		}
		order, err := loadOrder(ctx, tx, p.TenantID, orderID, false)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+scheduleCols+` from order_schedule_line
			where tenant_id = $1 and order_id = $2 and status in ('CONFIRMED', 'BACKORDER', 'UNPROMISED') order by line_no, seq`, p.TenantID, orderID)
		if err != nil {
			return err
		}
		schedule, err := scanScheduleLines(rows)
		if err != nil {
			return err
		}
		out = PromiseResult{Order: order, Promise: promise, CompleteOnly: completeOnly, PartialDelivery: policy, Schedule: schedule}
		return nil
	})
	return out, err
}

// backorderQty is the quantity of entries promised after the requested date or
// not at all.
func backorderQty(entries []domain.ScheduleEntry) string {
	sum := decimal.Zero
	for _, e := range entries {
		if e.Status != domain.SchedConfirmed {
			sum = sum.Add(e.Quantity)
		}
	}
	return sum.String()
}

// ListScheduleLines returns the active schedule lines of an order by line and
// sequence. An order that does not exist (or is outside the caller's scope) is 404.
func (s *Postgres) ListScheduleLines(ctx context.Context, orderID string) ([]ScheduleLine, error) {
	p, _ := tenant.FromContext(ctx)
	var out []ScheduleLine
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from sales_order where tenant_id = $1 and id = $2)`, p.TenantID, orderID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return orderNotFound()
		}
		rows, err := tx.Query(ctx, `select `+scheduleCols+` from order_schedule_line
			where tenant_id = $1 and order_id = $2 and status in ('CONFIRMED', 'BACKORDER', 'UNPROMISED') order by line_no, seq`, p.TenantID, orderID)
		if err != nil {
			return err
		}
		out, err = scanScheduleLines(rows)
		return err
	})
	return out, err
}

// ListBackorders lists the schedule lines that ship after the requested date
// or have no date, on confirmed orders, by promised date (undated last), then
// order number (FR-27.6).
func (s *Postgres) ListBackorders(ctx context.Context, f BackorderFilter) ([]BackorderItem, error) {
	p, _ := tenant.FromContext(ctx)
	out := []BackorderItem{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select sl.id::text, sl.legal_entity_id::text, o.id::text, o.order_number, o.version, o.customer_id::text, l.id::text, l.line_no,
				l.product_id::text, l.site_id::text, l.warehouse_id::text, sl.quantity::text, l.requested_ship_date::text, sl.delivery_date::text,
				(sl.delivery_date - l.requested_ship_date), sl.status
			from order_schedule_line sl
			join sales_order_line l on l.tenant_id = sl.tenant_id and l.id = sl.line_id
			join sales_order o on o.tenant_id = sl.tenant_id and o.id = sl.order_id
			where sl.tenant_id = $1 and sl.status in ('BACKORDER', 'UNPROMISED') and o.status in ('CONFIRMED', 'PARTIALLY_SHIPPED') and l.line_status not in ('CANCELLED', 'DELIVERED', 'INVOICED')
				and ($2 = '' or sl.legal_entity_id::text = $2) and ($3 = '' or l.product_id::text = $3) and ($4 = '' or o.customer_id::text = $4)
				and ($5 = '' or sl.delivery_date is null or sl.delivery_date <= nullif($5, '')::date)
			order by sl.delivery_date nulls last, o.order_number, l.line_no, sl.seq limit $6`,
			p.TenantID, f.LegalEntityID, f.ProductID, f.CustomerID, f.PromisedTo, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it BackorderItem
			var qty string
			if err := rows.Scan(&it.ScheduleLineID, &it.LegalEntityID, &it.OrderID, &it.OrderNumber, &it.OrderVersion, &it.CustomerID, &it.LineID, &it.LineNo, &it.ProductID,
				&it.SiteID, &it.WarehouseID, &qty, &it.RequestedShipDate, &it.PromisedDate, &it.DaysLate, &it.Status); err != nil {
				return err
			}
			it.Quantity = decimal.RequireFromString(qty)
			out = append(out, it)
		}
		return rows.Err()
	})
	return out, err
}
