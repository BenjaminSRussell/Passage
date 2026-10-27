// Package api exposes the wave-39 costing computations over HTTP. The routes are
// stateless: the caller supplies the structure, prices and actuals, and the
// service returns the rolled standard, the mass-roll run, the variance
// decomposition, the activation preview, the mixed cost, component postings,
// sales order estimates and their versions, ad-hoc estimates, comparisons or
// the standard reconciliation. Nothing is stored yet.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
)

const (
	readyTimeout = 500 * time.Millisecond
	// Duties from PRD section 7.39.17.
	dutyView         = "CostingView"
	dutyMaintain     = "CostingMaintain"
	dutyCalculate    = "CostingCalculate"
	dutyActivate     = "CostingActivate"
	dutySystem       = "CostingSystem"
	validationDetail = "request failed validation"
	// MaxItems bounds one roll request (AC-39.3 sizes the mass roll at 50,000).
	MaxItems = 50000
	// MaxRevalLines bounds one activation preview.
	MaxRevalLines = 50000
)

var decimalHundred = decimal.NewFromInt(100)

// Pinger is the readiness dependency (the Postgres pool in production).
type Pinger interface {
	Ping(ctx context.Context) error
}

// Config holds the environment-driven defaults for roll options.
type Config struct {
	MaxLevels int
	Workers   int
}

// API serves the costing routes and counts what it computes for /metrics.
type API struct {
	pinger Pinger
	cfg    Config

	calculations     atomic.Int64
	massRolls        atomic.Int64
	itemsRolled      atomic.Int64
	rollErrors       atomic.Int64
	varianceComputes atomic.Int64
	previews         atomic.Int64
	estimates        atomic.Int64

	reconciliationDifferences atomic.Int64
}

// New builds the API. pinger may be nil, in which case readiness only reports the process.
func New(pinger Pinger, cfg Config) *API {
	if cfg.MaxLevels == 0 {
		cfg.MaxLevels = domain.DefaultMaxLevels
	}
	if cfg.Workers == 0 {
		cfg.Workers = domain.DefaultWorkers
	}
	return &API{pinger: pinger, cfg: cfg}
}

// route is one duty-guarded endpoint; api/openapi.yaml lists the same table.
type route struct {
	method, path, duty string
	handler            http.HandlerFunc
}

func (a *API) routes() []route {
	return []route{
		{http.MethodPost, "/v1/costing/calculations", dutyCalculate, a.rollOne},
		{http.MethodPost, "/v1/costing/rolls", dutyCalculate, a.massRoll},
		{http.MethodPost, "/v1/costing/variances/compute", dutySystem, a.computeVariance},
		{http.MethodPost, "/v1/costing/activations:preview", dutyActivate, a.previewActivation},
		// Slice 2 (commercial.go).
		{http.MethodPost, "/v1/costing/mixed-costs", dutyCalculate, a.mixedCost},
		{http.MethodPost, "/v1/costing/postings:cogs", dutyCalculate, a.cogsPosting},
		{http.MethodPost, "/v1/costing/postings:price-difference", dutyCalculate, a.priceDifferencePosting},
		{http.MethodPost, "/v1/costing/sales-order-estimates", dutyCalculate, a.salesOrderEstimate},
		{http.MethodPost, "/v1/costing/comparisons", dutyView, a.compare},
		{http.MethodPost, "/v1/costing/reconciliations", dutyView, a.reconcile},
		// Slice 3 (lifecycle.go).
		{http.MethodPost, "/v1/costing/versions:transition", dutyMaintain, a.transitionVersion},
		{http.MethodPost, "/v1/costing/versions:activate", dutyActivate, a.activateVersion},
		{http.MethodPost, "/v1/costing/conversions:validate", dutyActivate, a.validateConversion},
		{http.MethodPost, "/v1/costing/conversions:advance", dutyActivate, a.advanceConversion},
		// Slice 4 (estimates.go).
		{http.MethodPost, "/v1/costing/estimates", dutyCalculate, a.adHocEstimate},
		{http.MethodPost, "/v1/costing/sales-order-estimates:refresh", dutyCalculate, a.refreshSalesEstimate},
		{http.MethodPost, "/v1/costing/sales-order-estimates:freeze", dutyCalculate, a.freezeSalesEstimate},
	}
}

// Register installs the routes on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	for _, rt := range a.routes() {
		r.With(authz.RequireDuty(rt.duty)).Method(rt.method, rt.path, rt.handler)
	}
}

