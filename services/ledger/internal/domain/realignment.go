package domain

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Realignment run statuses (FR-11.24).
const (
	RealignProposed  = "proposed"
	RealignPosted    = "posted"
	RealignReversed  = "reversed"
	RealignCancelled = "cancelled"

	// CodeRealignmentInvalid is the 422 code of a filter or mapping that cannot be run.
	CodeRealignmentInvalid = "LEDGER_REALIGNMENT_INVALID"
	// CodeRealignmentPeriodClosed is raised when a period of the run is closed for GL (LEDGER-0030).
	CodeRealignmentPeriodClosed = "LEDGER_REALIGNMENT_PERIOD_CLOSED"

	// MaxRealignmentMappings bounds the old to new value pairs of one run.
	MaxRealignmentMappings = 200
	// MaxRealignmentPeriods bounds the periods one run may span (one entry each).
	MaxRealignmentPeriods = 60
)

// RealignmentFilter selects the balances a run restates: an account range (blank means open), and
// a period range (both required, YYYY-MM).
type RealignmentFilter struct {
	AccountFrom string `json:"accountFrom,omitempty"`
	AccountTo   string `json:"accountTo,omitempty"`
	PeriodFrom  string `json:"periodFrom"`
	PeriodTo    string `json:"periodTo"`
}

// RealignmentMapping maps dimension attribute to old value to new value.
type RealignmentMapping map[string]map[string]string

// RealignmentRow is one affected (account, period, old combination) with its new combination.
// Net is debit minus credit of the period's balance row in the ledger currency.
type RealignmentRow struct {
	Account string            `json:"account"`
	Period  string            `json:"period"`
	OldDims map[string]string `json:"oldDimensions"`
	NewDims map[string]string `json:"newDimensions"`
	OldName string            `json:"oldCombination"`
	NewName string            `json:"newCombination"`
	Net     string            `json:"net"`
	// Quantities are the statistical quantities (BR-11.2) of the old combination that move with it,
	// one per unit of measure with a non-zero net.
	Quantities []RealignmentQuantity `json:"quantities,omitempty"`
	Lines      int64                 `json:"lineCount"`
	AccountID  string                `json:"-"`
	OldID      []byte                `json:"-"`
}

// RealignmentQuantity is the net statistical quantity of one unit of measure on a balance row:
// debit minus credit quantity, so a positive net moves as debit quantity on the new combination.
type RealignmentQuantity struct {
	Unit string `json:"unit"`
	Net  string `json:"net"`
}

// RealignmentCandidate is a balance row read for the filter.
type RealignmentCandidate struct {
	AccountID, Account, Period string
	CombinationID              []byte
	DisplayName                string
	Dimensions                 map[string]string
	Net                        decimal.Decimal
	Quantities                 []RealignmentQuantity
	LineCount                  int64
}

// ValidateRealignment checks the filter and mapping against the ledger's dimension attributes.
// The result maps a field to its problem; empty means valid.
func ValidateRealignment(f RealignmentFilter, m RealignmentMapping, attrs []string) map[string]string {
	problems := map[string]string{}
	if !periodOK(f.PeriodFrom) {
		problems["filter.periodFrom"] = "must be a period as YYYY-MM"
	}
	if !periodOK(f.PeriodTo) {
		problems["filter.periodTo"] = "must be a period as YYYY-MM"
	}
	if periodOK(f.PeriodFrom) && periodOK(f.PeriodTo) {
		n, err := PeriodSpan(f.PeriodFrom, f.PeriodTo)
		switch {
		case err != nil || n < 1:
			problems["filter.periodTo"] = "must not be before periodFrom"
		case n > MaxRealignmentPeriods:
			problems["filter.periodTo"] = fmt.Sprintf("a run may span at most %d periods", MaxRealignmentPeriods)
		}
	}
	if f.AccountFrom != "" && f.AccountTo != "" && f.AccountFrom > f.AccountTo {
		problems["filter.accountTo"] = "must not be before accountFrom"
	}
	known := map[string]bool{}
	for _, a := range attrs {
		known[a] = true
	}
	pairs := 0
	if len(m) == 0 {
		problems["mapping"] = "needs at least one attribute with an old to new value"
	}
	for attr, values := range m {
		if !known[attr] {
			problems["mapping."+attr] = "is not a dimension attribute of the ledger"
			continue
		}
		if len(values) == 0 {
			problems["mapping."+attr] = "needs at least one old to new value"
		}
		for from, to := range values {
			pairs++
			switch {
			case !dimValueRe.MatchString(from):
				problems["mapping."+attr] = fmt.Sprintf("old value %q is not a valid dimension value", from)
			case !dimValueRe.MatchString(to):
				problems["mapping."+attr] = fmt.Sprintf("new value %q is not a valid dimension value", to)
			case from == to:
				problems["mapping."+attr] = fmt.Sprintf("value %s maps to itself", from)
			}
		}
	}
	if pairs > MaxRealignmentMappings {
		problems["mapping"] = fmt.Sprintf("at most %d old to new values", MaxRealignmentMappings)
	}
	return problems
}

