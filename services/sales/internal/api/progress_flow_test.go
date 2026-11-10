package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

const trackedFixture = "../../../inventory/testdata/tracking-flow/orderprogress-delivered-tracked.json"

// A posted packing slip reaches sales as fulfillment's :record-progress call
// (#496): lines named by lineId, with the pick tracking. Through the real
// handler and store, the report is applied once and orderprogress.recorded,
// which inventory books the issue from, leaves through the outbox with the
// slip number and the tracking; a retry of the call under its key adds none.
func TestRecordProgress_APostedSlipEmitsTheDeliveredEvent(t *testing.T) {
	raw, err := os.ReadFile(trackedFixture)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	wantLine := want["lines"].([]any)[0].(map[string]any)
	le := want["legal_entity_id"].(string)
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "sales-rep", Duties: testkit.ScopeDuties("")})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	customer := ids.New()
	line := store.Line{LineNo: 1, ProductID: wantLine["product_id"].(string), Quantity: decimal.NewFromInt(5), Unit: "EA", SiteID: wantLine["site_id"].(string),
		WarehouseID: wantLine["warehouse_id"].(string), UnitPrice: decimal.NewFromInt(10), DiscPct: decimal.Zero, LineNet: decimal.NewFromInt(50), RequestedShipDate: "2027-03-04"}
	created, _, err := s.CreateOrder(ctx, store.NewOrder{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "USD",
		RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: ids.New(), OrderDate: time.Date(2027, 2, 20, 0, 0, 0, 0, time.UTC),
		Actor: "sales-rep", Lines: []store.Line{line}})
	if err != nil {
		t.Fatal(err)
	}
	confirmation, err := s.ConfirmOrder(ctx, created.ID, created.Version, "sales-rep", time.Now())
	if err != nil || !confirmation.Confirmed {
		t.Fatalf("confirm: %v %+v", err, confirmation.Evaluation)
	}
	order := confirmation.Order
	var entries []string
	for _, e := range wantLine["tracking"].([]any) {
		m := e.(map[string]any)
		entries = append(entries, `{"batch":"`+m["batch"].(string)+`","serial":"`+m["serial"].(string)+`","quantity":"`+m["quantity"].(string)+`"}`)
	}
	body := `{"kind":"DELIVERED","sourceRef":"` + want["source_ref"].(string) + `","lines":[{"lineId":"` + order.Lines[0].ID + `","quantity":"` +
		wantLine["quantity"].(string) + `","tracking":[` + strings.Join(entries, ",") + `]}]}`
	path := orders + "/" + order.ID + ":record-progress"
	headers := map[string]string{idempotencyKey: "fulfillment-packing-slip:ps42:" + order.ID}
	p := principal(dutyRecordDelivery)
	p.LegalEntityID = le
	if rec := serveBody(t, s, http.MethodPost, path, body, p, headers); rec.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", rec.Code, rec.Body)
	}
	if rec := serveBody(t, s, http.MethodPost, path, body, p, headers); rec.Code != http.StatusOK {
		t.Fatalf("retry under the same key: %d %s", rec.Code, rec.Body)
	}
	rows, err := pool.Query(context.Background(), `select payload->'data' from outbox where type = $1 and aggregate_id = $2`, salesevents.OrderProgressRecorded, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var emitted []map[string]any
	for rows.Next() {
		var d map[string]any
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		emitted = append(emitted, d)
	}
	if len(emitted) != 1 {
		t.Fatalf("%d orderprogress.recorded events, want 1", len(emitted))
	}
	got := emitted[0]
	gotLine := got["lines"].([]any)[0].(map[string]any)
	if got["order_id"] != order.ID || gotLine["line_id"] != order.Lines[0].ID {
		t.Fatalf("event %+v is not for the order and line reported", got)
	}
	for _, k := range []string{"legal_entity_id", "currency", "kind", "source_ref"} {
		if !reflect.DeepEqual(got[k], want[k]) {
			t.Errorf("event %s = %v, the fixture inventory books from has %v", k, got[k], want[k])
		}
	}
	for _, k := range []string{"line_no", "quantity", "product_id", "unit", "site_id", "warehouse_id", "tracking"} {
		if !reflect.DeepEqual(gotLine[k], wantLine[k]) {
			t.Errorf("line %s = %v, the fixture inventory books from has %v", k, gotLine[k], wantLine[k])
		}
	}
}
