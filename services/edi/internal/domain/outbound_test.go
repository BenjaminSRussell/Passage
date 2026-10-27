package domain

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var (
	us        = Party{Qualifier: "ZZ", ID: "PASSAGE"}
	them      = Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"}
	generated = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
)

// roundTrip envelopes a built transaction, serializes it and parses it back,
// so every builder test also proves Parse reads exactly what Serialize wrote.
func roundTrip(t *testing.T, tr Transaction) Transaction {
	t.Helper()
	ic, err := OutboundEnvelope(tr, us, them, "T", 42, generated)
	if err != nil {
		t.Fatalf("OutboundEnvelope: %v", err)
	}
	raw, err := Serialize(ic)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse of own output: %v\n%s", err, raw)
	}
	return back.Groups[0].Transactions[0]
}

func segmentsByID(body []Segment, id string) []Segment {
	var out []Segment
	for _, s := range body {
		if s.ID == id {
			out = append(out, s)
		}
	}
	return out
}

func segmentIDs(body []Segment) string {
	ids := make([]string, len(body))
	for i, s := range body {
		ids[i] = s.ID
	}
	return strings.Join(ids, " ")
}

func hlLevels(body []Segment) string {
	var out []string
	for _, s := range segmentsByID(body, "HL") {
		out = append(out, element(s, 2))
	}
	return strings.Join(out, "")
}

const (
	glnPlant = "0614141000012"
	glnDock  = "0614141000029"
)

// sscc completes a 14-digit prefix into a valid SSCC-18 for fixtures.
func sscc(prefix14 string) string {
	seventeen := prefix14 + "001"
	c, err := SSCCCheckDigit(seventeen)
	if err != nil {
		panic(err)
	}
	return seventeen + string(c)
}

// asnFixture is the AC-35.8 shape: a load of 2 pallets x 3 cases for 2 orders.
func asnFixture() ASN {
	item := func(line, ref string) ASNItem {
		return ASNItem{LineNo: line, Quantity: dec("1"), UOM: "CA", ItemRefs: []ItemRef{{Qualifier: "VP", Value: "VP-" + ref}, {Qualifier: "UP", Value: "00012345678905"}}}
	}
	return ASN{
		ShipmentID: "LOAD42-SHIP7", ShipDate: "20261021", ShipTime: "0930", BOL: "BOL9001", ProNumber: "PRO77",
		SCAC: "FDEG", GrossWeight: dec("812.5"), WeightUnit: "KG",
		ShipFrom: Location{Name: "Plant", GLN: glnPlant}, ShipTo: Location{Name: "Dock", GLN: glnDock},
		Orders: []ASNOrder{
			{PONumber: "PO-1001", PODate: "20261015", Packs: []ASNPack{{SSCC: sscc("00345678901234"), Items: []ASNItem{item("1", "A"), item("2", "A"), item("3", "A")}}}},
			{PONumber: "PO-1002", PODate: "20261016", Packs: []ASNPack{{SSCC: sscc("00345678909999"), Items: []ASNItem{item("1", "B"), item("2", "B"), item("3", "B")}}}},
		},
	}
}

func TestBuild856_AC358HierarchySSCCAndCount(t *testing.T) {
	tr, err := Build856(asnFixture())
	if err != nil {
		t.Fatalf("Build856: %v", err)
	}
	tr = roundTrip(t, tr)
	// S(1), then per order O, P and three I.
	if got := hlLevels(tr.Body); got != "SOPIIIOPIII" {
		t.Fatalf("HL levels = %s", got)
	}
	hl := segmentsByID(tr.Body, "HL")
	// Each order's parent is the shipment, each pack's its order, each item's its pack.
	if hl[1].Elements[1] != "1" || hl[2].Elements[1] != "2" || hl[3].Elements[1] != "3" || hl[6].Elements[1] != "1" || hl[7].Elements[1] != "7" || hl[8].Elements[1] != "8" {
		t.Fatalf("HL parents: %v", hl)
	}
	if ctt := segmentsByID(tr.Body, "CTT"); len(ctt) != 1 || ctt[0].Elements[0] != strconv.Itoa(len(hl)) {
		t.Fatalf("CTT01 = %v, want %d", ctt, len(hl))
	}
	man := segmentsByID(tr.Body, "MAN")
	if len(man) != 2 || man[0].Elements[0] != "GM" || man[0].Elements[1] != sscc("00345678901234") || man[1].Elements[1] != sscc("00345678909999") {
		t.Fatalf("MAN*GM = %v", man)
	}
	if td1 := segmentsByID(tr.Body, "TD1")[0]; strings.Join(td1.Elements, "*") != "PLT94*2****G*812.5*KG" {
		t.Fatalf("TD1 = %v", td1.Elements)
	}
	if td5 := segmentsByID(tr.Body, "TD5")[0]; strings.Join(td5.Elements, "*") != "*2*FDEG*M" {
		t.Fatalf("TD5 must carry TD502 qualifier 2 with the SCAC: %v", td5.Elements)
	}
	if lin := segmentsByID(tr.Body, "LIN")[0]; strings.Join(lin.Elements, "*") != "*VP*VP-A*UP*00012345678905" {
		t.Fatalf("LIN = %v", lin.Elements)
	}
	if prf := segmentsByID(tr.Body, "PRF")[1]; prf.Elements[0] != "PO-1002" || prf.Elements[3] != "20261016" {
		t.Fatalf("PRF = %v", prf.Elements)
	}
	if bsn := segmentsByID(tr.Body, "BSN")[0]; bsn.Elements[4] != "0001" {
		t.Fatalf("BSN05 = %v", bsn.Elements)
	}
}

