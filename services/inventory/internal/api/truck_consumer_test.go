package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

const (
	truckSite      = "99999999-9999-4999-8999-999999999991"
	truckWarehouse = "99999999-9999-4999-8999-999999999992"
	truckWO        = "99999999-9999-4999-8999-999999999993"
	truckLineA     = "99999999-9999-4999-8999-999999999994"
	truckLineB     = "99999999-9999-4999-8999-999999999995"
)

type fakeReplenisher struct{ calls [][3]string }

func (f *fakeReplenisher) ReplenishTrucks(_ context.Context, le, site, wh string, _ time.Time) ([]domain.TransferOrder, error) {
	f.calls = append(f.calls, [3]string{le, site, wh})
	return nil, nil
}

func completed(truck string, lines ...string) string {
	return `{"workOrderId":"` + truckWO + `","number":"SWO-000042","legalEntityId":"` + postEntity + `","completedAt":"2026-10-26T16:30:00Z","currency":"USD",` +
		`"truck":` + truck + `,"lines":[` + strings.Join(lines, ",") + `]}`
}

func partLine(id, qty, unit string) string {
	return `{"lineId":"` + id + `","kind":"PART","qty":"5","consumedQty":"` + qty + `","productId":"` + postProd + `","unit":"` + unit + `"}`
}

const truckJSON = `{"siteId":"` + truckSite + `","warehouseId":"` + truckWarehouse + `","locationId":null}`

// BR-45.7: a completed work order issues each consumed PART line from the truck's site and
// warehouse at DEDUCTED, once per line, converted into the inventory unit, then asks for the
// truck to be replenished.
func TestTruckConsumer_IssuesConsumedPartsFromTheTruck(t *testing.T) {
	f, repl := &fakePostings{cost: d("3.5")}, &fakeReplenisher{}
	h := TruckConsumerHandlers(f, &fakeUnits{}, false, repl)[WorkOrderCompletedType]
	data := completed(truckJSON, partLine(truckLineA, "2", "BOX"), partLine(truckLineB, "0", "EA"),
		`{"lineId":"`+truckLineB+`","kind":"LABOR","qty":"1","consumedQty":"0","productId":"","unit":""}`)
	if err := h.Handle(postingsCtx(t), nil, postingEvent(t, WorkOrderCompletedType, ServiceSource, data)); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 {
		t.Fatalf("bookings %d, want 1 (the unconsumed and labor lines book nothing)", len(f.got))
	}
	in := f.got[0]
	if in.SourceType != "SERVICE_LINE" || in.Status != domain.StatusDeducted || !in.Qty.Equal(d("-24")) || !in.EnteredQty.Equal(d("-2")) || in.EnteredUnit != "BOX" ||
		in.Dims[domain.DimSite] != truckSite || in.Dims[domain.DimWarehouse] != truckWarehouse || in.Reference != "SWO-000042" || in.Currency != "EUR" || !in.CostPrice.Equal(d("3.5")) {
		t.Fatalf("booking %+v", in)
	}
	if in.Negative.PhysicalAllowed {
		t.Fatal("a truck must not go negative unless service.allow-negative-truck is on")
	}
	if len(repl.calls) != 1 || repl.calls[0] != [3]string{postEntity, truckSite, truckWarehouse} {
		t.Fatalf("replenishment %v", repl.calls)
	}

	// The same event again books nothing: the line is found booked.
	f2 := &fakePostings{}
	f2.book("SERVICE_LINE", truckSourceID(postTenant, truckWO, truckLineA), storeRow())
	if err := h.Handle(postingsCtx(t), nil, postingEvent(t, WorkOrderCompletedType, ServiceSource, data)); err != nil {
		t.Fatal(err)
	}
	g := TruckConsumerHandlers(f2, &fakeUnits{}, false, nil)[WorkOrderCompletedType]
	if err := g.Handle(postingsCtx(t), nil, postingEvent(t, WorkOrderCompletedType, ServiceSource, data)); err != nil || len(f2.got) != 0 {
		t.Fatalf("replay booked %d: %v", len(f2.got), err)
	}
}

func TestTruckConsumer_PolicyAndRefusals(t *testing.T) {
	// The policy lifts the negative-stock refusal.
	f := &fakePostings{}
	h := TruckConsumerHandlers(f, &fakeUnits{}, true, nil)[WorkOrderCompletedType]
	if err := h.Handle(postingsCtx(t), nil, postingEvent(t, WorkOrderCompletedType, ServiceSource, completed(truckJSON, partLine(truckLineA, "1", "EA")))); err != nil {
		t.Fatal(err)
	}
	if !f.got[0].Negative.PhysicalAllowed || !f.got[0].Negative.FinancialAllowed {
		t.Fatalf("negative policy %+v", f.got[0].Negative)
	}

	h = TruckConsumerHandlers(&fakePostings{}, &fakeUnits{}, false, nil)[WorkOrderCompletedType]
	for name, c := range map[string]struct{ source, data string }{
		"no truck":            {ServiceSource, completed("null", partLine(truckLineA, "1", "EA"))},
		"another source":      {"sales", completed(truckJSON, partLine(truckLineA, "1", "EA"))},
		"a line with no unit": {ServiceSource, completed(truckJSON, partLine(truckLineA, "1", ""))},
	} {
		err := h.Handle(postingsCtx(t), nil, postingEvent(t, WorkOrderCompletedType, c.source, c.data))
		if err == nil || !eventing.IsPermanent(err) {
			t.Errorf("%s: %v, want a permanent failure", name, err)
		}
	}
	// A work order that consumed nothing from stock needs no truck.
	if err := h.Handle(postingsCtx(t), nil, postingEvent(t, WorkOrderCompletedType, ServiceSource, completed("null", partLine(truckLineA, "0", "EA")))); err != nil {
		t.Fatalf("nothing consumed: %v", err)
	}
}

func storeRow() store.TransactionRow { return store.TransactionRow{} }
