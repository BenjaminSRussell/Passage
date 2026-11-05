package store_test

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// cancelledFixture is the packingslip.cancelled of a catch-weight slip that
// the inventory postings consumer offsets the booked delivery with
// (services/inventory/testdata/catchweight-flow, #480).
const cancelledFixture = "../../../inventory/testdata/catchweight-flow/packingslip-cancelled.json"

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestPackingSlipCancelled_MatchesTheInventoryContract: a slip of 5 cases
// weighing 51 kg at 2.00 per kg, posted and cancelled, writes
// packingslip.cancelled with exactly the members of the fixture inventory
// books from: the cancelled slip's number (the source reference its delivery
// was reported under), the shipment's legal entity, the order and order line,
// and the negated cases, weight and cost. Only generated ids and dates differ.
func TestPackingSlipCancelled_MatchesTheInventoryContract(t *testing.T) {
	raw, err := os.ReadFile(cancelledFixture)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	wantLine := want["lines"].([]any)[0].(map[string]any)
	order, le, line := want["order_id"].(string), want["legal_entity_id"].(string), wantLine["order_line_id"].(string)

	s, admin := setup(t)
	ctx := ctxFor(28, "")
	wh := ids.New()
	groups, _ := domain.Release([]domain.ReleaseLine{{OrderID: order, OrderLineID: line, CustomerID: ids.New(), LegalEntityID: le, WarehouseID: wh,
		ProductID: ids.New(), Unit: "CS", Qty: dq("5"), ConfirmedShipDate: today(), ATPOK: true}}, domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
	res, err := s.RunRelease(ctx, wh, groups, idem("rel"))
	if err != nil || len(res.Shipments) != 1 {
		t.Fatalf("RunRelease: %+v, %v", res, err)
	}
	sh := res.Shipments[0]
	cost := map[string]store.Cost{sh.Lines[0].ID: {UnitCost: dq("2"), CostSnapshotID: "snap-cw", Weight: dq("51")}}
	packed := pickAndPack(t, s, ctx, sh)
	slip, err := s.PostPackingSlip(ctx, sh.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: cost}, idem("post"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel")); err != nil {
		t.Fatal(err)
	}
	cancelled := shipmentEvents[map[string]any](t, admin, sh.ID, fulevents.PackingSlipCancelled)
	if len(cancelled) != 1 {
		t.Fatalf("%d packingslip.cancelled events", len(cancelled))
	}
	got := cancelled[0]
	gotLine := got["lines"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(keysOf(got), keysOf(want)) || !reflect.DeepEqual(keysOf(gotLine), keysOf(wantLine)) {
		t.Fatalf("members differ from the fixture:\n got %v / %v\nwant %v / %v", keysOf(got), keysOf(gotLine), keysOf(want), keysOf(wantLine))
	}
	if got["packing_slip_id"] != slip.ID || got["packing_slip_number"] != slip.Number || slip.Number == "" {
		t.Errorf("slip %v %v, want %s %s", got["packing_slip_id"], got["packing_slip_number"], slip.ID, slip.Number)
	}
	for _, k := range []string{"order_id", "legal_entity_id", "currency"} {
		if !reflect.DeepEqual(got[k], want[k]) {
			t.Errorf("%s = %v, fixture %v", k, got[k], want[k])
		}
	}
	for _, k := range []string{"order_line_id", "qty", "weight", "cost_amount"} {
		if !reflect.DeepEqual(gotLine[k], wantLine[k]) {
			t.Errorf("line %s = %v, fixture %v", k, gotLine[k], wantLine[k])
		}
	}
	if !reflect.DeepEqual(got["journal"], want["journal"]) {
		t.Errorf("journal %v, fixture %v", got["journal"], want["journal"])
	}
}
