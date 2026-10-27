package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

func (f *fakeStore) UpsertIndexSeries(_ context.Context, in store.IndexSeries) (store.IndexSeries, error) {
	f.series[in.Code] = in
	return in, nil
}

func (f *fakeStore) ListIndexSeries(context.Context) ([]store.IndexSeries, error) {
	out := []store.IndexSeries{}
	for _, s := range f.series {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeStore) AddInsurancePolicy(_ context.Context, assetID string, in store.InsurancePolicy, _ string) (store.InsurancePolicy, error) {
	f.lastPolicy = in
	if _, ok := f.assets[assetID]; !ok {
		return store.InsurancePolicy{}, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset")
	}
	in.ID, in.AssetID, in.Warnings = ids.New(), assetID, []string{}
	return in, nil
}

func (f *fakeStore) ListInsurancePolicies(_ context.Context, assetID string) ([]store.InsurancePolicy, error) {
	if _, ok := f.assets[assetID]; !ok {
		return nil, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset")
	}
	return []store.InsurancePolicy{}, nil
}

func (f *fakeStore) CreateCapexRequest(_ context.Context, in store.CapexRequest, _ string) (store.CapexRequest, error) {
	in.ID, in.Status, in.Version = ids.New(), string(domain.CapexDraft), 1
	for i := range in.Variants {
		in.Variants[i].ID = ids.New()
		for _, a := range in.Variants[i].AmountsByYear {
			in.Variants[i].TotalAmount = in.Variants[i].TotalAmount.Add(a)
		}
	}
	f.capex[in.ID] = in
	return in, nil
}

func (f *fakeStore) ListCapexRequests(_ context.Context, _ store.CapexFilter) (store.CapexPage, error) {
	page := store.CapexPage{Items: []store.CapexRequest{}}
	for _, c := range f.capex {
		c.Variants = nil
		page.Items = append(page.Items, c)
	}
	return page, nil
}

func (f *fakeStore) GetCapexRequest(_ context.Context, id string) (store.CapexRequest, error) {
	c, ok := f.capex[id]
	if !ok {
		return store.CapexRequest{}, httpx.NewProblem(http.StatusNotFound, store.CodeCapexNotFound, "no capex request")
	}
	return c, nil
}

func (f *fakeStore) SubmitCapexRequest(_ context.Context, id string, in store.SubmitInput) (store.CapexRequest, error) {
	f.lastSubmit = in
	c, ok := f.capex[id]
	if !ok {
		return store.CapexRequest{}, httpx.NewProblem(http.StatusNotFound, store.CodeCapexNotFound, "no capex request")
	}
	if c.Version != in.Version {
		return store.CapexRequest{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	if err := domain.TransitionCapex(domain.CapexStatus(c.Status), domain.CapexSubmitted); err != nil {
		return store.CapexRequest{}, err
	}
	for _, v := range c.Variants {
		if v.ID == in.VariantID {
			level := domain.ApprovalLevel(v.TotalAmount)
			c.Status, c.ApprovalLevel, c.RequestedAmount, c.SelectedVariantID, c.Version = string(domain.CapexInApproval), &level, &v.TotalAmount, &v.ID, c.Version+1
			f.capex[id] = c
			return c, nil
		}
	}
	return store.CapexRequest{}, httpx.NewProblem(http.StatusNotFound, store.CodeCapexVariantNotFound, "no variant")
}

const insuranceDuties = "AssetView,AssetInsuranceMaintain"

func TestIndexSeries_PutValidatesAndLists(t *testing.T) {
	f := newFake()
	h := server(f)
	body := `{"country":"US","classCode":"MACHINE","values":[{"year":2024,"value":"112.0"},{"year":2026,"value":"118.5"}]}`
	if rec := call(h, http.MethodPut, "/v1/assets/index-series/US-MACH", body, "AssetView", false); rec.Code != http.StatusForbidden {
		t.Fatalf("without AssetInsuranceMaintain status = %d", rec.Code)
	}
	rec := call(h, http.MethodPut, "/v1/assets/index-series/US-MACH", body, insuranceDuties, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if got := f.series["US-MACH"]; got.Country != "US" || len(got.Values) != 2 || !got.Values[1].Value.Equal(decimal.RequireFromString("118.5")) {
		t.Errorf("store received %+v", got)
	}
	cases := map[string]struct{ path, body, field string }{
		"lower-case code": {"/v1/assets/index-series/us", body, "code"},
		"bad country":     {"/v1/assets/index-series/X", `{"country":"USA","classCode":"M","values":[{"year":2024,"value":"1"}]}`, "country"},
		"no values":       {"/v1/assets/index-series/X", `{"country":"US","classCode":"M","values":[]}`, "values"},
		"duplicate year":  {"/v1/assets/index-series/X", `{"country":"US","classCode":"M","values":[{"year":2024,"value":"1"},{"year":2024,"value":"2"}]}`, "values"},
		"zero value":      {"/v1/assets/index-series/X", `{"country":"US","classCode":"M","values":[{"year":2024,"value":"0"}]}`, "values"},
	}
	for name, tc := range cases {
		rec := call(h, http.MethodPut, tc.path, tc.body, insuranceDuties, false)
		if rec.Code != http.StatusBadRequest || problem(t, rec).Errors[tc.field] == "" {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := call(h, http.MethodGet, "/v1/assets/index-series", "", "AssetInsuranceMaintain", false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "US-MACH") {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
}

func TestInsurancePolicy_ValidationAndDefaults(t *testing.T) {
	f := newFake()
	h := server(f)
	a := createAsset(t, h)
	path := "/v1/assets/" + a.ID + "/insurance-policies"
	body := `{"insurerPartyId":"` + ids.New() + `","policyNumber":" P-1 ","insuranceType":"Property","coverageFrom":"2026-01-01","coverageTo":"2026-12-31",
		"currency":"USD","basis":"Replacement","baseValue":"100000.00","sumInsured":"95000.00","indexSeriesCode":"US-MACH","indexBaseYear":2024}`
	if rec := call(h, http.MethodPost, path, body, "AssetView", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without AssetInsuranceMaintain status = %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, path, body, insuranceDuties, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("without Idempotency-Key status = %d", rec.Code)
	}
	rec := call(h, http.MethodPost, path, body, insuranceDuties, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	got := f.lastPolicy
	if got.PolicyNumber != "P-1" || got.ValuationYear == nil || *got.ValuationYear != 2026 || !got.Premium.IsZero() || got.CoverageTo.String() != "2026-12-31" {
		t.Errorf("store received %+v (valuation year defaults to the coverage start year)", got)
	}

	cases := map[string]struct{ body, field string }{
		"coverage ends before it starts": {strings.Replace(body, `"2026-12-31"`, `"2025-12-31"`, 1), "coverageTo"},
		"index without base year":        {strings.Replace(body, `,"indexBaseYear":2024`, ``, 1), "indexBaseYear"},
		"base year without series":       {strings.Replace(body, `"indexSeriesCode":"US-MACH",`, ``, 1), "indexSeriesCode"},
		"unknown insurance type":         {strings.Replace(body, `"Property"`, `"Flood"`, 1), "insuranceType"},
		"sum insured beyond minor unit":  {strings.Replace(body, `"95000.00"`, `"95000.001"`, 1), "sumInsured"},
		"negative base value":            {strings.Replace(body, `"100000.00"`, `"-1"`, 1), "baseValue"},
	}
	for name, tc := range cases {
		rec := call(h, http.MethodPost, path, tc.body, insuranceDuties, true)
		if rec.Code != http.StatusBadRequest || problem(t, rec).Errors[tc.field] == "" {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := call(h, http.MethodPost, "/v1/assets/"+ids.New()+"/insurance-policies", body, insuranceDuties, true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown asset status = %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, path, "", "AssetView", false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("list = %d %s", rec.Code, rec.Body.String())
	}
}

const capexDuties = "CapexRequestCreate"

func TestCapex_CreateSubmitRoutesToCFO(t *testing.T) {
	// AC-37.11: a USD 320,000.00 request routes to the CFO level on submit.
	f := newFake()
	h := server(f)
	body := `{"legalEntityId":"` + ids.New() + `","number":"CR-1","title":"New press line","requesterId":"plant.manager","currency":"USD",
		"variants":[{"description":"Base","amountsByYear":{"2026":"200000.00","2027":"120000.00"},"inServiceDate":"2027-06-01","profileAssumption":"SL-120"}]}`
	if rec := call(h, http.MethodPost, "/v1/assets/capex-requests", body, "AssetView", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without CapexRequestCreate status = %d", rec.Code)
	}
	rec := call(h, http.MethodPost, "/v1/assets/capex-requests", body, capexDuties, true)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create status = %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	var created store.CapexRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != "Draft" || len(created.Variants) != 1 || created.Variants[0].TotalAmount.StringFixed(2) != "320000.00" {
		t.Fatalf("created = %+v", created)
	}

	cases := map[string]struct{ body, field string }{
		"no variants":           {`{"legalEntityId":"` + ids.New() + `","number":"CR-2","title":"T","requesterId":"r","currency":"USD","variants":[]}`, "variants"},
		"year key not a year":   {strings.Replace(body, `"2027":`, `"next":`, 1), "variants[0].amountsByYear"},
		"zero amount":           {strings.Replace(body, `"120000.00"`, `"0"`, 1), "variants[0].amountsByYear"},
		"bad in-service date":   {strings.Replace(body, `"2027-06-01"`, `"June"`, 1), "variants[0].inServiceDate"},
		"missing profile":       {strings.Replace(body, `"SL-120"`, `""`, 1), "variants[0].profileAssumption"},
		"lower-case currency":   {strings.Replace(body, `"USD"`, `"usd"`, 1), "currency"},
		"empty requester":       {strings.Replace(body, `"plant.manager"`, `" "`, 1), "requesterId"},
		"amount beyond minor":   {strings.Replace(body, `"200000.00"`, `"200000.005"`, 1), "variants[0].amountsByYear"},
		"legal entity not uuid": {strings.Replace(body, created.LegalEntityID, "x", 1), "legalEntityId"},
	}
	for name, tc := range cases {
		rec := call(h, http.MethodPost, "/v1/assets/capex-requests", tc.body, capexDuties, true)
		if rec.Code != http.StatusBadRequest || problem(t, rec).Errors[tc.field] == "" {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
	}

	submitPath := "/v1/assets/capex-requests/" + created.ID + "/submit"
	submitBody := `{"variantId":"` + created.Variants[0].ID + `"}`
	submit := func(etag string) *http.Response {
		headers := map[string]string{}
		if etag != "" {
			headers["If-Match"] = etag
		}
		return do(h, req{method: http.MethodPost, path: submitPath, body: submitBody, duties: capexDuties, headers: headers}).Result()
	}
	if res := submit(""); res.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("submit without If-Match status = %d", res.StatusCode)
	}
	if res := submit(`"4"`); res.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("submit with stale If-Match status = %d", res.StatusCode)
	}
	sub := do(h, req{method: http.MethodPost, path: submitPath, body: submitBody, duties: capexDuties, headers: map[string]string{"If-Match": `"1"`}})
	if sub.Code != http.StatusOK || sub.Header().Get("ETag") != `"2"` {
		t.Fatalf("submit status = %d etag %q body %s", sub.Code, sub.Header().Get("ETag"), sub.Body.String())
	}
	if !strings.Contains(sub.Body.String(), `"approvalLevel":"CFO"`) || !strings.Contains(sub.Body.String(), `"status":"InApproval"`) {
		t.Fatalf("submitted = %s", sub.Body.String())
	}
	again := do(h, req{method: http.MethodPost, path: submitPath, body: submitBody, duties: capexDuties, headers: map[string]string{"If-Match": `"2"`}})
	if again.Code != http.StatusUnprocessableEntity || problem(t, again).Code != domain.CodeCapexState {
		t.Fatalf("second submit status = %d body %s", again.Code, again.Body.String())
	}
	if rec := do(h, req{method: http.MethodPost, path: submitPath, body: `{"variantId":"x"}`, duties: capexDuties, headers: map[string]string{"If-Match": `"2"`}}); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed variant id status = %d", rec.Code)
	}

	if rec := call(h, http.MethodGet, "/v1/assets/capex-requests/"+created.ID, "", capexDuties, false); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get status = %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := call(h, http.MethodGet, "/v1/assets/capex-requests?status=InApproval", "", "AssetView", false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "CR-1") {
		t.Fatalf("list status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, http.MethodGet, "/v1/assets/capex-requests?status=Done", "", "AssetView", false); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown status filter = %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/capex-requests/"+ids.New(), "", capexDuties, false); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown request status = %d", rec.Code)
	}
}
