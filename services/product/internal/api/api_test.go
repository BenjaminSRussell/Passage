package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/product/internal/api"
	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
	"github.com/BenjaminSRussell/passage/services/product/internal/orgclient"
	"github.com/BenjaminSRussell/passage/services/product/internal/store"
)

type fakeStore struct {
	products       map[string]domain.Product
	released       map[string]domain.ReleasedProduct
	idents         map[string]domain.Identifier
	groups         map[string]domain.DimensionGroup
	created        []domain.Product
	pingErr        error
	lastExplode    domain.ExplosionResult
	configurations map[string]domain.ConfigurationRef
	convertedTo    string
}

func newFake() *fakeStore {
	return &fakeStore{
		products:       map[string]domain.Product{},
		released:       map[string]domain.ReleasedProduct{},
		idents:         map[string]domain.Identifier{},
		groups:         map[string]domain.DimensionGroup{},
		configurations: map[string]domain.ConfigurationRef{},
	}
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }
func (f *fakeStore) CreateDimensionGroup(_ context.Context, in domain.DimensionGroup) (domain.DimensionGroup, error) {
	in.ID = "dg-1"
	f.groups[in.ID] = in
	return in, nil
}
func (f *fakeStore) GetDimensionGroup(_ context.Context, id string) (domain.DimensionGroup, error) {
	return f.groups[id], nil
}
func (f *fakeStore) CreateItemGroup(_ context.Context, in domain.ItemGroup) (domain.ItemGroup, error) {
	in.ID = "ig-1"
	return in, nil
}
func (f *fakeStore) CreateItemModelGroup(_ context.Context, in domain.ItemModelGroup) (domain.ItemModelGroup, error) {
	in.ID = "img-1"
	return in, nil
}
func (f *fakeStore) CreateProduct(_ context.Context, in domain.Product, _ string) (domain.Product, error) {
	if err := domain.ValidateProductNumber(in.ProductNumber); err != nil {
		return domain.Product{}, httpx.NewProblem(http.StatusUnprocessableEntity, err.(*domain.Error).Code, err.Error())
	}
	for _, e := range f.products {
		if e.ProductNumber == in.ProductNumber {
			pr := httpx.NewProblem(http.StatusConflict, domain.CodeNumFormat, "product number already exists")
			pr.Errors = map[string]string{"existingId": e.ID}
			return domain.Product{}, pr
		}
	}
	if in.ID == "" {
		in.ID = "p-" + in.ProductNumber
	} else if _, taken := f.products[in.ID]; taken {
		return domain.Product{}, httpx.NewProblem(http.StatusConflict, "PRD-ID-TAKEN", "product id is already in use")
	}
	in.Version = 1
	f.products[in.ID] = in
	f.created = append(f.created, in)
	return in, nil
}
func (f *fakeStore) GetProduct(_ context.Context, id string) (domain.Product, error) {
	p, ok := f.products[id]
	if !ok {
		return p, httpx.NewProblem(http.StatusNotFound, "COM-0001", "product not found")
	}
	return p, nil
}
func (f *fakeStore) UpdateProduct(_ context.Context, id string, patch domain.ProductPatch, version int) (domain.Product, error) {
	p, ok := f.products[id]
	if !ok {
		return p, httpx.NewProblem(http.StatusNotFound, "COM-0001", "product not found")
	}
	if p.Version != version {
		return p, httpx.NewProblem(http.StatusPreconditionFailed, "PRD-VERSION-CONFLICT", "product changed since it was read")
	}
	from := p.LifecycleState
	if len(domain.ApplyProductPatch(&p, patch)) > 0 {
		if err := domain.AssertLifecycleTransition(from, p.LifecycleState, ""); err != nil {
			return domain.Product{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeLifecycleIllegal, err.Error())
		}
		p.Version++
	}
	f.products[id] = p
	return p, nil
}
func (f *fakeStore) GetSavedConfiguration(_ context.Context, id, productID string) (domain.ConfigurationRef, error) {
	c, ok := f.configurations[id]
	if !ok {
		return c, httpx.NewProblem(http.StatusNotFound, "PRD-CONFIGURATION-NOT-FOUND", "saved configuration not found")
	}
	c.BelongsToProduct = c.MasterID == productID
	return c, nil
}
func (f *fakeStore) ListProducts(_ context.Context, q, after string, limit int) ([]domain.Product, error) {
	var out []domain.Product
	for _, p := range f.created {
		if p.ProductNumber > after && strings.Contains(strings.ToLower(p.ProductNumber+" "+p.Name), strings.ToLower(q)) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProductNumber < out[j].ProductNumber })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *fakeStore) ListReleasedIDs(_ context.Context, after string, limit int) ([]string, error) {
	var out []string
	for id := range f.released {
		if id > after {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *fakeStore) ExplodeVariants(_ context.Context, masterID string, selected map[string][]string, limit int) (domain.ExplosionResult, error) {
	g := f.groups["dg-1"]
	res, err := domain.Explode(nil, domain.ExplosionRequest{MasterNumber: "SHIRT", Dimensions: g.Fields, Selected: selected})
	if limit > 0 && len(res.Created) > limit {
		res.Remaining = len(res.Created) - limit
		res.Created = res.Created[:limit]
	}
	f.lastExplode = res
	return res, err
}
func (f *fakeStore) LookupVariant(_ context.Context, _ string, values map[string]string) (domain.Product, error) {
	return domain.Product{ProductNumber: "V", DimensionValues: values, VariantHash: domain.VariantHash(values)}, nil
}
func (f *fakeStore) Release(_ context.Context, productID string, defaults domain.ReleaseDefaults, itemNumber string) (domain.ReleasedProduct, error) {
	p := f.products[productID]
	rp, err := domain.ApplyReleaseProfile(p, defaults, itemNumber)
	if err != nil {
		return rp, httpx.NewProblem(http.StatusUnprocessableEntity, err.(*domain.Error).Code, err.Error())
	}
	rp.ID = "rp-1"
	f.released[rp.ID] = rp
	return rp, nil
}
func (f *fakeStore) GetReleased(_ context.Context, id string) (domain.ReleasedProduct, error) {
	return f.released[id], nil
}
func (f *fakeStore) PatchReleased(_ context.Context, id string, patch domain.ReleasedProduct) (domain.ReleasedProduct, error) {
	rp := f.released[id]
	if patch.HasFinancialTxn {
		if err := domain.AssertDimensionGroupUnlocked(true); err != nil && patch.ItemModelGroupID != "" {
			return rp, httpx.NewProblem(http.StatusConflict, domain.CodeDimGroupLocked, err.Error())
		}
		rp.HasFinancialTxn = true
	}
	rp.BlockedSales = patch.BlockedSales
	rp.LifecycleState = patch.LifecycleState
	f.released[id] = rp
	return rp, nil
}
func (f *fakeStore) PutSiteDefault(_ context.Context, releasedID string, site domain.SiteDefault) (domain.SiteDefault, error) {
	rp := f.released[releasedID]
	if rp.SiteDefaults == nil {
		rp.SiteDefaults = map[string]domain.SiteDefault{}
	}
	rp.SiteDefaults[site.SiteID] = site
	f.released[releasedID] = rp
	return site, nil
}
func (f *fakeStore) ResolveSiteDefault(_ context.Context, releasedID, siteID string) (domain.SiteDefault, error) {
	rp := f.released[releasedID]
	return domain.ResolveSiteDefault(domain.ReleaseDefaults{LegalEntityID: rp.LegalEntityID, DefaultSiteID: rp.DefaultSiteID, DefaultWarehouseID: rp.DefaultWarehouseID, SiteDefaults: rp.SiteDefaults}, siteID)
}
func (f *fakeStore) AddIdentifier(_ context.Context, productID, value, uom string, allocate bool) (domain.Identifier, error) {
	if allocate {
		value, _, _ = domain.AllocateGTIN13("0614141", 123457)
	}
	if err := domain.ValidateGTIN(value); err != nil {
		return domain.Identifier{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeGTINCheckDigit, err.Error())
	}
	idn := domain.Identifier{ProductID: productID, Value: value, UOM: uom, Type: domain.IdentifierTypeForLength(value)}
	f.idents[value] = idn
	return idn, nil
}
func (f *fakeStore) GetIdentifier(_ context.Context, value string) (domain.Identifier, error) {
	return f.idents[value], nil
}
func (f *fakeStore) SetAttributes(context.Context, string, map[string]string) error { return nil }
func (f *fakeStore) AddRelation(_ context.Context, in domain.Relation) ([]domain.Relation, error) {
	return domain.AddRelation(nil, in, domain.SeededRelationTypes(), false)
}
func (f *fakeStore) EvaluatePolicy(_ context.Context, releasedID, process string) (domain.ProcessDecision, error) {
	return domain.EvaluateProcessPolicy(process, f.released[releasedID], domain.StateActive, domain.SeededLifecycleStates()), nil
}
func (f *fakeStore) ConvertUOM(_ context.Context, _, _, to, _ string) (string, error) {
	f.convertedTo = to
	return "1", nil
}
func (f *fakeStore) Search(context.Context, string, int) ([]domain.Product, error) {
	return f.created, nil
}
func (f *fakeStore) SeedLifecycle(context.Context) error                         { return nil }
func (f *fakeStore) UpsertAttribute(context.Context, domain.AttributeType) error { return nil }
func (f *fakeStore) SetGS1Prefix(context.Context, string, int64) error           { return nil }
func (f *fakeStore) AddProductConversion(context.Context, string, domain.Conversion) error {
	return nil
}

var _ store.Store = (*fakeStore)(nil)

func newRouter(f *fakeStore) *chi.Mux {
	r := httpx.NewRouter()
	api.New(f).Register(r)
	return r
}

func do(r http.Handler, method, path, body, duties string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestCreateProductAndRejectBadNumber(t *testing.T) {
	f := newFake()
	r := newRouter(f)
	ok := do(r, http.MethodPost, "/v1/product/products", `{"productNumber":"SHIRT1","subtype":"master","name":"Shirt","baseUom":"EA"}`, "product.write")
	if ok.Code != http.StatusCreated {
		t.Fatalf("create %d %s", ok.Code, ok.Body.String())
	}
	bad := do(r, http.MethodPost, "/v1/product/products", `{"productNumber":"bad number","name":"X","baseUom":"EA"}`, "product.write")
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad number %d %s", bad.Code, bad.Body.String())
	}
}

func TestValidateGTINRoute(t *testing.T) {
	r := newRouter(newFake())
	ok := do(r, http.MethodPost, "/v1/product/gtins:validate", `{"value":"06141411234579"}`, "product.read")
	if ok.Code != 200 {
		t.Fatalf("valid %d %s", ok.Code, ok.Body.String())
	}
	bad := do(r, http.MethodPost, "/v1/product/gtins:validate", `{"value":"06141411234570"}`, "product.read")
	if bad.Code != 422 {
		t.Fatalf("invalid %d", bad.Code)
	}
}

func TestSolveRouteAcceptsAndRejects(t *testing.T) {
	r := newRouter(newFake())
	ok := do(r, http.MethodPost, "/v1/product/configuration-models/bike:solve", `{"selections":{"frame":"aluminum","fork":"rigid","wheel":"29","tire":"slick","brake":"disc","handlebar":"flat","saddle":"sport","pedal":"platform","gear":"11","size":"M","motor":"none","color":"black"}}`, "product.configure")
	if ok.Code != 200 {
		t.Fatalf("solve valid %d %s", ok.Code, ok.Body.String())
	}
	bad := do(r, http.MethodPost, "/v1/product/configuration-models/bike:solve", `{"selections":{"frame":"carbon","brake":"rim"}}`, "product.configure")
	if bad.Code != 422 {
		t.Fatalf("solve invalid %d %s", bad.Code, bad.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(bad.Body.Bytes(), &body)
	if body["feasible"] != false {
		t.Fatalf("expected infeasible body %s", bad.Body.String())
	}
}

func TestDutyRequired(t *testing.T) {
	r := newRouter(newFake())
	rec := do(r, http.MethodPost, "/v1/product/products", `{"productNumber":"A","name":"A","baseUom":"EA"}`, "product.read")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing write duty %d", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	f := newFake()
	r := newRouter(f)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != 200 {
		t.Fatalf("ready %d", rec.Code)
	}
	f.pingErr = context.DeadlineExceeded
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != 503 {
		t.Fatalf("not ready %d", rec.Code)
	}
}

type fakeChecker struct {
	sites      map[string]orgclient.Site
	warehouses map[string]orgclient.Warehouse
	err        error
}

func (c fakeChecker) Site(_ context.Context, id string) (orgclient.Site, error) {
	if c.err != nil {
		return orgclient.Site{}, c.err
	}
	s, ok := c.sites[id]
	if !ok {
		return s, orgclient.ErrNotFound
	}
	return s, nil
}

func (c fakeChecker) Warehouse(_ context.Context, id string) (orgclient.Warehouse, error) {
	if c.err != nil {
		return orgclient.Warehouse{}, c.err
	}
	w, ok := c.warehouses[id]
	if !ok {
		return w, orgclient.ErrNotFound
	}
	return w, nil
}

func orgFixture() fakeChecker {
	return fakeChecker{
		sites: map[string]orgclient.Site{
			"S1": {ID: "S1", LegalEntityID: "LE1"},
			"S2": {ID: "S2", LegalEntityID: "LE1"},
			"SX": {ID: "SX", LegalEntityID: "LE2"},
		},
		warehouses: map[string]orgclient.Warehouse{
			"W1": {ID: "W1", LegalEntityID: "LE1", SiteID: "S1"},
			"W2": {ID: "W2", LegalEntityID: "LE1", SiteID: "S2"},
			"WX": {ID: "WX", LegalEntityID: "LE2", SiteID: "S1"},
		},
	}
}

func orgRouter(f *fakeStore, c fakeChecker) *chi.Mux {
	f.products["p-1"] = domain.Product{ID: "p-1", ProductNumber: "P1", LifecycleState: domain.StateActive}
	r := httpx.NewRouter()
	api.New(f).WithOrg(c).Register(r)
	return r
}

func releaseBody(site, warehouse, siteDefaults string) string {
	return `{"productId":"p-1","legalEntityId":"LE1","itemGroupId":"FG","itemModelGroupId":"STD","defaultSiteId":"` + site + `","defaultWarehouseId":"` + warehouse + `","siteDefaults":` + siteDefaults + `}`
}

func problemCode(t *testing.T, body string) (string, string) {
	t.Helper()
	var p struct{ Code, Detail string }
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	return p.Code, p.Detail
}

func TestReleaseChecksOrgMembership(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		checker    fakeChecker
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{"valid defaults", releaseBody("S1", "W1", `{}`), orgFixture(), http.StatusCreated, "", ""},
		{"valid site default entry", releaseBody("S1", "W1", `{"S2":{"warehouseId":"W2"}}`), orgFixture(), http.StatusCreated, "", ""},
		{"unknown site", releaseBody("SZ", "W1", `{}`), orgFixture(), 422, domain.CodeReleaseSiteInvalid, "defaultSiteId"},
		{"site of another legal entity", releaseBody("SX", "W1", `{}`), orgFixture(), 422, domain.CodeReleaseSiteInvalid, "defaultSiteId"},
		{"unknown warehouse", releaseBody("S1", "WZ", `{}`), orgFixture(), 422, domain.CodeReleaseWarehouseInvalid, "defaultWarehouseId"},
		{"warehouse of another legal entity", releaseBody("S1", "WX", `{}`), orgFixture(), 422, domain.CodeReleaseWarehouseInvalid, "defaultWarehouseId"},
		{"warehouse outside the site", releaseBody("S1", "W2", `{}`), orgFixture(), 422, domain.CodeReleaseWarehouseInvalid, "defaultWarehouseId"},
		{"invalid site default entry", releaseBody("S1", "W1", `{"S2":{"warehouseId":"WX"}}`), orgFixture(), 422, domain.CodeReleaseWarehouseInvalid, "siteDefaults[S2].warehouseId"},
		{"org unavailable", releaseBody("S1", "W1", `{}`), fakeChecker{err: orgclient.ErrUnavailable}, 503, "PRD-ORG-UNAVAILABLE", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			rec := do(orgRouter(f, tc.checker), http.MethodPost, "/v1/product/releases", tc.body, "product.release")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d %s", rec.Code, rec.Body.String())
			}
			if tc.wantStatus == http.StatusCreated {
				if len(f.released) != 1 {
					t.Fatalf("released = %d, want 1", len(f.released))
				}
				return
			}
			code, detail := problemCode(t, rec.Body.String())
			if code != tc.wantCode || !strings.Contains(detail, tc.wantField) {
				t.Fatalf("code %s detail %q", code, detail)
			}
			if len(f.released) != 0 {
				t.Fatalf("nothing may be written, got %d released", len(f.released))
			}
		})
	}
}

func TestReleaseWithoutCheckerSkipsOrg(t *testing.T) {
	f := newFake()
	f.products["p-1"] = domain.Product{ID: "p-1", LifecycleState: domain.StateActive}
	rec := do(newRouter(f), http.MethodPost, "/v1/product/releases", releaseBody("ANY", "ANY", `{}`), "product.release")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
}

func TestPutSiteDefaultChecksOrgMembership(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		checker    fakeChecker
		wantStatus int
		wantCode   string
	}{
		{"valid", "/v1/product/released-products/rp-1/site-defaults/S2", `{"warehouseId":"W2"}`, orgFixture(), http.StatusOK, ""},
		{"unknown site", "/v1/product/released-products/rp-1/site-defaults/SZ", `{"warehouseId":"W2"}`, orgFixture(), 422, domain.CodeReleaseSiteInvalid},
		{"warehouse outside site", "/v1/product/released-products/rp-1/site-defaults/S1", `{"warehouseId":"W2"}`, orgFixture(), 422, domain.CodeReleaseWarehouseInvalid},
		{"org unavailable", "/v1/product/released-products/rp-1/site-defaults/S2", `{"warehouseId":"W2"}`, fakeChecker{err: errors.Join(orgclient.ErrUnavailable)}, 503, "PRD-ORG-UNAVAILABLE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.released["rp-1"] = domain.ReleasedProduct{ID: "rp-1", LegalEntityID: "LE1"}
			rec := do(orgRouter(f, tc.checker), http.MethodPut, tc.path, tc.body, "product.released.write")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d %s", rec.Code, rec.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				return
			}
			if code, _ := problemCode(t, rec.Body.String()); code != tc.wantCode {
				t.Fatalf("code %s", code)
			}
			if len(f.released["rp-1"].SiteDefaults) != 0 {
				t.Fatal("nothing may be written")
			}
		})
	}
}

