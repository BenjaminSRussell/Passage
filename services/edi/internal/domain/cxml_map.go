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
		statuses := l.Statuses
		if len(statuses) == 0 {
			statuses = []CXMLStatus{{Type: l.Status, Quantity: l.Quantity}}
		}
		accepted := decimal.Zero
		for _, st := range statuses {
			if !strings.EqualFold(st.Type, "accept") {
				return "", &Park{Code: CodeCXMLNeedsReview, Detail: "line " + l.Line + " is " + strconv.Quote(st.Type) + ", not accepted as ordered; the buyer decides"}
			}
			q := st.Quantity
			if q == "" {
				q = l.Quantity
			}
			d, err := decimal.NewFromString(strings.TrimSpace(q))
			if err != nil {
				return "", &Park{Code: CodeCXMLMappingInvalid, Detail: "line " + l.Line + " has a status with the quantity " + strconv.Quote(q)}
			}
			accepted = accepted.Add(d)
		}
		ordered, err := decimal.NewFromString(strings.TrimSpace(l.Quantity))
		if err != nil || !accepted.Equal(ordered) {
			return "", &Park{Code: CodeCXMLNeedsReview, Detail: "line " + l.Line + " accepts " + accepted.String() + " of " + strings.TrimSpace(l.Quantity) + "; the buyer decides"}
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

// POLineQty is an open purchase order line as the confirmation is checked against it.
type POLineQty struct {
	LineNo int
	Qty    string
}

// CheckConfirmationCoversOrder holds an accepting confirmation to the purchase order it confirms: when it lists items, every open line
// of the order must be listed, with the ordered quantity (a confirmation that skips a line or states another quantity confirms
// something else than the order). A confirmation with no items accepts the whole order and is not checked.
func CheckConfirmationCoversOrder(c CXMLCanonical, open []POLineQty) *Park {
	if len(c.Lines) == 0 {
		return nil
	}
	listed := map[string]string{}
	for _, l := range c.Lines {
		listed[strings.TrimSpace(l.Line)] = strings.TrimSpace(l.Quantity)
	}
	for _, o := range open {
		q, ok := listed[strconv.Itoa(o.LineNo)]
		if !ok {
			return &Park{Code: CodeCXMLNeedsReview, Detail: "the confirmation does not list line " + strconv.Itoa(o.LineNo) + " of the order; the buyer decides"}
		}
		want, err1 := decimal.NewFromString(o.Qty)
		got, err2 := decimal.NewFromString(q)
		if err1 != nil || err2 != nil || !want.Equal(got) {
			return &Park{Code: CodeCXMLNeedsReview, Detail: "the confirmation states " + q + " for line " + strconv.Itoa(o.LineNo) + " but the order has " + o.Qty + "; the buyer decides"}
		}
	}
	return nil
}
