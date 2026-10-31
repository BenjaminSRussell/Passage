package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
	"github.com/BenjaminSRussell/passage/services/product/internal/orgclient"
	"github.com/BenjaminSRussell/passage/services/product/internal/store"
	"github.com/BenjaminSRussell/passage/services/product/internal/upstream"
)

const (
	readyTimeout      = 500 * time.Millisecond
	dutyView          = "product.read"
	dutyMaintain      = "product.write"
	dutyRelease       = "product.release"
	dutyReleasedWrite = "product.released.write"
	dutySetup         = "product.setup.write"
	dutyConfigure     = "product.configure"
	dutyGTIN          = "product.gtin.allocate"
	dutyJobs          = "product.jobs.write"
	dutyExplode       = "product.variants.explode"
	dutyReleasedRead  = "product.released.read"

	codeOrgUnavailable = "PRD-ORG-UNAVAILABLE"
)

type Solver interface {
	Solve(model domain.CfgModel, selections map[string]string) (domain.CfgResult, error)
}

type localSolver struct{}

func (localSolver) Solve(model domain.CfgModel, selections map[string]string) (domain.CfgResult, error) {
	return domain.SolveConfiguration(model, selections, map[string]domain.DecisionTable{"frame_fork_wheel": domain.FrameForkWheelTable()})
}

type API struct {
	store     store.Store
	solver    Solver
	created   atomic.Int64
	exploded  atomic.Int64
	released  atomic.Int64
	failed    atomic.Int64
	org       orgclient.Checker
	orgFailed atomic.Int64
}

func New(s store.Store) *API { return &API{store: s, solver: localSolver{}} }

func (a *API) WithOrg(c orgclient.Checker) *API {
	a.org = c
	return a
}

func NewWithSolver(s store.Store, solver Solver) *API {
	a := New(s)
	if solver != nil {
		a.solver = solver
	}
	return a
}

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutySetup)).Post("/v1/product/setup/dimension-groups", a.createDimensionGroup)
	r.With(authz.RequireDuty(dutySetup)).Post("/v1/product/setup/item-groups", a.createItemGroup)
	r.With(authz.RequireDuty(dutySetup)).Post("/v1/product/setup/item-model-groups", a.createItemModelGroup)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/product/products", a.createProduct)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/products", a.listProducts)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/products:search", a.search)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/products/{id}", a.getProduct)
	r.With(authz.RequireAnyDuty(dutyMaintain, dutyExplode)).Post("/v1/product/products/{id}/variants:explode", a.explode)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/products/{id}/variants:lookup", a.lookup)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/product/products/{id}/attributes", a.setAttributes)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/product/products/{id}/identifiers", a.addIdentifier)
	r.With(authz.RequireDuty(dutyMaintain)).Post("/v1/product/products/{id}/relations", a.addRelation)
	r.With(authz.RequireDuty(dutyRelease)).Post("/v1/product/releases", a.release)
	r.With(authz.RequireDuty(dutyReleasedWrite)).Patch("/v1/product/released-products/{id}", a.patchReleased)
	r.With(authz.RequireDuty(dutyReleasedWrite)).Put("/v1/product/released-products/{id}/site-defaults/{siteId}", a.putSiteDefault)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/released-products/{id}/site-defaults/{siteId}", a.getSiteDefault)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/released-products/{id}/process-policy", a.processPolicy)
	r.With(authz.RequireDuty(dutyGTIN)).Post("/v1/product/gtins", a.allocateGTIN)
	r.With(authz.RequireDuty(dutyView)).Post("/v1/product/gtins:validate", a.validateGTIN)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/gtins/{gtin}", a.getGTIN)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/uom/convert", a.convert)
	r.With(authz.RequireDuty(dutyConfigure)).Post("/v1/product/configuration-models/{id}:solve", a.solve)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/barcodes/{gtin}", a.barcode)
	r.With(authz.RequireDuty(dutyJobs)).Post("/v1/product/jobs/group-change", a.groupChange)
	r.With(authz.RequireAnyDuty(dutyView, dutyReleasedRead)).Get("/v1/product/released-products", a.listReleased)
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		tenant.WriteReplayMetrics(w)
		_, _ = fmt.Fprintf(w, "# HELP product_created_total Products created.\n# TYPE product_created_total counter\nproduct_created_total %d\n", a.created.Load())
		_, _ = fmt.Fprintf(w, "# HELP product_exploded_total Variant explosion jobs.\n# TYPE product_exploded_total counter\nproduct_exploded_total %d\n", a.exploded.Load())
		_, _ = fmt.Fprintf(w, "# HELP product_released_total Products released.\n# TYPE product_released_total counter\nproduct_released_total %d\n", a.released.Load())
		_, _ = fmt.Fprintf(w, "# HELP product_failed_total Failed mutating requests.\n# TYPE product_failed_total counter\nproduct_failed_total %d\n", a.failed.Load())
		_, _ = fmt.Fprintf(w, "# HELP product_org_check_failed_total Release or site-default writes rejected by the org membership check.\n# TYPE product_org_check_failed_total counter\nproduct_org_check_failed_total %d\n", a.orgFailed.Load())
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

