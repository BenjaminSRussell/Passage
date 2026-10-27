package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

// fakeStore counts calls and returns fixed bodies so handler logic is tested alone.
type fakeStore struct {
	calls   int
	run     store.RunRequest
	filter  string
	recon   store.ReconcileRequest
	topDown store.TopDownRequest
	settle  store.SettlementRequest

	measureValues store.MeasureValuesRequest
	template      store.AllocationTemplate
	expected      int
	templateRun   store.TemplateRunRequest
}

func (f *fakeStore) CreateCostObject(context.Context, string, string, store.CostObject) (int, []byte, error) {
	f.calls++
	return http.StatusCreated, []byte(`{"id":"o"}`), nil
}

func (f *fakeStore) CreateCostElement(context.Context, string, string, store.CostElement) (int, []byte, error) {
	f.calls++
	return http.StatusCreated, []byte(`{"id":"e"}`), nil
}

func (f *fakeStore) MirrorJournalLines(context.Context, string, string, store.MirrorRequest) (int, []byte, error) {
	f.calls++
	return http.StatusOK, []byte(`{"mirrored":1,"skipped":0}`), nil
}

func (f *fakeStore) RunAllocation(_ context.Context, _, _ string, in store.RunRequest) (int, []byte, error) {
	f.calls++
	f.run = in
	return http.StatusCreated, []byte(`{"runId":"r"}`), nil
}

func (f *fakeStore) EntriesFor(context.Context, string, string) ([]store.Entry, error) {
	f.calls++
	return nil, nil
}

func (f *fakeStore) ListCostObjects(_ context.Context, le, kind string) ([]store.CostObject, error) {
	f.calls++
	f.filter = le + "|" + kind
	return []store.CostObject{{ID: objA, Code: "CC-100"}}, nil
}

func (f *fakeStore) GetCostObject(context.Context, string) (store.CostObject, error) {
	f.calls++
	return store.CostObject{ID: objA}, nil
}

func (f *fakeStore) ListCostElements(context.Context, string) ([]store.CostElement, error) {
	f.calls++
	return nil, nil
}

func (f *fakeStore) ListRuns(context.Context, string) ([]store.Run, error) {
	f.calls++
	return nil, nil
}

func (f *fakeStore) GetRun(context.Context, string) (store.Run, error) {
	f.calls++
	return store.Run{}, nil
}

func (f *fakeStore) Reconcile(_ context.Context, in store.ReconcileRequest) (domain.Reconciliation, error) {
	f.calls++
	f.recon = in
	return domain.Reconciliation{Reconciled: true}, nil
}

func (f *fakeStore) Ping(context.Context) error { return nil }

const (
	tenantID = "00000000-0000-0000-0000-000000000001"
	entityID = "0196a6a4-0000-7000-8000-000000000010"
	objA     = "0196a6a4-0000-7000-8000-000000000011"
	objB     = "0196a6a4-0000-7000-8000-000000000012"
	elemID   = "0196a6a4-0000-7000-8000-000000000013"
	lineID   = "0196a6a4-0000-7000-8000-000000000014"
)

func serve(t *testing.T, st Store, method, path, body, duties, key string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	New(st).Register(r)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(idempotencyKey, key)
	}
	// httpx.NewRouter's tenant middleware is skipped here; the principal is attached directly.
	req = req.WithContext(testkit.Ctx(tenantID, strings.Split(duties, ",")...))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, rec.Body.String())
	}
	return p.Code
}

func TestCommands_DutyAndIdempotency(t *testing.T) {
	body := `{"legalEntityId":"` + entityID + `","kind":"CostCenter","code":"CC-100","name":"Plant"}`
	cases := []struct {
		name   string
		duties string
		key    string
		status int
		code   string
	}{
		{"missing duty", DutyView, "k1", http.StatusForbidden, "AUTH-0003"},
		{"missing key", DutyMaintain, "", http.StatusBadRequest, "COM-0001"},
		{"ok", DutyMaintain, "k1", http.StatusCreated, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &fakeStore{}
			rec := serve(t, st, http.MethodPost, "/v1/controlling/cost-objects", body, c.duties, c.key)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d (%s)", rec.Code, c.status, rec.Body.String())
			}
			if c.code != "" && problemCode(t, rec) != c.code {
				t.Fatalf("code %q, want %q", problemCode(t, rec), c.code)
			}
		})
	}
}

