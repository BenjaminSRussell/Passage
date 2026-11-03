package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const deliveryTerms = "/v1/sales/delivery-terms"

// deliveryTermsRequest is the body of POST /v1/sales/delivery-terms: a
// customer's transport days and receiving weekdays (FR-27.3, BR-27.2).
type deliveryTermsRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	CustomerID    string `json:"customerId"`
	TransportDays int    `json:"transportDays"`
	ReceivingDays []int  `json:"receivingDays"`
}

// replaceDeliveryTermsRequest is the body of PUT /v1/sales/delivery-terms/{id}.
type replaceDeliveryTermsRequest struct {
	TransportDays int   `json:"transportDays"`
	ReceivingDays []int `json:"receivingDays"`
}

// createDeliveryTerms stores a customer's terms. A repeat of Idempotency-Key
// with the same body returns the stored terms with 200.
func (a *API) createDeliveryTerms(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	var req deliveryTermsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	terms := domain.DeliveryTerms{TransportDays: req.TransportDays, ReceivingDays: req.ReceivingDays}
	fields := terms.Validate()
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.CustomerID) {
		fields["customerId"] = "must be a UUID"
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
	saved, created, err := a.store.CreateDeliveryTerms(r.Context(), store.NewDeliveryTerms{LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID,
		Terms: terms, IdempotencyKey: key, RequestHash: hash, Actor: p.UserID})
	if err != nil {
		a.fail(w, r, "create delivery terms", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	setETag(w, saved.Version)
	httpx.JSON(w, status, saved)
}

// listDeliveryTerms lists the terms of a legal entity (or every legal entity
// in the caller's scope), optionally of one customer.
func (a *API) listDeliveryTerms(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.DeliveryTermsFilter{LegalEntityID: legalEntity, CustomerID: q.Get("customerId")}
	fields := map[string]string{}
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
	items, err := a.store.ListDeliveryTerms(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list delivery terms", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func termsRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeDeliveryTermsNotFound, "delivery terms not found"))
			return
		}
		next(w, r, id)
	}
}

// getDeliveryTerms returns one customer's terms.
func (a *API) getDeliveryTerms(w http.ResponseWriter, r *http.Request) {
	termsRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		t, err := a.store.GetDeliveryTerms(r.Context(), id)
		if err != nil {
			a.fail(w, r, "get delivery terms", err)
			return
		}
		setETag(w, t.Version)
		httpx.JSON(w, http.StatusOK, t)
	})(w, r)
}

// replaceDeliveryTerms sets new terms under If-Match.
func (a *API) replaceDeliveryTerms(w http.ResponseWriter, r *http.Request) {
	termsRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req replaceDeliveryTermsRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		terms := domain.DeliveryTerms{TransportDays: req.TransportDays, ReceivingDays: req.ReceivingDays}
		if fields := terms.Validate(); len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		saved, err := a.store.ReplaceDeliveryTerms(r.Context(), id, version, terms, p.UserID, a.now().UTC().Truncate(time.Microsecond))
		if err != nil {
			a.fail(w, r, "replace delivery terms", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, saved)
	})(w, r)
}
