package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// fakeStore is an in-memory store.Store with the same version and idempotency
// semantics as the Postgres store, so the HTTP contract is testable without a database.
type fakeStore struct {
	mu       sync.Mutex
	projects map[string]domain.Project
	keys     map[string]string
	pingErr  error
}

// sampleProject is a valid CREATED internal project with the given id.
func sampleProject(id string) domain.Project {
	return domain.Project{ID: id, LegalEntityID: ids.New(), ProjectGroupID: "ENG", ProjectType: domain.TypeInternal, Currency: "USD", Stage: domain.StageCreated, StartDate: "2026-10-01"}
}

func newFake() *fakeStore {
	return &fakeStore{projects: map[string]domain.Project{}, keys: map[string]string{}}
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func (f *fakeStore) CreateProject(_ context.Context, in domain.Project, key string) (domain.Project, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.ParentProjectID != nil {
		parent, ok := f.projects[*in.ParentProjectID]
		if !ok {
			return domain.Project{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeParentUnknown, "parent project does not exist")
		}
		var err error
		if in, err = domain.InheritFromParent(in, parent); err != nil {
			return domain.Project{}, false, err
		}
	}
	if key != "" {
		if id, ok := f.keys[key]; ok {
			if !store.SameRequest(f.projects[id], in) {
				return domain.Project{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "key reused")
			}
			return f.projects[id], false, nil
		}
	}
	if _, ok := f.projects[in.ID]; ok {
		return domain.Project{}, false, httpx.NewProblem(http.StatusConflict, store.CodeDuplicate, "a project with this id already exists")
	}
	in.Version = 1
	f.projects[in.ID] = in
	if key != "" {
		f.keys[key] = in.ID
	}
	return in, true, nil
}

func (f *fakeStore) GetProject(_ context.Context, id string) (domain.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok {
		return domain.Project{}, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "project not found")
	}
	return p, nil
}

func (f *fakeStore) ListProjects(_ context.Context, flt store.Filter, after string, limit int) (store.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for id, p := range f.projects {
		if id <= after || (flt.Stage != "" && string(p.Stage) != flt.Stage) || (flt.ProjectType != "" && string(p.ProjectType) != flt.ProjectType) {
			continue
		}
		keys = append(keys, id)
	}
	sort.Strings(keys)
	page := store.Page{Items: []domain.Project{}}
	for _, id := range keys {
		page.Items = append(page.Items, f.projects[id])
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextID = page.Items[limit-1].ID
	}
	return page, nil
}

func (f *fakeStore) TransitionProject(_ context.Context, id string, version int, to domain.Stage) (domain.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok {
		return domain.Project{}, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "project not found")
	}
	if p.Version != version {
		return domain.Project{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	if err := domain.Transition(p.Stage, to); err != nil {
		return domain.Project{}, err
	}
	p.Stage = to
	p.Version++
	f.projects[id] = p
	return p, nil
}

func router(s store.Store) http.Handler {
	r := chi.NewRouter()
	New(s).Register(r)
	return r
}

// ctxFor returns a tenant-1 principal with no legal-entity restriction ("*").
func ctxFor(duties ...string) context.Context {
	return principalCtx("", duties...)
}

func principalCtx(legalEntity string, duties ...string) context.Context {
	p := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntity, UserID: "user-test", Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(context.Background(), p)
}

func TestCreateProject_LegalEntityOutsideScopeIsForbidden(t *testing.T) {
	h := router(newFake())
	ctx := principalCtx(ids.New(), DutyMaintain)
	body := `{"id":"P-SCOPE","legalEntityId":"` + ids.New() + `","projectGroupId":"ENG","projectType":"INTERNAL","currency":"USD","startDate":"2026-10-01"}`
	rec := do(t, h, ctx, http.MethodPost, "/v1/projects", body, map[string]string{"Idempotency-Key": "scope"})
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "AUTH-0003" {
		t.Fatalf("out-of-scope create: status %d body %s", rec.Code, rec.Body.String())
	}
}

func do(t *testing.T, h http.Handler, ctx context.Context, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, rec.Body.String())
	}
	return body.Code
}

