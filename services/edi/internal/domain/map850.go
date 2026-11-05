package domain

import (
	"regexp"
	"strconv"

	"github.com/shopspring/decimal"
)

// Catalog codes raised by the domain (see docs/ and the service README).
const (
	CodeCountMismatch = "EDI-0103" // CTT01 differs from the PO1 count
	CodeNoItemRef     = "EDI-0105" // no item cross-reference for any qualifier on a PO1
	CodeMalformed     = "EDI-0160" // a required 850 segment or element is missing or invalid (not in catalog)
	CodeUnmatchedAck  = "EDI-0161" // a received 997 answers no outbound group of the partner (not in catalog)
)

// Issue is one finding on a transaction. Reject true makes the 997 AK5 code R;
// otherwise the transaction is accepted with errors and its message is FAILED.
type Issue struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Reject bool   `json:"reject"`
	// Contrl places an EDIFACT issue for the CONTRL that rejects its message
	// (UCM, UCS and UCD); zero for X12 and for issues without a place.
	Contrl ContrlPlace `json:"-"`
}

// ContrlPlace is where a mapping issue lies in an EDIFACT message: the 0085
// code, the segment position (UNH is 1), and the data element and component
// positions inside the segment. Zero positions are left out of the CONTRL.
type ContrlPlace struct {
	Code      string
	Segment   int
	Element   int
	Component int
}

// ItemRef is one PO106/PO107 pair: qualifier BP, VP, UP or EN.
type ItemRef struct {
	Qualifier string `json:"qualifier"`
	Value     string `json:"value"`
}

// OrderLine is a PO1 loop with its PID description.
type OrderLine struct {
	LineNo      string   `json:"lineNo"`
	Quantity    string   `json:"quantity"`
	UOM         string   `json:"uom"`
	UnitPrice   string   `json:"unitPrice"`
	PriceBasis  string   `json:"priceBasis,omitempty"`
	ItemRef     *ItemRef `json:"itemRef,omitempty"`
	Description string   `json:"description,omitempty"`
}

// Charge is one SAC charge (C) or allowance (A). Amount is a decimal string
// read from SAC05, which is N2 (two implied decimals).
type Charge struct {
	Indicator   string `json:"indicator"`
	Code        string `json:"code"`
	Amount      string `json:"amount"`
	Description string `json:"description,omitempty"`
}

// Order is the canonical order produced from an inbound 850 (BR-35.1).
type Order struct {
	Purpose           string      `json:"purpose"`
	OrderType         string      `json:"orderType"`
	PONumber          string      `json:"poNumber"`
	PODate            string      `json:"poDate"`
	Currency          string      `json:"currency,omitempty"`
	Department        string      `json:"department,omitempty"`
	VendorID          string      `json:"vendorId,omitempty"`
	RequestedDelivery string      `json:"requestedDelivery,omitempty"`
	RequestedShip     string      `json:"requestedShip,omitempty"`
	ShipToName        string      `json:"shipToName,omitempty"`
	ShipToGLN         string      `json:"shipToGln,omitempty"`
	BillToName        string      `json:"billToName,omitempty"`
	BillToGLN         string      `json:"billToGln,omitempty"`
	Charges           []Charge    `json:"charges,omitempty"`
	Lines             []OrderLine `json:"lines"`
}

var (
	currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
	n2Re       = regexp.MustCompile(`^-?[0-9]{1,15}$`)
)

// glnQualifier reports whether an N103 qualifier carries a GLN (92 assigned by
// buyer, UL global location number), as BR-35.1 requires for ship-to.
func glnQualifier(q string) bool { return q == "92" || q == "UL" }

// itemRefPriority is the BR-35.1 order: buyer part, vendor part, UPC/GTIN, EAN.
var itemRefPriority = []string{"BP", "VP", "UP", "EN"}

