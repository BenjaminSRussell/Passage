package domain

import (
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// EDIFACT D.96A message mappings onto the canonical documents the X12 sets
// already use: ORDERS to Order (inbound, like the 850), DESADV from ASN (like
// the 856) and INVOIC from Invoice (like the 810). Each builder first runs the
// X12 builder's rules on the same document, so an ASN or invoice the 856/810
// refuses is refused here too and the two syntaxes never diverge in what they
// accept. Each Map* reads the message back into the canonical document.

// EdifactDirectory is the UNH S009 version the builders write.
const (
	EdifactVersion = "D"
	EdifactRelease = "96A"
	EdifactAgency  = "UN"
)

// EdifactMessageTypes lists the message types this service maps.
var EdifactMessageTypes = []string{"ORDERS", "DESADV", "INVOIC"}

// item reference qualifiers: X12 product id qualifier to EDIFACT 7143.
var (
	x12ToEdifactItem = map[string]string{"BP": "IN", "VP": "SA", "UP": "UP", "EN": "EN"}
	edifactToX12Item = map[string]string{"IN": "BP", "SA": "VP", "UP": "UP", "EN": "EN", "SRV": "EN"}
	x12ToEdifactUnit = map[string]string{"KG": "KGM", "LB": "LBR"}
	edifactToX12Unit = map[string]string{"KGM": "KG", "LBR": "LB"}
	x12ToEdifactTax  = map[string]string{"VA": "VAT", "ST": "STT", "GS": "GST"}
	edifactToX12Tax  = map[string]string{"VAT": "VA", "STT": "ST", "GST": "GS"}
	orderTypeToBGM   = map[string]string{"SA": "220", "BK": "221", "RL": "226"}
	bgmToOrderType   = map[string]string{"220": "SA", "221": "BK", "226": "RL"}
	purposeToBGM     = map[string]string{"00": "9", "01": "1", "04": "4"}
	bgmToPurpose     = map[string]string{"9": "00", "1": "01", "4": "04"}
)

func edifactMessage(typ string, segs []EdifactSegment) EdifactMessage {
	return EdifactMessage{Type: typ, Version: EdifactVersion, Release: EdifactRelease, Agency: EdifactAgency, Segments: segs}
}

func el(values ...string) []string { return values }

func nad(qualifier string, loc Location) EdifactSegment {
	return EdifactSegment{Tag: "NAD", Elements: [][]string{el(qualifier), el(loc.GLN, "", "9"), el(), el(loc.Name)}}
}

func dtm(qualifier, value, format string) EdifactSegment {
	return EdifactSegment{Tag: "DTM", Elements: [][]string{el(qualifier, value, format)}}
}

func pia(refs []ItemRef) EdifactSegment {
	s := EdifactSegment{Tag: "PIA", Elements: [][]string{el("5")}}
	for _, r := range refs {
		s.Elements = append(s.Elements, el(r.Value, x12ToEdifactItem[r.Qualifier]))
	}
	return s
}

// itemRefsOf reads LIN03 and PIA product ids back into X12 qualifiers.
func itemRefsOf(s EdifactSegment, first int) []ItemRef {
	var refs []ItemRef
	for i := first; i < len(s.Elements); i++ {
		if q, ok := edifactToX12Item[s.El(i, 1)]; ok && s.El(i, 0) != "" {
			refs = append(refs, ItemRef{Qualifier: q, Value: s.El(i, 0)})
		}
	}
	return refs
}

// edifactNumber reads a numeric data element; ISO 9735 allows a comma or a
// point as the decimal mark and no thousands separator.
func edifactNumber(s string) (decimal.Decimal, error) {
	return decimal.NewFromString(decimalPoint(s))
}

// decimalPoint rewrites a decimal comma as the point the canonical documents use.
func decimalPoint(s string) string { return strings.Replace(s, ",", ".", 1) }

func location(s EdifactSegment) Location { return Location{GLN: s.El(1, 0), Name: s.El(3, 0)} }

// BuildORDERS writes the canonical order as an ORDERS message. It is the
// partner side of the inbound mapping (the simulator and tests send it).
func BuildORDERS(o Order) (EdifactMessage, error) {
	bgm, ok := orderTypeToBGM[o.OrderType]
	fn, ok2 := purposeToBGM[o.Purpose]
	if !ok || !ok2 || o.PONumber == "" || !isDigits(o.PODate, 8) || len(o.Lines) == 0 {
		return EdifactMessage{}, outboundf("order needs a purpose, order type, PO number, CCYYMMDD date and lines")
	}
	segs := []EdifactSegment{Seg("BGM", bgm, o.PONumber, fn), dtm("137", o.PODate, "102")}
	if o.RequestedDelivery != "" {
		segs = append(segs, dtm("2", o.RequestedDelivery, "102"))
	}
	if o.RequestedShip != "" {
		segs = append(segs, dtm("10", o.RequestedShip, "102"))
	}
	if o.VendorID != "" {
		segs = append(segs, EdifactSegment{Tag: "NAD", Elements: [][]string{el("SU"), el(o.VendorID, "", "92")}})
	}
	if o.BillToGLN != "" || o.BillToName != "" {
		segs = append(segs, nad("IV", Location{Name: o.BillToName, GLN: o.BillToGLN}))
	}
	segs = append(segs, nad("DP", Location{Name: o.ShipToName, GLN: o.ShipToGLN}))
	if o.Currency != "" {
		segs = append(segs, Seg("CUX", "2:"+o.Currency+":9"))
	}
	for _, c := range o.Charges {
		segs = append(segs, EdifactSegment{Tag: "ALC", Elements: [][]string{el(c.Indicator), el(), el(), el(), el(c.Code, "", "", c.Description)}}, Seg("MOA", chargeMOA(c.Indicator)+":"+c.Amount))
	}
	for _, l := range o.Lines {
		segs = append(segs, Seg("LIN", l.LineNo))
		if l.ItemRef != nil {
			segs = append(segs, pia([]ItemRef{*l.ItemRef}))
		}
		if l.Description != "" {
			segs = append(segs, EdifactSegment{Tag: "IMD", Elements: [][]string{el("F"), el(), el("", "", "", l.Description)}})
		}
		segs = append(segs, Seg("QTY", "21:"+l.Quantity+":"+l.UOM), Seg("PRI", "AAA:"+l.UnitPrice))
	}
	segs = append(segs, Seg("UNS", "S"), Seg("CNT", "2:"+strconv.Itoa(len(o.Lines))))
	return edifactMessage("ORDERS", segs), nil
}

func chargeMOA(indicator string) string {
	if indicator == "A" {
		return "204"
	}
	return "23"
}

// MapORDERS converts an inbound ORDERS message into the canonical order with
// the 850's checks: BGM number and DTM 137 date, at least one line, numeric
// positive quantities and prices, and CNT 2 equal to the LIN count (EDI-0103).
func MapORDERS(m EdifactMessage) (Order, []Issue) {
	o := Order{Lines: []OrderLine{}}
	var issues []Issue
	malformed := func(detail string) { issues = append(issues, Issue{Code: CodeMalformed, Detail: detail, Reject: true}) }
	cnt := -1
	sawBGM := false
	for _, s := range m.Segments {
		last := len(o.Lines) - 1
		switch s.Tag {
		case "BGM":
			sawBGM = true
			o.OrderType, o.PONumber, o.Purpose = bgmToOrderType[s.El(0, 0)], s.El(1, 0), bgmToPurpose[s.El(2, 0)]
			if o.OrderType == "" {
				malformed("BGM 1001 " + s.El(0, 0) + " is not 220, 221 or 226")
			}
			if o.Purpose == "" {
				malformed("BGM 1225 " + s.El(2, 0) + " is not 9, 1 or 4")
			}
		case "DTM":
			switch s.El(0, 0) {
			case "137":
				o.PODate = s.El(0, 1)
			case "2":
				o.RequestedDelivery = s.El(0, 1)
			case "10":
				o.RequestedShip = s.El(0, 1)
			}
		case "NAD":
			loc := location(s)
			switch s.El(0, 0) {
			case "SU":
				o.VendorID = loc.GLN
			case "IV":
				o.BillToName, o.BillToGLN = loc.Name, loc.GLN
			case "DP", "ST":
				o.ShipToName, o.ShipToGLN = loc.Name, loc.GLN
			}
		case "CUX":
			o.Currency = s.El(0, 1)
		case "ALC":
			o.Charges = append(o.Charges, Charge{Indicator: s.El(0, 0), Code: s.El(4, 0), Description: s.El(4, 3)})
		case "MOA":
			if q := s.El(0, 0); (q == "23" || q == "204") && len(o.Charges) > 0 {
				o.Charges[len(o.Charges)-1].Amount = s.El(0, 1)
			}
		case "LIN":
			o.Lines = append(o.Lines, OrderLine{LineNo: s.El(0, 0)})
			if refs := itemRefsOf(s, 2); len(refs) > 0 {
				o.Lines[len(o.Lines)-1].ItemRef = &refs[0]
			}
		case "PIA":
			if last >= 0 && o.Lines[last].ItemRef == nil {
				if refs := itemRefsOf(s, 1); len(refs) > 0 {
					o.Lines[last].ItemRef = &refs[0]
				}
			}
		case "IMD":
			if last >= 0 {
				o.Lines[last].Description = s.El(2, 3)
			}
		case "QTY":
			if last >= 0 && s.El(0, 0) == "21" {
				o.Lines[last].Quantity, o.Lines[last].UOM = decimalPoint(s.El(0, 1)), s.El(0, 2)
			}
		case "PRI":
			if last >= 0 && (s.El(0, 0) == "AAA" || s.El(0, 0) == "AAB") {
				o.Lines[last].UnitPrice = decimalPoint(s.El(0, 1))
			}
		case "CNT":
			if s.El(0, 0) == "2" {
				n, err := strconv.Atoi(s.El(0, 1))
				if err != nil {
					malformed("CNT 2 " + s.El(0, 1) + " is not a number")
				}
				cnt = n
			}
		}
	}
	if !sawBGM || o.PONumber == "" {
		malformed("BGM with an order number is required")
	}
	if !isDigits(o.PODate, 8) {
		malformed("DTM 137 order date must be CCYYMMDD (format 102)")
	}
	if len(o.Lines) == 0 {
		malformed("at least one LIN is required")
	}
	for _, l := range o.Lines {
		q, err := edifactNumber(l.Quantity)
		p, err2 := edifactNumber(l.UnitPrice)
		if l.LineNo == "" || err != nil || !q.IsPositive() || err2 != nil || p.IsNegative() || l.UOM == "" {
			malformed("line " + l.LineNo + " needs QTY 21 with a positive quantity and unit and a PRI AAA price")
		}
		if l.ItemRef == nil {
			issues = append(issues, Issue{Code: CodeNoItemRef, Detail: "line " + l.LineNo + " has no LIN or PIA product id", Reject: true})
		}
	}
	if cnt >= 0 && cnt != len(o.Lines) {
		issues = append(issues, Issue{Code: CodeCountMismatch, Detail: "CNT 2 " + strconv.Itoa(cnt) + " differs from " + strconv.Itoa(len(o.Lines)) + " LIN segments", Reject: true})
	}
	return o, issues
}

// BuildDESADV writes the ASN as a DESADV: header BGM 351, despatch DTM 11,
// gross weight MEA, BOL and carrier references, ship-from and ship-to NAD,
// carrier TDT with the SCAC, then one CPS per pack with its SSCC in GIN BJ and
// the items as LIN, PIA, QTY 12 and the order reference RFF ON with DTM 171.
func BuildDESADV(a ASN) (EdifactMessage, error) {
	if _, err := Build856(a); err != nil {
		return EdifactMessage{}, err
	}
	segs := []EdifactSegment{Seg("BGM", "351", a.ShipmentID, "9"), dtm("137", a.ShipDate, "102"), dtm("11", a.ShipDate+a.ShipTime, "203"),
		Seg("MEA", "AAX", "AAD", x12ToEdifactUnit[a.WeightUnit]+":"+a.GrossWeight.String())}
	if a.BOL != "" {
		segs = append(segs, Seg("RFF", "BM:"+a.BOL))
	}
	if a.ProNumber != "" {
		segs = append(segs, Seg("RFF", "CN:"+a.ProNumber))
	}
	segs = append(segs, nad("SF", a.ShipFrom), nad("ST", a.ShipTo), Seg("TDT", "20", "", "", "", a.SCAC+"::182"), Seg("CPS", "1"))
	if a.LadingQuantity > 0 {
		segs = append(segs, Seg("PAC", strconv.Itoa(a.LadingQuantity)))
	}
	cps, lines := 1, 0
	for _, o := range a.Orders {
		for _, p := range o.Packs {
			cps++
			segs = append(segs, Seg("CPS", strconv.Itoa(cps), "1"), Seg("PAC", "1"), Seg("PCI", "33E"), Seg("GIN", "BJ", p.SSCC))
			for _, it := range p.Items {
				lines++
				segs = append(segs, Seg("LIN", it.LineNo), pia(it.ItemRefs), Seg("QTY", "12:"+it.Quantity.String()+":"+it.UOM),
					Seg("RFF", "ON:"+o.PONumber), dtm("171", o.PODate, "102"))
			}
		}
	}
	segs = append(segs, Seg("CNT", "2:"+strconv.Itoa(lines)))
	return edifactMessage("DESADV", segs), nil
}

// MapDESADV reads a DESADV back into the ASN, regrouping packs into orders by
// the RFF ON of their lines in order of first appearance. It returns
// ErrOutbound-style errors only for unreadable numbers; the result is then
// held to the 856 rules.
func MapDESADV(m EdifactMessage) (ASN, error) {
	var a ASN
	orderIdx := map[string]int{}
	var pack *ASNPack
	var packPO string
	var item *ASNItem
	flushPack := func() {
		if pack == nil {
			return
		}
		i, ok := orderIdx[packPO]
		if !ok {
			i = len(a.Orders)
			orderIdx[packPO] = i
			a.Orders = append(a.Orders, ASNOrder{PONumber: packPO})
		}
		a.Orders[i].Packs = append(a.Orders[i].Packs, *pack)
		pack, packPO = nil, ""
	}
	poDate := map[string]string{}
	var lastRFF string
	shipmentLevel := false
	for _, s := range m.Segments {
		switch s.Tag {
		case "BGM":
			a.ShipmentID = s.El(1, 0)
		case "DTM":
			switch s.El(0, 0) {
			case "137":
				a.ShipDate = s.El(0, 1)
			case "11":
				if v := s.El(0, 1); len(v) == 12 {
					a.ShipTime = v[8:]
				}
			case "171":
				if lastRFF != "" {
					poDate[lastRFF] = s.El(0, 1)
				}
			}
		case "MEA":
			w, err := edifactNumber(s.El(2, 1))
			if err != nil {
				return a, outboundf("MEA weight %q is not a number", s.El(2, 1))
			}
			a.GrossWeight, a.WeightUnit = w, edifactToX12Unit[s.El(2, 0)]
		case "RFF":
			switch s.El(0, 0) {
			case "BM":
				a.BOL = s.El(0, 1)
			case "CN":
				a.ProNumber = s.El(0, 1)
			case "ON":
				lastRFF = s.El(0, 1)
				if pack != nil {
					packPO = lastRFF
				}
			}
		case "NAD":
			switch s.El(0, 0) {
			case "SF":
				a.ShipFrom = location(s)
			case "ST":
				a.ShipTo = location(s)
			}
		case "TDT":
			a.SCAC = s.El(4, 0)
		case "CPS":
			flushPack()
			item = nil
			shipmentLevel = s.El(1, 0) == ""
		case "PAC":
			if shipmentLevel {
				if n, err := strconv.Atoi(s.El(0, 0)); err == nil {
					a.LadingQuantity = n
				}
			}
		case "GIN":
			if s.El(0, 0) == "BJ" {
				pack = &ASNPack{SSCC: s.El(1, 0)}
			}
		case "LIN":
			if pack == nil {
				return a, outboundf("LIN %s outside a packed CPS", s.El(0, 0))
			}
			pack.Items = append(pack.Items, ASNItem{LineNo: s.El(0, 0)})
			item = &pack.Items[len(pack.Items)-1]
		case "PIA":
			if item != nil {
				item.ItemRefs = append(item.ItemRefs, itemRefsOf(s, 1)...)
			}
		case "QTY":
			if item != nil && s.El(0, 0) == "12" {
				q, err := edifactNumber(s.El(0, 1))
				if err != nil {
					return a, outboundf("QTY %q is not a number", s.El(0, 1))
				}
				item.Quantity, item.UOM = q, s.El(0, 2)
			}
		}
	}
	flushPack()
	for i := range a.Orders {
		a.Orders[i].PODate = poDate[a.Orders[i].PONumber]
	}
	if _, err := Build856(a); err != nil {
		return a, err
	}
	return a, nil
}

// BuildINVOIC writes the invoice as an INVOIC: BGM 380 (381 for a credit
// note), dates, the order and BOL references, payee, invoicee and delivery
// party, payment terms, header allowances and charges, the lines (LIN, PIA,
// QTY 47, MOA 203, PRI AAA), then UNS, CNT, MOA 86 total and TAX with MOA 124.
func BuildINVOIC(inv Invoice) (EdifactMessage, error) {
	if _, err := Build810(inv); err != nil {
		return EdifactMessage{}, err
	}
	typ := "380"
	if inv.CreditNote {
		typ = "381"
	}
	segs := []EdifactSegment{Seg("BGM", typ, inv.Number, "9"), dtm("137", inv.Date, "102")}
	if inv.ShipDate != "" {
		segs = append(segs, dtm("35", inv.ShipDate, "102"))
	}
	segs = append(segs, Seg("RFF", "ON:"+inv.PONumber), dtm("171", inv.PODate, "102"))
	if inv.BOL != "" {
		segs = append(segs, Seg("RFF", "BM:"+inv.BOL))
	}
	segs = append(segs, nad("PE", inv.RemitTo), nad("IV", inv.BillTo), nad("DP", inv.ShipTo))
	if t := inv.Terms; t.Code != "" {
		segs = append(segs, Seg("PAT", "1", t.Code, "5:3:D:"+t.NetDays))
		if t.DiscountDays != "" || !t.DiscountPct.IsZero() {
			segs = append(segs, Seg("PAT", "22", "", "5:3:D:"+t.DiscountDays), Seg("PCD", "12:"+t.DiscountPct.String()))
		}
	}
	for _, c := range inv.Charges {
		segs = append(segs, EdifactSegment{Tag: "ALC", Elements: [][]string{el(c.Indicator), el(), el(), el(), el(c.Code)}}, Seg("MOA", chargeMOA(c.Indicator)+":"+c.Amount.StringFixed(2)))
	}
	for _, l := range inv.Lines {
		segs = append(segs, Seg("LIN", l.LineNo), pia(l.ItemRefs), Seg("QTY", "47:"+l.Quantity.String()+":"+l.UOM),
			Seg("MOA", "203:"+l.Quantity.Mul(l.UnitPrice).Round(2).StringFixed(2)), Seg("PRI", "AAA:"+l.UnitPrice.String()))
	}
	segs = append(segs, Seg("UNS", "S"), Seg("CNT", "2:"+strconv.Itoa(len(inv.Lines))), Seg("MOA", "86:"+inv.Total.StringFixed(2)))
	for _, t := range inv.Taxes {
		segs = append(segs, EdifactSegment{Tag: "TAX", Elements: [][]string{el("7"), el(x12ToEdifactTax[t.Type]), el(), el(), el("", "", "", t.Percent.String())}},
			Seg("MOA", "124:"+t.Amount.StringFixed(2)))
	}
	return edifactMessage("INVOIC", segs), nil
}

// MapINVOIC reads an INVOIC back into the canonical invoice and holds it to
// the 810 rules (the total must equal lines, tax and charges).
func MapINVOIC(m EdifactMessage) (Invoice, error) {
	var inv Invoice
	var lastRFF string
	num := func(s string) (decimal.Decimal, error) {
		v, err := edifactNumber(s)
		if err != nil {
			return v, outboundf("%q is not a number", s)
		}
		return v, nil
	}
	for _, s := range m.Segments {
		var err error
		last := len(inv.Lines) - 1
		switch s.Tag {
		case "BGM":
			inv.CreditNote = s.El(0, 0) == "381"
			inv.Number = s.El(1, 0)
		case "DTM":
			switch s.El(0, 0) {
			case "137":
				inv.Date = s.El(0, 1)
			case "35":
				inv.ShipDate = s.El(0, 1)
			case "171":
				if lastRFF == "ON" {
					inv.PODate = s.El(0, 1)
				}
			}
		case "RFF":
			lastRFF = s.El(0, 0)
			switch lastRFF {
			case "ON":
				inv.PONumber = s.El(0, 1)
			case "BM":
				inv.BOL = s.El(0, 1)
			}
		case "NAD":
			switch s.El(0, 0) {
			case "PE":
				inv.RemitTo = location(s)
			case "IV":
				inv.BillTo = location(s)
			case "DP":
				inv.ShipTo = location(s)
			}
		case "PAT":
			switch s.El(0, 0) {
			case "1":
				inv.Terms.Code, inv.Terms.NetDays = s.El(1, 0), s.El(2, 3)
			case "22":
				inv.Terms.DiscountDays = s.El(2, 3)
			}
		case "PCD":
			if s.El(0, 0) == "12" {
				inv.Terms.DiscountPct, err = num(s.El(0, 1))
			}
		case "ALC":
			inv.Charges = append(inv.Charges, InvoiceCharge{Indicator: s.El(0, 0), Code: s.El(4, 0)})
		case "TAX":
			pct := decimal.Zero
			if p := s.El(4, 3); p != "" {
				pct, err = num(p)
			}
			inv.Taxes = append(inv.Taxes, TaxLine{Type: edifactToX12Tax[s.El(1, 0)], Percent: pct})
		case "MOA":
			var v decimal.Decimal
			if v, err = num(s.El(0, 1)); err != nil {
				break
			}
			switch s.El(0, 0) {
			case "23", "204":
				if len(inv.Charges) > 0 {
					inv.Charges[len(inv.Charges)-1].Amount = v
				}
			case "124":
				if len(inv.Taxes) > 0 {
					inv.Taxes[len(inv.Taxes)-1].Amount = v
				}
			case "86":
				inv.Total = v
			}
		case "LIN":
			inv.Lines = append(inv.Lines, InvoiceLine{LineNo: s.El(0, 0), ItemRefs: itemRefsOf(s, 2)})
		case "PIA":
			if last >= 0 {
				inv.Lines[last].ItemRefs = append(inv.Lines[last].ItemRefs, itemRefsOf(s, 1)...)
			}
		case "QTY":
			if last >= 0 && s.El(0, 0) == "47" {
				inv.Lines[last].UOM = s.El(0, 2)
				inv.Lines[last].Quantity, err = num(s.El(0, 1))
			}
		case "PRI":
			if last >= 0 {
				inv.Lines[last].UnitPrice, err = num(s.El(0, 1))
			}
		}
		if err != nil {
			return inv, err
		}
	}
	if _, err := Build810(inv); err != nil {
		return inv, err
	}
	return inv, nil
}
