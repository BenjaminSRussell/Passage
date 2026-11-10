package api

import (
	"context"
	"encoding/base64"
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
	"github.com/google/uuid"

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
	defaultProductPage = 50
	maxProductPage     = 100
	readyTimeout       = 500 * time.Millisecond
	dutyView           = "product.read"
	dutyMaintain       = "product.write"
	dutyRelease        = "product.release"
	dutyReleasedWrite  = "product.released.write"
	// dutyImport is held by the migration loader service token; it opens only
	// product create/update, attributes, identifiers, relations, release and the
	// released-product patch; release and patch stay in its own legal entity.
	dutyImport = "product.import.write"
	// dutyImportRead lets the migration loader read back a product it loaded, by id, for reconciliation.
	dutyImportRead   = "product.import.read"
	dutySetup        = "product.setup.write"
	dutyConfigure    = "product.configure"
	dutyGTIN         = "product.gtin.allocate"
	dutyJobs         = "product.jobs.write"
	dutyExplode      = "product.variants.explode"
	dutyReleasedRead = "product.released.read"

	dutyConfigurationRead = "product.configuration.read"
	// dutyUOMConvert is the service-only duty of the unit conversion alone: the
	// inventory postings consumer converts a document quantity into the
	// product's inventory unit with it (#480) and reads nothing else.
	dutyUOMConvert = "product.uom.convert"

	codeOrgUnavailable = "PRD-ORG-UNAVAILABLE"
	// codeOrgServiceCaller: a service caller (the migration loader) placed a
	// site or warehouse, and product cannot sign an org lookup for it yet.
	codeOrgServiceCaller = "PRD-ORG-SERVICE-CALLER"
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
	r.With(authz.RequireAnyDuty(dutyMaintain, dutyImport)).Post("/v1/product/products", a.createProduct)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/products", a.listProducts)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/products:search", a.search)
	r.With(authz.RequireAnyDuty(dutyView, dutyImportRead)).Get("/v1/product/products/{id}", a.getProduct)
	r.With(authz.RequireAnyDuty(dutyMaintain, dutyImport)).Patch("/v1/product/products/{id}", a.updateProduct)
	r.With(authz.RequireAnyDuty(dutyMaintain, dutyExplode)).Post("/v1/product/products/{id}/variants:explode", a.explode)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/products/{id}/variants:lookup", a.lookup)
	r.With(authz.RequireAnyDuty(dutyMaintain, dutyImport)).Post("/v1/product/products/{id}/attributes", a.setAttributes)
	r.With(authz.RequireAnyDuty(dutyMaintain, dutyImport)).Post("/v1/product/products/{id}/identifiers", a.addIdentifier)
	r.With(authz.RequireAnyDuty(dutyMaintain, dutyImport)).Post("/v1/product/products/{id}/relations", a.addRelation)
	r.With(authz.RequireAnyDuty(dutyRelease, dutyImport)).Post("/v1/product/releases", a.release)
	r.With(authz.RequireAnyDuty(dutyReleasedWrite, dutyImport)).Patch("/v1/product/released-products/{id}", a.patchReleased)
	r.With(authz.RequireDuty(dutyReleasedWrite)).Put("/v1/product/released-products/{id}/site-defaults/{siteId}", a.putSiteDefault)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/released-products/{id}/site-defaults/{siteId}", a.getSiteDefault)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/released-products/{id}/process-policy", a.processPolicy)
	r.With(authz.RequireDuty(dutyGTIN)).Post("/v1/product/gtins", a.allocateGTIN)
	r.With(authz.RequireDuty(dutyView)).Post("/v1/product/gtins:validate", a.validateGTIN)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/product/gtins/{gtin}", a.getGTIN)
	r.With(authz.RequireAnyDuty(dutyView, dutyUOMConvert)).Get("/v1/product/uom/convert", a.convert)
	r.With(authz.RequireDuty(dutyConfigure)).Post("/v1/product/configuration-models/{id}:solve", a.solve)
	r.With(authz.RequireDuty(dutyConfigurationRead)).Get("/v1/product/saved-configurations/{id}", a.getSavedConfiguration)
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

func (a *API) getSavedConfiguration(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, productID := chi.URLParam(r, "id"), r.URL.Query().Get("productId")
	if !ids.Valid(productID) {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		p.Errors = map[string]string{"productId": "must be a UUID"}
		httpx.Fail(w, r, p)
		return
	}
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "PRD-CONFIGURATION-NOT-FOUND", "saved configuration not found"))
		return
	}
	c, err := a.store.GetSavedConfiguration(r.Context(), id, productID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
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
	// ID is a caller-chosen UUID, accepted only from a caller holding product.import.write.
	ID string `json:"id"`
}

