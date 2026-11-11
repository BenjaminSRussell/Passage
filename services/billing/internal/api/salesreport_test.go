package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

type fakeSalesReports struct {
	listed  string
	retried string
	err     error
	limit   int
	after   string
	more    bool
}

func (f *fakeSalesReports) ListSalesReportsPage(_ context.Context, status string, limit int, after string) ([]store.SalesReport, string, error) {
	f.listed, f.limit, f.after = status, limit, after
	if after == "" && f.more {
		return []store.SalesReport{{ID: "r1", Status: store.SalesReportParked}}, "2027-01-01T00:00:00Z|r1", nil
	}
	return []store.SalesReport{{ID: "r1", Status: store.SalesReportParked}}, "", nil
}

// #553: the list pages with a cursor instead of showing the newest 200 only.
func TestSalesReports_ListPagesWithACursor(t *testing.T) {
	fake := &fakeSalesReports{more: true}
	a, h := setupAPI(t, newFakeStore(), &fakeTax{perLine: "19.00"})
	a.WithSalesReports(fake)
	rec := doAs(h, http.MethodGet, "/v1/billing/sales-reports?limit=1", "", "", dutyView, nil)
	if rec.Code != http.StatusOK || fake.limit != 1 || !strings.Contains(rec.Body.String(), `"nextCursor"`) {
		t.Fatalf("first page = %d %s (limit %d)", rec.Code, rec.Body, fake.limit)
	}
	cursor := base64.RawURLEncoding.EncodeToString([]byte("2027-01-01T00:00:00Z|r1"))
	rec = doAs(h, http.MethodGet, "/v1/billing/sales-reports?limit=1&cursor="+cursor, "", "", dutyView, nil)
	if rec.Code != http.StatusOK || fake.after != "2027-01-01T00:00:00Z|r1" || strings.Contains(rec.Body.String(), `"nextCursor"`) {
		t.Fatalf("second page = %d %s (after %q)", rec.Code, rec.Body, fake.after)
	}
	for _, q := range []string{"limit=0", "limit=201", "limit=x", "cursor=***"} {
		if rec := doAs(h, http.MethodGet, "/v1/billing/sales-reports?"+q, "", "", dutyView, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, rec.Code)
		}
	}
}

func (f *fakeSalesReports) RetrySalesReport(_ context.Context, id string) (store.SalesReport, error) {
	f.retried = id
	return store.SalesReport{ID: id, Status: store.SalesReportPending}, f.err
}

// #29: parked sales reports are listed under invoice view and retried under invoice post, and nothing else may; an unknown
// status is a 400, a report that is not parked is a 409 and a deployment without the store answers 503.
func TestSalesReports_OperatorListAndRetry(t *testing.T) {
	fake := &fakeSalesReports{}
	a, h := setupAPI(t, newFakeStore(), &fakeTax{perLine: "19.00"})
	a.WithSalesReports(fake)
	id := "018f0000-0000-7000-8000-0000000000aa"

	if rec := doAs(h, http.MethodGet, "/v1/billing/sales-reports?status=PARKED", "", "", dutyView, nil); rec.Code != http.StatusOK || fake.listed != "PARKED" || !strings.Contains(rec.Body.String(), `"r1"`) {
		t.Fatalf("list = %d %s (status %q)", rec.Code, rec.Body, fake.listed)
	}
	if rec := doAs(h, http.MethodGet, "/v1/billing/sales-reports?status=DONE", "", "", dutyView, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown status = %d", rec.Code)
	}
	if rec := doAs(h, http.MethodGet, "/v1/billing/sales-reports", "", "", dutyPost, nil); rec.Code != http.StatusForbidden {
		t.Errorf("list with the post duty = %d, want 403", rec.Code)
	}
	for _, duty := range []string{dutyView, dutyMaintain, ""} {
		if rec := doAs(h, http.MethodPost, "/v1/billing/sales-reports/"+id+":retry", "", "", duty, nil); rec.Code != http.StatusForbidden || fake.retried != "" {
			t.Errorf("retry with duty %q = %d (retried %q), want 403", duty, rec.Code, fake.retried)
		}
	}
	if rec := doAs(h, http.MethodPost, "/v1/billing/sales-reports/"+id+":retry", "", "", dutyPost, nil); rec.Code != http.StatusOK || fake.retried != id {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(h, http.MethodPost, "/v1/billing/sales-reports/not-a-uuid:retry", "", "", dutyPost, nil); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id = %d", rec.Code)
	}
	fake.err = httpx.NewProblem(http.StatusConflict, store.CodeSalesReportState, "the sales report is REPORTED")
	if rec := doAs(h, http.MethodPost, "/v1/billing/sales-reports/"+id+":retry", "", "", dutyPost, nil); rec.Code != http.StatusConflict {
		t.Errorf("reported report = %d, want 409", rec.Code)
	}

	bare := setup(t, newFakeStore(), &fakeTax{perLine: "19.00"})
	if rec := doAs(bare, http.MethodGet, "/v1/billing/sales-reports", "", "", dutyView, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without a store = %d, want 503", rec.Code)
	}
}
