package domain

import (
	"github.com/shopspring/decimal"
)

// profitCenterCost prices an internal transfer between two profit centers of
// one legal entity in the ProfitCenter view (FR-39.17). The transfer-price
// table entry (TransferPrice) wins; otherwise the supplying profit center's
// cost (LegalCost) is marked up by CostPlusPct percent and rounded to the unit
// scale. With neither, the item fails with CST-0762. ok is true when a price
// was found.
func profitCenterCost(it Item) (cost decimal.Decimal, ok bool, diag *Diagnostic) {
	if it.TransferPrice != nil {
		return *it.TransferPrice, true, nil
	}
	if it.LegalCost != nil && it.CostPlusPct != nil {
		hundred := decimal.NewFromInt(100)
		return it.LegalCost.Mul(hundred.Add(*it.CostPlusPct)).Div(hundred).Round(UnitScale), true, nil
	}
	d := errDiag(CodeNoTransferPrice, "no transfer price and no cost-plus basis for profit-center item "+it.ID, it.ID)
	return decimal.Zero, false, &d
}
