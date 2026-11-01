package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

func scopedPrincipalCtx(le string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "planner", LegalEntityID: le, Duties: testkit.ScopeDuties(le)})
}

func TestPostgres_ConfiguredLineIsStoredConfirmedAndLinkedToProductionOnce(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(appOf(t, pool))
	leA, leB, customer := ids.New(), ids.New(), ids.New()
	ctxA, ctxB := scopedPrincipalCtx(leA), scopedPrincipalCtx(leB)
	configuration, productionOrder := ids.New(), ids.New()

	configured := Line{LineNo: 1, ProductID: ids.New(), Quantity: d(2), Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: d(100), DiscPct: d(0), LineNet: d(200),
		RequestedShipDate: "2027-03-04", ConfigurationID: &configuration}
	plain := Line{LineNo: 2, ProductID: ids.New(), Quantity: d(1), Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: d(10), DiscPct: d(0), LineNet: d(10), RequestedShipDate: "2027-03-04"}
	order, _, err := s.CreateOrder(ctxA, NewOrder{LegalEntityID: leA, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "USD",
		RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: "cfg-1", Lines: []Line{configured, plain}})
	if err != nil {
		t.Fatal(err)
	}
	if order.Lines[0].ConfigurationID == nil || *order.Lines[0].ConfigurationID != configuration || order.Lines[1].ConfigurationID != nil {
		t.Fatalf("configuration must be kept on the configured line only: %+v", order.Lines)
	}

	confirmed, err := s.ConfirmOrder(ctxA, order.ID, order.Version, "planner", time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC))
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirm: %+v %v", confirmed.Confirmed, err)
	}
	var data salesevents.OrderConfirmedData
	if err := json.Unmarshal(outboxData(t, pool, order.ID, salesevents.OrderConfirmed), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Lines) != 2 || data.Lines[0].ConfigurationID == nil || *data.Lines[0].ConfigurationID != configuration || data.Lines[1].ConfigurationID != nil {
		t.Fatalf("order.confirmed must name the configuration of the configured line: %+v", data.Lines)
	}

	link := LineProduction{OrderID: order.ID, LineID: confirmed.Order.Lines[0].ID, ConfigurationID: configuration, ProductionOrderID: productionOrder, Actor: "planner"}
	if err := s.RecordLineProduction(ctxB, link); err == nil {
		t.Fatal("another legal entity must not link production to the line")
	}
	if err := s.RecordLineProduction(ctxA, link); err != nil {
		t.Fatal(err)
	}
	second := link
	second.ProductionOrderID = ids.New()
	if err := s.RecordLineProduction(ctxA, second); err != nil {
		t.Fatalf("a repeat keeps the first link: %v", err)
	}
	wrong := link
	wrong.LineID, wrong.ConfigurationID = confirmed.Order.Lines[1].ID, configuration
	if err := s.RecordLineProduction(ctxA, wrong); err == nil {
		t.Fatal("a line without that configuration cannot be linked")
	}
	again, err := s.GetOrder(ctxA, order.ID)
	if err != nil || again.Lines[0].ProductionOrderID == nil || *again.Lines[0].ProductionOrderID != productionOrder || again.Lines[1].ProductionOrderID != nil {
		t.Fatalf("the first link stands: %+v %v", again.Lines, err)
	}
	if _, err := s.GetOrder(ctxB, order.ID); err == nil {
		t.Fatal("another legal entity must not read the order")
	}
	var visibleToB, visibleToA int
	count := func(ctx context.Context, n *int) error {
		return dbx.WithTenantTx(ctx, appOf(t, pool), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*) from order_line_production`).Scan(n)
		})
	}
	if err := count(ctxB, &visibleToB); err != nil || visibleToB != 0 {
		t.Fatalf("order_line_production rows visible to another legal entity: %d %v", visibleToB, err)
	}
	if err := count(ctxA, &visibleToA); err != nil || visibleToA != 1 {
		t.Fatalf("order_line_production rows visible to the owning legal entity: %d %v", visibleToA, err)
	}
}
