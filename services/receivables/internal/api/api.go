// Package api holds the HTTP handlers for the receivables service.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const (
	readyTimeout     = 500 * time.Millisecond
	dutyView         = "ARView"
	dutyPayment      = "ARPaymentPost"
	dutyRMACredit    = "ar.rmacredit.post"
	dutyImport       = "ARImport"
	maxLockboxBytes  = 3 << 20 // below httpx.Decode's 4 MiB body cap, leaving room for JSON escaping
	validationDetail = "request failed validation"
	idempotencyKey   = "Idempotency-Key"
	maxKeyLength     = 200
	maxSourceRef     = 120
	maxReference     = 200
	// maxPayerAccount bounds the raw payer account (IBAN is at most 34; MICR routing and account fit).
	maxPayerAccount   = 64
	maxShortPayReason = 120
	maxPayerName      = 140
)

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	paymentModes = map[string]bool{"CHECK": true, "ACH": true, "WIRE": true, "CARD": true, "CASH": true}
	paymentStats = map[string]bool{"RECEIVED": true, "SUGGESTED": true, "APPLIED": true, "ON_ACCOUNT": true, "REVERSED": true}
)

// API wires the store to HTTP routes and keeps request counters for /metrics.
type API struct {
	store    store.Store
	received atomic.Int64
	applied  atomic.Int64
	matched  atomic.Int64
	failed   atomic.Int64
}

// New returns an API over s.
func New(s store.Store) *API { return &API{store: s} }

// Register mounts the receivables routes on r.
func (a *API) Register(r chi.Router) {
	r.Route("/v1/receivables", func(r chi.Router) {
		r.With(authz.RequireDuty(dutyView)).Get("/open-items", a.listOpenItems)
		r.With(authz.RequireDuty(dutyOpeningImport)).Post("/open-items:batch", a.importOpeningItems)
		r.With(authz.RequireAnyDuty(dutyView, dutyOpeningRead)).Get("/open-items/{ref}", a.getOpenItem)
		r.With(authz.RequireDuty(dutyView)).Get("/open-item-imports", a.listOpeningImports)
		r.With(authz.RequireDuty(dutyView)).Get("/aging", a.aging)
		r.With(authz.RequireDuty(dutyPayment)).Post("/payments", a.createPayment)
		r.With(authz.RequireDuty(dutyView)).Get("/payments", a.listPayments)
		r.With(authz.RequireDuty(dutyView)).Get("/payments/{ref}", a.getPayment)
		// Custom methods (:apply, :match) share one route because chi does not
		// match a literal ':' after a path parameter; paymentAction dispatches.
		r.Post("/payments/{ref}", a.paymentAction)
		a.collectionsRoutes(r)
		a.dunningRoutes(r)
		a.interestRoutes(r)
		a.caseRoutes(r)
		a.confirmationRoutes(r)
		a.downPaymentRoutes(r)
		a.bankStatementRoutes(r)
		a.deductionRoutes(r)
		a.statementRoutes(r)
		r.With(authz.RequireDuty(dutyImport)).Post("/lockbox/imports", a.importLockbox)
		r.With(authz.RequireDuty(dutyView)).Get("/lockbox/imports", a.listLockboxBatches)
		r.With(authz.RequireDuty(dutyView)).Get("/worklist", a.worklist)
		r.With(authz.RequireDuty(dutyRMACredit)).Post("/rma-credits", a.postRMACredit)
		r.With(authz.RequireDuty(dutyView)).Get("/rma-credits", a.listRMACredits)
		r.With(authz.RequireDuty(dutyPayment)).Post("/rma-credits:sweep", a.sweepRMACredits)
		r.Post("/rma-credits/{ref}", a.rmaCreditAction)
	})
}

// Ready reports readiness with a bounded database ping.
func (a *API) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// Metrics serves Prometheus text counters; main mounts it on the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE receivables_payments_received_total counter\nreceivables_payments_received_total %d\n", a.received.Load())
		fmt.Fprintf(w, "# TYPE receivables_payments_applied_total counter\nreceivables_payments_applied_total %d\n", a.applied.Load())
		fmt.Fprintf(w, "# TYPE receivables_payment_matches_total counter\nreceivables_payment_matches_total %d\n", a.matched.Load())
		fmt.Fprintf(w, "# TYPE receivables_failures_total counter\nreceivables_failures_total %d\n", a.failed.Load())
	})
}

type createPaymentRequest struct {
	LegalEntityID string                  `json:"legalEntityId"`
	CustomerID    *string                 `json:"customerId"`
	PaymentMode   string                  `json:"paymentMode"`
	Source        string                  `json:"source"`
	SourceRef     *string                 `json:"sourceRef"`
	Amount        decimal.Decimal         `json:"amount"`
	Currency      string                  `json:"currency"`
	ReceivedDate  string                  `json:"receivedDate"`
	ValueDate     string                  `json:"valueDate"`
	PayerName     *string                 `json:"payerName"`
	Remittance    []domain.RemittanceLine `json:"remittance"`
	// PayerAccount is the payer's IBAN or MICR account as on the advice (R4). Only
	// its hash is stored.
	PayerAccount *string `json:"payerAccount"`
	// ShortPayReason explains a short payment (R7).
	ShortPayReason *string `json:"shortPayReason"`
}