func TestCostObject_Validation(t *testing.T) {
	cases := map[string]string{
		"bad kind":   `{"legalEntityId":"` + entityID + `","kind":"Asset","code":"CC-1","name":"x"}`,
		"lower code": `{"legalEntityId":"` + entityID + `","kind":"CostCenter","code":"cc-1","name":"x"}`,
		"no name":    `{"legalEntityId":"` + entityID + `","kind":"CostCenter","code":"CC-1","name":""}`,
	}
	for name, body := range cases {
		st := &fakeStore{}
		rec := serve(t, st, http.MethodPost, "/v1/controlling/cost-objects", body, DutyMaintain, "k")
		if rec.Code != http.StatusBadRequest || st.calls != 0 {
			t.Errorf("%s: status %d calls %d", name, rec.Code, st.calls)
		}
	}
}

func TestCostElement_RangeRules(t *testing.T) {
	cases := map[string]struct {
		body string
		ok   bool
	}{
		"primary ok":       {`{"legalEntityId":"` + entityID + `","kind":"Primary","code":"PR-600","name":"Payroll","mainAccountFrom":"600000","mainAccountTo":"609999"}`, true},
		"primary reversed": {`{"legalEntityId":"` + entityID + `","kind":"Primary","code":"PR-600","name":"Payroll","mainAccountFrom":"609999","mainAccountTo":"600000"}`, false},
		"primary no range": {`{"legalEntityId":"` + entityID + `","kind":"Primary","code":"PR-600","name":"Payroll"}`, false},
		"secondary ranged": {`{"legalEntityId":"` + entityID + `","kind":"Secondary","code":"SE-100","name":"Alloc","mainAccountFrom":"1"}`, false},
		"secondary ok":     {`{"legalEntityId":"` + entityID + `","kind":"Secondary","code":"SE-100","name":"Alloc"}`, true},
	}
	for name, c := range cases {
		st := &fakeStore{}
		rec := serve(t, st, http.MethodPost, "/v1/controlling/cost-elements", c.body, DutyMaintain, "k")
		if (rec.Code == http.StatusCreated) != c.ok {
			t.Errorf("%s: status %d (%s)", name, rec.Code, rec.Body.String())
		}
	}
}

func TestMirror_RejectsBadLines(t *testing.T) {
	cases := map[string]string{
		"no lines":   `{"legalEntityId":"` + entityID + `","lines":[]}`,
		"bad period": `{"legalEntityId":"` + entityID + `","lines":[{"journalLineId":"` + lineID + `","mainAccount":"600100","costObjectId":"` + objA + `","period":"2026-13","currency":"USD","amount":"10.00"}]}`,
		"5dp amount": `{"legalEntityId":"` + entityID + `","lines":[{"journalLineId":"` + lineID + `","mainAccount":"600100","costObjectId":"` + objA + `","period":"2026-09","currency":"USD","amount":"10.00001"}]}`,
		"bad curr":   `{"legalEntityId":"` + entityID + `","lines":[{"journalLineId":"` + lineID + `","mainAccount":"600100","costObjectId":"` + objA + `","period":"2026-09","currency":"usd","amount":"10"}]}`,
	}
	for name, body := range cases {
		st := &fakeStore{}
		rec := serve(t, st, http.MethodPost, "/v1/controlling/cost-entries:mirror", body, DutyMaintain, "k")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, rec.Code)
		}
		if st.calls != 0 {
			t.Errorf("%s: store was called", name)
		}
	}
}

