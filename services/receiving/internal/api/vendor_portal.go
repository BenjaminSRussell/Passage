package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

// Supplier portal routes (#511): a vendor's own advance ship notices, invoice documents and invoices. The vendor comes from the
// verified supplier-portal service token (the principal's VendorID) and never from the request; the duties are service-only. The
// handlers behind the routes are the buyer's, which read the vendor from the request context and narrow to it.
const (
	dutyVendorAsn     = "receiving.vendor-portal.asn"
	dutyVendorInvoice = "receiving.vendor-portal.invoice"
)

type vendorKey struct{}

// vendorFrom is the vendor a supplier portal caller acts for; empty on every buyer route.
func vendorFrom(ctx context.Context) string {
	v, _ := ctx.Value(vendorKey{}).(string)
	return v
}

// vendorScoped refuses a caller that is not the supplier portal acting for a vendor, and hands the vendor to the handler.
func vendorScoped(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := tenant.FromContext(r.Context())
		if !ok || p.Service != tenant.VendorPortalIssuer || !ids.Valid(p.VendorID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "RCV-VND-001", "this account is not a supplier account"))
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), vendorKey{}, strings.ToLower(p.VendorID))))
	}
}

func (a *API) registerVendorPortal(r chi.Router) {
	asn, invoice := authz.RequireDuty(dutyVendorAsn), authz.RequireDuty(dutyVendorInvoice)
	r.With(asn).Post("/v1/receiving/vendor/asns", vendorScoped(a.idempotent(a.createAsn)))
	r.With(asn).Get("/v1/receiving/vendor/asns", vendorScoped(a.listAsns))
	r.With(asn).Get("/v1/receiving/vendor/asns/{id}", vendorScoped(a.getAsn))
	r.With(invoice).Post("/v1/receiving/vendor/invoice-captures", vendorScoped(a.idempotent(a.createInvoiceCapture)))
	r.With(invoice).Get("/v1/receiving/vendor/invoice-captures", vendorScoped(a.listInvoiceCaptures))
	r.With(invoice).Get("/v1/receiving/vendor/invoice-captures/{id}", vendorScoped(a.getInvoiceCapture))
	r.With(invoice).Get("/v1/receiving/vendor/invoices", vendorScoped(a.vendorListInvoices))
	r.With(invoice).Get("/v1/receiving/vendor/invoices/{id}", vendorScoped(a.vendorGetInvoice))
}

// vendorInvoiceBody is what a vendor sees of a posted invoice: the commercial facts and a status in the vendor's words. The ledger
// entry, the audit sample and who released or rejected it stay internal.
type vendorInvoiceBody struct {
	ID            string `json:"id"`
	InvoiceNumber string `json:"invoiceNumber"`
	InvoiceDate   string `json:"invoiceDate"`
	Currency      string `json:"currency"`
	Total         string `json:"total"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
}

// vendorInvoiceStatus maps the buyer's statuses to the vendor's: an audit block is an internal control, so the vendor sees the
// invoice as still in review until it is released.
func vendorInvoiceStatus(s string) string {
	switch s {
	case "POSTED":
		return "ACCEPTED"
	case "AUDIT_REJECTED":
		return "REJECTED"
	default:
		return "IN_REVIEW"
	}
}

func toVendorInvoice(in store.Invoice) vendorInvoiceBody {
	b := toInvoiceBody(in)
	return vendorInvoiceBody{ID: b.ID, InvoiceNumber: b.InvoiceNumber, InvoiceDate: b.InvoiceDate, Currency: b.Currency, Total: b.Total,
		Status: vendorInvoiceStatus(in.Status), CreatedAt: b.CreatedAt}
}

type vendorInvoicePage struct {
	Items      []vendorInvoiceBody `json:"items"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

func (a *API) vendorListInvoices(w http.ResponseWriter, r *http.Request) {
	q := query{values: r.URL.Query(), fields: map[string]string{}}
	f := store.InvoiceFilter{VendorID: vendorFrom(r.Context()), Limit: q.limit()}
	f.CursorCreatedAt, f.CursorID = q.cursor()
	if len(q.fields) > 0 {
		httpx.Fail(w, r, validationProblem(q.fields))
		return
	}
	rows, err := a.store.ListInvoices(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page := vendorInvoicePage{Items: make([]vendorInvoiceBody, 0, len(rows))}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		page.NextCursor = encodeCursor(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
	}
	for _, inv := range rows {
		page.Items = append(page.Items, toVendorInvoice(inv))
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) vendorGetInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validationProblem(map[string]string{"id": "must be a UUID"}))
		return
	}
	inv, err := a.store.GetInvoice(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !strings.EqualFold(inv.VendorID, vendorFrom(r.Context())) {
		writeError(w, r, store.ErrInvoiceNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, toVendorInvoice(inv))
}