func TestBuild856_Rejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ASN)
	}{
		{"bad SSCC check digit", func(a *ASN) {
			s := a.Orders[0].Packs[0].SSCC
			a.Orders[0].Packs[0].SSCC = s[:17] + string('0'+(s[17]-'0'+1)%10)
		}},
		{"repeated SSCC", func(a *ASN) { a.Orders[1].Packs[0].SSCC = a.Orders[0].Packs[0].SSCC }},
		{"no item reference", func(a *ASN) { a.Orders[0].Packs[0].Items[0].ItemRefs = nil }},
		{"repeated qualifier", func(a *ASN) { a.Orders[0].Packs[0].Items[0].ItemRefs[1].Qualifier = "VP" }},
		{"unknown qualifier", func(a *ASN) { a.Orders[0].Packs[0].Items[0].ItemRefs[0].Qualifier = "ZZ" }},
		{"zero quantity", func(a *ASN) { a.Orders[0].Packs[0].Items[0].Quantity = dec("0") }},
		{"short GLN", func(a *ASN) { a.ShipTo.GLN = "123" }},
		{"no weight", func(a *ASN) { a.GrossWeight = decimal.Zero }},
		{"bad weight unit", func(a *ASN) { a.WeightUnit = "TON" }},
		{"no orders", func(a *ASN) { a.Orders = nil }},
		{"order without packs", func(a *ASN) { a.Orders[0].Packs = nil }},
		{"empty pack", func(a *ASN) { a.Orders[0].Packs[0].Items = nil }},
		{"bad ship date", func(a *ASN) { a.ShipDate = "2026-10-21" }},
		{"long shipment id", func(a *ASN) { a.ShipmentID = strings.Repeat("X", 31) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := asnFixture()
			tt.mutate(&a)
			if _, err := Build856(a); !errors.Is(err, ErrOutbound) {
				t.Fatalf("err = %v, want ErrOutbound", err)
			}
		})
	}
}

func invoiceFixture() Invoice {
	refs := []ItemRef{{Qualifier: "BP", Value: "BUY-1"}, {Qualifier: "VP", Value: "VEN-1"}}
	return Invoice{
		Number: "INV-7001", Date: "20261101", PONumber: "PO-1001", PODate: "20261015",
		ShipDate: "20261031", BOL: "BOL9001", Terms: Terms{Code: "05", DiscountPct: dec("2"), DiscountDays: "10", NetDays: "30"},
		RemitTo: Location{Name: "Remit", GLN: glnPlant}, BillTo: Location{Name: "Bill", GLN: glnDock}, ShipTo: Location{Name: "Dock", GLN: glnDock},
		Lines: []InvoiceLine{
			{LineNo: "1", Quantity: dec("10"), UOM: "EA", UnitPrice: dec("12.50"), ItemRefs: refs},
			// 3 x 4.105 = 12.315, rounded to 12.32 per line.
			{LineNo: "2", Quantity: dec("3"), UOM: "EA", UnitPrice: dec("4.105"), ItemRefs: refs},
		},
		Taxes:   []TaxLine{{Type: "ST", Amount: dec("26.09"), Percent: dec("19")}},
		Charges: []InvoiceCharge{{Indicator: "C", Code: "D240", Amount: dec("5.00")}, {Indicator: "A", Code: "C310", Amount: dec("1.50")}},
		// 125.00 + 12.32 + 26.09 + 5.00 - 1.50
		Total: dec("166.91"),
	}
}

