// Package api exposes the costing HTTP surface: settlement, ledger and aging
// previews, costing versions with item-cost import and activation, valuation
// setup, aging bucket sets and snapshots, the cost transaction projection with
// the inventory value report and its GL tie-out, manual cost adjustments,
// stored reconciliation runs, and the internal PPV hook.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

const (
	readyTimeout      = 500 * time.Millisecond
	idempotencyKey    = "Idempotency-Key"
	dutyRead          = "costing.read"
	dutyVersionWrite  = "costing.version.write"
	dutyPPVPost       = "costing.ppv.post"
	validationDetail  = "request failed validation"
	maxImportItems    = 50000
	maxTxns           = 20000
	maxIdempotencyKey = 255
	maxValuationType  = 64
	maxCSVBytes       = 8 << 20
	maxPPVDocumentID  = 80
	maxPPVLineID      = 64
)

var (
	codePattern     = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,31}$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	versionTypes    = map[string]bool{"STANDARD": true, "PLANNED": true}
	ppvKinds        = map[string]bool{"RECEIPT": true, "INVOICE": true}
	fallbacks       = map[string]bool{"": true, "RUNNING_AVG": true, "STANDARD": true, "LAST_RECEIPT": true}
	txnKinds        = map[string]domain.Kind{"RECEIPT": domain.Receipt, "ISSUE": domain.Issue}
)

// API holds the handlers and their counters.
type API struct {
	store               store.Store
	versionsCreated     atomic.Int64
	itemsImported       atomic.Int64
	previews            atomic.Int64
	ppvCalculated       atomic.Int64
	domainRefusals      atomic.Int64
	activations         atomic.Int64
	costsActivated      atomic.Int64
	itemsCopied         atomic.Int64
	agingSnapshots      atomic.Int64
	costTxnsRecorded    atomic.Int64
	adjustmentsPosted   atomic.Int64
	reconciliations     atomic.Int64
	reconciliationDiffs atomic.Int64
	vouchersPosted      atomic.Int64
	closesCompleted     atomic.Int64
	ledger              ledgerclient.Ledger
}

// New returns an API over s.
func New(s store.Store) *API { return &API{store: s} }

// Register mounts every route on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/costing/settlements:preview", a.previewSettlements)
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/costing/valuation-ledgers:preview", a.previewValuationLedgers)
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/costing/aging:preview", a.previewAging)
	r.With(authz.RequireDuty(dutyVersionWrite)).Post("/v1/costing/versions", a.createVersion)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/versions", a.listVersions)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/versions/{id}", a.getVersion)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/versions/{id}/item-costs", a.listItemCosts)
	r.With(authz.RequireDuty(dutyVersionWrite)).Post("/v1/costing/versions/{id}/item-costs:import", a.importItemCosts)
	r.With(authz.RequireDuty(dutyVersionWrite)).Post("/v1/costing/versions/{id}/item-costs:copy", a.copyItemCosts)
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/costing/versions/{id}:preview-activation", a.previewActivation)
	r.With(authz.RequireDuty(dutyVersionActivate)).Post("/v1/costing/versions/{id}:activate", a.activateVersion)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/products/{id}/cost", a.currentCost)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/valuation-ledgers", a.listValuationLedgers)
	r.With(authz.RequireDuty(dutyValuationWrite)).Post("/v1/costing/valuation-ledgers", a.createValuationLedger)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/valuation-categories", a.listValuationCategories)
	r.With(authz.RequireDuty(dutyValuationWrite)).Post("/v1/costing/valuation-categories", a.createValuationCategory)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/aging/bucket-sets", a.listBucketSets)
	r.With(authz.RequireDuty(dutyAgingWrite)).Post("/v1/costing/aging/bucket-sets", a.createBucketSet)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/aging/snapshots", a.listAgingSnapshots)
	r.With(authz.RequireDuty(dutyAgingWrite)).Post("/v1/costing/aging/snapshots", a.createAgingSnapshot)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/aging/snapshots/{id}", a.getAgingSnapshot)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/value-report", a.getValueReport)
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/costing/value-report:tie-out", a.tieOutValueReport)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/cost-transactions", a.listCostTransactions)
	r.With(authz.RequireDuty(dutyCostTxnPost)).Post("/internal/v1/costing/cost-transactions", a.recordCostTransactions)
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/costing/adjustments:preview", a.previewAdjustment)
	r.With(authz.RequireDuty(dutyCostAdjust)).Post("/v1/costing/adjustments", a.createAdjustment)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/adjustments", a.listAdjustments)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/adjustments/{id}", a.getAdjustment)
	r.With(authz.RequireDuty(dutyReconcile)).Post("/v1/costing/reconciliations", a.createReconciliation)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/reconciliations", a.listReconciliations)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/reconciliations/{id}", a.getReconciliation)
	r.With(authz.RequireDuty(dutyPPVPost)).Post("/internal/v1/costing/ppv", a.postPPV)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/posting-profiles", a.getPostingProfile)
	r.With(authz.RequireDuty(dutyPostingProfileWrite)).Post("/v1/costing/posting-profiles", a.savePostingProfile)
	r.With(authz.RequireDuty(dutyGLPost)).Post("/v1/costing/gl-postings", a.postGL)
	r.With(authz.RequireDuty(dutyCloseRun)).Post("/v1/costing/closes", a.runClose)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/closes", a.listCloses)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/costing/value-report:ledger-tie-out", a.ledgerTieOut)
}

