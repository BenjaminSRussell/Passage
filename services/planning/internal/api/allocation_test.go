package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const (
	allocKeysPath = "/v1/planning/forecast-allocation-keys"
	allocItemA    = "00000000-0000-0000-0000-0000000000a1"
	allocItemB    = "00000000-0000-0000-0000-0000000000b2"
)

func (f *fakeStore) ListAllocationKeys(_ context.Context, limit int) ([]store.AllocationKey, error) {
	f.lastLimit = limit
	out := make([]store.AllocationKey, len(f.allocKeys))
	for i, k := range f.allocKeys {
		k.Spec.Lines = nil
		out[i] = k
	}
	return out, nil
}

func (f *fakeStore) GetAllocationKey(_ context.Context, code string) (store.AllocationKey, error) {
	for _, k := range f.allocKeys {
		if k.Spec.Code == code {
			return k, nil
		}
	}
	return store.AllocationKey{}, httpx.NewProblem(http.StatusNotFound, store.CodeAllocationKeyNotFound, "allocation key not found")
}

func (f *fakeStore) CreateAllocationKey(_ context.Context, req store.AllocationKeyCreate) (store.AllocationKey, bool, error) {
	if _, err := f.GetAllocationKey(context.Background(), req.Spec.Code); err == nil {
		return store.AllocationKey{}, false, httpx.NewProblem(http.StatusConflict, store.CodeAllocationKeyExists, "exists")
	}
	k := store.AllocationKey{ID: "00000000-0000-0000-0000-0000000000k1", Version: 1, Spec: req.Spec, LineCount: len(req.Spec.Lines)}
	f.allocKeys = append(f.allocKeys, k)
	return k, false, nil
}

