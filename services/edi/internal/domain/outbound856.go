package domain

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/shopspring/decimal"
)

// Location is a named party with its GS1 Global Location Number, written in
// N1 with qualifier 92 (BR-35.2, BR-35.3).
type Location struct {
	Name string `json:"name"`
	GLN  string `json:"gln"`
}

// ASNItem is one shipped line inside a pack (HL level I). ItemRefs are the LIN
// pairs (VP vendor part, UP GTIN, BP buyer part, EN EAN), written in the given order.
type ASNItem struct {
	LineNo   string          `json:"lineNo"`
	Quantity decimal.Decimal `json:"quantity"`
	UOM      string          `json:"uom"`
	ItemRefs []ItemRef       `json:"itemRefs"`
}

// ASNPack is one logistics unit (HL level P) identified by its SSCC-18, which
// is the wave-31 license plate id.
type ASNPack struct {
	SSCC  string    `json:"sscc"`
	Items []ASNItem `json:"items"`
}

// ASNOrder is one customer order (HL level O) and the packs that carry its lines.
// A pack belongs to exactly one order; the caller splits a mixed pallet.
type ASNOrder struct {
	PONumber string    `json:"poNumber"`
	PODate   string    `json:"poDate"`
	Packs    []ASNPack `json:"packs"`
}

// ASN is the canonical advance ship notice built from a shipped load
// (wms.load.shipped). GrossWeight and WeightUnit (LB or KG) are the load totals
// written in TD1.
type ASN struct {
	ShipmentID  string          `json:"shipmentId"`
	ShipDate    string          `json:"shipDate"`
	ShipTime    string          `json:"shipTime"`
	BOL         string          `json:"bol,omitempty"`
	ProNumber   string          `json:"proNumber,omitempty"`
	SCAC        string          `json:"scac"`
	GrossWeight decimal.Decimal `json:"grossWeight"`
	WeightUnit  string          `json:"weightUnit"`
	ShipFrom    Location        `json:"shipFrom"`
	ShipTo      Location        `json:"shipTo"`
	Orders      []ASNOrder      `json:"orders"`
}

// ErrOutbound marks a canonical document that cannot be turned into an EDI
// transaction. The API answers it with 422 EDI-0007.
var ErrOutbound = errors.New("edi: outbound document")

func outboundf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrOutbound, fmt.Sprintf(format, args...))
}

// isGLN reports whether s is a 13-digit GS1 Global Location Number.
func isGLN(s string) bool { return isDigits(s, 13) }

// itemQualifiers are the product id qualifiers BR-35.1 to BR-35.3 use.
var itemQualifiers = map[string]bool{"BP": true, "VP": true, "UP": true, "EN": true}

// itemRefElements returns the qualifier/value pairs of LIN and IT1. It reports
// false when there is no reference, a qualifier is unknown or repeated, or a
// value is empty.
func itemRefElements(refs []ItemRef) ([]string, bool) {
	if len(refs) == 0 {
		return nil, false
	}
	seen := map[string]bool{}
	out := make([]string, 0, 2*len(refs))
	for _, r := range refs {
		if !itemQualifiers[r.Qualifier] || r.Value == "" || seen[r.Qualifier] {
			return nil, false
		}
		seen[r.Qualifier] = true
		out = append(out, r.Qualifier, r.Value)
	}
	return out, true
}

// segWriter collects transaction body segments and numbers HL segments for CTT01.
type segWriter struct {
	body []Segment
	hls  int
}

func (b *segWriter) seg(id string, elems ...string) {
	b.body = append(b.body, Segment{ID: id, Elements: elems})
}

// hl writes HL*id*parent*code and returns the new id. A parent of 0 is written empty.
func (b *segWriter) hl(parent int, code string) int {
	b.hls++
	p := ""
	if parent > 0 {
		p = strconv.Itoa(parent)
	}
	b.seg("HL", strconv.Itoa(b.hls), p, code)
	return b.hls
}

