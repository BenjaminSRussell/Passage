package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/api"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	engevents "github.com/BenjaminSRussell/passage/services/engineering/internal/events"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

const (
	allDuties  = "engineering.read,engineering.bom.write,engineering.eco.approve,engineering.resource.write"
	versions   = "/v1/engineering/bom-versions"
	ecos       = "/v1/engineering/ecos"
	oldVersion = "00000000-0000-0000-0000-0000000000a1"
	newVersion = "00000000-0000-0000-0000-0000000000a2"
	ecoID      = "00000000-0000-0000-0000-0000000000e1"
	validBom   = `{"productId":"BIKE","effectiveFrom":"2026-01-01","lines":[{"componentProductId":"WHEEL","quantity":"2"}]}`
)

var today = time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)

type releaseCall struct {
	eco  domain.Eco
	prev domain.EcoState
	plan domain.ReleasePlan
}

type fakeStore struct {
	store.Store
	err      error
	pingErr  error
	relErr   error
	catalog  store.Snapshot
	listed   []domain.BomVersion
	record   store.EcoRecord
	byKey    map[string]domain.BomVersion
	hashes   map[string]string
	created  []domain.BomVersion
	released []releaseCall
	res      *resourceState
	routes   []domain.RouteVersion

	activateErr error
	resList     []store.ResourceRecord
	groupList   []domain.ResourceGroup
	capList     []domain.Capability
}

func (f *fakeStore) CreateBomVersion(_ context.Context, v domain.BomVersion, idem store.Idem, check func(store.Snapshot) error) (domain.BomVersion, error) {
	if f.err != nil {
		return domain.BomVersion{}, f.err
	}
	if replay, ok := f.byKey[idem.Key]; ok && idem.Key != "" {
		if f.hashes[idem.Key] != idem.Hash {
			return domain.BomVersion{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reused")
		}
		return replay, nil
	}
	if err := check(f.catalog); err != nil {
		return domain.BomVersion{}, err
	}
	f.created = append(f.created, v)
	if f.byKey == nil {
		f.byKey, f.hashes = map[string]domain.BomVersion{}, map[string]string{}
	}
	f.byKey[idem.Key], f.hashes[idem.Key] = v, idem.Hash
	return v, nil
}

func (f *fakeStore) ListBomVersions(_ context.Context, after string, limit int) (store.BomVersionPage, error) {
	if f.err != nil {
		return store.BomVersionPage{}, f.err
	}
	page := store.BomVersionPage{Items: f.listed}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextID = page.Items[limit-1].ID
	}
	return page, nil
}

func (f *fakeStore) Catalog(context.Context, []string, bool) (store.Snapshot, error) {
	return f.catalog, f.err
}

func (f *fakeStore) CreateEco(_ context.Context, e domain.Eco, _ store.Idem) (domain.Eco, error) {
	return e, f.err
}

func (f *fakeStore) GetEco(context.Context, string) (store.EcoRecord, error) {
	return f.record, f.err
}

