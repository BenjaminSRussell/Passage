package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

var lastTerminate store.LeaseTerminationInput

func (f *fakeStore) TerminateLease(_ context.Context, id string, in store.LeaseTerminationInput, _ string) (store.LeaseTerminationResult, error) {
	lastTerminate = in
	l, ok := f.leases[id]
	if !ok {
		return store.LeaseTerminationResult{}, httpx.NewProblem(http.StatusNotFound, store.CodeLeaseNotFound, "no lease")
	}
	if l.Version != in.Version {
		return store.LeaseTerminationResult{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	if err := domain.CanTerminate(l.Status); err != nil {
		return store.LeaseTerminationResult{}, err
	}
	l.Status, l.Version = domain.LeaseTerminated, l.Version+1
	f.leases[id] = l
	return store.LeaseTerminationResult{Termination: store.LeaseTermination{ID: ids.New(), LeaseID: id, AfterPeriod: in.AfterPeriod, GainLoss: decimal.Zero},
		Lease: l, Messages: []string{domain.CodeLeaseTermination + ": lease terminated"}}, nil
}

func TestTerminateLease_PreconditionsValidationAndHappyPath(t *testing.T) {
	h := server(newFake())
	lease := newLease(t, h)
	path := "/v1/leases/" + lease.ID + "/terminate"
	body := `{"afterPeriod":2,"terminationPayment":"500.00","reason":"site closed"}`
	terminate := func(body, etag, duties string, key bool) *httptest.ResponseRecorder {
		headers := map[string]string{}
		if etag != "" {
			headers["If-Match"] = etag
		}
		return do(h, req{method: http.MethodPost, path: path, body: body, duties: duties, key: key, headers: headers})
	}
	if rec := terminate(body, `"1"`, "AssetView", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without LeaseMaintain status = %d", rec.Code)
	}
	if rec := terminate(body, `"1"`, "LeaseMaintain", false); rec.Code != http.StatusBadRequest {
		t.Fatalf("without Idempotency-Key status = %d", rec.Code)
	}
	if rec := terminate(body, "", "LeaseMaintain", true); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("without If-Match status = %d", rec.Code)
	}
	long := `{"afterPeriod":2,"reason":"` + strings.Repeat("x", 501) + `"}`
	for name, tc := range map[string]struct{ body, field string }{
		"period zero":      {`{"afterPeriod":0}`, "afterPeriod"},
		"negative payment": {`{"afterPeriod":2,"terminationPayment":"-1"}`, "terminationPayment"},
		"payment text":     {`{"afterPeriod":2,"terminationPayment":"lots"}`, "terminationPayment"},
		"reason too long":  {long, "reason"},
	} {
		rec := terminate(tc.body, `"1"`, "LeaseMaintain", true)
		if rec.Code != http.StatusBadRequest || problem(t, rec).Errors[tc.field] == "" {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := terminate(body, `"7"`, "LeaseMaintain", true); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match status = %d", rec.Code)
	}
	rec := terminate(body, `"1"`, "LeaseMaintain", true)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("terminate status = %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if lastTerminate.Version != 1 || lastTerminate.AfterPeriod != 2 || lastTerminate.Payment.String() != "500" || lastTerminate.Reason != "site closed" {
		t.Fatalf("store received %+v", lastTerminate)
	}
	if !strings.Contains(rec.Body.String(), `"messages":["AST-0036`) || !strings.Contains(rec.Body.String(), `"status":"Terminated"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// A terminated lease cannot be terminated again.
	if rec := terminate(body, `"2"`, "LeaseMaintain", true); rec.Code != http.StatusUnprocessableEntity || problem(t, rec).Code != domain.CodeLeaseTermination {
		t.Fatalf("second termination status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, req{method: http.MethodPost, path: "/v1/leases/x/terminate", body: body, duties: "LeaseMaintain", key: true,
		headers: map[string]string{"If-Match": `"1"`}}); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed lease id status = %d", rec.Code)
	}
}
