package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

func (f *fakeStore) SubcontractOperations(_ context.Context, sf store.SubcontractFilter) (store.SubcontractReport, error) {
	f.subFilters = append(f.subFilters, sf)
	return f.subcontract, f.getErr
}

func TestSubcontractReport_FormatsAndFilters(t *testing.T) {
	order, vendor, site := ids.New(), ids.New(), ids.New()
	due := "2026-11-30"
	requested := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	f := &fakeStore{subcontract: store.SubcontractReport{
		Items: []store.SubcontractRow{
			{OrderID: order, OrderNumber: "PO-000007", OrderStatus: domain.Released, Currency: "USD", DueDate: &due, OperNumber: 20,
				ResourceID: vendor, Qty: decimal.RequireFromString("102.040817"), QtyGood: decimal.Zero, UnitPrice: decimal.RequireFromString("1.255"),
				Amount: decimal.RequireFromString("128.0612"), OperStatus: domain.OperOpen, PORequestedAt: &requested},
			{OrderID: ids.New(), OrderNumber: "PO-000008", OrderStatus: domain.Estimated, Currency: "JPY", OperNumber: 10, ResourceID: vendor,
				Qty: decimal.RequireFromString("5"), QtyGood: decimal.Zero, UnitPrice: decimal.RequireFromString("300.5"),
				Amount: decimal.RequireFromString("1502.5"), OperStatus: domain.OperOpen},
		},
		Totals: []store.SubcontractTotal{{Currency: "JPY", Operations: 1, Amount: decimal.RequireFromString("1502.5")},
			{Currency: "USD", Operations: 1, Amount: decimal.RequireFromString("128.0612")}},
		NextCursor: order + ":20",
	}}
	_, h := newServer(t, f)
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/subcontract-operations", duties: []string{DutyCostRead}}),
		http.StatusForbidden, "AUTH-0003")

	rec := do(h, call{method: http.MethodGet, duties: []string{DutyRead},
		path: "/v1/production/subcontract-operations?status=open&orderStatus=Released&resourceId=" + vendor + "&siteId=" + site +
			"&dueFrom=2026-11-01&dueTo=2026-11-30&limit=10&cursor=" + order + ":10"})
	if rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	var v subcontractReportView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	// Amounts go to the currency's minor unit (JPY has none); price and quantity stay exact.
	first, second := v.Items[0], v.Items[1]
	if first.Amount != "128.06" || first.UnitPrice != "1.255" || first.Qty != "102.040817" || first.PORequestedAt == nil || *first.DueDate != due {
		t.Fatalf("first row = %+v", first)
	}
	if second.Amount != "1503" || second.PORequestedAt != nil || second.DueDate != nil {
		t.Fatalf("second row = %+v", second)
	}
	if len(v.Totals) != 2 || v.Totals[0].Amount != "1503" || v.Totals[1].Amount != "128.06" || v.NextCursor != order+":20" {
		t.Fatalf("totals = %+v next = %q", v.Totals, v.NextCursor)
	}
	got := f.subFilters[0]
	if got.OperStatus != domain.OperOpen || got.OrderStatus != domain.Released || got.ResourceID != vendor || got.SiteID != site ||
		got.DueFrom != "2026-11-01" || got.DueTo != "2026-11-30" || got.Limit != 10 || got.CursorOrder != order || got.CursorOper != 10 {
		t.Fatalf("filter = %+v", got)
	}

	// No filter: every subcontract operation, the default page size.
	do(h, call{method: http.MethodGet, path: "/v1/production/subcontract-operations", duties: []string{DutyRead}})
	if got := f.subFilters[1]; got != (store.SubcontractFilter{Limit: defaultPageSize}) {
		t.Fatalf("default filter = %+v", got)
	}
}

func TestSubcontractReport_RejectsBadFilters(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	p := expectProblem(t, do(h, call{method: http.MethodGet, duties: []string{DutyRead},
		path: "/v1/production/subcontract-operations?status=closed&orderStatus=Open&resourceId=v1&siteId=x&legalEntityId=y" +
			"&dueFrom=2026-12-01&dueTo=2026-11-01&limit=501&cursor=" + ids.New()}), http.StatusBadRequest, validationCode)
	for _, name := range []string{"status", "orderStatus", "resourceId", "siteId", "legalEntityId", "dueTo", "limit", "cursor"} {
		if _, ok := p.Errors[name]; !ok {
			t.Errorf("no error for %s: %v", name, p.Errors)
		}
	}
	p = expectProblem(t, do(h, call{method: http.MethodGet, duties: []string{DutyRead},
		path: "/v1/production/subcontract-operations?dueFrom=30.11.2026&cursor=" + ids.New() + ":0"}), http.StatusBadRequest, validationCode)
	for _, name := range []string{"dueFrom", "cursor"} {
		if _, ok := p.Errors[name]; !ok {
			t.Errorf("no error for %s: %v", name, p.Errors)
		}
	}
	if len(f.subFilters) != 0 {
		t.Fatalf("a refused filter reached the store: %+v", f.subFilters)
	}
}

func TestSubcontractReport_StoreFailure(t *testing.T) {
	f := &fakeStore{getErr: httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "database unavailable")}
	_, h := newServer(t, f)
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/subcontract-operations", duties: []string{DutyRead}}),
		http.StatusServiceUnavailable, "COM-0503")
}
