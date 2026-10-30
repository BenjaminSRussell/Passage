package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	backorderRuns   = "/v1/sales/backorder-runs"
	defaultPriority = 5
	maxPriorities   = 10000
)

// createBackorderRunRequest is the body of POST /v1/sales/backorder-runs
// (FR-27.17): one product at one site and warehouse, a strategy and the supply
// (the whole quantity available for the open lines, what they hold today
// included) arriving on supplyDate. Customers not in priorityClasses get
// defaultPriorityClass (5); loseThreshold defaults to 3.
type createBackorderRunRequest struct {
	LegalEntityID        string         `json:"legalEntityId"`
	ProductID            string         `json:"productId"`
	SiteID               string         `json:"siteId"`
	WarehouseID          string         `json:"warehouseId"`
	Strategy             string         `json:"strategy"`
	Supply               string         `json:"supply"`
	SupplyDate           string         `json:"supplyDate"`
	LoseThreshold        int            `json:"loseThreshold"`
	DefaultPriorityClass int            `json:"defaultPriorityClass"`
	PriorityClasses      map[string]int `json:"priorityClasses"`
}

func buildBackorderRun(req createBackorderRunRequest) (store.NewBackorderRun, map[string]string) {
	f := map[string]string{}
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "productId": req.ProductID, "siteId": req.SiteID, "warehouseId": req.WarehouseID} {
		if !ids.Valid(v) {
			f[name] = "must be a UUID"
		}
	}
	switch req.Strategy {
	case domain.StrategyWin, domain.StrategyGain, domain.StrategyRedistribute, domain.StrategyFill, domain.StrategyLose:
	default:
		f["strategy"] = "must be WIN, GAIN, REDISTRIBUTE, FILL or LOSE"
	}
	supply := parseDecimal(f, "supply", req.Supply)
	if f["supply"] == "" && supply.IsNegative() {
		f["supply"] = "must not be negative"
	}
	if _, err := time.Parse(dayLayout, req.SupplyDate); err != nil {
		f["supplyDate"] = "must be a date such as 2027-03-01"
	}
	threshold, def := req.LoseThreshold, req.DefaultPriorityClass
	if threshold == 0 {
		threshold = defaultLoseThresh
	}
	if def == 0 {
		def = defaultPriority
	}
	if threshold < 1 || threshold > 9 {
		f["loseThreshold"] = "must be 1 to 9"
	}
	if def < 1 || def > 9 {
		f["defaultPriorityClass"] = "must be 1 to 9"
	}
	if len(req.PriorityClasses) > maxPriorities {
		f["priorityClasses"] = fmt.Sprintf("must have at most %d customers", maxPriorities)
	}
	for c, v := range req.PriorityClasses {
		if !ids.Valid(c) {
			f["priorityClasses"] = "keys must be customer UUIDs"
		} else if v < 1 || v > 9 {
			f["priorityClasses"] = "classes must be 1 to 9"
		}
	}
	return store.NewBackorderRun{LegalEntityID: req.LegalEntityID, ProductID: req.ProductID, SiteID: req.SiteID, WarehouseID: req.WarehouseID,
		Strategy: req.Strategy, Supply: supply, SupplyDate: req.SupplyDate, LoseThreshold: threshold, DefaultPriority: def, Priorities: req.PriorityClasses}, f
}

// createBackorderRun reads the open lines from the store and stores the plan as
// a PREVIEW run (201); a repeat of the Idempotency-Key and body returns it (200).
// Nothing in the orders changes until the run is committed (BR-27.11).
func (a *API) createBackorderRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	var req createBackorderRunRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := buildBackorderRun(req)
	if key == "" {
		fields[idempotencyKey] = "is required"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !inScope(w, r, in.LegalEntityID) {
		return
	}
	hash, err := domain.RequestHash(req)
	if err != nil {
		a.fail(w, r, "hash request", err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	in.IdempotencyKey, in.RequestHash, in.Actor = key, hash, p.UserID
	saved, created, err := a.store.CreateBackorderRun(r.Context(), in, a.now().UTC().Truncate(time.Microsecond))
	if err != nil {
		a.fail(w, r, "create backorder run", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	setETag(w, saved.Version)
	httpx.JSON(w, status, runResponse(saved))
}

// backorderRunResponse adds the SLS-0015 warning to a run that would
// deconfirm lines.
type backorderRunResponse struct {
	store.BackorderRun
	Warning     string `json:"warning,omitempty"`
	WarningCode string `json:"warningCode,omitempty"`
}

func runResponse(run store.BackorderRun) backorderRunResponse {
	out := backorderRunResponse{BackorderRun: run}
	if run.Status == "PREVIEW" && run.DeconfirmedCount > 0 {
		out.WarningCode = codeDeconfirm
		out.Warning = fmt.Sprintf("backorder run would deconfirm %d lines", run.DeconfirmedCount)
	}
	return out
}

func runRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "backorder run not found"))
			return
		}
		next(w, r, id)
	}
}

// listBackorderRuns lists stored runs, newest first, without their lines.
func (a *API) listBackorderRuns(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.BackorderRunFilter{LegalEntityID: legalEntity, ProductID: q.Get("productId"), Status: q.Get("status")}
	fields := map[string]string{}
	if f.ProductID != "" && !ids.Valid(f.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	if f.Status != "" && f.Status != "PREVIEW" && f.Status != "COMMITTED" {
		fields["status"] = "must be PREVIEW or COMMITTED"
	}
	limit, ok := pageLimit(q.Get("limit"))
	if !ok {
		fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	f.Limit = limit
	items, err := a.store.ListBackorderRuns(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list backorder runs", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// getBackorderRun returns one run with its lines in processing order.
func (a *API) getBackorderRun(w http.ResponseWriter, r *http.Request) {
	runRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		saved, err := a.store.GetBackorderRun(r.Context(), id)
		if err != nil {
			a.fail(w, r, "get backorder run", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, runResponse(saved))
	})(w, r)
}

// commitBackorderRun applies a PREVIEW run at its ETag. An order that changed
// since the preview is 409 SLS-0042 and nothing is applied.
func (a *API) commitBackorderRun(w http.ResponseWriter, r *http.Request) {
	runRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		p, _ := tenant.FromContext(r.Context())
		saved, err := a.store.CommitBackorderRun(r.Context(), id, version, p.UserID, a.now().UTC().Truncate(time.Microsecond))
		if err != nil {
			a.fail(w, r, "commit backorder run", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, runResponse(saved))
	})(w, r)
}