// Metrics serves the Prometheus text exposition on the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		counter := func(name, help string, v int64) {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
		}
		counter("productcost_calculations_total", "Single-item roll-ups computed.", a.calculations.Load())
		counter("productcost_mass_rolls_total", "Mass roll runs computed.", a.massRolls.Load())
		counter("productcost_items_rolled_total", "Items rolled across all runs.", a.itemsRolled.Load())
		counter("productcost_roll_errors_total", "Items that failed to roll.", a.rollErrors.Load())
		counter("productcost_variance_computes_total", "Production order variance decompositions computed.", a.varianceComputes.Load())
		counter("productcost_activation_previews_total", "Activation revaluation previews computed.", a.previews.Load())
		counter("productcost_adhoc_estimates_total", "Ad-hoc estimates computed from costing models.", a.estimates.Load())
		counter("productcost_reconciliation_differences_total", "Sites whose standard value differs from the GL balance.", a.reconciliationDifferences.Load())
	})
}

type rollRequest struct {
	Items   []domain.Item      `json:"items"`
	ItemID  string             `json:"itemId"`
	Options domain.RollOptions `json:"options"`
}

type calculationResponse struct {
	Result domain.Result `json:"result"`
	// Split is the item's cost component split (own plus rolled), so the
	// component amounts are read without summing levels.
	Split domain.Split `json:"split"`
}

type revalRequest struct {
	Currency string             `json:"currency"`
	Lines    []domain.RevalLine `json:"lines"`
}

// currencyCode is an ISO 4217 alphabetic code.
var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	if a.pinger != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		if err := a.pinger.Ping(ctx); err != nil {
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// guard checks the caller is a tenant principal. Duties are enforced by authz.RequireDuty.
func guard(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

// decode reads a JSON body and turns decode failures into 400 problems.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := httpx.Decode(r, v); err != nil {
		httpx.Fail(w, r, err)
		return false
	}
	return true
}

func validation(w http.ResponseWriter, r *http.Request, fields map[string]string) bool {
	if len(fields) == 0 {
		return true
	}
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
	return false
}

// catalog validates the item list and indexes it by id.
func catalog(items []domain.Item, fields map[string]string) domain.Catalog {
	if len(items) == 0 {
		fields["items"] = "must contain at least one item"
		return nil
	}
	if len(items) > MaxItems {
		fields["items"] = fmt.Sprintf("must contain at most %d items", MaxItems)
		return nil
	}
	cat := make(domain.Catalog, len(items))
	for i, it := range items {
		if it.ID == "" {
			fields[fmt.Sprintf("items[%d].id", i)] = "is required"
			continue
		}
		if _, dup := cat[it.ID]; dup {
			fields[fmt.Sprintf("items[%d].id", i)] = "must be unique"
			continue
		}
		if it.Kind != domain.KindPurchased && it.Kind != domain.KindManufactured {
			fields[fmt.Sprintf("items[%d].kind", i)] = "must be Purchased or Manufactured"
		}
		checkItem(i, it, fields)
		cat[it.ID] = it
	}
	for i, it := range items {
		for j, line := range it.BOM {
			if child, ok := cat[line.ChildID]; ok && line.Phantom && child.Kind != domain.KindManufactured {
				fields[fmt.Sprintf("items[%d].bom[%d].phantom", i, j)] = "a phantom line must name a Manufactured item"
			}
		}
	}
	return cat
}

