package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

// Customer portal routes (#30): a customer's own posted invoices and credit notes. The customer comes from the verified
// customer-portal service token (the principal's CustomerID) and never from the request: a `customerId` query parameter on these
// routes is ignored, and an invoice of another customer, or a proposal not yet posted, reads as not found. The duty is
// service-only and read-only. The answers are the customer's view: no ledger accounts, tax codes, source documents or versions.
const dutyCustomerPortal = "billing.customer-portal.read"

func (a *API) registerCustomerPortal(r chi.Router) {
	read := authz.RequireDuty(dutyCustomerPortal)
	r.With(read).Get("/v1/billing/customer-portal/invoices", portalCustomerOnly(a.portalListInvoices))
	r.With(read).Get("/v1/billing/customer-portal/invoices/{id}", portalCustomerOnly(a.portalGetInvoice))
}

type portalHandler func(w http.ResponseWriter, r *http.Request, customer string)

// portalCustomerOnly refuses any caller that is not the customer portal acting for a customer, and hands the customer from the
// verified principal to the handler.
func portalCustomerOnly(next portalHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		customer, ok := tenant.PortalCustomer(r.Context())
		if !ok || !ids.Valid(customer) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "BIL-CUS-001", "this account is not a customer account"))
			return
		}
		next(w, r, customer)
	}
}

type portalLine struct {
	LineNo int `json:"lineNo"`
	// ProductID lets the portal name the product of a line when it requests a return; absent for a line that is not for a product.
	ProductID   string `json:"productId,omitempty"`
	Description string `json:"description"`
	Qty         string `json:"qty"`
	UnitPrice   string `json:"unitPrice"`
	DiscountPct string `json:"discountPct"`
	Net         string `json:"net"`
	Tax         string `json:"tax"`
}

type portalInvoice struct {
	ID              string       `json:"id"`
	Type            string       `json:"type"`
	Status          string       `json:"status"`
	Number          string       `json:"number"`
	Currency        string       `json:"currency"`
	InvoiceDate     string       `json:"invoiceDate"`
	DueDate         string       `json:"dueDate"`
	PaymentTermDays int          `json:"paymentTermDays"`
	Totals          totalsDTO    `json:"totals"`
	OriginalNumber  string       `json:"originalNumber,omitempty"`
	Lines           []portalLine `json:"lines,omitempty"`
}

func toPortalInvoice(inv store.Invoice, withLines bool) portalInvoice {
	s := toSummary(inv)
	out := portalInvoice{ID: s.ID, Type: s.Type, Status: s.Status, Number: s.Number, Currency: s.Currency, InvoiceDate: s.InvoiceDate,
		DueDate: s.DueDate, PaymentTermDays: s.PaymentTermDays, Totals: s.Totals, OriginalNumber: s.OriginalNumber}
	if withLines {
		exp := money.MinorUnits(inv.Currency)
		out.Lines = make([]portalLine, 0, len(inv.Lines))
		for _, l := range inv.Lines {
			out.Lines = append(out.Lines, portalLine{LineNo: l.No, ProductID: l.Source.ProductID, Description: l.Description, Qty: l.Qty.String(), UnitPrice: l.UnitPrice.String(),
				DiscountPct: l.DiscountPct.String(), Net: fixed(l.Net, exp), Tax: fixed(l.Tax, exp)})
		}
	}
	return out
}

// visibleToCustomer is true for an invoice that is the customer's and is posted: a proposal is internal until it is posted.
func visibleToCustomer(inv store.Invoice, customer string) bool {
	return strings.EqualFold(inv.CustomerID, customer) && inv.Status != domain.StatusProposal
}

type portalPage struct {
	Items      []portalInvoice `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

func (a *API) portalListInvoices(w http.ResponseWriter, r *http.Request, customer string) {
	q := r.URL.Query()
	f := store.ListFilter{CustomerID: customer, PostedOnly: true, Limit: defaultListLimit}
	fields := map[string]string{}
	if s := q.Get("status"); s != "" {
		if !statuses[s] || s == domain.StatusProposal {
			fields["status"] = "must be POSTED, PARTIALLY_SETTLED, SETTLED or REVERSED"
		}
		f.Status = s
	}
	if raw := q.Get("cursor"); raw != "" {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || !ids.Valid(string(b)) {
			fields["cursor"] = "must be a nextCursor returned by a previous page"
		}
		f.After = strings.ToLower(string(b))
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxListLimit {
			fields["limit"] = "must be 1 to " + strconv.Itoa(maxListLimit)
		}
		f.Limit = n
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	page, err := a.store.ListInvoices(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := portalPage{Items: make([]portalInvoice, 0, len(page.Items))}
	for _, inv := range page.Items {
		// Belt and braces: the filter narrows the query, and a row of another customer or a proposal is still never sent.
		if visibleToCustomer(inv, customer) {
			out.Items = append(out.Items, toPortalInvoice(inv, false))
		}
	}
	if page.NextAfter != "" {
		out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.NextAfter))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) portalGetInvoice(w http.ResponseWriter, r *http.Request, customer string) {
	notFound := httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "invoice not found")
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, notFound)
		return
	}
	inv, err := a.store.GetInvoice(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !visibleToCustomer(inv, customer) {
		httpx.Fail(w, r, notFound)
		return
	}
	httpx.JSON(w, http.StatusOK, toPortalInvoice(inv, true))
}
