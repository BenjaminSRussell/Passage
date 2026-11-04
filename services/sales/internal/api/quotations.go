package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const maxLostReason = 200

// createQuotationRequest is the body of POST /v1/sales/quotations (FR-27.1).
// invoiceAccountId defaults to the customer; validUntil and probabilityPct to
// the legal entity's quotation_expiry_days (30 unless set) and
// quotation_probability_default (50 unless set).
type createQuotationRequest struct {
	LegalEntityID    string             `json:"legalEntityId"`
	CustomerID       string             `json:"customerId"`
	InvoiceAccountID string             `json:"invoiceAccountId,omitempty"`
	Currency         string             `json:"currency"`
	ValidUntil       *string            `json:"validUntil,omitempty"`
	ProbabilityPct   *int               `json:"probabilityPct,omitempty"`
	Lines            []orderLineRequest `json:"lines"`
}

// convertQuotationRequest selects the lines to convert; an empty or absent
// list converts every open line. listingOverrideReason, with duty
// sales.order.approve, accepts a line the product listing denies (FR-27.15).
type convertQuotationRequest struct {
	LineNos               []int  `json:"lineNos"`
	ListingOverrideReason string `json:"listingOverrideReason,omitempty"`
}

// loseQuotationRequest records why a quotation was lost.
type loseQuotationRequest struct {
	Reason string `json:"reason"`
}

// buildQuotation validates a quotation request and computes line nets. Field
// errors are keyed by JSON path, as for orders.
func buildQuotation(req createQuotationRequest, today time.Time) (store.NewQuotation, map[string]string) {
	f := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		f["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.CustomerID) {
		f["customerId"] = "must be a UUID"
	}
	invoice := req.InvoiceAccountID
	if invoice == "" {
		invoice = req.CustomerID
	}
	if !ids.Valid(invoice) {
		f["invoiceAccountId"] = "must be a UUID"
	}
	if !currencyCode.MatchString(req.Currency) {
		f["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxLines {
		f["lines"] = fmt.Sprintf("must have 1 to %d lines", maxLines)
	}
	var validUntil time.Time // zero: the legal entity's quotation validity applies
	if req.ValidUntil != nil {
		day, err := time.Parse(dayLayout, *req.ValidUntil)
		switch {
		case err != nil:
			f["validUntil"] = "must be a date such as 2027-03-04"
		case day.Before(domain.DayOf(today)):
			f["validUntil"] = "must not be before today"
		case day.After(domain.QuotationValidUntil(today, domain.MaxQuotationExpiryDays)):
			f["validUntil"] = fmt.Sprintf("must be at most %d days from today", domain.MaxQuotationExpiryDays)
		default:
			validUntil = day
		}
	}
	probability := req.ProbabilityPct // nil: the legal entity's quotation_probability_default applies
	if probability != nil {
		if err := domain.ValidateProbability(*probability); err != nil {
			f["probabilityPct"] = err.Error()
		}
	}
	in := store.NewQuotation{LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID, InvoiceAccountID: invoice,
		Currency: req.Currency, ValidUntil: validUntil, IssuedAt: today, ProbabilityPct: probability}
	for i, l := range req.Lines {
		path := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		if l.DeliveryDateControl != nil {
			f[path("deliveryDateControl")] = "a quotation line has no delivery date control; set it on the order line"
		}
		if l.CatchWeight != nil {
			// The quotation store keeps no catch-weight spec, and an order made
			// from the quotation would carry a weight-priced LineNet on a plain
			// line (FR-24.7): quote catch-weight items on an order instead.
			f[path("catchWeight")] = "a quotation line cannot be a catch-weight item; set catchWeight on the order line"
			continue
		}
		if line, ok := buildLine(i, l, f, path); ok {
			in.Lines = append(in.Lines, line)
		}
	}
	return in, f
}

// createQuotation stores a DRAFT quotation. A repeat of Idempotency-Key with
// the same body returns the stored quotation with 200.
func (a *API) createQuotation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	var req createQuotationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := buildQuotation(req, a.now())
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
	saved, created, err := a.store.CreateQuotation(r.Context(), in)
	if err != nil {
		a.fail(w, r, "create quotation", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	setETag(w, saved.Version)
	httpx.JSON(w, status, saved)
}

// listQuotations returns quotations in the caller's scope, newest first.
func (a *API) listQuotations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.QuotationFilter{LegalEntityID: legalEntity, CustomerID: q.Get("customerId"), Status: q.Get("status")}
	fields := map[string]string{}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if f.Status != "" && !domain.IsQuotationStatus(f.Status) {
		fields["status"] = "must be DRAFT, SENT, CONVERTED, LOST, EXPIRED or CANCELLED"
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
	items, err := a.store.ListQuotations(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list quotations", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// quotationRoute parses the quotation id from the path.
func quotationRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeQuotationNotFound, "quotation not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getQuotation(w http.ResponseWriter, r *http.Request) {
	quotationRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		q, err := a.store.GetQuotation(r.Context(), id)
		if err != nil {
			a.fail(w, r, "get quotation", err)
			return
		}
		setETag(w, q.Version)
		httpx.JSON(w, http.StatusOK, q)
	})(w, r)
}

// sendQuotation moves a DRAFT quotation that is still valid to SENT.
// If-Match carries the version.
func (a *API) sendQuotation(w http.ResponseWriter, r *http.Request) {
	quotationRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		q, err := a.store.SendQuotation(r.Context(), id, version, a.now())
		if err != nil {
			a.fail(w, r, "send quotation", err)
			return
		}
		setETag(w, q.Version)
		httpx.JSON(w, http.StatusOK, q)
	})(w, r)
}

