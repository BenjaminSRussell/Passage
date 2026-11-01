package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	changeRequests = "/v1/sales/change-requests"
	changePolicy   = "/v1/sales/customers/{customerId}/change-policy"
)

var changeStatuses = map[string]bool{store.ChangePending: true, store.ChangeApproved: true, store.ChangeRejected: true, store.ChangeWithdrawn: true}

// decisionRequest is the body of :approve (comment optional) and :reject
// (comment required).
type decisionRequest struct {
	Comment string `json:"comment"`
}

type changePolicyRequest struct {
	ApprovalRequired *bool `json:"approvalRequired"`
}

func changeRequestRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeChangeNotFound, "change request not found"))
			return
		}
		next(w, r, id)
	}
}

// listChangeRequests is the approval queue (status=PENDING, oldest first) and
// the history of an order's changes (orderId).
func (a *API) listChangeRequests(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.ChangeRequestFilter{LegalEntityID: legalEntity, Status: q.Get("status"), OrderID: q.Get("orderId")}
	fields := map[string]string{}
	if f.Status != "" && !changeStatuses[f.Status] {
		fields["status"] = "must be PENDING, APPROVED, REJECTED or WITHDRAWN"
	}
	if f.OrderID != "" && !ids.Valid(f.OrderID) {
		fields["orderId"] = "must be a UUID"
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
	items, err := a.store.ListChangeRequests(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list change requests", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getChangeRequest(w http.ResponseWriter, r *http.Request) {
	changeRequestRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		cr, err := a.store.GetChangeRequest(r.Context(), id)
		if err != nil {
			a.fail(w, r, "get change request", err)
			return
		}
		setETag(w, cr.Version)
		httpx.JSON(w, http.StatusOK, cr)
	})(w, r)
}

// decide reads If-Match and the comment shared by :approve and :reject.
func decide(w http.ResponseWriter, r *http.Request, commentRequired bool) (int, string, bool) {
	version, ok := ifMatch(w, r)
	if !ok {
		return 0, "", false
	}
	var req decisionRequest
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return 0, "", false
		}
	}
	if len(req.Comment) > maxChangeReason {
		validationFailed(w, r, map[string]string{"comment": "must be at most 500 characters"})
		return 0, "", false
	}
	if commentRequired && req.Comment == "" {
		validationFailed(w, r, map[string]string{"comment": "is required"})
		return 0, "", false
	}
	return version, req.Comment, true
}

// approveChangeRequest applies the stored change and records the approval in
// one transaction (FR-27.9). The response is the decided request and the
// order after the change.
func (a *API) approveChangeRequest(w http.ResponseWriter, r *http.Request) {
	changeRequestRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, comment, ok := decide(w, r, false)
		if !ok {
			return
		}
		p, _ := tenant.FromContext(r.Context())
		res, err := a.store.ApproveChangeRequest(r.Context(), id, version, comment, p.UserID, a.now())
		if err != nil {
			a.fail(w, r, "approve change request", err)
			return
		}
		if res.Request.Kind == store.KindCancel {
			a.canceled.Add(1)
		} else {
			a.changed.Add(1)
		}
		setETag(w, res.Request.Version)
		httpx.JSON(w, http.StatusOK, res)
	})(w, r)
}

func (a *API) rejectChangeRequest(w http.ResponseWriter, r *http.Request) {
	changeRequestRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, comment, ok := decide(w, r, true)
		if !ok {
			return
		}
		p, _ := tenant.FromContext(r.Context())
		cr, err := a.store.RejectChangeRequest(r.Context(), id, version, comment, p.UserID, a.now())
		if err != nil {
			a.fail(w, r, "reject change request", err)
			return
		}
		setETag(w, cr.Version)
		httpx.JSON(w, http.StatusOK, cr)
	})(w, r)
}

// withdrawChangeRequest lets the requester take back a PENDING request. The
// comment is optional.
func (a *API) withdrawChangeRequest(w http.ResponseWriter, r *http.Request) {
	changeRequestRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, comment, ok := decide(w, r, false)
		if !ok {
			return
		}
		p, _ := tenant.FromContext(r.Context())
		cr, err := a.store.WithdrawChangeRequest(r.Context(), id, version, comment, p.UserID, a.now())
		if err != nil {
			a.fail(w, r, "withdraw change request", err)
			return
		}
		setETag(w, cr.Version)
		httpx.JSON(w, http.StatusOK, cr)
	})(w, r)
}

func customerRoute(w http.ResponseWriter, r *http.Request) (le, customer string, ok bool) {
	if !validTenant(w, r) {
		return "", "", false
	}
	customer = chi.URLParam(r, "customerId")
	if !ids.Valid(customer) {
		validationFailed(w, r, map[string]string{"customerId": "must be a UUID"})
		return "", "", false
	}
	le, ok = creditLegalEntity(w, r, true)
	return le, customer, ok
}

// getChangePolicy returns the customer's change approval switch, off at
// version 0 when never saved.
func (a *API) getChangePolicy(w http.ResponseWriter, r *http.Request) {
	le, customer, ok := customerRoute(w, r)
	if !ok {
		return
	}
	pol, err := a.store.GetChangePolicy(r.Context(), le, customer)
	if err != nil {
		a.fail(w, r, "get change policy", err)
		return
	}
	setETag(w, pol.Version)
	httpx.JSON(w, http.StatusOK, pol)
}

// putChangePolicy creates or replaces the switch. If-Match carries the current
// ETag, or "0" to create.
func (a *API) putChangePolicy(w http.ResponseWriter, r *http.Request) {
	le, customer, ok := customerRoute(w, r)
	if !ok {
		return
	}
	var req changePolicyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.ApprovalRequired == nil {
		validationFailed(w, r, map[string]string{"approvalRequired": "is required"})
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	saved, err := a.store.PutChangePolicy(r.Context(), store.ChangePolicy{LegalEntityID: le, CustomerID: customer, ApprovalRequired: *req.ApprovalRequired}, version, p.UserID)
	if err != nil {
		a.fail(w, r, "put change policy", err)
		return
	}
	setETag(w, saved.Version)
	httpx.JSON(w, http.StatusOK, saved)
}
