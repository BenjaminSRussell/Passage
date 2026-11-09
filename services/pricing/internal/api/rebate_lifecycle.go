package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	agreementRevisePath = rebateAgreementsPath + "/{id}:revise"
	agreementClosePath  = rebateAgreementsPath + "/{id}:close"
	agreementReopenPath = rebateAgreementsPath + "/{id}:reopen"
)

// agreementReviseRequest changes an ACTIVE agreement at the version the caller read; an
// omitted field stays as it is.
type agreementReviseRequest struct {
	Version int        `json:"version"`
	Name    *string    `json:"name"`
	ValidTo *string    `json:"validTo"`
	Tiers   *[]tierDTO `json:"tiers"`
	// ProductScope replaces the scope; {} takes every invoice line again.
	ProductScope *productScopeDTO `json:"productScope"`
}

type productScopeDTO struct {
	ProductID      string `json:"productId"`
	ProductGroupID string `json:"productGroupId"`
}

type agreementCloseRequest struct {
	Version   int    `json:"version"`
	CloseDate string `json:"closeDate"`
}

func (a *API) registerAgreementLifecycleRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(agreementRevisePath, a.reviseAgreement)
	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(agreementClosePath, a.closeAgreement)
	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(agreementReopenPath, a.reopenAgreement)
}

func (a *API) reviseAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, store.CodeAgreementNotFound, "rebate agreement")
	if !ok {
		return
	}
	var req agreementReviseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	rev := store.AgreementRevision{ID: id, Version: req.Version}
	if req.Name != nil {
		if *req.Name == "" || len(*req.Name) > 120 {
			fields["name"] = "must be 1 to 120 characters"
		}
		rev.Name = req.Name
	}
	if req.ValidTo != nil {
		to, err := time.Parse(dateLayout, *req.ValidTo)
		if err != nil {
			fields["validTo"] = "must be a date in YYYY-MM-DD form"
		}
		rev.ValidTo = &to
	}
	if req.Tiers != nil {
		tiers := make([]domain.Tier, 0, len(*req.Tiers))
		for _, t := range *req.Tiers {
			tier, ok := parseTier(t)
			if !ok {
				fields["tiers"] = "a tier has a value that is not a decimal"
				break
			}
			tiers = append(tiers, tier)
		}
		rev.Tiers = &tiers
	}
	if req.ProductScope != nil {
		sc := req.ProductScope
		canonicalUUID(fields, "productScope.productId", &sc.ProductID)
		canonicalUUID(fields, "productScope.productGroupId", &sc.ProductGroupID)
		if sc.ProductID != "" && sc.ProductGroupID != "" {
			fields["productScope"] = "at most one of productId or productGroupId"
		}
		rev.Scope = &store.ProductScope{ProductID: sc.ProductID, ProductGroupID: sc.ProductGroupID}
	}
	if rev.Name == nil && rev.ValidTo == nil && rev.Tiers == nil && rev.Scope == nil {
		fields["body"] = "change at least one of name, validTo, tiers or productScope"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.ReviseAgreement(r.Context(), rev, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, agreementResponse(out))
}

func (a *API) closeAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, store.CodeAgreementNotFound, "rebate agreement")
	if !ok {
		return
	}
	var req agreementCloseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	day, err := time.Parse(dateLayout, req.CloseDate)
	if err != nil {
		fields["closeDate"] = "must be a date in YYYY-MM-DD form"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.CloseAgreement(r.Context(), id, req.Version, day, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, agreementResponse(out))
}

type agreementReopenRequest struct {
	Version int    `json:"version"`
	ValidTo string `json:"validTo"`
}

// reopenAgreement makes a CLOSED agreement ACTIVE again with a later last day (BR-26.8).
func (a *API) reopenAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, store.CodeAgreementNotFound, "rebate agreement")
	if !ok {
		return
	}
	var req agreementReopenRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	day, err := time.Parse(dateLayout, req.ValidTo)
	if err != nil {
		fields["validTo"] = "must be a date in YYYY-MM-DD form"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.ReopenAgreement(r.Context(), id, req.Version, day, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, agreementResponse(out))
}
