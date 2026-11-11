package domain

import (
	"strconv"

	"github.com/shopspring/decimal"
)

// Terms is the ITD payment terms of an invoice (BR-35.3). An empty Code omits ITD.
type Terms struct {
	Code         string          `json:"code,omitempty"`
	DiscountPct  decimal.Decimal `json:"discountPct"`
	DiscountDays string          `json:"discountDays,omitempty"`
	NetDays      string          `json:"netDays,omitempty"`
}

// InvoiceLine is one wave-29 invoice line. Its extension is Quantity times
// UnitPrice rounded to the minor unit. ItemRefs are the IT106/IT107 pairs.
type InvoiceLine struct {
	LineNo    string          `json:"lineNo"`
	Quantity  decimal.Decimal `json:"quantity"`
	UOM       string          `json:"uom"`
	UnitPrice decimal.Decimal `json:"unitPrice"`
	ItemRefs  []ItemRef       `json:"itemRefs"`
	// DeliveryNote (RFF AAK), POLine (RFF LI) and Tax (a TAX with MOA 124 inside the line) are read from a customer's
	// self-billing INVOIC (#35, 35-5); the builders never write them.
	DeliveryNote string           `json:"deliveryNote,omitempty"`
	POLine       string           `json:"poLine,omitempty"`
	Tax          *decimal.Decimal `json:"tax,omitempty"`
}

// TaxLine is one TXI tax amount (ST sales, VA value added, GS goods and services).
type TaxLine struct {
	Type    string          `json:"type"`
	Amount  decimal.Decimal `json:"amount"`
	Percent decimal.Decimal `json:"percent"`
}

// InvoiceCharge is one SAC charge (C, such as D240 freight) or allowance (A).
// Amount is positive; an allowance reduces the total.
type InvoiceCharge struct {
	Indicator string          `json:"indicator"`
	Code      string          `json:"code"`
	Amount    decimal.Decimal `json:"amount"`
}

// Invoice is the canonical customer invoice for an X12 810 (wave 29 source).
// Number is BIG02; Date is BIG01 (CCYYMMDD); PODate is BIG03 and PONumber BIG04
// (the customer PO); CreditNote selects BIG07 CR instead of DI; ShipDate feeds
// DTM*011 and BOL feeds REF*BM, both optional. Total is the invoice total and
// TDS01 carries it times 100.
type Invoice struct {
	Number     string `json:"number"`
	Date       string `json:"date"`
	PONumber   string `json:"poNumber"`
	PODate     string `json:"poDate"`
	CreditNote bool   `json:"creditNote,omitempty"`
	// SelfBilled marks an INVOIC 389 (or 390) the customer issued for our deliveries.
	SelfBilled bool            `json:"selfBilled,omitempty"`
	ShipDate   string          `json:"shipDate,omitempty"`
	BOL        string          `json:"bol,omitempty"`
	Terms      Terms           `json:"terms"`
	RemitTo    Location        `json:"remitTo"`
	BillTo     Location        `json:"billTo"`
	ShipTo     Location        `json:"shipTo"`
	Lines      []InvoiceLine   `json:"lines"`
	Taxes      []TaxLine       `json:"taxes,omitempty"`
	Charges    []InvoiceCharge `json:"charges,omitempty"`
	Total      decimal.Decimal `json:"total"`
}

var taxTypes = map[string]bool{"ST": true, "VA": true, "GS": true}

// twoDecimals reports whether v has at most two decimals (an N2 amount).
func twoDecimals(v decimal.Decimal) bool { return v.Equal(v.Round(2)) }

