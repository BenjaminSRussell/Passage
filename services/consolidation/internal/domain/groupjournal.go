package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Top-side group journal document types (FR-40.19, `consol.documentTypes`).
const (
	DocAdjustment       = "Adjustment"
	DocReclassification = "Reclassification"
	DocElimination      = "Elimination"
	DocCoI              = "CoI"
	// DocCumulative is carried forward past year end (BR-40.18).
	DocCumulative = "Cumulative"
)

// DocumentTypes lists the seeded document types in display order.
var DocumentTypes = []string{DocAdjustment, DocReclassification, DocElimination, DocCoI, DocCumulative}

// CodeApprovalRequired is raised for a top-side journal at or above the approval
// threshold that has not been approved (CNS-0909).
const CodeApprovalRequired = "CNS-0909"

// DefaultApprovalThreshold is the group-currency amount from which a top-side
// journal needs approval (FR-40.19 default 100,000.00).
func DefaultApprovalThreshold() decimal.Decimal { return decimal.NewFromInt(100000) }

// GroupJournalLine is one top-side line. FSItem is the consolidation account or FS
// item; PartnerUnit names the counterpart unit for IC lines and may be empty.
type GroupJournalLine struct {
	FSItem      string
	Debit       decimal.Decimal
	Credit      decimal.Decimal
	PartnerUnit string
}

// GroupJournal is a manual journal posted to a group and period, or to one unit of
// it (FR-40.19). It attaches to the group and period, not to a run version, so it
// survives reruns. CarryForward applies it to later periods of the same fiscal year.
type GroupJournal struct {
	PeriodKey     string // YYYY-MM
	DocumentType  string
	UnitID        string // empty for a group-level journal
	Currency      string
	Reason        string
	AttachmentRef string
	CarryForward  bool
	Approved      bool
	Lines         []GroupJournalLine
}

// Validate checks the journal invariants: a known document type, a reason and an
// attachment (FR-40.19: every entry needs both), at least two lines, each line
// on one side only and at the group currency minor unit, and debits equal to
// credits (balanced in the group currency, CNS-0903).
func (j GroupJournal) Validate() error {
	if _, err := parsePeriod(j.PeriodKey); err != nil {
		return err
	}
	switch j.DocumentType {
	case DocAdjustment, DocReclassification, DocElimination, DocCoI, DocCumulative:
	default:
		return invalid(fmt.Sprintf("unknown top-side document type %q", j.DocumentType))
	}
	switch {
	case len(j.Currency) != 3:
		return invalid("group journal currency must be an ISO 4217 code")
	case j.Reason == "":
		return invalid("every top-side entry needs a reason")
	case j.AttachmentRef == "":
		return invalid("every top-side entry needs an attachment")
	case len(j.Lines) < 2:
		return invalid("a top-side journal needs at least two lines")
	}
	debit, credit := decimal.Zero, decimal.Zero
	for i, l := range j.Lines {
		switch {
		case l.FSItem == "":
			return invalid(fmt.Sprintf("line %d needs an FS item", i+1))
		case l.Debit.IsNegative() || l.Credit.IsNegative():
			return invalid(fmt.Sprintf("line %d amounts must not be negative", i+1))
		case l.Debit.IsPositive() == l.Credit.IsPositive():
			return invalid(fmt.Sprintf("line %d needs exactly one of debit and credit", i+1))
		case !round(l.Debit, j.Currency).Equal(l.Debit) || !round(l.Credit, j.Currency).Equal(l.Credit):
			return invalid(fmt.Sprintf("line %d has more decimals than %s allows", i+1, j.Currency))
		}
		debit = debit.Add(l.Debit)
		credit = credit.Add(l.Credit)
	}
	if !debit.Equal(credit) {
		places := money.MinorUnits(j.Currency)
		return &Error{Code: CodeUnbalancedRule, Detail: fmt.Sprintf("top-side journal unbalanced: debit %s, credit %s", debit.StringFixed(places), credit.StringFixed(places))}
	}
	return nil
}

// Total is the debit total of the journal, the amount the approval threshold tests.
func (j GroupJournal) Total() decimal.Decimal {
	total := decimal.Zero
	for _, l := range j.Lines {
		total = total.Add(l.Debit)
	}
	return total
}

// ApprovalRequired reports whether the journal total is at or above the threshold.
func (j GroupJournal) ApprovalRequired(threshold decimal.Decimal) bool {
	return j.Total().GreaterThanOrEqual(threshold)
}

// CheckApproval returns CNS-0909 when approval is required and missing.
func (j GroupJournal) CheckApproval(threshold decimal.Decimal) error {
	if j.ApprovalRequired(threshold) && !j.Approved {
		places := money.MinorUnits(j.Currency)
		return &Error{Code: CodeApprovalRequired, Detail: fmt.Sprintf("top-side journal of %s %s needs approval: the threshold is %s", j.Total().StringFixed(places), j.Currency, threshold.StringFixed(places))}
	}
	return nil
}

// AppliesTo reports whether the journal posts in period (YYYY-MM) under the fiscal
// year start month (1–12). The origin period always applies. A carried-forward
// journal applies to later periods of the same fiscal year, and past year end only
// when its document type is Cumulative (BR-40.18).
func (j GroupJournal) AppliesTo(period string, fyStartMonth int) (bool, error) {
	origin, err := parsePeriod(j.PeriodKey)
	if err != nil {
		return false, err
	}
	target, err := parsePeriod(period)
	if err != nil {
		return false, err
	}
	if fyStartMonth < 1 || fyStartMonth > 12 {
		return false, invalid("fiscal year start month must be 1 to 12")
	}
	if target.Before(origin) {
		return false, nil
	}
	if target.Equal(origin) {
		return true, nil
	}
	if !j.CarryForward {
		return false, nil
	}
	return j.DocumentType == DocCumulative || fiscalYear(origin, fyStartMonth) == fiscalYear(target, fyStartMonth), nil
}

// Periods lists the periods (YYYY-MM) the journal posts in from its origin
// through the end of the origin's fiscal year: the origin alone for a
// period-only journal, every period to year end for a carried-forward one
// (BR-40.18). PastYearEnd reports that a carried-forward Cumulative journal
// keeps applying after the listed periods.
func (j GroupJournal) Periods(fyStartMonth int) (periods []string, pastYearEnd bool, err error) {
	origin, err := parsePeriod(j.PeriodKey)
	if err != nil {
		return nil, false, err
	}
	if fyStartMonth < 1 || fyStartMonth > 12 {
		return nil, false, invalid("fiscal year start month must be 1 to 12")
	}
	if !j.CarryForward {
		return []string{j.PeriodKey}, false, nil
	}
	fy := fiscalYear(origin, fyStartMonth)
	for t := origin; fiscalYear(t, fyStartMonth) == fy; t = t.AddDate(0, 1, 0) {
		periods = append(periods, t.Format("2006-01"))
	}
	return periods, j.DocumentType == DocCumulative, nil
}

func parsePeriod(key string) (time.Time, error) {
	t, err := time.Parse("2006-01", key)
	if err != nil {
		return time.Time{}, invalid("period must be YYYY-MM")
	}
	return t, nil
}

// fiscalYear numbers the fiscal year that holds t: the calendar year of t, less one
// when t falls before the fiscal year start month.
func fiscalYear(t time.Time, startMonth int) int {
	if int(t.Month()) < startMonth {
		return t.Year() - 1
	}
	return t.Year()
}
