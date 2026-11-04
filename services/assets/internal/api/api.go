// Package api exposes the assets HTTP surface: asset register, books, the
// projected depreciation schedule, transactions and adjustments, additions,
// depreciation runs, disposal, insurance policies, index series and the
// insurance report, capex requests, physical inventory count sessions,
// transfers and splits, group assets and IFRS 16 leases with their
// remeasurement.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// Duties (privilege codes from PRD 7.37.17).
const (
	DutyView          = "AssetView"
	DutyMaintain      = "AssetMaintain"
	DutyDispose       = "AssetDispose"
	DutyLeaseMaintain = "LeaseMaintain"
	DutyDepreciate    = "AssetDepreciate"
	DutyPostJournal   = "AssetPostJournal"
	// DutyInsuranceMaintain covers insurance policies and index series (FR-37.15).
	DutyInsuranceMaintain = "AssetInsuranceMaintain"
	// DutyCapexCreate covers capital appropriation requests and their submission (FR-37.17).
	DutyCapexCreate = "CapexRequestCreate"
	// DutyCount covers physical inventory count sessions (FR-37.11).
	DutyCount = "AssetCount"
)

const (
	validationCode   = "COM-0001"
	validationDetail = "request is invalid"
	idempotencyKey   = "Idempotency-Key"
	defaultLimit     = 50
	maxLimit         = 200
)

var (
	classCode   = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	bookCode    = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	currency    = regexp.MustCompile(`^[A-Z]{3}$`)
	validConv   = map[string]bool{"None": true, "FullMonth": true, "MidMonth15": true, "HalfYear": true, "MidQuarter": true}
	validMeth   = map[string]bool{"StraightLine": true, "DecliningBalance": true, "MACRS": true}
	validTiming = map[string]bool{"arrears": true, "advance": true}
	validStatus = map[string]bool{"NotYetAcquired": true, "Open": true, "Sold": true, "Scrapped": true, "Transferred": true}
)

// API holds the store and counters used by /metrics.
type API struct {
	gl       store.GLBooks
	ledger   ledgerclient.Ledger
	glBooked atomic.Int64
	store    store.Store
	now      func() time.Time
	created  atomic.Int64
	booked   atomic.Int64
	disposed atomic.Int64
	leased   atomic.Int64
	adjusted atomic.Int64
	added    atomic.Int64
	runs     atomic.Int64
	capex    atomic.Int64
	// Slice 3: lease remeasurements and count sessions.
	remeasured atomic.Int64
	terminated atomic.Int64
	counted    atomic.Int64
	// Slice 4: capex decisions.
	decided atomic.Int64
	// Slice 5: transfers and splits, group assets.
	transferred atomic.Int64
	grouped     atomic.Int64
}

// New returns an API over s.
func New(s store.Store) *API { return &API{store: s, now: time.Now} }

