package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	CodeGLFailed    = "PROJ-GL-001"
	CodeNoEstimate  = "PROJ-GL-002"
	CodePeriodOrder = "PROJ-GL-003"
	GLKindTxn       = "TXN"
	GLKindWIP       = "WIP"
)

// GLLine is one line of a stored ledger entry; Amount is a decimal string in the currency's minor unit.
type GLLine struct {
	Account string `json:"account"`
	Credit  bool   `json:"credit"`
	Amount  string `json:"amount"`
}

// movement books amount as a debit to debit and a credit to credit; a negative amount reverses the sides.
func movement(debit, credit string, amount decimal.Decimal, currency string) []GLLine {
	mu := money.MinorUnits(currency)
	amount = amount.RoundBank(mu)
	if amount.IsZero() {
		return nil
	}
	if amount.IsNegative() {
		debit, credit = credit, debit
	}
	a := amount.Abs().StringFixed(mu)
	return []GLLine{{Account: debit, Amount: a}, {Account: credit, Credit: true, Amount: a}}
}

// TransactionLines books the cost of a project transaction into work in progress.
func TransactionLines(p PostingProfile, cost decimal.Decimal, currency string) []GLLine {
	return movement(p.WIPAccount, p.CostAccount, cost, currency)
}

// EstimateLines books the movement of a project's estimate position since the prior booked period: revenue earned
// moves to the accrued revenue account, the amount billed moves from it to the on-account account (so the accrued
// account carries earned minus billed: accrued when positive, deferred when negative), and a change of the loss
// provision writes work in progress down against the cost account.
func EstimateLines(p PostingProfile, revenue, billed, provision decimal.Decimal, currency string) []GLLine {
	lines := movement(p.AccruedRevenueAccount, p.InvoicedRevenueAccount, revenue, currency)
	lines = append(lines, movement(p.OnAccountAccount, p.AccruedRevenueAccount, billed, currency)...)
	return append(lines, movement(p.CostAccount, p.WIPAccount, provision, currency)...)
}