// Metrics serves Prometheus text for the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP costing_versions_created_total Costing versions created; idempotent replays are not counted.\n# TYPE costing_versions_created_total counter\ncosting_versions_created_total %d\n", a.versionsCreated.Load())
		fmt.Fprintf(w, "# HELP costing_item_costs_imported_total Item costs written by imports.\n# TYPE costing_item_costs_imported_total counter\ncosting_item_costs_imported_total %d\n", a.itemsImported.Load())
		fmt.Fprintf(w, "# HELP costing_settlement_previews_total Settlement previews computed.\n# TYPE costing_settlement_previews_total counter\ncosting_settlement_previews_total %d\n", a.previews.Load())
		fmt.Fprintf(w, "# HELP costing_versions_activated_total Costing versions activated (committed, not previews).\n# TYPE costing_versions_activated_total counter\ncosting_versions_activated_total %d\n", a.activations.Load())
		fmt.Fprintf(w, "# HELP costing_item_costs_activated_total Item costs made ACTIVE by activations.\n# TYPE costing_item_costs_activated_total counter\ncosting_item_costs_activated_total %d\n", a.costsActivated.Load())
		fmt.Fprintf(w, "# HELP costing_item_costs_copied_total Item costs written by copies.\n# TYPE costing_item_costs_copied_total counter\ncosting_item_costs_copied_total %d\n", a.itemsCopied.Load())
		fmt.Fprintf(w, "# HELP costing_ppv_calculated_total PPV amounts calculated.\n# TYPE costing_ppv_calculated_total counter\ncosting_ppv_calculated_total %d\n", a.ppvCalculated.Load())
		fmt.Fprintf(w, "# HELP costing_domain_refusals_total Requests refused by a domain rule.\n# TYPE costing_domain_refusals_total counter\ncosting_domain_refusals_total %d\n", a.domainRefusals.Load())
		fmt.Fprintf(w, "# HELP costing_aging_snapshots_total Aging snapshots stored; idempotent replays are not counted.\n# TYPE costing_aging_snapshots_total counter\ncosting_aging_snapshots_total %d\n", a.agingSnapshots.Load())
		fmt.Fprintf(w, "# HELP costing_cost_transactions_recorded_total Cost transactions recorded; replayed source keys are not counted.\n# TYPE costing_cost_transactions_recorded_total counter\ncosting_cost_transactions_recorded_total %d\n", a.costTxnsRecorded.Load())
		fmt.Fprintf(w, "# HELP costing_adjustments_posted_total Manual cost adjustments posted; idempotent replays are not counted.\n# TYPE costing_adjustments_posted_total counter\ncosting_adjustments_posted_total %d\n", a.adjustmentsPosted.Load())
		fmt.Fprintf(w, "# HELP costing_reconciliations_total Reconciliation runs stored; idempotent replays are not counted.\n# TYPE costing_reconciliations_total counter\ncosting_reconciliations_total %d\n", a.reconciliations.Load())
		fmt.Fprintf(w, "# HELP costing_reconciliation_diffs_total Differing groups of stored reconciliation runs (one reconciliation.diff event each).\n# TYPE costing_reconciliation_diffs_total counter\ncosting_reconciliation_diffs_total %d\n", a.reconciliationDiffs.Load())
		fmt.Fprintf(w, "# HELP costing_gl_vouchers_posted_total PPV vouchers confirmed by the ledger.\n# TYPE costing_gl_vouchers_posted_total counter\ncosting_gl_vouchers_posted_total %d\n", a.vouchersPosted.Load())
		fmt.Fprintf(w, "# HELP costing_closes_completed_total Inventory closes completed.\n# TYPE costing_closes_completed_total counter\ncosting_closes_completed_total %d\n", a.closesCompleted.Load())
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

