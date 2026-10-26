package api_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

const (
	addressesPath = "/v1/org/addresses"
	addressDuties = "org.read,org.address.write"
	ownerUUID     = "00000000-0000-0000-0004-000000000001"
)

type stubGeocoder struct {
	result domain.Geocode
	err    error
}

func (g stubGeocoder) Geocode(context.Context, domain.PostalAddress) (domain.Geocode, error) {
	return g.result, g.err
}

func addressBody(overrides ...string) string {
	fields := map[string]string{
		"ownerType": `"warehouse"`, "ownerId": `"` + ownerUUID + `"`, "countryCode": `"us"`, "regionCode": `"ca"`, "city": `"San Francisco"`,
		"postalCode": `"94105"`, "street1": `"1 Main St"`, "purposes": `[{"purpose":"business","primary":true}]`,
	}
	for i := 0; i+1 < len(overrides); i += 2 {
		if overrides[i+1] == "" {
			delete(fields, overrides[i])
		} else {
			fields[overrides[i]] = overrides[i+1]
		}
	}
	parts := make([]string, 0, len(fields))
	for k, v := range fields {
		parts = append(parts, `"`+k+`":`+v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestAddresses(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"create upper-cases codes and stays unverified", addressBody(), addressDuties, true, http.StatusCreated, `"countryCode":"US","regionCode":"CA"`},
		{"create geoStatus unverified", addressBody(), addressDuties, true, http.StatusCreated, `"geoStatus":"unverified"`},
		{"unknown ownerType", addressBody("ownerType", `"planet"`), addressDuties, true, http.StatusBadRequest, `"ownerType"`},
		{"malformed ownerId", addressBody("ownerId", `"nope"`), addressDuties, true, http.StatusBadRequest, `"ownerId"`},
		{"missing street1", addressBody("street1", ""), addressDuties, true, http.StatusBadRequest, `"street1"`},
		{"61-character city", addressBody("city", `"`+strings.Repeat("c", 61)+`"`), addressDuties, true, http.StatusBadRequest, `"city"`},
		{"validFrom 2026-13-01", addressBody("validFrom", `"2026-13-01"`), addressDuties, true, http.StatusBadRequest, `"validFrom"`},
		{"empty purposes", addressBody("purposes", `[]`), addressDuties, true, http.StatusBadRequest, `"purposes"`},
		{"purpose billing", addressBody("purposes", `[{"purpose":"billing"}]`), addressDuties, true, http.StatusBadRequest, `"purposes"`},
		{"duplicated purpose", addressBody("purposes", `[{"purpose":"invoice"},{"purpose":"invoice"}]`), addressDuties, true, http.StatusBadRequest, `"purposes"`},
		{"bad regionCode", addressBody("regionCode", `"toolong"`), addressDuties, true, http.StatusBadRequest, `"regionCode"`},
		{"unknown body field", addressBody("surprise", `1`), addressDuties, true, http.StatusBadRequest, "COM-0001"},
		{"without duty", addressBody(), "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"without tenant", addressBody(), "", false, http.StatusUnauthorized, "AUTH-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			r, _ := newRouter(f)
			rec := do(r, http.MethodPost, addressesPath, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus != http.StatusCreated && len(f.addresses) != 0 {
				t.Fatalf("rejected request reached the store")
			}
		})
	}
}

func TestAddresses_Geocoder(t *testing.T) {
	tests := []struct {
		name       string
		geocoder   stubGeocoder
		wantStatus string
		wantBody   string
	}{
		{"found sets coordinates", stubGeocoder{result: domain.Geocode{Latitude: decimal.RequireFromString("37.789"), Longitude: decimal.RequireFromString("-122.394"), Found: true}}, "geocoded", `"latitude":"37.789","longitude":"-122.394"`},
		{"error stays unverified", stubGeocoder{err: errors.New("down")}, "unverified", `"geoStatus":"unverified"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, a := newRouter(&fakeStore{})
			a.SetGeocoder(tc.geocoder)
			rec := do(r, http.MethodPost, addressesPath, addressBody(), addressDuties, true)
			if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"geoStatus":"`+tc.wantStatus+`"`) || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s", tc.name, rec.Code, rec.Body)
			}
		})
	}
}

