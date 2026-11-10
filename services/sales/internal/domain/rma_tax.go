package domain

import (
	"sort"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

type TaxBasisLine struct {
	LineNo      int
	ProductID   string
	TaxCode     string
	Qty         decimal.Decimal
	Tax         decimal.Decimal
	CreditedQty decimal.Decimal
	CreditedTax decimal.Decimal
	ReturnedQty decimal.Decimal
	ReturnedTax decimal.Decimal
}

type TaxReversal struct {
	InvoiceLineNo int
	TaxCode       string
	Qty           decimal.Decimal
	Tax           decimal.Decimal
}

func ReverseTax(currency string, lines []TaxBasisLine, returned map[string]decimal.Decimal) []TaxReversal {
	exp := money.MinorUnits(currency)
	products := make([]string, 0, len(returned))
	for p := range returned {
		products = append(products, p)
	}
	sort.Strings(products)
	var out []TaxReversal
	for _, product := range products {
		left := returned[product]
		for _, l := range lines {
			if l.ProductID != product || !left.IsPositive() || !l.Qty.IsPositive() {
				continue
			}
			doneQty := l.CreditedQty.Add(l.ReturnedQty)
			take := decimal.Min(left, l.Qty.Sub(doneQty))
			if !take.IsPositive() {
				continue
			}
			doneTax := l.CreditedTax.Add(l.ReturnedTax)
			tax := l.Tax.Sub(doneTax)
			if doneQty.Add(take).LessThan(l.Qty) {
				tax = l.Tax.Mul(doneQty.Add(take)).Div(l.Qty).RoundBank(exp).Sub(doneTax)
			}
			tax = decimal.Max(decimal.Zero, decimal.Min(tax, l.Tax.Sub(doneTax)))
			left = left.Sub(take)
			out = append(out, TaxReversal{InvoiceLineNo: l.LineNo, TaxCode: l.TaxCode, Qty: take, Tax: tax})
		}
	}
	return out
}