type dimensionGroupRequest struct {
	Code   string             `json:"code"`
	Name   string             `json:"name"`
	Kind   string             `json:"kind"`
	Fields []domain.Dimension `json:"fields"`
}

func (a *API) createDimensionGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req dimensionGroupRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	g, err := a.store.CreateDimensionGroup(r.Context(), domain.DimensionGroup{Code: strings.ToUpper(req.Code), Name: req.Name, Kind: req.Kind, Fields: req.Fields})
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, g)
}

type itemGroupRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func (a *API) createItemGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req itemGroupRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	g, err := a.store.CreateItemGroup(r.Context(), domain.ItemGroup{Code: strings.ToUpper(req.Code), Name: req.Name})
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, g)
}

type itemModelGroupRequest struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	CostingMethod string `json:"costingMethod"`
	Stocked       *bool  `json:"stocked"`
}

func (a *API) createItemModelGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req itemModelGroupRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := domain.ItemModelGroup{Code: strings.ToUpper(req.Code), Name: req.Name, CostingMethod: req.CostingMethod, Stocked: true, PostPhysical: true, PostFinancial: true}
	if req.Stocked != nil {
		in.Stocked = *req.Stocked
	}
	g, err := a.store.CreateItemModelGroup(r.Context(), in)
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, g)
}

type createProductRequest struct {
	ProductNumber    string                    `json:"productNumber"`
	Subtype          string                    `json:"subtype"`
	ProductType      string                    `json:"productType"`
	Name             string                    `json:"name"`
	Description      string                    `json:"description"`
	SearchName       string                    `json:"searchName"`
	BaseUOM          string                    `json:"baseUom"`
	DimensionGroupID string                    `json:"dimensionGroupId"`
	LifecycleState   string                    `json:"lifecycleState"`
	Service          *domain.ServiceAttributes `json:"service"`
}

func (a *API) createProduct(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createProductRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := domain.Product{
		ProductNumber: strings.ToUpper(req.ProductNumber), Subtype: req.Subtype, ProductType: req.ProductType,
		Name: req.Name, Description: req.Description, SearchName: req.SearchName, BaseUOM: strings.ToUpper(req.BaseUOM),
		DimensionGroupID: req.DimensionGroupID, LifecycleState: req.LifecycleState, Service: req.Service,
	}
	if in.Subtype == "" {
		in.Subtype = domain.SubtypeProduct
	}
	if in.ProductType == "" {
		in.ProductType = domain.TypeItem
	}
	if in.ProductType == domain.TypeService && in.ProductNumber == "" && in.Service != nil {
		in.ProductNumber = domain.ServiceNumber(in.Service.CategoryCode, 1)
	}
	p, err := a.store.CreateProduct(r.Context(), in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	a.created.Add(1)
	httpx.JSON(w, http.StatusCreated, p)
}

func (a *API) listProducts(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListProducts(r.Context(), 100)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getProduct(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, err := a.store.GetProduct(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

type explodeRequest struct {
	Selected map[string][]string `json:"selected"`
	Limit    int                 `json:"limit"`
}

func (a *API) explode(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req explodeRequest
	if r.ContentLength > 0 {
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	if req.Limit < 0 || req.Limit > store.MaxPage {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "limit must be 0 to 500"))
		return
	}
	res, err := a.store.ExplodeVariants(r.Context(), chi.URLParam(r, "id"), req.Selected, req.Limit)
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	a.exploded.Add(1)
	httpx.JSON(w, http.StatusOK, map[string]any{"created": len(res.Created), "skipped": len(res.Skipped), "remaining": res.Remaining, "variants": res.Created})
}

func (a *API) lookup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	values := map[string]string{}
	for k, vs := range r.URL.Query() {
		if len(vs) > 0 {
			values[k] = vs[0]
		}
	}
	p, err := a.store.LookupVariant(r.Context(), chi.URLParam(r, "id"), values)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (a *API) setAttributes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var values map[string]string
	if err := httpx.Decode(r, &values); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.store.SetAttributes(r.Context(), chi.URLParam(r, "id"), values); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type identifierRequest struct {
	Value    string `json:"value"`
	UOM      string `json:"uom"`
	Allocate bool   `json:"allocate"`
}

func (a *API) addIdentifier(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req identifierRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	idn, err := a.store.AddIdentifier(r.Context(), chi.URLParam(r, "id"), req.Value, req.UOM, req.Allocate)
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, idn)
}

func (a *API) addRelation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var in domain.Relation
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in.FromProductID = chi.URLParam(r, "id")
	out, err := a.store.AddRelation(r.Context(), in)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"items": out})
}