// checkItem validates the numbers and node types of one item. Costs, hours
// and lot sizes must not be negative; sheet nodes must be a known type, and a
// basisRef or refs must name an earlier Basis or Total node of the same sheet.
func checkItem(i int, it domain.Item, fields map[string]string) {
	at := func(format string, args ...any) string {
		return fmt.Sprintf("items[%d].", i) + fmt.Sprintf(format, args...)
	}
	negative := func(key string, v *decimal.Decimal) {
		if v != nil && v.IsNegative() {
			fields[at("%s", key)] = "must not be negative"
		}
	}
	negative("price", it.Price)
	negative("activePrice", it.ActivePrice)
	negative("legalCost", it.LegalCost)
	negative("transferPrice", it.TransferPrice)
	negative("costPlusPct", it.CostPlusPct)
	if it.Kind != domain.KindPurchased && (it.Intercompany || it.LegalCost != nil) {
		fields[at("intercompany")] = "only a Purchased item is bought from an intercompany partner"
	}
	if it.Kind != domain.KindPurchased && (it.ProfitCenterSource || it.TransferPrice != nil || it.CostPlusPct != nil) {
		fields[at("profitCenterSource")] = "only a Purchased item is supplied by another profit center"
	}
	if it.Intercompany && it.ProfitCenterSource {
		fields[at("profitCenterSource")] = "an item is bought from an intercompany partner or from another profit center, not both"
	}
	negative("costingLot", &it.CostingLot)
	for c, v := range it.ActiveSplit {
		if v.IsNegative() {
			fields[at("activeSplit.%s", c)] = "must not be negative"
		}
	}
	if it.Kind == domain.KindPurchased && (len(it.BOM) > 0 || len(it.Operations) > 0 || len(it.Sheet) > 0) {
		fields[at("kind")] = "a Purchased item has no bom, operations or sheet"
	}
	for j, line := range it.BOM {
		if line.ChildID == "" {
			fields[at("bom[%d].childId", j)] = "is required"
		}
		if !line.Qty.IsPositive() {
			fields[at("bom[%d].qty", j)] = "must be positive"
		}
		if line.ScrapPct.IsNegative() || line.ScrapPct.GreaterThanOrEqual(decimalHundred) {
			fields[at("bom[%d].scrapPct", j)] = "must be from 0 to below 100"
		}
		if line.Phantom && line.MakeToOrder {
			fields[at("bom[%d].makeToOrder", j)] = "a phantom line cannot also be make-to-order"
		}
	}
	for j, op := range it.Operations {
		for key, v := range map[string]decimal.Decimal{"setupHours": op.SetupHours, "runHours": op.RunHours, "rate": op.Rate, "lotSize": op.LotSize} {
			if v.IsNegative() {
				fields[at("operations[%d].%s", j, key)] = "must not be negative"
			}
		}
		if op.ScrapPct.IsNegative() || op.ScrapPct.GreaterThanOrEqual(decimalHundred) {
			fields[at("operations[%d].scrapPct", j)] = "must be from 0 to below 100"
		}
		// BR-39.5: setup is spread over the costing lot, so it needs one.
		if op.SetupHours.IsPositive() && !op.LotSize.IsPositive() && !it.CostingLot.IsPositive() {
			fields[at("costingLot")] = "is required when an operation has setup hours and no lotSize"
		}
	}
	checkSheet(it.Sheet, func(j int, key string) string { return at("sheet[%d].%s", j, key) }, fields)
}

// checkSheet validates the costing sheet nodes (FR-39.2). Surcharge and Rate
// nodes charge a component; Basis and Total nodes report subtotals and charge
// none. Node ids are unique, and references point to earlier subtotal nodes.
func checkSheet(sheet []domain.SheetNode, at func(j int, key string) string, fields map[string]string) {
	subtotal := map[string]bool{}
	seen := map[string]bool{}
	for j, node := range sheet {
		if node.ID == "" {
			fields[at(j, "id")] = "is required"
		} else if seen[node.ID] {
			fields[at(j, "id")] = "must be unique within the sheet"
		}
		seen[node.ID] = true
		charges := node.Type == domain.NodeSurcharge || node.Type == domain.NodeRate
		if charges && node.Component == "" {
			fields[at(j, "component")] = "is required"
		}
		if !charges && node.Component != "" {
			fields[at(j, "component")] = "a Basis or Total node charges no component"
		}
		switch node.Type {
		case domain.NodeSurcharge:
			switch {
			case node.BasisRef != "" && len(node.Basis) > 0:
				fields[at(j, "basisRef")] = "set either basis or basisRef, not both"
			case node.BasisRef != "" && !subtotal[node.BasisRef]:
				fields[at(j, "basisRef")] = "must name an earlier Basis or Total node"
			case node.BasisRef == "" && len(node.Basis) == 0:
				fields[at(j, "basis")] = "must name at least one cost component, or set basisRef"
			}
			if node.Percentage.IsNegative() {
				fields[at(j, "percentage")] = "must not be negative"
			}
		case domain.NodeRate:
			if node.Rate.IsNegative() {
				fields[at(j, "rate")] = "must not be negative"
			}
			if node.RateUnit != "" && node.RateUnit != "Hour" && node.RateUnit != "Unit" {
				fields[at(j, "rateUnit")] = "must be Hour or Unit"
			}
		case domain.NodeBasis:
			if len(node.Basis) == 0 {
				fields[at(j, "basis")] = "must name at least one cost component"
			}
		case domain.NodeTotal:
			if len(node.Refs) == 0 {
				fields[at(j, "refs")] = "must name at least one earlier Basis or Total node"
			}
			for k, ref := range node.Refs {
				if !subtotal[ref] {
					fields[at(j, fmt.Sprintf("refs[%d]", k))] = "must name an earlier Basis or Total node"
				}
			}
		default:
			fields[at(j, "type")] = "must be Basis, Surcharge, Rate or Total"
		}
		if node.Type == domain.NodeBasis || node.Type == domain.NodeTotal {
			subtotal[node.ID] = true
		}
	}
}

