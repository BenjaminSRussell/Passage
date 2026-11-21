package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/store"
)

const maxOwnerUserID = 200

type matchingPolicyRequest struct {
	LegalEntityID string         `json:"legalEntityId"`
	VendorID      string         `json:"vendorId"`
	ItemID        string         `json:"itemId"`
	OwnerUserID   string         `json:"ownerUserId"`
	Policy        *policyRequest `json:"policy"`
}

type matchingPolicyBody struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	VendorID      string `json:"vendorId,omitempty"`
	ItemID        string `json:"itemId,omitempty"`
	OwnerUserID   string `json:"ownerUserId"`
	MatchLevel    string `json:"matchLevel"`
}

type exceptionBody struct {
	InvoiceID      string `json:"invoiceId"`
	LegalEntityID  string `json:"legalEntityId"`
	Assignee       string `json:"assignee"`
	Reasons        string `json:"reasons"`
	Status         string `json:"status"`
	CreatedAt      string `json:"createdAt"`
	DecidedBy      string `json:"decidedBy,omitempty"`
	DecidedAt      string `json:"decidedAt,omitempty"`
	DecisionReason string `json:"decisionReason,omitempty"`
}

type approveExceptionRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Reason        string `json:"reason"`
}

func canonicalOrEmpty(fields map[string]string, name, v string) string {
	if v == "" {
		return ""
	}
	checkUUID(fields, name, v)
	canonical, _ := ids.Canonical(v)
	return canonical
}

func (a *API) setMatchingPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req matchingPolicyRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	vendorID := canonicalOrEmpty(fields, "vendorId", req.VendorID)
	itemID := canonicalOrEmpty(fields, "itemId", req.ItemID)
	owner := strings.TrimSpace(req.OwnerUserID)
	checkLength(fields, "ownerUserId", owner, 1, maxOwnerUserID)
	policy := buildPolicy(req.Policy, fields)
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	legalEntityID, _ := ids.Canonical(req.LegalEntityID)
	mp := domain.MatchingPolicy{
		ID: stableID(p.TenantID, "matching-policy", legalEntityID, vendorID, itemID), LegalEntityID: legalEntityID, VendorID: vendorID, ItemID: itemID, OwnerUserID: owner, Policy: policy,
	}
	if err := a.store.SetMatchingPolicy(r.Context(), mp, p.UserID); err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, matchingPolicyBody{ID: mp.ID, LegalEntityID: legalEntityID, VendorID: vendorID, ItemID: itemID, OwnerUserID: owner, MatchLevel: string(policy.MatchLevel)})
}

func (a *API) listExceptions(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	status, legalEntityID := r.URL.Query().Get("status"), r.URL.Query().Get("legalEntityId")
	if status == "" {
		status = domain.ExceptionOpen
	}
	fields := map[string]string{}
	if status != domain.ExceptionOpen && status != domain.ExceptionApproved && status != domain.ExceptionRejected {
		fields["status"] = "must be OPEN, APPROVED or REJECTED"
	}
	if legalEntityID != "" {
		checkUUID(fields, "legalEntityId", legalEntityID)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if legalEntityID != "" {
		if err := inScope(p, legalEntityID); err != nil {
			writeError(w, r, err)
			return
		}
		legalEntityID, _ = ids.Canonical(legalEntityID)
	}
	rows, err := a.store.ListExceptions(r.Context(), legalEntityID, status)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]exceptionBody, 0, len(rows))
	for _, e := range rows {
		body := exceptionBody{InvoiceID: e.InvoiceID, LegalEntityID: e.LegalEntityID, Assignee: e.Assignee, Reasons: e.Reasons, Status: e.Status,
			CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339), DecidedBy: e.DecidedBy, DecisionReason: e.DecisionReason}
		if e.DecidedAt != nil {
			body.DecidedAt = e.DecidedAt.UTC().Format(time.RFC3339)
		}
		items = append(items, body)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) approveException(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req approveExceptionRequest
	if err := httpx.Decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	reason := strings.TrimSpace(req.Reason)
	fields := map[string]string{}
	checkUUID(fields, "id", id)
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	checkLength(fields, "reason", reason, 1, maxRejectReason)
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	released, err := a.releaseHeld(r.Context(), p, id, req.LegalEntityID, func(fn store.ReleaseFunc) (store.Invoice, error) {
		return a.store.ApproveException(r.Context(), id, req.LegalEntityID, p.UserID, reason, fn)
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.exceptionsApproved.Add(1)
	httpx.JSON(w, http.StatusOK, toInvoiceBody(released))
}
