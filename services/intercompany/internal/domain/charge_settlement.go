package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Transfer price methods (ic.transferPrices).
const (
	TPCost     = "Cost"
	TPCostPlus = "CostPlus"
	TPTable    = "Table"
)

// TransferPrice is an effective-dated intercompany transfer price for one lender,
// borrower, charge category and resource role (FR-40.13, AX intercompany transfer prices).
type TransferPrice struct {
	Lender        string
	Borrower      string
	Category      string
	Role          string
	Method        string
	MarkupPct     decimal.Decimal
	UnitPrice     decimal.Decimal // used by Table
	Currency      string
	EffectiveFrom time.Time
}

// ResolveTransferPrice returns the price effective on the source transaction date
// (BR-40.12): the matching row with the latest EffectiveFrom not after on. No match
// fails with IC-0821 and the charge stays on suspense until repriced. Two matching
// rows effective on the same date are ambiguous and rejected.
func ResolveTransferPrice(prices []TransferPrice, lender, borrower, category, role string, on time.Time) (TransferPrice, error) {
	var best *TransferPrice
	for i := range prices {
		p := &prices[i]
		if p.Lender != lender || p.Borrower != borrower || p.Category != category || p.Role != role {
			continue
		}
		if p.EffectiveFrom.After(on) {
			continue
		}
		switch {
		case best == nil || p.EffectiveFrom.After(best.EffectiveFrom):
			best = p
		case p.EffectiveFrom.Equal(best.EffectiveFrom):
			return TransferPrice{}, invalid(fmt.Sprintf("two transfer prices for %s / %s are effective on %s", category, role, p.EffectiveFrom.Format("2006-01-02")))
		}
	}
	if best == nil {
		return TransferPrice{}, &Error{Code: CodeTransferPrice, Detail: fmt.Sprintf("no transfer price for %s / %s on %s", category, role, on.Format("2006-01-02"))}
	}
	return *best, nil
}

// PriceWithTransfer prices a charge with a resolved transfer price. Cost takes
// the cost, CostPlus the cost with markup, Table the quantity times the unit price.
func PriceWithTransfer(tp TransferPrice, quantity, unitCost decimal.Decimal) (ChargePricing, error) {
	switch tp.Method {
	case TPCost:
		return PriceCharge(quantity, unitCost, decimal.Zero, tp.Currency)
	case TPCostPlus:
		return PriceCharge(quantity, unitCost, tp.MarkupPct, tp.Currency)
	case TPTable:
		if len(tp.Currency) != 3 || !quantity.IsPositive() || tp.UnitPrice.IsNegative() || unitCost.IsNegative() {
			return ChargePricing{}, invalid("table price needs a currency, a positive quantity and non-negative prices")
		}
		cost := money.Amount{Value: quantity.Mul(unitCost), Currency: tp.Currency}.Round()
		price := money.Amount{Value: quantity.Mul(tp.UnitPrice), Currency: tp.Currency}.Round()
		margin, err := price.Add(cost.Neg())
		if err != nil {
			return ChargePricing{}, err
		}
		return ChargePricing{Cost: cost, Price: price, Margin: margin}, nil
	}
	return ChargePricing{}, invalid("unknown transfer price method " + tp.Method)
}

// ChargeJournal derives the Journal settlement mode legs of an IC charge (FR-40.13,
// AC-40.11). The borrower books the price as project cost against Due to the
// lender. The lender books Due from the borrower against cost recovery and the
// IC margin. Due accounts come from the matrix row for (lender, borrower). A table
// price below cost gives a negative margin, which posts as a debit (IC loss) on the
// margin account rather than as a negative credit.
func ChargeJournal(m Matrix, pairID, lender, borrower, costAccount, recoveryAccount, marginAccount string, p ChargePricing) (Journal, error) {
	return ChargeJournalWithTax(m, pairID, lender, borrower, costAccount, recoveryAccount, marginAccount, p, ChargeTax{})
}

// ChargeJournalWithTax is ChargeJournal for a cross-border charge: the lender's Due from and the borrower's Due to carry the price
// and the tax, the lender books the tax as output tax and the borrower as input tax, so each entity still balances. A zero tax
// gives the plain journal.
func ChargeJournalWithTax(m Matrix, pairID, lender, borrower, costAccount, recoveryAccount, marginAccount string, p ChargePricing, tax ChargeTax) (Journal, error) {
	if lender == "" || borrower == "" || lender == borrower {
		return Journal{}, invalid("lender and borrower must be different entities")
	}
	if costAccount == "" || recoveryAccount == "" || marginAccount == "" {
		return Journal{}, invalid("cost, recovery and margin accounts are required")
	}
	if !p.Price.Value.IsPositive() || p.Cost.Value.IsNegative() {
		return Journal{}, invalid("charge price must be positive and cost must not be negative")
	}
	row, err := m.Lookup(lender, borrower)
	if err != nil {
		return Journal{}, err
	}
	gross := p.Price
	var taxAmount money.Amount
	if tax.Amount.IsPositive() {
		if tax.LenderAccount == "" || tax.BorrowerAccount == "" {
			return Journal{}, &Error{Code: CodeNoTaxAccount, Detail: "a cross-border charge needs the output tax account of the lender and the input tax account of the borrower"}
		}
		taxAmount = money.Amount{Value: tax.Amount, Currency: p.Price.Currency}.Round()
		var err error
		if gross, err = p.Price.Add(taxAmount); err != nil {
			return Journal{}, err
		}
	}
	j := Journal{PairID: pairID, Legs: []Leg{
		{Entity: borrower, Account: costAccount, Side: Debit, Amount: p.Price},
		{Entity: borrower, Account: row.DueTo, Side: Credit, Amount: gross},
		{Entity: lender, Account: row.DueFrom, Side: Debit, Amount: gross},
		{Entity: lender, Account: recoveryAccount, Side: Credit, Amount: p.Cost},
	}}
	if taxAmount.Value.IsPositive() {
		j.Legs = append(j.Legs,
			Leg{Entity: borrower, Account: tax.BorrowerAccount, Side: Debit, Amount: taxAmount},
			Leg{Entity: lender, Account: tax.LenderAccount, Side: Credit, Amount: taxAmount})
	}
	switch {
	case p.Margin.Value.IsPositive():
		j.Legs = append(j.Legs, Leg{Entity: lender, Account: marginAccount, Side: Credit, Amount: p.Margin})
	case p.Margin.Value.IsNegative():
		j.Legs = append(j.Legs, Leg{Entity: lender, Account: marginAccount, Side: Debit, Amount: p.Margin.Neg()})
	}
	if err := j.Balanced(); err != nil {
		return Journal{}, err
	}
	return j, nil
}
