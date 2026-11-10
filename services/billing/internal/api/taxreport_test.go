package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

type fakeTaxReports struct {
	listed  string
	retried string
	err     error

	backfilled string
}

func (f *fakeTaxReports) ListTaxReports(_ context.Context, status string, _ int) ([]store.TaxReport, error) {
	f.listed = status
	return []store.TaxReport{{ID: "r1", Status: store.TaxReportParked}}, nil
}

func (f *fakeTaxReports) BackfillTaxReports(_ context.Context, after string, _ int) (store.TaxBackfill, error) {
	f.backfilled = after
	return store.TaxBackfill{Scanned: 2, Queued: 1}, f.err
}

func (f *fakeTaxReports) RetryTaxReport(_ context.Context, id string) (store.TaxReport, error) {
	f.retried = id
	return store.TaxReport{ID: id, Status: store.TaxReportPending}, f.err
}

// #29: parked tax reports are listed under invoice view and retried under invoice post, and nothing else may; an unknown
// status is a 400, a report that is not parked is a 409 and a deployment without the store answers 503.
func TestTaxReports_OperatorListAndRetry(t *testing.T) {
	fake := &fakeTaxReports{}
	a, h := setupAPI(t, newFakeStore(), &fakeTax{perLine: "19.00"})
	a.WithTaxReports(fake)
	id := "018f0000-0000-7000-8000-0000000000aa"

	if rec := doAs(h, http.MethodGet, "/v1/billing/tax-reports?status=PARKED", "", "", dutyView, nil); rec.Code != http.StatusOK || fake.listed != "PARKED" || !strings.Contains(rec.Body.String(), `"r1"`) {
		t.Fatalf("list = %d %s (status %q)", rec.Code, rec.Body, fake.listed)
	}
	if rec := doAs(h, http.MethodGet, "/v1/billing/tax-reports?status=DONE", "", "", dutyView, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown status = %d", rec.Code)
	}
	if rec := doAs(h, http.MethodGet, "/v1/billing/tax-reports", "", "", dutyPost, nil); rec.Code != http.StatusForbidden {
		t.Errorf("list with the post duty = %d, want 403", rec.Code)
	}
	for _, duty := range []string{dutyView, dutyMaintain, ""} {
		if rec := doAs(h, http.MethodPost, "/v1/billing/tax-reports/"+id+":retry", "", "", duty, nil); rec.Code != http.StatusForbidden || fake.retried != "" {
			t.Errorf("retry with duty %q = %d (retried %q), want 403", duty, rec.Code, fake.retried)
		}
	}
	if rec := doAs(h, http.MethodPost, "/v1/billing/tax-reports/"+id+":retry", "", "", dutyPost, nil); rec.Code != http.StatusOK || fake.retried != id {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(h, http.MethodPost, "/v1/billing/tax-reports/not-a-uuid:retry", "", "", dutyPost, nil); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id = %d", rec.Code)
	}
	fake.err = httpx.NewProblem(http.StatusConflict, store.CodeTaxReportState, "the tax report is REPORTED")
	if rec := doAs(h, http.MethodPost, "/v1/billing/tax-reports/"+id+":retry", "", "", dutyPost, nil); rec.Code != http.StatusConflict {
		t.Errorf("reported report = %d, want 409", rec.Code)
	}

	bare := setup(t, newFakeStore(), &fakeTax{perLine: "19.00"})
	if rec := doAs(bare, http.MethodGet, "/v1/billing/tax-reports", "", "", dutyView, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without a store = %d, want 503", rec.Code)
	}
}

// Documents booked before reports existed are queued by an operator under invoice post, a pass at a time.
func TestTaxReports_OperatorBackfill(t *testing.T) {
	fake := &fakeTaxReports{}
	a, h := setupAPI(t, newFakeStore(), &fakeTax{perLine: "19.00"})
	a.WithTaxReports(fake)
	if rec := doAs(h, http.MethodPost, "/v1/billing/tax-reports:backfill", "", "", dutyView, nil); rec.Code != http.StatusForbidden {
		t.Errorf("backfill with the view duty = %d, want 403", rec.Code)
	}
	rec := doAs(h, http.MethodPost, "/v1/billing/tax-reports:backfill?after=018f0000-0000-7000-8000-0000000000aa", "", "", dutyPost, nil)
	if rec.Code != http.StatusOK || fake.backfilled != "018f0000-0000-7000-8000-0000000000aa" || !strings.Contains(rec.Body.String(), `"queued":1`) {
		t.Fatalf("backfill = %d %s (after %q)", rec.Code, rec.Body, fake.backfilled)
	}
	if rec := doAs(h, http.MethodPost, "/v1/billing/tax-reports:backfill?after=x", "", "", dutyPost, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad cursor = %d, want 400", rec.Code)
	}
}
