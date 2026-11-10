package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// CodeInboundChangeNotFound is a change id the caller cannot see.
const CodeInboundChangeNotFound = "PRC-INB-NOT-FOUND"

// InboundChange is one change another service asked of a purchase order (slice 18): a planning message
// a planner applied, or the closed remainder of a drop-ship. Status says what happened to the request:
// APPLIED (the line moved; ReapprovalRequired when the change waits for re-approval as version VersionNo),
// REFUSED (a purchasing rule stopped it, Reason says which) or IGNORED (nothing to do).
type InboundChange struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	Source        string `json:"source"`
	SourceKey     string `json:"sourceKey"`
	EventID       string `json:"eventId"`
	Action        string `json:"action"`
	// PurchaseOrderID is empty when the request named no order we hold.
	PurchaseOrderID    string           `json:"purchaseOrderId,omitempty"`
	PONumber           string           `json:"poNumber,omitempty"`
	LineNo             int              `json:"lineNo,omitempty"`
	LineID             string           `json:"lineId,omitempty"`
	ProductID          string           `json:"productId,omitempty"`
	Status             string           `json:"status"`
	ReasonCode         string           `json:"reasonCode,omitempty"`
	Reason             string           `json:"reason,omitempty"`
	RequestedQty       *decimal.Decimal `json:"requestedQty,omitempty"`
	RequestedDate      *domain.Day      `json:"requestedDate,omitempty"`
	QtyBefore          *decimal.Decimal `json:"qtyBefore,omitempty"`
	QtyAfter           *decimal.Decimal `json:"qtyAfter,omitempty"`
	DateBefore         *domain.Day      `json:"dateBefore,omitempty"`
	DateAfter          *domain.Day      `json:"dateAfter,omitempty"`
	VersionNo          int              `json:"versionNo,omitempty"`
	ReapprovalRequired bool             `json:"reapprovalRequired"`
	RequestedBy        string           `json:"requestedBy,omitempty"`
	Note               string           `json:"note,omitempty"`
	CreatedAt          time.Time        `json:"createdAt"`
	// WarehouseID is the planning scope of a planning message (kept so a retry plans the same lines).
	WarehouseID string `json:"warehouseId,omitempty"`
	// Attempts counts the runs of the request: the consumer's first run and every retry.
	Attempts int `json:"attempts"`
	// Resolution says how a REFUSED change was settled: empty while it waits, ACKNOWLEDGED by a buyer, or
	// RETRIED once a retry took it out of REFUSED (the row then shows the retry's outcome).
	Resolution     string     `json:"resolution,omitempty"`
	ResolvedBy     string     `json:"resolvedBy,omitempty"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
	ResolutionNote string     `json:"resolutionNote,omitempty"`
}

// InboundFilter narrows ListInboundChanges; empty fields do not filter.
type InboundFilter struct {
	Source          string
	Status          string
	PurchaseOrderID string
	LegalEntityID   string
	// Resolution is NONE (not settled yet), ACKNOWLEDGED or RETRIED; empty does not filter.
	Resolution string
}

// DropShipClosedIn is the part of fulfillment's dropship.closed.v1 the consumer uses.
type DropShipClosedIn struct {
	EventID             string
	LegalEntityID       string
	DropShipID          string
	DropShipNumber      string
	PurchaseOrderID     string
	PurchaseOrderLineID string
	// QtyClosed is the drop-ship's running closed total after the event, as fulfillment publishes it. A
	// drop-ship can be closed more than once (sales cutting the order back closes part of it, the buyer
	// closes the rest), so the line goes down by what this total adds to the closings already handled.
	QtyClosed decimal.Decimal
}

// DropShipCancelledIn is the part of fulfillment's dropship.cancelled.v1 the consumer uses.
type DropShipCancelledIn struct {
	EventID             string
	LegalEntityID       string
	DropShipID          string
	DropShipNumber      string
	PurchaseOrderID     string
	PurchaseOrderLineID string
	// Qty is the ordered quantity of the drop-ship, QtyConfirmed what the vendor shipped before the
	// cancellation (nothing, for a drop-ship cancelled while OPEN).
	Qty          decimal.Decimal
	QtyConfirmed decimal.Decimal
}

// PlanningMessageIn is the part of planning's message.applied.v1 the consumer uses. SupplyRef is the
// number of the supply the message is about; it names a purchase order when it is one of ours.
type PlanningMessageIn struct {
	EventID       string
	LegalEntityID string
	MessageID     string
	Action        string
	SupplyRef     string
	ItemID        string
	WarehouseID   string
	Qty           decimal.Decimal
	SuggestedDate domain.Day
	DecidedBy     string
	Note          string
}

// ApplyDropShipClosed lowers the PO line a closed drop-ship bought against by the quantity that never
// shipped, through the change management path (FR-17.8): the next version of the PO, confirmed at once or
// waiting for re-approval when a rule fires. A line left with nothing is cancelled, and the PO with it
// when it was the last open line. A change a purchasing rule stops (the received floor PRC-PO-007, a line
// drawn on an agreement, a PO that is not CONFIRMED) is recorded as REFUSED for a buyer to settle by hand
// and is not retried. qty_closed is the drop-ship's running total, so the line goes down by what it adds to
// the closings of the drop-ship already recorded (whatever their outcome), and the drop-ship id with that
// total is the idempotency key: the same closing in a second event changes nothing, a later closing of the
// same drop-ship takes off only its own part. It reports whether anything was recorded (false for a closing
// already handled and for a purchase order this service does not hold).
func (s *Postgres) ApplyDropShipClosed(ctx context.Context, tx pgx.Tx, in DropShipClosedIn) (InboundChange, bool, error) {
	var out InboundChange
	var recorded bool
	err := withEventScope(ctx, tx, in.LegalEntityID, func() error {
		base := InboundChange{LegalEntityID: in.LegalEntityID, Source: domain.SourceDropShipClosed, SourceKey: in.DropShipID + ":" + in.QtyClosed.String(),
			EventID: in.EventID, Action: domain.ActionCloseRemainder, PurchaseOrderID: in.PurchaseOrderID, LineID: in.PurchaseOrderLineID,
			Note: in.DropShipNumber}
		if seen, ok, err := existingInbound(ctx, tx, base.Source, base.SourceKey); err != nil || ok {
			out = seen
			return err
		}
		poID, found, err := dropShipLine(ctx, tx, in.PurchaseOrderID, in.PurchaseOrderLineID)
		if err != nil || !found {
			// The drop-ship names a line this service does not hold (or one on another order): nothing to change.
			return err
		}
		handled, err := closedSoFar(ctx, tx, in.DropShipID)
		if err != nil {
			return err
		}
		qty := decimal.Max(decimal.Zero, in.QtyClosed.Sub(handled))
		base.RequestedQty = &qty
		recorded = true
		out, err = applyInbound(ctx, tx, base, poID, dropShipPlanner(domain.PlanDropShipClose, base.Action, in.PurchaseOrderLineID, qty))
		return err
	})
	return out, recorded, err
}

// dropShipLine finds the order a drop-ship's PO line is on; found is false when this service holds no
// such line, or the line is on another order than the drop-ship names.
func dropShipLine(ctx context.Context, tx pgx.Tx, poID, lineID string) (string, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var found string
	err := tx.QueryRow(ctx, `select purchase_order_id::text from purchase_order_line where tenant_id = $1 and id = $2`,
		p.TenantID, lineID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && found != strings.ToLower(poID)) {
		return "", false, nil
	}
	return found, err == nil, err
}

// closedSoFar is the quantity already taken off by the closings of a drop-ship, whatever their outcome.
func closedSoFar(ctx context.Context, tx pgx.Tx, dropShipID string) (decimal.Decimal, error) {
	p, _ := tenant.FromContext(ctx)
	var handled decimal.Decimal
	err := tx.QueryRow(ctx, `select coalesce(sum(requested_qty), 0) from purchase_inbound_change
		where tenant_id = $1 and source = $2 and split_part(source_key, ':', 1) = $3`,
		p.TenantID, domain.SourceDropShipClosed, dropShipID).Scan(&handled)
	return handled, err
}

// dropShipPlanner plans a quantity cut of the PO line a drop-ship bought against.
func dropShipPlanner(plan func(domain.InboundRequest, domain.InboundLine) domain.InboundPlan, action, lineID string, qty decimal.Decimal) func(PurchaseOrder) domain.InboundPlan {
	return func(cur PurchaseOrder) domain.InboundPlan {
		for _, l := range cur.Lines {
			if !strings.EqualFold(l.ID, lineID) {
				continue
			}
			if l.Status == domain.POLineCancelled {
				return domain.InboundPlan{Outcome: domain.InboundIgnored, Code: domain.CodeInboundNoLine, Reason: "the line is already cancelled"}
			}
			return plan(domain.InboundRequest{Action: action, Qty: qty}, inboundLine(l))
		}
		return domain.InboundPlan{Outcome: domain.InboundIgnored, Code: domain.CodeInboundNoLine, Reason: "the line is not on the purchase order"}
	}
}

// ApplyDropShipCancelled takes a drop-ship cancelled while open (dropship.cancelled.v1) off the PO line it
// bought against, the way a closed remainder does: the line goes down by the quantity the vendor will not
// ship (the ordered quantity less what was confirmed and what earlier closings took), through the change
// management path (FR-17.8), and a line left with nothing is cancelled with the order when it was the last.
// A change a purchasing rule stops is recorded as REFUSED for a buyer to settle. The drop-ship id is the
// idempotency key, so a cancellation delivered twice changes nothing twice. It reports whether anything
// was recorded (false for a cancellation already handled and for a line this service does not hold).
func (s *Postgres) ApplyDropShipCancelled(ctx context.Context, tx pgx.Tx, in DropShipCancelledIn) (InboundChange, bool, error) {
	var out InboundChange
	var recorded bool
	err := withEventScope(ctx, tx, in.LegalEntityID, func() error {
		base := InboundChange{LegalEntityID: in.LegalEntityID, Source: domain.SourceDropShipCancelled, SourceKey: in.DropShipID,
			EventID: in.EventID, Action: domain.ActionCancelDropShip, PurchaseOrderID: in.PurchaseOrderID, LineID: in.PurchaseOrderLineID,
			Note: in.DropShipNumber}
		if seen, ok, err := existingInbound(ctx, tx, base.Source, base.SourceKey); err != nil || ok {
			out = seen
			return err
		}
		poID, found, err := dropShipLine(ctx, tx, in.PurchaseOrderID, in.PurchaseOrderLineID)
		if err != nil || !found {
			return err
		}
		closed, err := closedSoFar(ctx, tx, in.DropShipID)
		if err != nil {
			return err
		}
		qty := decimal.Max(decimal.Zero, in.Qty.Sub(in.QtyConfirmed).Sub(closed))
		base.RequestedQty = &qty
		recorded = true
		out, err = applyInbound(ctx, tx, base, poID, dropShipPlanner(domain.PlanDropShipCancel, base.Action, in.PurchaseOrderLineID, qty))
		return err
	})
	return out, recorded, err
}

// ApplyPlanningMessage applies a planning message a planner applied (message.applied.v1) to the purchase
// order its supply reference names: postpone and advance move the delivery date of the line of the item,
// decrease and increase change its quantity by the message quantity, cancel cancels the line, each through
// the change management path. A supply reference that is no purchase order of ours (a planned order, a
// production or transfer order) and an action that does not apply to a PO record nothing. The message id
// is the idempotency key: the same message in a second event changes nothing.
func (s *Postgres) ApplyPlanningMessage(ctx context.Context, tx pgx.Tx, in PlanningMessageIn) (InboundChange, bool, error) {
	var out InboundChange
	var recorded bool
	err := withEventScope(ctx, tx, in.LegalEntityID, func() error {
		base := InboundChange{LegalEntityID: in.LegalEntityID, Source: domain.SourcePlanningMessage, SourceKey: in.MessageID,
			EventID: in.EventID, Action: in.Action, PONumber: in.SupplyRef, ProductID: in.ItemID, RequestedBy: in.DecidedBy, Note: in.Note, WarehouseID: in.WarehouseID}
		if in.Qty.Sign() != 0 {
			base.RequestedQty = &in.Qty
		}
		if !in.SuggestedDate.IsZero() {
			base.RequestedDate = &in.SuggestedDate
		}
		if seen, ok, err := existingInbound(ctx, tx, base.Source, base.SourceKey); err != nil || ok {
			out = seen
			return err
		}
		p, _ := tenant.FromContext(ctx)
		var poID string
		err := tx.QueryRow(ctx, `select id::text from purchase_order where tenant_id = $1 and number = $2`, p.TenantID, in.SupplyRef).Scan(&poID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // not a purchase order of this legal entity
		}
		if err != nil {
			return err
		}
		recorded = true
		out, err = applyInbound(ctx, tx, base, poID, planningPlanner(in))
		return err
	})
	return out, recorded, err
}

// planningPlanner plans an applied planning message against the open lines of its item.
func planningPlanner(in PlanningMessageIn) func(PurchaseOrder) domain.InboundPlan {
	return func(cur PurchaseOrder) domain.InboundPlan {
		var open []domain.InboundLine
		for _, l := range cur.Lines {
			if l.Status == domain.POLineOpen && l.ProductID == strings.ToLower(in.ItemID) {
				open = append(open, inboundLine(l))
			}
		}
		return domain.PlanPlanningMessage(domain.InboundRequest{Action: in.Action, Qty: in.Qty, Date: in.SuggestedDate,
			WarehouseID: strings.ToLower(in.WarehouseID)}, open)
	}
}

func inboundLine(l PurchaseOrderLine) domain.InboundLine {
	return domain.InboundLine{LineNo: l.LineNo, ProductID: l.ProductID, WarehouseID: l.WarehouseID, Qty: l.Qty,
		ReceivedQty: l.ReceivedQty, DeliveryDate: l.DeliveryDate}
}

// withEventScope runs fn with the legal-entity scope of the event: the consumer's service principal
// carries no legal entity, so without this every legal-entity row is hidden from it. The scope is put
// back afterwards.
func withEventScope(ctx context.Context, tx pgx.Tx, legalEntityID string, fn func() error) error {
	if _, ok := tenant.FromContext(ctx); !ok || !ids.Valid(legalEntityID) {
		return errors.New("tenant context and a legal entity id are required")
	}
	var saved string
	if err := tx.QueryRow(ctx, `select coalesce(current_setting('passage.legal_entity_scopes', true), ''), set_config('passage.legal_entity_scopes', $1, true)`,
		strings.ToLower(legalEntityID)).Scan(&saved, new(string)); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `select set_config('passage.legal_entity_scopes', $1, true)`, saved)
	return err
}

// existingInbound returns the change recorded for an idempotency key.
func existingInbound(ctx context.Context, tx pgx.Tx, source, key string) (InboundChange, bool, error) {
	p, _ := tenant.FromContext(ctx)
	rows, err := tx.Query(ctx, inboundSelect+` where tenant_id = $1 and source = $2 and source_key = $3`, p.TenantID, source, key)
	if err != nil {
		return InboundChange{}, false, err
	}
	items, err := pgx.CollectRows(rows, scanInbound)
	if err != nil || len(items) == 0 {
		return InboundChange{}, false, err
	}
	return items[0], true, nil
}

// applyInbound runs the plan against the purchase order poID under its row lock and records the outcome.
func applyInbound(ctx context.Context, tx pgx.Tx, base InboundChange, poID string, decide func(cur PurchaseOrder) domain.InboundPlan) (InboundChange, error) {
	if err := lockPurchaseOrder(ctx, tx, poID); err != nil {
		return InboundChange{}, err
	}
	cur, err := loadPurchaseOrder(ctx, tx, poID)
	if err != nil {
		return InboundChange{}, err
	}
	base.PurchaseOrderID, base.PONumber = cur.ID, cur.Number
	if cur.ApprovalStatus != domain.POStatusConfirmed {
		// A change in review or still a draft belongs to its buyer; an order that is cancelled or finalized
		// takes no change. The request waits as REFUSED for the buyer to see.
		return recordInbound(ctx, tx, base, refused(domain.CodeInboundOrderState,
			"purchase order is "+cur.ApprovalStatus+"; only a CONFIRMED order takes a change from another service"))
	}
	plan := decide(cur)
	if plan.Outcome != domain.InboundApplied {
		return recordInbound(ctx, tx, withLine(base, cur, plan.LineNo), resultOf(plan))
	}
	base = withLine(base, cur, plan.LineNo)
	var line PurchaseOrderLine
	for _, l := range cur.Lines {
		if l.LineNo == plan.LineNo {
			line = l
		}
	}
	qty0, date0 := line.Qty, line.DeliveryDate
	base.QtyBefore, base.DateBefore = &qty0, &date0
	// The change runs in a savepoint: a rule that refuses it after some rows were written leaves none behind.
	sub, err := tx.Begin(ctx)
	if err != nil {
		return InboundChange{}, err
	}
	res, err := applyPlan(ctx, sub, cur, line, plan)
	if err != nil {
		_ = sub.Rollback(ctx)
		if code, reason, ok := refusal(err); ok {
			base.QtyBefore, base.DateBefore = nil, nil
			return recordInbound(ctx, tx, base, refused(code, reason))
		}
		return InboundChange{}, err
	}
	if err := sub.Commit(ctx); err != nil {
		return InboundChange{}, err
	}
	if res.versionNo == 0 && !plan.CancelLine {
		base.QtyBefore, base.DateBefore = nil, nil
		return recordInbound(ctx, tx, base, resultOf(domain.InboundPlan{Outcome: domain.InboundIgnored, Code: domain.CodeInboundNoChange,
			Reason: "the line already has that quantity and date"}))
	}
	switch {
	case plan.CancelLine:
		zero := decimal.Zero
		base.QtyAfter = &zero
	case plan.SetQty:
		q := plan.NewQty
		base.QtyAfter = &q
	default:
		base.QtyAfter = &qty0
	}
	after := date0
	if plan.SetDate {
		after = plan.NewDate
	}
	base.DateAfter = &after
	base.VersionNo, base.ReapprovalRequired = res.versionNo, res.reapproval
	return recordInbound(ctx, tx, base, resultOf(domain.InboundPlan{Outcome: domain.InboundApplied}))
}

// result is the part of a plan a change row keeps.
type inboundResult struct {
	status, code, reason string
}

func resultOf(p domain.InboundPlan) inboundResult { return inboundResult{p.Outcome, p.Code, p.Reason} }

func refused(code, reason string) inboundResult {
	return inboundResult{domain.InboundRefused, code, reason}
}

func withLine(base InboundChange, cur PurchaseOrder, lineNo int) InboundChange {
	for _, l := range cur.Lines {
		if l.LineNo == lineNo {
			base.LineNo, base.ProductID = l.LineNo, l.ProductID
			if base.LineID == "" {
				base.LineID = l.ID
			}
		}
	}
	return base
}

// refusal says whether err is a purchasing rule (a domain rule or a 4xx problem) that stops the change, as
// opposed to a failure to retry.
func refusal(err error) (code, reason string, ok bool) {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code, de.Detail, true
	}
	var pr *httpx.Problem
	if errors.As(err, &pr) && pr.Status >= 400 && pr.Status < 500 {
		return pr.Code, pr.Detail, true
	}
	return "", "", false
}

// applyPlan moves the line the plan names: it builds the next content of the PO, priced again so line
// amounts, percentage charges and totals stay exact (BR-17.5), and sends it through the PO edit path under
// the row lock. Cancelling the last open line cancels the order.
func applyPlan(ctx context.Context, tx pgx.Tx, cur PurchaseOrder, line PurchaseOrderLine, plan domain.InboundPlan) (editResult, error) {
	next := cur
	next.Lines = make([]PurchaseOrderLine, 0, len(cur.Lines))
	for _, l := range cur.Lines {
		if l.Status == domain.POLineCancelled {
			continue
		}
		if l.LineNo == plan.LineNo {
			switch {
			case plan.CancelLine:
				continue
			case plan.SetQty:
				l.TaxAmount = scaledTax(l, plan.NewQty, money.MinorUnits(cur.Currency))
				l.Qty = plan.NewQty
			case plan.SetDate:
				l.DeliveryDate = plan.NewDate
			}
		}
		next.Lines = append(next.Lines, l)
	}
	if len(next.Lines) == 0 {
		// That was the last open line: the order is cancelled, which also gives back what the line held.
		if err := cancelOrder(ctx, tx, cur); err != nil {
			return editResult{}, err
		}
		head := cur
		head.ApprovalStatus = domain.POStatusCancelled
		return editResult{}, updateHeader(ctx, tx, head)
	}
	if err := priceLines(&next, cur.Charges); err != nil {
		return editResult{}, err
	}
	_, res, err := editLocked(ctx, tx, cur, cur.RowVersion, next, true)
	return res, err
}

// priceLines prices next again over its lines and the live charges of charges, so line amounts,
// percentage charges and totals stay exact (BR-17.5).
func priceLines(next *PurchaseOrder, charges []PurchaseOrderCharge) error {
	inputs := make([]domain.LineInput, 0, len(next.Lines))
	for _, l := range next.Lines {
		inputs = append(inputs, domain.LineInput{Qty: l.Qty, UnitPrice: l.UnitPrice, PriceUnit: l.PriceUnit, DiscountPct: l.DiscountPct, Tax: l.TaxAmount})
	}
	chargeInputs, kept := LiveCharges(charges, next.Lines)
	doc, err := domain.Price(inputs, chargeInputs, money.MinorUnits(next.Currency))
	if err != nil {
		return err
	}
	for i := range next.Lines {
		la := doc.Lines[i]
		next.Lines[i].GrossAmount, next.Lines[i].DiscountAmount, next.Lines[i].LineAmount, next.Lines[i].TaxAmount = la.Gross, la.Discount, la.Amount, la.Tax
	}
	next.Charges = kept
	for i := range next.Charges {
		next.Charges[i].Amount = doc.Charges[i]
	}
	next.Subtotal, next.DiscountTotal, next.ChargesTotal, next.TaxTotal, next.Total =
		doc.Totals.Subtotal, doc.Totals.DiscountTotal, doc.Totals.ChargesTotal, doc.Totals.TaxTotal, doc.Totals.Total
	return nil
}

// scaledTax keeps the tax of a line in proportion when its quantity changes.
func scaledTax(l PurchaseOrderLine, newQty decimal.Decimal, minor int32) decimal.Decimal {
	if l.TaxAmount.IsZero() || l.Qty.IsZero() {
		return l.TaxAmount
	}
	return l.TaxAmount.Mul(newQty).DivRound(l.Qty, 12).RoundBank(minor)
}

const inboundColumns = `id, legal_entity_id, source, source_key, event_id, action, coalesce(purchase_order_id::text, ''), po_number,
	coalesce(line_no, 0), coalesce(line_id::text, ''), coalesce(product_id::text, ''), status, reason_code, reason,
	requested_qty, requested_date, qty_before, qty_after, date_before, date_after, coalesce(version_no, 0), reapproval_required,
	requested_by, note, created_at, coalesce(warehouse_id::text, ''), attempts, resolution, resolved_by, resolved_at, resolution_note`

const inboundSelect = `select ` + inboundColumns + ` from purchase_inbound_change`

func scanInbound(row pgx.CollectableRow) (InboundChange, error) {
	var c InboundChange
	var reqDate, dateBefore, dateAfter *time.Time
	err := row.Scan(&c.ID, &c.LegalEntityID, &c.Source, &c.SourceKey, &c.EventID, &c.Action, &c.PurchaseOrderID, &c.PONumber,
		&c.LineNo, &c.LineID, &c.ProductID, &c.Status, &c.ReasonCode, &c.Reason, &c.RequestedQty, &reqDate, &c.QtyBefore, &c.QtyAfter,
		&dateBefore, &dateAfter, &c.VersionNo, &c.ReapprovalRequired, &c.RequestedBy, &c.Note, &c.CreatedAt,
		&c.WarehouseID, &c.Attempts, &c.Resolution, &c.ResolvedBy, &c.ResolvedAt, &c.ResolutionNote)
	c.RequestedDate, c.DateBefore, c.DateAfter = dayOrNil(reqDate), dayOrNil(dateBefore), dayOrNil(dateAfter)
	return c, err
}

// recordInbound writes the change row and its events; the row's id and time are set here. A base that
// already has an id is a retry of that change (slice 19): its outcome is rewritten in place, so the
// request keeps one row and one idempotency key however many times it runs.
func recordInbound(ctx context.Context, tx pgx.Tx, c InboundChange, r inboundResult) (InboundChange, error) {
	p, _ := tenant.FromContext(ctx)
	if c.ID != "" {
		return recordRetry(ctx, tx, c, r)
	}
	c.ID, c.Status, c.ReasonCode, c.Reason, c.Attempts = ids.New(), r.status, r.code, r.reason, 1
	err := tx.QueryRow(ctx, `insert into purchase_inbound_change (tenant_id, id, legal_entity_id, source, source_key, event_id, action,
		purchase_order_id, po_number, line_no, line_id, product_id, status, reason_code, reason, requested_qty, requested_date,
		qty_before, qty_after, date_before, date_after, version_no, reapproval_required, requested_by, note, warehouse_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)
		returning created_at`,
		p.TenantID, c.ID, c.LegalEntityID, c.Source, c.SourceKey, c.EventID, c.Action, nullable(c.PurchaseOrderID), c.PONumber,
		nullableInt(c.LineNo), nullable(c.LineID), nullable(c.ProductID), c.Status, c.ReasonCode, c.Reason, c.RequestedQty, dayPtr(c.RequestedDate),
		c.QtyBefore, c.QtyAfter, dayPtr(c.DateBefore), dayPtr(c.DateAfter), nullableInt(c.VersionNo), c.ReapprovalRequired, c.RequestedBy, c.Note,
		nullable(c.WarehouseID)).
		Scan(&c.CreatedAt)
	if err != nil {
		return InboundChange{}, fmt.Errorf("record inbound change: %w", err)
	}
	d := inboundData(c)
	if d.BuyerID, err = buyerOf(ctx, tx, c.PurchaseOrderID); err != nil {
		return InboundChange{}, err
	}
	if err := enqueue(ctx, tx, "inboundchange", c.ID, purchasingevents.InboundChangeRecorded, d); err != nil {
		return InboundChange{}, err
	}
	if c.Status == domain.InboundRefused {
		// The buyer's notification: addressed to the user who made the order, for the notification service
		// (wave 4) to route; the change waits in the refused queue until a buyer settles it.
		if err := enqueue(ctx, tx, "inboundchange", c.ID, purchasingevents.InboundChangeRefused, d); err != nil {
			return InboundChange{}, err
		}
	}
	return c, nil
}

// recordRetry rewrites the outcome of change c (its id is set) after a retry. A retry that leaves
// REFUSED settles the change as RETRIED and writes inboundchange.resolved.v1; one that is refused again
// only counts the attempt, and the change stays in the buyer's queue with the new reason.
func recordRetry(ctx context.Context, tx pgx.Tx, c InboundChange, r inboundResult) (InboundChange, error) {
	p, _ := tenant.FromContext(ctx)
	settled := r.status != domain.InboundRefused
	resolution, by := "", ""
	if settled {
		resolution, by = domain.ResolutionRetried, p.UserID
	}
	note := ""
	if settled {
		note = c.ResolutionNote
	}
	_, err := tx.Exec(ctx, `update purchase_inbound_change set status = $3, reason_code = $4, reason = $5, purchase_order_id = $6, po_number = $7,
		line_no = $8, line_id = coalesce($9::uuid, line_id), product_id = coalesce($10::uuid, product_id), qty_before = $11, qty_after = $12,
		date_before = $13, date_after = $14, version_no = $15, reapproval_required = $16, attempts = attempts + 1, resolution = $17,
		resolved_by = $18, resolved_at = case when $17 = '' then null else now() end, resolution_note = $19, updated_at = now()
		where tenant_id = $1 and id = $2`,
		p.TenantID, c.ID, r.status, r.code, r.reason, nullable(c.PurchaseOrderID), c.PONumber, nullableInt(c.LineNo), nullable(c.LineID),
		nullable(c.ProductID), c.QtyBefore, c.QtyAfter, dayPtr(c.DateBefore), dayPtr(c.DateAfter), nullableInt(c.VersionNo), c.ReapprovalRequired,
		resolution, by, note)
	if err != nil {
		return InboundChange{}, fmt.Errorf("record inbound retry: %w", err)
	}
	out, err := loadInbound(ctx, tx, c.ID, false)
	if err != nil {
		return InboundChange{}, err
	}
	if settled {
		return out, enqueue(ctx, tx, "inboundchange", out.ID, purchasingevents.InboundChangeResolved, resolvedData(out, p.UserID))
	}
	return out, nil
}

// buyerOf is the user who made the order, the one a refused change is for; empty when unknown. A failed
// read is returned, not swallowed: it aborts the transaction, so nothing after it could be written anyway.
func buyerOf(ctx context.Context, tx pgx.Tx, poID string) (string, error) {
	if poID == "" {
		return "", nil
	}
	p, _ := tenant.FromContext(ctx)
	var buyer string
	err := tx.QueryRow(ctx, `select coalesce(created_by::text, '') from purchase_order where tenant_id = $1 and id = $2`, p.TenantID, poID).Scan(&buyer)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return buyer, err
}

func resolvedData(c InboundChange, by string) purchasingevents.InboundChangeResolvedData {
	d := purchasingevents.InboundChangeResolvedData{ChangeID: c.ID, LegalEntityID: c.LegalEntityID, Source: c.Source, SourceKey: c.SourceKey,
		Action: c.Action, Resolution: c.Resolution, Status: c.Status, POID: c.PurchaseOrderID, Number: c.PONumber, LineNo: c.LineNo,
		ReasonCode: c.ReasonCode, Attempts: c.Attempts, ResolvedBy: by, Note: c.ResolutionNote, VersionNo: c.VersionNo,
		ReapprovalRequired: c.ReapprovalRequired}
	if c.ResolvedAt != nil {
		d.ResolvedAt = c.ResolvedAt.UTC().Format(time.RFC3339)
	}
	return d
}

// loadInbound reads one change, locking it for update when asked; 404 when it is not visible.
func loadInbound(ctx context.Context, tx pgx.Tx, id string, lock bool) (InboundChange, error) {
	p, _ := tenant.FromContext(ctx)
	q := inboundSelect + ` where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	rows, err := tx.Query(ctx, q, p.TenantID, id)
	if err != nil {
		return InboundChange{}, err
	}
	items, err := pgx.CollectRows(rows, scanInbound)
	if err != nil {
		return InboundChange{}, err
	}
	if len(items) == 0 {
		return InboundChange{}, httpx.NewProblem(http.StatusNotFound, CodeInboundChangeNotFound, "inbound change not found")
	}
	return items[0], nil
}

