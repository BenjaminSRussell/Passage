package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/product/internal/api"
	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
	"github.com/BenjaminSRussell/passage/services/product/internal/store"
)

type fakeStore struct {
	products  map[string]domain.Product
	released  map[string]domain.ReleasedProduct
	idents    map[string]domain.Identifier
	groups    map[string]domain.DimensionGroup
	created   []domain.Product
	pingErr   error
	lastExplode domain.ExplosionResult
}

func newFake() *fakeStore {
	return &fakeStore{
		products: map[string]domain.Product{},
		released: map[string]domain.ReleasedProduct{},
		idents:   map[string]domain.Identifier{},
		groups:   map[string]domain.DimensionGroup{},
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
	in.ID = "p-" + in.ProductNumber
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
func (f *fakeStore) ListProducts(context.Context, int) ([]domain.Product, error) { return f.created, nil }
func (f *fakeStore) ExplodeVariants(_ context.Context, masterID string, selected map[string][]string) (domain.ExplosionResult, error) {
	g := f.groups["dg-1"]
	res, err := domain.Explode(nil, domain.ExplosionRequest{MasterNumber: "SHIRT", Dimensions: g.Fields, Selected: selected})
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
func (f *fakeStore) ConvertUOM(context.Context, string, string, string, string) (string, error) {
	return "1", nil
}
func (f *fakeStore) Search(context.Context, string, int) ([]domain.Product, error) { return f.created, nil }
func (f *fakeStore) SeedLifecycle(context.Context) error                           { return nil }
func (f *fakeStore) UpsertAttribute(context.Context, domain.AttributeType) error   { return nil }
func (f *fakeStore) SetGS1Prefix(context.Context, string, int64) error             { return nil }
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
