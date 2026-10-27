package domain

import "github.com/shopspring/decimal"

// CashFlow is the indirect-method cash flow of BR-15.5, one amount per section.
type CashFlow struct {
	NetIncome            decimal.Decimal
	OperatingAdjustments decimal.Decimal
	WorkingCapital       decimal.Decimal
	Investing            decimal.Decimal
	Financing            decimal.Decimal
}

// NetCashFlow is the sum of the sections.
func (c CashFlow) NetCashFlow() decimal.Decimal {
	return c.NetIncome.Add(c.OperatingAdjustments).Add(c.WorkingCapital).Add(c.Investing).Add(c.Financing)
}

// WorkingCapitalMovement is the cash effect of one asset and one liability
// movement: minus the change in assets, plus the change in liabilities.
func WorkingCapitalMovement(assetDelta, liabilityDelta decimal.Decimal) decimal.Decimal {
	return liabilityDelta.Sub(assetDelta)
}

// ReconcileCashFlow enforces BR-15.5: no balance-sheet account may be
// unclassified (REPORT-0009), and the net cash flow must equal the change in
// cash-tagged balances to the cent (REPORT-0008).
func ReconcileCashFlow(cf CashFlow, deltaCash decimal.Decimal, unclassified int) error {
	if unclassified > 0 {
		return errf(CodeUnclassified, "%d balance-sheet accounts have no cash flow tag", unclassified)
	}
	net := cf.NetCashFlow()
	if !net.Equal(deltaCash) {
		return errf(CodeCashFlow, "net cash flow %s differs from change in cash %s", net.StringFixed(2), deltaCash.StringFixed(2))
	}
	return nil
}
