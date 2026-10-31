package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func (f *fakeStore) ListCoverageGroups(_ context.Context, limit int) ([]store.CoverageGroup, error) {
	f.lastLimit = limit
	return append([]store.CoverageGroup{}, f.groups...), nil
}

func (f *fakeStore) GetCoverageGroup(_ context.Context, code string) (store.CoverageGroup, error) {
	for _, g := range f.groups {
		if g.Spec.Code == code {
			return g, nil
		}
	}
	return store.CoverageGroup{}, httpx.NewProblem(http.StatusNotFound, store.CodeCoverageNotFound, "coverage group not found")
}

func (f *fakeStore) CreateCoverageGroup(_ context.Context, req store.CoverageCreate) (store.CoverageGroup, bool, error) {
	if req.IdempotencyKey != "" {
		if hash, ok := f.groupKeys[req.IdempotencyKey]; ok {
			if hash != req.InputHash {
				return store.CoverageGroup{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
			}
			g, err := f.GetCoverageGroup(context.Background(), req.Spec.Code)
			return g, true, err
		}
	}
	if _, err := f.GetCoverageGroup(context.Background(), req.Spec.Code); err == nil {
		return store.CoverageGroup{}, false, httpx.NewProblem(http.StatusConflict, store.CodeCoverageExists, "exists")
	}
	if req.IdempotencyKey != "" {
		if f.groupKeys == nil {
			f.groupKeys = map[string]string{}
		}
		f.groupKeys[req.IdempotencyKey] = req.InputHash
	}
	g := store.CoverageGroup{ID: testkit.TenantID(len(f.groups) + 10), Version: 1, Spec: req.Spec}
	f.groups = append(f.groups, g)
	return g, false, nil
}

func (f *fakeStore) UpdateCoverageGroup(_ context.Context, expected int, spec domain.CoverageGroupSpec) (store.CoverageGroup, error) {
	for i, g := range f.groups {
		if g.Spec.Code != spec.Code {
			continue
		}
		if g.Version != expected {
			return store.CoverageGroup{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
		}
		f.groups[i] = store.CoverageGroup{ID: g.ID, Version: g.Version + 1, Spec: spec}
		return f.groups[i], nil
	}
	return store.CoverageGroup{}, httpx.NewProblem(http.StatusNotFound, store.CodeCoverageNotFound, "coverage group not found")
}

const (
	coveragePath = "/v1/planning/coverage-groups"
	dutySetup    = "planning.setup.maintain"
)

func decodeGroup(t *testing.T, rec *httptest.ResponseRecorder) coverageGroupDTO {
	t.Helper()
	var g coverageGroupDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return g
}

// TestCreateCoverageGroup_AppliesFR222Defaults: omitted fields take the PRD
// defaults (coverage time fence 100, negative days 1, positive days 0,
// firming and freeze 0, auto-firm off, messages on), also for explicit nulls.
func TestCreateCoverageGroup_AppliesFR222Defaults(t *testing.T) {
	st := &fakeStore{}
	rec := do(t, st, http.MethodPost, coveragePath, `{"code":"REQ","coverageCode":"requirement","generateActions":null}`, dutySetup, true, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("ETag %q, want \"1\"", rec.Header().Get("ETag"))
	}
	g := decodeGroup(t, rec)
	if g.CoverageTimeFenceDays != 100 || g.NegativeDays != 1 || g.PositiveDays != 0 || g.FirmingFenceDays != 0 ||
		g.FreezeFenceDays != 0 || g.AutoFirm || g.SafetyStockPolicy != "today" || !g.GenerateActions || !g.GenerateFutures || g.Version != 1 {
		t.Fatalf("FR-22.2 defaults not applied: %+v", g)
	}
}

func TestCreateCoverageGroup_Refusals(t *testing.T) {
	cases := []struct {
		name, body, duties string
		status             int
		code               string
		fields             []string
	}{
		{"invalid fields are named", `{"code":"x","coverageCode":"weekly","autoFirm":true}`, dutySetup, http.StatusBadRequest, codeValidation,
			[]string{"code", "coverageCode", "autoFirm"}},
		{"unknown safety policy", `{"code":"REQ","coverageCode":"requirement","safetyStockPolicy":"monthly"}`, dutySetup, http.StatusBadRequest, codeValidation,
			[]string{"safetyStockPolicy"}},
		{"period coverage needs period days", `{"code":"PER","coverageCode":"period"}`, dutySetup, http.StatusBadRequest, codeValidation,
			[]string{"periodDays"}},
		{"fence out of range", `{"code":"REQ","coverageCode":"requirement","firmingFenceDays":3651}`, dutySetup, http.StatusBadRequest, codeValidation,
			[]string{"firmingFenceDays"}},
		{"unknown field", `{"code":"REQ","coverageCode":"requirement","safetyPolicy":"x"}`, dutySetup, http.StatusBadRequest, codeValidation, nil},
		{"read duty cannot create", `{"code":"REQ","coverageCode":"requirement"}`, dutyRead, http.StatusForbidden, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, &fakeStore{}, http.MethodPost, coveragePath, tc.body, tc.duties, true, "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.code == "" {
				return
			}
			p := problemOf(t, rec)
			if p.Code != tc.code {
				t.Fatalf("code %q, want %q", p.Code, tc.code)
			}
			for _, f := range tc.fields {
				if _, ok := p.Errors[f]; !ok {
					t.Fatalf("problem does not name %s: %v", f, p.Errors)
				}
			}
		})
	}
}

