package store

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// deliveredFixture is the DELIVERED orderprogress.recorded of a catch-weight
// line that the inventory postings consumer is tested with
// (services/inventory/testdata/catchweight-flow, #480).
const deliveredFixture = "../../../inventory/testdata/catchweight-flow/orderprogress-delivered.json"

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestPostgres_CatchWeightDeliveryMatchesTheInventoryContract: the event sales
// writes for a delivery of 5 cases weighing 51 kg has exactly the members of
// the fixture inventory books from, with the same quantities, weights, units,
// product, site, warehouse and currency. Only the generated ids differ.
func TestPostgres_CatchWeightDeliveryMatchesTheInventoryContract(t *testing.T) {
	raw, err := os.ReadFile(deliveredFixture)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	wantLine := want["lines"].([]any)[0].(map[string]any)

	s, pool, ctx := migrated(t)
	in := catchWeightOrder(t, want["legal_entity_id"].(string), ids.New(), 10)
	in.Lines[0].ProductID, in.Lines[0].SiteID, in.Lines[0].WarehouseID = wantLine["product_id"].(string), wantLine["site_id"].(string), wantLine["warehouse_id"].(string)
	o := confirmed(t, s, ctx, in)
	if _, err := report(t, s, ctx, o.ID, domain.ProgressDelivered, "PS-W", 5, weigh("51")); err != nil {
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
	for _, k := range []string{"legal_entity_id", "currency", "kind", "source_ref", "previous_status", "order_status", "actor", "version"} {
		if !reflect.DeepEqual(got[k], want[k]) {
			t.Errorf("%s = %v, fixture %v", k, got[k], want[k])
		}
	}
	for _, k := range []string{"line_no", "quantity", "delivered_qty", "invoiced_qty", "line_status", "reported_weight", "delivered_weight", "product_id", "unit", "site_id", "warehouse_id"} {
		if !reflect.DeepEqual(gotLine[k], wantLine[k]) {
			t.Errorf("line %s = %v, fixture %v", k, gotLine[k], wantLine[k])
		}
	}
}