func TestOrgFailuresAreCounted(t *testing.T) {
	f := newFake()
	a := api.New(f).WithOrg(fakeChecker{err: orgclient.ErrUnavailable})
	r := httpx.NewRouter()
	a.Register(r)
	f.products["p-1"] = domain.Product{ID: "p-1", LifecycleState: domain.StateActive}
	do(r, http.MethodPost, "/v1/product/releases", releaseBody("S1", "W1", `{}`), "product.release")
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "product_org_check_failed_total 1") {
		t.Fatalf("metrics: %s", rec.Body.String())
	}
}

func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_ = chi.Walk(newRouter(newFake()), func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.Contains(string(spec), "\n  "+route+":\n") { // exact path key, so /gtins is not matched by /gtins:validate
			t.Errorf("openapi.yaml does not document %s", route)
		}
		return nil
	})
}

func TestExplodePagesWithLimit(t *testing.T) {
	f := newFake()
	f.groups["dg-1"] = domain.DimensionGroup{Fields: []domain.Dimension{{Code: "size", Values: []string{"S", "M", "L"}}, {Code: "color", Values: []string{"R", "B"}}}}
	r := newRouter(f)
	rec := do(r, http.MethodPost, "/v1/product/products/m-1/variants:explode", `{"limit":4}`, "product.write")
	var body struct{ Created, Skipped, Remaining int }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || body.Created != 4 || body.Remaining != 2 {
		t.Fatalf("paged explode %d %s", rec.Code, rec.Body.String())
	}
	for _, limit := range []string{"-1", "501"} {
		if bad := do(r, http.MethodPost, "/v1/product/products/m-1/variants:explode", `{"limit":`+limit+`}`, "product.write"); bad.Code != http.StatusBadRequest {
			t.Fatalf("limit %s = %d", limit, bad.Code)
		}
	}
}

