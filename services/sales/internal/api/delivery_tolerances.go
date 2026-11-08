package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	deliveryTolerances = "/v1/sales/delivery-tolerances"
	toleranceResolve   = "/v1/sales/delivery-tolerances:resolve"
)

// deliveryToleranceRequest is the body of POST /v1/sales/delivery-tolerances: the
// tolerance of one product for one customer or, when customerId is absent, for
// every customer of the legal entity (BR-27.9, BR-28.7). The percentages are
// decimal strings (0 when absent); partialDelivery defaults to ALLOWED.
type deliveryToleranceRequest struct {
	LegalEntityID    string  `json:"legalEntityId"`
	ProductID        string  `json:"productId"`
	CustomerID       *string `json:"customerId"`
	OverdeliveryPct  string  `json:"overdeliveryPct"`
	UnderdeliveryPct string  `json:"underdeliveryPct"`
	PartialDelivery  string  `json:"partialDelivery"`
}

// replaceDeliveryToleranceRequest is the body of PUT /v1/sales/delivery-tolerances/{id}.
type replaceDeliveryToleranceRequest struct {
	OverdeliveryPct  string `json:"overdeliveryPct"`
	UnderdeliveryPct string `json:"underdeliveryPct"`
	PartialDelivery  string `json:"partialDelivery"`
}

// toleranceOf parses the figures of a request; an absent percentage is 0 and an
// absent policy ALLOWED.
func toleranceOf(over, under, partial string) (domain.DeliveryTolerance, map[string]string) {
	f := map[string]string{}
	pct := func(name, s string) decimal.Decimal {
		if s == "" {
			return decimal.Zero
		}
		v, err := decimal.NewFromString(s)
		if err != nil {
			f[name] = "must be a decimal such as 2.5"
			return decimal.Zero
		}
		return v
	}
	t := domain.DeliveryTolerance{OverdeliveryPct: pct("overdeliveryPct", over), UnderdeliveryPct: pct("underdeliveryPct", under), PartialDelivery: partial}
	if t.PartialDelivery == "" {
		t.PartialDelivery = domain.PartialAllowed
	}
	for k, v := range t.Validate() {
		if f[k] == "" {
			f[k] = v
		}
	}
	return t, f
}

