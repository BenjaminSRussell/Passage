package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/api"
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/store"
)

const (
	releasesPath = "/v1/platform/releases"
	digestA      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB      = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	gitSHA       = "0123456789abcdef0123456789abcdef01234567"
)

func releaseBody(mutate func(string) string) string {
	body := `{"service":"hello-ledger","environment":"stage","action":"promote","imageDigest":"` + digestB + `","previousDigest":"` + digestA + `","gitSha":"` + gitSHA + `","signed":true}`
	if mutate != nil {
		return mutate(body)
	}
	return body
}

func doRelease(r http.Handler, method, path, body, duties, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestRecordRelease(t *testing.T) {
	replace := func(old, new string) func(string) string {
		return func(b string) string { return strings.Replace(b, old, new, 1) }
	}
	tests := []struct {
		name       string
		store      *fakeStore
		body       string
		wantStatus int
		wantBody   string
	}{
		{"promote ok", &fakeStore{}, releaseBody(nil), http.StatusCreated, `"action":"promote"`},
		{"rollback ok", &fakeStore{}, releaseBody(replace(`"promote"`, `"rollback","reason":"bad deploy"`)), http.StatusCreated, `"reason":"bad deploy"`},
		{"unknown action", &fakeStore{}, releaseBody(replace(`"promote"`, `"deploy"`)), http.StatusBadRequest, "COM-0001"},
		{"digest not sha256", &fakeStore{}, releaseBody(replace(digestB, "latest")), http.StatusBadRequest, "COM-0001"},
		{"uppercase digest", &fakeStore{}, releaseBody(replace(digestB, strings.ToUpper(digestB))), http.StatusBadRequest, "COM-0001"},
		{"same digest", &fakeStore{}, releaseBody(replace(digestB, digestA)), http.StatusBadRequest, "COM-0001"},
		{"bad service", &fakeStore{}, releaseBody(replace("hello-ledger", "Hello_Ledger")), http.StatusBadRequest, "COM-0001"},
		{"empty environment", &fakeStore{}, releaseBody(replace(`"stage"`, `""`)), http.StatusBadRequest, "COM-0001"},
		{"missing git sha", &fakeStore{}, releaseBody(replace(`"gitSha":"`+gitSHA+`",`, "")), http.StatusBadRequest, "gitSha"},
		{"reason too long", &fakeStore{}, releaseBody(replace(`"signed":true`, `"reason":"`+strings.Repeat("a", 281)+`"`)), http.StatusBadRequest, "COM-0001"},
		{"unknown field", &fakeStore{}, releaseBody(replace(`"signed":true`, `"x":1`)), http.StatusBadRequest, "COM-0001"},
		{"malformed body", &fakeStore{}, `{`, http.StatusBadRequest, "COM-0001"},
		{"idempotency conflict", &fakeStore{err: store.ErrIdempotencyConflict}, releaseBody(nil), http.StatusConflict, "COM-0409"},
		{"unregistered environment", &fakeStore{err: store.ErrUnknownEnvironment}, releaseBody(nil), http.StatusBadRequest, "environment is not registered"},
		{"rollback to unseen digest", &fakeStore{err: store.ErrRollbackTargetUnseen}, releaseBody(replace(`"promote"`, `"rollback"`)), http.StatusUnprocessableEntity, "PLH-0422"},
		{"store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, releaseBody(nil), http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := doRelease(r, http.MethodPost, releasesPath, tc.body, "Maintain platform releases,Rollback environment", "")
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("internal detail leaked: %s", rec.Body.String())
			}
			if tc.wantStatus != http.StatusCreated && len(tc.store.releases) != 0 {
				t.Fatalf("rejected request reached the store")
			}
		})
	}
}

func TestRecordReleaseForwardsIdempotencyKey(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	if rec := doRelease(r, http.MethodPost, releasesPath, releaseBody(nil), "Maintain platform releases", "key-1"); rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if f.lastKey != "key-1" {
		t.Fatalf("store saw key %q", f.lastKey)
	}
}

func TestListReleasesFiltersAndValidates(t *testing.T) {
	f := &fakeStore{releases: []store.Release{
		{ID: "1", Service: "hello-ledger", Environment: "stage"},
		{ID: "2", Service: "hello-ledger", Environment: "prod"},
	}}
	r, _ := newRouter(f)
	tests := []struct {
		query      string
		wantStatus int
		wantCount  int
	}{
		{"", http.StatusOK, 2},
		{"?env=prod", http.StatusOK, 1},
		{"?service=hello-ledger&env=stage", http.StatusOK, 1},
		{"?service=other", http.StatusOK, 0},
		{"?env=PROD", http.StatusBadRequest, 0},
		{"?service=a%27%3B--", http.StatusBadRequest, 0},
	}
	for _, tc := range tests {
		rec := doRelease(r, http.MethodGet, releasesPath+tc.query, "", "View platform releases", "")
		if rec.Code != tc.wantStatus {
			t.Fatalf("%q: got %d %s; want %d", tc.query, rec.Code, rec.Body.String(), tc.wantStatus)
		}
		if tc.wantStatus == http.StatusOK && strings.Count(rec.Body.String(), `"id"`) != tc.wantCount {
			t.Fatalf("%q: body %s; want %d releases", tc.query, rec.Body.String(), tc.wantCount)
		}
	}
	if rec := doRelease(newRouterOnly(&fakeStore{}), http.MethodGet, releasesPath, "", "View platform releases", ""); !strings.HasPrefix(rec.Body.String(), "[]") {
		t.Fatalf("empty list must be an array, got %s", rec.Body.String())
	}
}

