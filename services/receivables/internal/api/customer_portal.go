package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// Customer portal routes (#30, 30-5a): a customer's own open items, payments and statement. The customer comes from the
// verified customer-portal service token (the principal's CustomerID) and never from the request: a `customer` query
// parameter on these routes is ignored, and an item of another customer reads as not found. The duty is service-only and
// read-only. The answers are the customer's view: no dunning state, import keys, payer bank hashes or internal references.
const dutyCustomerPortal = "receivables.customer-portal.read"

func (a *API) customerPortalRoutes(r chi.Router) {
	read := authz.RequireDuty(dutyCustomerPortal)
	r.With(read).Get("/customer-portal/open-items", portalCustomerOnly(a.portalOpenItems))
	r.With(read).Get("/customer-portal/open-items/{ref}", portalCustomerOnly(a.portalOpenItem))
	r.With(read).Get("/customer-portal/payments", portalCustomerOnly(a.portalPayments))
	r.With(read).Get("/customer-portal/statement", portalCustomerOnly(a.portalStatement))
}

type portalCustomerHandler func(w http.ResponseWriter, r *http.Request, customer string)

// portalCustomerOnly refuses any caller that is not the customer portal acting for a customer, and hands the customer from the
// verified principal to the handler.
func portalCustomerOnly(next portalCustomerHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		customer, ok := tenant.PortalCustomer(r.Context())
		if !ok || !ids.Valid(customer) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AR-CUS-001", "this account is not a customer account"))
			return
		}
		if !validTenant(w, r) {
			return
		}
		next(w, r, customer)
	}
}

type portalOpenItem struct {
	ID                 string          `json:"id"`
	DocumentNumber     string          `json:"documentNumber"`
	DocumentDate       string          `json:"documentDate"`
	DueDate            string          `json:"dueDate"`
	Currency           string          `json:"currency"`
	Amount             decimal.Decimal `json:"amount"`
	OpenAmount         decimal.Decimal `json:"openAmount"`
	Side               string          `json:"side"`
	Status             string          `json:"status"`
	SourceType         string          `json:"sourceType"`
	CashDiscountDate   *string         `json:"cashDiscountDate,omitempty"`
	CashDiscountAmount decimal.Decimal `json:"cashDiscountAmount"`
}

func toPortalOpenItem(o store.OpenItem) portalOpenItem {
	return portalOpenItem{ID: o.ID, DocumentNumber: o.DocumentNumber, DocumentDate: o.DocumentDate, DueDate: o.DueDate, Currency: o.Currency,
		Amount: o.Amount, OpenAmount: o.OpenAmount, Side: o.Side, Status: o.Status, SourceType: o.SourceType,
		CashDiscountDate: o.CashDiscountDate, CashDiscountAmount: o.CashDiscountAmount}
}

type portalPayment struct {
	ID              string          `json:"id"`
	PaymentMode     string          `json:"paymentMode"`
	Amount          decimal.Decimal `json:"amount"`
	UnappliedAmount decimal.Decimal `json:"unappliedAmount"`
	Currency        string          `json:"currency"`
	ReceivedDate    string          `json:"receivedDate"`
	ValueDate       string          `json:"valueDate"`
	Status          string          `json:"status"`
}

func (a *API) portalOpenItems(w http.ResponseWriter, r *http.Request, customer string) {
	status := r.URL.Query().Get("status")
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
	items, err := a.store.ListOpenItems(r.Context(), store.OpenItemFilter{CustomerID: customer, OpenOnly: status == "open"}, limit)
	if err != nil {
		a.fail("list customer portal open items", w, r, err)
		return
	}
	out := make([]portalOpenItem, 0, len(items))
	for _, o := range items {
		// Belt and braces: the filter above narrows the query, and a row of another customer is still never sent.
		if strings.EqualFold(o.CustomerID, customer) {
			out = append(out, toPortalOpenItem(o))
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) portalOpenItem(w http.ResponseWriter, r *http.Request, customer string) {
	id := chi.URLParam(r, "ref")
	if !ids.Valid(id) {
		badQuery(w, r, "ref", "must be a UUID")
		return
	}
	item, err := a.store.GetOpenItem(r.Context(), id)
	if err != nil {
		a.fail("get customer portal open item", w, r, err)
		return
	}
	if !strings.EqualFold(item.CustomerID, customer) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeOpenItemNotFound, "open item not found"))
		return
	}
	httpx.JSON(w, http.StatusOK, toPortalOpenItem(item))
}

func (a *API) portalPayments(w http.ResponseWriter, r *http.Request, customer string) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	rows, err := a.store.ListPayments(r.Context(), store.PaymentFilter{CustomerID: customer}, limit)
	if err != nil {
		a.fail("list customer portal payments", w, r, err)
		return
	}
	out := make([]portalPayment, 0, len(rows))
	for _, p := range rows {
		if p.CustomerID == nil || !strings.EqualFold(*p.CustomerID, customer) {
			continue
		}
		out = append(out, portalPayment{ID: p.ID, PaymentMode: p.PaymentMode, Amount: p.Amount, UnappliedAmount: p.UnappliedAmount,
			Currency: p.Currency, ReceivedDate: p.ReceivedDate, ValueDate: p.ValueDate, Status: p.Status})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

// portalStatement is the customer's account statement in the legal entity of the token: the entity is the token's and the
// customer is the token's, whatever the query says.
func (a *API) portalStatement(w http.ResponseWriter, r *http.Request, customer string) {
	p, _ := tenant.FromContext(r.Context())
	le := p.LegalEntityID
	if !ids.Valid(le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AR-CUS-001", "this account is not a customer account"))
		return
	}
	asOf, ok := dayParam(r.URL.Query().Get("asOf"))
	if !ok {
		badQuery(w, r, "asOf", "must be a YYYY-MM-DD date")
		return
	}
	sections, err := a.store.Statement(r.Context(), le, customer, asOf)
	if err != nil {
		a.fail("customer portal statement", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"asOf": asOf.Format("2006-01-02"), "sections": sections})
}