var periodRe = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

func periodOK(p string) bool { return periodRe.MatchString(p) }

// PeriodSpan is the number of months from a to b inclusive (both YYYY-MM).
func PeriodSpan(a, b string) (int, error) {
	from, err := time.Parse("2006-01", a)
	if err != nil {
		return 0, err
	}
	to, err := time.Parse("2006-01", b)
	if err != nil {
		return 0, err
	}
	return (to.Year()-from.Year())*12 + int(to.Month()-from.Month()) + 1, nil
}

// PeriodEnd is the last day of a YYYY-MM period.
func PeriodEnd(period string) (time.Time, error) {
	start, err := time.Parse("2006-01", period)
	if err != nil {
		return time.Time{}, err
	}
	return start.AddDate(0, 1, -1), nil
}

// ApplyMapping returns the dimensions after the mapping and whether any value changed.
func (m RealignmentMapping) Apply(dims map[string]string) (map[string]string, bool) {
	out := make(map[string]string, len(dims))
	changed := false
	for k, v := range dims {
		out[k] = v
	}
	for attr, values := range m {
		if to, ok := values[dims[attr]]; ok && dims[attr] != "" {
			out[attr] = to
			changed = true
		}
	}
	return out, changed
}

// PlanRealignment turns balance rows into the rows of a run: only combinations the mapping changes
// and whose net is not zero. Rows are ordered by period, account and old combination.
func PlanRealignment(m RealignmentMapping, candidates []RealignmentCandidate, combo func(account string, dims map[string]string) string) []RealignmentRow {
	var rows []RealignmentRow
	for _, c := range candidates {
		newDims, changed := m.Apply(c.Dimensions)
		if !changed || (c.Net.IsZero() && len(c.Quantities) == 0) {
			continue
		}
		rows = append(rows, RealignmentRow{Account: c.Account, AccountID: c.AccountID, Period: c.Period, OldDims: c.Dimensions, NewDims: newDims,
			OldID: c.CombinationID, OldName: c.DisplayName, NewName: combo(c.Account, newDims), Net: c.Net.String(),
			Quantities: c.Quantities, Lines: c.LineCount})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Period != b.Period {
			return a.Period < b.Period
		}
		if a.Account != b.Account {
			return a.Account < b.Account
		}
		return a.OldName < b.OldName
	})
	return rows
}

// RealignmentLines builds the reclassification lines of one period: each row credits (or debits, for
// a credit balance) the old combination by its net and does the opposite on the new one, so the
// entry balances by construction and the old combination's period balance returns to zero. Lines
// come in pairs, the old side then the new side.
func RealignmentLines(rows []RealignmentRow, currency, text string) []LineInput {
	var lines []LineInput
	for _, r := range rows {
		lines = append(lines, RealignmentRowLines(r, currency, text)...)
	}
	return lines
}

