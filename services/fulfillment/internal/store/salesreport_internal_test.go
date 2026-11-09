package store

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
)

// The :record-progress body of a slip matches the shared fixture that sales'
// handler test accepts (#496): one line per order line (two shipment lines of
// one order line summed), serials and batches as tracking ordered by batch and
// serial, one batch picked from two locations merged, the catch weight on the
// line and, with a single entry, on the entry; no location.
func TestSalesReportBodyMatchesTheSalesContract(t *testing.T) {
	dec := decimal.RequireFromString
	str := func(s string) *string { return &s }
	w := dec("51.5")
	lines := []PackingSlipLine{
		{OrderLineID: "5a1c0000-0000-4000-8000-000000000001", ShipmentLineID: "s1", Qty: dec("3")},
		{OrderLineID: "5a1c0000-0000-4000-8000-000000000002", ShipmentLineID: "s2", Qty: dec("10")},
		{OrderLineID: "5a1c0000-0000-4000-8000-000000000003", ShipmentLineID: "s3", Qty: dec("5"), Weight: &w},
		{OrderLineID: "5a1c0000-0000-4000-8000-000000000004", ShipmentLineID: "s4", Qty: dec("4")},
		{OrderLineID: "5a1c0000-0000-4000-8000-000000000004", ShipmentLineID: "s5", Qty: dec("2.5")},
	}
	tracked := map[string][]TrackingEntry{
		"s1": {
			{BatchNo: str("LOT-S"), SerialNo: str("SN-3"), LocationCode: str("A-02"), Qty: dec("1")},
			{SerialNo: str("SN-2"), LocationCode: str("A-01"), Qty: dec("1")},
			{SerialNo: str("SN-1"), LocationCode: str("A-01"), Qty: dec("1")},
		},
		"s2": {
			{BatchNo: str("LOT-2"), LocationCode: str("B-01"), Qty: dec("2")},
			{BatchNo: str("LOT-1"), LocationCode: str("A-01"), Qty: dec("6")},
			{BatchNo: str("LOT-1"), LocationCode: str("A-02"), Qty: dec("2")},
		},
		"s3": {{BatchNo: str("LOT-CW"), LocationCode: str("C-01"), Qty: dec("5")}},
	}
	got, err := json.Marshal(salesReportBody{Kind: "DELIVERED", SourceRef: "PS-0000042", Lines: salesReportLines(lines, tracked)})
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../testdata/sales-progress/record-progress-delivered.json")
	if err != nil {
		t.Fatal(err)
	}
	var g, wnt any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wnt); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, wnt) {
		t.Fatalf("body:\n got %s\nwant %s", got, want)
	}

	// A catch-weight line split over two entries carries no entry weights
	// (fulfillment does not weigh per entry); sales accepts weights on all or none.
	split := salesReportLines([]PackingSlipLine{{OrderLineID: "l", ShipmentLineID: "s", Qty: dec("2"), Weight: &w}},
		map[string][]TrackingEntry{"s": {{BatchNo: str("A"), Qty: dec("1")}, {BatchNo: str("B"), Qty: dec("1")}}})
	if len(split) != 1 || split[0].ActualWeight == nil || *split[0].ActualWeight != "51.5" || len(split[0].Tracking) != 2 ||
		split[0].Tracking[0].ActualWeight != nil || split[0].Tracking[1].ActualWeight != nil {
		t.Fatalf("split catch weight = %+v", split)
	}
}
