package domain

import (
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// Codes a cXML message is parked with when it cannot be put onto the downstream service (the message stays FAILED with the code and is
// posted again with POST /messages/{id}:post once the cause is fixed).
const (
	CodeCXMLUnknownOrder   = "EDI-0036"
	CodeCXMLNeedsReview    = "EDI-0037"
	CodeCXMLInvoiceTarget  = "EDI-0038"
	CodeCXMLDownstream     = "EDI-0039"
	CodeCXMLNotConfigured  = "EDI-0040"
	CodeCXMLSendFailed     = "EDI-0041"
	CodeCXMLMappingInvalid = "EDI-0042"
)

// CXMLASN is the receiving ASN a ship notice becomes: one line per PO line, quantities of repeated lines added.
type CXMLASN struct {
	OrderID        string
	TrackingNumber string
	ShipDate       string
	Lines          []CXMLASNLine
}

type CXMLASNLine struct {
	POLineNo int
	Qty      string
}

// PlanShipNotice reads a canonical ShipNoticeRequest as an ASN: one order, positive quantities and numeric line numbers. A document that
// is not like that is a Park (it names the line), not an error: no retry changes it.
func PlanShipNotice(c CXMLCanonical) (CXMLASN, *Park) {
	if len(c.OrderIDs) != 1 {
		return CXMLASN{}, &Park{Code: CodeCXMLMappingInvalid, Detail: "a ship notice for receiving names exactly one purchase order; this one names " + strconv.Itoa(len(c.OrderIDs))}
	}
	asn := CXMLASN{OrderID: c.OrderIDs[0], TrackingNumber: c.Number}
	if len(c.Date) >= 10 {
		asn.ShipDate = c.Date[:10]
	}
	at := map[int]int{}
	for _, l := range c.Lines {
		no, err := strconv.Atoi(strings.TrimSpace(l.Line))
		if err != nil || no < 1 {
			return CXMLASN{}, &Park{Code: CodeCXMLMappingInvalid, Detail: "ship notice line number " + strconv.Quote(l.Line) + " is not a purchase order line number"}
		}
		q, ok := positive(l.Quantity)
		if !ok {
			return CXMLASN{}, &Park{Code: CodeCXMLMappingInvalid, Detail: "ship notice line " + l.Line + " has no positive quantity"}
		}
		if i, seen := at[no]; seen {
			asn.Lines[i].Qty = addQty(asn.Lines[i].Qty, q)
			continue
		}
		at[no] = len(asn.Lines)
		asn.Lines = append(asn.Lines, CXMLASNLine{POLineNo: no, Qty: q})
	}
	if len(asn.Lines) == 0 {
		return CXMLASN{}, &Park{Code: CodeCXMLMappingInvalid, Detail: "the ship notice has no lines"}
	}
	return asn, nil
}

// PlanConfirmation decides what a canonical ConfirmationRequest means for the purchase order: accepted as ordered (the order is
// confirmed), or something that needs the buyer to look (a rejected, changed or backordered line): that is a Park, not a confirmation.
func PlanConfirmation(c CXMLCanonical) (orderID string, park *Park) {
	if len(c.OrderIDs) != 1 {
		return "", &Park{Code: CodeCXMLMappingInvalid, Detail: "a confirmation names exactly one purchase order; this one names " + strconv.Itoa(len(c.OrderIDs))}
	}
	if len(c.Lines) == 0 {
		if strings.EqualFold(c.Purpose, "accept") {
			return c.OrderIDs[0], nil
		}
		return "", &Park{Code: CodeCXMLNeedsReview, Detail: "the confirmation is " + strconv.Quote(c.Purpose) + ", not an acceptance, and lists no lines"}
	}
	for _, l := range c.Lines {
		if !strings.EqualFold(l.Status, "accept") {
			return "", &Park{Code: CodeCXMLNeedsReview, Detail: "line " + l.Line + " is " + strconv.Quote(l.Status) + ", not accepted as ordered; the buyer decides"}
		}
	}
	return c.OrderIDs[0], nil
}

func positive(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	d, err := decimalOf(s)
	if err != nil || !d.IsPositive() {
		return "", false
	}
	return d.String(), true
}

func decimalOf(s string) (decimal.Decimal, error) { return decimal.NewFromString(s) }

func addQty(a, b string) string {
	x, _ := decimal.NewFromString(a)
	y, _ := decimal.NewFromString(b)
	return x.Add(y).String()
}