func TestBuild810_TotalsSegmentOrderAndCodes(t *testing.T) {
	tr, err := Build810(invoiceFixture())
	if err != nil {
		t.Fatalf("Build810: %v", err)
	}
	tr = roundTrip(t, tr)
	// Heading, detail, summary in 004010 order: TDS precedes TXI and SAC.
	if got := segmentIDs(tr.Body); got != "BIG REF N1 N1 N1 ITD DTM IT1 IT1 TDS TXI SAC SAC CTT" {
		t.Fatalf("segment order = %s", got)
	}
	if tds := segmentsByID(tr.Body, "TDS")[0]; tds.Elements[0] != "16691" {
		t.Fatalf("TDS01 = %v, want 16691 (total x 100)", tds.Elements)
	}
	sac := segmentsByID(tr.Body, "SAC")
	if strings.Join(sac[0].Elements, "*") != "C*D240***500" || strings.Join(sac[1].Elements, "*") != "A*C310***150" {
		t.Fatalf("SAC = %v", sac)
	}
	if it1 := segmentsByID(tr.Body, "IT1")[1]; strings.Join(it1.Elements, "*") != "2*3*EA*4.105**BP*BUY-1*VP*VEN-1" {
		t.Fatalf("IT1 = %v", it1.Elements)
	}
	if itd := segmentsByID(tr.Body, "ITD")[0]; strings.Join(itd.Elements, "*") != "05**2**10**30" {
		t.Fatalf("ITD = %v", itd.Elements)
	}
	if txi := segmentsByID(tr.Body, "TXI")[0]; strings.Join(txi.Elements, "*") != "ST*26.09*19" {
		t.Fatalf("TXI = %v", txi.Elements)
	}
	if big := segmentsByID(tr.Body, "BIG")[0]; big.Elements[1] != "INV-7001" || big.Elements[3] != "PO-1001" || big.Elements[6] != "DI" {
		t.Fatalf("BIG = %v", big.Elements)
	}
	if ctt := segmentsByID(tr.Body, "CTT")[0]; ctt.Elements[0] != "2" {
		t.Fatalf("CTT01 = %v", ctt.Elements)
	}
}

func TestBuild810_CreditNoteWithoutOptionalSegments(t *testing.T) {
	inv := invoiceFixture()
	inv.CreditNote = true
	inv.Charges, inv.Taxes, inv.Terms, inv.ShipDate, inv.BOL = nil, nil, Terms{}, "", ""
	inv.Total = dec("137.32")
	tr, err := Build810(inv)
	if err != nil {
		t.Fatal(err)
	}
	if big := segmentsByID(tr.Body, "BIG")[0]; big.Elements[6] != "CR" {
		t.Fatalf("BIG07 = %v, want CR", big.Elements)
	}
	if got := segmentIDs(tr.Body); got != "BIG N1 N1 N1 IT1 IT1 TDS CTT" {
		t.Fatalf("segments = %s", got)
	}
}

func TestBuild810_Rejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Invoice)
	}{
		{"total differs from lines", func(i *Invoice) { i.Total = dec("166.92") }},
		{"allowance added instead of subtracted", func(i *Invoice) { i.Total = dec("169.91") }},
		{"total with three decimals", func(i *Invoice) { i.Total = dec("166.905") }},
		{"charge with three decimals", func(i *Invoice) { i.Charges[0].Amount = dec("5.005") }},
		{"charge without code", func(i *Invoice) { i.Charges[0].Code = "" }},
		{"charge with bad indicator", func(i *Invoice) { i.Charges[0].Indicator = "X" }},
		{"unknown tax type", func(i *Invoice) { i.Taxes[0].Type = "XX" }},
		{"negative tax", func(i *Invoice) { i.Taxes[0].Amount = dec("-26.09") }},
		{"no item reference", func(i *Invoice) { i.Lines[0].ItemRefs = nil }},
		{"no lines", func(i *Invoice) { i.Lines = nil }},
		{"bad GLN", func(i *Invoice) { i.RemitTo.GLN = "" }},
		{"bad ship date", func(i *Invoice) { i.ShipDate = "31-10-2026" }},
		{"discount over 100", func(i *Invoice) { i.Terms.DiscountPct = dec("101") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := invoiceFixture()
			tt.mutate(&inv)
			if _, err := Build810(inv); !errors.Is(err, ErrOutbound) {
				t.Fatalf("err = %v, want ErrOutbound", err)
			}
		})
	}
}

