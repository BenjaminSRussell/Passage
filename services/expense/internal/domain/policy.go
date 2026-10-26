package domain

import (
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Outcome is what a matching rule asks for.
type Outcome string

const (
	OutcomeWarn                 Outcome = "WARN"
	OutcomeBlock                Outcome = "BLOCK"
	OutcomeRequireJustification Outcome = "REQUIRE_JUSTIFICATION"
	OutcomeRequireReceipt       Outcome = "REQUIRE_RECEIPT"
)

// Predicate is the amount or date test a rule applies. CEL conditions are not
// in this slice; a typed predicate with explicit clauses gives the same
// exactness for the shipped rules (see NOT-IMPLEMENTED.md).
type Predicate string

const (
	PredNone          Predicate = "NONE"
	PredAmountAtLeast Predicate = "AMOUNT_AT_LEAST"
	PredAmountOver    Predicate = "AMOUNT_OVER"
	PredSubmittedLate Predicate = "SUBMITTED_LATE"
	// PredDuplicate holds for a line whose merchant, amount and date repeat an
	// earlier line of the same report (the first occurrence is not flagged).
	PredDuplicate Predicate = "DUPLICATE"
)

// Basis is what an amount predicate is tested against (BR-43.5).
type Basis string

const (
	// BasisPerLine tests each line's own amount (the default).
	BasisPerLine Basis = "PER_LINE"
	// BasisPerDay tests the sum of the day's lines the rule's category,
	// receipt and attendee clauses select; when it holds, every one of those
	// lines carries the result.
	BasisPerDay Basis = "PER_DAY"
)

// Message codes for policy outcomes (8.43.18). A REQUIRE_JUSTIFICATION result
// stops submission until justified, so it carries the error code EXP-POL-001.
const (
	CodeBlock          = "EXP-POL-001"
	CodeWarn           = "EXP-POL-002"
	CodeReceipt        = "EXP-REC-001"
	CodeRequisition    = "EXP-TR-001"
	CodeAdvanceApplied = "EXP-ADV-003"
)

func outcomeCode(o Outcome) string {
	switch o {
	case OutcomeWarn:
		return CodeWarn
	case OutcomeRequireReceipt:
		return CodeReceipt
	}
	return CodeBlock
}

// Rule is one versioned policy rule (BR-43.5). A rule applies when every clause
// set on it holds: effective date, categories, predicate, and the receipt and
// attendee clauses. When several versions of one ID are effective on a line's
// transaction date, the highest version wins.
type Rule struct {
	ID            string
	Version       int
	EffectiveFrom time.Time
	Categories    []string
	Basis         Basis
	Predicate     Predicate
	Amount        decimal.Decimal
	Days          int
	NoReceipt     bool
	NoAttendees   bool
	Outcome       Outcome
	Message       string
}

// Context carries the report-level facts a rule may test.
type Context struct {
	SubmitDate time.Time
}

// Result is one rule that applied to one line.
type Result struct {
	LineID    string  `json:"lineId"`
	RuleID    string  `json:"ruleId"`
	Version   int     `json:"ruleVersion"`
	Outcome   Outcome `json:"outcome"`
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	Satisfied bool    `json:"satisfied"`
}

func civil(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// DefaultRules is the shipped rule set from BR-43.5: receipts at 25.00 and up,
// alcohol blocked, submissions later than 60 days blocked, duplicates (same
// merchant, amount and date) blocked, and meals over 75.00 a day without
// attendees needing justification.
func DefaultRules() []Rule {
	epoch := civil(2000, time.January, 1)
	return []Rule{
		{ID: "EXP-RCPT-025", Version: 1, EffectiveFrom: epoch, Predicate: PredAmountAtLeast, Amount: decimal.NewFromInt(25),
			NoReceipt: true, Outcome: OutcomeRequireReceipt, Message: "Receipt required for lines of 25.00 or more"},
		{ID: "EXP-ALC-001", Version: 1, EffectiveFrom: epoch, Categories: []string{"ALCOHOL"}, Predicate: PredNone,
			Outcome: OutcomeBlock, Message: "Alcohol is not reimbursable"},
		{ID: "EXP-SUB-060", Version: 1, EffectiveFrom: epoch, Predicate: PredSubmittedLate, Days: 60,
			Outcome: OutcomeBlock, Message: "Submitted more than 60 days after the transaction date"},
		{ID: "EXP-DUP-001", Version: 1, EffectiveFrom: epoch, Predicate: PredDuplicate,
			Outcome: OutcomeBlock, Message: "Duplicate of another line with the same merchant, amount and date"},
		{ID: "EXP-MEAL-075", Version: 3, EffectiveFrom: epoch, Categories: []string{"MEALS"}, Basis: BasisPerDay, Predicate: PredAmountOver,
			Amount: decimal.NewFromInt(75), NoAttendees: true, Outcome: OutcomeRequireJustification,
			Message: "Meals over 75.00 a day without attendees need a justification"},
	}
}

// Evaluate returns the rules that apply to one line on its own (line commit).
func Evaluate(rules []Rule, line Line, ctx Context) []Result {
	return EvaluateReport(rules, []Line{line}, ctx)
}

// EvaluateReport returns, line by line in input order, the rules that apply to
// a report's lines at the submit date in ctx (FR-43.12). PER_DAY rules and
// duplicate detection look across the report's lines.
func EvaluateReport(rules []Rule, lines []Line, ctx Context) []Result {
	dayTotals := map[dayKey]decimal.Decimal{}
	for _, r := range rules {
		if r.Basis != BasisPerDay {
			continue
		}
		for _, l := range lines {
			if r.effectiveFor(rules, l) && r.selects(l) {
				k := dayKey{r.ID, l.TxnDate.Format(time.DateOnly)}
				dayTotals[k] = dayTotals[k].Add(l.Amount)
			}
		}
	}
	duplicate := duplicates(lines)

	var out []Result
	for i, line := range lines {
		for _, r := range rules {
			if !r.effectiveFor(rules, line) || !r.selects(line) {
				continue
			}
			amount := line.Amount
			if r.Basis == BasisPerDay {
				amount = dayTotals[dayKey{r.ID, line.TxnDate.Format(time.DateOnly)}]
			}
			if !r.holds(line, amount, duplicate[i], ctx) {
				continue
			}
			out = append(out, Result{LineID: line.ID, RuleID: r.ID, Version: r.Version, Outcome: r.Outcome,
				Code: outcomeCode(r.Outcome), Message: r.Message, Satisfied: satisfied(r.Outcome, line)})
		}
	}
	return out
}

type dayKey struct{ rule, day string }

// duplicates marks every line whose merchant (ignoring case and surrounding
// space), amount and transaction date repeat an earlier line.
func duplicates(lines []Line) []bool {
	type key struct{ merchant, amount, day string }
	seen := map[key]bool{}
	out := make([]bool, len(lines))
	for i, l := range lines {
		m := strings.ToLower(strings.TrimSpace(l.Merchant))
		if m == "" {
			continue
		}
		k := key{m, l.Amount.String(), l.TxnDate.Format(time.DateOnly)}
		out[i] = seen[k]
		seen[k] = true
	}
	return out
}

// Submittable is true when no BLOCK applied and every REQUIRE_* is satisfied (8.43.5).
func Submittable(results []Result) bool {
	for _, r := range results {
		if !r.Satisfied {
			return false
		}
	}
	return true
}

// effectiveFor reports whether r is the version of its rule ID in force on the
// line's transaction date: effective by then, and no higher version also is.
func (r Rule) effectiveFor(rules []Rule, line Line) bool {
	if line.TxnDate.Before(r.EffectiveFrom) {
		return false
	}
	for _, other := range rules {
		if other.ID == r.ID && other.Version > r.Version && !line.TxnDate.Before(other.EffectiveFrom) {
			return false
		}
	}
	return true
}

// selects applies the category, receipt and attendee clauses.
func (r Rule) selects(line Line) bool {
	if len(r.Categories) > 0 && !slices.Contains(r.Categories, line.Category) {
		return false
	}
	if r.NoReceipt && line.HasReceipt {
		return false
	}
	return !r.NoAttendees || line.Attendees == 0
}

// holds tests the predicate; amount is the line amount or, for PER_DAY, the day total.
func (r Rule) holds(line Line, amount decimal.Decimal, duplicate bool, ctx Context) bool {
	switch r.Predicate {
	case PredNone:
		return true
	case PredAmountAtLeast:
		return amount.GreaterThanOrEqual(r.Amount)
	case PredAmountOver:
		return amount.GreaterThan(r.Amount)
	case PredSubmittedLate:
		return civilDays(line.TxnDate, ctx.SubmitDate) > r.Days
	case PredDuplicate:
		return duplicate
	}
	return false
}

// civilDays counts calendar days from a to b, independent of time of day.
func civilDays(a, b time.Time) int {
	ad := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	bd := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(bd.Sub(ad).Hours() / 24)
}

func satisfied(o Outcome, line Line) bool {
	switch o {
	case OutcomeBlock:
		return false
	case OutcomeRequireReceipt:
		return line.HasReceipt
	case OutcomeRequireJustification:
		return strings.TrimSpace(line.Justification) != ""
	}
	return true
}
