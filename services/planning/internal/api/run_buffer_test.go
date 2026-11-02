package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

func (f *fakeStore) RunBuffers(_ context.Context, _, _ string, _ []string) ([]domain.RunBuffer, error) {
	return f.runBuffers, nil
}

// AC-22.12 in the run: a priority-based item with a stored buffer is
// replenished by the buffer's planned quantity (115), not by its own demand,
// and a red-zone buffer raises the PLAN-0010 message.
func TestRun_BufferReplenishesDecouplingPoint(t *testing.T) {
	st := &fakeStore{runBuffers: []domain.RunBuffer{redBuffer()}}
	_, req := runVersion(t, st, strings.Replace(runBody, `"production"`, `"purchase","vendorId":"`+defaultVendor+`"`, 1), "k-buffer")
	o := onlyOrder(t, req)
	if !o.Qty.Equal(decimal.RequireFromString("115")) {
		t.Fatalf("order %+v; want the buffer quantity 115, not the 120 demand", o)
	}
	if o.PlanningPriority == nil || !o.PlanningPriority.Equal(decimal.RequireFromString("0.25")) || o.BufferZone != domain.ZoneRed {
		t.Fatalf("order %+v; want the buffer's priority and zone", o)
	}
	if len(req.Result.Messages) != 1 || req.Result.Messages[0].Action != domain.ActionRedZone || req.Result.Messages[0].SupplyRef != o.Number {
		t.Fatalf("messages %+v", req.Result.Messages)
	}
}

func redBuffer() domain.RunBuffer {
	return domain.RunBuffer{ItemID: fgID, DLTDays: decimal.RequireFromString("10"), PlannedQty: decimal.RequireFromString("115"),
		Priority: decimal.RequireFromString("0.25"), Zone: domain.ZoneRed}
}

// A quota split of a buffer order: every part carries the priority, and the
// red-zone messages name the parts, never the order number the split replaced.
func TestRun_BufferOrderSplitByQuota(t *testing.T) {
	st := &fakeStore{runBuffers: []domain.RunBuffer{redBuffer()}, runQuotas: []domain.RunQuota{purchaseQuota(true)}}
	_, req := runVersion(t, st, strings.Replace(runBody, `"production"`, `"purchase"`, 1), "k-buffer-split")
	numbers := map[string]bool{}
	total := decimal.Zero
	for _, o := range req.Result.Orders {
		if o.PlanningPriority == nil || o.BufferZone != domain.ZoneRed || o.QuotaSourceID == "" {
			t.Fatalf("order %+v", o)
		}
		numbers[o.Number] = true
		total = total.Add(o.Qty)
	}
	if len(numbers) < 2 || !total.Equal(decimal.RequireFromString("115")) {
		t.Fatalf("orders %+v; want the 115 split over the sources", req.Result.Orders)
	}
	if len(req.Result.Messages) != len(numbers) {
		t.Fatalf("messages %+v; want one per part", req.Result.Messages)
	}
	for _, m := range req.Result.Messages {
		if m.Action != domain.ActionRedZone || !numbers[m.SupplyRef] {
			t.Fatalf("message %+v names no stored order", m)
		}
	}
}

// A simulation applies the buffer after the scenario: the lead-time override
// moves the buffer order, and demand the scenario adds to the buffered item is
// absorbed by the buffer instead of being netted on top.
func TestScenarioRun_BufferAfterScenario(t *testing.T) {
	st := &fakeStore{runBuffers: []domain.RunBuffer{redBuffer()}}
	body := `{"code":"BUF","planCode":"MAIN","name":"Buffer","demandFactor":"1","leadTimeOverrides":[{"itemId":"` + fgID + `","leadTimeDays":20}],
		"addedDemand":[{"itemId":"` + fgID + `","date":"2026-11-10","qty":"50"}]}`
	if rec := do(t, st, http.MethodPost, scenarioPath, body, dutySim, true, ""); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, st, http.MethodPost, scenarioPath+"/BUF:run", runBody, dutySim, true, "sim-buf"); rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	orders := st.lastSim.Result.Orders
	if len(orders) != 1 || !orders[0].Qty.Equal(decimal.RequireFromString("115")) || orders[0].DeliveryDate != 20 || orders[0].PlanningPriority == nil {
		t.Fatalf("simulated orders %+v; want the 115 buffer order delivered after the 20-day override", orders)
	}
	if len(st.lastSim.Result.Messages) != 1 || st.lastSim.Result.Messages[0].Action != domain.ActionRedZone {
		t.Fatalf("simulated messages %+v", st.lastSim.Result.Messages)
	}
}

func TestRun_WithoutBufferPlansByNetting(t *testing.T) {
	st := &fakeStore{}
	_, req := runVersion(t, st, runBody, "k-nobuffer")
	if o := onlyOrder(t, req); !o.Qty.Equal(decimal.RequireFromString("110")) {
		t.Fatalf("order %+v; want 110", o)
	}
}

// TestList_SortParameter: sort=priority is the only sort (FR-22.16); anything
// else is a 400 naming the field, on planned orders and on messages.
func TestList_SortParameter(t *testing.T) {
	for _, path := range []string{"/v1/planning/planned-orders?planCode=MAIN", "/v1/planning/messages?planCode=MAIN"} {
		if rec := do(t, &fakeStore{}, http.MethodGet, path+"&sort=priority", "", "planning.read", true, ""); rec.Code != http.StatusOK {
			t.Errorf("%s sort=priority = %d %s", path, rec.Code, rec.Body)
		}
		rec := do(t, &fakeStore{}, http.MethodGet, path+"&sort=date", "", "planning.read", true, "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "sort") {
			t.Errorf("%s sort=date = %d %s", path, rec.Code, rec.Body)
		}
	}
}