// RealignmentRowLines builds the pairs (old side, new side) of one row: one for the amount when the
// net is not zero, then one zero-amount statistical pair per unit of measure whose net quantity
// is not zero (BR-11.2), so the quantity totals follow the amounts to the new combination.
func RealignmentRowLines(r RealignmentRow, currency, text string) []LineInput {
	var lines []LineInput
	pair := func(net, qty decimal.Decimal, unit string) {
		oldSide, newSide := SideCredit, SideDebit
		if net.IsNegative() {
			oldSide, newSide = SideDebit, SideCredit
		}
		amount, quantity := net.Abs(), decimal.Zero
		if unit != "" {
			amount, quantity = decimal.Zero, qty.Abs()
		}
		lines = append(lines,
			LineInput{Account: r.Account, Dimensions: r.OldDims, Side: oldSide, Amount: amount, Currency: currency, Text: text, Quantity: quantity, Unit: unit},
			LineInput{Account: r.Account, Dimensions: r.NewDims, Side: newSide, Amount: amount, Currency: currency, Text: text, Quantity: quantity, Unit: unit})
	}
	if net := rowNet(r); !net.IsZero() {
		pair(net, decimal.Zero, "")
	}
	for _, q := range r.Quantities {
		n := decimal.RequireFromString(q.Net)
		pair(n, n, q.Unit)
	}
	return lines
}

// RealignmentRowLineCount is the number of lines RealignmentRowLines builds for a row.
func RealignmentRowLineCount(r RealignmentRow) int {
	n := 2 * len(r.Quantities)
	if !rowNet(r).IsZero() {
		n += 2
	}
	return n
}

// rowNet is the amount net of a row; an empty net is zero.
func rowNet(r RealignmentRow) decimal.Decimal {
	if r.Net == "" {
		return decimal.Zero
	}
	return decimal.RequireFromString(r.Net)
}

// RealignmentTotals sums the absolute net of rows and counts their lines.
func RealignmentTotals(rows []RealignmentRow) (moved decimal.Decimal, lines int64) {
	moved = decimal.Zero
	for _, r := range rows {
		moved = moved.Add(decimal.RequireFromString(r.Net).Abs())
		lines += r.Lines
	}
	return moved, lines
}

// CheckRealignmentReason applies the shared reason code rules (FR-11.15) to the reason of a
// realignment post or reverse: the code is required, must exist, be active and apply to ledger, and
// a code that requires a comment needs one. reason is nil when the code does not exist.
func CheckRealignmentReason(reason *ReasonInfo, code, comment string) []Violation {
	if code == "" {
		return []Violation{{Code: "LEDGER_REASON_REQUIRED", Field: "reasonCode", Message: "a realignment needs a reason code"}}
	}
	if reason == nil || !reason.Active {
		return []Violation{{Code: "LEDGER_REASON_REQUIRED", Field: "reasonCode", Message: fmt.Sprintf("reason code %s is unknown or inactive", code)}}
	}
	var v []Violation
	if !slices.Contains(reason.AppliesTo, ModuleLedger) {
		v = append(v, Violation{Code: "LEDGER_REASON_INVALID_MODULE", Field: "reasonCode", Message: fmt.Sprintf("reason code %s does not apply to ledger", code)})
	}
	if reason.RequiresComment && strings.TrimSpace(comment) == "" {
		v = append(v, Violation{Code: "LEDGER_REASON_REQUIRED", Field: "reasonComment", Message: fmt.Sprintf("reason code %s requires a comment", code)})
	}
	return v
}

// MaxRealignmentRowsPerEntry keeps a reclassification entry within MaxLinesPerEntry: each row
// produces two lines when it carries no statistical quantity.
const MaxRealignmentRowsPerEntry = MaxLinesPerEntry / 2

// ChunkRealignmentRows splits one period's rows into the rows of successive entries of at most
// size rows each and at most MaxLinesPerEntry lines each (a row with statistical quantities makes
// more than two lines), so a period with many combinations posts as several entries instead of
// failing the line limit.
func ChunkRealignmentRows(rows []RealignmentRow, size int) [][]RealignmentRow {
	if size < 1 {
		size = MaxRealignmentRowsPerEntry
	}
	var out [][]RealignmentRow
	var cur []RealignmentRow
	lines := 0
	for _, r := range rows {
		n := RealignmentRowLineCount(r)
		if len(cur) > 0 && (len(cur) >= size || lines+n > MaxLinesPerEntry) {
			out = append(out, cur)
			cur, lines = nil, 0
		}
		cur = append(cur, r)
		lines += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