func TestListReleasedPagesByKeyset(t *testing.T) {
	f := newFake()
	for n := 1; n <= 3; n++ {
		f.released[testkit.TenantID(n)] = domain.ReleasedProduct{ID: testkit.TenantID(n)}
	}
	r := newRouter(f)
	var page struct {
		Items []struct{ ID string }
		Next  string
	}
	rec := do(r, http.MethodGet, "/v1/product/released-products?limit=2", "", "product.read")
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 2 || page.Next != testkit.TenantID(2) {
		t.Fatalf("first page %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, http.MethodGet, "/v1/product/released-products?limit=2&after="+testkit.TenantID(2), "", "product.read")
	page.Items = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Next != "" {
		t.Fatalf("last page %d %s", rec.Code, rec.Body.String())
	}
	for _, query := range []string{"limit=0", "limit=501", "limit=x", "after=not-a-uuid"} {
		if bad := do(r, http.MethodGet, "/v1/product/released-products?"+query, "", "product.read"); bad.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d", query, bad.Code)
		}
	}
	if denied := do(r, http.MethodGet, "/v1/product/released-products", "", "product.write"); denied.Code != http.StatusForbidden {
		t.Fatalf("missing read duty %d", denied.Code)
	}
}

func TestListProductsPagesByKeysetAndFiltersByText(t *testing.T) {
	f := newFake()
	r := newRouter(f)
	for _, n := range []string{"A1", "A2", "B1"} {
		body := fmt.Sprintf(`{"productNumber":%q,"name":"Item %s","baseUom":"EA"}`, n, n)
		if rec := do(r, http.MethodPost, "/v1/product/products", body, "product.write"); rec.Code != http.StatusCreated {
			t.Fatalf("create %s = %d", n, rec.Code)
		}
	}
	var page struct {
		Items      []struct{ ProductNumber string }
		NextCursor string
	}
	rec := do(r, http.MethodGet, "/v1/product/products?limit=2", "", "product.read")
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("first page %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, http.MethodGet, "/v1/product/products?limit=2&cursor="+page.NextCursor, "", "product.read")
	page.Items = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].ProductNumber != "B1" {
		t.Fatalf("last page %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "nextCursor") {
		t.Fatalf("last page must not carry a cursor: %s", rec.Body.String())
	}
	rec = do(r, http.MethodGet, "/v1/product/products?q=a2", "", "product.read")
	page.Items = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].ProductNumber != "A2" {
		t.Fatalf("search %d %s", rec.Code, rec.Body.String())
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=x", "cursor=***"} {
		if bad := do(r, http.MethodGet, "/v1/product/products?"+query, "", "product.read"); bad.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d", query, bad.Code)
		}
	}
}

