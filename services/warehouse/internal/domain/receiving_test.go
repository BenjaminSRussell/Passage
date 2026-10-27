package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func asn3() Receipt {
	line := func(n int, lp string) ReceiptLine {
		return ReceiptLine{LineNo: n, ItemID: "BOLT", LPID: lp, ExpectedQty: d("100"), Weight: d("12"), Volume: d("0.5"), Status: PalletExpected}
	}
	return Receipt{ID: "R1", ASNRef: "ASN-3", TolerancePct: DefaultASNTolerance(), Status: ReceiptOpen,
		Lines: []ReceiptLine{line(1, "LP-1"), line(2, "LP-2"), line(3, "LP-3")}}
}

// AC-31.7: pallet 2 of a 3-pallet ASN received at 95 of 100 records a −5
// (−5%) discrepancy; the other pallets stay expected.
func TestReceive_AC31_7_ShortPalletIsADiscrepancy(t *testing.T) {
	r := asn3()
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.FixedZone("EST", -5*3600))
	got, err := r.Receive(2, d("95"), "clerk", at)
	if err != nil {
		t.Fatal(err)
	}
	if !got.VarianceQty.Equal(d("-5")) || !got.VariancePct.Equal(d("-5")) || !got.Discrepancy {
		t.Fatalf("variance = %s (%s%%) discrepancy=%v", got.VarianceQty, got.VariancePct, got.Discrepancy)
	}
	if got.Reason != "WMS-0107: Received 95 differs from ASN 100 by -5.00%; discrepancy recorded." {
		t.Fatalf("reason = %q", got.Reason)
	}
	if got.ReceivedBy != "clerk" || got.ReceivedAt == nil || got.ReceivedAt.Location() != time.UTC {
		t.Fatalf("receiver = %q at %v", got.ReceivedBy, got.ReceivedAt)
	}
	if r.Status != ReceiptPartial || r.Lines[0].Status != PalletExpected || r.Lines[2].Status != PalletExpected {
		t.Fatalf("receipt = %s, lines %s/%s", r.Status, r.Lines[0].Status, r.Lines[2].Status)
	}
	if r.Discrepancies() != 1 {
		t.Fatalf("discrepancies = %d", r.Discrepancies())
	}
}

// BR-31.10: the tolerance is inclusive; only a variance beyond it is a
// discrepancy, over or short.
func TestReceive_ToleranceBoundary(t *testing.T) {
	cases := []struct {
		qty, tol    string
		discrepancy bool
		pct         string
	}{
		{"98", "2", false, "-2"},
		{"102", "2", false, "2"},
		{"97.9", "2", true, "-2.1"},
		{"102.000001", "2", true, "2"}, // 2.000001% shows as 2.0000 but is beyond the tolerance
		{"103", "2", true, "3"},
		{"100", "0", false, "0"},
		{"100.5", "0", true, "0.5"},
		{"120", "20", false, "20"},
	}
	for _, tc := range cases {
		r := asn3()
		r.TolerancePct = d(tc.tol)
		got, err := r.Receive(1, d(tc.qty), "u", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		want := tc.discrepancy
		if got.Discrepancy != want || !got.VariancePct.Equal(d(tc.pct)) {
			t.Errorf("qty %s tol %s: discrepancy=%v pct=%s, want %v %s", tc.qty, tc.tol, got.Discrepancy, got.VariancePct, want, tc.pct)
		}
		if got.Discrepancy != (got.Reason != "") {
			t.Errorf("qty %s: reason %q does not follow the discrepancy flag", tc.qty, got.Reason)
		}
	}
}

func TestReceive_Refusals(t *testing.T) {
	r := asn3()
	if _, err := r.Receive(1, d("0"), "u", time.Now()); !errors.Is(err, ErrReceivedQty) {
		t.Fatalf("zero quantity: %v", err)
	}
	if _, err := r.Receive(9, d("1"), "u", time.Now()); !errors.Is(err, ErrPalletNotFound) {
		t.Fatalf("unknown pallet: %v", err)
	}
	if _, err := r.Receive(1, d("100"), "u", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := r.Receive(1, d("100"), "u", time.Now())
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeLineReceived || !strings.Contains(de.Detail, "LP-1") {
		t.Fatalf("second receipt: %v", err)
	}
	for _, n := range []int{2, 3} {
		if _, err := r.Receive(n, d("100"), "u", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if r.Status != ReceiptCompleted || r.Discrepancies() != 0 {
		t.Fatalf("all pallets received: status %s, discrepancies %d", r.Status, r.Discrepancies())
	}
}

// The put-away line keeps the LP identity and scales weight and volume to the
// quantity that actually arrived.
func TestPutAwayLine_ScalesToReceivedQuantity(t *testing.T) {
	r := asn3()
	l, err := r.Receive(2, d("95"), "u", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nl := l.PutAwayLine("R1")
	if nl.Direction != DirectionPut || nl.LPs != 1 || !nl.Qty.Equal(d("95")) {
		t.Fatalf("line = %+v", nl)
	}
	if !nl.Weight.Equal(d("11.4")) || !nl.Volume.Equal(d("0.475")) {
		t.Fatalf("weight %s volume %s, want 11.4 and 0.475", nl.Weight, nl.Volume)
	}
	if nl.Attributes["lpId"] != "LP-2" || nl.Attributes["receiptId"] != "R1" || nl.Attributes["asnLine"] != "2" {
		t.Fatalf("attributes = %v", nl.Attributes)
	}
}
