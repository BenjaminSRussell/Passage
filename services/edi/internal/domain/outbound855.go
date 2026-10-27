package domain

import (
	"strconv"

	"github.com/shopspring/decimal"
)

// Ack855Line is one PO1/ACK pair of a purchase order acknowledgment. Status is
// the ACK01 line status: IA accepted, IQ accepted with a quantity change, IR
// rejected.
type Ack855Line struct {
	LineNo     string          `json:"lineNo"`
	OrderedQty decimal.Decimal `json:"orderedQty"`
	UOM        string          `json:"uom"`
	Status     string          `json:"status"`
	AckQty     decimal.Decimal `json:"ackQty"`
}

// Ack855 is the canonical PO acknowledgment answering an inbound 850. AckType
// is BAK02: AD acknowledge with detail and no change, AC acknowledge with detail
// and change, RD reject with detail, AK acknowledge without detail, RJ reject
// without detail. PODate is BAK04 and AckDate BAK09, both CCYYMMDD.
type Ack855 struct {
	PONumber string       `json:"poNumber"`
	PODate   string       `json:"poDate"`
	AckDate  string       `json:"ackDate"`
	AckType  string       `json:"ackType"`
	Lines    []Ack855Line `json:"lines,omitempty"`
}

var lineStatuses = map[string]bool{"IA": true, "IQ": true, "IR": true}

// Build855 maps a PO acknowledgment to the body of an X12 855 (004010). AD lines
// are all IA, AC carries at least one changed or rejected line, RD lines are all
// IR, and AK and RJ carry no lines. An IA line acknowledges its ordered
// quantity, an IQ line a different one, and an IR line zero.
func Build855(a Ack855) (Transaction, error) {
	if a.PONumber == "" || !isDigits(a.PODate, 8) || !isDigits(a.AckDate, 8) {
		return Transaction{}, outboundf("PO number, PO date and acknowledgment date (CCYYMMDD) are required")
	}
	b := &segWriter{}
	b.seg("BAK", "00", a.AckType, a.PONumber, a.PODate, "", "", "", "", a.AckDate)
	switch a.AckType {
	case "AK", "RJ":
		if len(a.Lines) > 0 {
			return Transaction{}, outboundf("BAK02 %s acknowledges without detail and carries no lines", a.AckType)
		}
		return Transaction{SetID: "855", Body: b.body}, nil
	case "AD", "AC", "RD":
	default:
		return Transaction{}, outboundf("BAK02 %q is not AD, AC, RD, AK or RJ", a.AckType)
	}
	if len(a.Lines) == 0 {
		return Transaction{}, outboundf("BAK02 %s needs at least one line", a.AckType)
	}
	changed := 0
	for _, l := range a.Lines {
		if l.LineNo == "" || !l.OrderedQty.IsPositive() || l.UOM == "" || !lineStatuses[l.Status] {
			return Transaction{}, outboundf("acknowledgment line %q is incomplete or has an unknown status", l.LineNo)
		}
		switch l.Status {
		case "IA":
			if !l.AckQty.Equal(l.OrderedQty) {
				return Transaction{}, outboundf("line %s is accepted (IA) but its quantity differs from the order", l.LineNo)
			}
		case "IQ":
			if !l.AckQty.IsPositive() || l.AckQty.Equal(l.OrderedQty) {
				return Transaction{}, outboundf("line %s is IQ and needs a positive quantity different from the order", l.LineNo)
			}
			changed++
		case "IR":
			if !l.AckQty.IsZero() {
				return Transaction{}, outboundf("line %s is rejected (IR) but carries a quantity", l.LineNo)
			}
			changed++
		}
		if a.AckType == "AD" && l.Status != "IA" {
			return Transaction{}, outboundf("BAK02 AD acknowledges without change but line %s is %s", l.LineNo, l.Status)
		}
		if a.AckType == "RD" && l.Status != "IR" {
			return Transaction{}, outboundf("BAK02 RD rejects the order but line %s is %s", l.LineNo, l.Status)
		}
		b.seg("PO1", l.LineNo, l.OrderedQty.String(), l.UOM)
		b.seg("ACK", l.Status, l.AckQty.String(), l.UOM)
	}
	if a.AckType == "AC" && changed == 0 {
		return Transaction{}, outboundf("BAK02 AC needs at least one changed or rejected line; use AD")
	}
	b.seg("CTT", strconv.Itoa(len(a.Lines)))
	return Transaction{SetID: "855", Body: b.body}, nil
}