func newRouterOnly(f *fakeStore) http.Handler { r, _ := newRouter(f); return r }

func TestReleasesRequireDuty(t *testing.T) {
	prod := func(b string) string { return strings.Replace(b, `"stage"`, `"prod"`, 1) }
	rollback := func(b string) string { return strings.Replace(b, `"promote"`, `"rollback"`, 1) }
	tests := []struct {
		name, method, duties string
		body                 string
		wantStatus           int
	}{
		{"record without duty", http.MethodPost, "", releaseBody(nil), http.StatusForbidden},
		{"record with view duty only", http.MethodPost, "View platform releases", releaseBody(nil), http.StatusForbidden},
		{"list without duty", http.MethodGet, "", "", http.StatusForbidden},
		{"list with maintain duty only", http.MethodGet, "Maintain platform releases", "", http.StatusForbidden},
		{"stage promote with maintain duty", http.MethodPost, "Maintain platform releases", releaseBody(nil), http.StatusCreated},
		{"prod promote with maintain duty only", http.MethodPost, "Maintain platform releases", releaseBody(prod), http.StatusForbidden},
		{"prod promote with approve duty", http.MethodPost, "Approve production promotion", releaseBody(prod), http.StatusCreated},
		{"stage promote with approve duty only", http.MethodPost, "Approve production promotion", releaseBody(nil), http.StatusForbidden},
		{"stage rollback with maintain duty only", http.MethodPost, "Maintain platform releases", releaseBody(rollback), http.StatusForbidden},
		{"stage rollback with rollback duty", http.MethodPost, "Rollback environment", releaseBody(rollback), http.StatusCreated},
		{"prod rollback with approve duty only", http.MethodPost, "Approve production promotion", releaseBody(func(b string) string { return rollback(prod(b)) }), http.StatusForbidden},
		{"list with view duty", http.MethodGet, "View platform releases", "", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			rec := doRelease(newRouterOnly(f), tc.method, releasesPath, tc.body, tc.duties, "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d %s; want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			if tc.wantStatus == http.StatusForbidden && (!strings.Contains(rec.Body.String(), "AUTH-0003") || len(f.releases) != 0) {
				t.Fatalf("want AUTH-0003 and no write, got %s with %d rows", rec.Body.String(), len(f.releases))
			}
		})
	}
}

func TestRecordReleaseRestrictedToOperatorTenant(t *testing.T) {
	tests := []struct {
		name     string
		operator string
		tenantN  int
		wantBody string
	}{
		{"other tenant", testkit.TenantID(1), 2, "restricted to the platform operator tenant"},
		{"operator unset", "", 1, "PLATFORM_OPERATOR_TENANT_ID is not configured"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			r := httpx.NewRouter()
			api.New(f, tc.operator).Register(r)
			req := httptest.NewRequest(http.MethodPost, releasesPath, strings.NewReader(releaseBody(nil)))
			req.Header.Set("X-Passage-Tenant", testkit.TenantID(tc.tenantN))
			req.Header.Set("X-Passage-Duties", "Maintain platform releases,Approve production promotion,Rollback environment")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), tc.wantBody) || len(f.releases) != 0 {
				t.Fatalf("got %d %s with %d rows; want 403 containing %q and no write", rec.Code, rec.Body.String(), len(f.releases), tc.wantBody)
			}
		})
	}
}

func TestListReleasesOpenToAnyTenantWithDuty(t *testing.T) {
	r := httpx.NewRouter()
	api.New(&fakeStore{}, testkit.TenantID(1)).Register(r)
	req := httptest.NewRequest(http.MethodGet, releasesPath, nil)
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(2))
	req.Header.Set("X-Passage-Duties", "View platform releases")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestReleasesRequireTenant(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		req := httptest.NewRequest(method, releasesPath, strings.NewReader(releaseBody(nil)))
		rec := httptest.NewRecorder()
		newRouterOnly(&fakeStore{}).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "AUTH-0001") {
			t.Fatalf("%s: got %d %s", method, rec.Code, rec.Body.String())
		}
	}
}
