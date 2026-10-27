package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

type pegRequest struct {
	ParentOrderID    string `json:"parentOrderId"`
	DependentOrderID string `json:"dependentOrderId"`
}

// createPeg records that one order consumes another's output (FR-23.8), so a
// move with propagate pushes the dependent behind the parent.
func (a *API) createPeg(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req pegRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	req.ParentOrderID, req.DependentOrderID = strings.ToLower(req.ParentOrderID), strings.ToLower(req.DependentOrderID)
	fields := map[string]string{}
	if !ids.Valid(req.ParentOrderID) {
		fields["parentOrderId"] = "must be a UUID"
	}
	if !ids.Valid(req.DependentOrderID) {
		fields["dependentOrderId"] = "must be a UUID"
	}
	if len(fields) == 0 && req.ParentOrderID == req.DependentOrderID {
		fields["dependentOrderId"] = "must differ from parentOrderId"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	peg, isNew, err := a.store.CreatePeg(r.Context(), store.Peg{ParentOrderID: req.ParentOrderID, DependentOrderID: req.DependentOrderID}, idem)
	if err != nil {
		slog.Error("create peg failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	created(w, peg, isNew)
}

func (a *API) listPegs(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	orderID := strings.ToLower(r.URL.Query().Get("orderId"))
	if orderID != "" && !ids.Valid(orderID) {
		invalid(w, r, map[string]string{"orderId": "must be a UUID"})
		return
	}
	items, err := a.store.ListPegs(r.Context(), orderID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) deletePeg(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		invalid(w, r, map[string]string{"id": "must be a UUID"})
		return
	}
	if err := a.store.DeletePeg(r.Context(), id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
