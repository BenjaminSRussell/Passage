package domain

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// Value report and tie-out refusal codes (FR-20.4). CST-REC-001 is the PRD's
// daily reconciliation difference, raised by the reconcile job (a later slice),
// so the report's own refusals use the CST-RPT family.
const (
	CodeReportGroupBy   = "CST-RPT-001"
	CodeTieOutDimension = "CST-RPT-002"
	CodeTieOutDuplicate = "CST-RPT-003"
	CodeReportCurrency  = "CST-RPT-004"
)

// Cost transaction kinds (3.20.4).
const (
	KindPhysical      = "PHYSICAL"
	KindFinancial     = "FINANCIAL"
	KindInvoiceDiff   = "INVOICE_DIFF"
	KindSettlementAdj = "SETTLEMENT_ADJ"
	KindPPV           = "PPV"
	KindRevaluation   = "REVALUATION"
	KindManualAdj     = "MANUAL_ADJ"
	KindRecalc        = "RECALC"
)

var costKinds = map[string]bool{KindPhysical: true, KindFinancial: true, KindInvoiceDiff: true, KindSettlementAdj: true,
	KindPPV: true, KindRevaluation: true, KindManualAdj: true, KindRecalc: true}

// ValidCostKind reports whether k is a cost transaction kind.
func ValidCostKind(k string) bool { return costKinds[k] }

// CarriesQuantity reports whether a kind may move quantity. Physical and
// financial postings move stock; PPV may carry the received quantity for the PPV
// analysis. Every other kind is value-only ("qty 0 for value-only", 3.20.4).
func CarriesQuantity(k string) bool { return k == KindPhysical || k == KindFinancial || k == KindPPV }

// CountsInInventory reports whether a kind changes the inventory value. PPV is
// posted to the PPV account, not to stock: a STANDARD item received at 10.50
// against a standard of 10.00 stays at 1,000.00 (FR-20.1, AC-20.4).
func CountsInInventory(k string) bool { return costKinds[k] && k != KindPPV }

// reportDimensions are the keys the inventory value report can group by
// (FR-20.4 by product, site and warehouse; FR-20.16 by valuation type). The
// valuation ledger is a selector of the report, not a grouping (FR-20.15).
var reportDimensions = map[string]bool{"product": true, "site": true, "warehouse": true, "valuationType": true}

// Balance is the on-hand quantity and value at the finest report grain:
// product x site x warehouse x valuation type, for one valuation ledger.
// PhysicalValue sums PHYSICAL rows (received or shipped, not yet invoiced);
// FinancialValue sums every other inventory kind. PPV is never included.
type Balance struct {
	ProductID      string
	SiteID         string
	WarehouseID    string
	ValuationType  string
	Currency       string
	Qty            decimal.Decimal
	PhysicalValue  decimal.Decimal
	FinancialValue decimal.Decimal
}

func (b Balance) dims() map[string]string {
	return map[string]string{"product": b.ProductID, "site": b.SiteID, "warehouse": b.WarehouseID, "valuationType": b.ValuationType}
}

// Amounts is quantity and value, split into physical and financial value.
type Amounts struct {
	Qty            decimal.Decimal
	PhysicalValue  decimal.Decimal
	FinancialValue decimal.Decimal
}

// Value is the inventory value: physical plus financial.
func (a Amounts) Value() decimal.Decimal { return a.PhysicalValue.Add(a.FinancialValue) }

func (a *Amounts) add(b Balance) {
	a.Qty = a.Qty.Add(b.Qty)
	a.PhysicalValue = a.PhysicalValue.Add(b.PhysicalValue)
	a.FinancialValue = a.FinancialValue.Add(b.FinancialValue)
}

func zeroAmounts() Amounts {
	return Amounts{Qty: decimal.Zero, PhysicalValue: decimal.Zero, FinancialValue: decimal.Zero}
}

// ReportRow is one group of the value report.
type ReportRow struct {
	Key map[string]string
	Amounts
}

// ValueReport is the inventory value by the requested grouping, with totals.
// Currency is the one currency of the balances ("" when there are none).
type ValueReport struct {
	GroupBy  []string
	Currency string
	Rows     []ReportRow
	Total    Amounts
}

// ValidateGroupBy accepts a set of distinct report dimensions. An empty set
// means one total row.
func ValidateGroupBy(groupBy []string) error {
	seen := map[string]bool{}
	for _, g := range groupBy {
		if !reportDimensions[g] {
			return &Error{CodeReportGroupBy, fmt.Sprintf("%q is not a report dimension (product, site, warehouse, valuationType)", g)}
		}
		if seen[g] {
			return &Error{CodeReportGroupBy, fmt.Sprintf("dimension %q is listed twice", g)}
		}
		seen[g] = true
	}
	return nil
}

