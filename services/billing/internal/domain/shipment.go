package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Codes of the shipment proposal rules (§5.29.18).
const (
	// CodeDateBeforeSlip is BIL-0006: the invoice date precedes a packing slip's date (BR-29.4).
	CodeDateBeforeSlip = "BIL-0006"
	// CodeTooManyLines is BIL-0007: one packing slip alone holds more lines than an invoice may (BR-29.5).
	CodeTooManyLines = "BIL-0007"
)

// Source names the documents a shipment invoice line came from (FR-29.1,
// FR-29.12): the packing slip line, the order line, the product and the price
// explanation that priced it. It is empty on free-text lines and credit notes.
type Source struct {
	PackingSlipID     string
	PackingSlipLineID string
	PackingSlipDate   time.Time
	OrderID           string
	OrderLineID       string
	ProductID         string
	PriceExplainRef   string
}

// SlipLine is one posted packing slip line offered for billing. Line.No is
// assigned by PlanProposals; Source.PackingSlipID, PackingSlipDate and OrderID
// are taken from the slip.
type SlipLine struct {
	Source Source
	Line   Line
}

// Slip is a posted packing slip offered for billing (FR-29.1). Date is the
// slip's posting date; the invoice date may not precede it (BIL-0006). Charges
// are the slip's freight and handling, billed on the invoice that takes it.
type Slip struct {
	ID      string
	OrderID string
	Date    time.Time
	Charges decimal.Decimal
	Lines   []SlipLine
}

// Proposal is one invoice PlanProposals plans. Sources runs parallel to Lines.
type Proposal struct {
	Type    string
	Charges decimal.Decimal
	Lines   []Line
	Sources []Source
}

// PlanProposals groups posted packing slips into invoices (FR-29.1, FR-29.2,
// BR-29.5). Without consolidate there is one invoice per order, in order of
// first appearance; with it every slip of the request (one invoice account,
// currency and terms) goes on one invoice, CONSOLIDATED when it spans more than
// one order. A group over maxLines lines is split at slip boundaries into as
// many invoices as needed (BR-29.5 auto split); a slip is never split, so a
// slip alone over maxLines is refused with BIL-0007. The invoice date may not
// precede any slip's date (BIL-0006). Slip and slip line ids must be unique in
// the request (a slip line is invoiced once).
func PlanProposals(slips []Slip, consolidate bool, invoiceDate time.Time, maxLines int) ([]Proposal, error) {
	if len(slips) == 0 {
		return nil, &Error{Code: "COM-0001", Detail: "at least one packing slip is required"}
	}
	if maxLines < 1 {
		return nil, fmt.Errorf("domain: maxLines must be at least 1, got %d", maxLines)
	}
	seenSlip := map[string]bool{}
	seenLine := map[string]bool{}
	for _, s := range slips {
		switch {
		case s.ID == "" || seenSlip[s.ID]:
			return nil, &Error{Code: "COM-0001", Detail: "packing slip ids must be present and unique"}
		case s.OrderID == "":
			return nil, &Error{Code: "COM-0001", Detail: "packing slip " + s.ID + " has no order"}
		case len(s.Lines) == 0:
			return nil, &Error{Code: "COM-0001", Detail: "packing slip " + s.ID + " has no lines"}
		case s.Charges.IsNegative():
			return nil, &Error{Code: "COM-0001", Detail: "packing slip " + s.ID + " has negative charges"}
		case s.Date.After(invoiceDate):
			return nil, &Error{Code: CodeDateBeforeSlip, Detail: fmt.Sprintf("Invoice date %s precedes packing slip date %s",
				invoiceDate.Format("2006-01-02"), s.Date.Format("2006-01-02"))}
		case len(s.Lines) > maxLines:
			return nil, &Error{Code: CodeTooManyLines, Detail: fmt.Sprintf("Packing slip %s holds %d lines; an invoice holds at most %d",
				s.ID, len(s.Lines), maxLines)}
		}
		seenSlip[s.ID] = true
		for _, sl := range s.Lines {
			id := sl.Source.PackingSlipLineID
			if id == "" || seenLine[id] {
				return nil, &Error{Code: "COM-0001", Detail: "packing slip line ids must be present and unique"}
			}
			seenLine[id] = true
			if err := sl.Line.Validate(); err != nil {
				return nil, err
			}
		}
	}
	var out []Proposal
	for _, group := range groupSlips(slips, consolidate) {
		for _, part := range splitByLines(group, maxLines) {
			out = append(out, propose(part, consolidate))
		}
	}
	return out, nil
}

// propose numbers the lines of one invoice and records where each came from.
func propose(slips []Slip, consolidate bool) Proposal {
	p := Proposal{Type: TypeShipment, Charges: decimal.Zero}
	orders := map[string]bool{}
	for _, s := range slips {
		orders[s.OrderID] = true
		p.Charges = p.Charges.Add(s.Charges)
		for _, sl := range s.Lines {
			l := sl.Line
			l.No = len(p.Lines) + 1
			p.Lines = append(p.Lines, l)
			src := sl.Source
			src.PackingSlipID, src.PackingSlipDate, src.OrderID = s.ID, s.Date, s.OrderID
			p.Sources = append(p.Sources, src)
		}
	}
	if consolidate && len(orders) > 1 {
		p.Type = TypeConsolidated
	}
	return p
}

// groupSlips returns the slips of each invoice: all in one group when
// consolidating, otherwise one group per order in order of first appearance.
func groupSlips(slips []Slip, consolidate bool) [][]Slip {
	if consolidate {
		return [][]Slip{slips}
	}
	var groups [][]Slip
	index := map[string]int{}
	for _, s := range slips {
		i, ok := index[s.OrderID]
		if !ok {
			i = len(groups)
			index[s.OrderID] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], s)
	}
	return groups
}

// splitByLines packs the slips of one group, in order, into parts of at most
// maxLines lines. Every slip fits on its own (checked by PlanProposals).
func splitByLines(slips []Slip, maxLines int) [][]Slip {
	var parts [][]Slip
	var cur []Slip
	n := 0
	for _, s := range slips {
		if n+len(s.Lines) > maxLines && len(cur) > 0 {
			parts = append(parts, cur)
			cur, n = nil, 0
		}
		cur = append(cur, s)
		n += len(s.Lines)
	}
	return append(parts, cur)
}