func TestAddresses_Get(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	created := do(r, http.MethodPost, addressesPath, addressBody(), addressDuties, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	for _, tc := range []struct {
		name, path string
		status     int
		body       string
	}{
		{"ok", addressesPath + "/00000000-0000-0000-0003-000000000001", http.StatusOK, `"ownerType":"warehouse"`},
		{"unknown", addressesPath + "/00000000-0000-0000-0003-0000000000ff", http.StatusNotFound, "ORG-ADDR-NOT-FOUND"},
		{"malformed", addressesPath + "/nope", http.StatusNotFound, "ORG-ADDR-NOT-FOUND"},
	} {
		before := f.addressCalls
		rec := do(r, http.MethodGet, tc.path, "", addressDuties, true)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.body) {
			t.Fatalf("%s = %d %s", tc.name, rec.Code, rec.Body)
		}
		if tc.name == "malformed" && f.addressCalls != before {
			t.Fatalf("malformed id reached the store")
		}
	}
	if rec := do(r, http.MethodGet, addressesPath+"/00000000-0000-0000-0003-000000000001", "", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("get without tenant = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, addressesPath+"/00000000-0000-0000-0003-000000000001", "", "org.site.write", true); rec.Code != http.StatusForbidden {
		t.Fatalf("get without org.read = %d", rec.Code)
	}
}

const (
	stubVersionID  = "00000000-0000-0000-0003-000000000001"
	staleVersionID = "00000000-0000-0000-0003-0000000000ee"
	firstAddress   = addressesPath + "/00000000-0000-0000-0003-000000000001"
)

func patchBody(versionID string, overrides ...string) string {
	body := `{"versionId":"` + versionID + `"`
	for i := 0; i+1 < len(overrides); i += 2 {
		body += `,"` + overrides[i] + `":` + overrides[i+1]
	}
	return body + "}"
}

func seededAddress(t *testing.T, geocoder stubGeocoder) (*fakeStore, http.Handler, func(string, string) *httptest.ResponseRecorder) {
	t.Helper()
	f := &fakeStore{}
	r, a := newRouter(f)
	a.SetGeocoder(geocoder)
	if rec := do(r, http.MethodPost, addressesPath, addressBody(), addressDuties, true); rec.Code != http.StatusCreated {
		t.Fatalf("seed = %d %s", rec.Code, rec.Body)
	}
	return f, r, func(path, body string) *httptest.ResponseRecorder {
		return do(r, http.MethodPatch, path, body, addressDuties, true)
	}
}

func TestAddresses_Patch(t *testing.T) {
	found := stubGeocoder{result: domain.Geocode{Latitude: decimal.RequireFromString("37.789"), Longitude: decimal.RequireFromString("-122.394"), Found: true}}
	f, _, patch := seededAddress(t, found)
	rec := patch(firstAddress, patchBody(stubVersionID, "city", `"Oakland"`))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"city":"Oakland"`) || strings.Contains(rec.Body.String(), `"versionId":"`+stubVersionID+`"`) || f.reviseCalls != 1 {
		t.Fatalf("patch city = %d %s, reviseCalls %d", rec.Code, rec.Body, f.reviseCalls)
	}
}

func TestAddresses_PatchPurposesOnlyKeepsGeocode(t *testing.T) {
	f := &fakeStore{}
	r, a := newRouter(f)
	a.SetGeocoder(stubGeocoder{result: domain.Geocode{Latitude: decimal.RequireFromString("37.789"), Longitude: decimal.RequireFromString("-122.394"), Found: true}})
	do(r, http.MethodPost, addressesPath, addressBody(), addressDuties, true)
	a.SetGeocoder(stubGeocoder{err: errors.New("must not be called")})
	rec := do(r, http.MethodPatch, firstAddress, patchBody(stubVersionID, "purposes", `[{"purpose":"business","primary":true},{"purpose":"invoice"}]`), addressDuties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"latitude":"37.789"`) || !strings.Contains(rec.Body.String(), `"geoStatus":"geocoded"`) || !strings.Contains(rec.Body.String(), `"purpose":"invoice"`) {
		t.Fatalf("purposes-only patch = %d %s", rec.Code, rec.Body)
	}
}

