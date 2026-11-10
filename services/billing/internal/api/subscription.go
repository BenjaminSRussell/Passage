package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const maxPlanCodeLength = 50

type SubscriptionStore interface {
	FindSubscriptionByKey(ctx context.Context, key string) (store.Subscription, bool, error)
	CreateSubscription(ctx context.Context, sub store.Subscription) (store.Subscription, error)
	GetSubscription(ctx context.Context, id string) (store.Subscription, error)
	ChangeSubscription(ctx context.Context, id string, term domain.PlanTerm, expectedVersion int) (store.Subscription, error)
	CancelSubscription(ctx context.Context, id string, lastServiceDay time.Time, expectedVersion int) (store.Subscription, error)
	FindPeriodInvoice(ctx context.Context, subscriptionID string, periodFrom time.Time) (store.Invoice, bool, error)
	BillSubscription(ctx context.Context, id string, expectedVersion int, periodFrom, periodTo time.Time, inv store.Invoice, usageIDs []string) (store.Invoice, bool, error)
	RecordUsage(ctx context.Context, subscriptionID string, in store.Usage) (store.Usage, bool, error)
	ListUnbilledUsage(ctx context.Context, subscriptionID string, upTo time.Time) ([]store.Usage, error)
	DueSubscriptions(ctx context.Context, legalEntityID string, asOf time.Time, limit int) ([]string, error)
	SubscriptionLegalEntities(ctx context.Context) ([]string, error)
}

func (a *API) WithSubscriptions(s SubscriptionStore) *API {
	a.subscriptions = s
	return a
}

type subscriptionRequest struct {
	LegalEntityID    string `json:"legalEntityId"`
	CustomerID       string `json:"customerId"`
	InvoiceAccountID string `json:"invoiceAccountId"`
	Currency         string `json:"currency"`
	PlanCode         string `json:"planCode"`
	Price            string `json:"price"`
	PeriodMonths     int    `json:"periodMonths"`
	StartDate        string `json:"startDate"`
	RevenueAccount   string `json:"revenueAccount"`
	TaxCode          string `json:"taxCode"`
	PaymentTermDays  int    `json:"paymentTermDays"`
}

type planChangeRequest struct {
	PlanCode      string `json:"planCode"`
	Price         string `json:"price"`
	EffectiveFrom string `json:"effectiveFrom"`
}

type cancelRequest struct {
	LastServiceDay string `json:"lastServiceDay"`
}

type billRequest struct {
	PeriodFrom  string `json:"periodFrom"`
	InvoiceDate string `json:"invoiceDate,omitempty"`
}

type termDTO struct {
	PlanCode      string `json:"planCode"`
	Price         string `json:"price"`
	EffectiveFrom string `json:"effectiveFrom"`
}

type subscriptionDTO struct {
	ID               string    `json:"id"`
	LegalEntityID    string    `json:"legalEntityId"`
	CustomerID       string    `json:"customerId"`
	InvoiceAccountID string    `json:"invoiceAccountId"`
	Currency         string    `json:"currency"`
	PeriodMonths     int       `json:"periodMonths"`
	StartDate        string    `json:"startDate"`
	CancelEffective  string    `json:"lastServiceDay,omitempty"`
	Status           string    `json:"status"`
	RevenueAccount   string    `json:"revenueAccount"`
	TaxCode          string    `json:"taxCode"`
	PaymentTermDays  int       `json:"paymentTermDays"`
	BilledThrough    string    `json:"billedThrough,omitempty"`
	Terms            []termDTO `json:"terms"`
	Version          int       `json:"version"`
}

func toSubscriptionDTO(sub store.Subscription) subscriptionDTO {
	out := subscriptionDTO{ID: sub.ID, LegalEntityID: sub.LegalEntityID, CustomerID: sub.CustomerID, InvoiceAccountID: sub.InvoiceAccountID,
		Currency: sub.Currency, PeriodMonths: sub.PeriodMonths, StartDate: sub.Start.Format(dateLayout), Status: sub.Status,
		RevenueAccount: sub.RevenueAccount, TaxCode: sub.TaxCode, PaymentTermDays: sub.PaymentTermDays, Version: sub.Version, Terms: []termDTO{}}
	if sub.CancelEffective != nil {
		out.CancelEffective = sub.CancelEffective.Format(dateLayout)
	}
	if !sub.BilledThrough.IsZero() {
		out.BilledThrough = sub.BilledThrough.Format(dateLayout)
	}
	for _, t := range sub.EffectiveTerms() {
		out.Terms = append(out.Terms, termDTO{PlanCode: t.PlanCode, Price: fixed(t.Price, money.MinorUnits(sub.Currency)), EffectiveFrom: t.From.Format(dateLayout)})
	}
	return out
}

func (a *API) subscriptionsReady(w http.ResponseWriter, r *http.Request) bool {
	if a.subscriptions == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "BIL-0109", "subscriptions are not available in this deployment"))
		return false
	}
	return true
}

func hashOf(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func parseDay(fields map[string]string, name, raw string) time.Time {
	t, err := time.Parse(dateLayout, raw)
	if err != nil {
		fields[name] = "must be a YYYY-MM-DD date"
	}
	return t
}

func parsePlan(fields map[string]string, code, price string) (string, decimal.Decimal) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > maxPlanCodeLength {
		fields["planCode"] = fmt.Sprintf("is required, at most %d characters", maxPlanCodeLength)
	}
	if !decimalText.MatchString(price) || !decimal.RequireFromString(price).IsPositive() {
		fields["price"] = "must be a positive decimal with at most 6 fraction digits"
		return code, decimal.Zero
	}
	return code, decimal.RequireFromString(price)
}

