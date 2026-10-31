package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// allocationQuotaRequest is the body of POST /v1/sales/allocation-quotas
// (FR-27.16): a quota for one product over a period, for one customer or, when
// customerId is absent, every customer of the legal entity.
type allocationQuotaRequest struct {
	LegalEntityID string  `json:"legalEntityId"`
	ProductID     string  `json:"productId"`
	CustomerID    *string `json:"customerId"`
	PeriodStart   string  `json:"periodStart"`
	PeriodEnd     string  `json:"periodEnd"`
	QuotaQty      string  `json:"quotaQty"`
}

// changeQuotaRequest is the body of PATCH /v1/sales/allocation-quotas/{id}.
type changeQuotaRequest struct {
	QuotaQty string `json:"quotaQty"`
}

func buildAllocationQuota(req allocationQuotaRequest) (store.NewAllocationQuota, map[string]string) {
	f := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		f["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.ProductID) {
		f["productId"] = "must be a UUID"
	}
	if req.CustomerID != nil && !ids.Valid(*req.CustomerID) {
		f["customerId"] = "must be a UUID when given"
	}
	start, errStart := time.Parse(dayLayout, req.PeriodStart)
	end, errEnd := time.Parse(dayLayout, req.PeriodEnd)
	if errStart != nil {
		f["periodStart"] = "must be a date such as 2027-03-01"
	}
	if errEnd != nil {
		f["periodEnd"] = "must be a date such as 2027-03-31"
	}
	qty, err := decimal.NewFromString(req.QuotaQty)
	if err != nil {
		f["quotaQty"] = "must be a decimal"
	} else if errStart == nil && errEnd == nil {
		for k, v := range domain.ValidateQuotaPeriod(start, end, qty) {
			f[k] = v
		}
	} else if err := domain.ValidateQuantity(qty); err != nil {
		f["quotaQty"] = err.Error()
	}
	return store.NewAllocationQuota{LegalEntityID: req.LegalEntityID, ProductID: req.ProductID, CustomerID: req.CustomerID,
		PeriodStart: req.PeriodStart, PeriodEnd: req.PeriodEnd, Quantity: qty}, f
}

// createAllocationQuota stores a quota. A repeat of Idempotency-Key with the
// same body returns the stored quota with 200.
func (a *API) createAllocationQuota(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	var req allocationQuotaRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := buildAllocationQuota(req)
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
	saved, created, err := a.store.CreateAllocationQuota(r.Context(), in)
	if err != nil {
		a.fail(w, r, "create allocation quota", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	setETag(w, saved.Version)
	httpx.JSON(w, status, saved)
}

// listAllocationQuotas is the quota inquiry: quota, consumed and remaining per
// product and period, filtered by legalEntityId, productId, customerId and onDate.
func (a *API) listAllocationQuotas(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.AllocationFilter{LegalEntityID: legalEntity, ProductID: q.Get("productId"), CustomerID: q.Get("customerId"), OnDate: q.Get("onDate")}
	fields := map[string]string{}
	if f.ProductID != "" && !ids.Valid(f.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if f.OnDate != "" {
		if _, err := time.Parse(dayLayout, f.OnDate); err != nil {
			fields["onDate"] = "must be a date such as 2027-03-04"
		}
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
	items, err := a.store.ListAllocationQuotas(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list allocation quotas", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func quotaRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeQuotaNotFound, "allocation quota not found"))
			return
		}
		next(w, r, id)
	}
}

// getAllocationQuota returns one quota with its draw history.
func (a *API) getAllocationQuota(w http.ResponseWriter, r *http.Request) {
	quotaRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		q, err := a.store.GetAllocationQuota(r.Context(), id)
		if err != nil {
			a.fail(w, r, "get allocation quota", err)
			return
		}
		setETag(w, q.Version)
		httpx.JSON(w, http.StatusOK, q)
	})(w, r)
}

// changeAllocationQuota sets a new quota quantity under If-Match. A quantity
// below the consumed quantity is 409 (BR-27.15).
func (a *API) changeAllocationQuota(w http.ResponseWriter, r *http.Request) {
	quotaRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req changeQuotaRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		qty, err := decimal.NewFromString(req.QuotaQty)
		if err != nil {
			validationFailed(w, r, map[string]string{"quotaQty": "must be a decimal"})
			return
		}
		if err := domain.ValidateQuotaChange(qty, decimal.Zero); err != nil {
			validationFailed(w, r, map[string]string{"quotaQty": err.Error()})
			return
		}
		p, _ := tenant.FromContext(r.Context())
		q, err := a.store.ChangeAllocationQuota(r.Context(), id, version, qty, p.UserID, a.now().UTC().Truncate(time.Microsecond))
		if err != nil {
			a.fail(w, r, "change allocation quota", err)
			return
		}
		setETag(w, q.Version)
		httpx.JSON(w, http.StatusOK, q)
	})(w, r)
}
