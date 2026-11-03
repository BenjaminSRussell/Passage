package store

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// TestPostgres_CatchWeightLineNet is Wave 24 AC4: 10 cases at nominal 10 kg and
// 2.00/kg price at 200; recording an actual 102 kg restates LineNet (and the
// order net) to 204; 95 kg is refused with SLS-0061.
func TestPostgres_CatchWeightLineNet(t *testing.T) {
	s, _, ctx := migrated(t)
	le, product := ids.New(), ids.New()
	spec := &domain.CatchWeight{Nominal: decimal.RequireFromString("10"), Min: decimal.RequireFromString("9.8"), Max: decimal.RequireFromString("11")}
	net, err := domain.LineNetFor(d(10), d(2), d(0), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := NewOrder{LegalEntityID: le, CustomerID: ids.New(), InvoiceAccountID: ids.New(), OrderType: "ORDER", Currency: "USD",
		RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: ids.New(), OrderDate: time.Date(2027, 2, 20, 0, 0, 0, 0, time.UTC),
		Actor: "sales-rep", Lines: []Line{{
			LineNo: 1, ProductID: product, Quantity: d(10), Unit: "CS", SiteID: ids.New(), WarehouseID: ids.New(),
			UnitPrice: d(2), DiscPct: d(0), LineNet: net, RequestedShipDate: "2027-03-04", CatchWeight: spec,
		}}}
	o := confirmed(t, s, ctx, in)
	if !o.Lines[0].LineNet.Equal(d(200)) || !o.NetTotal.Equal(d(200)) || o.Lines[0].CatchWeight == nil {
		t.Fatalf("nominal order LineNet %s netTotal %s spec %+v, want 200", o.Lines[0].LineNet, o.NetTotal, o.Lines[0].CatchWeight)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	light := decimal.RequireFromString("95")
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-95", "k95", "h95",
		ProgressLineInput{LineNo: 1, Quantity: d(10), ActualWeight: &light}), now); problemCode(t, err) != domain.CodeCatchWeight {
		t.Fatalf("95 kg: %v, want SLS-0061", err)
	}

	heavy := decimal.RequireFromString("102")
	rep, created, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-102", "k102", "h102",
		ProgressLineInput{LineNo: 1, Quantity: d(10), ActualWeight: &heavy}), now)
	if err != nil || !created {
		t.Fatalf("102 kg delivery: created=%v err=%v", created, err)
	}
	got, err := s.GetOrder(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lines[0].ActualWeight == nil || !got.Lines[0].ActualWeight.Equal(heavy) || !got.Lines[0].LineNet.Equal(d(204)) || !got.NetTotal.Equal(d(204)) {
		t.Fatalf("after 102 kg: lineNet=%s netTotal=%s actual=%v, want 204 / 204 / 102", got.Lines[0].LineNet, got.NetTotal, got.Lines[0].ActualWeight)
	}
	if rep.OrderStatus != domain.OrderShipped {
		t.Fatalf("order status %s, want SHIPPED", rep.OrderStatus)
	}
}
