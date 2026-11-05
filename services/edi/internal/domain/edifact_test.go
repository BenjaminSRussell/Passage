package domain

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	edifactUs      = EdifactParty{ID: "PASSAGE", Qualifier: "ZZZ"}
	edifactPartner = EdifactParty{ID: "4012345000009", Qualifier: "14"}
)

func edifactFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "edifact", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// edifactGolden serializes the interchange with line breaks and compares it
// with testdata/edifact/name (UPDATE_GOLDEN=1 rewrites it).
func edifactGolden(t *testing.T, name string, ic EdifactInterchange) string {
	t.Helper()
	got, err := SerializeEdifact(ic, true)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "testdata", "edifact", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want := edifactFixture(t, name); got != want {
		t.Fatalf("%s differs from golden:\n got: %s\nwant: %s", name, got, want)
	}
	return got
}

func outboundEdifact(t *testing.T, m EdifactMessage) EdifactInterchange {
	t.Helper()
	ic, err := EdifactOutbound([]EdifactMessage{m}, edifactUs, edifactPartner, 1, true, fixedAt)
	if err != nil {
		t.Fatal(err)
	}
	return ic
}

func sameJSON(t *testing.T, got, want any) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Fatalf("round trip differs:\n got %s\nwant %s", g, w)
	}
}