// Register mounts every route on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.Group(func(r chi.Router) {
		r.Use(requireTenant)
		r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/assets", a.createAsset)
		r.With(authz.RequireDuty(DutyView)).Get("/v1/assets", a.listAssets)
		r.With(authz.RequireDuty(DutyView)).Get("/v1/assets/{id}", a.getAsset)
		r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/assets/{id}/books", a.addBook)
		r.With(authz.RequireDuty(DutyView)).Get("/v1/assets/{id}/schedule", a.schedule)
		r.With(authz.RequireDuty(DutyView)).Get("/v1/assets/{id}/transactions", a.listTransactions)
		r.With(authz.RequireDuty(DutyDispose)).Post("/v1/assets/{id}/dispose", a.dispose)
		r.With(authz.RequireDuty(DutyLeaseMaintain)).Post("/v1/leases", a.createLease)
		r.With(authz.RequireDuty(DutyLeaseMaintain)).Get("/v1/leases", a.listLeases)
		r.With(authz.RequireDuty(DutyLeaseMaintain)).Get("/v1/leases/{id}", a.getLease)
		r.With(authz.RequireDuty(DutyLeaseMaintain)).Post("/v1/leases/{id}/modify", a.modifyLease)
		r.With(authz.RequireDuty(DutyLeaseMaintain)).Post("/v1/leases/{id}/terminate", a.terminateLease)
		r.With(authz.RequireDuty(DutyPostJournal)).Post("/v1/leases/gl-postings", a.postLeasesToGL)
		r.With(authz.RequireDuty(DutyLeaseMaintain)).Get("/v1/leases/{id}/gl-postings", a.getLeasePostings)
		r.With(requireAny(DutyLeaseMaintain, DutyConfigure)).Get("/v1/leases/posting-profile", a.getLeasePostingProfile)
		r.With(authz.RequireDuty(DutyConfigure)).Put("/v1/leases/posting-profile", a.saveLeasePostingProfile)

		r.With(authz.RequireDuty(DutyPostJournal)).Post("/v1/assets/{id}/transactions", a.adjust)
		r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/assets/{id}/additions", a.addAddition)
		r.With(authz.RequireDuty(DutyView)).Get("/v1/assets/{id}/additions", a.listAdditions)
		r.With(authz.RequireDuty(DutyDepreciate)).Post("/v1/assets/depreciation-runs", a.proposeRun)
		r.With(requireAny(DutyView, DutyDepreciate)).Get("/v1/assets/depreciation-runs", a.listRuns)
		r.With(requireAny(DutyView, DutyDepreciate)).Get("/v1/assets/depreciation-runs/{id}", a.getRun)
		r.With(authz.RequireDuty(DutyPostJournal)).Post("/v1/assets/depreciation-runs/{id}/post", a.postRun)

		r.With(authz.RequireDuty(DutyInsuranceMaintain)).Put("/v1/assets/index-series/{code}", a.putIndexSeries)
		r.With(requireAny(DutyView, DutyInsuranceMaintain)).Get("/v1/assets/index-series", a.listIndexSeries)
		r.With(authz.RequireDuty(DutyInsuranceMaintain)).Post("/v1/assets/{id}/insurance-policies", a.addInsurancePolicy)
		r.With(requireAny(DutyView, DutyInsuranceMaintain)).Get("/v1/assets/{id}/insurance-policies", a.listInsurancePolicies)

		r.With(authz.RequireDuty(DutyCapexCreate)).Post("/v1/assets/capex-requests", a.createCapexRequest)
		r.With(requireAny(DutyView, DutyCapexCreate, DutyCapexApprove)).Get("/v1/assets/capex-requests", a.listCapexRequests)
		r.With(requireAny(DutyView, DutyCapexCreate, DutyCapexApprove)).Get("/v1/assets/capex-requests/{id}", a.getCapexRequest)
		r.With(authz.RequireDuty(DutyCapexCreate)).Post("/v1/assets/capex-requests/{id}/submit", a.submitCapexRequest)
		r.With(authz.RequireDuty(DutyCapexApprove)).Post("/v1/assets/capex-requests/{id}/approve", a.decideCapex(domain.DecisionApprove))
		r.With(authz.RequireDuty(DutyCapexApprove)).Post("/v1/assets/capex-requests/{id}/reject", a.decideCapex(domain.DecisionReject))
		r.With(authz.RequireDuty(DutyCapexApprove)).Post("/v1/assets/capex-requests/{id}/postpone", a.decideCapex(domain.DecisionPostpone))
		r.With(authz.RequireDuty(DutyCapexCreate)).Post("/v1/assets/capex-requests/{id}/reopen", a.decideCapex(domain.DecisionReopen))
		r.With(authz.RequireDuty(DutyCapexApprove)).Post("/v1/assets/capex-requests/{id}/release", a.decideCapex(domain.DecisionRelease))
		r.With(authz.RequireDuty(DutyCapexApprove)).Post("/v1/assets/capex-requests/{id}/close", a.decideCapex(domain.DecisionClose))

		r.With(authz.RequireDuty(DutyCount)).Post("/v1/assets/count-sessions", a.createCountSession)
		r.With(requireAny(DutyView, DutyCount)).Get("/v1/assets/count-sessions", a.listCountSessions)
		r.With(requireAny(DutyView, DutyCount)).Get("/v1/assets/count-sessions/{id}", a.getCountSession)
		r.With(requireAny(DutyView, DutyInsuranceMaintain)).Get("/v1/assets/insurance-report", a.insuranceReport)
		r.With(requireAny(DutyView, DutyDepreciate)).Get("/v1/assets/mid-quarter-test", a.midQuarterTest)

		r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/assets/{id}/transfer", a.transfer)
		r.With(authz.RequireDuty(DutyView)).Get("/v1/assets/{id}/transfers", a.listTransfers)
		r.With(authz.RequireDuty(DutyConfigure)).Post("/v1/assets/group-assets", a.createGroupAsset)
		r.With(requireAny(DutyView, DutyConfigure)).Get("/v1/assets/group-assets", a.listGroupAssets)
		r.With(requireAny(DutyView, DutyConfigure)).Get("/v1/assets/group-assets/{id}", a.getGroupAsset)
		r.With(authz.RequireDuty(DutyPostJournal)).Post("/v1/assets/gl-postings", a.postToGL)
		r.With(authz.RequireDuty(DutyView)).Get("/v1/assets/gl-reconciliation", a.glReconciliation)
		r.With(authz.RequireDuty(DutyView)).Get("/v1/assets/posting-profiles", a.listPostingProfiles)
		r.With(authz.RequireDuty(DutyConfigure)).Put("/v1/assets/posting-profiles", a.savePostingProfile)
	})
}