func TestAddresses_PatchGeocoderErrorStaysUnverified(t *testing.T) {
	_, _, patch := seededAddress(t, stubGeocoder{err: errors.New("down")})
	rec := patch(firstAddress, patchBody(stubVersionID, "postalCode", `"94106"`))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"geoStatus":"unverified"`) {
		t.Fatalf("patch with geocoder error = %d %s", rec.Code, rec.Body)
	}
}

func TestAddresses_PatchRejections(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		status           int
		want             string
		revises          int
	}{
		{"no-op", firstAddress, patchBody(stubVersionID, "city", `"San Francisco"`, "purposes", `[{"purpose":"business","primary":true}]`), http.StatusOK, `"city":"San Francisco"`, 0},
		{"stale versionId", firstAddress, patchBody(staleVersionID, "city", `"Oakland"`), http.StatusConflict, "ORG-VERSION-CONFLICT", 0},
		{"missing versionId", firstAddress, `{"city":"Oakland"}`, http.StatusBadRequest, `"versionId"`, 0},
		{"non-UUID versionId", firstAddress, patchBody("nope"), http.StatusBadRequest, `"versionId"`, 0},
		{"ownerId in body", firstAddress, patchBody(stubVersionID, "ownerId", `"`+ownerUUID+`"`), http.StatusBadRequest, "COM-0001", 0},
		{"empty city", firstAddress, patchBody(stubVersionID, "city", `""`), http.StatusBadRequest, `"city"`, 0},
		{"bad validFrom", firstAddress, patchBody(stubVersionID, "validFrom", `"2026-13-01"`), http.StatusBadRequest, `"validFrom"`, 0},
		{"empty purposes", firstAddress, patchBody(stubVersionID, "purposes", `[]`), http.StatusBadRequest, `"purposes"`, 0},
		{"unknown id", addressesPath + "/00000000-0000-0000-0003-0000000000ff", patchBody(stubVersionID, "city", `"Oakland"`), http.StatusNotFound, "ORG-ADDR-NOT-FOUND", 0},
		{"malformed id", addressesPath + "/nope", patchBody(stubVersionID, "city", `"Oakland"`), http.StatusNotFound, "ORG-ADDR-NOT-FOUND", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, patch := seededAddress(t, stubGeocoder{})
			rec := patch(tc.path, tc.body)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) || f.reviseCalls != tc.revises {
				t.Fatalf("%s = %d %s, reviseCalls %d; want %d containing %q", tc.name, rec.Code, rec.Body, f.reviseCalls, tc.status, tc.want)
			}
		})
	}
	r, _ := newRouter(&fakeStore{})
	if rec := do(r, http.MethodPatch, firstAddress, patchBody(stubVersionID), "org.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("patch without duty = %d", rec.Code)
	}
	if rec := do(r, http.MethodPatch, firstAddress, patchBody(stubVersionID), "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("patch without tenant = %d", rec.Code)
	}
}

func TestAddresses_VersionAndList(t *testing.T) {
	f, r, _ := seededAddress(t, stubGeocoder{})
	versions := "/v1/org/address-versions/"
	if rec := do(r, http.MethodGet, versions+stubVersionID, "", addressDuties, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"city":"San Francisco"`) {
		t.Fatalf("get version = %d %s", rec.Code, rec.Body)
	}
	calls := f.addressCalls
	if rec := do(r, http.MethodGet, versions+"nope", "", addressDuties, true); rec.Code != http.StatusNotFound || f.addressCalls != calls {
		t.Fatalf("malformed version id = %d, store calls %d -> %d", rec.Code, calls, f.addressCalls)
	}
	if rec := do(r, http.MethodGet, versions+stubVersionID, "", "org.site.write", true); rec.Code != http.StatusForbidden {
		t.Fatalf("version without org.read = %d", rec.Code)
	}
	owner := "?ownerType=warehouse&ownerId=" + ownerUUID
	for _, tc := range []struct {
		name, query string
		status      int
		want        string
	}{
		{"known owner", owner, http.StatusOK, `"items":[{`},
		{"other owner", "?ownerType=warehouse&ownerId=" + stubVersionID, http.StatusOK, `"items":[]`},
		{"missing ownerId", "?ownerType=warehouse", http.StatusBadRequest, `"ownerId"`},
		{"ownerType vendor", "?ownerType=vendor&ownerId=" + ownerUUID, http.StatusBadRequest, `"ownerType"`},
		{"purpose billing", owner + "&purpose=billing", http.StatusBadRequest, `"purpose"`},
		{"non-UUID cursor", owner + "&cursor=bm9wZQ", http.StatusBadRequest, `"cursor"`},
	} {
		if rec := do(r, http.MethodGet, addressesPath+tc.query, "", addressDuties, true); rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("list %s = %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodGet, addressesPath+owner, "", "org.site.write", true); rec.Code != http.StatusForbidden {
		t.Fatalf("list without org.read = %d", rec.Code)
	}
}