func (a *API) createPayment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if key == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "Idempotency-Key header is required"))
		return
	}
	var req createPaymentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	if fields := validatePayment(req, key); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	in := store.Payment{LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID, PaymentMode: req.PaymentMode, Source: req.Source,
		SourceRef: req.SourceRef, Amount: req.Amount, UnappliedAmount: req.Amount, Currency: req.Currency, ReceivedDate: req.ReceivedDate,
		ValueDate: req.ValueDate, PayerName: req.PayerName, Remittance: req.Remittance, ShortPayReason: trimmed(req.ShortPayReason)}
	if req.PayerAccount != nil {
		h := domain.AccountHash(*req.PayerAccount)
		in.PayerAccountHash = &h
	}
	out, err := a.store.CreatePayment(r.Context(), in, key)
	if err != nil {
		a.fail("create payment", w, r, err)
		return
	}
	a.received.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func validatePayment(req createPaymentRequest, key string) map[string]string {
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.CustomerID != nil && !ids.Valid(*req.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if !paymentModes[req.PaymentMode] {
		fields["paymentMode"] = "must be CHECK, ACH, WIRE, CARD or CASH"
	}
	if req.Source != "MANUAL" {
		fields["source"] = "must be MANUAL; lockbox, camt.053 and portal payments arrive through their importers"
	}
	if req.SourceRef != nil && (*req.SourceRef == "" || len(*req.SourceRef) > maxSourceRef) {
		fields["sourceRef"] = fmt.Sprintf("must be 1 to %d characters", maxSourceRef)
	}
	if !req.Amount.IsPositive() {
		fields["amount"] = "must be a positive decimal"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if _, err := store.ParseDay(req.ReceivedDate); err != nil {
		fields["receivedDate"] = "must be a YYYY-MM-DD date"
	}
	if _, err := store.ParseDay(req.ValueDate); err != nil {
		fields["valueDate"] = "must be a YYYY-MM-DD date"
	}
	for i, line := range req.Remittance {
		if line.Reference == "" || len(line.Reference) > maxReference || line.Amount.IsNegative() {
			fields[fmt.Sprintf("remittance[%d]", i)] = "needs a reference of 1 to 200 characters and a non-negative amount"
		}
	}
	if req.PayerName != nil && (strings.TrimSpace(*req.PayerName) == "" || utf8.RuneCountInString(*req.PayerName) > maxPayerName) {
		fields["payerName"] = fmt.Sprintf("must be 1 to %d characters", maxPayerName)
	}
	if req.PayerAccount != nil {
		if n := domain.NormalizeAccount(*req.PayerAccount); len(n) < 8 || len(*req.PayerAccount) > maxPayerAccount {
			fields["payerAccount"] = fmt.Sprintf("must be an IBAN or bank account of 8 to %d letters and digits", maxPayerAccount)
		}
	}
	if req.ShortPayReason != nil {
		if n := utf8.RuneCountInString(strings.TrimSpace(*req.ShortPayReason)); n == 0 || n > maxShortPayReason {
			fields["shortPayReason"] = fmt.Sprintf("must be 1 to %d characters", maxShortPayReason)
		}
	}
	if key == "" || len(key) > maxKeyLength {
		fields[idempotencyKey] = "must be 1 to 200 characters"
	}
	return fields
}

// trimmed returns a copy of an optional text with surrounding space removed.
func trimmed(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}

func (a *API) getPayment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "ref")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodePaymentNotFound, "payment not found"))
		return
	}
	out, err := a.store.GetPayment(r.Context(), id)
	if err != nil {
		a.fail("get payment", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// paymentAction handles POST /payments/{id}:apply and POST /payments/{id}:match.
func (a *API) paymentAction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "ref"), ":")
	if !found || !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodePaymentNotFound, "payment not found"))
		return
	}
	switch action {
	case "apply":
		a.applyPayment(w, r, id)
	case "match":
		a.matchPayment(w, r, id)
	default:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodePaymentNotFound, "unknown payment action "+action))
	}
}

type applyRequest struct {
	Settlements []settlementRequest `json:"settlements"`
	OnAccount   decimal.Decimal     `json:"onAccount"`
}

type settlementRequest struct {
	OpenItemID   string          `json:"openItemId"`
	Amount       decimal.Decimal `json:"amount"`
	CashDiscount decimal.Decimal `json:"cashDiscount"`
	WriteOff     decimal.Decimal `json:"writeOff"`
}

