package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Supplier warranty claim statuses (FR-45.15).
const (
	ClaimDraft     = "DRAFT"
	ClaimSubmitted = "SUBMITTED"
	ClaimAccepted  = "ACCEPTED"
	ClaimPartial   = "PARTIALLY_ACCEPTED"
	ClaimRejected  = "REJECTED"
	ClaimSettled   = "SETTLED"
	ClaimClosed    = "CLOSED"
)

// Claim sources (PRD 8.45.4 SupplierWarrantyClaim.source).
const (
	ClaimFromServiceWO = "SERVICE_WO"
	ClaimFromEAMWO     = "EAM_WO"
)

// DefaultClaimWindowDays is the vendor claim window when none is given (8.45.13).
const DefaultClaimWindowDays = 30

// Codes of the supplier claim rules (BR-45.12). SVC-SWC-001..003 are the PRD's
// message catalog (8.45.18); the rest are this service's. Each is listed in the
// service README.
const (
	CodeClaimWindow     = "SVC-SWC-001"
	CodeClaimReturn     = "SVC-SWC-002"
	CodeClaimOverAccept = "SVC-SWC-003"
	CodeClaimState      = "SVC-SWC-004"
	CodeClaimCredit     = "SVC-SWC-005"
	CodeClaimDecision   = "SVC-SWC-006"
	CodeClaimReference  = "SVC-SWC-010"
)

// ClaimLine is one failed item on a claim: the part cost at issue plus labor hours
// at the vendor's agreed rate. AcceptedAmount is set by the vendor decision.
type ClaimLine struct {
	SourceID       string          `json:"sourceId"`
	ItemID         string          `json:"itemId,omitempty"`
	Serial         string          `json:"serial,omitempty"`
	FailureCode    string          `json:"failureCode,omitempty"`
	PartAmount     decimal.Decimal `json:"partAmount"`
	LaborHours     decimal.Decimal `json:"laborHours"`
	LaborRate      decimal.Decimal `json:"laborRate"`
	ClaimedAmount  decimal.Decimal `json:"claimedAmount"`
	AcceptedAmount decimal.Decimal `json:"acceptedAmount"`
}

// ClaimableAmount is part + hours × rate (BR-45.12), rounded half-even to the
// currency's minor unit. A rate of zero is a vendor that does not cover labor.
func ClaimableAmount(part, hours, rate decimal.Decimal, currency string) decimal.Decimal {
	return part.Add(hours.Mul(rate)).RoundBank(money.MinorUnits(currency))
}

// WarrantyShare is what a posted work order puts on a claim: the warranty cost of
// its WARRANTY part lines and the hours of its WARRANTY labor lines (BR-45.4). Travel
// is not claimable from the vendor. ok is false when the order has no warranty line.
func WarrantyShare(p Posting) (partCost, laborHours decimal.Decimal, ok bool) {
	partCost, laborHours = decimal.Zero, decimal.Zero
	for _, l := range p.Lines {
		if l.Coverage != CoverWarranty {
			continue
		}
		switch l.Kind {
		case KindPart:
			partCost, ok = partCost.Add(l.WarrantyCost), true
		case KindLabor:
			laborHours, ok = laborHours.Add(l.Qty), true
		}
	}
	return partCost, laborHours, ok
}

// ClaimTotals sums the claimed and accepted amounts of the lines.
func ClaimTotals(lines []ClaimLine) (claimed, accepted decimal.Decimal) {
	claimed, accepted = decimal.Zero, decimal.Zero
	for _, l := range lines {
		claimed = claimed.Add(l.ClaimedAmount)
		accepted = accepted.Add(l.AcceptedAmount)
	}
	return claimed, accepted
}

// WindowEnds is the last day a claim may be submitted: windowDays after the work
// order completed (BR-45.12).
func WindowEnds(completedOn time.Time, windowDays int) time.Time {
	return completedOn.AddDate(0, 0, windowDays)
}

// CheckSubmit applies BR-45.12 to a submission on the calendar day today: the
// claim is a DRAFT, a failed-part return is recorded when the vendor requires one
// (SVC-SWC-002), and today is inside the claim window (SVC-SWC-001).
func CheckSubmit(status string, completedOn, today time.Time, windowDays int, returnRequired bool, returnOrderID string) error {
	if status != ClaimDraft {
		return &Error{Code: CodeClaimState, Detail: "only a DRAFT claim can be submitted; this one is " + status}
	}
	if returnRequired && returnOrderID == "" {
		return &Error{Code: CodeClaimReturn, Detail: "failed-part return required before submission"}
	}
	end := WindowEnds(completedOn, windowDays)
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	if day.After(end) {
		return &Error{Code: CodeClaimWindow, Detail: "claim window for the vendor ended on " + end.Format("2006-01-02")}
	}
	return nil
}

// Vendor decisions on a submitted claim.
const (
	DecisionAccepted = ClaimAccepted
	DecisionPartial  = ClaimPartial
	DecisionRejected = ClaimRejected
)

