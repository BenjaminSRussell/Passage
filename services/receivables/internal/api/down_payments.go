package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const (
	dutySettle = "ARSettle"
	dutyRefund = "ARRefund"
)

var downPaymentStatuses = map[string]bool{domain.DownRequested: true, domain.DownReceived: true, domain.DownCleared: true,
	domain.DownReleased: true, domain.DownCancelled: true}

// downPaymentActionDuty maps each custom method to its §5.30.17 duty: money in
// is ARPaymentPost, clearing against items is ARSettle, a refund is ARRefund.
var downPaymentActionDuty = map[string]string{"receive": dutyPayment, "cancel": dutyPayment, "clear": dutySettle, "release": dutyRefund}

// downPaymentRoutes mounts down payments and security deposits (FR-30.21).
func (a *API) downPaymentRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyPayment)).Post("/down-payments", a.createDownPayment)
	r.With(authz.RequireDuty(dutyView)).Get("/down-payments", a.listDownPayments)
	r.With(authz.RequireDuty(dutyView)).Get("/down-payments/{ref}", a.getDownPayment)
	// :receive, :cancel, :clear and :release share one route; downPaymentAction checks the duty.
	r.Post("/down-payments/{ref}", a.downPaymentAction)
}

type createDownPaymentRequest struct {
	LegalEntityID string          `json:"legalEntityId"`
	CustomerID    string          `json:"customerId"`
	SalesOrderID  *string         `json:"salesOrderId"`
	Kind          string          `json:"kind"`
	Currency      string          `json:"currency"`
	Amount        decimal.Decimal `json:"amount"`
}

// createDownPayment handles POST /down-payments: a request, with no posting.
func (a *API) createDownPayment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requiredKey(w, r)
	if !ok {
		return
	}
	var req createDownPaymentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if req.SalesOrderID != nil && !ids.Valid(*req.SalesOrderID) {
		fields["salesOrderId"] = "must be a UUID"
	}
	if !domain.ValidDownPaymentKind(req.Kind) {
		fields["kind"] = "must be DOWN_PAYMENT or SECURITY_DEPOSIT"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if !req.Amount.IsPositive() {
		fields["amount"] = "must be a positive decimal"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.CreateDownPayment(r.Context(), store.DownPayment{LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID,
		SalesOrderID: req.SalesOrderID, Kind: req.Kind, Currency: req.Currency, Amount: req.Amount}, key)
	if err != nil {
		a.fail("create down payment", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

// listDownPayments handles GET /down-payments?customer=&status=&kind=.
func (a *API) listDownPayments(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.DownPaymentFilter{CustomerID: q.Get("customer"), Status: q.Get("status"), Kind: q.Get("kind")}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		badQuery(w, r, "customer", "must be a UUID")
		return
	}
	if f.Status != "" && !downPaymentStatuses[f.Status] {
		badQuery(w, r, "status", "must be REQUESTED, RECEIVED, CLEARED, RELEASED or CANCELLED")
		return
	}
	if f.Kind != "" && !domain.ValidDownPaymentKind(f.Kind) {
		badQuery(w, r, "kind", "must be DOWN_PAYMENT or SECURITY_DEPOSIT")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListDownPayments(r.Context(), f, limit)
	if err != nil {
		a.fail("list down payments", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// getDownPayment handles GET /down-payments/{id}.
func (a *API) getDownPayment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "ref")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeDownPaymentNotFound, "down payment not found"))
		return
	}
	out, err := a.store.GetDownPayment(r.Context(), id)
	if err != nil {
		a.fail("get down payment", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

type downPaymentActionRequest struct {
	ReceivedDate string           `json:"receivedDate"`
	OpenItemID   string           `json:"openItemId"`
	Amount       *decimal.Decimal `json:"amount"`
}

// downPaymentAction handles POST /down-payments/{id}:receive, :cancel, :clear and :release. All need If-Match.
func (a *API) downPaymentAction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "ref"), ":")
	duty, known := downPaymentActionDuty[action]
	if !found || !ids.Valid(id) || !known {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeDownPaymentNotFound, "unknown down payment or action"))
		return
	}
	if !require(w, r, duty) {
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req downPaymentActionRequest
	if err := decodeOptional(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var out store.DownPayment
	switch action {
	case "receive":
		if req.ReceivedDate != "" {
			if _, err := store.ParseDay(req.ReceivedDate); err != nil {
				writeFields(w, r, map[string]string{"receivedDate": "must be a YYYY-MM-DD date"})
				return
			}
		}
		out, err = a.store.ReceiveDownPayment(r.Context(), id, version, req.ReceivedDate)
	case "cancel":
		out, err = a.store.CancelDownPayment(r.Context(), id, version)
	case "clear":
		fields := map[string]string{}
		if !ids.Valid(req.OpenItemID) {
			fields["openItemId"] = "must be a UUID"
		}
		if req.Amount != nil && !req.Amount.IsPositive() {
			fields["amount"] = "must be a positive decimal when set"
		}
		if len(fields) > 0 {
			writeFields(w, r, fields)
			return
		}
		out, err = a.store.ClearDownPayment(r.Context(), id, version, req.OpenItemID, req.Amount)
	case "release":
		out, err = a.store.ReleaseDownPayment(r.Context(), id, version)
	}
	if err != nil {
		a.fail(action+" down payment", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}
