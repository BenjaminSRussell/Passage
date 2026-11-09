package api

import (
	"context"
	"encoding/json"
	"net/http"
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

// A posted packing slip reaches sales as fulfillment's :record-progress call
// (#496): lines named by lineId, with the pick tracking. Through the real
// handler and store, the report is applied once and orderprogress.recorded,
// which inventory books the issue from, leaves through the outbox with the
// slip number and the tracking; a retry of the call under its key adds none.
func TestRecordProgress_APostedSlipEmitsTheDeliveredEvent(t *testing.T) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "sales-rep", Duties: testkit.ScopeDuties("")})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	product, customer := ids.New(), ids.New()
	line := store.Line{LineNo: 1, ProductID: product, Quantity: decimal.NewFromInt(5), Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(),
		UnitPrice: decimal.NewFromInt(10), DiscPct: decimal.Zero, LineNet: decimal.NewFromInt(50), RequestedShipDate: "2027-03-04"}
	created, _, err := s.CreateOrder(ctx, store.NewOrder{LegalEntityID: testLE, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "USD",
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
	body := `{"kind":"DELIVERED","sourceRef":"PS-0000042","lines":[{"lineId":"` + order.Lines[0].ID + `","quantity":"5","tracking":[` +
		`{"batch":"LOT-1","quantity":"3"},{"batch":"LOT-2","quantity":"2"}]}]}`
	path := orders + "/" + order.ID + ":record-progress"
	headers := map[string]string{idempotencyKey: "fulfillment-packing-slip:ps42:" + order.ID}
	if rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyRecordDelivery), headers); rec.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", rec.Code, rec.Body)
	}
	if rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyRecordDelivery), headers); rec.Code != http.StatusOK {
		t.Fatalf("retry under the same key: %d %s", rec.Code, rec.Body)
	}
	rows, err := pool.Query(context.Background(), `select payload->'data' from outbox where type = $1 and aggregate_id = $2`, salesevents.OrderProgressRecorded, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var emitted []salesevents.OrderProgressData
	for rows.Next() {
		var raw json.RawMessage
		var d salesevents.OrderProgressData
		if err := rows.Scan(&raw); err != nil || json.Unmarshal(raw, &d) != nil {
			t.Fatalf("event %s: %v", raw, err)
		}
		emitted = append(emitted, d)
	}
	if len(emitted) != 1 {
		t.Fatalf("%d orderprogress.recorded events, want 1", len(emitted))
	}
	e := emitted[0]
	if e.Kind != "DELIVERED" || e.SourceRef != "PS-0000042" || e.OrderID != order.ID || len(e.Lines) != 1 ||
		e.Lines[0].LineID != order.Lines[0].ID || e.Lines[0].ProductID != product || e.Lines[0].Quantity != "5" || len(e.Lines[0].Tracking) != 2 ||
		e.Lines[0].Tracking[0].Batch != "LOT-1" || e.Lines[0].Tracking[1].Quantity != "2" || strings.TrimSpace(e.Lines[0].WarehouseID) == "" {
		t.Fatalf("event %+v", e)
	}
}