func TestAddresses_ReviseFailureIsLoggedWithCorrelationId(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	f, _, patch := seededAddress(t, stubGeocoder{})
	f.err = errors.New("db down")
	rec := patch(firstAddress, patchBody(stubVersionID, "city", `"Oakland"`))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(logs.String(), "revise address failed") || !strings.Contains(logs.String(), "correlationId") {
		t.Fatalf("patch = %d, logs %s", rec.Code, logs.String())
	}
}

func TestAddresses_MetricsCountRevisions(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	do(r, http.MethodPost, addressesPath, addressBody(), addressDuties, true)
	do(r, http.MethodPatch, firstAddress, patchBody(stubVersionID, "city", `"Oakland"`), addressDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "org_addresses_revised_total 1") {
		t.Fatalf("metrics missing the revision counter in %s", rec.Body.String())
	}
}

func TestAddresses_MetricsCountCreates(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	do(r, http.MethodPost, addressesPath, addressBody(), addressDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "org_addresses_created_total 1") {
		t.Fatalf("metrics missing the address counter in %s", rec.Body.String())
	}
}

func TestAddresses_Search(t *testing.T) {
	f, r, _ := seededAddress(t, stubGeocoder{})
	long := strings.Repeat("a", 101)
	for _, tc := range []struct {
		name, query string
		status      int
		want        string
	}{
		{"match across owners", "?q=francisco", http.StatusOK, `"items":[{`},
		{"no match", "?q=nowhere", http.StatusOK, `"items":[]`},
		{"country filter match", "?q=Main&countryCode=US", http.StatusOK, `"items":[{`},
		{"country filter excludes", "?q=Main&countryCode=CA", http.StatusOK, `"items":[]`},
		{"two characters", "?q=ab", http.StatusBadRequest, `"q"`},
		{"101 characters", "?q=" + long, http.StatusBadRequest, `"q"`},
		{"whitespace only", "?q=%20%20%20", http.StatusBadRequest, `"q"`},
		{"hundred multi-byte runes", "?q=" + strings.Repeat("%C3%A9", 100), http.StatusOK, `"items":[]`},
		{"ownerType without ownerId", "?q=Main&ownerType=warehouse", http.StatusBadRequest, `"ownerId"`},
		{"ownerId without ownerType", "?q=Main&ownerId=" + ownerUUID, http.StatusBadRequest, `"ownerType"`},
		{"owner pair with q", "?q=Main&ownerType=warehouse&ownerId=" + ownerUUID, http.StatusOK, `"items":[{`},
		{"no q and no owner", "", http.StatusBadRequest, `"ownerType"`},
		{"countryCode lowercase", "?q=Main&countryCode=us", http.StatusBadRequest, `"countryCode"`},
		{"countryCode without owner or q", "?countryCode=US", http.StatusBadRequest, `"ownerType"`},
		{"countryCode three letters", "?q=Main&countryCode=USA", http.StatusBadRequest, `"countryCode"`},
	} {
		if rec := do(r, http.MethodGet, addressesPath+tc.query, "", addressDuties, true); rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("search %s = %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	do(r, http.MethodGet, addressesPath+"?q=%20Main%20", "", addressDuties, true)
	if f.addressFilter.Query != "Main" {
		t.Fatalf("filter query = %q, want the trimmed text", f.addressFilter.Query)
	}
}

func TestAddresses_SearchMetricCountsOnlyQueryRequests(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	do(r, http.MethodGet, addressesPath+"?q=Main", "", addressDuties, true)
	do(r, http.MethodGet, addressesPath+"?q=x", "", addressDuties, true)
	do(r, http.MethodGet, addressesPath+"?ownerType=warehouse&ownerId="+ownerUUID, "", addressDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "org_address_searches_total 2") {
		t.Fatalf("metrics = %s", rec.Body.String())
	}
}

func TestAddresses_AsOf(t *testing.T) {
	f, r, _ := seededAddress(t, stubGeocoder{})
	for _, tc := range []struct {
		name, query string
		status      int
		want        string
	}{
		{"on or after first version", "?asOf=2026-03-15", http.StatusOK, `"versionId":"` + stubVersionID + `"`},
		{"before first version", "?asOf=2025-12-31", http.StatusNotFound, "ORG-ADDR-NOT-FOUND"},
		{"non-calendar date", "?asOf=2026-02-30", http.StatusBadRequest, `"asOf"`},
		{"wrong layout", "?asOf=06/01/2026", http.StatusBadRequest, `"asOf"`},
		{"unpadded", "?asOf=2026-6-1", http.StatusBadRequest, `"asOf"`},
		{"empty", "?asOf=", http.StatusBadRequest, `"asOf"`},
		{"omitted", "", http.StatusOK, `"versionId"`},
	} {
		if rec := do(r, http.MethodGet, firstAddress+tc.query, "", addressDuties, true); rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("asOf %s = %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	do(r, http.MethodGet, firstAddress+"?asOf=2026-05-31", "", addressDuties, true)
	if f.asOfArg != "2026-05-31" {
		t.Fatalf("store asOf = %q", f.asOfArg)
	}
}

func TestAddresses_Versions(t *testing.T) {
	f, r, _ := seededAddress(t, stubGeocoder{})
	versions := firstAddress + "/versions"
	if rec := do(r, http.MethodGet, versions, "", addressDuties, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[{"versionId":"`+stubVersionID+`"`) || !strings.Contains(rec.Body.String(), `"validTo":null`) {
		t.Fatalf("versions = %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{addressesPath + "/00000000-0000-0000-0003-0000000000ff/versions", addressesPath + "/nope/versions"} {
		if rec := do(r, http.MethodGet, path, "", addressDuties, true); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "ORG-ADDR-NOT-FOUND") {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodGet, versions, "", "org.site.write", true); rec.Code != http.StatusForbidden {
		t.Fatalf("versions without org.read = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, versions, "", addressDuties, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("versions without tenant = %d", rec.Code)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	f.err = errors.New("db down")
	if rec := do(r, http.MethodGet, versions, "", addressDuties, true); rec.Code != http.StatusInternalServerError || !strings.Contains(logs.String(), "list address versions failed") || !strings.Contains(logs.String(), "correlationId") {
		t.Fatalf("store failure = %d, logs %s", rec.Code, logs.String())
	}
}
