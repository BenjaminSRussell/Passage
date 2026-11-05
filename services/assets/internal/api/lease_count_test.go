package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// Slice 3 fake store methods. They record the input so tests check what the
// handler passed on.
var (
	lastModify      store.LeaseModificationInput
	lastCount       store.CountSessionInput
	lastCountFilter store.CountFilter
	lastReport      store.InsuranceReportFilter
)

func (f *fakeStore) ModifyLease(_ context.Context, id string, in store.LeaseModificationInput, _ string) (store.LeaseModificationResult, error) {
	lastModify = in
	l, ok := f.leases[id]
	if !ok {
		return store.LeaseModificationResult{}, httpx.NewProblem(http.StatusNotFound, store.CodeLeaseNotFound, "no lease")
	}
	if l.Version != in.Version {
		return store.LeaseModificationResult{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	if err := domain.CanModify(l.Status); err != nil {
		return store.LeaseModificationResult{}, err
	}
	l.Status, l.Version, l.PaymentAmount = "Modified", l.Version+1, in.PaymentAmount
	f.leases[id] = l
	return store.LeaseModificationResult{Modification: store.LeaseModification{ID: ids.New(), LeaseID: id, EffectivePeriod: in.EffectivePeriod},
		Lease: l, Messages: []string{domain.CodeLeaseRemeasured + ": lease remeasured"}}, nil
}

func (f *fakeStore) CreateCountSession(_ context.Context, in store.CountSessionInput, _ string) (store.CountSession, error) {
	lastCount = in
	return store.CountSession{ID: ids.New(), LegalEntityID: in.LegalEntityID, Location: in.Location, CountedDate: in.CountedDate,
		UnknownCount: len(in.Barcodes), Lines: []store.CountLine{}, Warnings: []string{}}, nil
}

func (f *fakeStore) ListCountSessions(_ context.Context, filter store.CountFilter) (store.CountPage, error) {
	lastCountFilter = filter
	return store.CountPage{Items: []store.CountSession{}}, nil
}

func (f *fakeStore) GetCountSession(_ context.Context, id string) (store.CountSession, error) {
	return store.CountSession{}, httpx.NewProblem(http.StatusNotFound, store.CodeCountSessionNotFound, "no count session "+id)
}

func (f *fakeStore) InsuranceReport(_ context.Context, filter store.InsuranceReportFilter) (store.InsuranceReport, error) {
	lastReport = filter
	return store.InsuranceReport{AsOf: filter.AsOf, Items: []store.InsuranceReportRow{}}, nil
}

func newLease(t *testing.T, h http.Handler) store.Lease {
	t.Helper()
	body := `{"legalEntityId":"` + testLE + `","leaseNumber":"L-9","commencementDate":"2026-01-01","termMonths":60,
		"paymentsPerYear":1,"paymentAmount":"10000","paymentTiming":"arrears","ibrRate":"0.05","currency":"USD"}`
	rec := call(h, http.MethodPost, "/v1/leases", body, "assets.lease.maintain", true)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create lease status = %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	var l store.Lease
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestModifyLease_PreconditionsValidationAndHappyPath(t *testing.T) {
	h := server(newFake())
	lease := newLease(t, h)
	path := "/v1/leases/" + lease.ID + "/modify"
	body := `{"effectivePeriod":3,"paymentAmount":"12000"}`
	modify := func(body, etag, duties string, key bool) *httptest.ResponseRecorder {
		headers := map[string]string{}
		if etag != "" {
			headers["If-Match"] = etag
		}
		return do(h, req{method: http.MethodPost, path: path, body: body, duties: duties, key: key, headers: headers})
	}
	if rec := modify(body, `"1"`, "assets.asset.view", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without assets.lease.maintain status = %d", rec.Code)
	}
	if rec := modify(body, `"1"`, "assets.lease.maintain", false); rec.Code != http.StatusBadRequest {
		t.Fatalf("without Idempotency-Key status = %d", rec.Code)
	}
	if rec := modify(body, "", "assets.lease.maintain", true); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("without If-Match status = %d", rec.Code)
	}
	cases := map[string]struct{ body, field string }{
		"period zero":               {`{"effectivePeriod":0,"paymentAmount":"12000"}`, "effectivePeriod"},
		"negative payment":          {`{"effectivePeriod":3,"paymentAmount":"-1"}`, "paymentAmount"},
		"term without scope change": {`{"effectivePeriod":3,"paymentAmount":"12000","termMonths":84}`, "termMonths"},
		"term beyond 50 years":      {`{"effectivePeriod":3,"paymentAmount":"12000","termMonths":601,"scopeChange":true,"ibrRate":"0.06"}`, "termMonths"},
		"scope change without rate": {`{"effectivePeriod":3,"paymentAmount":"12000","scopeChange":true}`, "ibrRate"},
		"rate without scope change": {`{"effectivePeriod":3,"paymentAmount":"12000","ibrRate":"0.06"}`, "ibrRate"},
		"rate not a decimal":        {`{"effectivePeriod":3,"paymentAmount":"12000","scopeChange":true,"ibrRate":"six"}`, "ibrRate"},
	}
	for name, tc := range cases {
		rec := modify(tc.body, `"1"`, "assets.lease.maintain", true)
		if rec.Code != http.StatusBadRequest || problem(t, rec).Errors[tc.field] == "" {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := modify(body, `"7"`, "assets.lease.maintain", true); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match status = %d", rec.Code)
	}
	rec := modify(`{"effectivePeriod":4,"paymentAmount":"12000","termMonths":84,"scopeChange":true,"ibrRate":"0.06"}`, `"1"`, "assets.lease.maintain", true)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("modify status = %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if lastModify.Version != 1 || lastModify.EffectivePeriod != 4 || lastModify.TermMonths != 84 || !lastModify.ScopeChange ||
		lastModify.IBRRate == nil || lastModify.IBRRate.String() != "0.06" {
		t.Fatalf("store received %+v", lastModify)
	}
	if !strings.Contains(rec.Body.String(), `"messages":["AST-0516`) || !strings.Contains(rec.Body.String(), `"status":"Modified"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if get := call(h, http.MethodGet, "/v1/leases/"+lease.ID, "", "assets.lease.maintain", false); get.Header().Get("ETag") != `"2"` {
		t.Fatalf("get lease etag = %q", get.Header().Get("ETag"))
	}
	if rec := do(h, req{method: http.MethodPost, path: "/v1/leases/x/modify", body: body, duties: "assets.lease.maintain", key: true,
		headers: map[string]string{"If-Match": `"1"`}}); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed lease id status = %d", rec.Code)
	}
}

func TestCreateLease_RateMethod(t *testing.T) {
	f := newFake()
	h := server(f)
	body := `{"legalEntityId":"` + testLE + `","leaseNumber":"L-E","commencementDate":"2026-01-01","termMonths":12,
		"paymentsPerYear":12,"paymentAmount":"1000","paymentTiming":"arrears","ibrRate":"0.05","currency":"USD","rateMethod":"Effective"}`
	rec := call(h, http.MethodPost, "/v1/leases", body, "assets.lease.maintain", true)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"rateMethod":"Effective"`) {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	nominal := call(h, http.MethodPost, "/v1/leases", strings.Replace(body, `"Effective"`, `"Nominal"`, 1), "assets.lease.maintain", true)
	var eff, nom store.Lease
	_ = json.Unmarshal(rec.Body.Bytes(), &eff)
	_ = json.Unmarshal(nominal.Body.Bytes(), &nom)
	if !eff.Liability.GreaterThan(nom.Liability) {
		t.Fatalf("effective liability %s should exceed nominal %s", eff.Liability, nom.Liability)
	}
	bad := call(h, http.MethodPost, "/v1/leases", strings.Replace(body, `"Effective"`, `"Simple"`, 1), "assets.lease.maintain", true)
	if bad.Code != http.StatusBadRequest || problem(t, bad).Errors["rateMethod"] == "" {
		t.Fatalf("unknown rate method: status %d body %s", bad.Code, bad.Body.String())
	}
}

func TestCountSession_ValidationScopeAndDuties(t *testing.T) {
	h := server(newFake())
	le := testLE
	body := `{"legalEntityId":"` + le + `","location":" Warehouse 1 ","countedDate":"2026-04-10","barcodes":[" BC-1 ","BC-2","BC-1"]}`
	if rec := call(h, http.MethodPost, "/v1/assets/count-sessions", body, "assets.asset.view", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without assets.asset.count status = %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/assets/count-sessions", body, "assets.asset.count", false); rec.Code != http.StatusBadRequest {
		t.Fatalf("without Idempotency-Key status = %d", rec.Code)
	}
	rec := call(h, http.MethodPost, "/v1/assets/count-sessions", body, "assets.asset.count", true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if lastCount.Location != "Warehouse 1" || strings.Join(lastCount.Barcodes, ",") != "BC-1,BC-2,BC-1" || lastCount.CountedDate.String() != "2026-04-10" {
		t.Fatalf("store received %+v (trimmed, duplicates kept for the reconciliation)", lastCount)
	}
	many := make([]string, 5001)
	for i := range many {
		many[i] = `"B"`
	}
	cases := map[string]struct{ body, field string }{
		"legal entity not uuid": {strings.Replace(body, le, "x", 1), "legalEntityId"},
		"blank location":        {strings.Replace(body, `" Warehouse 1 "`, `"  "`, 1), "location"},
		"bad date":              {strings.Replace(body, `"2026-04-10"`, `"April"`, 1), "countedDate"},
		"blank barcode":         {strings.Replace(body, `"BC-2"`, `" "`, 1), "barcodes"},
		"no barcodes field":     {`{"legalEntityId":"` + le + `","location":"W"}`, "barcodes"},
		"too many scans":        {`{"legalEntityId":"` + le + `","location":"W","barcodes":[` + strings.Join(many, ",") + `]}`, "barcodes"},
	}
	for name, tc := range cases {
		rec := call(h, http.MethodPost, "/v1/assets/count-sessions", tc.body, "assets.asset.count", true)
		if rec.Code != http.StatusBadRequest || problem(t, rec).Errors[tc.field] == "" {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
	}
	other := do(h, req{method: http.MethodPost, path: "/v1/assets/count-sessions", body: body, duties: "assets.asset.count", key: true,
		headers: map[string]string{"X-Passage-Legal-Entity": ids.New()}})
	if other.Code != http.StatusForbidden {
		t.Fatalf("count outside the caller's legal entity status = %d", other.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/count-sessions?legalEntityId="+le+"&limit=10", "", "assets.asset.count", false); rec.Code != http.StatusOK ||
		lastCountFilter.LegalEntityID != le || lastCountFilter.Limit != 10 {
		t.Fatalf("list status = %d filter %+v", rec.Code, lastCountFilter)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/count-sessions?legalEntityId=x", "", "assets.asset.view", false); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad filter status = %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/count-sessions", "", "assets.lease.maintain", false); rec.Code != http.StatusForbidden {
		t.Fatalf("list without assets.asset.view or assets.asset.count status = %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/count-sessions/"+ids.New(), "", "assets.asset.view", false); rec.Code != http.StatusNotFound ||
		problem(t, rec).Code != store.CodeCountSessionNotFound {
		t.Fatalf("unknown session status = %d", rec.Code)
	}
}

func TestInsuranceReport_FiltersAndDefaults(t *testing.T) {
	h := server(newFake())
	le := testLE
	if rec := call(h, http.MethodGet, "/v1/assets/insurance-report?legalEntityId="+le, "assets.lease.maintain", "assets.lease.maintain", false); rec.Code != http.StatusForbidden {
		t.Fatalf("without assets.asset.view status = %d", rec.Code)
	}
	rec := call(h, http.MethodGet, "/v1/assets/insurance-report?legalEntityId="+le+"&asOf=2026-04-20&insuranceType=Fleet&expiringWithinDays=30", "", "assets.insurance.maintain", false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"asOf":"2026-04-20"`) {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if lastReport.LegalEntityID != le || lastReport.InsuranceType != "Fleet" || lastReport.ExpiringWithinDays == nil || *lastReport.ExpiringWithinDays != 30 ||
		lastReport.Limit != 50 {
		t.Fatalf("store received %+v", lastReport)
	}
	if rec := call(h, http.MethodGet, "/v1/assets/insurance-report?legalEntityId="+le, "", "assets.asset.view", false); rec.Code != http.StatusOK || lastReport.ExpiringWithinDays != nil ||
		lastReport.AsOf.IsZero() {
		t.Fatalf("defaults: status %d filter %+v", rec.Code, lastReport)
	}
	cases := map[string]string{
		"legalEntityId":      "/v1/assets/insurance-report",
		"asOf":               "/v1/assets/insurance-report?legalEntityId=" + le + "&asOf=today",
		"insuranceType":      "/v1/assets/insurance-report?legalEntityId=" + le + "&insuranceType=Flood",
		"expiringWithinDays": "/v1/assets/insurance-report?legalEntityId=" + le + "&expiringWithinDays=400",
	}
	for field, path := range cases {
		rec := call(h, http.MethodGet, path, "", "assets.asset.view", false)
		if rec.Code != http.StatusBadRequest || problem(t, rec).Errors[field] == "" {
			t.Errorf("%s: status %d body %s", field, rec.Code, rec.Body.String())
		}
	}
}
