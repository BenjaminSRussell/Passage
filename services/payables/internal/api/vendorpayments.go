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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

// Payment status for the supplier portal (#511). The route is for the supplier portal service only: the duty is service-only and
// the vendor comes from the verified service token, never from the request, so a vendor sees its own invoices and nothing else.
const (
	dutyVendorPortalRead = "payables.vendor-portal.read"
	pathVendorPayments   = "/v1/payables/vendor/payments"
	// CodeVendorScope is answered to a caller that is not the supplier portal acting for a vendor.
	CodeVendorScope = "PAY-VND-001"
	vendorPageSize  = 50
)

// WithVendorPayments enables the supplier portal's payment status read.
func (a *API) WithVendorPayments(s store.VendorPaymentStore) *API {
	a.vendorPayments = s
	return a
}

func (a *API) registerVendorPayments(r chi.Router) {
	if a.vendorPayments == nil {
		return
	}
	r.With(authz.RequireDuty(dutyVendorPortalRead)).Get(pathVendorPayments, a.listVendorPayments)
}

type vendorPaymentBody struct {
	InvoiceID     string `json:"invoiceId"`
	InvoiceNumber string `json:"invoiceNumber"`
	InvoiceDate   string `json:"invoiceDate"`
	DueDate       string `json:"dueDate"`
	Currency      string `json:"currency"`
	Amount        string `json:"amount"`
	PaidAmount    string `json:"paidAmount"`
	OpenAmount    string `json:"openAmount"`
	Status        string `json:"status"`
	ScheduledFor  string `json:"scheduledFor,omitempty"`
	PaidAt        string `json:"paidAt,omitempty"`
}

func encodePaymentCursor(date, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(date + "|" + id))
}

func decodePaymentCursor(v string) (date, id string, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(v)
	date, id, found := strings.Cut(string(raw), "|")
	return date, id, err == nil && found && len(date) == 10 && ids.Valid(id)
}

func (a *API) listVendorPayments(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || p.Service != tenant.VendorPortalIssuer || !ids.Valid(p.VendorID) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, CodeVendorScope, "this account is not a supplier account"))
		return
	}
	q := r.URL.Query()
	f := store.VendorPaymentFilter{Status: q.Get("status"), Limit: vendorPageSize}
	fields := map[string]string{}
	switch f.Status {
	case "", store.PaymentOpen, store.PaymentScheduled, store.PaymentPartiallyPaid, store.PaymentPaid, store.PaymentCancelled:
	default:
		fields["status"] = "must be OPEN, SCHEDULED, PARTIALLY_PAID, PAID or CANCELLED"
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			fields["limit"] = "must be 1 to 200"
		} else {
			f.Limit = n
		}
	}
	if v := q.Get("cursor"); v != "" {
		date, id, ok := decodePaymentCursor(v)
		if !ok {
			fields["cursor"] = "is not a cursor issued by this API"
		}
		f.CursorDate, f.CursorID = date, id
	}
	if len(fields) > 0 {
		problem := httpx.NewProblem(http.StatusBadRequest, "PAY-0001", "request failed validation")
		problem.Errors = fields
		fail(w, r, problem)
		return
	}
	rows, err := a.vendorPayments.VendorPayments(r.Context(), strings.ToLower(p.VendorID), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	page := struct {
		Items      []vendorPaymentBody `json:"items"`
		NextCursor string              `json:"nextCursor,omitempty"`
	}{Items: make([]vendorPaymentBody, 0, len(rows))}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		page.NextCursor = encodePaymentCursor(rows[len(rows)-1].Cursor())
	}
	for _, v := range rows {
		page.Items = append(page.Items, vendorPaymentBody{InvoiceID: v.InvoiceID, InvoiceNumber: v.InvoiceNumber, InvoiceDate: v.InvoiceDate, DueDate: v.DueDate, Currency: v.Currency,
			Amount: v.Amount.String(), PaidAmount: v.PaidAmount.String(), OpenAmount: v.OpenAmount.String(), Status: v.Status, ScheduledFor: v.ScheduledFor, PaidAt: v.PaidAt})
	}
	httpx.JSON(w, http.StatusOK, page)
}