func (f *fakeStore) ReleaseEco(_ context.Context, e domain.Eco, prev domain.EcoState, plan domain.ReleasePlan) error {
	f.released = append(f.released, releaseCall{e, prev, plan})
	return f.relErr
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

type approverFunc func(context.Context, string) (domain.Decision, error)

func (a approverFunc) Decide(ctx context.Context, id string) (domain.Decision, error) {
	return a(ctx, id)
}

var approves = approverFunc(func(context.Context, string) (domain.Decision, error) {
	return domain.Decision{Approved: true, Actor: "alice"}, nil
})

func newRouter(f *fakeStore, ap domain.Approver) (*chi.Mux, *api.API) {
	a := api.New(f, ap, func() time.Time { return today })
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func do(r http.Handler, method, path, body, duties string, withTenant bool, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if withTenant {
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Duties", duties)
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func oneLine(no int, component string, qty int64) domain.BomLine {
	return domain.BomLine{LineNo: no, ComponentProductID: component, Quantity: decimal.NewFromInt(qty), Series: decimal.NewFromInt(1), LineType: domain.LineItem}
}

func activeVersion(id, product, from, to string, lines ...domain.BomLine) domain.BomVersion {
	v := domain.BomVersion{ID: id, ProductID: product, EffectiveFrom: day(from), Status: domain.StatusActive, Lines: lines}
	if to != "" {
		v.EffectiveTo = day(to)
	}
	return v
}

func TestCreateBomVersion(t *testing.T) {
	cycle := store.Snapshot{"WHEEL": {activeVersion("w1", "WHEEL", "2025-01-01", "", oneLine(1, "BIKE", 1))}}
	ended := store.Snapshot{"BIKE": {activeVersion("b0", "BIKE", "2024-01-01", "2025-12-31", oneLine(1, "WHEEL", 1))}}
	overlapping := store.Snapshot{"BIKE": {activeVersion("b1", "BIKE", "2025-01-01", "", oneLine(1, "WHEEL", 1))}}
	tests := []struct {
		name       string
		store      *fakeStore
		body       string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"decimal string quantity", &fakeStore{}, validBom, allDuties, true, http.StatusCreated, `"productId":"BIKE"`},
		{"decimal number quantity", &fakeStore{}, `{"productId":"BIKE","effectiveFrom":"2026-01-01","lines":[{"componentProductId":"WHEEL","quantity":1.5}]}`, allDuties, true, http.StatusCreated, `"quantity":"1.5"`},
		{"future effective date", &fakeStore{}, `{"productId":"BIKE","effectiveFrom":"2031-01-01","lines":[{"componentProductId":"WHEEL","quantity":"1"}]}`, allDuties, true, http.StatusCreated, "2031-01-01"},
		{"no tenant", &fakeStore{}, validBom, "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"missing bom write duty", &fakeStore{}, validBom, "engineering.read", true, http.StatusForbidden, "AUTH-0003"},
		{"malformed json", &fakeStore{}, `{`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"null body", &fakeStore{}, `null`, allDuties, true, http.StatusBadRequest, `"lines"`},
		{"wrong typed quantity", &fakeStore{}, `{"productId":"BIKE","effectiveFrom":"2026-01-01","lines":[{"componentProductId":"WHEEL","quantity":"abc"}]}`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"object for scalar", &fakeStore{}, `{"productId":"BIKE","effectiveFrom":"2026-01-01","lines":[{"componentProductId":"WHEEL","quantity":{"a":1}}]}`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"NaN quantity", &fakeStore{}, `{"productId":"BIKE","effectiveFrom":"2026-01-01","lines":[{"componentProductId":"WHEEL","quantity":"NaN"}]}`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"negative quantity", &fakeStore{}, `{"productId":"BIKE","effectiveFrom":"2026-01-01","lines":[{"componentProductId":"WHEEL","quantity":"-1"}]}`, allDuties, true, http.StatusBadRequest, `"lines[0]"`},
		{"huge exponent quantity", &fakeStore{}, `{"productId":"BIKE","effectiveFrom":"2026-01-01","lines":[{"componentProductId":"WHEEL","quantity":"1e999999999"}]}`, allDuties, true, http.StatusBadRequest, `"lines[0]"`},
		{"inverted range", &fakeStore{}, `{"productId":"BIKE","effectiveFrom":"2026-06-01","effectiveTo":"2026-01-01","lines":[{"componentProductId":"WHEEL","quantity":"1"}]}`, allDuties, true, http.StatusBadRequest, `"effectiveTo"`},
		{"empty lines", &fakeStore{}, `{"productId":"BIKE","effectiveFrom":"2026-01-01","lines":[]}`, allDuties, true, http.StatusBadRequest, `"lines"`},
		{"duplicate line number", &fakeStore{}, `{"productId":"BIKE","effectiveFrom":"2026-01-01","lines":[{"lineNo":1,"componentProductId":"A","quantity":"1"},{"lineNo":1,"componentProductId":"B","quantity":"1"}]}`, allDuties, true, http.StatusBadRequest, "duplicated"},
		{"first active version is allowed", &fakeStore{}, strings.Replace(validBom, `"lines"`, `"status":"active","lines"`, 1), allDuties, true, http.StatusCreated, `"status":"active"`},
		{"later active version needs an ECO", &fakeStore{catalog: ended}, strings.Replace(validBom, `"lines"`, `"status":"active","lines"`, 1), allDuties, true, http.StatusUnprocessableEntity, domain.CodeBomEcoRequired},
		{"component cycle", &fakeStore{catalog: cycle}, validBom, allDuties, true, http.StatusUnprocessableEntity, domain.CodeBomCycle},
		{"overlapping active range", &fakeStore{catalog: overlapping}, strings.Replace(validBom, `"lines"`, `"status":"active","lines"`, 1), allDuties, true, http.StatusUnprocessableEntity, domain.CodeBomOverlap},
		{"store unavailable hides detail", &fakeStore{err: errors.New("pg secret detail")}, validBom, allDuties, true, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store, approves)
			rec := do(r, http.MethodPost, versions, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("internal detail leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestCreateBomVersion_IdempotencyKey(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f, approves)
	post := func(body string) *httptest.ResponseRecorder {
		return do(r, http.MethodPost, versions, body, allDuties, true, "Idempotency-Key", "key-1")
	}
	first, replay := post(validBom), post(validBom)
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || first.Body.String() != replay.Body.String() || len(f.created) != 1 {
		t.Fatalf("replay must return the original version: %d %s / %d %s, rows %d", first.Code, first.Body, replay.Code, replay.Body, len(f.created))
	}
	reuse := post(strings.Replace(validBom, `"BIKE"`, `"TRIKE"`, 1))
	if reuse.Code != http.StatusUnprocessableEntity || !strings.Contains(reuse.Body.String(), store.CodeIdempotencyReuse) {
		t.Fatalf("different body under the same key = %d %s; want 422 %s", reuse.Code, reuse.Body, store.CodeIdempotencyReuse)
	}
}

func TestListBomVersions(t *testing.T) {
	f := &fakeStore{listed: []domain.BomVersion{
		{ID: oldVersion, ProductID: "A", EffectiveFrom: day("2026-01-01"), Status: domain.StatusActive},
		{ID: newVersion, ProductID: "B", EffectiveFrom: day("2026-01-01"), Status: domain.StatusDraft},
	}}
	r, _ := newRouter(f, approves)
	empty, _ := newRouter(&fakeStore{}, approves)
	if rec := do(empty, http.MethodGet, versions, "", allDuties, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty list = %d %s", rec.Code, rec.Body)
	}
	first := do(r, http.MethodGet, versions+"?limit=1", "", allDuties, true)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"nextCursor"`) || strings.Contains(first.Body.String(), `"B"`) {
		t.Fatalf("first page = %d %s", first.Code, first.Body)
	}
	for _, bad := range []string{"?limit=0", "?limit=101", "?limit=x", "?cursor=!!", "?cursor=YWJj"} {
		if rec := do(r, http.MethodGet, versions+bad, "", allDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Fatalf("%s = %d %s; want 400 COM-0001", bad, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodGet, versions, "", "", true); rec.Code != http.StatusForbidden {
		t.Fatalf("list without read duty = %d; want 403", rec.Code)
	}
	failing, _ := newRouter(&fakeStore{err: errors.New("boom")}, approves)
	if rec := do(failing, http.MethodGet, versions, "", allDuties, true); rec.Code != http.StatusInternalServerError {
		t.Fatalf("list store error = %d; want 500", rec.Code)
	}
}

func goldenCatalog() store.Snapshot {
	sizes := []int{1, 4, 12, 30, 60, 150, 300, 1444}
	pid := func(l, i int) string { return fmt.Sprintf("L%d-%04d", l, i) }
	lines := map[string][]domain.BomLine{}
	for l := 1; l < len(sizes); l++ {
		for i := 0; i < sizes[l]; i++ {
			ln := oneLine(i, pid(l, i), int64(i%5+1))
			if i%3 == 0 {
				ln.ScrapVarPct, ln.RoundUp = decimal.NewFromInt(5), domain.RoundUpQuantity
			}
			if l == 4 && i%10 == 0 {
				ln.LineType = domain.LinePhantom
			}
			parent := pid(l-1, i%sizes[l-1])
			lines[parent] = append(lines[parent], ln)
		}
	}
	snapshot := store.Snapshot{}
	for parent, ls := range lines {
		snapshot[parent] = []domain.BomVersion{activeVersion("v-"+parent, parent, "2025-01-01", "", ls...)}
	}
	return snapshot
}

type explosionResponse struct {
	Root struct {
		Children []json.RawMessage `json:"children"`
	} `json:"root"`
	Flat []struct {
		ProductID string          `json:"productId"`
		Quantity  decimal.Decimal `json:"quantity"`
	} `json:"flat"`
}

func TestExplode_7Level2000Under500ms(t *testing.T) {
	var golden struct {
		FlatCount     int
		TotalQuantity string
		AsOf          string
		RequestQty    string
	}
	raw, err := os.ReadFile("../../testdata/bom-golden-7x2000.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	r, _ := newRouter(&fakeStore{catalog: goldenCatalog()}, approves)
	body := fmt.Sprintf(`{"productId":"L0-0000","asOf":%q,"quantity":%q,"levels":7}`, golden.AsOf, golden.RequestQty)

	start := time.Now()
	rec := do(r, http.MethodPost, "/v1/engineering/boms:explode", body, allDuties, true)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("explode = %d %s", rec.Code, rec.Body.String()[:200])
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("explosion through the handler took %v, want < 500ms", elapsed)
	}
	var got explosionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	total := decimal.Zero
	for _, f := range got.Flat {
		total = total.Add(f.Quantity)
	}
	if len(got.Flat) != golden.FlatCount || total.String() != golden.TotalQuantity {
		t.Fatalf("flat = %d lines total %s; want %d lines total %s", len(got.Flat), total, golden.FlatCount, golden.TotalQuantity)
	}
}

func TestExplode_PicksVersionByAsOfDate(t *testing.T) {
	catalog := store.Snapshot{"BIKE": {
		activeVersion("v1", "BIKE", "2025-01-01", "2026-05-31", oneLine(1, "OLD-WHEEL", 1)),
		activeVersion("v2", "BIKE", "2026-06-01", "", oneLine(1, "NEW-WHEEL", 1)),
	}}
	r, _ := newRouter(&fakeStore{catalog: catalog}, approves)
	for asOf, want := range map[string]string{"2026-01-01": "OLD-WHEEL", "2026-06-01": "NEW-WHEEL", "": "NEW-WHEEL"} {
		rec := do(r, http.MethodPost, "/v1/engineering/boms:explode", fmt.Sprintf(`{"productId":"BIKE","asOf":%q}`, asOf), allDuties, true)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"flat":[{"productId":"`+want+`"`) {
			t.Fatalf("asOf %q = %d %s; want %s", asOf, rec.Code, rec.Body, want)
		}
	}
}

func TestExplode_Rejections(t *testing.T) {
	catalog := store.Snapshot{"BIKE": {activeVersion("v1", "BIKE", "2025-01-01", "", oneLine(1, "WHEEL", 1))}}
	tests := []struct {
		name       string
		body       string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"unknown product", `{"productId":"NOPE"}`, allDuties, http.StatusUnprocessableEntity, domain.CodeBomNoVersion},
		{"no version at date", `{"productId":"BIKE","asOf":"2020-01-01"}`, allDuties, http.StatusUnprocessableEntity, domain.CodeBomNoVersion},
		{"zero quantity", `{"productId":"BIKE","quantity":"0"}`, allDuties, http.StatusUnprocessableEntity, domain.CodeBomQty},
		{"negative quantity", `{"productId":"BIKE","quantity":"-3"}`, allDuties, http.StatusUnprocessableEntity, domain.CodeBomQty},
		{"levels above maximum", `{"productId":"BIKE","levels":26}`, allDuties, http.StatusUnprocessableEntity, domain.CodeBomLevels},
		{"impossible date", `{"productId":"BIKE","asOf":"2026-13-40"}`, allDuties, http.StatusBadRequest, `"asOf"`},
		{"missing product", `{}`, allDuties, http.StatusBadRequest, `"productId"`},
		{"missing read duty", `{"productId":"BIKE"}`, "engineering.bom.write", http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{catalog: catalog}, approves)
			rec := do(r, http.MethodPost, "/v1/engineering/boms:explode", tc.body, tc.duties, true)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
	failing, _ := newRouter(&fakeStore{err: errors.New("boom")}, approves)
	if rec := do(failing, http.MethodPost, "/v1/engineering/boms:explode", `{"productId":"BIKE"}`, allDuties, true); rec.Code != http.StatusInternalServerError {
		t.Fatalf("catalog failure = %d; want 500", rec.Code)
	}
}

func TestWhereUsed(t *testing.T) {
	catalog := store.Snapshot{
		"BIKE":  {activeVersion("b1", "BIKE", "2025-01-01", "", oneLine(1, "WHEEL", 2))},
		"WHEEL": {activeVersion("w1", "WHEEL", "2025-01-01", "", oneLine(1, "SPOKE", 36))},
	}
	r, _ := newRouter(&fakeStore{catalog: catalog}, approves)
	rec := do(r, http.MethodGet, "/v1/engineering/boms:where-used?component=SPOKE&asOf=2026-03-01", "", allDuties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"productId":"WHEEL"`) || !strings.Contains(rec.Body.String(), `"parents":[{"productId":"BIKE"`) {
		t.Fatalf("where-used = %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{"", "?component=SPOKE&asOf=2026-13-40", "?component=SPOKE&levels=0", "?component=SPOKE&levels=26", "?component=SPOKE&levels=x"} {
		if rec := do(r, http.MethodGet, "/v1/engineering/boms:where-used"+bad, "", allDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Fatalf("%q = %d %s; want 400 COM-0001", bad, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodGet, "/v1/engineering/boms:where-used?component=SPOKE", "", "", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without read duty = %d; want 403", rec.Code)
	}
}

func TestLeadTime_Fixtures(t *testing.T) {
	var fixtures []struct {
		Name          string
		Quantity      string
		HoursPerDay   string
		Operations    json.RawMessage
		ExpectedHours string
		ExpectedDays  int
	}
	raw, err := os.ReadFile("../../testdata/routing-leadtime.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	r, _ := newRouter(&fakeStore{}, approves)
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			body := fmt.Sprintf(`{"quantity":%q,"hoursPerDay":%q,"operations":%s}`, f.Quantity, f.HoursPerDay, f.Operations)
			rec := do(r, http.MethodPost, "/v1/engineering/routes:lead-time", body, allDuties, true)
			var got struct {
				Hours        decimal.Decimal
				CalendarDays int
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("lead time = %d %s", rec.Code, rec.Body)
			}
			if !got.Hours.Equal(dec(f.ExpectedHours)) || got.CalendarDays != f.ExpectedDays {
				t.Fatalf("got %s h / %d d, want %s h / %d d", got.Hours, got.CalendarDays, f.ExpectedHours, f.ExpectedDays)
			}
		})
	}
}

func TestLeadTime_Rejections(t *testing.T) {
	op := func(next int) string {
		return fmt.Sprintf(`{"no":10,"next":%d,"setup":"1","run":"1","processQty":"1"}`, next)
	}
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{"no operations", `{"operations":[],"quantity":"1","hoursPerDay":"8"}`, http.StatusBadRequest, `"operations"`},
		{"null operations", `{"quantity":"1","hoursPerDay":"8"}`, http.StatusBadRequest, `"operations"`},
		{"hours per day as string text", `{"operations":[` + op(0) + `],"quantity":"1","hoursPerDay":"eight"}`, http.StatusBadRequest, "COM-0001"},
		{"zero hours per day", `{"operations":[` + op(0) + `],"quantity":"1","hoursPerDay":"0"}`, http.StatusUnprocessableEntity, domain.CodeRouteQty},
		{"absent quantity", `{"operations":[` + op(0) + `],"hoursPerDay":"8"}`, http.StatusUnprocessableEntity, domain.CodeRouteQty},
		{"dangling next operation", `{"operations":[` + op(99) + `],"quantity":"1","hoursPerDay":"8"}`, http.StatusUnprocessableEntity, domain.CodeRouteNextMiss},
		{"cyclic operation", `{"operations":[` + op(10) + `],"quantity":"1","hoursPerDay":"8"}`, http.StatusUnprocessableEntity, domain.CodeRouteCycle},
		{"huge exponent", `{"operations":[{"no":10,"setup":"1e999999999","processQty":"1"}],"quantity":"1","hoursPerDay":"8"}`, http.StatusBadRequest, `"operations"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{}, approves)
			rec := do(r, http.MethodPost, "/v1/engineering/routes:lead-time", tc.body, allDuties, true)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestCreateEco(t *testing.T) {
	replace := `{"oldVersionId":"` + oldVersion + `","newVersionId":"` + newVersion + `"}`
	tests := []struct {
		name       string
		store      *fakeStore
		body       string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"created as submitted", &fakeStore{}, `{"number":"ECO-1","replaces":[` + replace + `]}`, allDuties, http.StatusCreated, `"state":"submitted"`},
		{"past release date accepted as draft", &fakeStore{}, `{"number":"ECO-1","releaseDate":"2020-01-01","replaces":[` + replace + `]}`, allDuties, http.StatusCreated, "2020-01-01"},
		{"no replacements", &fakeStore{}, `{"number":"ECO-1","replaces":[]}`, allDuties, http.StatusBadRequest, `"replaces"`},
		{"same old and new", &fakeStore{}, `{"number":"ECO-1","replaces":[{"oldVersionId":"` + oldVersion + `","newVersionId":"` + oldVersion + `"}]}`, allDuties, http.StatusBadRequest, `"replaces"`},
		{"bad release date", &fakeStore{}, `{"number":"ECO-1","releaseDate":"soon","replaces":[` + replace + `]}`, allDuties, http.StatusBadRequest, `"releaseDate"`},
		{"missing number", &fakeStore{}, `{"replaces":[` + replace + `]}`, allDuties, http.StatusBadRequest, `"number"`},
		{"missing bom write duty", &fakeStore{}, `{"number":"ECO-1","replaces":[` + replace + `]}`, "engineering.read", http.StatusForbidden, "AUTH-0003"},
		{"version of another tenant", &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeVersionNotFound, "missing")}, `{"number":"ECO-1","replaces":[` + replace + `]}`, allDuties, http.StatusNotFound, store.CodeVersionNotFound},
		{"store unavailable", &fakeStore{err: errors.New("boom")}, `{"number":"ECO-1","replaces":[` + replace + `]}`, allDuties, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store, approves)
			rec := do(r, http.MethodPost, ecos, tc.body, tc.duties, true)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func releasableRecord(state domain.EcoState, releaseDate time.Time) store.EcoRecord {
	draft := activeVersion(newVersion, "BIKE", "2026-07-01", "", oneLine(1, "WHEEL", 1))
	draft.Status = domain.StatusDraft
	return store.EcoRecord{
		Eco:      domain.Eco{ID: ecoID, Number: "ECO-1", State: state, ReleaseDate: releaseDate, Replaces: []domain.Replacement{{OldVersionID: oldVersion, NewVersionID: newVersion}}},
		Versions: []domain.BomVersion{activeVersion(oldVersion, "BIKE", "2025-01-01", "", oneLine(1, "WHEEL", 1)), draft},
	}
}

func release(r http.Handler, duties string, headers ...string) *httptest.ResponseRecorder {
	return do(r, http.MethodPost, ecos+"/"+ecoID+":release", "", duties, true, headers...)
}

func TestReleaseEco_ExpiresOldAndActivatesNew(t *testing.T) {
	f := &fakeStore{record: releasableRecord(domain.EcoSubmitted, time.Time{})}
	r, a := newRouter(f, approves)
	rec := release(r, allDuties, "Idempotency-Key", "k1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"released"`) || !strings.Contains(rec.Body.String(), `"decidedBy":"alice"`) {
		t.Fatalf("release = %d %s", rec.Code, rec.Body)
	}
	if len(f.released) != 1 {
		t.Fatalf("store releases = %d, want 1", len(f.released))
	}
	call := f.released[0]
	if call.prev != domain.EcoSubmitted || call.plan.Expire[0].ID != oldVersion || call.plan.Expire[0].EffectiveTo.Format("2006-01-02") != "2026-06-30" ||
		call.plan.Activate[0].ID != newVersion || call.plan.Activate[0].EffectiveFrom.Format("2006-01-02") != "2026-07-01" {
		t.Fatalf("release plan = %+v", call)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "engineering_ecos_released_total 1") {
		t.Fatalf("metrics = %s", metrics.Body)
	}
}

func TestReleaseEco_FutureDateSchedulesWithoutActivating(t *testing.T) {
	f := &fakeStore{record: releasableRecord(domain.EcoSubmitted, day("2026-09-01"))}
	r, _ := newRouter(f, approves)
	rec := release(r, allDuties)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"scheduled"`) || len(f.released[0].plan.Activate) != 0 {
		t.Fatalf("future release = %d %s %+v", rec.Code, rec.Body, f.released)
	}
}

func TestReleaseEco_Rejections(t *testing.T) {
	unavailable := approverFunc(func(context.Context, string) (domain.Decision, error) {
		return domain.Decision{}, errors.New("workflow down")
	})
	tests := []struct {
		name       string
		store      *fakeStore
		approver   domain.Approver
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"approver unavailable fails closed", &fakeStore{record: releasableRecord(domain.EcoSubmitted, time.Time{})}, unavailable, allDuties, http.StatusServiceUnavailable, domain.CodeEcoApproverUnavailable},
		{"cancelled eco", &fakeStore{record: releasableRecord(domain.EcoCancelled, time.Time{})}, approves, allDuties, http.StatusUnprocessableEntity, domain.CodeEcoNotApproved},
		{"release date in the past", &fakeStore{record: releasableRecord(domain.EcoSubmitted, day("2026-01-01"))}, approves, allDuties, http.StatusUnprocessableEntity, domain.CodeEcoDate},
		{"missing approve duty", &fakeStore{record: releasableRecord(domain.EcoSubmitted, time.Time{})}, approves, "engineering.read,engineering.bom.write", http.StatusForbidden, "AUTH-0003"},
		{"unknown eco", &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeEcoNotFound, "missing")}, approves, allDuties, http.StatusNotFound, store.CodeEcoNotFound},
		{"store unavailable", &fakeStore{err: errors.New("boom")}, approves, allDuties, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store, tc.approver)
			rec := release(r, tc.duties)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if len(tc.store.released) != 0 {
				t.Fatalf("a rejected release must not touch the store, got %+v", tc.store.released)
			}
		})
	}
}

func TestReleaseEco_ApproverUnavailable_FailsClosed(t *testing.T) {
	f := &fakeStore{record: releasableRecord(domain.EcoSubmitted, time.Time{})}
	r, a := newRouter(f, approverFunc(func(context.Context, string) (domain.Decision, error) {
		return domain.Decision{}, errors.New("no workflow at host-7.internal")
	}))
	rec := release(r, allDuties)
	if rec.Code != http.StatusServiceUnavailable || len(f.released) != 0 || strings.Contains(rec.Body.String(), "host-7") {
		t.Fatalf("an unavailable approver must block release with a 503 and no detail leak: %d %s %+v", rec.Code, rec.Body, f.released)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "engineering_ecos_failed_total 1") {
		t.Fatalf("an unavailable approver must count as a failure: %s", metrics.Body)
	}
}

func TestReleaseEco_RepeatReturnsReleasedEcoWithoutSideEffects(t *testing.T) {
	f := &fakeStore{record: releasableRecord(domain.EcoReleased, time.Time{})}
	r, _ := newRouter(f, approverFunc(func(context.Context, string) (domain.Decision, error) {
		t.Fatal("a released ECO must not be reviewed again")
		return domain.Decision{}, nil
	}))
	rec := release(r, allDuties, "Idempotency-Key", "k1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"released"`) || len(f.released) != 0 {
		t.Fatalf("repeat release = %d %s %+v; want 200 released and no store write", rec.Code, rec.Body, f.released)
	}
}

func TestReleaseEco_ApproverRejection_IsPersisted(t *testing.T) {
	f := &fakeStore{record: releasableRecord(domain.EcoSubmitted, time.Time{})}
	r, _ := newRouter(f, approverFunc(func(context.Context, string) (domain.Decision, error) { return domain.Decision{Actor: "bob"}, nil }))
	rec := release(r, allDuties)
	if rec.Code != http.StatusUnprocessableEntity || len(f.released) != 1 || f.released[0].eco.State != domain.EcoRejected || len(f.released[0].plan.Activate) != 0 {
		t.Fatalf("rejection = %d %s %+v", rec.Code, rec.Body, f.released)
	}
}

func TestReleaseEco_ActivationCycleIsRefused(t *testing.T) {
	f := &fakeStore{record: releasableRecord(domain.EcoSubmitted, time.Time{}), catalog: store.Snapshot{"WHEEL": {activeVersion("W1", "WHEEL", "2025-01-01", "", oneLine(1, "BIKE", 1))}}}
	r, _ := newRouter(f, approves)
	if rec := release(r, allDuties); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeBomCycle) || len(f.released) != 0 {
		t.Fatalf("cycle = %d %s %+v", rec.Code, rec.Body, f.released)
	}
}

func TestReleaseEco_ScheduledEcoReleasesLate(t *testing.T) {
	f := &fakeStore{record: releasableRecord(domain.EcoScheduled, day("2026-06-20"))}
	r, _ := newRouter(f, approves)
	rec := release(r, allDuties)
	if rec.Code != http.StatusOK || len(f.released) != 1 || f.released[0].plan.Activate[0].EffectiveFrom.Format("2006-01-02") != "2026-07-01" {
		t.Fatalf("late scheduled release = %d %s %+v", rec.Code, rec.Body, f.released)
	}
}

func TestReleaseEco_RejectsBadIdAndPersistFailure(t *testing.T) {
	r, _ := newRouter(&fakeStore{}, approves)
	if rec := do(r, http.MethodPost, ecos+"/not-a-uuid:release", "", allDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
		t.Fatalf("non-UUID id = %d %s", rec.Code, rec.Body)
	}
	conflicting, _ := newRouter(&fakeStore{record: releasableRecord(domain.EcoSubmitted, time.Time{}), relErr: httpx.NewProblem(http.StatusConflict, domain.CodeEcoState, "changed concurrently")}, approves)
	if rec := release(conflicting, allDuties); rec.Code != http.StatusConflict {
		t.Fatalf("concurrent change = %d %s; want 409", rec.Code, rec.Body)
	}
}

func TestProbes(t *testing.T) {
	down := &fakeStore{pingErr: errors.New("db down")}
	tests := []struct {
		name       string
		store      *fakeStore
		path       string
		wantStatus int
	}{
		{"readyz ok", &fakeStore{}, "/readyz", http.StatusOK},
		{"readyz fails closed when database is down", down, "/readyz", http.StatusServiceUnavailable},
		{"healthz stays up when database is down", down, "/healthz", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store, approves)
			rec := do(r, http.MethodGet, tc.path, "", "", false)
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusServiceUnavailable && !strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
				t.Fatalf("unexpected body %s", rec.Body)
			}
		})
	}
}

func TestMetricsCountBusinessOperations(t *testing.T) {
	f := &fakeStore{}
	r, a := newRouter(f, approves)
	do(r, http.MethodPost, versions, validBom, allDuties, true)
	f.err = errors.New("boom")
	do(r, http.MethodPost, versions, validBom, allDuties, true)
	f.err = nil
	f.catalog = store.Snapshot{"BIKE": {activeVersion("v1", "BIKE", "2025-01-01", "", oneLine(1, "WHEEL", 1))}}
	do(r, http.MethodPost, "/v1/engineering/boms:explode", `{"productId":"BIKE"}`, allDuties, true)

	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		"# TYPE engineering_bom_versions_created_total counter",
		"engineering_bom_versions_created_total 1",
		"engineering_bom_versions_failed_total 1",
		"engineering_explosions_total 1",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}

func TestExplode_DiamondBlowUpIsRefused(t *testing.T) {
	catalog := store.Snapshot{}
	for i := 0; i < domain.MaxLevels; i++ {
		next := fmt.Sprintf("P%d", i+1)
		product := fmt.Sprintf("P%d", i)
		catalog[product] = []domain.BomVersion{activeVersion("v"+product, product, "2025-01-01", "", oneLine(1, next, 1), oneLine(2, next, 1))}
	}
	r, _ := newRouter(&fakeStore{catalog: catalog}, approves)
	rec := do(r, http.MethodPost, "/v1/engineering/boms:explode", `{"productId":"P0"}`, allDuties, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeBomTooLarge) {
		t.Fatalf("diamond explosion = %d %.200s; want 422 %s", rec.Code, rec.Body, domain.CodeBomTooLarge)
	}
}

func TestSpecs_MatchRoutes(t *testing.T) {
	openapi, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	asyncapi, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newRouter(&fakeStore{}, approves)
	registered := map[string]bool{}
	_ = chi.Walk(r, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[route] = true
		if !strings.Contains(string(openapi), "\n  "+route+":") {
			t.Errorf("openapi.yaml does not document %s", route)
		}
		return nil
	})
	for _, match := range regexp.MustCompile(`(?m)^  (/\S+):$`).FindAllStringSubmatch(string(openapi), -1) {
		if !registered[match[1]] {
			t.Errorf("openapi.yaml documents %s but no route is registered", match[1])
		}
	}
	for _, typ := range []string{engevents.BomVersionActivated, engevents.BomVersionExpired, engevents.EcoSubmitted, engevents.EcoApproved, engevents.EcoReleased} {
		if !events.ValidType(typ) || !strings.Contains(string(asyncapi), typ) || !strings.Contains(string(asyncapi), events.Topic(typ)) {
			t.Errorf("asyncapi.yaml does not document %s on %s", typ, events.Topic(typ))
		}
	}
}
