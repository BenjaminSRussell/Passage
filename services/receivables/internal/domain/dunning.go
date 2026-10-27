package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Dunning rules (FR-30.7, BR-30.2). Pure functions, no I/O.
const (
	ReasonBlocked      = "BLOCKED"
	ReasonDispute      = "DISPUTE"
	ReasonPromise      = "PROMISE"
	ReasonBelowMinimum = "BELOW_MINIMUM"
	ReasonInterval     = "INTERVAL"

	// DefaultGraceDays is the default customer-group grace period.
	DefaultGraceDays = 5
	// DefaultIntervalDays is the minimum days between letters to one customer.
	DefaultIntervalDays = 10

	CodeRunState       = "AR-DUNNING-STATE"
	CodeRunNotReviewed = "AR-0007"
)

// DunnableItem is the dunning and interest view of one open item.
type DunnableItem struct {
	ID             string
	CustomerID     string
	SourceType     string
	DocumentNumber string
	Currency       string
	DueDate        time.Time
	OpenAmount     decimal.Decimal
	DunningLevel   int
	DunningBlock   bool
	HasDispute     bool
	PromiseDate    *time.Time
	LastDunnedAt   *time.Time
	// InterestedTo is the calculation date of the item's latest interest note;
	// the next note charges only the days after it (BR-30.5).
	InterestedTo *time.Time
}

// LetterLine is one item on a planned letter, raised from FromLevel to ToLevel.
type LetterLine struct {
	OpenItemID     string
	DocumentNumber string
	DueDate        time.Time
	OpenAmount     decimal.Decimal
	DaysOverdue    int
	FromLevel      int
	ToLevel        int
}

// PlannedLetter is one customer's letter in one currency.
type PlannedLetter struct {
	CustomerID string
	Currency   string
	Level      int
	PriorLevel int
	Lines      []LetterLine
	Amount     decimal.Decimal
	Fee        decimal.Decimal
}

// Exclusion records an item left off a run and why.
type Exclusion struct {
	OpenItemID string `json:"openItemId"`
	CustomerID string `json:"customerId"`
	Reason     string `json:"reason"`
}

// DunningPlan is the proposal for one run.
type DunningPlan struct {
	Letters    []PlannedLetter
	Exclusions []Exclusion
}

// IneligibleReason returns why an item may not be dunned at asOf ("" when eligible).
func IneligibleReason(it DunnableItem, asOf time.Time) string {
	switch {
	case it.DunningBlock:
		return ReasonBlocked
	case it.HasDispute:
		return ReasonDispute
	case it.PromiseDate != nil && PromiseHolds(*it.PromiseDate, asOf):
		return ReasonPromise
	}
	return ""
}

// TargetLevel maps days overdue to the level the item has earned (BR-30.2).
// No level is earned until the grace days have passed, whatever the grace.
func TargetLevel(daysOverdue, graceDays int) int {
	switch {
	case daysOverdue <= graceDays:
		return 0
	case daysOverdue >= 45:
		return 4
	case daysOverdue >= 30:
		return 3
	case daysOverdue >= 15:
		return 2
	}
	return 1
}

// NextLevel raises an item at most one level per run and never lowers it.
func NextLevel(current, target int) int {
	if target <= current {
		return current
	}
	if current+1 < target {
		return current + 1
	}
	return target
}

// MinLetter is the minimum letter amount per customer (BR-30.2).
func MinLetter(currency string) decimal.Decimal {
	if currency == "EUR" {
		return decimal.NewFromInt(45)
	}
	return decimal.NewFromInt(50)
}

// LetterFee is the level 3 or 4 fee, charged once when a letter first reaches
// that level (BR-30.2).
func LetterFee(currency string, level, priorLevel int) decimal.Decimal {
	if level < 3 || level <= priorLevel {
		return decimal.Zero
	}
	eur := currency == "EUR"
	switch {
	case level == 3 && eur:
		return decimal.NewFromInt(20)
	case level == 3:
		return decimal.NewFromInt(25)
	case eur:
		return decimal.NewFromInt(40)
	}
	return decimal.NewFromInt(50)
}

// daysBetween counts whole days from one UTC date to another.
func daysBetween(from, to time.Time) int { return int(to.Sub(from).Hours() / 24) }

type candidate struct {
	item   DunnableItem
	target int
}

