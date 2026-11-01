package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

func (f *fakeStore) ApplyFiniteDates(_ context.Context, in store.FiniteDates) (store.Order, bool, error) {
	f.finite = append(f.finite, in)
	if f.finiteErr != nil {
		return store.Order{}, false, f.finiteErr
	}
	return store.Order{ID: in.OrderID, Version: 5, ScheduleBasis: domain.BasisFinite}, f.finiteChanged, nil
}

func finiteCall(id, body string, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/production/orders/" + id + ":" + actionApplyFiniteDates, body: body, duties: duties,
		headers: map[string]string{"If-Match": `"4"`}}
}

const oneWindow = `{"operations":[{"operNumber":10,"start":"2026-11-02T08:00:00Z","end":"2026-11-02T09:00:00Z"}]}`

func TestApplyFiniteDates_PassesTheWindows(t *testing.T) {
	f := &fakeStore{finiteChanged: true}
	_, h := newServer(t, f)
	id := ids.New()
	rec := do(h, finiteCall(id, `{"runId":"run-7","operations":[{"operNumber":10,"start":"2026-11-02T08:00:00Z","end":"2026-11-04T08:00:00+01:00"}]}`, DutyWrite))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("apply: %d %s %v", rec.Code, rec.Body.String(), rec.Header())
	}
	got := f.finite[0]
	if got.OrderID != id || got.RunID != "run-7" || got.IfMatch != 4 || got.Actor != "planner-1" || len(got.Windows) != 1 || got.Windows[0].OperNumber != 10 ||
		got.Windows[0].End.UTC().Hour() != 7 {
		t.Fatalf("finite: %+v", got)
	}
}

func TestApplyFiniteDates_NoChangeIsMarkedReplayed(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	rec := do(h, finiteCall(ids.New(), oneWindow, DutyWrite))
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("no-op: %d %v", rec.Code, rec.Header())
	}
}

func TestApplyFiniteDates_RefusesBadInput(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	for name, body := range map[string]string{
		"no windows": `{"operations":[]}`,
		"backwards":  `{"operations":[{"operNumber":10,"start":"2026-11-02T09:00:00Z","end":"2026-11-02T08:00:00Z"}]}`,
		"missing":    `{"operations":[{"operNumber":10}]}`,
		"bad number": `{"operations":[{"operNumber":0,"start":"2026-11-02T08:00:00Z","end":"2026-11-02T09:00:00Z"}]}`,
		"twice": `{"operations":[{"operNumber":10,"start":"2026-11-02T08:00:00Z","end":"2026-11-02T09:00:00Z"},
			{"operNumber":10,"start":"2026-11-02T08:00:00Z","end":"2026-11-02T09:00:00Z"}]}`,
	} {
		if rec := do(h, finiteCall(ids.New(), body, DutyWrite)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(f.finite) != 0 {
		t.Error("an invalid request must not reach the store")
	}
}

func TestApplyFiniteDates_NeedsTheWriteDuty(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	if rec := do(h, finiteCall(ids.New(), `{}`, DutyRead)); rec.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", rec.Code)
	}
}

// A person must say which version the windows were typed against, so a stale form cannot
// overwrite newer dates; a malformed If-Match is refused like on every other order write.
func TestApplyFiniteDates_PeopleNeedIfMatch(t *testing.T) {
	f := &fakeStore{finiteChanged: true}
	_, h := newServer(t, f)
	c := finiteCall(ids.New(), oneWindow, DutyWrite)
	c.headers = nil
	expectProblem(t, do(h, c), http.StatusPreconditionRequired, "precondition_required")
	c.headers = map[string]string{"If-Match": "soon"}
	expectProblem(t, do(h, c), http.StatusBadRequest, validationCode)
	if len(f.finite) != 0 {
		t.Fatal("a request without a usable If-Match must not reach the store")
	}
}

// A service applying a scheduling run (the consumer handoff) may omit If-Match; it is passed on
// when sent.
func TestApplyFiniteDates_ServiceMayOmitIfMatch(t *testing.T) {
	f := &fakeStore{finiteChanged: true}
	a, _ := newServer(t, f)
	id := ids.New()
	for want, header := range map[int]string{0: "", 6: `"6"`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/production/orders/"+id+":"+actionApplyFiniteDates, strings.NewReader(oneWindow))
		if header != "" {
			req.Header.Set("If-Match", header)
		}
		req = req.WithContext(tenant.With(req.Context(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "planner-1",
			Service: "scheduling", Duties: map[string]struct{}{DutyWrite: {}}}))
		rec := httptest.NewRecorder()
		a.applyFiniteDates(rec, req, id)
		if rec.Code != http.StatusOK || f.finite[len(f.finite)-1].IfMatch != want {
			t.Fatalf("service, If-Match %q: %d %s %+v", header, rec.Code, rec.Body.String(), f.finite)
		}
	}
}