func TestGetSavedConfigurationReportsWhetherItBelongsToTheProduct(t *testing.T) {
	f := newFake()
	master, other, config := testkit.TenantID(1), testkit.TenantID(2), testkit.TenantID(3)
	f.configurations[config] = domain.ConfigurationRef{ID: config, ModelID: testkit.TenantID(4), MasterID: master, Status: "valid"}
	r := newRouter(f)
	var got domain.ConfigurationRef
	rec := do(r, http.MethodGet, "/v1/product/saved-configurations/"+config+"?productId="+master, "", "product.configuration.read")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || !got.BelongsToProduct || got.MasterID != master {
		t.Fatalf("own product %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, http.MethodGet, "/v1/product/saved-configurations/"+config+"?productId="+other, "", "product.configuration.read")
	got = domain.ConfigurationRef{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.BelongsToProduct {
		t.Fatalf("other product %d %s", rec.Code, rec.Body.String())
	}
	if rec = do(r, http.MethodGet, "/v1/product/saved-configurations/"+testkit.TenantID(9)+"?productId="+master, "", "product.configuration.read"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown configuration %d", rec.Code)
	}
	if rec = do(r, http.MethodGet, "/v1/product/saved-configurations/"+config+"?productId=x", "", "product.configuration.read"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad product id %d", rec.Code)
	}
	if rec = do(r, http.MethodGet, "/v1/product/saved-configurations/"+config+"?productId="+master, "", "product.read"); rec.Code != http.StatusForbidden {
		t.Fatalf("missing duty %d", rec.Code)
	}
}

// The unit conversion without a target unit converts into the product's base
// unit and names it; product.uom.convert reaches the conversion and nothing
// else (the inventory postings consumer, #480).
func TestConvertIntoTheBaseUnitUnderTheNarrowDuty(t *testing.T) {
	f := newFake()
	id := testkit.TenantID(5)
	f.products[id] = domain.Product{ID: id, ProductNumber: "BOLT", BaseUOM: "EA"}
	r := newRouter(f)
	rec := do(r, http.MethodGet, "/v1/product/uom/convert?product="+id+"&from=BOX&qty=2", "", "product.uom.convert")
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got["to"] != "EA" || f.convertedTo != "EA" {
		t.Fatalf("base-unit conversion %d %s (converted to %q)", rec.Code, rec.Body.String(), f.convertedTo)
	}
	if rec = do(r, http.MethodGet, "/v1/product/uom/convert?from=BOX&qty=2", "", "product.uom.convert"); rec.Code != http.StatusBadRequest {
		t.Fatalf("no target and no product %d", rec.Code)
	}
	if rec = do(r, http.MethodGet, "/v1/product/uom/convert?product="+testkit.TenantID(6)+"&from=BOX&qty=2", "", "product.uom.convert"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown product %d", rec.Code)
	}
	if rec = do(r, http.MethodGet, "/v1/product/uom/convert?from=BOX&to=EA&qty=2", "", "product.read"); rec.Code != http.StatusOK || f.convertedTo != "EA" {
		t.Fatalf("explicit target under product.read %d", rec.Code)
	}
	if rec = do(r, http.MethodGet, "/v1/product/products/"+id, "", "product.uom.convert"); rec.Code != http.StatusForbidden {
		t.Fatalf("product read under product.uom.convert %d", rec.Code)
	}
}