// The 810 total rule holds for any lines: TDS01 is the line sum (each line
// rounded to the minor unit) times 100, and any other total is refused.
func TestBuild810_PropertyTotalIsLineSum(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		inv := invoiceFixture()
		inv.Taxes, inv.Charges, inv.Lines = nil, nil, nil
		n := rapid.IntRange(1, 6).Draw(t, "lines")
		total := decimal.Zero
		for i := 0; i < n; i++ {
			qty := decimal.NewFromInt(rapid.Int64Range(1, 500).Draw(t, "qty"))
			price := decimal.New(rapid.Int64Range(0, 1_000_000).Draw(t, "price"), -3)
			inv.Lines = append(inv.Lines, InvoiceLine{LineNo: strconv.Itoa(i + 1), Quantity: qty, UOM: "EA", UnitPrice: price, ItemRefs: []ItemRef{{Qualifier: "VP", Value: "V"}}})
			total = total.Add(qty.Mul(price).Round(2))
		}
		inv.Total = total
		tr, err := Build810(inv)
		if err != nil {
			t.Fatalf("Build810: %v", err)
		}
		if got := segmentsByID(tr.Body, "TDS")[0].Elements[0]; got != total.Shift(2).StringFixed(0) {
			t.Fatalf("TDS01 = %s, want %s", got, total.Shift(2).StringFixed(0))
		}
		inv.Total = total.Add(dec("0.01"))
		if _, err := Build810(inv); !errors.Is(err, ErrOutbound) {
			t.Fatalf("a total one cent off was accepted: %v", err)
		}
	})
}

func ackFixture() Ack855 {
	return Ack855{PONumber: "PO-1001", PODate: "20261015", AckDate: "20261016", AckType: "AC", Lines: []Ack855Line{
		{LineNo: "1", OrderedQty: dec("10"), UOM: "EA", Status: "IA", AckQty: dec("10")},
		{LineNo: "2", OrderedQty: dec("5"), UOM: "EA", Status: "IQ", AckQty: dec("4")},
		{LineNo: "3", OrderedQty: dec("2"), UOM: "EA", Status: "IR", AckQty: dec("0")},
	}}
}

func TestBuild855_WithChangeWritesBAK09AndLines(t *testing.T) {
	tr, err := Build855(ackFixture())
	if err != nil {
		t.Fatalf("Build855: %v", err)
	}
	tr = roundTrip(t, tr)
	if bak := segmentsByID(tr.Body, "BAK")[0]; strings.Join(bak.Elements, "*") != "00*AC*PO-1001*20261015*****20261016" {
		t.Fatalf("BAK = %v (the acknowledgment date belongs in BAK09)", bak.Elements)
	}
	acks := segmentsByID(tr.Body, "ACK")
	if len(acks) != 3 || strings.Join(acks[1].Elements, "*") != "IQ*4*EA" {
		t.Fatalf("ACK = %v", acks)
	}
	if ctt := segmentsByID(tr.Body, "CTT")[0]; ctt.Elements[0] != "3" {
		t.Fatalf("CTT01 = %v", ctt.Elements)
	}
}

func TestBuild855_TypesWithoutDetailAndWithoutChange(t *testing.T) {
	for _, typ := range []string{"AK", "RJ"} {
		a := ackFixture()
		a.AckType, a.Lines = typ, nil
		tr, err := Build855(a)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if got := segmentIDs(tr.Body); got != "BAK" {
			t.Fatalf("%s segments = %s", typ, got)
		}
	}
	a := ackFixture()
	a.AckType = "AD"
	for i := range a.Lines {
		a.Lines[i].Status, a.Lines[i].AckQty = "IA", a.Lines[i].OrderedQty
	}
	if _, err := Build855(a); err != nil {
		t.Fatalf("AD with every line accepted: %v", err)
	}
}