func (f *fakeStore) UpdateAllocationKey(_ context.Context, expected int, spec domain.AllocationKeySpec) (store.AllocationKey, error) {
	for i, k := range f.allocKeys {
		if k.Spec.Code != spec.Code {
			continue
		}
		if k.Version != expected {
			return store.AllocationKey{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
		}
		f.allocKeys[i] = store.AllocationKey{ID: k.ID, Version: k.Version + 1, Spec: spec, LineCount: len(spec.Lines)}
		return f.allocKeys[i], nil
	}
	return store.AllocationKey{}, httpx.NewProblem(http.StatusNotFound, store.CodeAllocationKeyNotFound, "allocation key not found")
}

func (f *fakeStore) AllocateForecast(_ context.Context, req store.AllocateRequest) (store.AllocateOutcome, error) {
	k, err := f.GetAllocationKey(context.Background(), req.KeyCode)
	if err != nil {
		return store.AllocateOutcome{}, err
	}
	if req.ExpectedVersion != 0 {
		return store.AllocateOutcome{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	f.allocated = req
	return store.AllocateOutcome{Model: store.ForecastModel{Code: req.ModelCode, Version: 1, ReductionPrinciple: domain.ReductionTransactions, ReductionPercent: "0", Status: domain.ConsensusDraft},
		Key: k, Items: len(k.Spec.Lines), CellsWritten: len(k.Spec.Lines) * len(req.Periods)}, nil
}

const allocKeyBody = `{"code":"FAM-A","name":"Family A","forecastUom":"CASE","lines":[` +
	`{"itemId":"` + allocItemA + `","share":"60"},{"itemId":"` + allocItemB + `","share":"40","uomFactor":"6"}]}`

func TestAllocationKey_CreateDefaultsFactorAndReplays(t *testing.T) {
	st := &fakeStore{}
	rec := do(t, st, http.MethodPost, allocKeysPath, allocKeyBody, dutySetup, true, "")
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s etag %q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	var k allocationKeyDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil {
		t.Fatal(err)
	}
	if k.LineCount != 2 || k.Lines[0].UomFactor.String() != "1" || k.Lines[1].UomFactor.String() != "6" || k.Lines[0].Share.String() != "60" {
		t.Fatalf("key: %+v", k)
	}
	dup := do(t, st, http.MethodPost, allocKeysPath, allocKeyBody, dutySetup, true, "")
	if dup.Code != http.StatusConflict || problemOf(t, dup).Code != store.CodeAllocationKeyExists {
		t.Fatalf("duplicate: %d %s", dup.Code, dup.Body.String())
	}
}

func TestAllocationKey_Refusals(t *testing.T) {
	cases := []struct {
		name, body, duties string
		status             int
		fields             []string
	}{
		{"shares must add up to 100", `{"code":"FAM-A","name":"n","forecastUom":"CASE","lines":[{"itemId":"` + allocItemA + `","share":"99"}]}`, dutySetup, http.StatusBadRequest, []string{"lines"}},
		{"bad fields named", `{"code":"x","name":"","forecastUom":"","lines":[{"itemId":"nope","share":"100","uomFactor":"0"}]}`, dutySetup, http.StatusBadRequest,
			[]string{"code", "name", "forecastUom", "lines[0].itemId", "lines[0].uomFactor"}},
		{"same item in capitals repeats it", `{"code":"FAM-A","name":"n","forecastUom":"CASE","lines":[{"itemId":"` + allocItemA + `","share":"50"},{"itemId":"` + strings.ToUpper(allocItemA) + `","share":"50"}]}`,
			dutySetup, http.StatusBadRequest, []string{"lines[1].itemId"}},
		{"unknown field", `{"code":"FAM-A","name":"n","forecastUom":"CASE","lines":[],"extra":1}`, dutySetup, http.StatusBadRequest, nil},
		{"read duty cannot create", allocKeyBody, dutyRead, http.StatusForbidden, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, &fakeStore{}, http.MethodPost, allocKeysPath, tc.body, tc.duties, true, "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status != http.StatusBadRequest {
				return
			}
			p := problemOf(t, rec)
			for _, f := range tc.fields {
				if _, ok := p.Errors[f]; !ok {
					t.Fatalf("problem does not name %s: %v", f, p.Errors)
				}
			}
		})
	}
}

func TestAllocationKey_ListGetAndUpdate(t *testing.T) {
	st := &fakeStore{}
	do(t, st, http.MethodPost, allocKeysPath, allocKeyBody, dutySetup, true, "")
	list := do(t, st, http.MethodGet, allocKeysPath+"?limit=5", "", dutyRead, true, "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"lineCount":2`) || st.lastLimit != 5 {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	if get := do(t, st, http.MethodGet, allocKeysPath+"/FAM-A", "", dutyRead, true, ""); get.Code != http.StatusOK || !strings.Contains(get.Body.String(), allocItemB) {
		t.Fatalf("get: %d %s", get.Code, get.Body.String())
	}
	if rec := do(t, st, http.MethodGet, allocKeysPath+"/lower", "", dutyRead, true, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("ill-formed code: %d", rec.Code)
	}
	update := `{"name":"Family A v2","forecastUom":"PALLET","lines":[{"itemId":"` + allocItemA + `","share":"100"}]}`
	if rec := doIfMatch(t, st, http.MethodPut, allocKeysPath+"/FAM-A", update, "", dutySetup); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	rec := doIfMatch(t, st, http.MethodPut, allocKeysPath+"/FAM-A", update, `"1"`, dutySetup)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if stale := doIfMatch(t, st, http.MethodPut, allocKeysPath+"/FAM-A", update, `"1"`, dutySetup); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", stale.Code)
	}
	if rec := doIfMatch(t, st, http.MethodPut, allocKeysPath+"/NOPE", update, `"1"`, dutySetup); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}
	if rec := doIfMatch(t, st, http.MethodPut, allocKeysPath+"/FAM-A", update, `"2"`, dutyRead); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty update: %d", rec.Code)
	}
}

const allocatePath = "/v1/planning/forecasts/FC-MAIN:allocate"

func allocateBody(periods string) string {
	return `{"keyCode":"FAM-A","siteId":"` + siteID + `","periods":` + periods + `}`
}

