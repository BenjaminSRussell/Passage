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

// createAgreementRequest is the body of POST /v1/sales/agreements (FR-27.10):
// a quantity commitment for one product at an agreed price.
type createAgreementRequest struct {
	LegalEntityID    string  `json:"legalEntityId"`
	CustomerID       string  `json:"customerId"`
	InvoiceAccountID string  `json:"invoiceAccountId"`
	ProductID        string  `json:"productId"`
	Unit             string  `json:"unit"`
	UnitPrice        string  `json:"unitPrice"`
	Currency         string  `json:"currency"`
	CommitmentQty    string  `json:"commitmentQty"`
	ValidFrom        *string `json:"validFrom"`
	ValidTo          *string `json:"validTo"`
}

// releaseOrderRequest is the body of POST /v1/sales/agreements/{id}:release-order:
// the quantity to draw and where and when the created order line ships.
type releaseOrderRequest struct {
	Quantity          string `json:"quantity"`
	SiteID            string `json:"siteId"`
	WarehouseID       string `json:"warehouseId"`
	RequestedShipDate string `json:"requestedShipDate"`
}

// buildAgreement validates a commitment. Field errors are keyed by JSON path.
func buildAgreement(req createAgreementRequest) (store.NewAgreement, map[string]string) {
	f := map[string]string{}
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "customerId": req.CustomerID, "productId": req.ProductID} {
		if !ids.Valid(v) {
			f[name] = "must be a UUID"
		}
	}
	invoice := req.InvoiceAccountID
	if invoice == "" {
		invoice = req.CustomerID
	}
	if !ids.Valid(invoice) {
		f["invoiceAccountId"] = "must be a UUID"
	}
	if n := len(req.Unit); n == 0 || n > 10 {
		f["unit"] = "must be 1 to 10 characters"
	}
	if !currencyCode.MatchString(req.Currency) {
		f["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	price, err := decimal.NewFromString(req.UnitPrice)
	if err != nil || price.IsNegative() || !price.Round(4).Equal(price) {
		f["unitPrice"] = "must be a decimal of at least 0 with at most 4 places"
	}
	commitment, err := decimal.NewFromString(req.CommitmentQty)
	if err != nil {
		f["commitmentQty"] = "must be a decimal"
	} else if err := domain.ValidateQuantity(commitment); err != nil {
		f["commitmentQty"] = err.Error()
	}
	validityFields(req.ValidFrom, req.ValidTo, f)
	return store.NewAgreement{LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID, InvoiceAccountID: invoice, ProductID: req.ProductID,
		Unit: req.Unit, UnitPrice: price, Currency: req.Currency, Commitment: commitment, ValidFrom: req.ValidFrom, ValidTo: req.ValidTo}, f
}

// buildRelease validates a release request.
func buildRelease(req releaseOrderRequest) (store.AgreementRelease, map[string]string) {
	f := map[string]string{}
	qty, err := decimal.NewFromString(req.Quantity)
	if err != nil {
		f["quantity"] = "must be a decimal"
	} else if err := domain.ValidateQuantity(qty); err != nil {
		f["quantity"] = err.Error()
	}
	if !ids.Valid(req.SiteID) {
		f["siteId"] = "must be a UUID"
	}
	if !ids.Valid(req.WarehouseID) {
		f["warehouseId"] = "must be a UUID"
	}
	if _, err := time.Parse(dayLayout, req.RequestedShipDate); err != nil {
		f["requestedShipDate"] = "must be a date such as 2027-03-04"
	}
	return store.AgreementRelease{Quantity: qty, SiteID: req.SiteID, WarehouseID: req.WarehouseID, RequestedShipDate: req.RequestedShipDate}, f
}

// createAgreement stores an ACTIVE agreement. A repeat of Idempotency-Key with
// the same body returns the stored agreement with 200.
func (a *API) createAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	var req createAgreementRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := buildAgreement(req)
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
	saved, created, err := a.store.CreateAgreement(r.Context(), in)
	if err != nil {
		a.fail(w, r, "create agreement", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	setETag(w, saved.Version)
	httpx.JSON(w, status, saved)
}

// listAgreements returns agreements in the caller's scope, newest first.
func (a *API) listAgreements(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.AgreementFilter{LegalEntityID: legalEntity, CustomerID: q.Get("customerId"), Status: q.Get("status")}
	fields := map[string]string{}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if f.Status != "" && f.Status != domain.AgreementActive && f.Status != domain.AgreementExhausted {
		fields["status"] = "must be ACTIVE or EXHAUSTED"
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
	items, err := a.store.ListAgreements(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list agreements", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// agreementRoute parses the agreement id from the path.
func agreementRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "sales agreement not found"))
			return
		}
		next(w, r, id)
	}
}

// getAgreement returns one agreement with its remaining commitment and release
// history.
func (a *API) getAgreement(w http.ResponseWriter, r *http.Request) {
	agreementRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		saved, err := a.store.GetAgreement(r.Context(), id)
		if err != nil {
			a.fail(w, r, "get agreement", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, saved)
	})(w, r)
}

// releaseOrder draws quantity against the commitment and creates a DRAFT order
// at the agreement price. A release beyond the remaining commitment is 422
// AGREEMENT_EXHAUSTED and changes nothing (BR-27.9, AC-27.9).
func (a *API) releaseOrder(w http.ResponseWriter, r *http.Request) {
	agreementRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		key := r.Header.Get(idempotencyKey)
		var req releaseOrderRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		in, fields := buildRelease(req)
		if key == "" {
			fields[idempotencyKey] = "is required"
		}
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		hash, err := domain.RequestHash(req)
		if err != nil {
			a.fail(w, r, "hash request", err)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		in.IdempotencyKey, in.RequestHash, in.Actor = key, hash, p.UserID
		res, created, err := a.store.ReleaseAgreement(r.Context(), id, in, a.now().UTC().Truncate(time.Microsecond))
		if err != nil {
			a.fail(w, r, "release agreement", err)
			return
		}
		status := http.StatusOK
		if created {
			a.created.Add(1)
			status = http.StatusCreated
		}
		setETag(w, res.Agreement.Version)
		httpx.JSON(w, status, res)
	})(w, r)
}
