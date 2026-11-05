package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/api"
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/store"
	"github.com/BenjaminSRussell/passage/services/platform-health/migrations"
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
			rec := doRelease(r, http.MethodPost, releasesPath, tc.body, "platform.release.maintain,platform.environment.rollback", "")
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

func TestRollbackToPreRegistryDigestIsAcceptedAfterGoLive(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	a := api.New(store.New(app), testkit.TenantID(1))
	r := httpx.NewRouter()
	a.Register(r)
	toRollback := func(b string) string { return strings.Replace(b, `"promote"`, `"rollback"`, 1) }
	swapped := strings.NewReplacer(digestB, digestA, digestA, digestB)
	duties := "platform.release.maintain,platform.environment.rollback"
	if rec := doRelease(r, http.MethodPost, releasesPath, releaseBody(nil), duties, ""); rec.Code != http.StatusCreated {
		t.Fatalf("go-live promote: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doRelease(r, http.MethodPost, releasesPath, swapped.Replace(toRollback(releaseBody(nil))), duties, ""); rec.Code != http.StatusCreated {
		t.Fatalf("rollback to the pre-registry digest: %d %s", rec.Code, rec.Body.String())
	}
	never := strings.Replace(toRollback(releaseBody(nil)), digestB, "sha256:"+strings.Repeat("c", 64), 1)
	if rec := doRelease(r, http.MethodPost, releasesPath, never, duties, ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("rollback to a never-seen digest: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecordReleaseForwardsIdempotencyKey(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	if rec := doRelease(r, http.MethodPost, releasesPath, releaseBody(nil), "platform.release.maintain", "key-1"); rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if f.lastKey != "key-1" {
		t.Fatalf("store saw key %q", f.lastKey)
	}
}

func TestReplayedReleaseIsNotCountedAsRecorded(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	for range 2 {
		if rec := doRelease(r, http.MethodPost, releasesPath, releaseBody(nil), "platform.release.maintain", "key-1"); rec.Code != http.StatusCreated {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"hello_ledger_releases_recorded_total 1", "hello_ledger_releases_replayed_total 1"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, rec.Body.String())
		}
	}
}

func TestChainBreakIsCountedAndReplaysAreNot(t *testing.T) {
	r, a := newRouter(&fakeStore{chainBreak: true})
	if rec := doRelease(r, http.MethodPost, releasesPath, releaseBody(nil), "platform.release.maintain", ""); rec.Code != http.StatusCreated {
		t.Fatalf("a chain break is flagged, not refused: %d %s", rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "hello_ledger_release_chain_breaks_total 1") {
		t.Fatalf("metrics missing the chain break:\n%s", rec.Body.String())
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
		rec := doRelease(r, http.MethodGet, releasesPath+tc.query, "", "platform.release.view", "")
		if rec.Code != tc.wantStatus {
			t.Fatalf("%q: got %d %s; want %d", tc.query, rec.Code, rec.Body.String(), tc.wantStatus)
		}
		if tc.wantStatus == http.StatusOK && strings.Count(rec.Body.String(), `"id"`) != tc.wantCount {
			t.Fatalf("%q: body %s; want %d releases", tc.query, rec.Body.String(), tc.wantCount)
		}
	}
	if rec := doRelease(newRouterOnly(&fakeStore{}), http.MethodGet, releasesPath, "", "platform.release.view", ""); !strings.HasPrefix(rec.Body.String(), "[]") {
		t.Fatalf("empty list must be an array, got %s", rec.Body.String())
	}
}

func TestListReleasesUnknownCursorIs400(t *testing.T) {
	r, _ := newRouter(&fakeStore{err: store.ErrUnknownCursor})
	rec := doRelease(r, http.MethodGet, releasesPath+"?cursor=00000000-0000-4000-8000-000000000009", "", "platform.release.view", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestListReleasesCursor(t *testing.T) {
	f := &fakeStore{nextCursor: "00000000-0000-4000-8000-000000000001"}
	r, _ := newRouter(f)
	rec := doRelease(r, http.MethodGet, releasesPath+"?cursor=00000000-0000-4000-8000-000000000002", "", "platform.release.view", "")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Next-Cursor") != f.nextCursor || f.lastAfter != "00000000-0000-4000-8000-000000000002" {
		t.Fatalf("got %d header %q after %q", rec.Code, rec.Header().Get("X-Next-Cursor"), f.lastAfter)
	}
	if rec := doRelease(r, http.MethodGet, releasesPath+"?cursor=not-an-id", "", "platform.release.view", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor: %d", rec.Code)
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
		{"record with view duty only", http.MethodPost, "platform.release.view", releaseBody(nil), http.StatusForbidden},
		{"list without duty", http.MethodGet, "", "", http.StatusForbidden},
		{"list with maintain duty only", http.MethodGet, "platform.release.maintain", "", http.StatusForbidden},
		{"stage promote with maintain duty", http.MethodPost, "platform.release.maintain", releaseBody(nil), http.StatusCreated},
		{"prod promote with maintain duty only", http.MethodPost, "platform.release.maintain", releaseBody(prod), http.StatusForbidden},
		{"prod promote with approve duty", http.MethodPost, "platform.promotion.approve", releaseBody(prod), http.StatusCreated},
		{"stage promote with approve duty only", http.MethodPost, "platform.promotion.approve", releaseBody(nil), http.StatusForbidden},
		{"stage rollback with maintain duty only", http.MethodPost, "platform.release.maintain", releaseBody(rollback), http.StatusForbidden},
		{"stage rollback with rollback duty", http.MethodPost, "platform.environment.rollback", releaseBody(rollback), http.StatusCreated},
		{"prod rollback with approve duty only", http.MethodPost, "platform.promotion.approve", releaseBody(func(b string) string { return rollback(prod(b)) }), http.StatusForbidden},
		{"list with view duty", http.MethodGet, "platform.release.view", "", http.StatusOK},
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

func TestRecordReleaseForbiddenNamesEveryAcceptedDuty(t *testing.T) {
	rec := doRelease(newRouterOnly(&fakeStore{}), http.MethodPost, releasesPath, releaseBody(nil), "platform.release.view", "")
	for _, duty := range []string{"platform.release.maintain", "platform.promotion.approve", "platform.environment.rollback"} {
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), duty) {
			t.Fatalf("got %d %s; want 403 naming %q", rec.Code, rec.Body.String(), duty)
		}
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
			req.Header.Set("X-Passage-Duties", "platform.release.maintain,platform.promotion.approve,platform.environment.rollback")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), tc.wantBody) || len(f.releases) != 0 {
				t.Fatalf("got %d %s with %d rows; want 403 containing %q and no write", rec.Code, rec.Body.String(), len(f.releases), tc.wantBody)
			}
		})
	}
}

func TestListReleasesOpenToAnyTenantWithDuty(t *testing.T) {
	tests := []struct {
		name      string
		tenantN   int
		wantActor bool
	}{
		{"other tenant never sees the operator's actor", 2, false},
		{"operator tenant sees the actor", 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httpx.NewRouter()
			api.New(&fakeStore{releases: []store.Release{{ID: "1", Actor: "operator-user"}}}, testkit.TenantID(1)).Register(r)
			req := httptest.NewRequest(http.MethodGet, releasesPath, nil)
			req.Header.Set("X-Passage-Tenant", testkit.TenantID(tc.tenantN))
			req.Header.Set("X-Passage-Duties", "platform.release.view")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "operator-user") != tc.wantActor {
				t.Fatalf("got %d %s; want 200 with actor visible = %v", rec.Code, rec.Body.String(), tc.wantActor)
			}
		})
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