// Build810 maps an invoice to the body of an X12 810 (004010, BR-35.3) in
// segment order: heading BIG, REF, N1, ITD, DTM; detail IT1; summary TDS, TXI,
// SAC, CTT. It rejects an invoice whose total differs from line extensions plus
// tax plus charges minus allowances: the 810 never carries a total its lines
// cannot produce.
func Build810(inv Invoice) (Transaction, error) {
	if inv.Number == "" || len(inv.Number) > 22 {
		return Transaction{}, outboundf("invoice number must be 1 to 22 characters")
	}
	if !isDigits(inv.Date, 8) || !isDigits(inv.PODate, 8) || inv.PONumber == "" {
		return Transaction{}, outboundf("invoice date, PO date (CCYYMMDD) and PO number are required")
	}
	if inv.ShipDate != "" && !isDigits(inv.ShipDate, 8) {
		return Transaction{}, outboundf("ship date must be CCYYMMDD")
	}
	if !isGLN(inv.RemitTo.GLN) || !isGLN(inv.BillTo.GLN) || !isGLN(inv.ShipTo.GLN) {
		return Transaction{}, outboundf("remit-to, bill-to and ship-to GLNs must be 13 digits")
	}
	if len(inv.Lines) == 0 {
		return Transaction{}, outboundf("an invoice needs at least one line")
	}
	if !twoDecimals(inv.Total) {
		return Transaction{}, outboundf("total carries more than two decimals")
	}
	if inv.Terms.DiscountPct.IsNegative() || inv.Terms.DiscountPct.GreaterThan(decimal.NewFromInt(100)) {
		return Transaction{}, outboundf("terms discount percent must be 0 to 100")
	}

	sum := decimal.Zero
	for _, l := range inv.Lines {
		if l.LineNo == "" || !l.Quantity.IsPositive() || l.UOM == "" || l.UnitPrice.IsNegative() {
			return Transaction{}, outboundf("line %q needs a line number, positive quantity, unit and non-negative price", l.LineNo)
		}
		if _, ok := itemRefElements(l.ItemRefs); !ok {
			return Transaction{}, outboundf("line %s needs item references with distinct BP, VP, UP or EN qualifiers (%s)", l.LineNo, CodeNoItemRef)
		}
		sum = sum.Add(l.Quantity.Mul(l.UnitPrice).Round(2))
	}
	for _, t := range inv.Taxes {
		if !taxTypes[t.Type] {
			return Transaction{}, outboundf("tax type %q is not ST, VA or GS", t.Type)
		}
		if t.Amount.IsNegative() || !twoDecimals(t.Amount) || t.Percent.IsNegative() {
			return Transaction{}, outboundf("tax %s needs a non-negative amount with two decimals and a non-negative percent", t.Type)
		}
		sum = sum.Add(t.Amount)
	}
	for _, c := range inv.Charges {
		if (c.Indicator != "C" && c.Indicator != "A") || c.Code == "" || len(c.Code) > 4 {
			return Transaction{}, outboundf("charge %q needs indicator C or A and a 1 to 4 character SAC02 code", c.Code)
		}
		if !c.Amount.IsPositive() || !twoDecimals(c.Amount) {
			return Transaction{}, outboundf("charge %s needs a positive amount with two decimals", c.Code)
		}
		if c.Indicator == "A" {
			sum = sum.Sub(c.Amount)
		} else {
			sum = sum.Add(c.Amount)
		}
	}
	if !sum.Equal(inv.Total) {
		return Transaction{}, outboundf("invoice total %s differs from lines, tax and charges %s (%s)", inv.Total, sum, CodeArithmetic)
	}

	typeCode := "DI"
	if inv.CreditNote {
		typeCode = "CR"
	}
	b := &segWriter{}
	b.seg("BIG", inv.Date, inv.Number, inv.PODate, inv.PONumber, "", "", typeCode)
	if inv.BOL != "" {
		b.seg("REF", "BM", inv.BOL)
	}
	b.seg("N1", "RE", inv.RemitTo.Name, "92", inv.RemitTo.GLN)
	b.seg("N1", "BT", inv.BillTo.Name, "92", inv.BillTo.GLN)
	b.seg("N1", "ST", inv.ShipTo.Name, "92", inv.ShipTo.GLN)
	if inv.Terms.Code != "" {
		b.seg("ITD", inv.Terms.Code, "", inv.Terms.DiscountPct.String(), "", inv.Terms.DiscountDays, "", inv.Terms.NetDays)
	}
	if inv.ShipDate != "" {
		b.seg("DTM", "011", inv.ShipDate)
	}
	for _, l := range inv.Lines {
		refs, _ := itemRefElements(l.ItemRefs)
		b.seg("IT1", append([]string{l.LineNo, l.Quantity.String(), l.UOM, l.UnitPrice.String(), ""}, refs...)...)
	}
	b.seg("TDS", cents(inv.Total))
	for _, t := range inv.Taxes {
		b.seg("TXI", t.Type, t.Amount.StringFixed(2), t.Percent.String())
	}
	for _, c := range inv.Charges {
		b.seg("SAC", c.Indicator, c.Code, "", "", cents(c.Amount))
	}
	b.seg("CTT", strconv.Itoa(len(inv.Lines)))
	return Transaction{SetID: "810", Body: b.body}, nil
}

// cents writes an amount as an X12 N2 value with two implied decimals.
func cents(v decimal.Decimal) string {
	return v.Round(2).Shift(2).StringFixed(0)
}