func TestCreateProject_ValidatesAndRequiresIdempotencyKey(t *testing.T) {
	h := router(newFake())
	ctx := ctxFor(DutyMaintain)
	legal := ids.New()
	valid := `{"id":"PROJ-0001","legalEntityId":"` + legal + `","projectGroupId":"ENG","projectType":"TIME_MATERIAL",` +
		`"customerPartyId":"` + ids.New() + `","currency":"USD","startDate":"2026-10-01"}`

	cases := []struct {
		name    string
		body    string
		headers map[string]string
		status  int
		code    string
	}{
		{"missing key", valid, nil, http.StatusBadRequest, "COM-0001"},
		{"bad id", strings.Replace(valid, "PROJ-0001", "bad id", 1), map[string]string{"Idempotency-Key": "k1"}, http.StatusBadRequest, "COM-0001"},
		{"billable without customer", `{"id":"P-2","legalEntityId":"` + legal + `","projectGroupId":"ENG","projectType":"FIXED_PRICE","currency":"USD","startDate":"2026-10-01"}`,
			map[string]string{"Idempotency-Key": "k2"}, http.StatusBadRequest, "COM-0001"},
		{"end before start", strings.Replace(valid, `"startDate":"2026-10-01"`, `"startDate":"2026-10-01","endDate":"2026-09-01"`, 1),
			map[string]string{"Idempotency-Key": "k3"}, http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, ctx, http.MethodPost, "/v1/projects", tc.body, tc.headers)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
			if got := errorCode(t, rec); got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
		})
	}

	rec := do(t, h, ctx, http.MethodPost, "/v1/projects", valid, map[string]string{"Idempotency-Key": "k-ok"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("ETag = %q, want \"1\"", rec.Header().Get("ETag"))
	}
}

func TestCreateProject_ReplayReturnsStoredProject(t *testing.T) {
	h := router(newFake())
	ctx := ctxFor(DutyMaintain)
	body := `{"id":"P-9","legalEntityId":"` + ids.New() + `","projectGroupId":"ENG","projectType":"INTERNAL","currency":"USD","startDate":"2026-10-01"}`
	first := do(t, h, ctx, http.MethodPost, "/v1/projects", body, map[string]string{"Idempotency-Key": "same"})
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d", first.Code)
	}
	replay := do(t, h, ctx, http.MethodPost, "/v1/projects", body, map[string]string{"Idempotency-Key": "same"})
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status = %d, replayed = %q", replay.Code, replay.Header().Get("Idempotent-Replayed"))
	}
}

