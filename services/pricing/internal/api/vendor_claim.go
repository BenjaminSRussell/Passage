package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	vendorClaimsPath  = "/v1/pricing/rebates/vendor-claims"
	claimPreviewPath  = rebateAgreementsPath + "/{id}/claim-preview"
	dutyClaimApprove  = "pricing.rebate-claim.approve"
	timestampLayout   = time.RFC3339
	claimPeriodDetail = "must be a period such as 2027-03, 2027-Q1 or 2027"
)

type vendorClaimRequest struct {
	AgreementID    string `json:"agreementId"`
	Period         string `json:"period"`
	SettlementMode string `json:"settlementMode"`
}

type vendorClaimView struct {
	ID             string `json:"id"`
	Number         string `json:"number"`
	LegalEntityID  string `json:"legalEntityId"`
	AgreementID    string `json:"agreementId"`
	VendorID       string `json:"vendorId"`
	PeriodID       string `json:"periodId"`
	AccruedTotal   string `json:"accruedTotal"`
	PriorClaimed   string `json:"priorClaimed"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency"`
	SettlementMode string `json:"settlementMode"`
	Status         string `json:"status"`
	Version        int    `json:"version"`
	CreatedBy      string `json:"createdBy,omitempty"`
	ApprovedBy     string `json:"approvedBy,omitempty"`
	CreatedAt      string `json:"createdAt"`
	ApprovedAt     string `json:"approvedAt,omitempty"`
}

type claimPreviewView struct {
	AgreementID    string `json:"agreementId"`
	VendorID       string `json:"vendorId"`
	PeriodID       string `json:"periodId"`
	Accrued        string `json:"accrued"`
	PriorClaimed   string `json:"priorClaimed"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency"`
	SettlementMode string `json:"settlementMode"`
	Claims         int    `json:"claims"`
}

// previewVendorClaim shows the claim one period of a VENDOR agreement would produce, without
// writing anything (AC-26.11: "the period claim preview shows 600.00").
func (a *API) previewVendorClaim(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "no rebate agreement "+id))
		return
	}
	period := r.URL.Query().Get("period")
	if !periodKey.MatchString(period) {
		validationFailed(w, r, map[string]string{"period": claimPeriodDetail})
		return
	}
	p, err := a.store.PreviewVendorClaim(r.Context(), id, period)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, claimPreviewView{AgreementID: p.AgreementID, VendorID: p.VendorID, PeriodID: p.PeriodKey,
		Accrued: p.Accrued.String(), PriorClaimed: p.PriorClaimed.String(), Amount: p.Amount.String(), Currency: p.Currency,
		SettlementMode: p.SettlementMode, Claims: p.Claims})
}

// createVendorClaim claims one period of a VENDOR agreement as RC-n (FR-26.15, BR-26.11).
func (a *API) createVendorClaim(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req vendorClaimRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.AgreementID) {
		fields["agreementId"] = "must be a UUID"
	}
	if !periodKey.MatchString(req.Period) {
		fields["period"] = claimPeriodDetail
	}
	if !domain.ValidClaimMode(req.SettlementMode) {
		fields["settlementMode"] = "must be CREDIT_NOTE or PAYMENT_DEDUCTION; a negative claim settles by vendor invoice on its own"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	c, err := a.store.CreateVendorClaim(r.Context(), store.VendorClaimInput{AgreementID: req.AgreementID, PeriodKey: req.Period,
		SettlementMode: req.SettlementMode}, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("vendor rebate claim failed", "err", err, "agreementId", req.AgreementID, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, claimResponse(c))
}

// listVendorClaims is the Vendor rebate claims inquiry (§5.26.14): newest first, filtered by
// agreement, vendor, period and status.
func (a *API) listVendorClaims(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.ClaimFilter{AgreementID: q.Get("agreementId"), VendorID: q.Get("vendorId"), PeriodKey: q.Get("period"),
		Status: q.Get("status"), Limit: store.MaxListLimit}
	fields := map[string]string{}
	if f.AgreementID != "" && !ids.Valid(f.AgreementID) {
		fields["agreementId"] = "must be a UUID"
	}
	if f.VendorID != "" && !ids.Valid(f.VendorID) {
		fields["vendorId"] = "must be a UUID"
	}
	if f.PeriodKey != "" && !periodKey.MatchString(f.PeriodKey) {
		fields["period"] = claimPeriodDetail
	}
	if f.Status != "" && f.Status != domain.ClaimOpen && f.Status != domain.ClaimApproved {
		fields["status"] = "must be OPEN or APPROVED"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rows, err := a.store.ListVendorClaims(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]vendorClaimView, 0, len(rows))
	for _, c := range rows {
		items = append(items, claimResponse(c))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func claimID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeClaimNotFound, "no vendor rebate claim "+id))
		return "", false
	}
	return id, true
}

func (a *API) getVendorClaim(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := claimID(w, r)
	if !ok {
		return
	}
	c, err := a.store.GetVendorClaim(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, claimResponse(c))
}

// approveVendorClaim approves an OPEN claim at the body version for w19 AP (duty
// VendorRebateClaimApprove; the claim's creator cannot approve it).
func (a *API) approveVendorClaim(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := claimID(w, r)
	if !ok {
		return
	}
	var req versionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Version < 1 {
		validationFailed(w, r, map[string]string{"version": "is required and must be at least 1"})
		return
	}
	c, err := a.store.ApproveVendorClaim(r.Context(), id, req.Version, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, claimResponse(c))
}

// registerVendorClaimRoutes mounts the vendor rebate claim surface (FR-26.15). Reads take
// RebateView, creating a claim RebateMaintain, approving VendorRebateClaimApprove (§5.26.17).
func (a *API) registerVendorClaimRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyRebateView)).Get(claimPreviewPath, a.previewVendorClaim)
	r.With(authz.RequireDuty(dutyRebateView)).Get(vendorClaimsPath, a.listVendorClaims)
	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(vendorClaimsPath, a.createVendorClaim)
	r.With(authz.RequireDuty(dutyRebateView)).Get(vendorClaimsPath+"/{id}", a.getVendorClaim)
	r.With(authz.RequireDuty(dutyClaimApprove)).Post(vendorClaimsPath+"/{id}:approve", a.approveVendorClaim)
}

func claimResponse(c store.VendorClaim) vendorClaimView {
	v := vendorClaimView{ID: c.ID, Number: c.Number, LegalEntityID: c.LegalEntityID, AgreementID: c.AgreementID, VendorID: c.VendorID,
		PeriodID: c.PeriodKey, AccruedTotal: c.AccruedTotal.String(), PriorClaimed: c.PriorClaimed.String(), Amount: c.Amount.String(),
		Currency: c.Currency, SettlementMode: c.SettlementMode, Status: c.Status, Version: c.Version, CreatedBy: c.CreatedBy,
		ApprovedBy: c.ApprovedBy, CreatedAt: c.CreatedAt.UTC().Format(timestampLayout)}
	if c.ApprovedAt != nil {
		v.ApprovedAt = c.ApprovedAt.UTC().Format(timestampLayout)
	}
	return v
}
