package domain

import (
	"github.com/shopspring/decimal"
)

// Inbound change sources (slice 18). Both arrive as events from another service and move a line of a
// confirmed purchase order through the change management path.
const (
	// SourcePlanningMessage is an action message a planner applied in master planning (wave 22,
	// message.applied.v1); the key is the message id.
	SourcePlanningMessage = "PLANNING_MESSAGE"
	// SourceDropShipClosed is the closing of a drop-ship remainder in fulfillment (wave 28,
	// dropship.closed.v1); the key is the drop-ship id.
	SourceDropShipClosed = "DROPSHIP_CLOSED"
	// SourceDropShipCancelled is a drop-ship cancelled while open in fulfillment (dropship.cancelled.v1,
	// slice 19); the key is the drop-ship id.
	SourceDropShipCancelled = "DROPSHIP_CANCELLED"
)

// How a refused change was settled (slice 19). A change waits with an empty resolution.
const (
	// ResolutionAcknowledged: a buyer dealt with the refused request by hand.
	ResolutionAcknowledged = "ACKNOWLEDGED"
	// ResolutionRetried: a retry ran the request again and it left REFUSED.
	ResolutionRetried = "RETRIED"
)

// Inbound change outcomes.
const (
	InboundApplied = "APPLIED"
	InboundRefused = "REFUSED"
	InboundIgnored = "IGNORED"
)

// Planning actions a purchase order can take (the other actions of message.applied.v1 are for
// planned orders, production and transfers, or cannot be applied at all).
const (
	ActionPostpone = "postpone"
	ActionAdvance  = "advance"
	ActionCancel   = "cancel"
	ActionDecrease = "decrease"
	ActionIncrease = "increase"
	// ActionCloseRemainder is the action recorded for a closed drop-ship.
	ActionCloseRemainder = "close-remainder"
	// ActionCancelDropShip is the action recorded for a cancelled drop-ship.
	ActionCancelDropShip = "cancel-dropship"
)

// Inbound problem codes. They are recorded on the change row and never returned over HTTP.
const (
	CodeInboundNotApplicable = "PRC-INB-001"
	CodeInboundNoLine        = "PRC-INB-002"
	CodeInboundAmbiguous     = "PRC-INB-003"
	CodeInboundQuantity      = "PRC-INB-004"
	CodeInboundDate          = "PRC-INB-005"
	CodeInboundOrderState    = "PRC-INB-006"
	CodeInboundNoChange      = "PRC-INB-007"
	// CodeInboundNotOpen is the 409 of acknowledging or retrying a change that is not a REFUSED one
	// waiting for a buyer (slice 19).
	CodeInboundNotOpen = "PRC-INB-008"
)

// InboundLine is the view of an open PO line the inbound rules need.
type InboundLine struct {
	LineNo       int
	ProductID    string
	WarehouseID  string // effective: the line's own, else the header's
	Qty          decimal.Decimal
	ReceivedQty  decimal.Decimal
	DeliveryDate Day
}

// InboundRequest is what the other service asked for.
type InboundRequest struct {
	Action string
	// Qty is the quantity the message carries: for decrease and increase the quantity to take off or
	// add (planning's surplus or need), for a closed drop-ship the quantity that never shipped.
	Qty decimal.Decimal
	// Date is the suggested date of a postpone or advance.
	Date Day
	// WarehouseID narrows the lines to the planning scope of the message; empty for the site scope.
	WarehouseID string
}

// InboundPlan is the decision on one request. Outcome IGNORED and REFUSED carry Code and Reason and
// no change. Otherwise exactly one of the three changes is set: NewQty (SetQty), NewDate (SetDate) or
// CancelLine.
type InboundPlan struct {
	Outcome    string
	Code       string
	Reason     string
	LineNo     int
	SetQty     bool
	NewQty     decimal.Decimal
	SetDate    bool
	NewDate    Day
	CancelLine bool
}

func ignore(code, reason string) InboundPlan {
	return InboundPlan{Outcome: InboundIgnored, Code: code, Reason: reason}
}

func refuse(code, reason string) InboundPlan {
	return InboundPlan{Outcome: InboundRefused, Code: code, Reason: reason}
}

