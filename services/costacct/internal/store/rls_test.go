package store

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costacct/migrations"
)

func TestAppRole_CrossTenantRLS(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	testkit.CrossTenant(t, app, []testkit.RLSCase{
		testkit.OutboxCase(),
		testkit.IdempotencyCase(),
		{
			Table:       "cost_object",
			Insert:      `insert into cost_object (tenant_id, id, legal_entity_id, kind, code, name) values ($1, $2, $3, 'CostCenter', 'CC-100', 'Plant')`,
			Args:        func(tenantID, entityID string) []any { return []any{tenantID, ids.New(), entityID} },
			LegalEntity: true,
		},
		{
			Table:       "cost_element",
			Insert:      `insert into cost_element (tenant_id, id, legal_entity_id, kind, code, name) values ($1, $2, $3, 'Secondary', 'ALLOC-1', 'Allocation')`,
			Args:        func(tenantID, entityID string) []any { return []any{tenantID, ids.New(), entityID} },
			LegalEntity: true,
			NoUpdate:    true,
		},
	})
}

func TestAppRole_RowLockingPathsRun(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := testCtx(testkit.TenantID(6))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	f := ledgerFixture{ctx: ctx, st: New(app), le: ids.New()}
	_, raw, err := f.st.CreateCostElement(ctx, ids.New(), "h", CostElement{LegalEntityID: f.le, Kind: "Primary", Code: "FREIGHT", Name: "Freight",
		MainAccountFrom: "610000", MainAccountTo: "619999"})
	if err != nil {
		t.Fatal(err)
	}
	f.primary = idOf(t, raw)
	_, raw, err = f.st.CreateCostElement(ctx, ids.New(), "h", CostElement{LegalEntityID: f.le, Kind: "Secondary", Code: "SETTLE", Name: "Settlement"})
	if err != nil {
		t.Fatal(err)
	}
	f.secondary = idOf(t, raw)
	order := f.object(t, "InternalOrder", "IO-1")
	receiver := f.object(t, "CostCenter", "CC-1")
	f.book(t, order, "500.00")
	if _, _, err := f.st.SettleInternalOrder(ctx, ids.New(), "h", SettlementRequest{OrderID: order, Period: "2026-09", Currency: "USD",
		ReceiverID: receiver, SecondaryElementID: f.secondary, Amount: dec("200.00")}); err != nil {
		t.Fatalf("settle as app role: %v", err)
	}
	source := f.object(t, "CostCenter", "ENTITY")
	segment := f.object(t, "Product", "NORTH")
	f.book(t, source, "100.00")
	if _, _, err := f.st.RunTopDown(ctx, ids.New(), "h", TopDownRequest{LegalEntityID: f.le, Period: "2026-09", Currency: "USD",
		SourceCostObjectID: source, CostElementID: f.primary, Amount: dec("100.00"), Segments: []TopDownSegment{{CostObjectID: segment, Basis: dec("1")}}}); err != nil {
		t.Fatalf("top-down as app role: %v", err)
	}
}
