package domain

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SchemaVersion is the JSON schema version of exported definitions (FR-15.11).
const SchemaVersion = "1"

// Limits from FR-15.2 and FR-15.3.
const (
	MaxRows    = 2000
	MaxColumns = 60
)

// ShapeCode is the generic validation code for malformed fields.
const ShapeCode = "COM-0001"

type Kind string

const (
	KindTrialBalance    Kind = "trial_balance"
	KindBalanceSheet    Kind = "balance_sheet"
	KindIncomeStatement Kind = "income_statement"
	KindCashFlow        Kind = "cash_flow"
	KindCustom          Kind = "custom"
)

type RowKind string

const (
	RowHeader          RowKind = "header"
	RowAccountRange    RowKind = "account_range"
	RowDimensionFilter RowKind = "dimension_filter"
	RowTotal           RowKind = "total"
	RowCalculation     RowKind = "calculation"
)

var (
	codePattern  = regexp.MustCompile(`^[A-Z][A-Z0-9-]{1,29}$`)
	validKinds   = map[Kind]bool{KindTrialBalance: true, KindBalanceSheet: true, KindIncomeStatement: true, KindCashFlow: true, KindCustom: true}
	validRowKind = map[RowKind]bool{RowHeader: true, RowAccountRange: true, RowDimensionFilter: true, RowTotal: true, RowCalculation: true}
	cashFlowTags = map[string]bool{"": true, "operating_adjustment": true, "working_capital": true, "investing": true, "financing": true, "cash": true}
)

// Format is the presentation of a row (PRD §2.15.4).
type Format struct {
	Bold      bool `json:"bold,omitempty"`
	Indent    int  `json:"indent,omitempty"`
	PrintZero bool `json:"printZero,omitempty"`
	PageBreak bool `json:"pageBreak,omitempty"`
}

// Row is one line of a statement. Code is the identifier other rows use in
// totals and calculations.
type Row struct {
	Code            string              `json:"code"`
	Kind            RowKind             `json:"kind"`
	Label           string              `json:"label"`
	AccountFrom     string              `json:"accountFrom,omitempty"`
	AccountTo       string              `json:"accountTo,omitempty"`
	DimensionFilter map[string][]string `json:"dimensionFilter,omitempty"`
	References      []string            `json:"references,omitempty"`
	Expression      string              `json:"expression,omitempty"`
	NormalBalance   string              `json:"normalBalance"`
	CashFlowTag     string              `json:"cashFlowTag,omitempty"`
	Format          Format              `json:"format"`
}

// Column is one period column. Columns with CALC take their values from other
// columns of the same row through Expression. An empty CurrencyType uses the
// currency type of the run.
type Column struct {
	Code         string `json:"code"`
	Label        string `json:"label"`
	PeriodExpr   string `json:"periodExpr"`
	CurrencyType string `json:"currencyType,omitempty"`
	Expression   string `json:"calcExpression,omitempty"`
}

// Definition is a statement design: the rows, the columns and the presentation
// flags. Status and Version are set by the store when published.
type Definition struct {
	SchemaVersion            string   `json:"schemaVersion"`
	Code                     string   `json:"code"`
	Name                     string   `json:"name"`
	Version                  int      `json:"version"`
	Status                   string   `json:"status"`
	Kind                     Kind     `json:"kind"`
	IncludeAdjustmentPeriods bool     `json:"includeAdjustmentPeriods"`
	Rows                     []Row    `json:"rows"`
	Columns                  []Column `json:"columns"`
}

// Issue is one validation finding: the field, its catalog code and the text.
type Issue struct {
	Field  string
	Code   string
	Detail string
}

// Validate checks shape, limits, expression syntax and references, and cycles.
// An empty result means the definition may be saved and published.
func (d Definition) Validate() []Issue {
	var out []Issue
	add := func(field, code, detail string) { out = append(out, Issue{Field: field, Code: code, Detail: detail}) }
	if !codePattern.MatchString(d.Code) {
		add("code", ShapeCode, "must be 2 to 30 characters: upper-case letters, digits or hyphens, starting with a letter")
	}
	if n := utf8.RuneCountInString(d.Name); n < 1 || n > 120 {
		add("name", ShapeCode, "must be 1 to 120 characters")
	}
	if !validKinds[d.Kind] {
		add("kind", ShapeCode, "must be trial_balance, balance_sheet, income_statement, cash_flow or custom")
	}
	if len(d.Rows) == 0 || len(d.Rows) > MaxRows {
		add("rows", CodeTooLarge, "must contain 1 to 2000 rows")
	}
	if len(d.Columns) == 0 || len(d.Columns) > MaxColumns {
		add("columns", CodeTooLarge, "must contain 1 to 60 columns")
	}
	rowKinds := map[string]RowKind{}
	edges := map[string][]string{}
	for i, r := range d.Rows {
		field := "rows[" + strconv.Itoa(i) + "]"
		if !ValidCode(r.Code) {
			add(field+".code", ShapeCode, "must start with a letter and use letters, digits or underscores")
			continue
		}
		if _, dup := rowKinds[r.Code]; dup {
			add(field+".code", ShapeCode, "row code "+r.Code+" is used twice")
		}
		rowKinds[r.Code] = r.Kind
		if !validRowKind[r.Kind] {
			add(field+".kind", ShapeCode, "unknown row kind")
		}
		if strings.TrimSpace(r.Label) == "" {
			add(field+".label", ShapeCode, "label is required")
		}
		if r.NormalBalance != "debit" && r.NormalBalance != "credit" {
			add(field+".normalBalance", ShapeCode, "must be debit or credit")
		}
		if !cashFlowTags[r.CashFlowTag] {
			add(field+".cashFlowTag", ShapeCode, "unknown cash flow tag")
		}
		switch r.Kind {
		case RowAccountRange:
			if r.AccountFrom == "" || r.AccountTo == "" || r.AccountFrom > r.AccountTo {
				add(field, ShapeCode, "account_range needs accountFrom <= accountTo")
			}
		case RowDimensionFilter:
			if len(r.DimensionFilter) == 0 {
				add(field, ShapeCode, "dimension_filter needs at least one dimension")
			}
		case RowTotal:
			if len(r.References) == 0 {
				add(field, ShapeCode, "total needs references")
			}
			edges[r.Code] = r.References
		case RowCalculation:
			_, expr, err := ParseRowExpr(r.Expression)
			if err != nil {
				add(field+".expression", CodeExpression, err.Error())
				break
			}
			edges[r.Code] = expr.Refs()
		}
	}
	for _, r := range d.Rows {
		for _, ref := range edges[r.Code] {
			kind, ok := rowKinds[ref]
			if !ok {
				add("rows."+r.Code, CodeExpression, "reference "+ref+" is not a row of this definition")
			} else if r.Kind == RowTotal && kind == RowCalculation {
				add("rows."+r.Code, CodeExpression, "total may not sum calculation row "+ref)
			}
		}
	}
	if hasCycle(edges) {
		add("rows", CodeExpression, "rows reference each other in a cycle")
	}
	out = append(out, validateColumns(d.Columns)...)
	return out
}

