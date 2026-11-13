package domain

import (
	"strconv"

	"github.com/shopspring/decimal"
)

// Map810 reads an inbound X12 810 (004010) as a customer's self-billed invoice (#35, 35-5): Passage sends its own 810s and takes
// vendor invoices through cXML and AP, so an 810 that arrives is the customer billing itself for our deliveries, matched against the
// shipments our 856s registered like an EDIFACT INVOIC 389. BIG01 is the date, BIG02 the customer's document number, BIG03 and
// BIG04 the PO date and number, and a BIG07 of CR or CN makes it a credit. REF*PK (packing list) or REF*BM (bill of lading) is the
// delivery note, in the heading for every line or after an IT1 for that line; REF*LI after an IT1 names the PO line, which is IT101
// otherwise. IT1 carries quantity (IT102), unit (IT103), unit price (IT104) and the IT106/IT107 item pairs. A TXI after an IT1 is that
// line's tax; after TDS it is a header tax. TDS01 is the total in cents.
func Map810(t Transaction) (Invoice, []Issue) {
	inv := Invoice{SelfBilled: true, Lines: []InvoiceLine{}}
	var issues []Issue
	malformed := func(detail string) { issues = append(issues, Issue{Code: CodeMalformed, Detail: detail, Reject: true}) }
	num := func(s, what string) decimal.Decimal {
		v, err := decimal.NewFromString(s)
		if err != nil {
			malformed(what + " " + strconv.Quote(s) + " is not a number")
		}
		return v
	}
	var headerNote string
	sawBIG, pastTDS := false, false
	for _, s := range t.Body {
		last := len(inv.Lines) - 1
		switch s.ID {
		case "BIG":
			sawBIG = true
			inv.Date, inv.Number, inv.PODate, inv.PONumber = element(s, 0), element(s, 1), element(s, 2), element(s, 3)
			switch element(s, 6) {
			case "CR", "CN":
				inv.CreditNote = true
			}
		case "CUR":
			inv.Currency = element(s, 1)
		case "REF":
			q, v := element(s, 0), element(s, 1)
			switch {
			case (q == "PK" || q == "BM") && last >= 0:
				inv.Lines[last].DeliveryNote = v
			case q == "PK" || q == "BM":
				headerNote = v
				if q == "BM" {
					inv.BOL = v
				}
			case q == "LI" && last >= 0:
				inv.Lines[last].POLine = v
			}
		case "IT1":
			l := InvoiceLine{LineNo: element(s, 0), UOM: element(s, 2)}
			l.Quantity = num(element(s, 1), "IT102")
			l.UnitPrice = num(element(s, 3), "IT104")
			for i := 5; i+1 < len(s.Elements); i += 2 {
				if q, v := element(s, i), element(s, i+1); q != "" && v != "" {
					l.ItemRefs = append(l.ItemRefs, ItemRef{Qualifier: q, Value: v})
				}
			}
			if l.LineNo == "" {
				malformed("IT1 has no assigned line number (IT101)")
			}
			inv.Lines = append(inv.Lines, l)
		case "TXI":
			tax := TaxLine{Type: element(s, 0), Amount: num(element(s, 1), "TXI02")}
			if p := element(s, 2); p != "" {
				tax.Percent = num(p, "TXI03")
			}
			if !pastTDS && last >= 0 {
				amount := tax.Amount
				if inv.Lines[last].Tax != nil {
					amount = amount.Add(*inv.Lines[last].Tax)
				}
				inv.Lines[last].Tax = &amount
				continue
			}
			inv.Taxes = append(inv.Taxes, tax)
		case "TDS":
			pastTDS = true
			inv.Total = num(element(s, 0), "TDS01").Shift(-2)
		}
	}
	if !sawBIG || inv.Number == "" || !isDigits(inv.Date, 8) {
		malformed("BIG needs the invoice date (CCYYMMDD) and the document number")
	}
	if len(inv.Lines) == 0 {
		malformed("the 810 has no IT1 line")
	}
	for i := range inv.Lines {
		if inv.Lines[i].DeliveryNote == "" {
			inv.Lines[i].DeliveryNote = headerNote
		}
		if inv.Lines[i].POLine == "" {
			inv.Lines[i].POLine = inv.Lines[i].LineNo
		}
	}
	return inv, issues
}
