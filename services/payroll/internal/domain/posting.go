package domain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Payroll GL account roles of the BR-43.1 posting example. The posting profile
// that maps a role, earning/deduction/tax code and position dimension to a
// ledger account (FR-43.9) is not built yet; these names stand for the roles.
// Per-code accounts are AccountPayable(code) for a deduction, its match and a
// state withholding, and AccountMatchExpense(code) for an employer match.
const (
	AccountSalaryExpense      = "PAYROLL_SALARY_EXPENSE"
	AccountEmployerTaxExpense = "PAYROLL_EMPLOYER_TAX_EXPENSE"
	AccountNetPayClearing     = "PAYROLL_NET_PAY_CLEARING"
	AccountFederalWHPayable   = "PAYROLL_FED_WH_PAYABLE"
	AccountFICAPayable        = "PAYROLL_FICA_PAYABLE"
	AccountFUTAPayable        = "PAYROLL_FUTA_PAYABLE"
)

// AccountPayable is the liability role for a deduction code (401K, MED125, a
// post-tax code) or a state withholding line code (STATE_CO_WH).
func AccountPayable(code string) string { return "PAYROLL_" + code + "_PAYABLE" }

// AccountMatchExpense is the expense role for an employer match on code.
func AccountMatchExpense(code string) string { return "PAYROLL_" + code + "_MATCH_EXPENSE" }

// ErrUnbalancedPosting means a payroll journal does not net to zero. It is a
// programming error.
var ErrUnbalancedPosting = errors.New("payroll posting: journal lines do not balance")

// JournalLine is one summarized account amount: debits positive, credits negative.
type JournalLine struct {
	Account string          `json:"account"`
	Amount  decimal.Decimal `json:"amount"`
}

// RunPosting summarizes the statements of a run into one balanced journal by
// account role (FR-43.9 at SUMMARY granularity, BR-43.1 GL example):
//
//	Dr salary expense          Σ earnings (gross)
//	Dr employer tax expense    Σ SS_ER + MED_ER + FUTA
//	Dr <code> match expense    Σ MATCH_<code>
//	Cr net pay clearing        Σ net
//	Cr <code> payable          Σ pre-tax and post-tax deductions + MATCH_<code>
//	Cr federal WH payable      Σ FED_WH
//	Cr FICA payable            Σ SS_EE + MED_EE + SS_ER + MED_ER
//	Cr FUTA payable            Σ FUTA
//	Cr <state WH> payable      Σ STATE_<st>_WH
//
// Debits equal gross plus employer cost. Accounts appear in first-use order and
// a line code the matrix does not know is an error, never a guess.
func RunPosting(statements []Statement) ([]JournalLine, error) {
	var order []string
	sums := map[string]decimal.Decimal{}
	post := func(account string, amount decimal.Decimal) {
		if _, ok := sums[account]; !ok {
			order = append(order, account)
			sums[account] = decimal.Zero
		}
		sums[account] = sums[account].Add(amount)
	}
	for _, st := range statements {
		for _, l := range st.Lines {
			switch {
			case l.Kind == KindEarning:
				post(AccountSalaryExpense, l.Amount)
			case l.Kind == KindPreTax || l.Kind == KindPostTax:
				post(AccountPayable(l.Code), l.Amount.Neg())
			case l.Kind == KindTax && l.Code == "FED_WH":
				post(AccountFederalWHPayable, l.Amount.Neg())
			case l.Kind == KindTax && (l.Code == "SS_EE" || l.Code == "MED_EE"):
				post(AccountFICAPayable, l.Amount.Neg())
			case l.Kind == KindTax && strings.HasPrefix(l.Code, "STATE_"):
				post(AccountPayable(l.Code), l.Amount.Neg())
			case l.Kind == KindEmployer && (l.Code == "SS_ER" || l.Code == "MED_ER"):
				post(AccountEmployerTaxExpense, l.Amount)
				post(AccountFICAPayable, l.Amount.Neg())
			case l.Kind == KindEmployer && l.Code == "FUTA":
				post(AccountEmployerTaxExpense, l.Amount)
				post(AccountFUTAPayable, l.Amount.Neg())
			case l.Kind == KindEmployer && strings.HasPrefix(l.Code, "MATCH_"):
				code := strings.TrimPrefix(l.Code, "MATCH_")
				post(AccountMatchExpense(code), l.Amount)
				post(AccountPayable(code), l.Amount.Neg())
			default:
				return nil, fmt.Errorf("payroll posting: no account for %s line %s", l.Kind, l.Code)
			}
		}
		post(AccountNetPayClearing, st.Net.Neg())
	}
	out := make([]JournalLine, 0, len(order))
	total := decimal.Zero
	for _, a := range order {
		total = total.Add(sums[a])
		out = append(out, JournalLine{Account: a, Amount: sums[a]})
	}
	if !total.IsZero() {
		return nil, ErrUnbalancedPosting
	}
	return out, nil
}
