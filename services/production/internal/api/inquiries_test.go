package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

func (f *fakeStore) OperationsOverview(_ context.Context, of store.OperationFilter) (store.OperationsOverview, error) {
	f.opFilters = append(f.opFilters, of)
	return f.opOverview, f.getErr
}

func (f *fakeStore) JournalInquiry(_ context.Context, jf store.JournalFilter) (store.JournalInquiry, error) {
	f.journalFilters = append(f.journalFilters, jf)
	return f.journalPage, f.getErr
}

func TestOperationsOverview_FormatsAndFilters(t *testing.T) {
	order, res, site := ids.New(), ids.New(), ids.New()
	due := "2026-11-30"
	f := &fakeStore{}
	f.opOverview = store.OperationsOverview{
		Items: []store.OperationRow{{OrderID: order, OrderNumber: "PO-000001", OrderStatus: domain.Started, SiteID: site, ProductID: ids.New(), DueDate: &due,
			OperNumber: 10, ResourceID: res, ProcessQty: decimal.RequireFromString("100.000000"), PlannedHours: decimal.RequireFromString("50"),
			HoursReported: decimal.RequireFromString("20.5"), QtyGood: decimal.RequireFromString("60"), QtyError: decimal.RequireFromString("2"), OperStatus: domain.OperStarted}},
		Totals:     store.OperationTotals{Operations: 3, Open: 2, Started: 1, PlannedHours: decimal.RequireFromString("100"), HoursReported: decimal.RequireFromString("20.5")},
		NextCursor: order + ":10",
	}
	_, h := newServer(t, f)
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/operations", duties: []string{DutyCostRead}}), http.StatusForbidden, "AUTH-0003")

	rec := do(h, call{method: http.MethodGet, duties: []string{DutyRead}, path: "/v1/production/operations?status=started&orderStatus=Started&resourceId=" + res +
		"&siteId=" + site + "&orderId=" + order + "&dueFrom=2026-11-01&dueTo=2026-11-30&limit=10&cursor=" + order + ":5"})
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body.String())
	}
	var v operationsView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	row := v.Items[0]
	if row.PlannedHours != "50.00" || row.HoursReported != "20.50" || row.ProcessQty != "100" || row.QtyGood != "60" || *row.DueDate != due ||
		v.Totals.Operations != 3 || v.Totals.PlannedHours != "100.00" || v.Totals.Open != 2 || v.NextCursor != order+":10" {
		t.Fatalf("view = %+v", v)
	}
	got := f.opFilters[0]
	if got.OperStatus != domain.OperStarted || got.OrderStatus != domain.Started || got.ResourceID != res || got.SiteID != site || got.OrderID != order ||
		got.DueFrom != "2026-11-01" || got.DueTo != "2026-11-30" || got.Limit != 10 || got.CursorOrder != order || got.CursorOper != 5 {
		t.Fatalf("filter = %+v", got)
	}
	do(h, call{method: http.MethodGet, path: "/v1/production/operations", duties: []string{DutyRead}})
	if got := f.opFilters[1]; got != (store.OperationFilter{Limit: defaultPageSize}) {
		t.Fatalf("default filter = %+v", got)
	}
}

func TestOperationsOverview_RejectsBadFiltersAndStoreFailure(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	p := expectProblem(t, do(h, call{method: http.MethodGet, duties: []string{DutyRead},
		path: "/v1/production/operations?status=closed&orderStatus=Open&resourceId=v1&siteId=x&legalEntityId=y&orderId=z&dueFrom=2026-12-01&dueTo=2026-11-01&limit=501&cursor=bad"}),
		http.StatusBadRequest, validationCode)
	for _, name := range []string{"status", "orderStatus", "resourceId", "siteId", "legalEntityId", "orderId", "dueTo", "limit", "cursor"} {
		if _, ok := p.Errors[name]; !ok {
			t.Errorf("no error for %s: %v", name, p.Errors)
		}
	}
	if len(f.opFilters) != 0 {
		t.Fatalf("a refused filter reached the store: %+v", f.opFilters)
	}
	_, h = newServer(t, &fakeStore{getErr: httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "database unavailable")})
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/operations", duties: []string{DutyRead}}), http.StatusServiceUnavailable, "COM-0503")
}

