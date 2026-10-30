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

const maxScopeKey = 80

// productListingRequest is the body of POST /v1/sales/product-listings
// (FR-27.15). A CUSTOMER scope key is a customer UUID; a GROUP key is the
// customer group code.
type productListingRequest struct {
	LegalEntityID string  `json:"legalEntityId"`
	ScopeType     string  `json:"scopeType"`
	ScopeKey      string  `json:"scopeKey"`
	ProductID     string  `json:"productId"`
	ListingType   string  `json:"listingType"`
	ValidFrom     *string `json:"validFrom"`
	ValidTo       *string `json:"validTo"`
}

// buildListing validates a listing entry. Field errors are keyed by JSON path.
func buildListing(req productListingRequest) (store.ProductListing, map[string]string) {
	f := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		f["legalEntityId"] = "must be a UUID"
	}
	switch req.ScopeType {
	case domain.ScopeCustomer:
		if !ids.Valid(req.ScopeKey) {
			f["scopeKey"] = "must be a customer UUID when scopeType is CUSTOMER"
		}
	case domain.ScopeGroup:
		if n := len(req.ScopeKey); n == 0 || n > maxScopeKey {
			f["scopeKey"] = fmt.Sprintf("must be 1 to %d characters when scopeType is GROUP", maxScopeKey)
		}
	default:
		f["scopeType"] = "must be CUSTOMER or GROUP"
	}
	if !ids.Valid(req.ProductID) {
		f["productId"] = "must be a UUID"
	}
	if req.ListingType != domain.ListingAllow && req.ListingType != domain.ListingDeny {
		f["listingType"] = "must be ALLOW or DENY"
	}
	validityFields(req.ValidFrom, req.ValidTo, f)
	return store.ProductListing{LegalEntityID: req.LegalEntityID, ScopeType: req.ScopeType, ScopeKey: req.ScopeKey,
		ProductID: req.ProductID, ListingType: req.ListingType, ValidFrom: req.ValidFrom, ValidTo: req.ValidTo}, f
}

// validityFields checks optional validFrom/validTo days and their order.
func validityFields(validFrom, validTo *string, f map[string]string) {
	from, errFrom := optionalDay(validFrom)
	to, errTo := optionalDay(validTo)
	if errFrom != nil {
		f["validFrom"] = "must be a date such as 2027-03-04"
	}
	if errTo != nil {
		f["validTo"] = "must be a date such as 2027-03-04"
	}
	if from != nil && to != nil && from.After(*to) {
		f["validTo"] = "must not be before validFrom"
	}
}

// optionalDay parses an optional YYYY-MM-DD string.
func optionalDay(raw *string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	t, err := time.Parse(dayLayout, *raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// inScope refuses a body legal entity outside the caller's legal-entity scope.
func inScope(w http.ResponseWriter, r *http.Request, legalEntityID string) bool {
	p, _ := tenant.FromContext(r.Context())
	if !tenant.InLegalEntityScope(p, legalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside the caller's scope"))
		return false
	}
	return true
}

// createListing stores one ALLOW or DENY entry for a product.
func (a *API) createListing(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req productListingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := buildListing(req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !inScope(w, r, in.LegalEntityID) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	in.CreatedBy = p.UserID
	saved, err := a.store.CreateListing(r.Context(), in)
	if err != nil {
		a.fail(w, r, "create product listing", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, saved)
}

// listListings is the listing inquiry: entries in the caller's scope, filtered
// by legalEntityId, productId and scopeKey.
func (a *API) listListings(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.ListingFilter{LegalEntityID: legalEntity, ProductID: q.Get("productId"), ScopeKey: q.Get("scopeKey")}
	fields := map[string]string{}
	if f.ProductID != "" && !ids.Valid(f.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	if len(f.ScopeKey) > maxScopeKey {
		fields["scopeKey"] = fmt.Sprintf("must be at most %d characters", maxScopeKey)
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
	items, err := a.store.ListListings(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list product listings", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// deleteListing removes one listing entry.
func (a *API) deleteListing(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeListingNotFound, "product listing not found"))
		return
	}
	if err := a.store.DeleteListing(r.Context(), id); err != nil {
		a.fail(w, r, "delete product listing", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
