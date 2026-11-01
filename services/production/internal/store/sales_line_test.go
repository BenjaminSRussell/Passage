package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

func salesLineOrder(key, legalEntity, product, line, configuration string) NewOrder {
	date := "2026-11-30"
	return NewOrder{LegalEntityID: legalEntity, ProductID: product, SiteID: ids.New(), WarehouseID: ids.New(), Currency: "USD", Quantity: d("5"),
		DeliveryDate: &date, OriginType: "SalesOrderLine", OriginID: &line, ConfigurationID: &configuration, IdempotencyKey: key, RequestHash: "hash-" + key}
}

func TestSalesLineOrder_IsIdempotentPerLineAndRefusesOtherLegalEntities(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	tenantID := testkit.TenantID(81)
	leA, leB := ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(tenantID, leA), scopedCtx(tenantID, leB)
	product, line, configuration := ids.New(), ids.New(), ids.New()

	order, created, err := s.CreateOrder(ctxA, salesLineOrder("line-key-1", leA, product, line, configuration))
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if order.OriginType != "SalesOrderLine" || order.OriginID == nil || *order.OriginID != line || order.ConfigurationID == nil || *order.ConfigurationID != configuration {
		t.Fatalf("order must carry its line and configuration: %+v", order)
	}
	if len(order.Lines) != 0 || len(order.Operations) != 0 {
		t.Fatalf("no route is exploded for a configured line yet: %d lines, %d operations", len(order.Lines), len(order.Operations))
	}
	var raw json.RawMessage
	if err := admin.QueryRow(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2`, order.ID, prodevents.OrderCreated).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data prodevents.OrderCreatedData `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil || evt.Data.ConfigurationID == nil || *evt.Data.ConfigurationID != configuration {
		t.Fatalf("order.created must carry the configuration: %s %v", raw, err)
	}

	again, created, err := s.CreateOrder(ctxA, salesLineOrder("line-key-2", leA, product, line, configuration))
	if err != nil || created || again.ID != order.ID {
		t.Fatalf("a second request for the line (new key) must return the first order: created=%v id=%s err=%v", created, again.ID, err)
	}
	_, _, err = s.CreateOrder(ctxA, salesLineOrder("line-key-3", leA, product, line, ids.New()))
	expectProblem(t, err, 409, CodeOriginConflict)
	_, _, err = s.CreateOrder(ctxA, salesLineOrder("line-key-4", leA, ids.New(), line, configuration))
	expectProblem(t, err, 409, CodeOriginConflict)

	_, _, err = s.CreateOrder(ctxB, salesLineOrder("line-key-5", leB, product, line, configuration))
	expectProblem(t, err, 409, CodeOriginConflict)
	if _, err := s.GetOrder(ctxB, order.ID); err == nil {
		t.Fatal("another legal entity must not read the order")
	}

	var count int
	if err := admin.QueryRow(context.Background(), `select count(*) from production_order where origin_id = $1`, line).Scan(&count); err != nil || count != 1 {
		t.Fatalf("want one order for the line, got %d (%v)", count, err)
	}
	if _, err := admin.Exec(context.Background(), `insert into production_order (tenant_id, id, legal_entity_id, order_number, product_id, site_id, warehouse_id, currency, quantity, origin_type, idempotency_key, request_hash, configuration_id)
		values ($1, $2, $3, 'PO-X', $4, $5, $6, 'USD', 1, 'Manual', 'k', 'h', $7)`, tenantID, ids.New(), leA, product, ids.New(), ids.New(), configuration); err == nil {
		t.Fatal("a configuration is allowed only on a SalesOrderLine order")
	}
}
