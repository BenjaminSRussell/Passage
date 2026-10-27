package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// operationCommand serves POST /v1/production/orders/{id}/operations/{operNumber}:finish
// and :reopen (FR-21.6). Finish closes an operation to further route cards without
// posting one; reopen opens a finished operation again. Both need the order's
// If-Match version and an Idempotency-Key, like every order command, and the
// duty that posts route cards.
func (a *API) operationCommand(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	raw, name, found := strings.Cut(chi.URLParam(r, "operCommand"), ":")
	oper, err := strconv.ParseInt(raw, 10, 32)
	action := domain.OperAction(name)
	if !found || err != nil || oper < 1 || !action.Valid() {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, unknownActionCode,
			"use :finish or :reopen on an operation number, for example operations/20:finish"))
		return
	}
	ifMatch, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	if err := decodeOptional(r, &struct{}{}); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	order, res, err := a.store.ApplyOperationCommand(r.Context(), store.OperationCommand{
		OrderID: id, OperNumber: int(oper), Action: action, IfMatch: ifMatch, Actor: p.UserID,
		TraceID: middleware.GetReqID(r.Context()), IdempotencyKey: key, RequestHash: hashOf([]any{oper, name}),
	})
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(order.Version))
	switch {
	case res.Replayed:
		a.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
	case res.Changed:
		a.moved.Add(1)
	}
	httpx.JSON(w, http.StatusOK, order)
}
