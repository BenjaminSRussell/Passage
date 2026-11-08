package domain

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// CodeConsolidation is REPORT-0042: the run cannot be consolidated by entity
	// (definition kind, currency type, ledger list, column limit or an
	// unsupported combination with a tree or hierarchy).
	CodeConsolidation = "REPORT-0042"
	// MaxConsolidationLedgers bounds the ledgers of one consolidated run, so
	// that columns times (ledgers + 1) stays within MaxColumns for a definition
	// of a few columns.
	MaxConsolidationLedgers = 12
	// MaxLedgerLabel is the longest column suffix naming an entity.
	MaxLedgerLabel = 40
)

// LedgerColumn names one ledger of a consolidated run and the text that follows
// the column label of that entity's columns.
type LedgerColumn struct {
	LedgerID string `json:"ledgerId"`
	Label    string `json:"label"`
}

// Consolidate expands a definition for a consolidated-by-entity run (FR-15.5):
// every column becomes one column for the sum of all ledgers (same code, the
// label suffixed "Consolidated") followed by one column per ledger (code
// "<code>_E<n>", label "<label> - <entity label>") that reads only that
// ledger's balances. A CALC column is expanded the same way: its consolidated
// column calculates over the consolidated columns and each entity's column over
// that entity's columns (a variance per entity, and the variance of the totals).
// Rows are unchanged. The expansion is deterministic, so a stored run rebuilds
// the definition it was evaluated with from the published definition and the
// ledger list. Amounts of the ledgers are added as stored: there is no currency
// translation and no intercompany elimination, which is why the run requires
// the reporting currency type and why cash flow definitions are refused.
func Consolidate(d Definition, ledgers []LedgerColumn) (Definition, error) {
	if d.Kind == KindCashFlow {
		return d, errf(CodeConsolidation, "a cash flow definition cannot be consolidated by entity; its net income and cash reconcile per ledger.")
	}
	if n := len(ledgers); n < 2 || n > MaxConsolidationLedgers {
		return d, errf(CodeConsolidation, "a consolidated run needs 2 to %d ledgers.", MaxConsolidationLedgers)
	}
	seen := map[string]bool{}
	for _, l := range ledgers {
		if l.LedgerID == "" || seen[l.LedgerID] {
			return d, errf(CodeConsolidation, "ledger %q is empty or listed twice.", l.LedgerID)
		}
		seen[l.LedgerID] = true
	}
	if len(d.Columns)*(len(ledgers)+1) > MaxColumns {
		return d, errf(CodeConsolidation, "%d columns for %d ledgers and the total exceed the %d column limit.", len(d.Columns), len(ledgers), MaxColumns)
	}
	columns := make(map[string]bool, len(d.Columns))
	for _, c := range d.Columns {
		columns[c.Code] = true
	}
	out := d
	out.Columns = make([]Column, 0, len(d.Columns)*(len(ledgers)+1))
	for _, c := range d.Columns {
		total := c
		total.Label = c.Label + " - Consolidated"
		out.Columns = append(out.Columns, total)
		for i, l := range ledgers {
			suffix := "_E" + strconv.Itoa(i+1)
			e := c
			e.Code = c.Code + suffix
			e.Label = fmt.Sprintf("%s - %s", c.Label, l.Label)
			e.Ledger = l.LedgerID
			if c.PeriodExpr == "CALC" {
				expr, err := entityExpression(c.Expression, suffix, columns)
				if err != nil {
					return d, err
				}
				e.Expression = expr
			}
			out.Columns = append(out.Columns, e)
		}
	}
	// Column codes may contain underscores, so a definition with columns P and
	// P_E1 would expand to two columns P_E1; the cells of a run are keyed by
	// column code.
	codes := map[string]bool{}
	for _, c := range out.Columns {
		if codes[c.Code] {
			return d, errf(CodeConsolidation, "column %s of the definition collides with an entity column of the expansion; rename it to consolidate.", c.Code)
		}
		codes[c.Code] = true
	}
	return out, nil
}

// entityExpression rewrites a CALC column's expression for one entity: every
// reference to a column of the definition names that column's entity column
// (suffix "_E<n>"), so "CUR-PY" becomes "CUR_E1 - PY_E1".
func entityExpression(src, suffix string, columns map[string]bool) (string, error) {
	toks, err := lex(src)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(toks))
	for _, t := range toks {
		switch {
		case t.kind == 'e':
		case t.kind == 'i' && columns[t.text]:
			parts = append(parts, t.text+suffix)
		default:
			parts = append(parts, t.text)
		}
	}
	return strings.Join(parts, " "), nil
}