func TestCreateCoverageGroup_DuplicateAndIdempotency(t *testing.T) {
	st := &fakeStore{}
	body := `{"code":"REQ","coverageCode":"requirement"}`
	if rec := do(t, st, http.MethodPost, coveragePath, body, dutySetup, true, "k-1"); rec.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}
	replay := do(t, st, http.MethodPost, coveragePath, body, dutySetup, true, "k-1")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %q", replay.Code, replay.Header().Get("Idempotent-Replayed"))
	}
	if len(st.groups) != 1 {
		t.Fatalf("replay created another group: %d", len(st.groups))
	}
	reuse := do(t, st, http.MethodPost, coveragePath, `{"code":"REQ","coverageCode":"minmax"}`, dutySetup, true, "k-1")
	if reuse.Code != http.StatusUnprocessableEntity || problemOf(t, reuse).Code != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %d %s", reuse.Code, reuse.Body.String())
	}
	dup := do(t, st, http.MethodPost, coveragePath, body, dutySetup, true, "")
	if dup.Code != http.StatusConflict || problemOf(t, dup).Code != store.CodeCoverageExists {
		t.Fatalf("duplicate: %d %s", dup.Code, dup.Body.String())
	}
}

func TestCoverageGroup_ListGetAndUpdate(t *testing.T) {
	st := &fakeStore{}
	do(t, st, http.MethodPost, coveragePath, `{"code":"REQ","coverageCode":"requirement"}`, dutySetup, true, "")

	list := do(t, st, http.MethodGet, coveragePath+"?limit=10", "", dutyRead, true, "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"code":"REQ"`) || st.lastLimit != 10 {
		t.Fatalf("list: %d %s (limit %d)", list.Code, list.Body.String(), st.lastLimit)
	}
	if rec := do(t, st, http.MethodGet, coveragePath+"?limit=0", "", dutyRead, true, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit 0: %d", rec.Code)
	}
	get := do(t, st, http.MethodGet, coveragePath+"/REQ", "", dutyRead, true, "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d etag %q", get.Code, get.Header().Get("ETag"))
	}
	if rec := do(t, st, http.MethodGet, coveragePath+"/NOPE", "", dutyRead, true, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown code: %d", rec.Code)
	}

	update := `{"coverageCode":"requirement","firmingFenceDays":5,"autoFirm":true}`
	if rec := doIfMatch(t, st, http.MethodPut, coveragePath+"/REQ", update, "", dutySetup); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("update without If-Match: %d", rec.Code)
	}
	if rec := doIfMatch(t, st, http.MethodPut, coveragePath+"/REQ", update, `"1"`, dutyRead); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty update: %d", rec.Code)
	}
	rec := doIfMatch(t, st, http.MethodPut, coveragePath+"/REQ", update, `"1"`, dutySetup)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("update: %d %s etag %q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	if g := decodeGroup(t, rec); !g.AutoFirm || g.FirmingFenceDays != 5 || g.Code != "REQ" || g.CoverageTimeFenceDays != 100 {
		t.Fatalf("updated group: %+v", g)
	}
	stale := doIfMatch(t, st, http.MethodPut, coveragePath+"/REQ", update, `"1"`, dutySetup)
	if stale.Code != http.StatusPreconditionFailed || problemOf(t, stale).Code != store.CodeVersionConflict {
		t.Fatalf("stale update: %d %s", stale.Code, stale.Body.String())
	}
	if rec := doIfMatch(t, st, http.MethodPut, coveragePath+"/REQ", `{"coverageCode":"requirement","autoFirm":true}`, `"2"`, dutySetup); rec.Code != http.StatusBadRequest {
		t.Fatalf("auto-firm without fence: %d", rec.Code)
	}
	if rec := doIfMatch(t, st, http.MethodPut, coveragePath+"/OTHER", update, `"1"`, dutySetup); rec.Code != http.StatusNotFound {
		t.Fatalf("update of unknown group: %d", rec.Code)
	}
}

// TestRunPlan_AutoFirmOnTheRunInput: the run input carries the firming fence
// and auto-firm, the engine marks the order, and auto-firm without a fence is
// refused.
func TestRunPlan_AutoFirmOnTheRunInput(t *testing.T) {
	body := func(coverage string) string {
		return `{"asOf":"2026-11-02","siteId":"` + siteID + `","items":[{"itemId":"` + fgID + `","coverage":` + coverage + `,
			"orderType":"purchase","leadTimeDays":2,"demand":[{"ref":"SO-1","refType":"sales-line","date":"2026-11-04","qty":"10"}]}]}`
	}
	st := &fakeStore{}
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", body(`{"code":"requirement","firmingFenceDays":3,"autoFirm":true}`), "planning.run", true, "run-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	if len(st.last.Result.Orders) != 1 || !st.last.Result.Orders[0].AutoFirmed {
		t.Fatalf("order inside the firming fence not auto-firmed: %+v", st.last.Result.Orders)
	}
	bad := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", body(`{"code":"requirement","autoFirm":true}`), "planning.run", true, "run-2")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("auto-firm without fence: %d %s", bad.Code, bad.Body.String())
	}
	if _, ok := problemOf(t, bad).Errors["items[0].coverage.autoFirm"]; !ok {
		t.Fatalf("problem does not name autoFirm: %s", bad.Body.String())
	}
}
