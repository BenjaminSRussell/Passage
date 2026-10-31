package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// WOStatus is the work-order status machine (BR-45.5).
type WOStatus string

const (
	WOUnscheduled WOStatus = "UNSCHEDULED"
	WOScheduled   WOStatus = "SCHEDULED"
	WODispatched  WOStatus = "DISPATCHED"
	WOTraveling   WOStatus = "TRAVELING"
	WOInProgress  WOStatus = "IN_PROGRESS"
	WOOnHold      WOStatus = "ON_HOLD"
	WOCompleted   WOStatus = "COMPLETED"
	WOPosted      WOStatus = "POSTED"
	WOCanceled    WOStatus = "CANCELED"
)

var transitions = map[WOStatus][]WOStatus{
	WOUnscheduled: {WOScheduled, WOCanceled},
	WOScheduled:   {WODispatched, WOCanceled},
	WODispatched:  {WOTraveling, WOCanceled},
	WOTraveling:   {WOInProgress, WOCanceled},
	WOInProgress:  {WOOnHold, WOCompleted, WOCanceled},
	WOOnHold:      {WOInProgress, WOCanceled},
	WOCompleted:   {WOPosted},
}

// CanTransition reports whether a work order may move from one status to another.
func CanTransition(from, to WOStatus) bool {
	for _, next := range transitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// Coverage says who pays for a line (BR-45.4).
type Coverage string

const (
	CoverBillable  Coverage = "BILLABLE"
	CoverWarranty  Coverage = "WARRANTY"
	CoverAgreement Coverage = "AGREEMENT"
)

// LineKind is the kind of a work-order line.
type LineKind string

const (
	KindLabor  LineKind = "LABOR"
	KindPart   LineKind = "PART"
	KindTravel LineKind = "TRAVEL"
	KindFee    LineKind = "FEE"
)

// WorkLine is one line of a service work order as entered.
type WorkLine struct {
	ID        string          `json:"id"`
	Kind      LineKind        `json:"kind"`
	Qty       decimal.Decimal `json:"qty"`
	UnitPrice decimal.Decimal `json:"unitPrice"`
	UnitCost  decimal.Decimal `json:"unitCost"`
}

// WarrantyCover is the warranty term of the service object at completion.
type WarrantyCover struct {
	Active bool `json:"active"`
	Parts  bool `json:"parts"`
	Labor  bool `json:"labor"`
	Travel bool `json:"travel"`
}

// AgreementCover is the agreement that governs the work order, with the
// labor hours still available (included hours less earlier usage).
type AgreementCover struct {
	Parts          bool            `json:"parts"`
	Labor          bool            `json:"labor"`
	Travel         bool            `json:"travel"`
	HoursRemaining decimal.Decimal `json:"hoursRemaining"`
}

// WarrantyActive reports whether a warranty that starts on start and runs for
// months covers the date on, unless the item is excluded (FR-45.2).
func WarrantyActive(start time.Time, months int, on time.Time, excluded bool) bool {
	if excluded || months <= 0 {
		return false
	}
	end := addMonthsClamped(start, months)
	return !on.Before(start) && on.Before(end)
}

// addMonthsClamped adds whole months keeping the day of month, clamped to the
// last day of the target month (31 Aug + 6 months = 28 Feb), as Postgres
// date + interval does. time.AddDate would overflow into the next month.
func addMonthsClamped(day time.Time, months int) time.Time {
	y, m, d := day.Date()
	first := time.Date(y, m+time.Month(months), 1, 0, 0, 0, 0, day.Location())
	if last := first.AddDate(0, 1, -1).Day(); d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, day.Hour(), day.Minute(), day.Second(), day.Nanosecond(), day.Location())
}

// PostInput is everything the billing post needs.
type PostInput struct {
	Status    WOStatus
	Currency  string
	Lines     []WorkLine
	Warranty  WarrantyCover
	Agreement *AgreementCover
}

// PostedLine is one billed or covered piece of a work-order line. A labor line
// split between included hours and billable hours yields two posted lines.
type PostedLine struct {
	LineID         string          `json:"lineId"`
	Kind           LineKind        `json:"kind"`
	Coverage       Coverage        `json:"coverage"`
	Qty            decimal.Decimal `json:"qty"`
	Amount         decimal.Decimal `json:"amount"`
	WarrantyCost   decimal.Decimal `json:"warrantyCost"`
	AgreementHours decimal.Decimal `json:"agreementHours"`
}