func TestAllocateForecast_WritesAndSendsETag(t *testing.T) {
	st := &fakeStore{}
	do(t, st, http.MethodPost, allocKeysPath, allocKeyBody, dutySetup, true, "")
	rec := doIfMatch(t, st, http.MethodPost, allocatePath,
		allocateBody(`[{"periodStart":"2026-12-01","periodEnd":"2026-12-31","qty":"100.5"},{"periodStart":"2027-01-01","periodEnd":"2027-01-31","qty":"0"}]`), `"0"`, dutyForecasts)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("allocate: %d %s etag %q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	var body struct {
		KeyCode      string
		Items        int
		Periods      int
		CellsWritten int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.KeyCode != "FAM-A" || body.Items != 2 || body.Periods != 2 || body.CellsWritten != 4 {
		t.Fatalf("body: %+v", body)
	}
	if st.allocated.ModelCode != "FC-MAIN" || st.allocated.Periods[0].Qty.String() != "100.5" || st.allocated.SiteID != siteID {
		t.Fatalf("store got %+v", st.allocated)
	}
}

func TestAllocateForecast_Refusals(t *testing.T) {
	st := &fakeStore{}
	do(t, st, http.MethodPost, allocKeysPath, allocKeyBody, dutySetup, true, "")
	period := `{"periodStart":"2026-12-01","periodEnd":"2026-12-31","qty":"10"}`
	cases := []struct {
		name, body, ifMatch, duties string
		status                      int
		field                       string
	}{
		{"needs If-Match", allocateBody("[" + period + "]"), "", dutyForecasts, http.StatusPreconditionRequired, ""},
		{"needs the forecast duty", allocateBody("[" + period + "]"), `"0"`, dutyRead, http.StatusForbidden, ""},
		{"stale model version", allocateBody("[" + period + "]"), `"3"`, dutyForecasts, http.StatusPreconditionFailed, ""},
		{"no periods", allocateBody("[]"), `"0"`, dutyForecasts, http.StatusBadRequest, "periods"},
		{"negative quantity", allocateBody(`[{"periodStart":"2026-12-01","periodEnd":"2026-12-31","qty":"-1"}]`), `"0"`, dutyForecasts, http.StatusBadRequest, "periods[0].qty"},
		{"seven decimals", allocateBody(`[{"periodStart":"2026-12-01","periodEnd":"2026-12-31","qty":"1.0000001"}]`), `"0"`, dutyForecasts, http.StatusBadRequest, "periods[0].qty"},
		{"over the largest quantity", allocateBody(`[{"periodStart":"2026-12-01","periodEnd":"2026-12-31","qty":"1000000000000"}]`), `"0"`, dutyForecasts, http.StatusBadRequest, "periods[0].qty"},
		{"end before start", allocateBody(`[{"periodStart":"2026-12-31","periodEnd":"2026-12-01","qty":"1"}]`), `"0"`, dutyForecasts, http.StatusBadRequest, "periods[0].periodEnd"},
		{"repeated period", allocateBody("[" + period + "," + period + "]"), `"0"`, dutyForecasts, http.StatusBadRequest, "periods[1].periodStart"},
		{"bad site", `{"keyCode":"FAM-A","siteId":"x","periods":[` + period + `]}`, `"0"`, dutyForecasts, http.StatusBadRequest, "siteId"},
		{"bad key code", `{"keyCode":"a","siteId":"` + siteID + `","periods":[` + period + `]}`, `"0"`, dutyForecasts, http.StatusBadRequest, "keyCode"},
		{"unknown key", `{"keyCode":"NOPE","siteId":"` + siteID + `","periods":[` + period + `]}`, `"0"`, dutyForecasts, http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doIfMatch(t, st, http.MethodPost, allocatePath, tc.body, tc.ifMatch, tc.duties)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.field != "" {
				if _, ok := problemOf(t, rec).Errors[tc.field]; !ok {
					t.Fatalf("problem does not name %s: %s", tc.field, rec.Body.String())
				}
			}
		})
	}
}