// validTenant mirrors the org service: a principal with a well-formed tenant.
func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

func (a *API) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		a.domainRefusals.Add(1)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail))
		return
	}
	// A problem below 500 is the caller's to fix (not found, conflict, stale
	// version); httpx already logs the request with its status.
	if p := (*httpx.Problem)(nil); errors.As(err, &p) && p.Status < http.StatusInternalServerError {
		httpx.Fail(w, r, err)
		return
	}
	slog.Error(op+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	httpx.Fail(w, r, err)
}

// ---- settlement preview ----

// transactionDTO: valuationType names the split valuation type (FR-20.16);
// markedReceiptId, on an ISSUE, names the receipt to settle against first (FR-20.8).
type transactionDTO struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	Qty             string  `json:"qty"`
	Value           *string `json:"value"`
	FinancialDate   string  `json:"financialDate"`
	PhysicalDate    *string `json:"physicalDate"`
	ValuationType   string  `json:"valuationType,omitempty"`
	MarkedReceiptID string  `json:"markedReceiptId,omitempty"`
}

type settlePreviewRequest struct {
	Method               string           `json:"method"`
	Currency             string           `json:"currency"`
	Fallback             string           `json:"fallback"`
	StandardUnitCost     *string          `json:"standardUnitCost"`
	DefaultValuationType string           `json:"defaultValuationType,omitempty"`
	Transactions         []transactionDTO `json:"transactions"`
}

type settlementDTO struct {
	Seq       int    `json:"seq"`
	IssueID   string `json:"issueId"`
	ReceiptID string `json:"receiptId,omitempty"`
	Pool      string `json:"pool,omitempty"`
	Qty       string `json:"qty"`
	Value     string `json:"value"`
}

type exceptionDTO struct {
	IssueID  string `json:"issueId"`
	Qty      string `json:"qty"`
	Value    string `json:"value"`
	Fallback string `json:"fallback"`
}

// settlePreviewResponse: remainingByValuationType is set when stock is split by valuation type (FR-20.16).
type settlePreviewResponse struct {
	Method          string            `json:"method"`
	Currency        string            `json:"currency"`
	IssueCosts      map[string]string `json:"issueCosts"`
	Settlements     []settlementDTO   `json:"settlements"`
	Exceptions      []exceptionDTO    `json:"exceptions"`
	RemainingQty    string            `json:"remainingQty"`
	RemainingValue  string            `json:"remainingValue"`
	RemainingByType map[string]string `json:"remainingByValuationType,omitempty"`
}

// parseDecimal reads a decimal string, recording a field error when it is not
// one (or is negative when nonNegative is set). Money never passes through float.
func parseDecimal(fields map[string]string, field, raw string, nonNegative bool) decimal.Decimal {
	d, err := decimal.NewFromString(raw)
	switch {
	case err != nil && nonNegative:
		fields[field] = "must be a non-negative decimal string"
	case err != nil:
		fields[field] = "must be a decimal string"
	case nonNegative && d.IsNegative():
		fields[field] = "must be a non-negative decimal string"
	}
	return d
}