// Map850 converts an 850 transaction into the canonical order. It never returns
// a partial order silently: every problem is an Issue.
func Map850(t Transaction) (Order, []Issue) {
	o := Order{Lines: []OrderLine{}}
	var issues []Issue
	malformed := func(detail string) { issues = append(issues, Issue{Code: CodeMalformed, Detail: detail, Reject: true}) }
	sawBEG, sawCTT := false, false
	lineIdx := -1 // index of the PO1 that a following PID describes
	poCount := 0
	for _, s := range t.Body {
		switch s.ID {
		case "BEG":
			if sawBEG {
				malformed("more than one BEG segment")
				continue
			}
			sawBEG = true
			o.Purpose, o.OrderType, o.PONumber, o.PODate = element(s, 0), element(s, 1), element(s, 2), element(s, 4)
			if o.Purpose != "00" && o.Purpose != "01" && o.Purpose != "04" {
				malformed("BEG01 purpose " + o.Purpose + " is not 00, 01 or 04")
			}
			if o.OrderType != "SA" && o.OrderType != "BK" && o.OrderType != "RL" {
				malformed("BEG02 order type " + o.OrderType + " is not SA, BK or RL")
			}
			if o.PONumber == "" {
				malformed("BEG03 purchase order number is required")
			}
			if !isDigits(o.PODate, 8) {
				malformed("BEG05 date must be CCYYMMDD")
			}
		case "CUR":
			o.Currency = element(s, 1)
			if !currencyRe.MatchString(o.Currency) {
				malformed("CUR02 currency " + o.Currency + " is not an ISO 4217 code")
			}
		case "REF":
			switch element(s, 0) {
			case "DP":
				o.Department = element(s, 1)
			case "IA":
				o.VendorID = element(s, 1)
			}
		case "SAC":
			c, ok := mapSAC(s)
			if !ok {
				malformed("SAC " + element(s, 0) + " needs indicator C or A and an N2 amount in SAC05")
				continue
			}
			o.Charges = append(o.Charges, c)
		case "DTM":
			switch element(s, 0) {
			case "002":
				o.RequestedDelivery = element(s, 1)
			case "010":
				o.RequestedShip = element(s, 1)
			}
		case "N1":
			switch element(s, 0) {
			case "ST":
				o.ShipToName = element(s, 1)
				if glnQualifier(element(s, 2)) {
					o.ShipToGLN = element(s, 3)
				}
			case "BT":
				o.BillToName = element(s, 1)
				if glnQualifier(element(s, 2)) {
					o.BillToGLN = element(s, 3)
				}
			}
		case "PO1":
			poCount++
			l, ok := mapPO1(s)
			lineIdx = -1
			if !ok {
				malformed("PO1 " + strconv.Itoa(poCount) + " has an invalid quantity or unit price")
				continue
			}
			if l.ItemRef == nil {
				issues = append(issues, Issue{Code: CodeNoItemRef, Detail: "no item cross-reference for line " + l.LineNo})
			}
			o.Lines = append(o.Lines, l)
			lineIdx = len(o.Lines) - 1
		case "PID":
			if element(s, 0) == "F" && lineIdx >= 0 {
				o.Lines[lineIdx].Description = element(s, 4)
			}
		case "CTT":
			sawCTT = true
			if element(s, 0) != strconv.Itoa(poCount) {
				issues = append(issues, Issue{Code: CodeCountMismatch, Detail: "CTT01 " + element(s, 0) + " does not match PO1 count " + strconv.Itoa(poCount), Reject: true})
			}
		}
	}
	if !sawBEG {
		malformed("no BEG segment")
	}
	if !sawCTT {
		malformed("no CTT segment")
	}
	if len(o.Lines) == 0 {
		malformed("no PO1 lines")
	}
	return o, issues
}

// mapSAC reads SAC01 (C charge, A allowance), SAC02 code, SAC05 amount (N2,
// two implied decimals) and SAC15 description.
func mapSAC(s Segment) (Charge, bool) {
	ind, raw := element(s, 0), element(s, 4)
	if (ind != "C" && ind != "A") || !n2Re.MatchString(raw) {
		return Charge{}, false
	}
	cents, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return Charge{}, false
	}
	return Charge{Indicator: ind, Code: element(s, 1), Amount: decimal.New(cents, -2).StringFixed(2), Description: element(s, 14)}, true
}

// mapPO1 reads PO101..PO105 and the PO106/PO107 pairs. It reports false when the
// quantity or price is not a decimal or the quantity is not positive.
func mapPO1(s Segment) (OrderLine, bool) {
	qty, err := decimal.NewFromString(element(s, 1))
	if err != nil || !qty.IsPositive() {
		return OrderLine{}, false
	}
	price, err := decimal.NewFromString(element(s, 3))
	if err != nil || price.IsNegative() {
		return OrderLine{}, false
	}
	l := OrderLine{LineNo: element(s, 0), Quantity: qty.String(), UOM: element(s, 2), UnitPrice: price.String(), PriceBasis: element(s, 4)}
	found := map[string]string{}
	for i := 5; i+1 < len(s.Elements); i += 2 {
		q, v := s.Elements[i], s.Elements[i+1]
		if _, seen := found[q]; !seen && v != "" {
			found[q] = v
		}
	}
	for _, q := range itemRefPriority {
		if v, ok := found[q]; ok {
			l.ItemRef = &ItemRef{Qualifier: q, Value: v}
			break
		}
	}
	return l, true
}

func isDigits(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