// checkOrder validates an ended order before it is decomposed.
func checkOrder(o domain.Order, fields map[string]string) {
	if o.Currency != "" && !currencyCode.MatchString(o.Currency) {
		fields["currency"] = "must be an ISO 4217 code such as USD"
	}
	if !o.PlannedQty.IsPositive() {
		fields["plannedQty"] = "must be positive"
	}
	if o.RAFQty.IsNegative() {
		fields["rafQty"] = "must not be negative"
	}
	if len(o.Lines) == 0 {
		fields["lines"] = "must contain at least one line"
	}
	for i, ln := range o.Lines {
		at := func(key string) string { return fmt.Sprintf("lines[%d].%s", i, key) }
		if ln.Component == "" {
			fields[at("component")] = "is required"
		}
		if ln.Fixed && !ln.Planned {
			fields[at("fixed")] = "a fixed per-lot line must be planned"
		}
		for key, v := range map[string]decimal.Decimal{"plannedQty": ln.PlannedQty, "stdUnitCost": ln.StdUnitCost, "estPerLot": ln.EstPerLot, "actualUnitCost": ln.ActualUnitCost} {
			if v.IsNegative() {
				fields[at(key)] = "must not be negative"
			}
		}
	}
}

// withDefaults fills unset roll options from the service configuration and validates them.
func (a *API) withDefaults(opts domain.RollOptions, fields map[string]string) domain.RollOptions {
	if opts.MaxLevels == 0 {
		opts.MaxLevels = a.cfg.MaxLevels
	}
	if opts.Workers == 0 {
		opts.Workers = a.cfg.Workers
	}
	if err := opts.Validate(); err != nil {
		fields["options"] = err.Error()
	}
	return opts
}

func (a *API) rollOne(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req rollRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	cat := catalog(req.Items, fields)
	opts := a.withDefaults(req.Options, fields)
	if req.ItemID == "" {
		fields["itemId"] = "is required"
	} else if _, ok := cat[req.ItemID]; cat != nil && !ok {
		fields["itemId"] = "must be one of the supplied items"
	}
	if !validation(w, r, fields) {
		return
	}
	res, _ := domain.RollOne(cat, req.ItemID, opts)
	a.calculations.Add(1)
	a.itemsRolled.Add(1)
	if res.Status == domain.StatusError {
		a.rollErrors.Add(1)
	}
	httpx.JSON(w, http.StatusOK, calculationResponse{Result: res, Split: res.Combined()})
}

func (a *API) massRoll(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req rollRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	cat := catalog(req.Items, fields)
	opts := a.withDefaults(req.Options, fields)
	if !validation(w, r, fields) {
		return
	}
	run := domain.MassRoll(cat, opts)
	a.massRolls.Add(1)
	a.itemsRolled.Add(int64(run.ItemCount))
	a.rollErrors.Add(int64(run.ErrorCount))
	httpx.JSON(w, http.StatusOK, run)
}

func (a *API) computeVariance(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var o domain.Order
	if !decode(w, r, &o) {
		return
	}
	fields := map[string]string{}
	checkOrder(o, fields)
	if !validation(w, r, fields) {
		return
	}
	dec, err := domain.Decompose(o)
	if err != nil {
		var ce *domain.CodedError
		if errors.As(err, &ce) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, ce.Code, ce.Detail))
			return
		}
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = map[string]string{"order": err.Error()}
		httpx.Fail(w, r, p)
		return
	}
	a.varianceComputes.Add(1)
	httpx.JSON(w, http.StatusOK, dec)
}

func (a *API) previewActivation(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req revalRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	if req.Currency != "" && !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code such as USD"
	}
	switch {
	case len(req.Lines) == 0:
		fields["lines"] = "must contain at least one line"
	case len(req.Lines) > MaxRevalLines:
		fields["lines"] = fmt.Sprintf("must contain at most %d lines", MaxRevalLines)
	}
	for i, ln := range req.Lines {
		if ln.ItemID == "" {
			fields[fmt.Sprintf("lines[%d].itemId", i)] = "is required"
		}
		if ln.SiteID == "" {
			fields[fmt.Sprintf("lines[%d].siteId", i)] = "is required"
		}
		if ln.OldStd.IsNegative() {
			fields[fmt.Sprintf("lines[%d].oldStd", i)] = "must not be negative"
		}
		if ln.NewStd.IsNegative() {
			fields[fmt.Sprintf("lines[%d].newStd", i)] = "must not be negative"
		}
	}
	if !validation(w, r, fields) {
		return
	}
	a.previews.Add(1)
	httpx.JSON(w, http.StatusOK, domain.Revalue(req.Lines, req.Currency))
}
