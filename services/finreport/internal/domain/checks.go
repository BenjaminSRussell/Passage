package domain

import (
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// SeverityWarning marks a check that is recorded with a run without failing it.
const SeverityWarning = "warning"

// Check is a finding recorded with a completed statement run. Blocking findings
// fail the run instead and are returned as errors (see CheckCashFlow).
type Check struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
}

// RunChecks returns the warnings a completed run carries. A definition with the
// PL-FUNC check row whose value is not zero gets REPORT-0015 (FR-15.14); the
// run still completes. The result is never nil.
func RunChecks(def Definition, res Result) []Check {
	out := []Check{}
	if hasRow(def, PLFuncCheckRow) {
		if err := PLFuncCheck(res); err != nil {
			out = append(out, Check{Code: err.Code, Severity: SeverityWarning, Detail: err.Detail})
		}
	}
	return out
}

func hasRow(def Definition, code string) bool {
	for _, r := range def.Rows {
		if r.Code == code {
			return true
		}
	}
	return false
}

// CheckCashFlow enforces BR-15.5 on an evaluated cash flow definition, for every
// column that sums balances. Per column:
//
//   - net income is minus the P&L movement (closing entries excluded) of the
//     column's periods, read from the balances, so it does not depend on how the
//     definition labels its rows;
//   - the sections are the displayed values of the account rows tagged
//     operating_adjustment, working_capital, investing and financing (credit-
//     normal rows show the cash effect, BR-15.3), and the change in cash is the
//     displayed value of the rows tagged cash;
//   - a balance-sheet account that moved and falls in no tagged row is
//     unclassified (REPORT-0009);
//   - the net cash flow must equal the change in cash exactly (REPORT-0008).
//
// Balance-sheet and P&L accounts are told apart by the default chart ranges
// (ASSUMPTION until MainAccountCategory exists, see standard.go). A definition
// that is not of kind cash_flow, or tags no row, passes.
func CheckCashFlow(in Input, res Result) error {
	def := in.Definition
	if def.Kind != KindCashFlow || !hasTaggedRow(def) {
		return nil
	}
	plans, err := planColumns(in)
	if err != nil {
		return err
	}
	shown := map[[2]string]Cell{}
	for _, c := range res.Cells {
		shown[[2]string{c.Row, c.Column}] = c
	}
	for j, col := range def.Columns {
		plan := plans[j]
		if plan.calc || !plan.found {
			continue
		}
		var cf CashFlow
		cash := decimal.Zero
		for _, r := range def.Rows {
			if r.Kind != RowAccountRange || r.CashFlowTag == "" {
				continue
			}
			c := shown[[2]string{r.Code, col.Code}]
			if c.Blank {
				continue
			}
			switch r.CashFlowTag {
			case tagOperating:
				cf.OperatingAdjustments = cf.OperatingAdjustments.Add(c.Value)
			case tagWorkingCap:
				cf.WorkingCapital = cf.WorkingCapital.Add(c.Value)
			case tagInvesting:
				cf.Investing = cf.Investing.Add(c.Value)
			case tagFinancing:
				cf.Financing = cf.Financing.Add(c.Value)
			case tagCash:
				cash = cash.Add(c.Value)
			}
		}
		moved := map[string]decimal.Decimal{}
		for _, b := range in.Balances {
			if b.LedgerID != in.LedgerID || b.CurrencyType != plan.currency || !plan.include[b.Period] {
				continue
			}
			move := b.Net.Sub(b.ClosingNet)
			if b.Account >= rangeRevenueFrom && b.Account <= rangeExpenseTo {
				cf.NetIncome = cf.NetIncome.Sub(move)
				continue
			}
			if b.Account < rangeRevenueFrom && !coveredByTag(def, b) {
				moved[b.Account] = moved[b.Account].Add(move)
			}
		}
		var unclassified []string
		for acct, m := range moved {
			if !m.IsZero() {
				unclassified = append(unclassified, acct)
			}
		}
		if len(unclassified) > 0 {
			sort.Strings(unclassified)
			return errf(CodeUnclassified, "%d balance-sheet accounts have no cash flow tag in column %s: %s",
				len(unclassified), col.Code, sample(unclassified, 10))
		}
		if net := cf.NetCashFlow(); !net.Equal(cash) {
			return errf(CodeCashFlow, "Net cash flow %s differs from change in cash %s in column %s.",
				net.StringFixed(2), cash.StringFixed(2), col.Code)
		}
	}
	return nil
}

func hasTaggedRow(def Definition) bool {
	for _, r := range def.Rows {
		if r.Kind == RowAccountRange && r.CashFlowTag != "" {
			return true
		}
	}
	return false
}

// coveredByTag reports whether a tagged account row picks up the balance: its
// account range and dimension filter both match.
func coveredByTag(def Definition, b Balance) bool {
	for _, r := range def.Rows {
		if r.Kind != RowAccountRange || r.CashFlowTag == "" {
			continue
		}
		if r.coversAccount(b.Account) && dimsMatch(r.DimensionFilter, b.Dims) {
			return true
		}
	}
	return false
}

// coversAccount reports whether an account_range row picks up account: inside
// its range when it has one, and on its explicit account list when it has one.
func (r Row) coversAccount(account string) bool {
	if r.AccountFrom != "" && (account < r.AccountFrom || account > r.AccountTo) {
		return false
	}
	if len(r.Accounts) == 0 {
		return true
	}
	for _, a := range r.Accounts {
		if a == account {
			return true
		}
	}
	return false
}

func sample(list []string, n int) string {
	if len(list) <= n {
		return strings.Join(list, ", ")
	}
	return strings.Join(list[:n], ", ") + ", …"
}
