package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const (
	dutyDeduction       = "DeductionMaintain"
	dutyWriteOffApprove = "ARWriteOffApprove"
	maxReasonCode       = 60
	maxDecisionNote     = 1000
	maxSplitParts       = 20
)

// deductionRoutes mounts the deductions workbench (FR-30.17). Reads need ARView;
// recording and every workbench move need DeductionMaintain (§5.30.17).
func (a *API) deductionRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyDeduction)).Post("/deductions", a.createDeduction)
	r.With(authz.RequireDuty(dutyView)).Get("/deductions", a.listDeductions)
	r.With(authz.RequireDuty(dutyView)).Get("/deductions/{ref}", a.getDeduction)
	// :match, :approve, :deny, :split and :write-off share one route; deductionAction dispatches.
	r.Post("/deductions/{ref}", a.deductionAction)
}

type createDeductionRequest struct {
	LegalEntityID string          `json:"legalEntityId"`
	OpenItemID    string          `json:"openItemId"`
	PaymentID     *string         `json:"paymentId"`
	Type          string          `json:"type"`
	ReasonCode    string          `json:"reasonCode"`
	Amount        decimal.Decimal `json:"amount"`
}

// createDeduction handles POST /deductions: move the unexplained part of a short
// payment off its open item into the workbench. Idempotency-Key is required.
func (a *API) createDeduction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requiredKey(w, r)
	if !ok {
		return
	}
	var req createDeductionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.ReasonCode = strings.TrimSpace(req.ReasonCode)
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.OpenItemID) {
		fields["openItemId"] = "must be a UUID"
	}
	if req.PaymentID != nil && !ids.Valid(*req.PaymentID) {
		fields["paymentId"] = "must be a UUID"
	}
	if !domain.ValidDeductionType(req.Type) {
		fields["type"] = "must be one of " + strings.Join(domain.DeductionTypes, ", ")
	}
	if n := len(req.ReasonCode); n < 1 || n > maxReasonCode {
		fields["reasonCode"] = fmt.Sprintf("must be 1 to %d characters", maxReasonCode)
	}
	if !req.Amount.IsPositive() {
		fields["amount"] = "must be a positive decimal"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.CreateDeduction(r.Context(), store.Deduction{LegalEntityID: req.LegalEntityID, OpenItemID: req.OpenItemID,
		PaymentID: req.PaymentID, Type: req.Type, ReasonCode: req.ReasonCode, Amount: req.Amount}, key)
	if err != nil {
		a.fail("create deduction", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

// listDeductions handles GET /deductions?status=&type=&customer=.
func (a *API) listDeductions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.DeductionFilter{Status: q.Get("status"), Type: q.Get("type"), CustomerID: q.Get("customer")}
	if f.Status != "" && !domain.ValidDeductionStatus(f.Status) {
		badQuery(w, r, "status", "must be OPEN, MATCHED, APPROVED, DENIED, SPLIT or WRITTEN_OFF")
		return
	}
	if f.Type != "" && !domain.ValidDeductionType(f.Type) {
		badQuery(w, r, "type", "must be one of "+strings.Join(domain.DeductionTypes, ", "))
		return
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		badQuery(w, r, "customer", "must be a UUID")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListDeductions(r.Context(), f, limit)
	if err != nil {
		a.fail("list deductions", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// getDeduction handles GET /deductions/{id}.
func (a *API) getDeduction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "ref")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeDeductionNotFound, "deduction not found"))
		return
	}
	out, err := a.store.GetDeduction(r.Context(), id)
	if err != nil {
		a.fail("get deduction", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// deductionActionRequest is the body of every workbench move; each move reads its own fields.
type deductionActionRequest struct {
	ClaimType      string            `json:"claimType"`
	ClaimRef       string            `json:"claimRef"`
	ClaimRemaining *decimal.Decimal  `json:"claimRemaining"`
	Reason         string            `json:"reason"`
	Note           *string           `json:"note"`
	Parts          []decimal.Decimal `json:"parts"`
}

// deductionAction handles POST /deductions/{id}:match|approve|deny|split|write-off.
// Every move needs DeductionMaintain and If-Match; a write-off above
// deduction_write_off_limit also needs ARWriteOffApprove.
func (a *API) deductionAction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "ref"), ":")
	if !found || !ids.Valid(id) || !slices.Contains(domain.DeductionActions, action) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeDeductionNotFound, "unknown deduction or action"))
		return
	}
	if !require(w, r, dutyDeduction) {
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req deductionActionRequest
	if err := decodeOptional(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Note != nil {
		trimmed := strings.TrimSpace(*req.Note)
		if len(trimmed) > maxDecisionNote {
			writeFields(w, r, map[string]string{"note": fmt.Sprintf("must be at most %d characters", maxDecisionNote)})
			return
		}
		req.Note = &trimmed
		if trimmed == "" {
			req.Note = nil
		}
	}
	var out store.Deduction
	switch action {
	case "match":
		fields := map[string]string{}
		if !domain.ValidClaimType(req.ClaimType) {
			fields["claimType"] = "must be one of " + strings.Join(domain.ClaimTypes, ", ")
		}
		req.ClaimRef = strings.TrimSpace(req.ClaimRef)
		if n := len(req.ClaimRef); n < 1 || n > maxReference {
			fields["claimRef"] = fmt.Sprintf("must be 1 to %d characters", maxReference)
		}
		if req.ClaimRemaining == nil || req.ClaimRemaining.IsNegative() {
			fields["claimRemaining"] = "must be the claim's remaining amount, zero or more"
		}
		if len(fields) > 0 {
			writeFields(w, r, fields)
			return
		}
		out, err = a.store.MatchDeduction(r.Context(), id, version, store.ClaimMatch{Type: req.ClaimType, Ref: req.ClaimRef, Remaining: *req.ClaimRemaining})
	case "approve":
		out, err = a.store.ApproveDeduction(r.Context(), id, version, req.Note)
	case "deny":
		if !domain.ValidDenialReason(req.Reason) {
			writeFields(w, r, map[string]string{"reason": "must be one of " + strings.Join(domain.DenialReasons, ", ")})
			return
		}
		out, err = a.store.DenyDeduction(r.Context(), id, version, req.Reason, req.Note)
	case "split":
		if len(req.Parts) < 2 || len(req.Parts) > maxSplitParts {
			writeFields(w, r, map[string]string{"parts": fmt.Sprintf("must be 2 to %d amounts", maxSplitParts)})
			return
		}
		for i, part := range req.Parts {
			if !part.IsPositive() {
				writeFields(w, r, map[string]string{fmt.Sprintf("parts[%d]", i): "must be a positive decimal"})
				return
			}
		}
		out, err = a.store.SplitDeduction(r.Context(), id, version, req.Parts)
	case "write-off":
		p, _ := tenant.FromContext(r.Context())
		out, err = a.store.WriteOffDeduction(r.Context(), id, version, p.HasDuty(dutyWriteOffApprove), req.Note)
	}
	if err != nil {
		a.fail(action+" deduction", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}
