package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	dutyHoldManage     = "payment-hold.manage"
	dutyVendorBlock    = "vendor-block.manage"
	pathVendors        = "/v1/payables/vendors"
	maxBlockReasonSize = 500
)

type holdCounters struct {
	held, released, blocked, unblocked atomic.Int64
}

func (a *API) registerHolds(r chi.Router) {
	r.With(authz.RequireDuty(dutyHoldManage)).Post(pathOpenItems+"/{id}:hold", a.setHold(true))
	r.With(authz.RequireDuty(dutyHoldManage)).Post(pathOpenItems+"/{id}:release", a.setHold(false))
	r.With(authz.RequireDuty(dutyVendorBlock)).Post(pathVendors+"/{id}:block", a.blockVendor)
	r.With(authz.RequireDuty(dutyVendorBlock)).Post(pathVendors+"/{id}:unblock", a.unblockVendor)
}

func (a *API) identified(w http.ResponseWriter, r *http.Request, what string) (string, string, bool) {
	if !a.validTenant(w, r) {
		return "", "", false
	}
	caller, _ := tenant.FromContext(r.Context())
	id := chi.URLParam(r, "id")
	switch {
	case caller.UserID == "":
		a.problem(w, r, http.StatusForbidden, "AUTH-0003", what+" needs an identified user")
		return "", "", false
	case !ids.Valid(id):
		a.invalid(w, r, map[string]string{"id": "must be a UUID"})
		return "", "", false
	}
	return caller.UserID, id, true
}

func (a *API) setHold(held bool) http.HandlerFunc {
	action, counter := "hold released", &a.proposals.holds.released
	if held {
		action, counter = "hold placed", &a.proposals.holds.held
	}
	return func(w http.ResponseWriter, r *http.Request) {
		user, id, ok := a.identified(w, r, "a payment hold change")
		if !ok {
			return
		}
		ctx := r.Context()
		err := a.proposals.store.SetPaymentHold(ctx, id, held)
		if errors.Is(err, store.ErrNotFound) {
			a.problem(w, r, http.StatusNotFound, codeNotFound, "open item not found")
			return
		}
		if err != nil {
			a.fault(w, r, "payment hold change failed", err)
			return
		}
		item, err := a.proposals.store.Get(ctx, id)
		if err != nil {
			a.fault(w, r, "open item get failed", err)
			return
		}
		counter.Add(1)
		slog.Info("audit: open item "+action, "openItemId", id, "actor", user, "correlationId", middleware.GetReqID(ctx))
		httpx.JSON(w, http.StatusOK, openItemBody{OpenItem: item})
	}
}

type vendorBlockRequest struct {
	Reason string `json:"reason"`
}

type vendorBlockBody struct {
	VendorID string `json:"vendorId"`
	Blocked  bool   `json:"blocked"`
	Reason   string `json:"reason,omitempty"`
}

func (a *API) blockVendor(w http.ResponseWriter, r *http.Request) {
	user, id, ok := a.identified(w, r, "a vendor block")
	if !ok {
		return
	}
	var req vendorBlockRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || utf8.RuneCountInString(req.Reason) > maxBlockReasonSize {
		a.invalid(w, r, map[string]string{"reason": "is required, at most 500 characters"})
		return
	}
	if err := a.proposals.store.BlockVendor(r.Context(), id, req.Reason); err != nil {
		a.fault(w, r, "vendor block failed", err)
		return
	}
	a.proposals.holds.blocked.Add(1)
	slog.Info("audit: vendor blocked for payment", "vendorId", id, "actor", user, "reason", req.Reason, "correlationId", middleware.GetReqID(r.Context()))
	httpx.JSON(w, http.StatusOK, vendorBlockBody{VendorID: id, Blocked: true, Reason: req.Reason})
}

func (a *API) unblockVendor(w http.ResponseWriter, r *http.Request) {
	user, id, ok := a.identified(w, r, "a vendor unblock")
	if !ok {
		return
	}
	err := a.proposals.store.UnblockVendor(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		a.problem(w, r, http.StatusNotFound, codeNotFound, "vendor is not blocked")
		return
	}
	if err != nil {
		a.fault(w, r, "vendor unblock failed", err)
		return
	}
	a.proposals.holds.unblocked.Add(1)
	slog.Info("audit: vendor unblocked for payment", "vendorId", id, "actor", user, "correlationId", middleware.GetReqID(r.Context()))
	httpx.JSON(w, http.StatusOK, vendorBlockBody{VendorID: id})
}