// Posting is the result of posting a completed work order.
type Posting struct {
	Currency            string          `json:"currency"`
	Lines               []PostedLine    `json:"lines"`
	BillableTotal       decimal.Decimal `json:"billableTotal"`
	WarrantyCost        decimal.Decimal `json:"warrantyCost"`
	EntitlementConsumed decimal.Decimal `json:"entitlementConsumed"`
}

// Error is a domain rule violation with a stable code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

// Codes returned by the work-order rules.
const (
	CodeNotCompleted      = "SVC-WO-010"
	CodeInvalidTransition = "SVC-WO-011"
)

// coverFor applies BR-45.4: warranty first, then agreement, then billable.
func coverFor(kind LineKind, w WarrantyCover, a *AgreementCover) Coverage {
	var warrantyCovers, agreementCovers bool
	switch kind {
	case KindPart:
		warrantyCovers, agreementCovers = w.Parts, a != nil && a.Parts
	case KindLabor:
		warrantyCovers, agreementCovers = w.Labor, a != nil && a.Labor
	case KindTravel:
		warrantyCovers, agreementCovers = w.Travel, a != nil && a.Travel
	}
	switch {
	case w.Active && warrantyCovers:
		return CoverWarranty
	case agreementCovers:
		return CoverAgreement
	default:
		return CoverBillable
	}
}

// Post computes per-line coverage and amounts. Billable amounts are rounded
// half-even to the currency minor unit once per line (BR-45.8). Included
// agreement hours are consumed first-in first-out across labor lines.
func Post(in PostInput) (Posting, error) {
	if in.Status != WOCompleted {
		return Posting{}, &Error{Code: CodeNotCompleted, Detail: "only a COMPLETED work order can be posted"}
	}
	scale := money.MinorUnits(in.Currency)
	zero := decimal.Zero
	out := Posting{Currency: in.Currency, BillableTotal: zero, WarrantyCost: zero, EntitlementConsumed: zero, Lines: []PostedLine{}}
	remaining := zero
	if in.Agreement != nil {
		remaining = in.Agreement.HoursRemaining
	}
	for _, l := range in.Lines {
		switch coverFor(l.Kind, in.Warranty, in.Agreement) {
		case CoverWarranty:
			out.Lines = append(out.Lines, PostedLine{LineID: l.ID, Kind: l.Kind, Coverage: CoverWarranty, Qty: l.Qty,
				Amount: zero, WarrantyCost: l.Qty.Mul(l.UnitCost).RoundBank(scale), AgreementHours: zero})
			out.WarrantyCost = out.WarrantyCost.Add(l.Qty.Mul(l.UnitCost).RoundBank(scale))
		case CoverAgreement:
			if l.Kind != KindLabor {
				out.Lines = append(out.Lines, PostedLine{LineID: l.ID, Kind: l.Kind, Coverage: CoverAgreement, Qty: l.Qty,
					Amount: zero, WarrantyCost: zero, AgreementHours: zero})
				continue
			}
			use := decimal.Min(l.Qty, decimal.Max(remaining, zero))
			remaining = remaining.Sub(use)
			out.EntitlementConsumed = out.EntitlementConsumed.Add(use)
			if use.IsPositive() {
				out.Lines = append(out.Lines, PostedLine{LineID: l.ID, Kind: l.Kind, Coverage: CoverAgreement, Qty: use,
					Amount: zero, WarrantyCost: zero, AgreementHours: use})
			}
			if rest := l.Qty.Sub(use); rest.IsPositive() {
				amount := rest.Mul(l.UnitPrice).RoundBank(scale)
				out.Lines = append(out.Lines, PostedLine{LineID: l.ID, Kind: l.Kind, Coverage: CoverBillable, Qty: rest,
					Amount: amount, WarrantyCost: zero, AgreementHours: zero})
				out.BillableTotal = out.BillableTotal.Add(amount)
			}
		default:
			amount := l.Qty.Mul(l.UnitPrice).RoundBank(scale)
			out.Lines = append(out.Lines, PostedLine{LineID: l.ID, Kind: l.Kind, Coverage: CoverBillable, Qty: l.Qty,
				Amount: amount, WarrantyCost: zero, AgreementHours: zero})
			out.BillableTotal = out.BillableTotal.Add(amount)
		}
	}
	return out, nil
}