// loseQuotation marks a DRAFT or SENT quotation LOST with a reason.
func (a *API) loseQuotation(w http.ResponseWriter, r *http.Request) {
	quotationRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req loseQuotationRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		reason := strings.TrimSpace(req.Reason)
		if n := len(reason); n == 0 || n > maxLostReason {
			validationFailed(w, r, map[string]string{"reason": fmt.Sprintf("must be 1 to %d characters", maxLostReason)})
			return
		}
		q, err := a.store.LoseQuotation(r.Context(), id, version, reason)
		if err != nil {
			a.fail(w, r, "lose quotation", err)
			return
		}
		setETag(w, q.Version)
		httpx.JSON(w, http.StatusOK, q)
	})(w, r)
}

// convertQuotation turns the selected open lines into one DRAFT order
// (FR-27.1, AC-27.6) and returns 201 with the quotation and the order.
// If-Match carries the quotation version; the body is optional.
func (a *API) convertQuotation(w http.ResponseWriter, r *http.Request) {
	quotationRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req convertQuotationRequest
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &req); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		fields := map[string]string{}
		if len(req.LineNos) > maxLines {
			fields["lineNos"] = fmt.Sprintf("must have at most %d line numbers", maxLines)
		}
		for _, n := range req.LineNos {
			if n <= 0 {
				fields["lineNos"] = "line numbers must be positive"
			}
		}
		if len(req.ListingOverrideReason) > maxOverrideReason {
			fields["listingOverrideReason"] = fmt.Sprintf("must be at most %d characters", maxOverrideReason)
		}
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		res, err := a.store.ConvertQuotation(r.Context(), id, store.ConvertRequest{Version: version, LineNos: req.LineNos, Actor: p.UserID,
			CanOverrideListing: p.HasDuty(dutyOrderApprove), ListingOverrideReason: req.ListingOverrideReason}, a.now())
		if err != nil {
			a.fail(w, r, "convert quotation", err)
			return
		}
		a.created.Add(1)
		setETag(w, res.Quotation.Version)
		httpx.JSON(w, http.StatusCreated, res)
	})(w, r)
}