func TestJournalInquiry_FormatsAndFilters(t *testing.T) {
	order, id := ids.New(), ids.New()
	posted := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	f := &fakeStore{}
	f.journalPage = store.JournalInquiry{
		Items: []store.JournalRow{{ID: id, OrderID: order, OrderNumber: "PO-000007", Type: domain.JournalRouteCard, JournalNumber: "PJ-000002", PostingDate: "2026-10-20",
			Posted: true, PostedAt: &posted, QtyGood: decimal.RequireFromString("100"), Currency: "JPY", Lines: 1,
			Consumed: decimal.RequireFromString("1840.5"), Received: decimal.Zero}},
		Total: 9, NextCursor: id,
	}
	_, h := newServer(t, f)
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/journals", duties: []string{DutyJournalPost}}), http.StatusForbidden, "AUTH-0003")
	rec := do(h, call{method: http.MethodGet, duties: []string{DutyRead}, path: "/v1/production/journals?type=route-card&posted=true&orderNumber=%20PO-000007%20&orderId=" + order +
		"&from=2026-10-01&to=2026-10-31&limit=5&cursor=" + id})
	if rec.Code != http.StatusOK {
		t.Fatalf("inquiry: %d %s", rec.Code, rec.Body.String())
	}
	var v journalInquiryView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	// Values go to the currency's minor unit (JPY has none).
	if r := v.Items[0]; r.Consumed != "1841" || r.Received != "0" || r.QtyGood != "100" || r.Lines != 1 || !r.Posted || r.JournalEntryID != nil || v.Total != 9 || v.NextCursor != id {
		t.Fatalf("view = %+v", v)
	}
	want := store.JournalFilter{OrderID: order, OrderNumber: "PO-000007", Type: domain.JournalRouteCard, Posted: "true", From: "2026-10-01", To: "2026-10-31", Cursor: id, Limit: 5}
	if f.journalFilters[0] != want {
		t.Fatalf("filter = %+v", f.journalFilters[0])
	}
	do(h, call{method: http.MethodGet, path: "/v1/production/journals?type=split-transfer", duties: []string{DutyRead}})
	if f.journalFilters[1].Type != domain.JournalSplitTransfer {
		t.Fatalf("split-transfer filter = %+v", f.journalFilters[1])
	}
}

func TestJournalInquiry_RejectsBadFiltersAndStoreFailure(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	p := expectProblem(t, do(h, call{method: http.MethodGet, duties: []string{DutyRead},
		path: "/v1/production/journals?type=time-card&posted=maybe&orderNumber=" + strings.Repeat("9", 65) + "&orderId=z&legalEntityId=y&from=2026-12-01&to=2026-11-01&limit=0&cursor=bad"}),
		http.StatusBadRequest, validationCode)
	for _, name := range []string{"type", "posted", "orderNumber", "orderId", "legalEntityId", "to", "limit", "cursor"} {
		if _, ok := p.Errors[name]; !ok {
			t.Errorf("no error for %s: %v", name, p.Errors)
		}
	}
	if len(f.journalFilters) != 0 {
		t.Fatalf("a refused filter reached the store: %+v", f.journalFilters)
	}
	_, h = newServer(t, &fakeStore{getErr: httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "database unavailable")})
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/journals", duties: []string{DutyRead}}), http.StatusServiceUnavailable, "COM-0503")
}

func (f *fakeStore) KPIs(_ context.Context, kf store.KPIFilter) (store.KPIReport, error) {
	f.kpiFilters = append(f.kpiFilters, kf)
	return f.kpi, f.getErr
}

