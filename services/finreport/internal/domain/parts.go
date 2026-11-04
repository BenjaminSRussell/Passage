package domain

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Slice-local message codes for the designer parts (FR-15.2, FR-15.3); they are
// not in the PRD catalog yet.
const (
	// CodePartNotFound is REPORT-0037: the row set or column set does not exist.
	CodePartNotFound = "REPORT-0037"
	// CodePartInvalid is REPORT-0038: a row set or column set body is malformed.
	CodePartInvalid = "REPORT-0038"
)

// RowSet is a named, reusable list of rows (PRD §2.15.5 /row-sets). A
// definition takes its rows from a row set with Compose.
type RowSet struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Rows      []Row  `json:"rows"`
	UpdatedBy string `json:"updatedBy,omitempty"`
}

// ColumnSet is a named, reusable list of columns (PRD §2.15.5 /column-sets).
// Templates are column sets the service ships.
type ColumnSet struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Columns     []Column `json:"columns"`
	UpdatedBy   string   `json:"updatedBy,omitempty"`
}

// placeholderRow and placeholderColumn let the definition rules check a part
// on its own: a row set needs some column, a column set some row.
var (
	placeholderRow    = Row{Code: "PLACEHOLDER", Kind: RowAccountRange, Label: "x", AccountFrom: "0", AccountTo: "9", NormalBalance: "debit"}
	placeholderColumn = Column{Code: "PLACEHOLDER", Label: "x", PeriodExpr: "P"}
)

func partCodeIssues(code, name string) []Issue {
	var out []Issue
	if !codePattern.MatchString(code) {
		out = append(out, Issue{Field: "code", Code: CodePartInvalid, Detail: "must be 2 to 30 characters: upper-case letters, digits or hyphens, starting with a letter"})
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > 120 {
		out = append(out, Issue{Field: "name", Code: CodePartInvalid, Detail: "must be 1 to 120 characters"})
	}
	return out
}

// keep returns the issues of the definition rules whose field starts with prefix,
// under the part code.
func keep(issues []Issue, prefix string) []Issue {
	var out []Issue
	for _, is := range issues {
		if strings.HasPrefix(is.Field, prefix) {
			out = append(out, is)
		}
	}
	return out
}

// Validate applies the row rules of a definition (limits, kinds, references,
// cycles, expression syntax) to the set on its own.
func (s RowSet) Validate() []Issue {
	out := partCodeIssues(s.Code, s.Name)
	d := Definition{Code: "PLACEHOLDER", Name: "x", Kind: KindCustom, Rows: s.Rows, Columns: []Column{placeholderColumn}}
	return append(out, keep(d.Validate(), "rows")...)
}

// Validate applies the column rules of a definition (limit, period
// expressions, CALC references, cycles) to the set on its own.
func (s ColumnSet) Validate() []Issue {
	out := partCodeIssues(s.Code, s.Name)
	if utf8.RuneCountInString(s.Description) > 500 {
		out = append(out, Issue{Field: "description", Code: CodePartInvalid, Detail: "must be at most 500 characters"})
	}
	d := Definition{Code: "PLACEHOLDER", Name: "x", Kind: KindCustom, Rows: []Row{placeholderRow}, Columns: s.Columns}
	return append(out, keep(d.Validate(), "columns")...)
}

// Compose builds the draft body of a definition from a row set and a column
// set. The result is not stored; the caller saves it as a draft, where the
// whole definition is validated again. Rows and columns are copied, so later
// edits of the sets do not change definitions already saved.
func Compose(code, name string, kind Kind, adjustment bool, rows RowSet, cols ColumnSet) (Definition, []Issue) {
	d := Definition{
		SchemaVersion: SchemaVersion, Code: code, Name: name, Status: "draft", Kind: kind, IncludeAdjustmentPeriods: adjustment,
		Rows:    append([]Row(nil), rows.Rows...),
		Columns: append([]Column(nil), cols.Columns...),
	}
	return d, d.Validate()
}

// comparative builds the variance and percent columns that follow a pair of
// columns: variance is current minus comparative, percent is the variance over
// the comparative (a zero comparative renders blank, like any division by zero).
// Both work on displayed values (BR-15.3), so a growing expense row shows a
// positive variance; a comparative below zero turns the percent's sign around
// (the expression language has no ABS).
func comparative(cur, cmp, label string, n int) []Column {
	v := "C" + strconv.Itoa(n)
	return []Column{
		{Code: v, Label: label + " variance", PeriodExpr: "CALC", Expression: cur + "-" + cmp},
		{Code: "C" + strconv.Itoa(n+1), Label: label + " variance %", PeriodExpr: "CALC", Expression: "(" + cur + "-" + cmp + ")/" + cmp + "*100"},
	}
}

// ColumnTemplates are the comparative column sets the service ships (FR-15.3:
// comparative column sets are templates): actual against prior year, year to
// date against prior year to date, against the prior period, and all of them.
// Each is valid on its own; a caller saves one under its own code.
func ColumnTemplates() []ColumnSet {
	cols := func(base []Column, pairs ...[3]string) []Column {
		out := append([]Column(nil), base...)
		for _, p := range pairs {
			out = append(out, comparative(p[0], p[1], p[2], len(out)+1)...)
		}
		return out
	}
	out := []ColumnSet{
		{Code: "CMP-PY", Name: "Period against prior year", Description: "The period, the same period a year earlier, the variance and the variance percent.",
			Columns: cols([]Column{{Code: "C1", Label: "Period", PeriodExpr: "P"}, {Code: "C2", Label: "Prior year", PeriodExpr: "PY"}}, [3]string{"C1", "C2", "Period"})},
		{Code: "CMP-YTD-PY", Name: "Year to date against prior year", Description: "Year to date, prior year to date, the variance and the variance percent.",
			Columns: cols([]Column{{Code: "C1", Label: "Year to date", PeriodExpr: "YTD"}, {Code: "C2", Label: "Prior year to date", PeriodExpr: "PYTD"}}, [3]string{"C1", "C2", "YTD"})},
		{Code: "CMP-PRIOR-PERIOD", Name: "Period against prior period", Description: "The period, the period before it, the variance and the variance percent.",
			Columns: cols([]Column{{Code: "C1", Label: "Period", PeriodExpr: "P"}, {Code: "C2", Label: "Prior period", PeriodExpr: "P-1"}}, [3]string{"C1", "C2", "Period"})},
		{Code: "CMP-FULL", Name: "Period and year to date against prior year", Description: "Period and year to date, each with its prior-year column, variance and variance percent.",
			Columns: cols([]Column{
				{Code: "C1", Label: "Period", PeriodExpr: "P"}, {Code: "C2", Label: "Prior year", PeriodExpr: "PY"},
				{Code: "C3", Label: "Year to date", PeriodExpr: "YTD"}, {Code: "C4", Label: "Prior year to date", PeriodExpr: "PYTD"},
			}, [3]string{"C1", "C2", "Period"}, [3]string{"C3", "C4", "YTD"})},
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}
