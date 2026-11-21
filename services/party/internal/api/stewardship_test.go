package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/party/internal/api"
	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
	"github.com/BenjaminSRussell/depot/services/party/internal/store"
)

type fakeSteward struct {
	status   string
	resolved store.ResolveStewardTask
	address  domain.NewElectronicAddress
	err      error
}

func (f *fakeSteward) ListStewardTasks(_ context.Context, status string) ([]store.StewardTask, error) {
	f.status = status
	return []store.StewardTask{{ID: knownID, Status: status}}, f.err
}

func (f *fakeSteward) ResolveStewardTask(_ context.Context, id string, in store.ResolveStewardTask) (store.StewardTask, error) {
	f.resolved = in
	return store.StewardTask{ID: id, Status: store.StewardResolved, Resolution: in.Resolution}, f.err
}

func (f *fakeSteward) AddElectronicAddress(_ context.Context, partyID string, in domain.NewElectronicAddress) (domain.ElectronicAddress, error) {
	f.address = in
	return domain.ElectronicAddress{ID: knownID, PartyID: partyID, Kind: in.Kind, Value: in.Value}, f.err
}

func (f *fakeSteward) ListElectronicAddresses(_ context.Context, partyID string) ([]domain.ElectronicAddress, error) {
	return []domain.ElectronicAddress{{ID: knownID, PartyID: partyID, Kind: "email", Value: "a@b.example"}}, f.err
}

func stewardAPI(v *fakeVendors, s *fakeSteward) (http.Handler, *api.API) {
	r := httpx.NewRouter()
	a := api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithVendors(v).WithStewardship(s)
	a.Register(r)
	return r, a
}

func stewardRouter(v *fakeVendors, s *fakeSteward) http.Handler {
	r, _ := stewardAPI(v, s)
	return r
}

