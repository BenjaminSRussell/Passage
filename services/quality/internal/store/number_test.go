package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
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