func validateColumns(cols []Column) []Issue {
	var out []Issue
	add := func(field, code, detail string) { out = append(out, Issue{Field: field, Code: code, Detail: detail}) }
	colCodes := map[string]bool{}
	for _, c := range cols {
		colCodes[c.Code] = true
	}
	edges := map[string][]string{}
	seen := map[string]bool{}
	for i, c := range cols {
		field := "columns[" + strconv.Itoa(i) + "]"
		if !ValidCode(c.Code) || seen[c.Code] {
			add(field+".code", ShapeCode, "column code is missing, invalid or repeated")
		}
		seen[c.Code] = true
		if strings.TrimSpace(c.Label) == "" {
			add(field+".label", ShapeCode, "label is required")
		}
		if !ValidPeriodExpr(c.PeriodExpr) {
			add(field+".periodExpr", CodeExpression, "period expression "+c.PeriodExpr+" is not supported")
		}
		if c.CurrencyType != "" && c.CurrencyType != "acct" && c.CurrencyType != "rpt" && c.CurrencyType != "txn" {
			add(field+".currencyType", ShapeCode, "must be acct, rpt or txn, or empty for the run's currency type")
		}
		if c.PeriodExpr != "CALC" {
			if c.Expression != "" {
				add(field+".calcExpression", ShapeCode, "only CALC columns take a calculation")
			}
			continue
		}
		expr, err := ParseExpr(c.Expression)
		if err != nil {
			add(field+".calcExpression", CodeExpression, err.Error())
			continue
		}
		edges[c.Code] = expr.Refs()
		for _, ref := range expr.Refs() {
			if !colCodes[ref] {
				add(field+".calcExpression", CodeExpression, "reference "+ref+" is not a column of this definition")
			}
		}
	}
	if hasCycle(edges) {
		add("columns", CodeExpression, "columns reference each other in a cycle")
	}
	return out
}

// hasCycle reports whether the reference graph has a cycle (depth-first search).
func hasCycle(edges map[string][]string) bool {
	const (
		unvisited = iota
		active
		done
	)
	state := map[string]int{}
	var visit func(n string) bool
	visit = func(n string) bool {
		switch state[n] {
		case active:
			return true
		case done:
			return false
		}
		state[n] = active
		for _, m := range edges[n] {
			if visit(m) {
				return true
			}
		}
		state[n] = done
		return false
	}
	keys := make([]string, 0, len(edges))
	for k := range edges {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if visit(k) {
			return true
		}
	}
	return false
}

// Marshal writes the definition as schema-versioned JSON (FR-15.11).
func (d Definition) Marshal() ([]byte, error) {
	d.SchemaVersion = SchemaVersion
	return json.MarshalIndent(d, "", "  ")
}

// Parse reads an exported definition. Unknown fields and unsupported schema
// versions are rejected with REPORT-0012 for the version (BR-15.11, FR-15.11).
func Parse(b []byte) (Definition, error) {
	var d Definition
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Definition{}, &Error{Code: ShapeCode, Detail: "malformed definition JSON: " + err.Error()}
	}
	if d.SchemaVersion != SchemaVersion {
		return Definition{}, errf(CodeImportSchema, "schema version %q is not supported (supported: %s)", d.SchemaVersion, SchemaVersion)
	}
	return d, nil
}

// CurrencyTypes lists the distinct currency types the definition's columns read,
// with run standing in for columns that name none.
func (d Definition) CurrencyTypes(run string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range d.Columns {
		if c.PeriodExpr == "CALC" {
			continue
		}
		ct := c.CurrencyType
		if ct == "" {
			ct = run
		}
		if !seen[ct] {
			seen[ct] = true
			out = append(out, ct)
		}
	}
	sort.Strings(out)
	return out
}