func (a *API) applyPayment(w http.ResponseWriter, r *http.Request, id string) {
	if !require(w, r, dutyPayment) {
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) > maxKeyLength {
		badQuery(w, r, idempotencyKey, fmt.Sprintf("must be at most %d characters", maxKeyLength))
		return
	}
	var req applyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.OnAccount.IsNegative() {
		fields["onAccount"] = "must not be negative"
	}
	plan := store.ApplyRequest{Residual: req.OnAccount}
	for i, s := range req.Settlements {
		if !ids.Valid(s.OpenItemID) {
			fields[fmt.Sprintf("settlements[%d].openItemId", i)] = "must be a UUID"
		}
		if s.Amount.IsNegative() || s.CashDiscount.IsNegative() || s.WriteOff.IsNegative() {
			fields[fmt.Sprintf("settlements[%d]", i)] = "amounts must not be negative"
		}
		plan.Allocations = append(plan.Allocations, domain.Allocation{OpenItemID: s.OpenItemID, Amount: s.Amount,
			CashDiscount: s.CashDiscount, WriteOff: s.WriteOff})
	}
	if len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	out, err := a.store.ApplyPayment(r.Context(), id, version, plan, key)
	if err != nil {
		a.fail("apply payment", w, r, err)
		return
	}
	a.applied.Add(1)
	w.Header().Set("ETag", etag(out.Payment.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) matchPayment(w http.ResponseWriter, r *http.Request, id string) {
	if !require(w, r, dutyPayment) {
		return
	}
	res, err := a.store.MatchPayment(r.Context(), id)
	if err != nil {
		a.fail("match payment", w, r, err)
		return
	}
	a.matched.Add(1)
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) listPayments(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && !paymentStats[status] {
		badQuery(w, r, "status", "must be RECEIVED, SUGGESTED, APPLIED, ON_ACCOUNT or REVERSED")
		return
	}
	customer := q.Get("customer")
	if customer != "" && !ids.Valid(customer) {
		badQuery(w, r, "customer", "must be a UUID")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListPayments(r.Context(), store.PaymentFilter{Status: status, CustomerID: customer}, limit)
	if err != nil {
		a.fail("list payments", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// pageLimit reads the optional limit query parameter (1 to MaxPageSize).
func pageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return store.MaxPageSize, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > store.MaxPageSize {
		badQuery(w, r, "limit", fmt.Sprintf("must be 1 to %d", store.MaxPageSize))
		return 0, false
	}
	return n, true
}

func (a *API) listOpenItems(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	customer := q.Get("customer")
	if customer != "" && !ids.Valid(customer) {
		badQuery(w, r, "customer", "must be a UUID")
		return
	}
	status := q.Get("status")
	if status == "" {
		status = "open"
	}
	if status != "open" && status != "all" {
		badQuery(w, r, "status", "must be open or all")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	source := q.Get("importSource")
	if source != "" && !domain.ImportSourceName.MatchString(source) {
		badQuery(w, r, "importSource", "must name a legacy system, as sent to open-items:batch")
		return
	}
	items, err := a.store.ListOpenItems(r.Context(), store.OpenItemFilter{CustomerID: customer, OpenOnly: status == "open", ImportSource: source}, limit)
	if err != nil {
		a.fail("list open items", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) aging(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	asOf := time.Now().UTC()
	if raw := r.URL.Query().Get("asOf"); raw != "" {
		t, err := store.ParseDay(raw)
		if err != nil {
			badQuery(w, r, "asOf", "must be a YYYY-MM-DD date")
			return
		}
		asOf = t
	}
	rows, err := a.store.AgingRows(r.Context())
	if err != nil {
		a.fail("aging", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"asOf": asOf.Format("2006-01-02"), "totals": domain.Age(asOf, rows)})
}

func (a *API) fail(op string, w http.ResponseWriter, r *http.Request, err error) {
	a.failed.Add(1)
	slog.Error(op+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	httpx.Fail(w, r, err)
}

// ifMatch parses the required If-Match header for custom methods (428 when absent).
func ifMatch(r *http.Request) (int, error) {
	raw := strings.Trim(r.Header.Get("If-Match"), `" `)
	if raw == "" {
		return 0, httpx.NewProblem(http.StatusPreconditionRequired, "COM-0428", "If-Match header with the resource version is required")
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 0, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "If-Match must be a resource version")
	}
	return v, nil
}

func etag(version int) string { return `"` + strconv.Itoa(version) + `"` }

func badQuery(w http.ResponseWriter, r *http.Request, field, rule string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = map[string]string{field: rule}
	httpx.Fail(w, r, p)
}

// require checks a duty for actions that depend on the request (custom methods).
func require(w http.ResponseWriter, r *http.Request, duty string) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
		return false
	}
	if !p.HasDuty(duty) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+duty))
		return false
	}
	return true
}

// scopeAllows refuses a legal entity outside the caller's scope (AUTH-0003).
func scopeAllows(w http.ResponseWriter, r *http.Request, legalEntityID string) bool {
	p, _ := tenant.FromContext(r.Context())
	if !tenant.InLegalEntityScope(p, legalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity is outside the caller's scope"))
		return false
	}
	return true
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	if p.LegalEntityID != "" && !ids.Valid(p.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "legal entity must be a single UUID"))
		return false
	}
	return true
}