// PlanDunning proposes letters for the items as of asOf. Items below one unit,
// already at their target level, or with no further level are not candidates.
// Excluded candidates are listed with a reason; customers whose letter is
// below the minimum or inside the interval are excluded as a group.
func PlanDunning(asOf time.Time, graceDays, intervalDays int, items []DunnableItem) DunningPlan {
	var plan DunningPlan
	last := map[string]time.Time{}
	for _, it := range items {
		if it.LastDunnedAt != nil {
			if prev, ok := last[it.CustomerID]; !ok || it.LastDunnedAt.After(prev) {
				last[it.CustomerID] = *it.LastDunnedAt
			}
		}
	}
	type group struct{ customer, currency string }
	var order []group
	members := map[group][]candidate{}
	for _, it := range items {
		if it.OpenAmount.LessThan(decimal.NewFromInt(1)) {
			continue
		}
		target := TargetLevel(daysBetween(it.DueDate, asOf), graceDays)
		if target <= it.DunningLevel {
			continue
		}
		if reason := IneligibleReason(it, asOf); reason != "" {
			plan.Exclusions = append(plan.Exclusions, Exclusion{OpenItemID: it.ID, CustomerID: it.CustomerID, Reason: reason})
			continue
		}
		g := group{it.CustomerID, it.Currency}
		if _, ok := members[g]; !ok {
			order = append(order, g)
		}
		members[g] = append(members[g], candidate{item: it, target: target})
	}
	for _, g := range order {
		cands := members[g]
		reason := ""
		if l, ok := last[g.customer]; ok && daysBetween(l, asOf) < intervalDays {
			reason = ReasonInterval
		}
		letter := PlannedLetter{CustomerID: g.customer, Currency: g.currency, Amount: decimal.Zero}
		for _, c := range cands {
			to := NextLevel(c.item.DunningLevel, c.target)
			letter.Lines = append(letter.Lines, LetterLine{OpenItemID: c.item.ID, DocumentNumber: c.item.DocumentNumber,
				DueDate: c.item.DueDate, OpenAmount: c.item.OpenAmount, DaysOverdue: daysBetween(c.item.DueDate, asOf),
				FromLevel: c.item.DunningLevel, ToLevel: to})
			letter.Amount = letter.Amount.Add(c.item.OpenAmount)
			if to > letter.Level {
				letter.Level = to
			}
			if c.item.DunningLevel > letter.PriorLevel {
				letter.PriorLevel = c.item.DunningLevel
			}
		}
		if reason == "" && letter.Amount.LessThan(MinLetter(g.currency)) {
			reason = ReasonBelowMinimum
		}
		if reason != "" {
			for _, c := range cands {
				plan.Exclusions = append(plan.Exclusions, Exclusion{OpenItemID: c.item.ID, CustomerID: g.customer, Reason: reason})
			}
			continue
		}
		letter.Fee = LetterFee(g.currency, letter.Level, letter.PriorLevel)
		plan.Letters = append(plan.Letters, letter)
	}
	return plan
}

// ReviseLetter re-checks a planned letter at issue time against the current item
// state, because exclusions are evaluated at issue as well as at proposal. It
// returns false when no line remains or the remainder is below the minimum.
func ReviseLetter(asOf time.Time, planned PlannedLetter, current map[string]DunnableItem) (PlannedLetter, bool) {
	out := PlannedLetter{CustomerID: planned.CustomerID, Currency: planned.Currency, PriorLevel: planned.PriorLevel, Amount: decimal.Zero}
	for _, ln := range planned.Lines {
		it, ok := current[ln.OpenItemID]
		if !ok || it.DunningLevel != ln.FromLevel || IneligibleReason(it, asOf) != "" || it.OpenAmount.LessThan(decimal.NewFromInt(1)) {
			continue
		}
		kept := ln
		kept.OpenAmount = it.OpenAmount
		kept.DaysOverdue = daysBetween(it.DueDate, asOf)
		out.Lines = append(out.Lines, kept)
		out.Amount = out.Amount.Add(kept.OpenAmount)
		if kept.ToLevel > out.Level {
			out.Level = kept.ToLevel
		}
	}
	if len(out.Lines) == 0 || out.Amount.LessThan(MinLetter(out.Currency)) {
		return out, false
	}
	out.Fee = LetterFee(out.Currency, out.Level, out.PriorLevel)
	return out, true
}

// CanMoveRun reports whether a dunning run may move from → to (FR-30.7).
func CanMoveRun(from, to string) bool {
	switch {
	case from == "DRAFT" && (to == "REVIEWED" || to == "CANCELLED"):
		return true
	case from == "REVIEWED" && (to == "ISSUED" || to == "CANCELLED"):
		return true
	}
	return false
}
