// Package api exposes the costing HTTP surface: settlement preview, costing
// versions with item-cost import, and the internal purchase price variance hook.
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
	store           store.Store
	versionsCreated atomic.Int64
	itemsImported   atomic.Int64
	previews        atomic.Int64
	ppvCalculated   atomic.Int64
	domainRefusals  atomic.Int64
	activations     atomic.Int64
	costsActivated  atomic.Int64
	itemsCopied     atomic.Int64
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
	r.With(authz.RequireDuty(dutyPPVPost)).Post("/internal/v1/costing/ppv", a.postPPV)
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
	if p, _ := tenant.FromContext(r.Context()); p.LegalEntityID != "" && p.LegalEntityID != req.LegalEntityID {
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
}

type ppvResponse struct {
	Kind             string `json:"kind"`
	Currency         string `json:"currency"`
	Amount           string `json:"amount"`
	Direction        string `json:"direction"`
	SourceDocumentID string `json:"sourceDocumentId"`
}

// postPPV calculates the purchase price variance for a STANDARD item at receipt
// (PO price against standard) or at invoice (invoice price against PO price).
// A positive amount is a debit. Persisting the CostTransaction is a later slice.
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
	var amount decimal.Decimal
	scale := money.MinorUnits(req.Currency)
	if req.Kind == "RECEIPT" {
		std := parseDecimal(fields, "standardUnitCost", req.StandardUnitCost, true)
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
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
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
		Direction: direction, SourceDocumentID: req.SourceDocumentID})
}
