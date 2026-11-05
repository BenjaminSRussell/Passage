package store

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// trackedFixture is the DELIVERED orderprogress.recorded of a serial-tracked
// line that the inventory postings consumer issues batch and serial by
// batch and serial (services/inventory/testdata/tracking-flow, #480).
const trackedFixture = "../../../inventory/testdata/tracking-flow/orderprogress-delivered-tracked.json"

func plainOrder(t *testing.T, le, customer string, qty int64) NewOrder {
	t.Helper()
	in := catchWeightOrder(t, le, customer, qty)
	net, err := domain.LineNetFor(d(qty), d(0), d(2), d(0), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	in.Lines[0].CatchWeight, in.Lines[0].Unit, in.Lines[0].LineNet = nil, "EA", net
	return in
}

func tracking(entries ...[3]string) []ProgressTracking {
	out := make([]ProgressTracking, 0, len(entries))
	for _, e := range entries {
		out = append(out, ProgressTracking{Batch: e[0], Serial: e[1], Quantity: decimal.RequireFromString(e[2])})
	}
	return out
}

// TestPostgres_TrackedDeliveryMatchesTheInventoryContract: a delivery of 3
// serials writes exactly the members of the fixture inventory books from,
// with the entries in the order they were reported.
func TestPostgres_TrackedDeliveryMatchesTheInventoryContract(t *testing.T) {
	raw, err := os.ReadFile(trackedFixture)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	wantLine := want["lines"].([]any)[0].(map[string]any)

	s, pool, ctx := migrated(t)
	in := plainOrder(t, want["legal_entity_id"].(string), ids.New(), 10)
	in.Lines[0].ProductID, in.Lines[0].SiteID, in.Lines[0].WarehouseID = wantLine["product_id"].(string), wantLine["site_id"].(string), wantLine["warehouse_id"].(string)
	o := confirmed(t, s, ctx, in)
	line := ProgressLineInput{LineNo: 1, Quantity: d(3), Tracking: tracking(
		[3]string{"LOT-7", "SN-0002", "1"}, [3]string{"LOT-7", "SN-0003", "1"}, [3]string{"LOT-8", "SN-0005", "1"})}
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-T", "k-t", "h-t", line), time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	prog := eventsOf(t, pool, salesevents.OrderProgressRecorded, o.ID)
	if len(prog) != 1 {
		t.Fatalf("%d progress events", len(prog))
	}
	var got map[string]any
	if err := json.Unmarshal(prog[0], &got); err != nil {
		t.Fatal(err)
	}
	gotLine := got["lines"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(keysOf(got), keysOf(want)) || !reflect.DeepEqual(keysOf(gotLine), keysOf(wantLine)) {
		t.Fatalf("members differ from the fixture:\n got %v / %v\nwant %v / %v", keysOf(got), keysOf(gotLine), keysOf(want), keysOf(wantLine))
	}
	for _, k := range []string{"line_no", "quantity", "delivered_qty", "invoiced_qty", "line_status", "product_id", "unit", "site_id", "warehouse_id", "tracking"} {
		if !reflect.DeepEqual(gotLine[k], wantLine[k]) {
			t.Errorf("line %s = %v, fixture %v", k, gotLine[k], wantLine[k])
		}
	}
}

// A catch-weight delivery split over batches carries each entry's weight; a
// plain line refuses entry weights.
func TestPostgres_TrackedCatchWeightDeliveryCarriesEntryWeights(t *testing.T) {
	s, pool, ctx := migrated(t)
	o := confirmed(t, s, ctx, catchWeightOrder(t, ids.New(), ids.New(), 10))
	entries := tracking([3]string{"B1", "", "2"}, [3]string{"B2", "", "3"})
	entries[0].ActualWeight, entries[1].ActualWeight = weigh("20.5"), weigh("30.5")
	line := ProgressLineInput{LineNo: 1, Quantity: d(5), ActualWeight: weigh("51"), Tracking: entries}
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-CW", "k-cw", "h-cw", line), time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	var p salesevents.OrderProgressData
	prog := eventsOf(t, pool, salesevents.OrderProgressRecorded, o.ID)
	if len(prog) != 1 || json.Unmarshal(prog[0], &p) != nil || len(p.Lines) != 1 {
		t.Fatalf("progress %s", prog)
	}
	want := []salesevents.ProgressTracking{{Batch: "B1", Quantity: "2", ReportedWeight: "20.5"}, {Batch: "B2", Quantity: "3", ReportedWeight: "30.5"}}
	if l := p.Lines[0]; l.ReportedWeight != "51" || !reflect.DeepEqual(l.Tracking, want) {
		t.Fatalf("line %+v, want weight 51 and tracking %+v", l, want)
	}

	plain := confirmed(t, s, ctx, plainOrder(t, ids.New(), ids.New(), 10))
	bad := tracking([3]string{"B1", "", "2"})
	bad[0].ActualWeight = weigh("4")
	_, _, err := s.RecordProgress(ctx, progressIn(plain.ID, domain.ProgressDelivered, "PS-P", "k-p", "h-p",
		ProgressLineInput{LineNo: 1, Quantity: d(2), Tracking: bad}), time.Now().UTC().Truncate(time.Microsecond))
	var prob *httpx.Problem
	if !errors.As(err, &prob) || prob.Status != http.StatusUnprocessableEntity || prob.Code != domain.CodeCatchWeightInput {
		t.Fatalf("entry weight on a plain line: %v", err)
	}
}
