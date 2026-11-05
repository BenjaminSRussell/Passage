package store

import (
	"context"
	"encoding/json"
	"errors"
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

// Order progress problem codes (BR-27.5).
const (
	CodeNotProgressable  = "SLS-0058"
	CodeProgressQuantity = "SLS-0059"
	CodeProgressRepeated = "SLS-0060"
)

// ProgressLineInput is the quantity a report covers on one line. ActualWeight
// is the catch-weight of a DELIVERED report (FR-24.7); absent, a catch-weight
// line stays priced at cases × nominal.
type ProgressLineInput struct {
	LineNo       int
	Quantity     decimal.Decimal
	ActualWeight *decimal.Decimal
	// Tracking names the batches and serials a DELIVERED report took (#480),
	// passed through on orderprogress.recorded so inventory issues them.
	Tracking []ProgressTracking
}

// ProgressTracking is the part of a delivered line taken from one batch or
// serial: Quantity in the line's unit (cases on a catch-weight line) and, on
// a catch-weight line split over several entries, the entry's ActualWeight.
type ProgressTracking struct {
	Batch        string
	Serial       string
	Quantity     decimal.Decimal
	ActualWeight *decimal.Decimal
}

// NewProgress is a delivery or invoice report for one order. SourceRef names
// the packing slip, shipment or invoice; the same (order, kind, source) is
// applied once.
type NewProgress struct {
	OrderID        string
	Kind           string
	SourceRef      string
	Lines          []ProgressLineInput
	IdempotencyKey string
	RequestHash    string
	Actor          string
}

// ProgressLineResult is one line of a stored report: the quantity it covered
// and the line after it.
type ProgressLineResult struct {
	LineID       string          `json:"lineId"`
	LineNo       int             `json:"lineNo"`
	Quantity     decimal.Decimal `json:"quantity"`
	DeliveredQty decimal.Decimal `json:"deliveredQty"`
	InvoicedQty  decimal.Decimal `json:"invoicedQty"`
	LineStatus   string          `json:"lineStatus"`
}

// OrderProgress is a stored delivery or invoice report.
type OrderProgress struct {
	ID            string               `json:"id"`
	LegalEntityID string               `json:"legalEntityId"`
	OrderID       string               `json:"orderId"`
	OrderNumber   string               `json:"orderNumber"`
	Kind          string               `json:"kind"`
	SourceRef     string               `json:"sourceRef"`
	Lines         []ProgressLineResult `json:"lines"`
	OrderStatus   string               `json:"orderStatus"`
	OrderVersion  int                  `json:"orderVersion"`
	RecordedBy    string               `json:"recordedBy"`
	RecordedAt    time.Time            `json:"recordedAt"`
	requestHash   string
}

// ProgressFilter narrows the list. Empty fields do not filter.
type ProgressFilter struct {
	LegalEntityID string
	OrderID       string
	Kind          string
	Limit         int
}

const progressColumns = `p.id::text, p.legal_entity_id::text, p.order_id::text, o.order_number, p.kind, p.source_ref, p.lines::text,
	p.order_status, p.order_version, p.recorded_by, p.recorded_at, p.request_hash`

func scanProgress(row pgx.Row) (OrderProgress, error) {
	var p OrderProgress
	var lines string
	if err := row.Scan(&p.ID, &p.LegalEntityID, &p.OrderID, &p.OrderNumber, &p.Kind, &p.SourceRef, &lines, &p.OrderStatus, &p.OrderVersion,
		&p.RecordedBy, &p.RecordedAt, &p.requestHash); err != nil {
		return p, err
	}
	return p, json.Unmarshal([]byte(lines), &p.Lines)
}

func loadProgressBy(ctx context.Context, tx pgx.Tx, where string, args ...any) (*OrderProgress, error) {
	p, err := scanProgress(tx.QueryRow(ctx, `select `+progressColumns+` from order_progress p
		join sales_order o on o.tenant_id = p.tenant_id and o.id = p.order_id where p.tenant_id = $1 and `+where, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// RecordProgress applies a delivery or invoice report to a CONFIRMED,
// PARTIALLY_SHIPPED (or, for an invoice, SHIPPED) order under the order's row
// lock: each line's delivered or invoiced quantity grows, its status follows
// (PICKED in part, DELIVERED, INVOICED), the order status is derived from its
// lines (PARTIALLY_SHIPPED, SHIPPED, INVOICED), the order gets a version with the
// field-level diff, and orderprogress.recorded is written, all in one
// transaction. A repeat of the Idempotency-Key, or of the same source with the
// same body, returns the stored report with created=false; the same source with
// another body is 409 SLS-0060.
func (s *Postgres) RecordProgress(ctx context.Context, in NewProgress, now time.Time) (OrderProgress, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out OrderProgress
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := loadOrder(ctx, tx, p.TenantID, in.OrderID, true)
		if err != nil {
			return err
		}
		if prior, err := loadProgressBy(ctx, tx, `p.idempotency_key = $2`, p.TenantID, in.IdempotencyKey); err != nil {
			return err
		} else if prior != nil {
			if prior.requestHash != in.RequestHash || prior.OrderID != in.OrderID {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out = *prior
			return nil
		}
		if prior, err := loadProgressBy(ctx, tx, `p.order_id = $2 and p.kind = $3 and p.source_ref = $4`, p.TenantID, in.OrderID, in.Kind, in.SourceRef); err != nil {
			return err
		} else if prior != nil {
			if prior.requestHash != in.RequestHash {
				return problem(http.StatusConflict, CodeProgressRepeated, fmt.Sprintf("%s %q was already recorded on this order with other lines or quantities", in.Kind, in.SourceRef))
			}
			out = *prior
			return nil
		}
		switch {
		case o.OrderType == domain.OrderTypeJournal:
			return problem(http.StatusUnprocessableEntity, CodeNotProgressable, "a JOURNAL order never ships or invoices (BR-27.7)")
		case o.OrderType == domain.OrderTypeReturned:
			return problem(http.StatusUnprocessableEntity, CodeNotProgressable, "a RETURNED order receives goods and is credited; returns record their own receipts and credit notes (wave 30)")
		case o.OrderType == domain.OrderTypeSubscription && in.Kind == domain.ProgressInvoiced:
			return problem(http.StatusUnprocessableEntity, CodeNotProgressable, "a SUBSCRIPTION order is invoiced per billing period by its billing subscription (FR-29.10), not against the line quantity")
		}
		if in.Kind == domain.ProgressDelivered && (o.Status == domain.OrderShipped || o.Status == domain.OrderInvoiced) {
			// Also where fulfillment posts a slip for a remainder that sales
			// closed before order.remaindercancelled reached it: the goods are not
			// on the order any more and need a return or a new order.
			return problem(http.StatusUnprocessableEntity, CodeNotProgressable, fmt.Sprintf(
				"order is %s: nothing is left to deliver; every line is delivered in full or its remainder was cancelled", o.Status))
		}
		if !progressable(o.Status, in.Kind) {
			return problem(http.StatusUnprocessableEntity, CodeNotProgressable, fmt.Sprintf("order is %s; %s cannot be recorded", o.Status, in.Kind))
		}
		byNo := make(map[int]*Line, len(o.Lines))
		for i := range o.Lines {
			byNo[o.Lines[i].LineNo] = &o.Lines[i]
		}
		before := make([]domain.LineProgress, len(o.Lines))
		for i, l := range o.Lines {
			before[i] = domain.LineProgress{Status: l.LineStatus, Quantity: l.Quantity, Delivered: l.DeliveredQty, Invoiced: l.InvoicedQty}
		}
		var diff []domain.FieldChange
		results := make([]ProgressLineResult, 0, len(in.Lines))
		evLines := make([]salesevents.ProgressLine, 0, len(in.Lines))
		for _, req := range in.Lines {
			l := byNo[req.LineNo]
			if l == nil {
				return problem(http.StatusNotFound, CodeLineNotFound, fmt.Sprintf("order line %d not found", req.LineNo))
			}
			cur := domain.LineProgress{Status: l.LineStatus, Quantity: l.Quantity, Delivered: l.DeliveredQty, Invoiced: l.InvoicedQty}
			next, err := domain.ApplyProgress(cur, in.Kind, req.Quantity)
			if err != nil {
				return problem(http.StatusUnprocessableEntity, CodeProgressQuantity, fmt.Sprintf("line %d: %s", l.LineNo, err))
			}
			oldNet := l.LineNet
			if err := applyCatchWeightProgress(l, in.Kind, req.Quantity, next.Delivered, req.ActualWeight); err != nil {
				return err
			}
			if !l.LineNet.Equal(oldNet) {
				diff = append(diff, domain.FieldChange{LineNo: l.LineNo, Field: "lineNet", Old: oldNet.String(), New: l.LineNet.String()})
			}
			if _, err := tx.Exec(ctx, `update sales_order_line set delivered_qty = $4::numeric, invoiced_qty = $5::numeric, line_status = $6,
					actual_weight = $7, line_net = $8::numeric
				where tenant_id = $1 and order_id = $2 and id = $3`, p.TenantID, o.ID, l.ID, next.Delivered.String(), next.Invoiced.String(), next.Status,
				nullDec(l.ActualWeight), l.LineNet.String()); err != nil {
				return err
			}
			if in.Kind == domain.ProgressDelivered {
				diff = append(diff, domain.FieldChange{LineNo: l.LineNo, Field: "deliveredQty", Old: cur.Delivered.String(), New: next.Delivered.String()})
			} else {
				diff = append(diff, domain.FieldChange{LineNo: l.LineNo, Field: "invoicedQty", Old: cur.Invoiced.String(), New: next.Invoiced.String()})
			}
			if next.Status != cur.Status {
				diff = append(diff, domain.FieldChange{LineNo: l.LineNo, Field: "lineStatus", Old: cur.Status, New: next.Status})
			}
			l.DeliveredQty, l.InvoicedQty, l.LineStatus = next.Delivered, next.Invoiced, next.Status
			results = append(results, ProgressLineResult{LineID: l.ID, LineNo: l.LineNo, Quantity: req.Quantity,
				DeliveredQty: next.Delivered, InvoicedQty: next.Invoiced, LineStatus: next.Status})
			ev := salesevents.ProgressLine{LineID: l.ID, LineNo: l.LineNo, Quantity: req.Quantity.String(),
				DeliveredQty: next.Delivered.String(), InvoicedQty: next.Invoiced.String(), LineStatus: next.Status,
				ProductID: l.ProductID, Unit: l.Unit, SiteID: l.SiteID, WarehouseID: l.WarehouseID}
			if in.Kind == domain.ProgressDelivered && l.CatchWeight != nil && l.ActualWeight != nil {
				reported := req.Quantity.Mul(l.CatchWeight.Nominal)
				if req.ActualWeight != nil {
					reported = *req.ActualWeight
				}
				ev.ReportedWeight, ev.DeliveredWeight = reported.String(), l.ActualWeight.String()
			}
			if len(req.Tracking) > 0 {
				if in.Kind != domain.ProgressDelivered {
					return problem(http.StatusUnprocessableEntity, CodeProgressQuantity, fmt.Sprintf("line %d: tracking applies only to a DELIVERED report", l.LineNo))
				}
				for _, t := range req.Tracking {
					if t.ActualWeight != nil && l.CatchWeight == nil {
						return problem(http.StatusUnprocessableEntity, domain.CodeCatchWeightInput, fmt.Sprintf("line %d is not a catch-weight item; tracking entries carry no actualWeight", l.LineNo))
					}
					et := salesevents.ProgressTracking{Batch: t.Batch, Serial: t.Serial, Quantity: t.Quantity.String()}
					if t.ActualWeight != nil {
						et.ReportedWeight = t.ActualWeight.String()
					}
					ev.Tracking = append(ev.Tracking, et)
				}
			}
			evLines = append(evLines, ev)
		}
		after := make([]domain.LineProgress, len(o.Lines))
		for i, l := range o.Lines {
			after[i] = domain.LineProgress{Status: l.LineStatus, Quantity: l.Quantity, Delivered: l.DeliveredQty, Invoiced: l.InvoicedQty}
		}
		status := domain.OrderStatusFor(after)
		if status != o.Status {
			diff = append(diff, domain.FieldChange{LineNo: 0, Field: "status", Old: o.Status, New: status})
		}
		total := netOf(o.Lines)
		if !total.Equal(o.NetTotal) {
			diff = append(diff, domain.FieldChange{LineNo: 0, Field: "netTotal", Old: o.NetTotal.String(), New: total.String()})
		}
		version := o.Version + 1
		if _, err := tx.Exec(ctx, `update sales_order set status = $3, version = $4, net_total = $5::numeric, updated_at = $6 where tenant_id = $1 and id = $2`,
			p.TenantID, o.ID, status, version, total.String(), now); err != nil {
			return err
		}
		reason := fmt.Sprintf("%s recorded: %s", in.Kind, in.SourceRef)
		if err := insertVersion(ctx, tx, p.TenantID, o.LegalEntityID, o.ID, version, in.Actor, reason, diff, now); err != nil {
			return err
		}
		raw, err := json.Marshal(results)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into order_progress (tenant_id, id, legal_entity_id, order_id, kind, source_ref, lines, order_status, order_version,
				idempotency_key, request_hash, recorded_by, recorded_at)
			values ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12, $13)`,
			p.TenantID, id, o.LegalEntityID, o.ID, in.Kind, in.SourceRef, string(raw), status, version, in.IdempotencyKey, in.RequestHash, in.Actor, now); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "order", o.ID, salesevents.OrderProgressRecorded, p.TenantID, salesevents.OrderProgressData{
			ProgressID: id, OrderID: o.ID, LegalEntityID: o.LegalEntityID, Currency: o.Currency, Kind: in.Kind, SourceRef: in.SourceRef, Lines: evLines,
			PreviousStatus: o.Status, OrderStatus: status, Actor: in.Actor, Version: version}); err != nil {
			return err
		}
		stored, err := loadProgressBy(ctx, tx, `p.id = $2`, p.TenantID, id)
		if err != nil {
			return err
		}
		out, created = *stored, true
		return nil
	})
	return out, created, err
}

// progressable reports whether a report of kind may be recorded on an order in
// status: goods ship on a CONFIRMED or PARTIALLY_SHIPPED order, and an invoice
// may also follow a SHIPPED one (a CONFIRMED order can be invoiced for what was
// delivered at a partial shipment).
func progressable(status, kind string) bool {
	switch status {
	case domain.OrderConfirmed, domain.OrderPartiallyShipped:
		return true
	case domain.OrderShipped:
		return kind == domain.ProgressInvoiced
	}
	return false
}

// ListProgress returns stored reports in the caller's scope, newest first.
func (s *Postgres) ListProgress(ctx context.Context, f ProgressFilter) ([]OrderProgress, error) {
	p, _ := tenant.FromContext(ctx)
	out := []OrderProgress{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+progressColumns+` from order_progress p
			join sales_order o on o.tenant_id = p.tenant_id and o.id = p.order_id
			where p.tenant_id = $1 and ($2 = '' or p.legal_entity_id::text = $2) and ($3 = '' or p.order_id::text = $3) and ($4 = '' or p.kind = $4)
			order by p.recorded_at desc, p.id limit $5`, p.TenantID, f.LegalEntityID, f.OrderID, f.Kind, f.Limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OrderProgress, error) { return scanProgress(row) })
		return err
	})
	return out, err
}

func applyCatchWeightProgress(l *Line, kind string, cases decimal.Decimal, delivered decimal.Decimal, actual *decimal.Decimal) error {
	if l.CatchWeight != nil && !domain.WholeCases(cases) {
		// Every report on a catch-weight line counts whole cases, a delivery
		// without a weight (priced at nominal) and an invoice included.
		return problem(http.StatusUnprocessableEntity, domain.CodeCatchWeightInput,
			fmt.Sprintf("line %d is a catch-weight item; a %s report must cover a whole number of cases", l.LineNo, kind))
	}
	if actual != nil {
		switch {
		case kind != domain.ProgressDelivered:
			return problem(http.StatusUnprocessableEntity, domain.CodeCatchWeightInput, "actualWeight applies only to a DELIVERED report")
		case l.CatchWeight == nil:
			return problem(http.StatusUnprocessableEntity, domain.CodeCatchWeightInput, fmt.Sprintf("line %d is not a catch-weight item", l.LineNo))
		}
		if err := domain.ValidateCatchWeight(*l.CatchWeight, cases, *actual); err != nil {
			var cw *domain.CatchWeightError
			if errors.As(err, &cw) {
				return problem(http.StatusUnprocessableEntity, cw.Code, fmt.Sprintf("line %d: %s", l.LineNo, cw.Detail))
			}
			return err
		}
	}
	if kind != domain.ProgressDelivered || l.CatchWeight == nil {
		return nil
	}
	reported := cases.Mul(l.CatchWeight.Nominal)
	if actual != nil {
		reported = *actual
	}
	total := reported
	if l.ActualWeight != nil {
		total = total.Add(*l.ActualWeight)
	} else {
		total = total.Add(l.DeliveredQty.Mul(l.CatchWeight.Nominal))
	}
	net, err := domain.LineNetFor(l.Quantity, delivered, l.UnitPrice, l.DiscPct, l.CatchWeight, &total)
	if err != nil {
		return problem(http.StatusUnprocessableEntity, CodeProgressQuantity, fmt.Sprintf("line %d: %s", l.LineNo, err))
	}
	l.ActualWeight, l.LineNet = &total, net
	return nil
}

func nullDec(d *decimal.Decimal) any {
	if d == nil {
		return nil
	}
	return d.String()
}