func doHeader(r http.Handler, method, path, body, duties, header, value string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Depot-Duties", duties)
	if header != "" {
		req.Header.Set(header, value)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestVendorDuplicate_RoutesCandidatesAndOverride(t *testing.T) {
	vendors := &fakeVendors{}
	r, a := stewardAPI(vendors, &fakeSteward{})
	path, body := "/v1/party/parties/"+knownID+"/vendor", `{"accountNumber":"V-1","vendorGroupId":"`+vendorGroup+`","currencyCode":"USD"}`

	vendors.err = &store.DuplicateError{
		Problem:    httpx.NewProblem(http.StatusConflict, store.CodeDupLikely, "similar vendor"),
		Candidates: []store.Candidate{{PartyID: knownID, PartyNumber: "P-1", Name: "Acme", Weight: 11.9, Class: "possible"}},
	}
	rec := do(r, http.MethodPost, path, body, "party.write", true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"PTY-DUP-LIKELY"`) || !strings.Contains(rec.Body.String(), `"candidates":[{"partyId":"`+knownID+`"`) || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("duplicate: %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if strings.Contains(rec.Body.String(), "@") {
		t.Fatalf("candidates must not carry addresses: %s", rec.Body)
	}

	vendors.err = nil
	rec = doHeader(r, http.MethodPost, path, body, "party.write", "X-Duplicate-Override-Reason", "  separate subsidiary ")
	if rec.Code != http.StatusCreated || vendors.reason != "separate subsidiary" {
		t.Fatalf("override: %d %s reason=%q", rec.Code, rec.Body, vendors.reason)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `party_duplicate_checks_total{outcome="blocked"} 1`) || !strings.Contains(metrics.Body.String(), `party_duplicate_checks_total{outcome="overridden"} 1`) {
		t.Fatalf("metrics: %s", metrics.Body)
	}
}

func TestVendorDuplicate_BlankOrOverlongOverrideReasonIsRefused(t *testing.T) {
	vendors := &fakeVendors{}
	r := stewardRouter(vendors, &fakeSteward{})
	path, body := "/v1/party/parties/"+knownID+"/vendor", `{"accountNumber":"V-1","vendorGroupId":"`+vendorGroup+`","currencyCode":"USD"}`
	for _, reason := range []string{"", "   ", strings.Repeat("x", store.MaxReasonLen+1)} {
		rec := doHeader(r, http.MethodPost, path, body, "party.write", "X-Duplicate-Override-Reason", reason)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"PTY-STEWARD-INVALID"`) || vendors.partyID != "" {
			t.Fatalf("reason %q: %d %s created for %q", reason, rec.Code, rec.Body, vendors.partyID)
		}
	}
}

func TestStewardshipRoutes_HappyPaths(t *testing.T) {
	steward := &fakeSteward{}
	r := stewardRouter(&fakeVendors{}, steward)
	rec := do(r, http.MethodGet, "/v1/party/steward-tasks", "", "party.read", true)
	if rec.Code != http.StatusOK || steward.status != store.StewardOpen || !strings.Contains(rec.Body.String(), `"stewardTasks":[{`) {
		t.Fatalf("list: %d %s status=%q", rec.Code, rec.Body, steward.status)
	}
	rec = do(r, http.MethodPost, "/v1/party/steward-tasks/"+knownID+":resolve", `{"resolution":" distinct ","reason":" different entity "}`, "party.write", true)
	if rec.Code != http.StatusOK || steward.resolved.Resolution != "distinct" || steward.resolved.Reason != "different entity" {
		t.Fatalf("resolve: %d %s %+v", rec.Code, rec.Body, steward.resolved)
	}
	rec = do(r, http.MethodPost, "/v1/party/parties/"+knownID+"/electronic-addresses", `{"kind":"EMAIL","value":" Info@Example.com "}`, "party.write", true)
	if rec.Code != http.StatusCreated || steward.address.Value != "info@example.com" || steward.address.Kind != "email" {
		t.Fatalf("add address: %d %s %+v", rec.Code, rec.Body, steward.address)
	}
	rec = do(r, http.MethodGet, "/v1/party/parties/"+knownID+"/electronic-addresses", "", "party.read", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"electronicAddresses":[{`) {
		t.Fatalf("list addresses: %d %s", rec.Code, rec.Body)
	}
}

func TestStewardshipRoutes_RefusalsAndValidation(t *testing.T) {
	r := stewardRouter(&fakeVendors{}, &fakeSteward{})
	taskPath, addrPath := "/v1/party/steward-tasks/"+knownID+":resolve", "/v1/party/parties/"+knownID+"/electronic-addresses"
	for _, tc := range []struct {
		name, method, path, body, duties string
		tenant                           bool
		want                             int
		wantBody                         string
	}{
		{"list without tenant", http.MethodGet, "/v1/party/steward-tasks", "", "", false, http.StatusUnauthorized, ""},
		{"list needs party.read", http.MethodGet, "/v1/party/steward-tasks", "", "party.write", true, http.StatusForbidden, ""},
		{"list unknown status", http.MethodGet, "/v1/party/steward-tasks?status=done", "", "party.read", true, http.StatusUnprocessableEntity, `"status"`},
		{"resolve needs party.write", http.MethodPost, taskPath, `{"resolution":"distinct","reason":"r"}`, "party.read", true, http.StatusForbidden, ""},
		{"resolve without tenant", http.MethodPost, taskPath, `{"resolution":"distinct","reason":"r"}`, "", false, http.StatusUnauthorized, ""},
		{"resolve other resolution", http.MethodPost, taskPath, `{"resolution":"merge","reason":"r"}`, "party.write", true, http.StatusUnprocessableEntity, `"resolution"`},
		{"resolve empty reason", http.MethodPost, taskPath, `{"resolution":"distinct","reason":" "}`, "party.write", true, http.StatusUnprocessableEntity, `"reason"`},
		{"resolve number for string", http.MethodPost, taskPath, `{"resolution":1,"reason":"r"}`, "party.write", true, http.StatusBadRequest, ""},
		{"address needs party.write", http.MethodPost, addrPath, `{"kind":"email","value":"a@b.example"}`, "party.read", true, http.StatusForbidden, ""},
		{"address list needs party.read", http.MethodGet, addrPath, "", "party.write", true, http.StatusForbidden, ""},
		{"address unknown kind", http.MethodPost, addrPath, `{"kind":"fax","value":"1"}`, "party.write", true, http.StatusUnprocessableEntity, `"kind"`},
		{"address invalid email", http.MethodPost, addrPath, `{"kind":"email","value":"nope"}`, "party.write", true, http.StatusUnprocessableEntity, `PTY-ELECTRONIC-INVALID`},
		{"address number for string", http.MethodPost, addrPath, `{"kind":"email","value":5}`, "party.write", true, http.StatusBadRequest, ""},
	} {
		rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.tenant)
		if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("%s: %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.want, tc.wantBody)
		}
	}
}