type releaseRequest struct {
	ProductID          string                        `json:"productId"`
	LegalEntityID      string                        `json:"legalEntityId"`
	ItemNumber         string                        `json:"itemNumber"`
	ItemGroupID        string                        `json:"itemGroupId"`
	ItemModelGroupID   string                        `json:"itemModelGroupId"`
	DefaultSiteID      string                        `json:"defaultSiteId"`
	DefaultWarehouseID string                        `json:"defaultWarehouseId"`
	SiteDefaults       map[string]domain.SiteDefault `json:"siteDefaults"`
}

func (a *API) release(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req releaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	placements := []placement{{"defaultSiteId", req.DefaultSiteID, "defaultWarehouseId", req.DefaultWarehouseID}}
	sites := make([]string, 0, len(req.SiteDefaults))
	for key := range req.SiteDefaults {
		sites = append(sites, key)
	}
	sort.Strings(sites)
	for _, key := range sites {
		entry := req.SiteDefaults[key]
		siteID := entry.SiteID
		if siteID == "" {
			siteID = key
		}
		prefix := "siteDefaults[" + key + "]."
		placements = append(placements, placement{prefix + "siteId", siteID, prefix + "warehouseId", entry.WarehouseID})
	}
	if !a.checkOrg(w, r, req.LegalEntityID, placements...) {
		return
	}
	rp, err := a.store.Release(r.Context(), req.ProductID, domain.ReleaseDefaults{
		LegalEntityID: req.LegalEntityID, ItemGroupID: req.ItemGroupID, ItemModelGroupID: req.ItemModelGroupID,
		DefaultSiteID: req.DefaultSiteID, DefaultWarehouseID: req.DefaultWarehouseID, SiteDefaults: req.SiteDefaults,
	}, req.ItemNumber)
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	a.released.Add(1)
	httpx.JSON(w, http.StatusCreated, rp)
}

func (a *API) patchReleased(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.ReleasedProduct
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rp, err := a.store.PatchReleased(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rp)
}

func (a *API) putSiteDefault(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.SiteDefault
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.SiteID = chi.URLParam(r, "siteId")
	if a.org != nil {
		rp, err := a.store.GetReleased(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if !a.checkOrg(w, r, rp.LegalEntityID, placement{"siteId", req.SiteID, "warehouseId", req.WarehouseID}) {
			return
		}
	}
	got, err := a.store.PutSiteDefault(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, got)
}

func (a *API) getSiteDefault(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	got, err := a.store.ResolveSiteDefault(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "siteId"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, got)
}

func (a *API) processPolicy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	dec, err := a.store.EvaluatePolicy(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("process"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if dec.Policy == "block" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, dec.Code, dec.Detail))
		return
	}
	httpx.JSON(w, status, dec)
}

func (a *API) allocateGTIN(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req identifierRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	idn, err := a.store.AddIdentifier(r.Context(), r.URL.Query().Get("productId"), req.Value, req.UOM, true)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, idn)
}

