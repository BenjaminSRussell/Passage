package domain

import (
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Settlement is how an open cash advance is applied to an approved report (BR-43.9).
type Settlement struct {
	Applied         decimal.Decimal
	OpenAfter       decimal.Decimal
	APInvoiceAmount decimal.Decimal
}

// SettleAdvance applies min(open advance, reimbursable total). The remainder of
// the advance stays open; the rest of the reimbursement goes to the AP invoice.
func SettleAdvance(open, reimbursable decimal.Decimal) Settlement {
	open = decimal.Max(decimal.Zero, open)
	reimbursable = decimal.Max(decimal.Zero, reimbursable)
	applied := decimal.Min(open, reimbursable)
	return Settlement{Applied: applied, OpenAfter: open.Sub(applied), APInvoiceAmount: reimbursable.Sub(applied)}
}

// Cash advance statuses after payment (FR-43.15).
const (
	AdvancePaid             = "PAID"
	AdvancePartiallySettled = "PARTIALLY_SETTLED"
	AdvanceSettled          = "SETTLED"
)

// Advance is a PAID cash advance of the worker in the report currency.
// Cleared is what earlier reports, cash returns, payroll recovery and
// write-offs have already taken off it.
type Advance struct {
	ID      string
	PaidOn  time.Time
	Paid    decimal.Decimal
	Cleared decimal.Decimal
}

// Open is the part of the advance not yet cleared.
func (a Advance) Open() decimal.Decimal { return decimal.Max(decimal.Zero, a.Paid.Sub(a.Cleared)) }

// AdvanceStatus is PAID while nothing is cleared, SETTLED once the paid amount
// is cleared in full and PARTIALLY_SETTLED in between (AC-43.9).
func AdvanceStatus(paid, cleared decimal.Decimal) string {
	switch {
	case !cleared.IsPositive():
		return AdvancePaid
	case cleared.GreaterThanOrEqual(paid):
		return AdvanceSettled
	}
	return AdvancePartiallySettled
}

// Application is what one advance contributes to a report's settlement.
type Application struct {
	AdvanceID string
	Applied   decimal.Decimal
	OpenAfter decimal.Decimal
	Status    string
}

// ReportSettlement is the outcome of applying a worker's advances to one report.
type ReportSettlement struct {
	Applications    []Application
	Applied         decimal.Decimal
	APInvoiceAmount decimal.Decimal
}

// SettleAdvances applies open advances oldest first (by payment date, then ID)
// to the report's reimbursable total (BR-43.9). Each advance takes
// min(its open amount, what is still unapplied); the AP invoice is reduced by
// the total applied. Advances with nothing open are skipped.
func SettleAdvances(advances []Advance, reimbursable decimal.Decimal) ReportSettlement {
	ordered := make([]Advance, len(advances))
	copy(ordered, advances)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].PaidOn.Equal(ordered[j].PaidOn) {
			return ordered[i].PaidOn.Before(ordered[j].PaidOn)
		}
		return ordered[i].ID < ordered[j].ID
	})
	remaining := decimal.Max(decimal.Zero, reimbursable)
	out := ReportSettlement{Applied: decimal.Zero}
	for _, a := range ordered {
		open := a.Open()
		if !open.IsPositive() {
			continue
		}
		s := SettleAdvance(open, remaining)
		remaining = s.APInvoiceAmount
		out.Applied = out.Applied.Add(s.Applied)
		out.Applications = append(out.Applications, Application{
			AdvanceID: a.ID, Applied: s.Applied, OpenAfter: s.OpenAfter,
			Status: AdvanceStatus(a.Paid, a.Cleared.Add(s.Applied)),
		})
	}
	out.APInvoiceAmount = remaining
	return out
}

// ExceedsRequisition is BR-43.13: a linked report needs justification when its
// actual total is above estimated × (1 + tolerance). tolerance is a fraction (0.10 = 10%).
func ExceedsRequisition(estimated, actual, tolerance decimal.Decimal) bool {
	ceiling := estimated.Mul(decimal.NewFromInt(1).Add(tolerance))
	return actual.GreaterThan(ceiling)
}

// CardTxn is an imported card transaction (FR-43.13).
type CardTxn struct {
	ID       string
	WorkerID string
	Date     time.Time
	Merchant string
	Amount   decimal.Decimal
}

// Receipt is an OCR draft or uploaded receipt the worker owns.
type Receipt struct {
	ID       string
	WorkerID string
	Date     time.Time
	Merchant string
	Amount   decimal.Decimal
}

// Tolerance is the card match window: amount as a fraction of the card amount,
// and date within Days. The default is ±1% and 2 days.
type Tolerance struct {
	Pct  decimal.Decimal
	Days int
}

// DefaultTolerance returns the tenant default card match tolerance.
func DefaultTolerance() Tolerance {
	return Tolerance{Pct: decimal.RequireFromString("0.01"), Days: 2}
}

// Matches reports whether a receipt can be matched to a card transaction: the
// same worker, a merchant equal ignoring case and surrounding space, the date
// within the window, and the amount within the percentage tolerance. Matching
// never crosses workers.
func Matches(c CardTxn, r Receipt, tol Tolerance) bool {
	if c.WorkerID == "" || c.WorkerID != r.WorkerID {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(c.Merchant), strings.TrimSpace(r.Merchant)) {
		return false
	}
	days := civilDays(r.Date, c.Date)
	if days < 0 {
		days = -days
	}
	if days > tol.Days {
		return false
	}
	return c.Amount.Sub(r.Amount).Abs().LessThanOrEqual(c.Amount.Abs().Mul(tol.Pct))
}

// Pair is one card transaction matched to one receipt.
type Pair struct {
	CardID    string
	ReceiptID string
}

// MatchReceipts pairs card transactions with receipts one to one (a card
// transaction belongs to at most one line, 8.43.4). Cards are taken in date
// then ID order; each takes the unused receipt that Matches with the smallest
// amount difference, then the closest date, then the lowest ID. Unmatched
// cards stay in the worker's unassigned pool.
func MatchReceipts(cards []CardTxn, receipts []Receipt, tol Tolerance) []Pair {
	ordered := make([]CardTxn, len(cards))
	copy(ordered, cards)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].Date.Equal(ordered[j].Date) {
			return ordered[i].Date.Before(ordered[j].Date)
		}
		return ordered[i].ID < ordered[j].ID
	})
	used := make([]bool, len(receipts))
	var out []Pair
	for _, c := range ordered {
		best := -1
		for i, r := range receipts {
			if used[i] || !Matches(c, r, tol) {
				continue
			}
			if best < 0 || closer(c, r, receipts[best]) {
				best = i
			}
		}
		if best >= 0 {
			used[best] = true
			out = append(out, Pair{CardID: c.ID, ReceiptID: receipts[best].ID})
		}
	}
	return out
}

// closer reports whether receipt a is a better match for c than receipt b.
func closer(c CardTxn, a, b Receipt) bool {
	da, db := c.Amount.Sub(a.Amount).Abs(), c.Amount.Sub(b.Amount).Abs()
	if !da.Equal(db) {
		return da.LessThan(db)
	}
	ga, gb := c.Date.Sub(a.Date).Abs(), c.Date.Sub(b.Date).Abs()
	if ga != gb {
		return ga < gb
	}
	return a.ID < b.ID
}
