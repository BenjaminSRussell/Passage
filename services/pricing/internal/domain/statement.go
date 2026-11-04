package domain

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

// StatusSettled marks a rebate accrual that a settlement (w30 credit note, w19 payment) has paid.
const StatusSettled = "SETTLED"

// Statement line sources: an invoice line accrued from the invoice feed, an invoice line a
// retroactive recompute replayed into the period basis (accrues nothing itself), and the
// period delta the recompute posted.
const (
	SourceInvoice    = "INVOICE"
	SourceReplayed   = "REPLAYED"
	SourceRetroDelta = "RETRO_DELTA"
)

// StatementLine is one accrual row of a rebate statement: a posted invoice line, a replayed
// line of a retroactive recompute, or a retroactive delta.
type StatementLine struct {
	InvoiceLineID string
	InvoiceDate   string
	BasisValue    decimal.Decimal
	BasisQty      decimal.Decimal
	Accrued       decimal.Decimal
	Status        string
	Retroactive   bool
}

// Source names where the line comes from. A retroactive delta is a retroactive row whose
// invoice line id is retro:<run>:<period> (a replayed line may not use the prefix); its stored
// basis is the period's cumulative basis, not one of its own, so it does not count toward the
// period basis.
func (l StatementLine) Source() string {
	switch {
	case l.Retroactive && strings.HasPrefix(l.InvoiceLineID, "retro:"):
		return SourceRetroDelta
	case l.Retroactive:
		return SourceReplayed
	}
	return SourceInvoice
}

// StatementInput is what the store read for one agreement and one period: the cumulative
// period state, the exact sum of the settled rows (independent of how many lines are listed)
// and the listed lines.
type StatementInput struct {
	Period  PeriodState
	Settled decimal.Decimal
	Lines   []StatementLine
	// LineCount is the number of accrual rows of the period; it exceeds len(Lines) when the
	// list was cut at the statement limit.
	LineCount int
	// RowsAccrued is the sum of every accrual row of the period, and RowsBasisValue and
	// RowsBasisQty the sum of their own basis (retroactive delta rows excluded), all over every
	// row whatever the list limit. They are what the period row must agree with.
	RowsAccrued    decimal.Decimal
	RowsBasisValue decimal.Decimal
	RowsBasisQty   decimal.Decimal
}

// RebateStatement is the rebate agreement statement document (PRD §5.26.15): header
// (agreement, party, period), lines (invoice, basis, accrued) and footer (accrued, settled,
// open). The tier is the one in force at the period's cumulative basis; Target is what that
// tier owes on the whole period (BR-26.8). Reconciled says the accrual rows agree with it:
// the rows sum to the target, and their basis sums to the period's cumulative basis.
type RebateStatement struct {
	Agreement  RebateAgreement
	PeriodKey  string
	Tier       *Tier
	BasisValue decimal.Decimal
	BasisQty   decimal.Decimal
	Target     decimal.Decimal
	Accrued    decimal.Decimal
	// RowsAccrued is the sum of the period's accrual rows (every row, not only those listed).
	RowsAccrued decimal.Decimal
	Settled     decimal.Decimal
	Open        decimal.Decimal
	Reconciled  bool
	Lines       []StatementLine
	LineCount   int
	Truncated   bool
}

var (
	monthPeriod   = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
	quarterPeriod = regexp.MustCompile(`^[0-9]{4}-Q[1-4]$`)
	yearPeriod    = regexp.MustCompile(`^[0-9]{4}$`)
)

// ValidStatementPeriod checks a statement period key against the agreement's accrual period:
// a MONTH agreement accrues by 2027-03, a QUARTER agreement by 2027-Q1, a YEAR agreement by 2027.
func ValidStatementPeriod(a RebateAgreement, key string) *Error {
	var re *regexp.Regexp
	var want string
	switch a.Period {
	case PeriodQuarter:
		re, want = quarterPeriod, "a quarter such as 2027-Q1"
	case PeriodYear:
		re, want = yearPeriod, "a year such as 2027"
	default:
		re, want = monthPeriod, "a month such as 2027-03"
	}
	if key == "" || !re.MatchString(key) {
		return &Error{Code: CodeValidation, Detail: "rebate statement period is invalid",
			Fields: map[string]string{"period": fmt.Sprintf("required; this agreement accrues by %s, so it must be %s", PeriodNoun(a.Period), want)}}
	}
	return nil
}

// PeriodNoun names an accrual period for messages.
func PeriodNoun(period string) string {
	switch period {
	case PeriodQuarter:
		return "quarter"
	case PeriodYear:
		return "year"
	}
	return "month"
}

// BuildRebateStatement composes the statement from the stored period state. It is pure: the
// same inputs give the same document. Open is accrued less settled; a period with no activity
// is a statement with zero totals and no lines.
//
// Reconciled is the check an auditor makes on the document: what the tier in force owes on
// the whole period (one rate on the cumulative basis, BR-26.8; the only tier rule there is)
// equals the period's accrued total, the accrual rows (invoice lines, credits as negative
// rows, retroactive deltas) sum to that same total, and the rows' own basis sums to the
// period's cumulative basis. Comparing the period row with the target alone would always
// agree, since the period row is written as the target; the rows are the independent side.
// Every row is in the agreement currency (accrual refuses any other), so no conversion enters.
func BuildRebateStatement(a RebateAgreement, key string, in StatementInput) RebateStatement {
	s := RebateStatement{Agreement: a, PeriodKey: key, BasisValue: in.Period.Value, BasisQty: in.Period.Qty,
		Accrued: in.Period.Accrued, RowsAccrued: in.RowsAccrued, Settled: in.Settled, Lines: in.Lines, LineCount: in.LineCount}
	cum := in.Period.Value
	if a.Basis == BasisQuantity {
		cum = in.Period.Qty
	}
	if t, ok := TierFor(a.Tiers, cum); ok {
		s.Tier = &t
	}
	s.Target = PeriodTarget(a, in.Period.Value, in.Period.Qty)
	s.Open = s.Accrued.Sub(s.Settled)
	s.Reconciled = s.Target.Equal(s.Accrued) && in.RowsAccrued.Equal(s.Accrued) &&
		in.RowsBasisValue.Equal(in.Period.Value) && in.RowsBasisQty.Equal(in.Period.Qty)
	s.Truncated = in.LineCount > len(in.Lines)
	return s
}