// openRefused loads a change under lock and checks it is a REFUSED one nobody settled.
func openRefused(ctx context.Context, tx pgx.Tx, id string, idem Idem) (cur InboundChange, replay bool, err error) {
	cur, err = loadInbound(ctx, tx, id, true)
	if err != nil {
		return cur, false, err
	}
	if _, replay, err = claimKey(ctx, tx, idem, id); err != nil || replay {
		return cur, replay, err
	}
	if cur.Status != domain.InboundRefused || cur.Resolution != "" {
		return cur, false, httpx.NewProblem(http.StatusConflict, domain.CodeInboundNotOpen,
			"only a refused change nobody has settled can be acknowledged or retried; this one is "+cur.Status+settledSuffix(cur))
	}
	return cur, false, nil
}

func settledSuffix(c InboundChange) string {
	if c.Resolution == "" {
		return ""
	}
	return " and " + strings.ToLower(c.Resolution)
}

// AcknowledgeInboundChange settles a REFUSED change as ACKNOWLEDGED: the buyer dealt with the request by
// hand (or decided to leave the order as it is), so it leaves the queue. The change is not applied. 409
// PRC-INB-008 when it is not a refused change waiting for a buyer. A replayed Idempotency-Key returns the
// change as it is now. Writes inboundchange.resolved.v1.
func (s *Postgres) AcknowledgeInboundChange(ctx context.Context, id, note string, idem Idem) (InboundChange, error) {
	p, _ := tenant.FromContext(ctx)
	var out InboundChange
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, replay, err := openRefused(ctx, tx, id, idem)
		if err != nil || replay {
			out = cur
			return err
		}
		if _, err := tx.Exec(ctx, `update purchase_inbound_change set resolution = $3, resolved_by = $4, resolved_at = now(), resolution_note = $5,
			updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, domain.ResolutionAcknowledged, p.UserID, note); err != nil {
			return err
		}
		out, err = loadInbound(ctx, tx, id, false)
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "inboundchange", out.ID, purchasingevents.InboundChangeResolved, resolvedData(out, p.UserID))
	})
	return out, err
}

// RetryInboundChange runs a REFUSED change's request again against the purchase order as it is now (the
// buyer has fixed what stopped it: confirmed the order, freed the agreement draw, raised nothing past the
// received floor), through the same planning and change management path as the consumer. The change keeps
// its row: it becomes APPLIED or IGNORED and RETRIED, or stays REFUSED with the new reason and one more
// attempt. 409 PRC-INB-008 when it is not a refused change waiting for a buyer. A replayed Idempotency-Key
// returns the change as it is now.
func (s *Postgres) RetryInboundChange(ctx context.Context, id, note string, idem Idem) (InboundChange, error) {
	var out InboundChange
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, replay, err := openRefused(ctx, tx, id, idem)
		if err != nil || replay {
			out = cur
			return err
		}
		var decide func(PurchaseOrder) domain.InboundPlan
		switch {
		case cur.PurchaseOrderID == "":
			return httpx.NewProblem(http.StatusConflict, domain.CodeInboundNotOpen, "the change names no purchase order to retry against")
		case cur.Source == domain.SourcePlanningMessage:
			in := PlanningMessageIn{Action: cur.Action, ItemID: cur.ProductID, WarehouseID: cur.WarehouseID}
			if cur.RequestedQty != nil {
				in.Qty = *cur.RequestedQty
			}
			if cur.RequestedDate != nil {
				in.SuggestedDate = *cur.RequestedDate
			}
			decide = planningPlanner(in)
		case cur.Source == domain.SourceDropShipClosed && cur.RequestedQty != nil:
			decide = dropShipPlanner(domain.PlanDropShipClose, cur.Action, cur.LineID, *cur.RequestedQty)
		case cur.Source == domain.SourceDropShipCancelled && cur.RequestedQty != nil:
			decide = dropShipPlanner(domain.PlanDropShipCancel, cur.Action, cur.LineID, *cur.RequestedQty)
		default:
			return httpx.NewProblem(http.StatusConflict, domain.CodeInboundNotOpen, "the change does not hold the request to run again")
		}
		// The run starts from the request alone: what the last attempt measured or moved is not carried over.
		base := cur
		base.QtyBefore, base.QtyAfter, base.DateBefore, base.DateAfter = nil, nil, nil, nil
		base.VersionNo, base.ReapprovalRequired, base.ResolutionNote = 0, false, note
		out, err = applyInbound(ctx, tx, base, cur.PurchaseOrderID, decide)
		return err
	})
	return out, err
}

func inboundData(c InboundChange) purchasingevents.InboundChangeData {
	d := purchasingevents.InboundChangeData{ChangeID: c.ID, LegalEntityID: c.LegalEntityID, Source: c.Source, SourceKey: c.SourceKey,
		Action: c.Action, Status: c.Status, POID: c.PurchaseOrderID, Number: c.PONumber, LineNo: c.LineNo, ReasonCode: c.ReasonCode,
		Reason: c.Reason, VersionNo: c.VersionNo, ReapprovalRequired: c.ReapprovalRequired, Attempts: c.Attempts}
	if c.QtyBefore != nil {
		d.QtyBefore = c.QtyBefore.String()
	}
	if c.QtyAfter != nil {
		d.QtyAfter = c.QtyAfter.String()
	}
	if c.DateBefore != nil {
		d.DateBefore = c.DateBefore.String()
	}
	if c.DateAfter != nil {
		d.DateAfter = c.DateAfter.String()
	}
	return d
}

// GetInboundChange returns one change; 404 when it is not visible to the caller.
func (s *Postgres) GetInboundChange(ctx context.Context, id string) (InboundChange, error) {
	var out InboundChange
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadInbound(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ListInboundChanges returns changes newest first, after the cursor id.
func (s *Postgres) ListInboundChanges(ctx context.Context, f InboundFilter, after string, limit int) (Page[InboundChange], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []InboundChange
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, inboundSelect+` where tenant_id = $1 and ($2 = '' or source = $2) and ($3 = '' or status = $3)
			and ($4::uuid is null or purchase_order_id = $4::uuid) and ($5 = '' or legal_entity_id::text = $5)
			and ($6::uuid is null or id < $6::uuid)
			and ($8 = '' or ($8 = 'NONE' and resolution = '') or resolution = $8) order by id desc limit $7`,
			p.TenantID, f.Source, f.Status, nullable(f.PurchaseOrderID), f.LegalEntityID, nullable(after), limit+1, f.Resolution)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, scanInbound)
		return err
	})
	if err != nil {
		return Page[InboundChange]{}, err
	}
	if items == nil {
		items = []InboundChange{}
	}
	return pageOf(items, limit, func(c InboundChange) string { return c.ID }), nil
}