func parseDate(fields map[string]string, field, raw string) time.Time {
	t, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		fields[field] = "must be a date as YYYY-MM-DD"
	}
	return t
}

func (a *API) previewSettlements(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req settlePreviewRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !currencyPattern.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if !domain.Method(req.Method).Valid() {
		fields["method"] = "must be FIFO, LIFO, LIFO_DATE, WEIGHTED_AVG_RUNNING, WEIGHTED_AVG_PERIODIC, MOVING_AVG or STANDARD"
	}
	if len(req.Transactions) == 0 || len(req.Transactions) > maxTxns {
		fields["transactions"] = fmt.Sprintf("must hold 1 to %d transactions", maxTxns)
	}
	if !fallbacks[req.Fallback] {
		fields["fallback"] = "must be RUNNING_AVG, STANDARD or LAST_RECEIPT"
	}
	if len(req.DefaultValuationType) > maxValuationType {
		fields["defaultValuationType"] = fmt.Sprintf("must be at most %d characters", maxValuationType)
	}
	opt := domain.Options{Method: domain.Method(req.Method), Scale: money.MinorUnits(req.Currency), Fallback: domain.Fallback(req.Fallback)}
	if req.StandardUnitCost != nil {
		opt.Standard = parseDecimal(fields, "standardUnitCost", *req.StandardUnitCost, true)
	}
	txns := parseTransactions(fields, req.Transactions)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	res, perType, err := domain.SettleByValuationType(txns, opt, req.DefaultValuationType)
	if err != nil {
		a.fail(w, r, "settle", err)
		return
	}
	a.previews.Add(1)
	out := settlePreview(res, req.Method, req.Currency, int(opt.Scale))
	if namedTypes(perType) {
		out.RemainingByType = map[string]string{}
		for vt, pr := range perType {
			out.RemainingByType[vt] = pr.RemainingValue.StringFixed(opt.Scale)
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// namedTypes reports whether any partition is a real valuation type rather than
// the unsplit product.
func namedTypes(perType map[string]domain.Result) bool {
	for vt := range perType {
		if vt != "" {
			return true
		}
	}
	return false
}

// parseTransactions validates the transaction DTOs into domain transactions,
// recording field errors in fields.
func parseTransactions(fields map[string]string, dtos []transactionDTO) []domain.Txn {
	txns := make([]domain.Txn, 0, len(dtos))
	for i, t := range dtos {
		key := fmt.Sprintf("transactions[%d]", i)
		kind, ok := txnKinds[t.Kind]
		if !ok {
			fields[key+".kind"] = "must be RECEIPT or ISSUE"
		}
		txn := domain.Txn{ID: t.ID, Kind: kind, ValuationType: t.ValuationType, MarkedReceiptID: t.MarkedReceiptID}
		if t.ID == "" {
			fields[key+".id"] = "is required"
		}
		if t.MarkedReceiptID != "" && kind != domain.Issue {
			fields[key+".markedReceiptId"] = "is only allowed on an ISSUE"
		}
		if len(t.ValuationType) > maxValuationType {
			fields[key+".valuationType"] = fmt.Sprintf("must be at most %d characters", maxValuationType)
		}
		txn.Qty = parseDecimal(fields, key+".qty", t.Qty, false)
		txn.FinancialDate = parseDate(fields, key+".financialDate", t.FinancialDate)
		if t.PhysicalDate != nil {
			txn.PhysicalDate = parseDate(fields, key+".physicalDate", *t.PhysicalDate)
		} else {
			txn.PhysicalDate = txn.FinancialDate
		}
		if t.Value != nil {
			txn.Value = parseDecimal(fields, key+".value", *t.Value, true)
		}
		if kind == domain.Receipt && t.Value == nil {
			fields[key+".value"] = "is required for a receipt"
		}
		txns = append(txns, txn)
	}
	return txns
}

func settlePreview(res domain.Result, method, currency string, scale int) settlePreviewResponse {
	out := settlePreviewResponse{Method: method, Currency: currency, IssueCosts: map[string]string{},
		Settlements: []settlementDTO{}, Exceptions: []exceptionDTO{}, RemainingQty: res.RemainingQty.String(),
		RemainingValue: res.RemainingValue.StringFixed(int32(scale))}
	for id, c := range res.IssueCosts {
		out.IssueCosts[id] = c.StringFixed(int32(scale))
	}
	for _, s := range res.Settlements {
		out.Settlements = append(out.Settlements, settlementDTO{Seq: s.Seq, IssueID: s.IssueID, ReceiptID: s.ReceiptID,
			Pool: s.Pool, Qty: s.Qty.String(), Value: s.Value.StringFixed(int32(scale))})
	}
	for _, x := range res.Exceptions {
		out.Exceptions = append(out.Exceptions, exceptionDTO{IssueID: x.IssueID, Qty: x.Qty.String(),
			Value: x.Value.StringFixed(int32(scale)), Fallback: string(x.Fallback)})
	}
	return out
}

// ---- costing versions ----

type createVersionRequest struct {
	LegalEntityID   string `json:"legalEntityId"`
	Code            string `json:"code"`
	Type            string `json:"type"`
	Currency        string `json:"currency"`
	EffectiveFrom   string `json:"effectiveFrom"`
	BlockActivation bool   `json:"blockActivation"`
}

func (a *API) createVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createVersionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if key := strings.TrimSpace(r.Header.Get(idempotencyKey)); key == "" || len(key) > maxIdempotencyKey {
		fields[idempotencyKey] = fmt.Sprintf("is required, at most %d characters", maxIdempotencyKey)
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !codePattern.MatchString(req.Code) {
		fields["code"] = "must be 2 to 32 characters of A-Z, 0-9, _ or -"
	}
	if !versionTypes[req.Type] {
		fields["type"] = "must be STANDARD or PLANNED"
	}
	if !currencyPattern.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	parseDate(fields, "effectiveFrom", req.EffectiveFrom)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity is outside the caller's scope"))
		return
	}
	v, created, err := a.store.CreateCostingVersion(r.Context(), store.CostingVersion{LegalEntityID: req.LegalEntityID, Code: req.Code,
		Type: req.Type, Currency: req.Currency, EffectiveFrom: req.EffectiveFrom, BlockActivation: req.BlockActivation},
		strings.TrimSpace(r.Header.Get(idempotencyKey)))
	if err != nil {
		a.fail(w, r, "create costing version", err)
		return
	}
	if created {
		a.versionsCreated.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, v)
}

type importItemCostsRequest struct {
	Items []store.ItemCost `json:"items"`
}

// importItemCosts writes pending costs into a DRAFT version from JSON
// ({"items": [...]}) or, with Content-Type text/csv, from a file with the
// header productId,siteId,unitCost (FR-20.5 import). Either is all or nothing.
func (a *API) importItemCosts(w http.ResponseWriter, r *http.Request) {
	id, ok := versionID(w, r)
	if !ok {
		return
	}
	var items []store.ItemCost
	if mediaType(r) == "text/csv" {
		rows, err := domain.ParseItemCostCSV(http.MaxBytesReader(w, r.Body, maxCSVBytes), maxImportItems)
		if err != nil {
			a.fail(w, r, "import item costs", err)
			return
		}
		items = make([]store.ItemCost, len(rows))
		for i, row := range rows {
			items[i] = store.ItemCost{ProductID: row.ProductID, SiteID: row.SiteID, UnitCost: row.UnitCost.String()}
		}
	} else {
		var req importItemCostsRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		fields := validateItems(req.Items)
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		items = req.Items
	}
	n, err := a.store.ImportItemCosts(r.Context(), id, items)
	if err != nil {
		a.fail(w, r, "import item costs", err)
		return
	}
	a.itemsImported.Add(int64(n))
	httpx.JSON(w, http.StatusOK, map[string]any{"versionId": id, "imported": n})
}

func mediaType(r *http.Request) string {
	ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	return strings.ToLower(strings.TrimSpace(ct))
}

// validateItems checks JSON import lines and normalises their ids and costs in place.
func validateItems(items []store.ItemCost) map[string]string {
	fields := map[string]string{}
	if len(items) == 0 || len(items) > maxImportItems {
		fields["items"] = fmt.Sprintf("must hold 1 to %d item costs", maxImportItems)
	}
	type pair struct{ product, site string }
	seen := map[pair]bool{}
	for i, it := range items {
		key := fmt.Sprintf("items[%d]", i)
		if !ids.Valid(it.ProductID) {
			fields[key+".productId"] = "must be a UUID"
		}
		if !ids.Valid(it.SiteID) {
			fields[key+".siteId"] = "must be a UUID"
		}
		d := parseDecimal(fields, key+".unitCost", it.UnitCost, true)
		if _, bad := fields[key+".unitCost"]; !bad {
			if !domain.ValidUnitCost(d) {
				fields[key+".unitCost"] = "must have at most 14 integer digits and 6 decimal places"
			}
			items[i].UnitCost = d.String()
		}
		items[i].ProductID, items[i].SiteID = strings.ToLower(it.ProductID), strings.ToLower(it.SiteID)
		p := pair{items[i].ProductID, items[i].SiteID}
		if seen[p] {
			fields[key] = "duplicates an earlier product and site in this import"
		}
		seen[p] = true
	}
	return fields
}

// ---- internal PPV hook ----

type ppvRequest struct {
	Kind             string `json:"kind"`
	Currency         string `json:"currency"`
	Qty              string `json:"qty"`
	StandardUnitCost string `json:"standardUnitCost"`
	PoUnitPrice      string `json:"poUnitPrice"`
	InvoiceUnitPrice string `json:"invoiceUnitPrice"`
	SourceDocumentID string `json:"sourceDocumentId"`
	// The five fields below are given together to also append the PPV
	// CostTransaction (kind PPV, idempotent per legal entity, document and
	// line); without them the hook only calculates.
	SourceLineID  string `json:"sourceLineId"`
	LegalEntityID string `json:"legalEntityId"`
	ProductID     string `json:"productId"`
	SiteID        string `json:"siteId"`
	FinancialDate string `json:"financialDate"`
}

type ppvResponse struct {
	Kind             string `json:"kind"`
	Currency         string `json:"currency"`
	Amount           string `json:"amount"`
	Direction        string `json:"direction"`
	SourceDocumentID string `json:"sourceDocumentId"`
	Recorded         bool   `json:"recorded"`
}

// standardMatches refuses (422) a recorded receipt whose standard cost or currency is not the one the stored ACTIVE item cost
// carries on the financial date: the voucher books inventory at that standard, so it never comes from the caller.
func (a *API) standardMatches(w http.ResponseWriter, r *http.Request, row store.CostTransaction, std decimal.Decimal, currency string) bool {
	cur, err := a.store.CurrentCost(r.Context(), row.ProductID, row.SiteID, row.FinancialDate)
	if err != nil {
		a.fail(w, r, "ppv standard cost", err)
		return false
	}
	stored, err := decimal.NewFromString(cur.UnitCost)
	if err != nil || !stored.Equal(std) || cur.Currency != currency {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodePPVStandard, "standardUnitCost and currency must equal the stored standard cost in force on financialDate"))
		return false
	}
	return true
}