func TestBuild855_Rejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Ack855)
	}{
		{"accepted with different quantity", func(a *Ack855) { a.Lines[0].AckQty = dec("9") }},
		{"change equal to order", func(a *Ack855) { a.Lines[1].AckQty = dec("5") }},
		{"change to zero", func(a *Ack855) { a.Lines[1].AckQty = dec("0") }},
		{"rejected with quantity", func(a *Ack855) { a.Lines[2].AckQty = dec("1") }},
		{"unknown status", func(a *Ack855) { a.Lines[0].Status = "ZZ" }},
		{"unknown type", func(a *Ack855) { a.AckType = "XX" }},
		{"AD with a changed line", func(a *Ack855) { a.AckType = "AD" }},
		{"RD with an accepted line", func(a *Ack855) { a.AckType = "RD" }},
		{"AC without any change", func(a *Ack855) { a.Lines = a.Lines[:1] }},
		{"AK with lines", func(a *Ack855) { a.AckType = "AK" }},
		{"AC without lines", func(a *Ack855) { a.Lines = nil }},
		{"no acknowledgment date", func(a *Ack855) { a.AckDate = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := ackFixture()
			tt.mutate(&a)
			if _, err := Build855(a); !errors.Is(err, ErrOutbound) {
				t.Fatalf("err = %v, want ErrOutbound", err)
			}
		})
	}
}

func TestBuildOutbound_DecodesNormalizesAndRefuses(t *testing.T) {
	doc, _ := json.Marshal(asnFixture())
	tr, normalized, err := BuildOutbound("856", doc)
	if err != nil || tr.SetID != "856" {
		t.Fatalf("BuildOutbound: %v %+v", err, tr)
	}
	// The same document with other spacing normalizes to the same bytes.
	_, again, err := BuildOutbound("856", json.RawMessage(strings.ReplaceAll(string(doc), ",", ", ")))
	if err != nil || string(again) != string(normalized) {
		t.Fatalf("normalization differs: %v\n%s\n%s", err, normalized, again)
	}
	for name, c := range map[string]struct{ set, doc string }{
		"inbound-only set":  {"850", string(doc)},
		"unknown field":     {"856", `{"shipmentId":"X","colour":"red"}`},
		"empty document":    {"810", ``},
		"not an object":     {"855", `"PO-1"`},
		"decimal as a word": {"810", `{"total":"lots"}`},
	} {
		if _, _, err := BuildOutbound(c.set, json.RawMessage(c.doc)); !errors.Is(err, ErrOutbound) {
			t.Errorf("%s: err = %v, want ErrOutbound", name, err)
		}
	}
}

func TestOutboundEnvelope_ControlNumbersAndUsage(t *testing.T) {
	tr, err := Build855(ackFixture())
	if err != nil {
		t.Fatal(err)
	}
	ic, err := OutboundEnvelope(tr, us, them, "P", 7, generated)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Serialize(ic)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	g := back.Groups[0]
	if back.Control != "000000007" || g.Control != "7" || g.FunctionalID != "PR" || g.Transactions[0].Control != "0001" || back.Usage != "P" {
		t.Fatalf("envelope: ISA13=%s GS06=%s GS01=%s ST02=%s usage=%s", back.Control, g.Control, g.FunctionalID, g.Transactions[0].Control, back.Usage)
	}
	if back.Sender != us || back.Receiver != them {
		t.Fatalf("parties: %+v -> %+v", back.Sender, back.Receiver)
	}
	if _, err := OutboundEnvelope(Transaction{SetID: "850"}, us, them, "P", 1, generated); err == nil {
		t.Fatal("850 is inbound only and has no outbound functional id")
	}
	if _, err := OutboundEnvelope(tr, us, them, "P", 1_000_000_000, generated); err == nil {
		t.Fatal("a control number over nine digits must be refused")
	}
}

func TestParse997_ReadsOwn997(t *testing.T) {
	received := Interchange{Delims: DefaultDelims, Sender: them, Receiver: us, Version: "00401", Usage: "T", Control: "000000005"}
	group := Group{FunctionalID: "SH", Control: "42", Version: "004010"}
	ic, err := Ack997(received, group, []TxOutcome{{SetID: "856", Control: "0001", Code: AckAccepted}, {SetID: "856", Control: "0002", Code: AckRejected}}, "000000009", "9", generated)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := Parse997(ic.Groups[0].Transactions[0])
	if err != nil {
		t.Fatalf("Parse997: %v", err)
	}
	want := FunctionalAck{FunctionalID: "SH", GroupControl: "42", Code: AckPartial, Sets: []AckedSet{{"856", "0001", "A"}, {"856", "0002", "R"}}}
	got, _ := json.Marshal(ack)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("ack = %s, want %s", got, exp)
	}
	if InterchangeAckStatus(ack.Code) != StatusAcceptedWithErrors {
		t.Fatalf("AK9 P maps to %s", InterchangeAckStatus(ack.Code))
	}
}