// ready checks the database; liveness stays on /healthz with no dependency checks.
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err)
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// Metrics serves the Prometheus text counters.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "# TYPE assets_created_total counter\nassets_created_total %d\n", a.created.Load())
		fmt.Fprintf(w, "# TYPE assets_books_added_total counter\nassets_books_added_total %d\n", a.booked.Load())
		fmt.Fprintf(w, "# TYPE assets_disposals_total counter\nassets_disposals_total %d\n", a.disposed.Load())
		fmt.Fprintf(w, "# TYPE assets_leases_recognized_total counter\nassets_leases_recognized_total %d\n", a.leased.Load())
		fmt.Fprintf(w, "# TYPE assets_adjustments_total counter\nassets_adjustments_total %d\n", a.adjusted.Load())
		fmt.Fprintf(w, "# TYPE assets_additions_total counter\nassets_additions_total %d\n", a.added.Load())
		fmt.Fprintf(w, "# TYPE assets_depreciation_runs_posted_total counter\nassets_depreciation_runs_posted_total %d\n", a.runs.Load())
		fmt.Fprintf(w, "# TYPE assets_capex_requests_total counter\nassets_capex_requests_total %d\n", a.capex.Load())
		fmt.Fprintf(w, "# TYPE assets_leases_terminated_total counter\nassets_leases_terminated_total %d\n", a.terminated.Load())
		fmt.Fprintf(w, "# TYPE assets_leases_remeasured_total counter\nassets_leases_remeasured_total %d\n", a.remeasured.Load())
		fmt.Fprintf(w, "# TYPE assets_count_sessions_total counter\nassets_count_sessions_total %d\n", a.counted.Load())
		fmt.Fprintf(w, "# TYPE assets_capex_decisions_total counter\nassets_capex_decisions_total %d\n", a.decided.Load())
		fmt.Fprintf(w, "# TYPE assets_transfers_total counter\nassets_transfers_total %d\n", a.transferred.Load())
		fmt.Fprintf(w, "# TYPE assets_group_assets_total counter\nassets_group_assets_total %d\n", a.grouped.Load())
	})
}