func TestAllocationRun_ValidatesSquareBasis(t *testing.T) {
	good := `{"legalEntityId":"` + entityID + `","period":"2026-09","currency":"USD","secondaryElementId":"` + elemID +
		`","costObjectIds":["` + objA + `","` + objB + `"],"basis":[["0","1"],["1","0"]]}`
	st := &fakeStore{}
	rec := serve(t, st, http.MethodPost, "/v1/controlling/allocation-runs", good, DutyRun, "k")
	if rec.Code != http.StatusCreated {
		t.Fatalf("good run: status %d (%s)", rec.Code, rec.Body.String())
	}
	if len(st.run.Basis) != 2 || st.run.CostObjectIDs[0] != objA {
		t.Errorf("store got %+v", st.run)
	}

	bad := strings.Replace(good, `[["0","1"],["1","0"]]`, `[["0","1"]]`, 1)
	if rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/controlling/allocation-runs", bad, DutyRun, "k2"); rec.Code != http.StatusBadRequest {
		t.Errorf("non-square basis: status %d", rec.Code)
	}
	neg := strings.Replace(good, `["0","1"],["1","0"]`, `["0","-1"],["1","0"]`, 1)
	if rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/controlling/allocation-runs", neg, DutyRun, "k3"); rec.Code != http.StatusBadRequest {
		t.Errorf("negative basis: status %d", rec.Code)
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/controlling/allocation-runs", good, DutyMaintain, "k4"); rec.Code != http.StatusForbidden {
		t.Errorf("run without CostAccountingRun: status %d", rec.Code)
	}
}

func TestEntries_RequiresPeriod(t *testing.T) {
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/cost-objects/"+objA+"/entries", "", DutyView, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("missing period: status %d", rec.Code)
	}
	rec := serve(t, st, http.MethodGet, "/v1/controlling/cost-objects/"+objA+"/entries?period=2026-09", "", DutyView, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("list: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestReads_FiltersAndDuty(t *testing.T) {
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/cost-objects", "", DutyMaintain, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("list without view duty: %d", rec.Code)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/cost-objects?kind=Asset", "", DutyView, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad kind filter: %d", rec.Code)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/allocation-runs?legalEntityId=x", "", DutyView, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad legal entity filter: %d", rec.Code)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/allocation-runs/nope", "", DutyView, ""); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeRunNotFound {
		t.Fatalf("bad run id: %d", rec.Code)
	}
	if st.calls != 0 {
		t.Fatalf("rejected reads reached the store %d times", st.calls)
	}
	rec := serve(t, st, http.MethodGet, "/v1/controlling/cost-objects?legalEntityId="+entityID+"&kind=CostCenter", "", DutyView, "")
	if rec.Code != http.StatusOK || st.filter != entityID+"|CostCenter" || !strings.Contains(rec.Body.String(), "CC-100") {
		t.Fatalf("list: %d filter %q body %s", rec.Code, st.filter, rec.Body.String())
	}
	rec = serve(t, st, http.MethodGet, "/v1/controlling/cost-elements", "", DutyView, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty list must be an array: %s", rec.Body.String())
	}
}

func TestReconcile_ValidatesAndPassesBalances(t *testing.T) {
	st := &fakeStore{}
	bad := `{"legalEntityId":"` + entityID + `","period":"2026-9","currency":"USD","journalBalances":[]}`
	if rec := serve(t, st, http.MethodPost, "/v1/controlling/reconciliations", bad, DutyView, ""); rec.Code != http.StatusBadRequest || st.calls != 0 {
		t.Fatalf("bad period: %d", rec.Code)
	}
	good := `{"legalEntityId":"` + entityID + `","period":"2026-09","currency":"USD","journalBalances":[{"mainAccount":"600100","amount":"150.25"}]}`
	rec := serve(t, st, http.MethodPost, "/v1/controlling/reconciliations", good, DutyView, "")
	if rec.Code != http.StatusOK || len(st.recon.Journal) != 1 || st.recon.Journal[0].Amount.String() != "150.25" {
		t.Fatalf("status %d recon %+v", rec.Code, st.recon)
	}
}