func TestEdifact_InboundORDERSParsesMapsAndReserializesByteForByte(t *testing.T) {
	raw := edifactFixture(t, "orders-inbound.edi")
	ic, err := ParseEdifact(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !ic.WithUNA || ic.Control != "42" || !ic.AckRequest || ic.Test || len(ic.Messages) != 1 || ic.Messages[0].Association != "EAN008" {
		t.Fatalf("envelope %+v", ic)
	}
	o, issues := MapORDERS(ic.Messages[0])
	if len(issues) != 0 {
		t.Fatalf("issues %+v", issues)
	}
	if o.PONumber != "PO-EU-881" || o.Purpose != "00" || o.OrderType != "SA" || o.Currency != "EUR" || o.VendorID != "4012345000016" ||
		o.BillToName != "Händler GmbH + Co. KG" || o.ShipToGLN != "4012345000030" || o.RequestedDelivery != "20261112" {
		t.Fatalf("header %+v", o)
	}
	if len(o.Lines) != 2 || o.Lines[0].Description != "Schraube M8:20" || o.Lines[0].UnitPrice != "0.15" || o.Lines[0].ItemRef.Qualifier != "EN" ||
		o.Lines[1].ItemRef.Qualifier != "VP" || o.Lines[1].ItemRef.Value != "SKU-77" || len(o.Charges) != 1 || o.Charges[0].Amount != "12.50" {
		t.Fatalf("lines %+v charges %+v", o.Lines, o.Charges)
	}
	again, err := SerializeEdifact(ic, true)
	if err != nil || again != raw {
		t.Fatalf("reserialized differs (%v):\n%s", err, again)
	}
	// The order survives the partner-side builder too.
	m, err := BuildORDERS(o)
	if err != nil {
		t.Fatal(err)
	}
	back, issues := MapORDERS(m)
	if len(issues) != 0 {
		t.Fatalf("issues %+v", issues)
	}
	sameJSON(t, back, o)
}

func TestMapORDERS_CountMismatchAndMissingItemRef(t *testing.T) {
	ic, err := ParseEdifact(edifactFixture(t, "orders-inbound.edi"))
	if err != nil {
		t.Fatal(err)
	}
	m := ic.Messages[0]
	for i, s := range m.Segments {
		switch s.Tag {
		case "CNT":
			m.Segments[i] = Seg("CNT", "2:3")
		case "PIA":
			m.Segments[i] = Seg("PIA", "5", "X:ZZ")
		}
	}
	_, issues := MapORDERS(m)
	codes := map[string]bool{}
	for _, is := range issues {
		codes[is.Code] = true
	}
	if !codes[CodeCountMismatch] || !codes[CodeNoItemRef] {
		t.Fatalf("issues %+v", issues)
	}
}

func TestBuildDESADV_GoldenAndRoundTrip(t *testing.T) {
	a := asnFixture()
	a.LadingQuantity = 2
	m, err := BuildDESADV(a)
	if err != nil {
		t.Fatal(err)
	}
	raw := edifactGolden(t, "desadv-from-asn.edi", outboundEdifact(t, m))
	ic, err := ParseEdifact(raw)
	if err != nil {
		t.Fatal(err)
	}
	back, err := MapDESADV(ic.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, back, a)
	bad := asnFixture()
	bad.Orders[0].Packs[0].SSCC = "123"
	if _, err := BuildDESADV(bad); !errors.Is(err, ErrOutbound) {
		t.Fatalf("an ASN the 856 refuses must be refused: %v", err)
	}
}

func TestBuildINVOIC_GoldenAndRoundTrip(t *testing.T) {
	inv := invoiceFixture()
	m, err := BuildINVOIC(inv)
	if err != nil {
		t.Fatal(err)
	}
	raw := edifactGolden(t, "invoic-from-invoice.edi", outboundEdifact(t, m))
	ic, err := ParseEdifact(raw)
	if err != nil {
		t.Fatal(err)
	}
	back, err := MapINVOIC(ic.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, back, inv)

	// A partner invoice whose MOA 86 does not add up is refused like an 810.
	msg := ic.Messages[0]
	for i, s := range msg.Segments {
		if s.Tag == "MOA" && s.El(0, 0) == "86" {
			msg.Segments[i] = Seg("MOA", "86:166.92")
		}
	}
	if _, err := MapINVOIC(msg); !errors.Is(err, ErrOutbound) || !strings.Contains(err.Error(), CodeArithmetic) {
		t.Fatalf("err = %v", err)
	}
	inv.CreditNote = true
	if m, _ := BuildINVOIC(inv); m.Segments[0].El(0, 0) != "381" {
		t.Fatal("credit note is BGM 381")
	}
}

func TestCONTRL_GoldenAcceptAndReject(t *testing.T) {
	received, err := ParseEdifact(edifactFixture(t, "orders-inbound.edi"))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := BuildCONTRL(received, []EdifactOutcome{{Ref: "1", Accepted: true}}, "9", fixedAt)
	if err != nil {
		t.Fatal(err)
	}
	if ok.AckRequest || ok.Sender != received.Recipient || ok.Recipient != received.Sender {
		t.Fatalf("CONTRL goes back to the sender and requests nothing: %+v", ok)
	}
	edifactGolden(t, "contrl-accepted.edi", ok)

	rej, err := BuildCONTRL(received, []EdifactOutcome{{Ref: "1", ErrorCode: ContrlErrCount, SegmentPosition: 21}}, "10", fixedAt)
	if err != nil {
		t.Fatal(err)
	}
	raw := edifactGolden(t, "contrl-rejected.edi", rej)
	back, err := ParseEdifact(raw)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := ParseCONTRL(back.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if ack.InterchangeRef != "42" || ack.Action != ContrlRejected || len(ack.Messages) != 1 || ack.Messages[0].ErrorCode != ContrlErrCount || ack.Status(1) != StatusRejected {
		t.Fatalf("%+v", ack)
	}
	accepted, _ := ParseCONTRL(ok.Messages[0])
	if accepted.Status(1) != StatusAccepted {
		t.Fatalf("%+v", accepted)
	}
	partial := ContrlAck{Action: ContrlAcknowledged, Messages: []ContrlMessage{{Ref: "2", Action: ContrlRejected}}}
	if partial.Status(2) != StatusAcceptedWithErrors {
		t.Fatal("one of two rejected is accepted with errors")
	}
	whole, err := RejectInterchangeCONTRL(received, ContrlErrReferences, "11", fixedAt)
	if err != nil || whole.Messages[0].Segments[0].El(4, 0) != ContrlErrReferences || whole.Messages[0].Segments[0].El(3, 0) != ContrlRejected {
		t.Fatalf("%v %+v", err, whole)
	}
	if _, err := BuildCONTRL(back, []EdifactOutcome{{Ref: "1", Accepted: true}}, "12", fixedAt); err == nil {
		t.Fatal("a CONTRL must not be answered with a CONTRL")
	}
	if _, err := BuildCONTRL(received, nil, "13", fixedAt); err == nil {
		t.Fatal("every message needs an outcome")
	}
}

func TestParseEdifact_EnvelopeErrors(t *testing.T) {
	good := edifactFixture(t, "orders-inbound.edi")
	cases := map[string]string{
		"UNT count":       strings.Replace(good, "UNT+21+1'", "UNT+20+1'", 1),
		"UNT ref":         strings.Replace(good, "UNT+21+1'", "UNT+21+2'", 1),
		"UNZ count":       strings.Replace(good, "UNZ+1+42'", "UNZ+2+42'", 1),
		"UNZ ref":         strings.Replace(good, "UNZ+1+42'", "UNZ+1+43'", 1),
		"group":           strings.Replace(good, "UNH+1+", "UNG+ORDERS'\nUNH+1+", 1),
		"no terminator":   strings.TrimSuffix(strings.TrimSpace(good), "'"),
		"syntax version":  strings.Replace(good, "UNOC:3", "UNOC:2", 1),
		"release at end":  good + "?",
		"no UNB":          "UNA:+.? '\nUNH+1+ORDERS:D:96A:UN'\nUNT+2+1'\nUNZ+1+1'\n",
		"unclosed":        strings.Replace(good, "UNT+21+1'\n", "", 1),
		"same delimiters": "UNA++.? '" + good[9:],
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseEdifact(raw); !errors.Is(err, ErrEdifactSyntax) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestSerializeEdifact_EscapesAndCustomDelimiters(t *testing.T) {
	m := EdifactMessage{Type: "ORDERS", Version: "D", Release: "96A", Agency: "UN",
		Segments: []EdifactSegment{{Tag: "FTX", Elements: [][]string{el("AAI"), el(), el(), el("50% off? 'today' + a:b")}}}}
	ic, err := EdifactOutbound([]EdifactMessage{m}, edifactUs, edifactPartner, 5, false, fixedAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []EdifactDelims{DefaultEdifactDelims, {Component: '>', Element: '*', Decimal: ',', Release: '!', Repetition: '^', Segment: '~'}} {
		ic.Delims = d
		raw, err := SerializeEdifact(ic, false)
		if err != nil {
			t.Fatal(err)
		}
		back, err := ParseEdifact(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := back.Messages[0].Segments[0].El(3, 0); got != "50% off? 'today' + a:b" {
			t.Fatalf("value = %q in %s", got, raw)
		}
	}
	ic.WithUNA = false
	if _, err := SerializeEdifact(ic, false); err == nil {
		t.Fatal("custom delimiters without UNA must be refused")
	}
}
