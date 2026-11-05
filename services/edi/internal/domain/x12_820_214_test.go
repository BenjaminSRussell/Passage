package domain

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var fixedAt = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

func remittance() Remittance {
	return Remittance{TraceNumber: "ACH0001234", PaymentDate: "20261105", Method: "ACH", Currency: "USD", Amount: dec("1166.10"),
		Payer: Location{Name: "Demo Distributor", GLN: "0614141000012"}, Payee: Location{Name: "Demo Manufacturer", GLN: "0614141000029"},
		Items: []RemittanceItem{
			{InvoiceNumber: "INV-2026-0001", InvoiceDate: "20261021", Paid: dec("184.10"), Invoiced: dec("184.10"), Discount: dec("0.00")},
			{InvoiceNumber: "INV-2026-0002", InvoiceDate: "20261022", Paid: dec("1000.00"), Invoiced: dec("1020.00"), Discount: dec("20.00")},
			{InvoiceNumber: "CM-2026-0003", Paid: dec("-18.00"), Invoiced: dec("-18.00"), Discount: dec("0.00")},
		}}
}

func shipmentStatus() ShipmentStatus {
	return ShipmentStatus{ProNumber: "PRO778812", ShipmentID: "SHP-0042", SCAC: "ODFL", BOL: "BOL-9001", PONumber: "PO-4711",
		ShipFrom: Location{Name: "Demo Manufacturer DC", GLN: "0614141000029"}, ShipTo: Location{Name: "Demo Distributor East", GLN: "0614141000012"},
		Events: []StatusEvent{
			{Code: "AF", Reason: "NS", Date: "20261021", Time: "0915", City: "Columbus", State: "OH", Country: "US"},
			{Code: "X6", Reason: "NS", Date: "20261022", Time: "1400", City: "Pittsburgh", State: "PA", Country: "US"},
			{Code: "D1", Reason: "NS", Date: "20261023", Time: "1032"},
		}}
}

// x12RoundTrip serializes tr in the outbound envelope and parses it back, as a
// partner's translator would read it.
func x12RoundTrip(t *testing.T, tr Transaction) Transaction {
	t.Helper()
	env, err := OutboundEnvelope(tr, Party{Qualifier: "ZZ", ID: "PASSAGE"}, Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"}, "T", 7, fixedAt)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Serialize(env)
	if err != nil {
		t.Fatal(err)
	}
	ic, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	g := ic.Groups[0]
	if g.FunctionalID != FunctionalIDOf(tr.SetID) || len(g.Transactions) != 1 {
		t.Fatalf("group %s with %d sets", g.FunctionalID, len(g.Transactions))
	}
	return g.Transactions[0]
}

func TestBuild820_GoldenAndRoundTrip(t *testing.T) {
	r := remittance()
	tr, err := Build820(r)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "820-remittance.x12", tr, "820")
	got, issues := Map820(x12RoundTrip(t, tr))
	if len(issues) != 0 {
		t.Fatalf("issues: %+v", issues)
	}
	if !reflect.DeepEqual(got, r) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, r)
	}
}

func TestBuild820_RefusesAmountThatDiffersFromItems(t *testing.T) {
	cases := map[string]func(*Remittance){
		"sum":      func(r *Remittance) { r.Amount = dec("1166.11") },
		"method":   func(r *Remittance) { r.Method = "CASH" },
		"date":     func(r *Remittance) { r.PaymentDate = "2026-11-05" },
		"zeroPaid": func(r *Remittance) { r.Items[0].Paid = dec("0") },
		"noItems":  func(r *Remittance) { r.Items = nil },
		"gln":      func(r *Remittance) { r.Payee.GLN = "123" },
		"currency": func(r *Remittance) { r.Currency = "usd" },
		"decimals": func(r *Remittance) { r.Amount = dec("1166.100001") },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			r := remittance()
			mut(&r)
			if _, err := Build820(r); !errors.Is(err, ErrOutbound) {
				t.Fatalf("err = %v, want ErrOutbound", err)
			}
		})
	}
}

func TestMap820_InboundFixtureAndArithmeticReject(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "x12", "820-inbound-00501.x12"))
	if err != nil {
		t.Fatal(err)
	}
	ic, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	r, issues := Map820(ic.Groups[0].Transactions[0])
	if len(issues) != 0 || !r.Amount.Equal(dec("250.00")) || r.Method != "CHK" || len(r.Items) != 2 || r.Items[1].InvoiceNumber != "INV-2026-0005" {
		t.Fatalf("%+v %+v", r, issues)
	}
	bad := ic.Groups[0].Transactions[0]
	bad.Body[0].Elements[1] = "251.00"
	if _, issues := Map820(bad); len(issues) != 1 || issues[0].Code != CodeArithmetic || !issues[0].Reject {
		t.Fatalf("issues = %+v", issues)
	}
	if _, issues := Map820(Transaction{SetID: "820"}); len(issues) < 3 {
		t.Fatalf("empty 820 issues = %+v", issues)
	}
}

func TestBuild214_GoldenAndRoundTrip(t *testing.T) {
	s := shipmentStatus()
	tr, err := Build214(s)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "214-status.x12", tr, "214")
	got, issues := Map214(x12RoundTrip(t, tr))
	if len(issues) != 0 || !reflect.DeepEqual(got, s) {
		t.Fatalf("round trip: %+v %+v", got, issues)
	}
}

func TestBuild214_Refuses(t *testing.T) {
	cases := map[string]func(*ShipmentStatus){
		"scac":     func(s *ShipmentStatus) { s.SCAC = "odfl" },
		"noEvents": func(s *ShipmentStatus) { s.Events = nil },
		"code":     func(s *ShipmentStatus) { s.Events[0].Code = "DELIVERED" },
		"time":     func(s *ShipmentStatus) { s.Events[0].Time = "9:15" },
		"shipment": func(s *ShipmentStatus) { s.ShipmentID = "" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			s := shipmentStatus()
			mut(&s)
			if _, err := Build214(s); !errors.Is(err, ErrOutbound) {
				t.Fatalf("err = %v, want ErrOutbound", err)
			}
		})
	}
	if _, issues := Map214(Transaction{SetID: "214", Body: []Segment{{ID: "AT7", Elements: []string{"D1"}}}}); len(issues) < 2 {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestBuildOutbound_820And214(t *testing.T) {
	for set, doc := range map[string]any{"820": remittance(), "214": shipmentStatus()} {
		raw, _ := json.Marshal(doc)
		tr, _, err := BuildOutbound(set, raw)
		if err != nil || tr.SetID != set {
			t.Fatalf("%s: %v", set, err)
		}
	}
	if FunctionalIDOf("820") != "RA" || FunctionalIDOf("214") != "QM" {
		t.Fatal("functional ids")
	}
}