// Build856 maps an ASN to the body of an X12 856 in structure S/O/P/I (BR-35.2).
// The body excludes ST and SE, which Serialize writes. Every SSCC must be a
// valid GS1 SSCC-18 and unique in the notice; CTT01 is the HL count.
func Build856(a ASN) (Transaction, error) {
	if a.ShipmentID == "" || len(a.ShipmentID) > 30 {
		return Transaction{}, outboundf("shipment id must be 1 to 30 characters")
	}
	if !isDigits(a.ShipDate, 8) || !isDigits(a.ShipTime, 4) {
		return Transaction{}, outboundf("ship date must be CCYYMMDD and ship time HHMM")
	}
	if !isGLN(a.ShipFrom.GLN) || !isGLN(a.ShipTo.GLN) {
		return Transaction{}, outboundf("ship-from and ship-to GLNs must be 13 digits")
	}
	if len(a.SCAC) < 2 || len(a.SCAC) > 4 {
		return Transaction{}, outboundf("SCAC must be 2 to 4 characters")
	}
	if !a.GrossWeight.IsPositive() || (a.WeightUnit != "LB" && a.WeightUnit != "KG") {
		return Transaction{}, outboundf("gross weight must be positive with unit LB or KG")
	}
	if len(a.Orders) == 0 {
		return Transaction{}, outboundf("an ASN needs at least one order")
	}
	seen := map[string]bool{}
	packs := 0
	for _, o := range a.Orders {
		if o.PONumber == "" || !isDigits(o.PODate, 8) || len(o.Packs) == 0 {
			return Transaction{}, outboundf("order %q needs a PO number, a CCYYMMDD PO date and at least one pack", o.PONumber)
		}
		for _, p := range o.Packs {
			if !ValidSSCC(p.SSCC) || seen[p.SSCC] {
				return Transaction{}, outboundf("pack SSCC %q is invalid or repeated", p.SSCC)
			}
			seen[p.SSCC] = true
			if len(p.Items) == 0 {
				return Transaction{}, outboundf("pack %s carries no items", p.SSCC)
			}
			for _, it := range p.Items {
				if it.LineNo == "" || !it.Quantity.IsPositive() || it.UOM == "" {
					return Transaction{}, outboundf("pack %s has a line without line number, positive quantity or unit", p.SSCC)
				}
				if _, ok := itemRefElements(it.ItemRefs); !ok {
					return Transaction{}, outboundf("line %s needs item references with distinct BP, VP, UP or EN qualifiers (%s)", it.LineNo, CodeNoItemRef)
				}
			}
			packs++
		}
	}

	b := &segWriter{}
	b.seg("BSN", "00", a.ShipmentID, a.ShipDate, a.ShipTime, "0001")
	s := b.hl(0, "S")
	// TD1: packaging PLT94, lading quantity, then TD106 G gross weight, TD107, TD108 unit.
	b.seg("TD1", "PLT94", strconv.Itoa(packs), "", "", "", "G", a.GrossWeight.String(), a.WeightUnit)
	// TD5: TD502 qualifier 2 (SCAC) is required with TD503; TD504 M motor.
	b.seg("TD5", "", "2", a.SCAC, "M")
	if a.BOL != "" {
		b.seg("REF", "BM", a.BOL)
	}
	if a.ProNumber != "" {
		b.seg("REF", "CN", a.ProNumber)
	}
	b.seg("DTM", "011", a.ShipDate)
	b.seg("N1", "ST", a.ShipTo.Name, "92", a.ShipTo.GLN)
	b.seg("N1", "SF", a.ShipFrom.Name, "92", a.ShipFrom.GLN)
	for _, o := range a.Orders {
		oh := b.hl(s, "O")
		b.seg("PRF", o.PONumber, "", "", o.PODate)
		for _, p := range o.Packs {
			ph := b.hl(oh, "P")
			b.seg("MAN", "GM", p.SSCC)
			for _, it := range p.Items {
				b.hl(ph, "I")
				refs, _ := itemRefElements(it.ItemRefs)
				b.seg("LIN", append([]string{""}, refs...)...)
				b.seg("SN1", it.LineNo, it.Quantity.String(), it.UOM)
			}
		}
	}
	b.seg("CTT", strconv.Itoa(b.hls))
	return Transaction{SetID: "856", Body: b.body}, nil
}