func (a *API) createSubscription(w http.ResponseWriter, r *http.Request) {
	if !a.subscriptionsReady(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if key == "" || len(key) > 255 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "Idempotency-Key header is required (1 to 255 characters)"))
		return
	}
	var req subscriptionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "customerId": req.CustomerID, "invoiceAccountId": req.InvoiceAccountID} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if !domain.ValidPeriodMonths(req.PeriodMonths) {
		fields["periodMonths"] = "must be 1, 3, 6 or 12"
	}
	if req.PaymentTermDays < 0 || req.PaymentTermDays > 365 {
		fields["paymentTermDays"] = "must be 0 to 365"
	}
	if !accountText.MatchString(req.RevenueAccount) {
		fields["revenueAccount"] = "must be a ledger account of up to 20 letters, digits, dots, dashes or underscores"
	}
	if !taxCodeText.MatchString(req.TaxCode) {
		fields["taxCode"] = "must be a tax code of up to 20 capital letters, digits, dashes or underscores"
	}
	start := parseDay(fields, "startDate", req.StartDate)
	plan, price := parsePlan(fields, req.PlanCode, req.Price)
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	hash := hashOf(req)
	if existing, found, err := a.subscriptions.FindSubscriptionByKey(r.Context(), key); err != nil {
		httpx.Fail(w, r, err)
		return
	} else if found {
		if existing.RequestHash != hash {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "idempotency_key_reuse", "Idempotency-Key was already used with a different request body"))
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		w.Header().Set("ETag", etag(existing.Version))
		httpx.JSON(w, http.StatusOK, toSubscriptionDTO(existing))
		return
	}
	created, err := a.subscriptions.CreateSubscription(r.Context(), store.Subscription{
		ID: ids.New(), LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID, InvoiceAccountID: req.InvoiceAccountID,
		RevenueAccount: req.RevenueAccount, TaxCode: req.TaxCode, PaymentTermDays: req.PaymentTermDays, IdempotencyKey: key, RequestHash: hash,
		Subscription: domain.Subscription{Currency: req.Currency, PeriodMonths: req.PeriodMonths, Start: start,
			Terms: []domain.PlanTerm{{PlanCode: plan, Price: price, From: start}}},
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.subscriptionsCreated.Add(1)
	w.Header().Set("ETag", etag(created.Version))
	httpx.JSON(w, http.StatusCreated, toSubscriptionDTO(created))
}

func (a *API) loadSubscription(w http.ResponseWriter, r *http.Request) (store.Subscription, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "subscription not found"))
		return store.Subscription{}, false
	}
	sub, err := a.subscriptions.GetSubscription(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return store.Subscription{}, false
	}
	return sub, true
}

func (a *API) getSubscription(w http.ResponseWriter, r *http.Request) {
	if !a.subscriptionsReady(w, r) {
		return
	}
	if sub, ok := a.loadSubscription(w, r); ok {
		w.Header().Set("ETag", etag(sub.Version))
		httpx.JSON(w, http.StatusOK, toSubscriptionDTO(sub))
	}
}

func (a *API) changePlan(w http.ResponseWriter, r *http.Request) {
	if !a.subscriptionsReady(w, r) {
		return
	}
	var req planChangeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	expected, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	from := parseDay(fields, "effectiveFrom", req.EffectiveFrom)
	plan, price := parsePlan(fields, req.PlanCode, req.Price)
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	a.updateSubscription(w, r, func(id string) (store.Subscription, error) {
		return a.subscriptions.ChangeSubscription(r.Context(), id, domain.PlanTerm{PlanCode: plan, Price: price, From: from}, expected)
	})
}

func (a *API) cancelSubscription(w http.ResponseWriter, r *http.Request) {
	if !a.subscriptionsReady(w, r) {
		return
	}
	var req cancelRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	expected, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	last := parseDay(fields, "lastServiceDay", req.LastServiceDay)
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	a.updateSubscription(w, r, func(id string) (store.Subscription, error) {
		return a.subscriptions.CancelSubscription(r.Context(), id, last, expected)
	})
}

func (a *API) updateSubscription(w http.ResponseWriter, r *http.Request, apply func(id string) (store.Subscription, error)) {
	current, ok := a.loadSubscription(w, r)
	if !ok {
		return
	}
	if !tenant.InLegalEntityScope(principalOf(r), current.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	updated, err := apply(current.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(updated.Version))
	httpx.JSON(w, http.StatusOK, toSubscriptionDTO(updated))
}

func principalOf(r *http.Request) tenant.Principal {
	p, _ := tenant.FromContext(r.Context())
	return p
}

func ifMatchVersion(w http.ResponseWriter, r *http.Request) (int, bool) {
	expected, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", `If-Match must be the subscription version as an entity tag, for example "1"`))
	}
	return expected, ok
}

func (a *API) billSubscription(w http.ResponseWriter, r *http.Request) {
	if !a.subscriptionsReady(w, r) {
		return
	}
	var req billRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	periodFrom := parseDay(fields, "periodFrom", req.PeriodFrom)
	invoiceDate := periodFrom
	if req.InvoiceDate != "" {
		invoiceDate = parseDay(fields, "invoiceDate", req.InvoiceDate)
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	sub, ok := a.loadSubscription(w, r)
	if !ok {
		return
	}
	if !tenant.InLegalEntityScope(principalOf(r), sub.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	created, replayed, err := a.billPeriod(r.Context(), sub, periodFrom, invoiceDate)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		w.Header().Set("ETag", etag(created.Version))
		httpx.JSON(w, http.StatusOK, toDTO(created))
		return
	}
	a.subscriptionInvoices.Add(1)
	w.Header().Set("ETag", etag(created.Version))
	httpx.JSON(w, http.StatusCreated, toDTO(created))
}
