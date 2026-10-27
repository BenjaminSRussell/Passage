package domain

import (
	"strings"

	"github.com/shopspring/decimal"
)

// Rule IDs of the slice-3 checks. They report through the same Result shape
// as the default rule set.
const (
	// RuleRequisitionRequired is the FR-43.16 "travel requisition required"
	// parameter applied to a line; its result carries CodeRequisitionRequired.
	RuleRequisitionRequired = "EXP-TR-002"
	// RuleLodgingCap is the BR-43.5 lodging example: per night at most the
	// per-diem lodging rate × 1.5, else justification.
	RuleLodgingCap = "EXP-LODG-150"
)

// CodeRequisitionRequired is the 8.43.18 error for a line whose category
// needs an approved requisition the report does not have.
const CodeRequisitionRequired = "EXP-TR-002"

// lodgingCapFactor is the BR-43.5 ceiling over the per-diem lodging rate.
var lodgingCapFactor = decimal.RequireFromString("1.5")

// requisitionResult is the EXP-TR-002 BLOCK for one line, or false when the
// line's category does not need a requisition or the report has one.
func requisitionResult(line Line, ctx Context) (Result, bool) {
	if ctx.HasRequisition || !containsFold(ctx.RequisitionCategories, line.Category) {
		return Result{}, false
	}
	return Result{LineID: line.ID, RuleID: RuleRequisitionRequired, Version: 1, Outcome: OutcomeBlock,
		Code: CodeRequisitionRequired, Message: "Requisition required for category " + line.Category}, true
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// LodgingCheck is the outcome of the BR-43.5 lodging cap for one stay.
type LodgingCheck struct {
	// CapPerNight is the per-diem lodging rate × 1.5.
	CapPerNight decimal.Decimal
	// Cap is CapPerNight × nights: the most the stay may cost without a justification.
	Cap decimal.Decimal
	// OverCap is true when the stay costs more than Cap.
	OverCap bool
	// Result is the REQUIRE_JUSTIFICATION result when OverCap, satisfied by a
	// non-blank justification.
	Result *Result
}

// CheckLodging applies the BR-43.5 lodging cap to a stay of nights nights
// costing amount, against the per-diem lodging rate for the city and date.
// The comparison is amount > rate × 1.5 × nights, so a stay at exactly the cap
// is allowed and no per-night division rounds the test. nights must be >= 1.
func CheckLodging(lineID string, amount decimal.Decimal, nights int, rate decimal.Decimal, justification string) LodgingCheck {
	perNight := rate.Mul(lodgingCapFactor)
	limit := perNight.Mul(decimal.NewFromInt(int64(nights)))
	out := LodgingCheck{CapPerNight: perNight, Cap: limit, OverCap: amount.GreaterThan(limit)}
	if out.OverCap {
		out.Result = &Result{LineID: lineID, RuleID: RuleLodgingCap, Version: 1, Outcome: OutcomeRequireJustification,
			Code: outcomeCode(OutcomeRequireJustification), Message: "Lodging above 1.5 times the per-diem lodging rate needs a justification",
			Satisfied: strings.TrimSpace(justification) != ""}
	}
	return out
}

// MileageAmount is distance × rate per unit, rounded half away from zero to
// the cent (BR-43.5 "mileage at IRS rate from table"). The rate is the pack's
// table value for the vehicle type and date (IRS 0.70 USD/mile, BMF 0.30
// EUR/km in 2025); it is never hard-coded here.
func MileageAmount(distance, rate decimal.Decimal) decimal.Decimal {
	return distance.Mul(rate).Round(2)
}

// MatchBatch is a one-to-one card-to-receipt match over a batch (FR-43.13).
type MatchBatch struct {
	Pairs             []Pair
	UnmatchedCards    []string
	UnmatchedReceipts []string
	// Rate is matched cards over all cards, to four places; zero for no cards.
	Rate decimal.Decimal
}

// MatchAll runs MatchReceipts over a batch and reports what stayed unmatched,
// in input order, and the auto-match rate AC-43.7 measures.
func MatchAll(cards []CardTxn, receipts []Receipt, tol Tolerance) MatchBatch {
	pairs := MatchReceipts(cards, receipts, tol)
	cardUsed, receiptUsed := map[string]bool{}, map[string]bool{}
	for _, p := range pairs {
		cardUsed[p.CardID], receiptUsed[p.ReceiptID] = true, true
	}
	out := MatchBatch{Pairs: pairs, UnmatchedCards: []string{}, UnmatchedReceipts: []string{}, Rate: decimal.Zero}
	for _, c := range cards {
		if !cardUsed[c.ID] {
			out.UnmatchedCards = append(out.UnmatchedCards, c.ID)
		}
	}
	for _, r := range receipts {
		if !receiptUsed[r.ID] {
			out.UnmatchedReceipts = append(out.UnmatchedReceipts, r.ID)
		}
	}
	if len(cards) > 0 {
		out.Rate = decimal.NewFromInt(int64(len(pairs))).Div(decimal.NewFromInt(int64(len(cards)))).Round(4)
	}
	return out
}