// PlanPlanningMessage decides what an applied planning message does to a purchase order whose open
// lines of the message's item are given. postpone and advance move the delivery date to the suggested
// date, decrease and increase take or add the message quantity, cancel cancels the line. A line is
// chosen by item (and the message's warehouse when the line has one); several candidates are narrowed
// for a date move by the open quantity, which planning sends as the quantity of the supply, and
// otherwise refused as ambiguous, because the message names a supply and not a line.
func PlanPlanningMessage(req InboundRequest, lines []InboundLine) InboundPlan {
	switch req.Action {
	case ActionPostpone, ActionAdvance, ActionCancel, ActionDecrease, ActionIncrease:
	default:
		return ignore(CodeInboundNotApplicable, "action "+req.Action+" does not change a purchase order")
	}
	cands := make([]InboundLine, 0, len(lines))
	for _, l := range lines {
		if req.WarehouseID != "" && l.WarehouseID != "" && l.WarehouseID != req.WarehouseID {
			continue
		}
		cands = append(cands, l)
	}
	if len(cands) == 0 {
		return ignore(CodeInboundNoLine, "the purchase order has no open line of the item in that warehouse")
	}
	if len(cands) > 1 && (req.Action == ActionPostpone || req.Action == ActionAdvance) {
		var same []InboundLine
		for _, l := range cands {
			if l.Qty.Sub(l.ReceivedQty).Equal(req.Qty) {
				same = append(same, l)
			}
		}
		if len(same) > 0 {
			cands = same
		}
	}
	if len(cands) > 1 {
		return refuse(CodeInboundAmbiguous, "the purchase order has more than one open line of the item; change the line by hand")
	}
	l := cands[0]
	switch req.Action {
	case ActionPostpone, ActionAdvance:
		if req.Date.IsZero() {
			return refuse(CodeInboundDate, "the message has no suggested date")
		}
		if req.Date.Equal(l.DeliveryDate.Time) {
			return ignore(CodeInboundDate, "the line is already due on "+req.Date.String())
		}
		if req.Action == ActionPostpone && req.Date.Before(l.DeliveryDate.Time) {
			return refuse(CodeInboundDate, "postpone to "+req.Date.String()+" is before the due date "+l.DeliveryDate.String())
		}
		if req.Action == ActionAdvance && req.Date.After(l.DeliveryDate.Time) {
			return refuse(CodeInboundDate, "advance to "+req.Date.String()+" is after the due date "+l.DeliveryDate.String())
		}
		return InboundPlan{Outcome: InboundApplied, LineNo: l.LineNo, SetDate: true, NewDate: req.Date}
	case ActionCancel:
		return InboundPlan{Outcome: InboundApplied, LineNo: l.LineNo, CancelLine: true}
	case ActionDecrease:
		if !req.Qty.IsPositive() {
			return refuse(CodeInboundQuantity, "the message carries no quantity to take off")
		}
		return lowerLine(l, l.Qty.Sub(req.Qty))
	}
	if !req.Qty.IsPositive() {
		return refuse(CodeInboundQuantity, "the message carries no quantity to add")
	}
	return InboundPlan{Outcome: InboundApplied, LineNo: l.LineNo, SetQty: true, NewQty: l.Qty.Add(req.Qty)}
}

// PlanDropShipClose decides what a closed drop-ship remainder does to the PO line the drop-ship
// bought against: its quantity goes down by the quantity that never shipped, and a line left with
// nothing ordered and nothing received is cancelled.
func PlanDropShipClose(req InboundRequest, l InboundLine) InboundPlan {
	if !req.Qty.IsPositive() {
		return ignore(CodeInboundQuantity, "nothing was closed")
	}
	return lowerLine(l, l.Qty.Sub(req.Qty))
}

// PlanDropShipCancel decides what a cancelled drop-ship does to the PO line it bought against: the
// quantity the vendor will never ship (the ordered quantity less what was confirmed or closed before)
// comes off the line, and a line left with nothing ordered and nothing received is cancelled.
func PlanDropShipCancel(req InboundRequest, l InboundLine) InboundPlan {
	if !req.Qty.IsPositive() {
		return ignore(CodeInboundQuantity, "nothing was left to cancel")
	}
	return lowerLine(l, l.Qty.Sub(req.Qty))
}

// lowerLine takes a line to newQty: a line left with nothing ordered is cancelled when nothing was
// received, and below zero is refused. A quantity below what was received is for the store's
// received/invoiced floor (PRC-PO-007) to refuse, as it does on a hand edit.
func lowerLine(l InboundLine, newQty decimal.Decimal) InboundPlan {
	switch {
	case newQty.IsNegative():
		return refuse(CodeInboundQuantity, "the quantity to take off ("+l.Qty.Sub(newQty).String()+") is more than the line quantity "+l.Qty.String())
	case newQty.IsZero() && l.ReceivedQty.IsZero():
		return InboundPlan{Outcome: InboundApplied, LineNo: l.LineNo, CancelLine: true}
	case newQty.IsZero():
		return refuse(CodeInboundQuantity, "the line has receipts; its quantity cannot go to zero")
	}
	return InboundPlan{Outcome: InboundApplied, LineNo: l.LineNo, SetQty: true, NewQty: newQty}
}
