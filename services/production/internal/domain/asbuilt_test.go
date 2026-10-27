package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

const (
	motorItem = "0190f2a4-7b1c-7d2e-8f3a-000000000031"
	boltItem  = "0190f2a4-7b1c-7d2e-8f3a-000000000032"
)

// AC-21.14 shape: an order for 2 units that consumed 2 motors and 8 bolts.
func asBuiltFacts() AsBuiltFacts {
	return AsBuiltFacts{Status: Started, OrderQty: d("2"), Lines: []OrderLine{
		{LineNo: "10", ItemID: motorItem, QtyConsumed: d("2")},
		{LineNo: "20", ItemID: boltItem, QtyConsumed: d("5")},
		{LineNo: "30.1", ItemID: boltItem, QtyConsumed: d("3")},
	}}
}

func TestCheckAsBuilt(t *testing.T) {
	motor := AsBuilt{FinishedSerial: "S-1", ComponentItemID: motorItem, Tracking: TrackSerial, ComponentSerial: "M-1", Qty: decimal.NewFromInt(1)}
	tests := []struct {
		name string
		a    AsBuilt
		mut  func(f *AsBuiltFacts)
		code string
	}{
		{"first motor", motor, nil, ""},
		{"ReportedAsFinished keeps assigning", motor, func(f *AsBuiltFacts) { f.Status = ReportedAsFinished }, ""},
		{"Released has consumed nothing", motor, func(f *AsBuiltFacts) { f.Status = Released }, CodeAsBuiltState},
		{"Ended is closed", motor, func(f *AsBuiltFacts) { f.Status = Ended }, CodeAsBuiltState},
		{"a component off the BOM", AsBuilt{FinishedSerial: "S-1", ComponentItemID: "0190f2a4-7b1c-7d2e-8f3a-000000000099",
			Tracking: TrackSerial, ComponentSerial: "X", Qty: decimal.NewFromInt(1)}, nil, CodeAsBuiltNotOnBOM},
		{"a third motor of two consumed", motor, func(f *AsBuiltFacts) { f.ComponentAssigned = d("2") }, CodeAsBuiltExceedsConsumed},
		{"bolts count over every line of the item", AsBuilt{FinishedSerial: "S-1", ComponentItemID: boltItem, Tracking: TrackBatch,
			ComponentSerial: "B-1", Qty: d("8")}, nil, ""},
		{"bolts beyond the consumption", AsBuilt{FinishedSerial: "S-1", ComponentItemID: boltItem, Tracking: TrackBatch,
			ComponentSerial: "B-1", Qty: d("8.000001")}, nil, CodeAsBuiltExceedsConsumed},
		{"a third finished serial for 2 units", motor, func(f *AsBuiltFacts) { f.FinishedCount = 2 }, CodeAsBuiltFinishedCount},
		{"a known finished serial never counts again", motor, func(f *AsBuiltFacts) { f.FinishedCount, f.FinishedKnown = 2, true }, ""},
		{"a fractional order quantity rounds up", motor, func(f *AsBuiltFacts) { f.OrderQty, f.FinishedCount = d("2.5"), 2 }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := asBuiltFacts()
			if tt.mut != nil {
				tt.mut(&f)
			}
			err := CheckAsBuilt(tt.a, f)
			switch {
			case tt.code == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tt.code != "" && (err == nil || err.(*Error).Code != tt.code):
				t.Fatalf("got %v, want %s", err, tt.code)
			}
		})
	}
}

func TestTrackingAndReferenceTypes(t *testing.T) {
	if !TrackSerial.Valid() || !TrackBatch.Valid() || Tracking("lot").Valid() {
		t.Fatal("tracking must be serial or batch")
	}
	if !ReworkRefOrder.Valid() || !ReworkRefBatch.Valid() || !ReworkRefSerial.Valid() || ReworkRefType("lot").Valid() {
		t.Fatal("a rework reference is an order, a batch or a serial")
	}
}