// importScope keeps a caller admitted only by product.import.write inside its
// own legal entity: someone holding the route's ordinary duty (holdsOrdinary,
// computed by the caller with the duty constant inline so the identity
// contract scan can read it) is governed by the usual scope checks, so the
// human path is unchanged.
func importScope(w http.ResponseWriter, r *http.Request, holdsOrdinary bool, legalEntityID string) bool {
	p, _ := tenant.FromContext(r.Context())
	if holdsOrdinary || tenant.InLegalEntityScope(p, legalEntityID) {
		return true
	}
	httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity is outside the import token's scope"))
	return false
}

// importID canonicalises a caller-supplied product id: the 36-character
// hyphenated form of an RFC 9562 UUID of version 1 to 8 (so neither the nil
// nor the max UUID), lower-cased as Postgres returns it.
func importID(s string) (string, bool) {
	u, err := uuid.Parse(s)
	if err != nil || len(s) != 36 || u.Variant() != uuid.RFC4122 || u.Version() < 1 || u.Version() > 8 {
		return "", false
	}
	return u.String(), true
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
	if req.ID != "" {
		if p, _ := tenant.FromContext(r.Context()); !p.HasDuty(dutyImport) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a caller-supplied id needs duty "+dutyImport))
			return
		}
		id, ok := importID(req.ID)
		if !ok {
			p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
			p.Errors = map[string]string{"id": "must be a hyphenated RFC 9562 UUID of version 1 to 8"}
			httpx.Fail(w, r, p)
			return
		}
		req.ID = id
	}
	in := domain.Product{
		ID: req.ID, ProductNumber: strings.ToUpper(req.ProductNumber), Subtype: req.Subtype, ProductType: req.ProductType,
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
	query := r.URL.Query()
	after := ""
	if raw := query.Get("cursor"); raw != "" {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "cursor must be a nextCursor returned by a previous page"))
			return
		}
		after = string(b)
	}
	limit := defaultProductPage
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxProductPage {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", fmt.Sprintf("limit must be 1 to %d", maxProductPage)))
			return
		}
		limit = n
	}
	items, err := a.store.ListProducts(r.Context(), query.Get("q"), after, limit+1)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := map[string]any{}
	if len(items) > limit {
		items = items[:limit]
		out["nextCursor"] = base64.RawURLEncoding.EncodeToString([]byte(items[limit-1].ProductNumber))
	}
	out["items"] = items
	httpx.JSON(w, http.StatusOK, out)
}

// updateProduct patches name, description, searchName and lifecycleState under
// If-Match (the product version, as returned in the body).
func (a *API) updateProduct(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	version, err := strconv.Atoi(strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`))
	if err != nil || version < 1 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, "PRD-VERSION-REQUIRED", "If-Match with the product version is required"))
		return
	}
	if !ids.Valid(chi.URLParam(r, "id")) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "COM-0001", "product not found"))
		return
	}
	var patch domain.ProductPatch
	if err := httpx.Decode(r, &patch); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	bad := map[string]string{}
	if patch.Name != nil && strings.TrimSpace(*patch.Name) == "" {
		bad["name"] = "must not be empty"
	}
	if patch.LifecycleState != nil && !domain.ValidLifecycle(*patch.LifecycleState) {
		bad["lifecycleState"] = "must be draft, active, phase_out or obsolete"
	}
	if len(bad) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		p.Errors = bad
		httpx.Fail(w, r, p)
		return
	}
	out, err := a.store.UpdateProduct(r.Context(), chi.URLParam(r, "id"), patch, version)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getProduct(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	if !ids.Valid(chi.URLParam(r, "id")) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "COM-0001", "product not found"))
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
	if p, _ := tenant.FromContext(r.Context()); !importScope(w, r, p.HasDuty(dutyRelease), req.LegalEntityID) {
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
	if p, _ := tenant.FromContext(r.Context()); !p.HasDuty(dutyReleasedWrite) {
		cur, err := a.store.GetReleased(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if !importScope(w, r, false, cur.LegalEntityID) {
			return
		}
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
	product, to := q.Get("product"), q.Get("to")
	if to == "" {
		// Without a target unit the quantity is converted into the product's
		// base (inventory) unit, which the answer names (#480).
		if !ids.Valid(product) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "to, or a product to convert into its base unit, is required"))
			return
		}
		p, err := a.store.GetProduct(r.Context(), product)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		to = p.BaseUOM
	}
	qty, err := a.store.ConvertUOM(r.Context(), product, q.Get("from"), to, q.Get("qty"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"from": q.Get("from"), "to": to, "quantity": qty})
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
	if errors.Is(err, orgclient.ErrServiceCaller) {
		slog.Error("org membership check cannot be signed for a service caller", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		return httpx.NewProblem(http.StatusServiceUnavailable, codeOrgServiceCaller,
			"org cannot confirm sites and warehouses for a service caller until product holds a service grant to org (org.read) and a signing key; nothing is written")
	}
	slog.Warn("org membership check unavailable", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	return httpx.NewProblem(http.StatusServiceUnavailable, codeOrgUnavailable, "org service is unavailable")
}
