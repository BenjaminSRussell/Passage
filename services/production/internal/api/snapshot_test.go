package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

func (f *fakeStore) RunCostSnapshot(_ context.Context, in store.CostSnapshotRun) (store.CostSnapshotResult, error) {
	f.snapRuns = append(f.snapRuns, in)
	return store.CostSnapshotResult{LegalEntityID: in.LegalEntityID, SnapshotDate: in.Date, Orders: 3}, f.getErr
}

func (f *fakeStore) CostSnapshots(_ context.Context, cf store.CostSnapshotFilter) (store.CostSnapshotPage, error) {
	f.snapFilters = append(f.snapFilters, cf)
	return f.snapPage, f.getErr
}

func snapRun(body string, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/production/cost-snapshots:run", body: body, duties: duties}
}

func TestCostSnapshotRun_DefaultsToTheCallersEntityAndNeedsTheDuty(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	if rec := do(h, snapRun(`{}`, DutyJournalPost)); rec.Code != http.StatusForbidden {
		t.Fatalf("without the cost duty: %d", rec.Code)
	}
	rec := do(h, snapRun(`{"date":"2026-10-30"}`, DutyCostRead))
	if rec.Code != http.StatusOK || len(f.snapRuns) != 1 {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.snapRuns[0]; got.LegalEntityID != entityID || got.Date != "2026-10-30" || got.Actor != "planner-1" {
		t.Fatalf("run: %+v", got)
	}
	var res store.CostSnapshotResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Orders != 3 {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestCostSnapshotRun_RefusesBadInput(t *testing.T) {
	future := time.Now().UTC().AddDate(0, 0, 2).Format(time.DateOnly)
	for name, body := range map[string]string{
		"legalEntityId": `{"legalEntityId":"x"}`, "date": `{"date":"30/10/2026"}`, "future": `{"date":"` + future + `"}`,
		"other entity": `{"legalEntityId":"` + ids.New() + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			expectProblem(t, do(h, snapRun(body, DutyCostRead)), http.StatusBadRequest, validationCode)
			if len(f.snapRuns) != 0 {
				t.Error("nothing is written for a bad request")
			}
		})
	}
}

func TestCostSnapshots_FiltersAndRendersToTheMinorUnit(t *testing.T) {
	order := ids.New()
	f := &fakeStore{snapPage: store.CostSnapshotPage{NextCursor: "2026-10-30|" + order, Items: []store.CostSnapshot{{
		OrderID: order, OrderNumber: "PO-000001", SnapshotDate: "2026-10-30", Status: "Started", Currency: "USD",
		QtyOrdered: decimal.NewFromInt(100), QtyReported: decimal.NewFromInt(40), PlannedCost: decimal.RequireFromString("1000.5"),
		ActualCost: decimal.RequireFromString("1200.25"), WIPBalance: decimal.RequireFromString("300")}}}}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, duties: []string{DutyCostRead}, path: "/v1/production/cost-snapshots?orderId=" + order +
		"&from=2026-10-01&to=2026-10-31&limit=20&cursor=2026-10-31|" + order})
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.snapFilters[0]; got.OrderID != order || got.From != "2026-10-01" || got.To != "2026-10-31" || got.Limit != 20 {
		t.Fatalf("filter: %+v", got)
	}
	var page struct {
		Items      []map[string]string `json:"items"`
		NextCursor string              `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("body %s", rec.Body.String())
	}
	if it := page.Items[0]; it["plannedCost"] != "1000.50" || it["costVariance"] != "199.75" || it["wipBalance"] != "300.00" {
		t.Fatalf("amounts: %v", it)
	}
}

func TestCostSnapshots_RefusesBadFilters(t *testing.T) {
	for name, q := range map[string]string{"orderId": "orderId=x", "from": "from=yesterday", "to": "to=2026-13-01", "limit": "limit=0", "cursor": "cursor=nope"} {
		t.Run(name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			p := expectProblem(t, do(h, call{method: http.MethodGet, duties: []string{DutyCostRead}, path: "/v1/production/cost-snapshots?" + q}), http.StatusBadRequest, validationCode)
			if _, ok := p.Errors[name]; !ok || len(f.snapFilters) != 0 {
				t.Fatalf("errors %v", p.Errors)
			}
		})
	}
}
