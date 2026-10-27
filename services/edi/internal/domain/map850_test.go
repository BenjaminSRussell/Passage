package domain

import (
	"reflect"
	"testing"
)

func goodPO850Body() []Segment {
	return []Segment{
		{ID: "BEG", Elements: []string{"00", "SA", "PO-1001", "", "20260101"}},
		{ID: "CUR", Elements: []string{"BY", "EUR"}},
		{ID: "DTM", Elements: []string{"002", "20260115"}},
		{ID: "N1", Elements: []string{"ST", "ACME DC", "92", "0614141000001"}},
		{ID: "PO1", Elements: []string{"1", "10", "EA", "12.50", "PE", "BP", "BUY-1", "VP", "VEND-1"}},
		{ID: "PID", Elements: []string{"F", "", "", "", "Widget"}},
		{ID: "PO1", Elements: []string{"2", "5", "CA", "3", "PE", "UP", "0123456789012"}},
		{ID: "CTT", Elements: []string{"2"}},
	}
}

func issueCodes(issues []Issue) []string {
	var codes []string
	for _, i := range issues {
		codes = append(codes, i.Code)
	}
	return codes
}

func TestMap850_MapsHeaderAndLines(t *testing.T) {
	order, issues := Map850(Transaction{SetID: "850", Control: "0001", Body: goodPO850Body()})
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	want := Order{Purpose: "00", OrderType: "SA", PONumber: "PO-1001", PODate: "20260101", Currency: "EUR",
		RequestedDelivery: "20260115", ShipToName: "ACME DC", ShipToGLN: "0614141000001",
		Lines: []OrderLine{
			{LineNo: "1", Quantity: "10", UOM: "EA", UnitPrice: "12.5", PriceBasis: "PE", ItemRef: &ItemRef{Qualifier: "BP", Value: "BUY-1"}, Description: "Widget"},
			{LineNo: "2", Quantity: "5", UOM: "CA", UnitPrice: "3", PriceBasis: "PE", ItemRef: &ItemRef{Qualifier: "UP", Value: "0123456789012"}},
		}}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order mismatch:\n got %+v\nwant %+v", order, want)
	}
}

func TestMap850_MapsReferencesBillToAndCharges(t *testing.T) {
	body := goodPO850Body()
	extra := []Segment{
		{ID: "REF", Elements: []string{"DP", "042"}},
		{ID: "REF", Elements: []string{"IA", "V-77"}},
		{ID: "SAC", Elements: []string{"C", "D240", "", "", "1250"}},
		{ID: "SAC", Elements: []string{"A", "C310", "", "", "500", "", "", "", "", "", "", "", "", "", "Promo"}},
		{ID: "N1", Elements: []string{"BT", "ACME HQ", "UL", "0614141000002"}},
	}
	body = append(body[:3], append(extra, body[3:]...)...)
	order, issues := Map850(Transaction{SetID: "850", Control: "0001", Body: body})
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	if order.Department != "042" || order.VendorID != "V-77" || order.BillToName != "ACME HQ" || order.BillToGLN != "0614141000002" {
		t.Fatalf("references or bill-to not mapped: %+v", order)
	}
	want := []Charge{{Indicator: "C", Code: "D240", Amount: "12.50"}, {Indicator: "A", Code: "C310", Amount: "5.00", Description: "Promo"}}
	if !reflect.DeepEqual(order.Charges, want) {
		t.Fatalf("charges = %+v, want %+v (SAC05 has two implied decimals)", order.Charges, want)
	}
}

func TestMap850_Rules(t *testing.T) {
	withBody := func(edit func([]Segment) []Segment) Transaction {
		return Transaction{SetID: "850", Control: "0001", Body: edit(goodPO850Body())}
	}
	tests := []struct {
		name      string
		tx        Transaction
		wantCodes []string
		wantRej   bool
	}{
		{"vendor part wins over UPC when both are present", withBody(func(b []Segment) []Segment {
			b[6] = Segment{ID: "PO1", Elements: []string{"2", "5", "CA", "3", "PE", "UP", "0123", "VP", "V-9"}}
			return b
		}), nil, false},
		{"no item cross-reference is a finding, not a rejection", withBody(func(b []Segment) []Segment {
			b[4] = Segment{ID: "PO1", Elements: []string{"1", "10", "EA", "12.50", "PE"}}
			return b
		}), []string{CodeNoItemRef}, false},
		{"CTT01 that disagrees with the PO1 count rejects", withBody(func(b []Segment) []Segment {
			b[7] = Segment{ID: "CTT", Elements: []string{"3"}}
			return b
		}), []string{CodeCountMismatch}, true},
		{"unknown purpose code rejects", withBody(func(b []Segment) []Segment {
			b[0] = Segment{ID: "BEG", Elements: []string{"09", "SA", "PO-1001", "", "20260101"}}
			return b
		}), []string{CodeMalformed}, true},
		{"non-positive quantity rejects", withBody(func(b []Segment) []Segment {
			b[4] = Segment{ID: "PO1", Elements: []string{"1", "0", "EA", "12.50", "PE", "BP", "X"}}
			return b
		}), []string{CodeMalformed}, true},
		{"currency that is not ISO 4217 rejects", withBody(func(b []Segment) []Segment {
			b[1] = Segment{ID: "CUR", Elements: []string{"BY", "euro"}}
			return b
		}), []string{CodeMalformed}, true},
		{"SAC with a decimal point in the N2 amount rejects", withBody(func(b []Segment) []Segment {
			return append(b[:7:7], Segment{ID: "SAC", Elements: []string{"C", "D240", "", "", "12.50"}}, b[7])
		}), []string{CodeMalformed}, true},
		{"missing CTT rejects", withBody(func(b []Segment) []Segment { return b[:7] }), []string{CodeMalformed}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, issues := Map850(tc.tx)
			if got := issueCodes(issues); !reflect.DeepEqual(got, tc.wantCodes) {
				t.Fatalf("codes = %v, want %v", got, tc.wantCodes)
			}
			for _, i := range issues {
				if i.Reject != tc.wantRej {
					t.Fatalf("issue %s reject = %v, want %v", i.Code, i.Reject, tc.wantRej)
				}
			}
		})
	}
}