// requireTenant rejects a malformed tenant or legal-entity header before any
// query casts it to uuid.
func requireTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := tenant.FromContext(r.Context())
		if !ok || !ids.Valid(p.TenantID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
			return
		}
		if p.LegalEntityID != "" && !ids.Valid(p.LegalEntityID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, validationCode, "legal entity must be a single UUID"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// inScope refuses a legal entity outside the caller's scope.
func inScope(w http.ResponseWriter, r *http.Request, legalEntityID string) bool {
	p, _ := tenant.FromContext(r.Context())
	if !tenant.InLegalEntityScope(p, legalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside the caller's scope"))
		return false
	}
	return true
}

type createAssetRequest struct {
	LegalEntityID string  `json:"legalEntityId"`
	AssetNumber   string  `json:"assetNumber"`
	Name          string  `json:"name"`
	ClassCode     string  `json:"classCode"`
	Barcode       *string `json:"barcode"`
	Quantity      *string `json:"quantity"`
}

type addBookRequest struct {
	BookCode             string  `json:"bookCode"`
	Currency             string  `json:"currency"`
	Method               string  `json:"method"`
	Factor               *string `json:"factor"`
	SwitchToStraightLine bool    `json:"switchToStraightLine"`
	ServiceLifePeriods   int     `json:"serviceLifePeriods"`
	SalvageValue         *string `json:"salvageValue"`
	Convention           string  `json:"convention"`
	InServiceDate        string  `json:"inServiceDate"`
	Cost                 string  `json:"cost"`
}

type disposeRequest struct {
	Kind            string  `json:"kind"`
	Percentage      string  `json:"percentage"`
	Proceeds        *string `json:"proceeds"`
	TransactionDate *string `json:"transactionDate"`
}

type leaseRequest struct {
	LegalEntityID    string `json:"legalEntityId"`
	LeaseNumber      string `json:"leaseNumber"`
	CommencementDate string `json:"commencementDate"`
	TermMonths       int    `json:"termMonths"`
	PaymentsPerYear  int    `json:"paymentsPerYear"`
	PaymentAmount    string `json:"paymentAmount"`
	PaymentTiming    string `json:"paymentTiming"`
	IBRRate          string `json:"ibrRate"`
	RateMethod       string `json:"rateMethod"`
	Currency         string `json:"currency"`
}

type scheduleLine struct {
	Index       int    `json:"index"`
	Period      string `json:"period"`
	Amount      string `json:"amount"`
	Accumulated string `json:"accumulated"`
	NBV         string `json:"nbv"`
}

type scheduleResponse struct {
	BookID   string         `json:"bookId"`
	BookCode string         `json:"bookCode"`
	Currency string         `json:"currency"`
	Total    string         `json:"total"`
	Lines    []scheduleLine `json:"lines"`
}

func (a *API) createAsset(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req createAssetRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if n := strings.TrimSpace(req.AssetNumber); n == "" || len(n) > 40 {
		fields["assetNumber"] = "must be 1 to 40 characters"
	}
	if n := strings.TrimSpace(req.Name); n == "" || len(n) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if !classCode.MatchString(req.ClassCode) {
		fields["classCode"] = "must be upper-case letters, digits, hyphen or underscore"
	}
	var barcode *string
	if req.Barcode != nil {
		if b := strings.TrimSpace(*req.Barcode); b == "" || len(b) > 64 {
			fields["barcode"] = "must be 1 to 64 characters, or omitted"
		} else {
			barcode = &b
		}
	}
	quantity := decimal.NewFromInt(1)
	if req.Quantity != nil {
		q, err := decimal.NewFromString(*req.Quantity)
		if err != nil || !q.IsPositive() || q.Exponent() < -4 {
			fields["quantity"] = "must be a positive decimal with at most 4 places"
		} else {
			quantity = q
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	asset, err := a.store.CreateAsset(r.Context(), store.Asset{LegalEntityID: req.LegalEntityID, AssetNumber: strings.TrimSpace(req.AssetNumber),
		Name: strings.TrimSpace(req.Name), ClassCode: req.ClassCode, Barcode: barcode, Quantity: quantity}, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.created.Add(1)
	setETag(w, asset.Version)
	httpx.JSON(w, http.StatusCreated, asset)
}

// pageLimit reads ?limit= (default 50, at most 200) and ?after= (an id).
func pageLimit(r *http.Request, fields map[string]string) (int, string) {
	limit := defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLimit {
			fields["limit"] = "must be 1 to 200"
		} else {
			limit = n
		}
	}
	after := r.URL.Query().Get("after")
	if after != "" && !ids.Valid(after) {
		fields["after"] = "must be the nextCursor of the previous page"
	}
	return limit, after
}

func (a *API) listAssets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	limit, after := pageLimit(r, fields)
	status := q.Get("status")
	if status != "" && !validStatus[status] {
		fields["status"] = "is not a known asset status"
	}
	legalEntity := q.Get("legalEntityId")
	if legalEntity != "" && !ids.Valid(legalEntity) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := a.store.ListAssets(r.Context(), store.AssetFilter{LegalEntityID: legalEntity, Status: status, After: after, Limit: limit})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getAsset(w http.ResponseWriter, r *http.Request) {
	asset, ok := a.loadAsset(w, r)
	if ok {
		setETag(w, asset.Version)
		httpx.JSON(w, http.StatusOK, asset)
	}
}

func (a *API) loadAsset(w http.ResponseWriter, r *http.Request) (store.AssetDetail, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset with id "+id))
		return store.AssetDetail{}, false
	}
	asset, err := a.store.GetAsset(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return store.AssetDetail{}, false
	}
	return asset, true
}

func (a *API) addBook(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req addBookRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !bookCode.MatchString(req.BookCode) {
		fields["bookCode"] = "must be upper-case letters, digits, hyphen or underscore"
	}
	if !currency.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if !validMeth[req.Method] {
		fields["method"] = "must be StraightLine, DecliningBalance or MACRS"
	}
	if !validConv[req.Convention] {
		fields["convention"] = "must be None, FullMonth, MidMonth15, HalfYear or MidQuarter"
	}
	if req.ServiceLifePeriods < 1 || req.ServiceLifePeriods > 1200 {
		fields["serviceLifePeriods"] = "must be 1 to 1200"
	}
	inService, err := store.ParseDate(req.InServiceDate)
	if err != nil {
		fields["inServiceDate"] = "must be a YYYY-MM-DD date"
	}
	cost, costErr := decimal.NewFromString(req.Cost)
	if costErr != nil || cost.IsNegative() {
		fields["cost"] = "must be a non-negative decimal"
	}
	salvage := decimal.Zero
	if req.SalvageValue != nil {
		if salvage, err = decimal.NewFromString(*req.SalvageValue); err != nil || salvage.IsNegative() {
			fields["salvageValue"] = "must be a non-negative decimal"
		}
	}
	factor := decimal.Zero
	if req.Factor != nil {
		if factor, err = decimal.NewFromString(*req.Factor); err != nil || factor.IsNegative() {
			fields["factor"] = "must be a non-negative decimal such as 2 for 200%"
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	asset, ok := a.loadAsset(w, r)
	if !ok {
		return
	}
	minor := money.MinorUnits(req.Currency)
	book := store.Book{BookCode: req.BookCode, Currency: req.Currency, Method: req.Method, Factor: factor, SwitchToStraightLine: req.SwitchToStraightLine,
		ServiceLifePeriods: req.ServiceLifePeriods, SalvageValue: salvage.Round(minor), Convention: req.Convention, InServiceDate: inService, Cost: cost.Round(minor)}
	if _, err := domain.Schedule(book.Profile()); err != nil {
		writeDomain(w, r, err)
		return
	}
	created, err := a.store.AddBook(r.Context(), asset.ID, book, key)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	a.booked.Add(1)
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) schedule(w http.ResponseWriter, r *http.Request) {
	asset, ok := a.loadAsset(w, r)
	if !ok {
		return
	}
	bookID := r.URL.Query().Get("book")
	if !ids.Valid(bookID) {
		writeValidation(w, r, map[string]string{"book": "must be a book UUID"})
		return
	}
	book, err := a.store.GetBook(r.Context(), asset.ID, bookID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// A Sold or Scrapped book has nothing left to depreciate.
	lines := []domain.Line{}
	if book.Status == string(domain.StatusOpen) {
		if lines, err = domain.Schedule(book.Profile()); err != nil {
			writeDomain(w, r, err)
			return
		}
	}
	minor := money.MinorUnits(book.Currency)
	out := scheduleResponse{BookID: book.ID, BookCode: book.BookCode, Currency: book.Currency,
		Total: domain.TotalAmount(lines).StringFixed(minor), Lines: make([]scheduleLine, 0, len(lines))}
	for _, l := range lines {
		out.Lines = append(out.Lines, scheduleLine{Index: l.Index, Period: l.Period, Amount: l.Amount.StringFixed(minor),
			Accumulated: l.Accumulated.StringFixed(minor), NBV: l.NBV.StringFixed(minor)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listTransactions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset with id "+id))
		return
	}
	txs, err := a.store.ListTransactions(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": txs})
}

func (a *API) dispose(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req disposeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	kind := domain.DisposalKind(req.Kind)
	if kind != domain.DisposalSale && kind != domain.DisposalScrap {
		fields["kind"] = "must be sale or scrap"
	}
	pct, err := decimal.NewFromString(req.Percentage)
	if err != nil {
		fields["percentage"] = "must be a decimal"
	}
	proceeds := decimal.Zero
	if req.Proceeds != nil {
		if proceeds, err = decimal.NewFromString(*req.Proceeds); err != nil || proceeds.IsNegative() {
			fields["proceeds"] = "must be a non-negative decimal"
		}
	}
	if kind == domain.DisposalScrap && !proceeds.IsZero() {
		fields["proceeds"] = "must be 0 or omitted for a scrap"
	}
	date := store.NewDate(a.now())
	if req.TransactionDate != nil {
		if date, err = store.ParseDate(*req.TransactionDate); err != nil {
			fields["transactionDate"] = "must be a YYYY-MM-DD date"
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !pct.IsPositive() || pct.GreaterThan(decimal.NewFromInt(100)) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeInvalidDisposal, "disposal percentage must be above 0 and at most 100"))
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset with id "+id))
		return
	}
	res, err := a.store.Dispose(r.Context(), id, store.DisposalInput{Version: version, Kind: kind, Percentage: pct, Proceeds: proceeds, Date: date}, key)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	a.disposed.Add(1)
	setETag(w, res.Asset.Version)
	httpx.JSON(w, http.StatusCreated, res)
}

func (a *API) createLease(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req leaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if n := strings.TrimSpace(req.LeaseNumber); n == "" || len(n) > 40 {
		fields["leaseNumber"] = "must be 1 to 40 characters"
	}
	commencement, err := store.ParseDate(req.CommencementDate)
	if err != nil {
		fields["commencementDate"] = "must be a YYYY-MM-DD date"
	}
	if !validTiming[req.PaymentTiming] {
		fields["paymentTiming"] = "must be arrears or advance"
	}
	if !currency.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	payment, perr := decimal.NewFromString(req.PaymentAmount)
	if perr != nil || !payment.IsPositive() {
		fields["paymentAmount"] = "must be a positive decimal"
	}
	rate, rerr := decimal.NewFromString(req.IBRRate)
	if rerr != nil {
		fields["ibrRate"] = "must be a decimal such as 0.05"
	}
	if req.RateMethod == "" {
		req.RateMethod = domain.RateNominal
	}
	if req.RateMethod != domain.RateNominal && req.RateMethod != domain.RateEffective {
		fields["rateMethod"] = "must be Nominal or Effective"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	payment = payment.Round(money.MinorUnits(req.Currency))
	sched, err := domain.ComputeLease(domain.LeaseTerms{Payment: payment, PaymentsPerYear: req.PaymentsPerYear, TermMonths: req.TermMonths,
		Advance: req.PaymentTiming == "advance", AnnualRate: rate, Currency: req.Currency, RateMethod: req.RateMethod})
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	lease := store.Lease{LegalEntityID: req.LegalEntityID, LeaseNumber: strings.TrimSpace(req.LeaseNumber), CommencementDate: commencement,
		TermMonths: req.TermMonths, PaymentsPerYear: req.PaymentsPerYear, PaymentAmount: payment, PaymentTiming: req.PaymentTiming,
		IBRRate: rate, RateMethod: req.RateMethod, Currency: req.Currency, Liability: sched.Liability, ROUAsset: sched.ROUAsset, Schedule: toLeaseLines(sched.Lines)}
	created, err := a.store.CreateLease(r.Context(), lease, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.leased.Add(1)
	setETag(w, created.Version)
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listLeases(w http.ResponseWriter, r *http.Request) {
	fields := map[string]string{}
	limit, after := pageLimit(r, fields)
	legalEntity := r.URL.Query().Get("legalEntityId")
	if legalEntity != "" && !ids.Valid(legalEntity) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := a.store.ListLeases(r.Context(), store.LeaseFilter{LegalEntityID: legalEntity, After: after, Limit: limit})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getLease(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeLeaseNotFound, "no lease with id "+id))
		return
	}
	lease, err := a.store.GetLease(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, lease.Version)
	httpx.JSON(w, http.StatusOK, lease)
}

func toLeaseLines(in []domain.LeaseLine) []store.LeaseLine {
	out := make([]store.LeaseLine, 0, len(in))
	for _, l := range in {
		out = append(out, store.LeaseLine{PeriodNo: l.PeriodNo, Opening: l.Opening, Remeasurement: l.Remeasurement, Interest: l.Interest, Payment: l.Payment,
			Closing: l.Closing, ROUDepreciation: l.ROUDepreciation})
	}
	return out
}

func requireKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(idempotencyKey)
	if key == "" || len(key) > 255 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, validationCode, "Idempotency-Key header is required (max 255 characters)"))
		return "", false
	}
	return key, true
}

// setETag sets the strong ETag of a versioned resource.
func setETag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", fmt.Sprintf("%q", strconv.Itoa(version)))
}

// ifMatch reads the required If-Match version: missing is 428, malformed is 400,
// and a stale one is 412 from the store.
func ifMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.Header.Get("If-Match")
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, store.CodeVersionConflict, "If-Match with the resource's current ETag is required"))
		return 0, false
	}
	v, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(raw, "W/"), `"`))
	if err != nil || v < 1 {
		writeValidation(w, r, map[string]string{"If-Match": `must be an ETag such as "3"`})
		return 0, false
	}
	return v, true
}

func writeValidation(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, validationCode, validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

// writeDomain maps a domain rule violation to 422 with its stable code.
func writeDomain(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail))
		return
	}
	httpx.Fail(w, r, err)
}
