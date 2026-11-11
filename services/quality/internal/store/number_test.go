package store

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/migrations"
)

// Every quality order gets the next QO number of its tenant on insert (migration
// 0008), and the worklist finds an order by the start of its number.
func TestPostgres_OrdersAreNumberedPerTenant(t *testing.T) {
	s, _, ctx := setup(t)
	group, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "GRP-NUM", Tests: []domain.TestLine{
		{TestID: ids.New(), Name: "visual", Kind: domain.KindAttribute, Required: true, PassingOutcomes: []string{ids.New()}},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	create := func(tenantNo int) domain.Order {
		t.Helper()
		c := testkit.Ctx(testkit.TenantID(tenantNo), "quality.order.maintain")
		g := group
		if tenantNo != 1 {
			g, _, err = s.CreateTestGroup(c, domain.TestGroup{Code: "GRP-NUM", Tests: group.Tests}, "")
			if err != nil {
				t.Fatal(err)
			}
		}
		o, _, err := s.CreateOrder(c, domain.Order{ID: ids.New(), Trigger: domain.TriggerManual, SourceRef: domain.SourceRef{Type: "manual", ID: ids.New()},
			ItemID: ids.New(), SiteID: ids.New(), WarehouseID: ids.New(), Dims: map[string]string{}, LotQty: decimal.NewFromInt(10), SampleQty: decimal.NewFromInt(1),
			UOM: "EA", TestGroupID: g.ID, TestGroupVersion: 1, SamplingMethod: domain.MethodFixed, Severity: domain.SeverityNormal, AcceptNo: 0, RejectNo: 1}, "")
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	first, second, other := create(1), create(1), create(2)
	if first.Number != "QO-000001" || second.Number != "QO-000002" || other.Number != "QO-000001" {
		t.Fatalf("numbers = %q %q, other tenant %q; want QO-000001, QO-000002 and a separate count per tenant", first.Number, second.Number, other.Number)
	}
	got, err := s.GetOrder(ctx, second.ID)
	if err != nil || got.Number != "QO-000002" {
		t.Fatalf("read back = %q, %v", got.Number, err)
	}
	found, err := s.ListOrders(ctx, OrderFilter{Number: "qo-000002", Limit: 10})
	if err != nil || len(found) != 1 || found[0].ID != second.ID {
		t.Fatalf("by number = %+v, %v", found, err)
	}
	if all, err := s.ListOrders(ctx, OrderFilter{Number: "QO-0000", Limit: 10}); err != nil || len(all) != 2 {
		t.Fatalf("by prefix = %d, %v; want both of tenant 1", len(all), err)
	}
	if none, err := s.ListOrders(ctx, OrderFilter{Number: "%", Limit: 10}); err != nil || len(none) != 0 {
		t.Fatalf("a LIKE wildcard matched %d orders, %v", len(none), err)
	}
}

func numberTestOrder(t *testing.T, s *Postgres, ctx context.Context) domain.Order {
	t.Helper()
	group, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "GRP-" + strings.ToUpper(ids.New()[:6]), Tests: []domain.TestLine{
		{TestID: ids.New(), Name: "visual", Kind: domain.KindAttribute, Required: true, PassingOutcomes: []string{ids.New()}},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateOrder(ctx, domain.Order{ID: ids.New(), Trigger: domain.TriggerManual, SourceRef: domain.SourceRef{Type: "manual", ID: ids.New()},
		ItemID: ids.New(), SiteID: ids.New(), WarehouseID: ids.New(), Dims: map[string]string{}, LotQty: decimal.NewFromInt(10), SampleQty: decimal.NewFromInt(1),
		UOM: "EA", TestGroupID: group.ID, TestGroupVersion: 1, SamplingMethod: domain.MethodFixed, Severity: domain.SeverityNormal, AcceptNo: 0, RejectNo: 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// Past 999999 the number grows a digit instead of being cut to six.
func TestPostgres_OrderNumbersGrowPastSixDigits(t *testing.T) {
	s, pool, ctx := setup(t)
	if _, err := pool.Exec(context.Background(), `insert into quality_order_counter (tenant_id, last_number) values ($1, 999999)`, testkit.TenantID(1)); err != nil {
		t.Fatal(err)
	}
	first, second := numberTestOrder(t, s, ctx), numberTestOrder(t, s, ctx)
	if first.Number != "QO-1000000" || second.Number != "QO-1000001" {
		t.Fatalf("numbers %q and %q; want QO-1000000 and QO-1000001", first.Number, second.Number)
	}
}

// The backfill numbers orders left without a number after the counter, so a
// re-run after the trigger has numbered orders reuses no number.
func TestPostgres_OrderNumberBackfillContinuesAfterTheCounter(t *testing.T) {
	s, pool, ctx := setup(t)
	made := []domain.Order{numberTestOrder(t, s, ctx), numberTestOrder(t, s, ctx), numberTestOrder(t, s, ctx)}
	if _, err := pool.Exec(context.Background(), `update quality_order set number = null where tenant_id = $1 and id = $2`, testkit.TenantID(1), made[1].ID); err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(migrations.FS, "0008_order_number.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	start := strings.Index(sql, "DO $$\nBEGIN\n  IF NOT EXISTS")
	end := strings.Index(sql[start:], "$$;")
	if start < 0 || end < 0 {
		t.Fatal("backfill block not found in 0008")
	}
	for run := 0; run < 2; run++ {
		if _, err := pool.Exec(context.Background(), sql[start:start+end+3]); err != nil {
			t.Fatalf("backfill run %d: %v", run+1, err)
		}
	}
	got, err := s.GetOrder(ctx, made[1].ID)
	if err != nil || got.Number != "QO-000004" {
		t.Fatalf("backfilled number %q, %v; want QO-000004 after the counter's 3", got.Number, err)
	}
	if next := numberTestOrder(t, s, ctx); next.Number != "QO-000005" {
		t.Fatalf("next number %q; want QO-000005", next.Number)
	}
}