func TestParse997_GroupOnlyAndVariants(t *testing.T) {
	seg := func(id string, el ...string) Segment { return Segment{ID: id, Elements: el} }
	ack, err := Parse997(Transaction{SetID: "997", Body: []Segment{seg("AK1", "IN", "12"), seg("AK9", "A", "1", "1", "1")}})
	if err != nil || len(ack.Sets) != 0 || ack.Code != AckAccepted || ack.GroupControl != "12" {
		t.Fatalf("group-only 997: %+v %v", ack, err)
	}
	// AK3/AK4 detail is skipped and M (authentication failure) is a rejection.
	ack, err = Parse997(Transaction{SetID: "997", Body: []Segment{
		seg("AK1", "PR", "3"), seg("AK2", "855", "0001"), seg("AK3", "BAK", "1"), seg("AK4", "3", "", "7"), seg("AK5", "M", "5"), seg("AK9", "R", "1", "1", "0"),
	}})
	if err != nil || len(ack.Sets) != 1 || ack.Sets[0].Code != AckRejected || InterchangeAckStatus(ack.Code) != StatusRejected {
		t.Fatalf("rejected 997: %+v %v", ack, err)
	}
	bad := map[string][]Segment{
		"no AK1":           {seg("AK9", "A")},
		"no AK9":           {seg("AK1", "IN", "1")},
		"AK2 without AK5":  {seg("AK1", "IN", "1"), seg("AK2", "810", "0001"), seg("AK9", "A")},
		"AK5 without AK2":  {seg("AK1", "IN", "1"), seg("AK5", "A"), seg("AK9", "A")},
		"P on a set":       {seg("AK1", "IN", "1"), seg("AK2", "810", "0001"), seg("AK5", "P"), seg("AK9", "P")},
		"unknown AK9":      {seg("AK1", "IN", "1"), seg("AK9", "Q")},
		"letters in AK102": {seg("AK1", "IN", "1A"), seg("AK9", "A")},
		"two AK1":          {seg("AK1", "IN", "1"), seg("AK1", "IN", "2"), seg("AK9", "A")},
	}
	for name, body := range bad {
		if _, err := Parse997(Transaction{SetID: "997", Body: body}); !errors.Is(err, ErrSyntax) {
			t.Errorf("%s: err = %v, want ErrSyntax", name, err)
		}
	}
	if _, err := Parse997(Transaction{SetID: "850"}); !errors.Is(err, ErrSyntax) {
		t.Fatal("an 850 is not a 997")
	}
}

func TestSSCCCheckDigit_KnownVectors(t *testing.T) {
	// GS1 General Specifications example SSCC (00) 376104250021234569.
	if c, err := SSCCCheckDigit("37610425002123456"); err != nil || c != '9' {
		t.Fatalf("check digit = %c, %v; want 9", c, err)
	}
	if !ValidSSCC("376104250021234569") || ValidSSCC("376104250021234568") {
		t.Fatal("the GS1 example SSCC must validate and a wrong check digit must not")
	}
	if c, err := SSCCCheckDigit("00000000000000001"); err != nil || c != '7' {
		t.Fatalf("check digit = %c, %v; want 7", c, err)
	}
	for _, bad := range []string{"1234", "3761042500212345A", ""} {
		if _, err := SSCCCheckDigit(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSSCC_PropertyGeneratedValidatesAndAnyOtherDigitFails(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seventeen := rapid.StringMatching(`[0-9]{17}`).Draw(t, "seventeen")
		c, err := SSCCCheckDigit(seventeen)
		if err != nil {
			t.Fatal(err)
		}
		if !ValidSSCC(seventeen + string(c)) {
			t.Fatalf("generated %s%c does not validate", seventeen, c)
		}
		other := byte('0' + (int(c-'0')+1+rapid.IntRange(0, 8).Draw(t, "shift"))%10)
		if ValidSSCC(seventeen + string(other)) {
			t.Fatalf("altered check digit %c still validates for %s", other, seventeen)
		}
	})
}