// postPPV calculates the purchase price variance for a STANDARD item at receipt
// (PO price against standard) or at invoice (invoice price against PO price).
// A positive amount is a debit. With legalEntityId, productId, siteId,
// financialDate and sourceLineId it also appends the PPV CostTransaction, keyed
// by kind, legal entity, source document and line, so the PPV analysis sees it
// and the inventory value does not.
func (a *API) postPPV(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req ppvRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ppvKinds[req.Kind] {
		fields["kind"] = "must be RECEIPT or INVOICE"
	}
	if !currencyPattern.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if req.SourceDocumentID == "" {
		fields["sourceDocumentId"] = "is required"
	}
	qty := parseDecimal(fields, "qty", req.Qty, false)
	po := parseDecimal(fields, "poUnitPrice", req.PoUnitPrice, true)
	var amount, std decimal.Decimal
	scale := money.MinorUnits(req.Currency)
	if req.Kind == "RECEIPT" {
		std = parseDecimal(fields, "standardUnitCost", req.StandardUnitCost, true)
		if len(fields) == 0 {
			if err := domain.CheckPPVInputs(qty, po, std); err != nil {
				a.fail(w, r, "ppv", err)
				return
			}
		}
		amount = domain.ReceiptPPV(qty, po, std, scale)
	} else {
		inv := parseDecimal(fields, "invoiceUnitPrice", req.InvoiceUnitPrice, true)
		if len(fields) == 0 {
			if err := domain.CheckPPVInputs(qty, po, inv); err != nil {
				a.fail(w, r, "ppv", err)
				return
			}
		}
		amount = domain.InvoicePPV(qty, inv, po, scale)
	}
	grir := qty.Mul(po).RoundBank(scale)
	persist := req.SourceLineID != "" || req.LegalEntityID != "" || req.ProductID != "" || req.SiteID != "" || req.FinancialDate != ""
	var row store.CostTransaction
	if persist {
		ppvQty := "0"
		if req.Kind == "RECEIPT" {
			ppvQty = qty.String()
		}
		if n := len(req.SourceLineID); n < 1 || n > maxPPVLineID {
			fields["sourceLineId"] = fmt.Sprintf("must be 1 to %d characters", maxPPVLineID)
		}
		if len(req.SourceDocumentID) > maxPPVDocumentID {
			fields["sourceDocumentId"] = fmt.Sprintf("must be at most %d characters", maxPPVDocumentID)
		}
		for name, v := range map[string]string{"sourceDocumentId": req.SourceDocumentID, "sourceLineId": req.SourceLineID} {
			if strings.Contains(v, ":") {
				fields[name] = "must not contain a colon"
			}
		}
		row = validateCostTransaction(fields, "", costTransactionRequest{
			SourceKey:     strings.Join([]string{"ppv", req.Kind, strings.ToLower(req.LegalEntityID), req.SourceDocumentID, req.SourceLineID}, ":"),
			LegalEntityID: req.LegalEntityID, ProductID: req.ProductID, SiteID: req.SiteID, Kind: domain.KindPPV, Qty: ppvQty,
			Amount: amount.StringFixed(scale), Currency: req.Currency, FinancialDate: req.FinancialDate})
		row.GrirAmount = grir.StringFixed(scale)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if persist {
		if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, row.LegalEntityID) {
			httpx.Fail(w, r, outOfScope())
			return
		}
		if req.Kind == "RECEIPT" {
			recorded, err := a.store.CostTransactionRecorded(r.Context(), row.SourceKey)
			if err != nil {
				a.fail(w, r, "ppv replay lookup", err)
				return
			}
			if !recorded && !a.standardMatches(w, r, row, std, req.Currency) {
				return
			}
		}
		inserted, err := a.store.RecordCostTransactions(r.Context(), []store.CostTransaction{row})
		if err != nil {
			a.fail(w, r, "record ppv cost transaction", err)
			return
		}
		a.costTxnsRecorded.Add(int64(inserted))
	}
	direction := "NONE"
	switch {
	case amount.IsPositive():
		direction = "DR"
	case amount.IsNegative():
		direction = "CR"
	}
	a.ppvCalculated.Add(1)
	httpx.JSON(w, http.StatusOK, ppvResponse{Kind: req.Kind, Currency: req.Currency, Amount: amount.StringFixed(scale),
		Direction: direction, SourceDocumentID: req.SourceDocumentID, Recorded: persist})
}
