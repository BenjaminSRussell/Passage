package domain

import "github.com/shopspring/decimal"

// Charge calculations (FR-17.9). PER_UNIT multiplies by the document quantity,
// PERCENT takes a percentage of the line-amount base.
const (
	CalcFixed   = "FIXED"
	CalcPerUnit = "PER_UNIT"
	CalcPercent = "PERCENT"
)

var (
	hundred = decimal.NewFromInt(100)
	one     = decimal.NewFromInt(1)
)

// LineInput is what the caller enters for a PO or requisition line. Tax is the
// tax already computed by the tax engine (wave 13); this slice takes it as input.
type LineInput struct {
	Qty         decimal.Decimal
	UnitPrice   decimal.Decimal
	PriceUnit   decimal.Decimal
	DiscountPct decimal.Decimal
	Tax         decimal.Decimal
}

// LineAmounts are the rounded amounts of one line. Gross − Discount = Amount.
type LineAmounts struct {
	Gross    decimal.Decimal
	Discount decimal.Decimal
	Amount   decimal.Decimal
	Tax      decimal.Decimal
}

// ChargeInput is a charge before it is priced. Line is the 1-based position among the priced lines of
// the one line the charge belongs to (FR-17.9 line charge: PER_UNIT takes that line's quantity and
// PERCENT its amount after discount); 0 is a header charge over the whole document.
type ChargeInput struct {
	Calc  string
	Value decimal.Decimal
	Line  int
}

// Totals are the persisted document totals (FR-17.11).
type Totals struct {
	Subtotal      decimal.Decimal
	DiscountTotal decimal.Decimal
	ChargesTotal  decimal.Decimal
	TaxTotal      decimal.Decimal
	Total         decimal.Decimal
}

// Document is a priced PO: its lines, its charge amounts and its totals.
type Document struct {
	Lines   []LineAmounts
	Charges []decimal.Decimal
	Totals  Totals
}

// ComputeLine prices one line to minor units. The exact amount is
// qty × unitPrice ÷ priceUnit; gross and net are each rounded half-even once
// from that exact value, so discount = gross − net and no amount is re-rounded.
func ComputeLine(in LineInput, minor int32) (LineAmounts, error) {
	switch {
	case in.Qty.Sign() <= 0:
		return LineAmounts{}, fail(CodeValidation, "quantity must be positive")
	case in.UnitPrice.Sign() < 0:
		return LineAmounts{}, fail(CodeValidation, "unit price must not be negative")
	case in.PriceUnit.Sign() <= 0:
		return LineAmounts{}, fail(CodeValidation, "price unit must be positive")
	case in.DiscountPct.Sign() < 0 || in.DiscountPct.GreaterThan(hundred):
		return LineAmounts{}, fail(CodeValidation, "discount percent must be 0 to 100")
	case in.Tax.Sign() < 0:
		return LineAmounts{}, fail(CodeValidation, "tax must not be negative")
	}
	exact := in.Qty.Mul(in.UnitPrice).DivRound(in.PriceUnit, priceDivPrecision)
	net := exact.Mul(one.Sub(in.DiscountPct.Shift(-2)))
	gross := exact.RoundBank(minor)
	amount := net.RoundBank(minor)
	return LineAmounts{Gross: gross, Discount: gross.Sub(amount), Amount: amount, Tax: in.Tax.RoundBank(minor)}, nil
}

// ChargeAmount prices one charge to minor units. base is the sum of line
// amounts; qty is the sum of line quantities.
func ChargeAmount(c ChargeInput, qty, base decimal.Decimal, minor int32) (decimal.Decimal, error) {
	switch c.Calc {
	case CalcFixed:
		return c.Value.RoundBank(minor), nil
	case CalcPerUnit:
		return c.Value.Mul(qty).RoundBank(minor), nil
	case CalcPercent:
		return base.Mul(c.Value.Shift(-2)).RoundBank(minor), nil
	}
	return decimal.Zero, fail(CodeValidation, "charge calculation %q is not FIXED, PER_UNIT or PERCENT", c.Calc)
}

// Price prices a whole document. Total = Σ line amounts + Σ charges + Σ tax, which
// equals subtotal − discount_total + charges_total + tax_total exactly.
func Price(lines []LineInput, charges []ChargeInput, minor int32) (Document, error) {
	doc := Document{Lines: make([]LineAmounts, 0, len(lines)), Charges: make([]decimal.Decimal, 0, len(charges))}
	if len(lines) == 0 {
		return doc, fail(CodeValidation, "a document needs at least one line")
	}
	qty := decimal.Zero
	lineQty := make([]decimal.Decimal, 0, len(lines))
	t := Totals{Subtotal: decimal.Zero, DiscountTotal: decimal.Zero, ChargesTotal: decimal.Zero, TaxTotal: decimal.Zero}
	for _, in := range lines {
		la, err := ComputeLine(in, minor)
		if err != nil {
			return Document{}, err
		}
		doc.Lines = append(doc.Lines, la)
		qty = qty.Add(in.Qty)
		lineQty = append(lineQty, in.Qty)
		t.Subtotal = t.Subtotal.Add(la.Gross)
		t.DiscountTotal = t.DiscountTotal.Add(la.Discount)
		t.TaxTotal = t.TaxTotal.Add(la.Tax)
	}
	base := t.Subtotal.Sub(t.DiscountTotal)
	for _, c := range charges {
		cq, cb := qty, base
		if c.Line != 0 {
			if c.Line < 0 || c.Line > len(lines) {
				return Document{}, fail(CodeValidation, "a line charge names line %d of %d", c.Line, len(lines))
			}
			cq, cb = lineQty[c.Line-1], doc.Lines[c.Line-1].Amount
		}
		amt, err := ChargeAmount(c, cq, cb, minor)
		if err != nil {
			return Document{}, err
		}
		doc.Charges = append(doc.Charges, amt)
		t.ChargesTotal = t.ChargesTotal.Add(amt)
	}
	t.Total = base.Add(t.ChargesTotal).Add(t.TaxTotal)
	doc.Totals = t
	return doc, nil
}