// groupKey is the canonical key of a group: the dimension values in groupBy
// order, as a JSON array, so that no two value lists can collide.
func groupKey(groupBy []string, dims map[string]string) string {
	vals := make([]string, 0, len(groupBy))
	for _, g := range groupBy {
		vals = append(vals, dims[g])
	}
	raw, _ := json.Marshal(vals) // a []string always marshals
	return string(raw)
}

func pick(groupBy []string, dims map[string]string) map[string]string {
	out := make(map[string]string, len(groupBy))
	for _, g := range groupBy {
		out[g] = dims[g]
	}
	return out
}

// BuildValueReport groups balances by groupBy and totals them. Sums are exact
// decimals and do not depend on the input order; rows come back sorted by key.
// Balances in more than one currency cannot be added and are refused
// (CST-RPT-004): one valuation ledger has one currency.
func BuildValueReport(bal []Balance, groupBy []string) (ValueReport, error) {
	if err := ValidateGroupBy(groupBy); err != nil {
		return ValueReport{}, err
	}
	rep := ValueReport{GroupBy: append([]string{}, groupBy...), Total: zeroAmounts(), Rows: []ReportRow{}}
	rows := map[string]*ReportRow{}
	for _, b := range bal {
		if rep.Currency == "" {
			rep.Currency = b.Currency
		} else if b.Currency != rep.Currency {
			return ValueReport{}, &Error{CodeReportCurrency, fmt.Sprintf("the valuation ledger holds cost transactions in %s and %s", rep.Currency, b.Currency)}
		}
		dims := b.dims()
		key := groupKey(groupBy, dims)
		row, ok := rows[key]
		if !ok {
			row = &ReportRow{Key: pick(groupBy, dims), Amounts: zeroAmounts()}
			rows[key] = row
		}
		row.add(b)
		rep.Total.add(b)
	}
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rep.Rows = append(rep.Rows, *rows[k])
	}
	return rep, nil
}

// GLBalance is the ledger balance of the inventory account(s) for one group,
// keyed by exactly the report's grouping dimensions.
type GLBalance struct {
	Dims    map[string]string
	Balance decimal.Decimal
}

// TieRow compares the inventory value of one group with its ledger balance.
type TieRow struct {
	Key       map[string]string
	Inventory decimal.Decimal
	Ledger    decimal.Decimal
	Diff      decimal.Decimal
}

// TieOut is the inventory-to-GL reconciliation of a value report (FR-20.4).
// TotalDiff is the net difference; Differences counts the groups that differ.
type TieOut struct {
	Rows        []TieRow
	Differences int
	TotalDiff   decimal.Decimal
}

// Ties reports whether every group agrees with the ledger.
func (t TieOut) Ties() bool { return t.Differences == 0 }

// TieOutReport compares a value report with ledger balances grouped the same
// way. Each balance must name every grouping dimension and no other
// (CST-RPT-002), and at most once (CST-RPT-003). A group on one side only is
// compared with zero, so an unexplained ledger balance shows as a difference
// and is never dropped.
func TieOutReport(rep ValueReport, gl []GLBalance) (TieOut, error) {
	groupBy := rep.GroupBy
	ledger := make(map[string]GLBalance, len(gl))
	for i, g := range gl {
		if len(g.Dims) != len(groupBy) {
			return TieOut{}, &Error{CodeTieOutDimension, fmt.Sprintf("ledger balance %d must name exactly the grouping dimensions %v", i, groupBy)}
		}
		for _, dim := range groupBy {
			if _, ok := g.Dims[dim]; !ok {
				return TieOut{}, &Error{CodeTieOutDimension, fmt.Sprintf("ledger balance %d must name exactly the grouping dimensions %v", i, groupBy)}
			}
		}
		key := groupKey(groupBy, g.Dims)
		if _, dup := ledger[key]; dup {
			return TieOut{}, &Error{CodeTieOutDuplicate, fmt.Sprintf("ledger balance %d names the same dimension values as an earlier one", i)}
		}
		ledger[key] = g
	}
	inv := make(map[string]ReportRow, len(rep.Rows))
	keys := make([]string, 0, len(rep.Rows)+len(gl))
	for _, r := range rep.Rows {
		k := groupKey(groupBy, r.Key)
		inv[k] = r
		keys = append(keys, k)
	}
	for k := range ledger {
		if _, ok := inv[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := TieOut{Rows: []TieRow{}, TotalDiff: decimal.Zero}
	for _, k := range keys {
		row := TieRow{Inventory: decimal.Zero, Ledger: decimal.Zero}
		if r, ok := inv[k]; ok {
			row.Key, row.Inventory = r.Key, r.Value()
		}
		if g, ok := ledger[k]; ok {
			if row.Key == nil {
				row.Key = pick(groupBy, g.Dims)
			}
			row.Ledger = g.Balance
		}
		row.Diff = row.Inventory.Sub(row.Ledger)
		if !row.Diff.IsZero() {
			out.Differences++
		}
		out.TotalDiff = out.TotalDiff.Add(row.Diff)
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}