// Decide applies a vendor decision to a SUBMITTED claim and returns its lines with
// the accepted amounts set. ACCEPTED accepts every line in full when accepted is
// nil; PARTIALLY_ACCEPTED needs one amount per line and a total above zero and below
// the claimed total; REJECTED needs a reason and accepts nothing. No line accepts
// more than it claimed (SVC-SWC-003). Amounts are rounded to the currency.
func Decide(status, decision, reason, currency string, lines []ClaimLine, accepted []decimal.Decimal) ([]ClaimLine, error) {
	if status != ClaimSubmitted {
		return nil, &Error{Code: CodeClaimState, Detail: "only a SUBMITTED claim takes a vendor decision; this one is " + status}
	}
	scale := money.MinorUnits(currency)
	out := make([]ClaimLine, len(lines))
	copy(out, lines)
	switch decision {
	case DecisionAccepted, DecisionRejected:
		if accepted != nil && len(accepted) != len(lines) {
			return nil, &Error{Code: CodeClaimDecision, Detail: fmt.Sprintf("give one accepted amount per claim line (%d) or none", len(lines))}
		}
	case DecisionPartial:
		if len(accepted) != len(lines) {
			return nil, &Error{Code: CodeClaimDecision, Detail: fmt.Sprintf("a partial acceptance needs one accepted amount per claim line (%d)", len(lines))}
		}
	default:
		return nil, &Error{Code: CodeClaimDecision, Detail: "the decision must be ACCEPTED, PARTIALLY_ACCEPTED or REJECTED"}
	}
	if decision == DecisionRejected && reason == "" {
		return nil, &Error{Code: CodeClaimDecision, Detail: "a rejected claim closes with a reason"}
	}
	for i := range out {
		a := decimal.Zero
		switch {
		case accepted != nil:
			a = accepted[i].RoundBank(scale)
		case decision == DecisionAccepted:
			a = out[i].ClaimedAmount
		}
		if a.IsNegative() {
			return nil, &Error{Code: CodeClaimDecision, Detail: "accepted amounts cannot be negative"}
		}
		if a.GreaterThan(out[i].ClaimedAmount) {
			return nil, &Error{Code: CodeClaimOverAccept, Detail: fmt.Sprintf("accepted amount exceeds claimed amount on line %d (%s > %s)", i+1, a, out[i].ClaimedAmount)}
		}
		out[i].AcceptedAmount = a
	}
	claimed, total := ClaimTotals(out)
	switch decision {
	case DecisionAccepted:
		if !total.Equal(claimed) {
			return nil, &Error{Code: CodeClaimDecision, Detail: "an ACCEPTED claim accepts every line in full; use PARTIALLY_ACCEPTED"}
		}
	case DecisionPartial:
		if !total.IsPositive() || !total.LessThan(claimed) {
			return nil, &Error{Code: CodeClaimDecision, Detail: "a partial acceptance accepts more than zero and less than the claimed total"}
		}
	case DecisionRejected:
		if !total.IsZero() {
			return nil, &Error{Code: CodeClaimDecision, Detail: "a rejected claim accepts nothing"}
		}
	}
	return out, nil
}

// CheckSettle matches a vendor credit note to an ACCEPTED or PARTIALLY_ACCEPTED
// claim (BR-45.12): the note quotes the claim's vendor reference (SVC-SWC-010) and
// credits exactly the accepted total in the claim currency (SVC-SWC-005). The
// recovered amount is the credit.
func CheckSettle(status, claimReference, claimCurrency string, acceptedTotal decimal.Decimal, noteReference, noteCurrency string, credit decimal.Decimal) (decimal.Decimal, error) {
	if status != ClaimAccepted && status != ClaimPartial {
		return decimal.Zero, &Error{Code: CodeClaimState, Detail: "only an ACCEPTED or PARTIALLY_ACCEPTED claim is settled; this one is " + status}
	}
	if claimReference == "" || noteReference != claimReference {
		return decimal.Zero, &Error{Code: CodeClaimReference, Detail: "the credit note's vendor reference does not match the claim's"}
	}
	rounded := credit.RoundBank(money.MinorUnits(claimCurrency))
	if noteCurrency != claimCurrency || !rounded.Equal(acceptedTotal) {
		return decimal.Zero, &Error{Code: CodeClaimCredit, Detail: fmt.Sprintf("the credit note must credit the accepted total %s %s", acceptedTotal, claimCurrency)}
	}
	return rounded, nil
}

// CheckCloseClaim lets a REJECTED or SETTLED claim close (BR-45.12: rejected claims
// close with their reason and no posting).
func CheckCloseClaim(status string) error {
	if status != ClaimRejected && status != ClaimSettled {
		return &Error{Code: CodeClaimState, Detail: "only a REJECTED or SETTLED claim can be closed; this one is " + status}
	}
	return nil
}