func TestGetProject_NotFoundAndETag(t *testing.T) {
	f := newFake()
	h := router(f)
	ctx := ctxFor(DutyInquire)
	if rec := do(t, h, ctx, http.MethodGet, "/v1/projects/NOPE", "", nil); rec.Code != http.StatusNotFound || errorCode(t, rec) != store.CodeNotFound {
		t.Fatalf("missing project: status %d", rec.Code)
	}
	_, _, _ = f.CreateProject(context.Background(), sampleProject("P-1"), "")
	rec := do(t, h, ctx, http.MethodGet, "/v1/projects/P-1", "", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: status %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestTransition_RequiresIfMatchAndLegalMove(t *testing.T) {
	f := newFake()
	h := router(f)
	ctx := ctxFor(DutyManage)
	_, _, _ = f.CreateProject(context.Background(), sampleProject("P-7"), "")
	path := "/v1/projects/P-7/transitions"

	if rec := do(t, h, ctx, http.MethodPost, path, `{"to":"IN_PROCESS"}`, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status %d", rec.Code)
	}
	if rec := do(t, h, ctx, http.MethodPost, path, `{"to":"IN_PROCESS"}`, map[string]string{"If-Match": `"9"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale version: status %d", rec.Code)
	}
	if rec := do(t, h, ctx, http.MethodPost, path, `{"to":"FINISHED"}`, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeStage {
		t.Fatalf("illegal move: status %d body %s", rec.Code, rec.Body.String())
	}
	rec := do(t, h, ctx, http.MethodPost, path, `{"to":"IN_PROCESS"}`, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("legal move: status %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestList_FiltersAndPaginates(t *testing.T) {
	f := newFake()
	h := router(f)
	ctx := ctxFor(DutyInquire)
	for _, id := range []string{"P-A", "P-B", "P-C"} {
		_, _, _ = f.CreateProject(context.Background(), sampleProject(id), "")
	}
	rec := do(t, h, ctx, http.MethodGet, "/v1/projects?limit=2", "", nil)
	var page struct {
		Items      []domain.Project `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("first page: %v %s", err, rec.Body.String())
	}
	rec = do(t, h, ctx, http.MethodGet, "/v1/projects?limit=2&cursor="+page.NextCursor, "", nil)
	var last struct {
		Items      []domain.Project `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil || len(last.Items) != 1 || last.Items[0].ID != "P-C" || last.NextCursor != "" {
		t.Fatalf("second page: %v %s", err, rec.Body.String())
	}
	if rec := do(t, h, ctx, http.MethodGet, "/v1/projects?stage=DONE", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad stage filter: status %d", rec.Code)
	}
	if rec := do(t, h, ctx, http.MethodGet, "/v1/projects?limit=0", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: status %d", rec.Code)
	}
}

func TestDuties_ForbidWithoutDuty(t *testing.T) {
	h := router(newFake())
	if rec := do(t, h, ctxFor(), http.MethodGet, "/v1/projects", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("list without ProjInquire: status %d", rec.Code)
	}
	if rec := do(t, h, ctxFor(DutyInquire), http.MethodPost, "/v1/projects", "{}", map[string]string{"Idempotency-Key": "x"}); rec.Code != http.StatusForbidden {
		t.Fatalf("create without ProjMaintain: status %d", rec.Code)
	}
}

func TestMissingTenant_IsUnauthenticated(t *testing.T) {
	h := router(newFake())
	rec := do(t, h, context.Background(), http.MethodGet, "/v1/projects", "", nil)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "AUTH-0001" {
		t.Fatalf("no principal: status %d body %s", rec.Code, rec.Body.String())
	}
	bad := tenant.With(context.Background(), tenant.Principal{TenantID: "not-a-uuid", Duties: map[string]struct{}{DutyInquire: {}}})
	if rec := do(t, h, bad, http.MethodGet, "/v1/projects", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("malformed tenant: status %d", rec.Code)
	}
}

func TestCreateProject_SubProjectInheritsAndCannotChangeCustomer(t *testing.T) {
	f := newFake()
	h := router(f)
	ctx := ctxFor(DutyMaintain)
	legal, customer := ids.New(), ids.New()
	parent := `{"id":"P-100","legalEntityId":"` + legal + `","projectGroupId":"ENG","projectType":"FIXED_PRICE","customerPartyId":"` + customer + `","currency":"USD","startDate":"2026-10-01"}`
	if rec := do(t, h, ctx, http.MethodPost, "/v1/projects", parent, map[string]string{"Idempotency-Key": "parent"}); rec.Code != http.StatusCreated {
		t.Fatalf("parent: status %d %s", rec.Code, rec.Body.String())
	}

	sub := `{"id":"P-100-1","legalEntityId":"` + legal + `","parentProjectId":"P-100","projectType":"FIXED_PRICE","currency":"USD","startDate":"2026-10-01"}`
	rec := do(t, h, ctx, http.MethodPost, "/v1/projects", sub, map[string]string{"Idempotency-Key": "sub"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("sub-project without group and customer: status %d %s", rec.Code, rec.Body.String())
	}
	var got domain.Project
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ProjectGroupID != "ENG" || got.CustomerPartyID == nil || *got.CustomerPartyID != customer {
		t.Fatalf("inherited group %q customer %v", got.ProjectGroupID, got.CustomerPartyID)
	}

	other := `{"id":"P-100-2","legalEntityId":"` + legal + `","parentProjectId":"P-100","projectType":"FIXED_PRICE","customerPartyId":"` + ids.New() + `","currency":"USD","startDate":"2026-10-01"}`
	rec = do(t, h, ctx, http.MethodPost, "/v1/projects", other, map[string]string{"Idempotency-Key": "other"})
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeParentConflict {
		t.Fatalf("customer change: status %d %s", rec.Code, rec.Body.String())
	}

	orphan := strings.Replace(sub, `"P-100-1"`, `"P-100-3"`, 1)
	orphan = strings.Replace(orphan, `"parentProjectId":"P-100"`, `"parentProjectId":"NOPE"`, 1)
	rec = do(t, h, ctx, http.MethodPost, "/v1/projects", orphan, map[string]string{"Idempotency-Key": "orphan"})
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != store.CodeParentUnknown {
		t.Fatalf("unknown parent: status %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateProject_KeyReusedWithOtherBodyIsRefused(t *testing.T) {
	h := router(newFake())
	ctx := ctxFor(DutyMaintain)
	body := `{"id":"P-K","legalEntityId":"` + ids.New() + `","projectGroupId":"ENG","projectType":"INTERNAL","currency":"USD","startDate":"2026-10-01"}`
	if rec := do(t, h, ctx, http.MethodPost, "/v1/projects", body, map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusCreated {
		t.Fatalf("first: status %d", rec.Code)
	}
	changed := strings.Replace(body, `"USD"`, `"EUR"`, 1)
	rec := do(t, h, ctx, http.MethodPost, "/v1/projects", changed, map[string]string{"Idempotency-Key": "k"})
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != store.CodeIdempotencyReuse {
		t.Fatalf("reuse: status %d %s", rec.Code, rec.Body.String())
	}
	long := map[string]string{"Idempotency-Key": strings.Repeat("k", 256)}
	if rec := do(t, h, ctx, http.MethodPost, "/v1/projects", body, long); rec.Code != http.StatusBadRequest {
		t.Fatalf("over-long key: status %d", rec.Code)
	}
}

func TestList_FiltersByProjectType(t *testing.T) {
	f := newFake()
	h := router(f)
	fixed := sampleProject("P-FP")
	fixed.ProjectType = domain.TypeFixedPrice
	_, _, _ = f.CreateProject(context.Background(), fixed, "")
	_, _, _ = f.CreateProject(context.Background(), sampleProject("P-IN"), "")
	rec := do(t, h, ctxFor(DutyInquire), http.MethodGet, "/v1/projects?projectType=FIXED_PRICE", "", nil)
	var page struct {
		Items []domain.Project `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].ID != "P-FP" {
		t.Fatalf("type filter: %v %s", err, rec.Body.String())
	}
	if rec := do(t, h, ctxFor(DutyInquire), http.MethodGet, "/v1/projects?projectType=HOBBY", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad type filter: status %d", rec.Code)
	}
}

func TestTransition_RequiresManageDuty(t *testing.T) {
	f := newFake()
	_, _, _ = f.CreateProject(context.Background(), sampleProject("P-D"), "")
	rec := do(t, router(f), ctxFor(DutyMaintain, DutyInquire), http.MethodPost, "/v1/projects/P-D/transitions", `{"to":"IN_PROCESS"}`, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "AUTH-0003" {
		t.Fatalf("transition without ProjManage: status %d %s", rec.Code, rec.Body.String())
	}
}

func TestReadyz_PingsTheStore(t *testing.T) {
	f := newFake()
	r := httpx.NewRouter()
	New(f).Register(r)
	if rec := do(t, r, context.Background(), http.MethodGet, "/readyz", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("ready with a healthy store: status %d", rec.Code)
	}
	f.pingErr = errors.New("connection refused")
	if rec := do(t, r, context.Background(), http.MethodGet, "/readyz", "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready with a failing store: status %d", rec.Code)
	}
}
