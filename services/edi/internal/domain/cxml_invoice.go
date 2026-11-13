package domain

import (
	"strconv"
	"strings"
	"time"
)

// CXMLInvoice is the vendor invoice a canonical InvoiceDetailRequest becomes in payables' vendor-invoice intake (#35, 35-4).
type CXMLInvoice struct {
	OrderID  string
	Number   string
	Date     string
	Purpose  string
	Currency string
	Gross    string
	Lines    []CXMLInvoiceLine
}

type CXMLInvoiceLine struct {
	LineNo    string
	Item      string
	Quantity  string
	Unit      string
	UnitPrice string
}

var cxmlInvoicePurposes = map[string]bool{"standard": true, "creditMemo": true, "debitMemo": true, "lineLevelCreditMemo": true}

// PlanInvoice reads a canonical InvoiceDetailRequest as a vendor invoice for payables: one purchase order (the vendor comes from it), a
// date, a currency and a quantity and unit price on every line. A document that is not like that is a Park, not an error.
func PlanInvoice(c CXMLCanonical) (CXMLInvoice, *Park) {
	if len(c.OrderIDs) != 1 {
		return CXMLInvoice{}, &Park{Code: CodeCXMLInvoiceTarget, Detail: "invoice " + c.Number + " names " + strconv.Itoa(len(c.OrderIDs)) +
			" purchase orders; only an invoice of exactly one order is put into payables, so it is matched by an AP clerk"}
	}
	inv := CXMLInvoice{OrderID: c.OrderIDs[0], Number: strings.TrimSpace(c.Number), Purpose: c.Purpose, Currency: c.Currency, Gross: c.Gross}
	if inv.Purpose == "" {
		inv.Purpose = "standard"
	}
	if !cxmlInvoicePurposes[inv.Purpose] {
		return CXMLInvoice{}, &Park{Code: CodeCXMLMappingInvalid, Detail: "invoice purpose " + strconv.Quote(c.Purpose) + " is not standard, creditMemo, debitMemo or lineLevelCreditMemo"}
	}
	if len(c.Date) >= 10 {
		if _, err := time.Parse(time.DateOnly, c.Date[:10]); err == nil {
			inv.Date = c.Date[:10]
		}
	}
	if inv.Date == "" {
		return CXMLInvoice{}, &Park{Code: CodeCXMLMappingInvalid, Detail: "invoice date " + strconv.Quote(c.Date) + " is not a date"}
	}
	if len(inv.Currency) != 3 || strings.ToUpper(inv.Currency) != inv.Currency {
		return CXMLInvoice{}, &Park{Code: CodeCXMLMappingInvalid, Detail: "the invoice names no currency on its unit prices"}
	}
	if inv.Gross != "" {
		if _, err := decimalOf(inv.Gross); err != nil {
			return CXMLInvoice{}, &Park{Code: CodeCXMLMappingInvalid, Detail: "the invoice summary amount " + strconv.Quote(inv.Gross) + " is not a number"}
		}
	}
	for _, l := range c.Lines {
		q, err := decimalOf(strings.TrimSpace(l.Quantity))
		if err != nil || q.IsZero() {
			return CXMLInvoice{}, &Park{Code: CodeCXMLMappingInvalid, Detail: "invoice line " + l.Line + " has no quantity"}
		}
		p, err := decimalOf(strings.TrimSpace(l.UnitPrice))
		if err != nil || p.IsNegative() {
			return CXMLInvoice{}, &Park{Code: CodeCXMLMappingInvalid, Detail: "invoice line " + l.Line + " has no unit price"}
		}
		if strings.TrimSpace(l.Line) == "" {
			return CXMLInvoice{}, &Park{Code: CodeCXMLMappingInvalid, Detail: "an invoice line has no line number"}
		}
		inv.Lines = append(inv.Lines, CXMLInvoiceLine{LineNo: strings.TrimSpace(l.Line), Item: l.Item, Quantity: q.String(), Unit: l.Unit, UnitPrice: p.String()})
	}
	return inv, nil
}
