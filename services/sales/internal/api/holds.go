package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const maxReasonCode = 80

// applyHoldRequest is the body of POST /v1/sales/orders/{id}/holds.
type applyHoldRequest struct {
	HoldType   string `json:"holdType"`
	ReasonCode string `json:"reasonCode"`
}

// applyHold places a MANUAL, FRAUD or PRICE_REVIEW hold (FR-27.8, duty
// SalesOrderHold). CREDIT holds are placed only by the credit check. If-Match
// carries the order ETag, so a repeated request cannot apply the hold twice.
func (a *API) applyHold(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		var req applyHoldRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		fields := map[string]string{}
		if !domain.CanApplyHold(req.HoldType) {
			fields["holdType"] = "must be MANUAL, FRAUD or PRICE_REVIEW"
		}
		if n := len(req.ReasonCode); n == 0 || n > maxReasonCode {
			fields["reasonCode"] = "must be 1 to 80 characters"
		}
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		p, _ := tenant.FromContext(r.Context())
		order, err := a.store.ApplyHold(r.Context(), id, version,
			store.HoldRequest{HoldType: req.HoldType, ReasonCode: req.ReasonCode}, p.UserID)
		if err != nil {
			a.fail(w, r, "apply hold", err)
			return
		}
		a.applied.Add(1)
		setETag(w, order.Version)
		httpx.JSON(w, http.StatusCreated, order)
	})(w, r)
}

const maxRejectComment = 500

// rejectHoldRequest is the body of POST .../holds/{holdId}:reject.
type rejectHoldRequest struct {
	Comment string `json:"comment"`
}

// rejectHold rejects an ACTIVE CREDIT hold of an unconfirmed order with a
// required comment and cancels the order (PRD §5.27.6, duty CreditRelease).
// If-Match carries the order ETag; a retry therefore gets 412 and never rejects
// twice.
func (a *API) rejectHold(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		holdID := chi.URLParam(r, "holdId")
		if !ids.Valid(holdID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeHoldNotFound, "hold not found"))
			return
		}
		var req rejectHoldRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		req.Comment = strings.TrimSpace(req.Comment)
		if n := len([]rune(req.Comment)); n == 0 || n > maxRejectComment {
			validationFailed(w, r, map[string]string{"comment": "must be 1 to 500 characters"})
			return
		}
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		p, _ := tenant.FromContext(r.Context())
		order, err := a.store.RejectHold(r.Context(), id, holdID, version, req.Comment, p.UserID, a.now())
		if err != nil {
			a.fail(w, r, "reject hold", err)
			return
		}
		a.rejected.Add(1)
		a.canceled.Add(1)
		setETag(w, order.Version)
		httpx.JSON(w, http.StatusOK, order)
	})(w, r)
}

// listHolds is the release queue (PRD §5.27.6): ACTIVE holds in the caller's
// scope, oldest first, with the failed credit rule and the customer's limit.
// holdType filters by type (CREDIT is the credit release queue).
func (a *API) listHolds(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.HoldQueueFilter{LegalEntityID: legalEntity, HoldType: q.Get("holdType")}
	fields := map[string]string{}
	if f.HoldType != "" && !holdTypes[f.HoldType] {
		fields["holdType"] = "must be CREDIT, FRAUD, MANUAL or PRICE_REVIEW"
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
	items, err := a.store.ListHolds(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list holds", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