// createDeliveryTolerance stores a tolerance rule. A repeat of Idempotency-Key
// with the same body returns the stored rule with 200.
func (a *API) createDeliveryTolerance(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	var req deliveryToleranceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	tol, fields := toleranceOf(req.OverdeliveryPct, req.UnderdeliveryPct, req.PartialDelivery)
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	if req.CustomerID != nil && !ids.Valid(*req.CustomerID) {
		fields["customerId"] = "must be a UUID when given"
	}
	if key == "" {
		fields[idempotencyKey] = "is required"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	hash, err := domain.RequestHash(req)
	if err != nil {
		a.fail(w, r, "hash request", err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	saved, created, err := a.store.CreateDeliveryTolerance(r.Context(), store.NewDeliveryTolerance{LegalEntityID: req.LegalEntityID, ProductID: req.ProductID,
		CustomerID: req.CustomerID, Tolerance: tol, IdempotencyKey: key, RequestHash: hash, Actor: p.UserID})
	if err != nil {
		a.fail(w, r, "create delivery tolerance", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	setETag(w, saved.Version)
	httpx.JSON(w, status, saved)
}

// listDeliveryTolerances lists the rules of a legal entity (or of every legal
// entity in the caller's scope), optionally of one product or customer.
func (a *API) listDeliveryTolerances(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.DeliveryToleranceFilter{LegalEntityID: legalEntity, ProductID: q.Get("productId"), CustomerID: q.Get("customerId")}
	fields := map[string]string{}
	if f.ProductID != "" && !ids.Valid(f.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fields["customerId"] = "must be a UUID"
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
	items, err := a.store.ListDeliveryTolerances(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list delivery tolerances", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func toleranceRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeToleranceNotFound, "delivery tolerance not found"))
			return
		}
		next(w, r, id)
	}
}

// getDeliveryTolerance returns one rule.
func (a *API) getDeliveryTolerance(w http.ResponseWriter, r *http.Request) {
	toleranceRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		t, err := a.store.GetDeliveryTolerance(r.Context(), id)
		if err != nil {
			a.fail(w, r, "get delivery tolerance", err)
			return
		}
		setETag(w, t.Version)
		httpx.JSON(w, http.StatusOK, t)
	})(w, r)
}

// replaceDeliveryTolerance sets new figures under If-Match.
func (a *API) replaceDeliveryTolerance(w http.ResponseWriter, r *http.Request) {
	toleranceRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req replaceDeliveryToleranceRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		tol, fields := toleranceOf(req.OverdeliveryPct, req.UnderdeliveryPct, req.PartialDelivery)
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		saved, err := a.store.ReplaceDeliveryTolerance(r.Context(), id, version, tol, p.UserID, a.now().UTC().Truncate(time.Microsecond))
		if err != nil {
			a.fail(w, r, "replace delivery tolerance", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, saved)
	})(w, r)
}

// deleteDeliveryTolerance removes a rule (404 SLS-0127 when absent).
func (a *API) deleteDeliveryTolerance(w http.ResponseWriter, r *http.Request) {
	toleranceRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		p, _ := tenant.FromContext(r.Context())
		if err := a.store.DeleteDeliveryTolerance(r.Context(), id, p.UserID); err != nil {
			a.fail(w, r, "delete delivery tolerance", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}

// resolvedToleranceView is the response of the resolve call.
type resolvedToleranceView struct {
	Source           string           `json:"source"`
	ToleranceID      string           `json:"toleranceId,omitempty"`
	OverdeliveryPct  decimal.Decimal  `json:"overdeliveryPct"`
	UnderdeliveryPct decimal.Decimal  `json:"underdeliveryPct"`
	PartialDelivery  string           `json:"partialDelivery"`
	OrderedQty       *decimal.Decimal `json:"orderedQty,omitempty"`
	MinQty           *decimal.Decimal `json:"minQty,omitempty"`
	MaxQty           *decimal.Decimal `json:"maxQty,omitempty"`
}

// resolveDeliveryTolerance answers what a product may ship for a customer: the
// customer's own rule, else the all-customers rule, else the default (exact
// quantities, partial delivery allowed). With orderedQty it also returns the
// least and most a shipment may carry, for the shipping check of BR-28.7
// (fulfillment still checks its own tolerance table; see NOT-IMPLEMENTED.md).
func (a *API) resolveDeliveryTolerance(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, true)
	if !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	if !ids.Valid(q.Get("productId")) {
		fields["productId"] = "must be a UUID"
	}
	if !ids.Valid(q.Get("customerId")) {
		fields["customerId"] = "must be a UUID"
	}
	var ordered *decimal.Decimal
	if s := q.Get("orderedQty"); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil {
			fields["orderedQty"] = "must be a decimal"
		} else if err := domain.ValidateQuantity(v); err != nil {
			fields["orderedQty"] = err.Error()
		} else {
			ordered = &v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	res, err := a.store.ResolveDeliveryTolerance(r.Context(), legalEntity, q.Get("productId"), q.Get("customerId"))
	if err != nil {
		a.fail(w, r, "resolve delivery tolerance", err)
		return
	}
	view := resolvedToleranceView{Source: res.Source, ToleranceID: res.ToleranceID, OverdeliveryPct: res.Tolerance.OverdeliveryPct,
		UnderdeliveryPct: res.Tolerance.UnderdeliveryPct, PartialDelivery: res.Tolerance.PartialDelivery}
	if ordered != nil {
		lo, hi := res.Tolerance.Range(*ordered)
		view.OrderedQty, view.MinQty, view.MaxQty = ordered, &lo, &hi
	}
	httpx.JSON(w, http.StatusOK, view)
}

func (a *API) registerDeliveryTolerances(r chi.Router) {
	r.With(authz.RequireDuty(dutyParameter)).Post(deliveryTolerances, a.createDeliveryTolerance)
	r.With(authz.RequireDuty(dutyRead)).Get(deliveryTolerances, a.listDeliveryTolerances)
	r.With(authz.RequireDuty(dutyRead)).Get(toleranceResolve, a.resolveDeliveryTolerance)
	r.With(authz.RequireDuty(dutyRead)).Get(deliveryTolerances+"/{id}", a.getDeliveryTolerance)
	r.With(authz.RequireDuty(dutyParameter)).Put(deliveryTolerances+"/{id}", a.replaceDeliveryTolerance)
	r.With(authz.RequireDuty(dutyParameter)).Delete(deliveryTolerances+"/{id}", a.deleteDeliveryTolerance)
}