func (a *API) validateGTIN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value string `json:"value"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := domain.ValidateGTIN(req.Value); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeGTINCheckDigit, err.Error()))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"value": req.Value, "valid": true, "type": domain.IdentifierTypeForLength(req.Value)})
}

func (a *API) getGTIN(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idn, err := a.store.GetIdentifier(r.Context(), chi.URLParam(r, "gtin"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, idn)
}

func (a *API) convert(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	qty, err := a.store.ConvertUOM(r.Context(), q.Get("product"), q.Get("from"), q.Get("to"), q.Get("qty"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"from": q.Get("from"), "to": q.Get("to"), "quantity": qty})
}

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.Search(r.Context(), r.URL.Query().Get("q"), 10)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) solve(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req struct {
		Selections map[string]string `json:"selections"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	res, err := a.solver.Solve(domain.BicycleModel(), req.Selections)
	if err != nil {
		if res.IIS != nil {
			httpx.JSON(w, http.StatusUnprocessableEntity, map[string]any{"code": domain.CodeCfgInfeasible, "feasible": false, "iis": res.IIS})
			return
		}
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCfgInfeasible, err.Error()))
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) barcode(w http.ResponseWriter, r *http.Request) {
	svg, err := domain.RenderEAN13SVG(chi.URLParam(r, "gtin"))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeGTINCheckDigit, err.Error()))
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(svg))
}

func (a *API) groupChange(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req struct {
		Items []domain.GroupChangeItem `json:"items"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": domain.RunGroupChange(req.Items, nil)})
}

func (a *API) listReleased(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	after := r.URL.Query().Get("after")
	if after != "" && !ids.Valid(after) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "after must be a UUID"))
		return
	}
	limit := store.MaxPage
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPage {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "limit must be 1 to 500"))
			return
		}
		limit = n
	}
	found, err := a.store.ListReleasedIDs(r.Context(), after, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]map[string]string, len(found))
	for i, id := range found {
		items[i] = map[string]string{"id": id}
	}
	next := ""
	if len(found) == limit {
		next = found[len(found)-1]
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "next": next})
}

type placement struct {
	siteField, siteID, warehouseField, warehouseID string
}

func (a *API) checkOrg(w http.ResponseWriter, r *http.Request, legalEntityID string, placements ...placement) bool {
	if a.org == nil {
		return true
	}
	ctx := upstream.WithAuthorization(r.Context(), r.Header.Get("Authorization"))
	for _, p := range placements {
		if err := a.checkPlacement(ctx, legalEntityID, p); err != nil {
			a.failed.Add(1)
			a.orgFailed.Add(1)
			httpx.Fail(w, r, orgProblem(r, err))
			return false
		}
	}
	return true
}

func (a *API) checkPlacement(ctx context.Context, legalEntityID string, p placement) error {
	if p.siteID != "" {
		ref, err := lookup(func() (*domain.OrgRef, error) {
			s, err := a.org.Site(ctx, p.siteID)
			return &domain.OrgRef{ID: s.ID, LegalEntityID: s.LegalEntityID}, err
		})
		if err != nil {
			return err
		}
		if err := domain.CheckSite(p.siteField, legalEntityID, ref); err != nil {
			return err
		}
	}
	if p.warehouseID != "" {
		ref, err := lookup(func() (*domain.OrgRef, error) {
			wh, err := a.org.Warehouse(ctx, p.warehouseID)
			return &domain.OrgRef{ID: wh.ID, LegalEntityID: wh.LegalEntityID, SiteID: wh.SiteID}, err
		})
		if err != nil {
			return err
		}
		return domain.CheckWarehouse(p.warehouseField, legalEntityID, p.siteID, ref)
	}
	return nil
}

func lookup(fetch func() (*domain.OrgRef, error)) (*domain.OrgRef, error) {
	ref, err := fetch()
	switch {
	case err == nil:
		return ref, nil
	case errors.Is(err, orgclient.ErrNotFound):
		return nil, nil
	}
	return nil, err
}

func orgProblem(r *http.Request, err error) *httpx.Problem {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	slog.Warn("org membership check unavailable", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	return httpx.NewProblem(http.StatusServiceUnavailable, codeOrgUnavailable, "org service is unavailable")
}