func TestOrderKPIs_FormatsAndFilters(t *testing.T) {
	site := ids.New()
	f := &fakeStore{}
	g := domain.KPIGroup{Ended: 3, Dated: 3, OnTime: 2, Cycled: 2, CycleDaysSum: decimal.RequireFromString("7.5"),
		QtyFinished: decimal.RequireFromString("97"), QtyError: decimal.RequireFromString("3"), Open: 4, Overdue: 1}
	f.kpi = store.KPIReport{Groups: []store.KPIRow{{Key: site, KPIGroup: g}, {Key: ids.New()}}, Totals: g}
	_, h := newServer(t, f)
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/kpi", duties: []string{DutyCostRead}}), http.StatusForbidden, "AUTH-0003")

	rec := do(h, call{method: http.MethodGet, duties: []string{DutyRead}, path: "/v1/production/kpi?groupBy=site&siteId=" + site + "&from=2026-10-01&to=2026-10-31&asOf=2026-10-20&limit=1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("kpi: %d %s", rec.Code, rec.Body.String())
	}
	var v kpiView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Groups) != 1 || v.GroupsTotal != 2 || v.Groups[0].Key != site || *v.Groups[0].OnTimePct != "66.67" || *v.Groups[0].AvgCycleDays != "3.75" ||
		*v.Groups[0].ScrapPct != "3.00" || v.Groups[0].QtyFinished != "97" || v.Totals.Open != 4 || v.Totals.Overdue != 1 || v.AsOf != "2026-10-20" || v.GroupBy != "site" {
		t.Fatalf("view = %+v", v)
	}
	if got := f.kpiFilters[0]; got != (store.KPIFilter{SiteID: site, From: "2026-10-01", To: "2026-10-31", AsOf: "2026-10-20", GroupBy: "site"}) {
		t.Fatalf("filter = %+v", got)
	}

	// Nothing ended: the rates are null, not zero, and as-of defaults to today.
	f.kpi = store.KPIReport{}
	rec = do(h, call{method: http.MethodGet, path: "/v1/production/kpi", duties: []string{DutyRead}})
	var empty kpiView
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || empty.Totals.OnTimePct != nil || empty.Totals.ScrapPct != nil || empty.Totals.AvgCycleDays != nil || len(empty.Groups) != 0 ||
		empty.AsOf != time.Now().UTC().Format(time.DateOnly) {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOrderKPIs_RejectsBadFiltersAndStoreFailure(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	for _, path := range []string{"/v1/production/kpi?groupBy=month", "/v1/production/kpi?siteId=x", "/v1/production/kpi?from=2026-13-01",
		"/v1/production/kpi?from=2026-10-31&to=2026-10-01", "/v1/production/kpi?asOf=today", "/v1/production/kpi?limit=0"} {
		rec := do(h, call{method: http.MethodGet, path: path, duties: []string{DutyRead}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	if len(f.kpiFilters) != 0 {
		t.Fatalf("store called for invalid input: %+v", f.kpiFilters)
	}
	f.getErr = context.DeadlineExceeded
	if rec := do(h, call{method: http.MethodGet, path: "/v1/production/kpi", duties: []string{DutyRead}}); rec.Code != http.StatusInternalServerError {
		t.Fatalf("store failure: %d", rec.Code)
	}
}

func (f *fakeStore) MaterialRequirements(_ context.Context, mf store.MaterialFilter) (store.MaterialRequirements, error) {
	f.matFilters = append(f.matFilters, mf)
	return f.matLines, f.getErr
}

func (f *fakeStore) MaterialItems(_ context.Context, mf store.MaterialFilter) (store.MaterialItems, error) {
	f.matFilters = append(f.matFilters, mf)
	return f.matItems, f.getErr
}

func TestMaterialRequirements_FormatsAndFilters(t *testing.T) {
	order, item, site := ids.New(), ids.New(), ids.New()
	due := "2026-11-30"
	f := &fakeStore{}
	f.matLines = store.MaterialRequirements{
		Items: []store.MaterialLine{{OrderID: order, OrderNumber: "PO-000001", OrderStatus: domain.Released, SiteID: site, DueDate: &due, LineNumber: "10", LineSeq: 1,
			ItemID: item, QtyRequired: decimal.RequireFromString("200.000000"), QtyIssued: decimal.RequireFromString("50"), QtyOutstanding: decimal.RequireFromString("150")}},
		Totals: store.MaterialTotals{Lines: 4, Items: 2, Orders: 3}, NextCursor: order + ":1",
	}
	_, h := newServer(t, f)
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/material-requirements", duties: []string{DutyCostRead}}), http.StatusForbidden, "AUTH-0003")
	rec := do(h, call{method: http.MethodGet, duties: []string{DutyRead}, path: "/v1/production/material-requirements?itemId=" + item + "&siteId=" + site + "&orderId=" + order +
		"&orderStatus=Released&dueFrom=2026-11-01&dueTo=2026-11-30&all=true&limit=10&cursor=" + order + ":5"})
	if rec.Code != http.StatusOK {
		t.Fatalf("requirements: %d %s", rec.Code, rec.Body.String())
	}
	var v materialRequirementsView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	row := v.Items[0]
	if row.QtyRequired != "200" || row.QtyIssued != "50" || row.QtyOutstanding != "150" || row.LineNumber != "10" || *row.DueDate != due ||
		v.Totals.Lines != 4 || v.Totals.Items != 2 || v.Totals.Orders != 3 || v.NextCursor != order+":1" {
		t.Fatalf("view = %+v", v)
	}
	got := f.matFilters[0]
	if got.ItemID != item || got.SiteID != site || got.OrderID != order || got.OrderStatus != domain.Released || got.DueFrom != "2026-11-01" || got.DueTo != "2026-11-30" ||
		!got.All || got.Limit != 10 || got.CursorOrder != order || got.CursorSeq != 5 {
		t.Fatalf("filter = %+v", got)
	}
	do(h, call{method: http.MethodGet, path: "/v1/production/material-requirements", duties: []string{DutyRead}})
	if got := f.matFilters[1]; got != (store.MaterialFilter{Limit: defaultPageSize}) {
		t.Fatalf("default filter = %+v", got)
	}
}

func TestMaterialItems_FormatsAndFilters(t *testing.T) {
	item := ids.New()
	due := "2026-11-30"
	f := &fakeStore{}
	f.matItems = store.MaterialItems{Items: []store.MaterialItem{{ItemID: item, Orders: 2, Lines: 3, QtyRequired: decimal.RequireFromString("300"),
		QtyIssued: decimal.RequireFromString("0"), QtyOutstanding: decimal.RequireFromString("300"), EarliestDue: &due}}, NextCursor: item}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, duties: []string{DutyRead}, path: "/v1/production/material-requirements/by-item?limit=5&cursor=" + item})
	if rec.Code != http.StatusOK {
		t.Fatalf("by item: %d %s", rec.Code, rec.Body.String())
	}
	var v materialItemsView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if r := v.Items[0]; r.QtyOutstanding != "300" || r.Orders != 2 || r.Lines != 3 || *r.EarliestDue != due || v.NextCursor != item {
		t.Fatalf("view = %+v", v)
	}
	if got := f.matFilters[0]; got.CursorItem != item || got.Limit != 5 {
		t.Fatalf("filter = %+v", got)
	}
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/material-requirements/by-item", duties: []string{DutyCostRead}}), http.StatusForbidden, "AUTH-0003")
}

func TestMaterialRequirements_RejectsBadFiltersAndStoreFailure(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	p := expectProblem(t, do(h, call{method: http.MethodGet, duties: []string{DutyRead},
		path: "/v1/production/material-requirements?orderStatus=Open&itemId=v1&siteId=x&legalEntityId=y&orderId=z&dueFrom=2026-12-01&dueTo=2026-11-01&all=maybe&limit=501&cursor=bad"}),
		http.StatusBadRequest, validationCode)
	for _, name := range []string{"orderStatus", "itemId", "siteId", "legalEntityId", "orderId", "dueTo", "all", "limit", "cursor"} {
		if _, ok := p.Errors[name]; !ok {
			t.Errorf("no error for %s: %v", name, p.Errors)
		}
	}
	expectProblem(t, do(h, call{method: http.MethodGet, duties: []string{DutyRead}, path: "/v1/production/material-requirements/by-item?cursor=1:2"}), http.StatusBadRequest, validationCode)
	if len(f.matFilters) != 0 {
		t.Fatalf("a refused filter reached the store: %+v", f.matFilters)
	}
	_, h = newServer(t, &fakeStore{getErr: httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "database unavailable")})
	for _, path := range []string{"/v1/production/material-requirements", "/v1/production/material-requirements/by-item"} {
		expectProblem(t, do(h, call{method: http.MethodGet, path: path, duties: []string{DutyRead}}), http.StatusServiceUnavailable, "COM-0503")
	}
}
