package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func (a *API) listPeriods(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.periods == nil {
		fail(w, r, missingSlice("period control"))
		return
	}
	ledgerID := r.URL.Query().Get("ledgerId")
	if !ids.Valid(p.LegalEntityID) || !ids.Valid(ledgerID) {
		v := httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "request failed validation")
		v.Errors = map[string]string{"legalEntityId": "a single legal entity is required", "ledgerId": "must be a UUID"}
		if ids.Valid(p.LegalEntityID) {
			delete(v.Errors, "legalEntityId")
		}
		if ids.Valid(ledgerID) {
			delete(v.Errors, "ledgerId")
		}
		fail(w, r, v)
		return
	}
	rows, err := a.periods.PeriodMatrix(r.Context(), p.LegalEntityID, ledgerID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if rows == nil {
		rows = []domain.PeriodStatus{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": rows})
}

type transitionRequest struct {
	LedgerID       string `json:"ledgerId"`
	Status         string `json:"status"`
	AllowedGroupID string `json:"allowedGroupId"`
	Reason         string `json:"reason"`
}

func (a *API) transition(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.periods == nil {
		fail(w, r, missingSlice("period control"))
		return
	}
	var req transitionRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	module, err := parseModule(chi.URLParam(r, "module"))
	if err != nil {
		fail(w, r, err)
		return
	}
	to, err := parseStatus(req.Status)
	if err != nil {
		fail(w, r, err)
		return
	}
	periodID := chi.URLParam(r, "fiscalPeriodId")
	if !ids.Valid(p.LegalEntityID) || !ids.Valid(periodID) || !ids.Valid(req.LedgerID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "legalEntityId, fiscalPeriodId and ledgerId must be UUIDs"))
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, req.LedgerID) {
		return
	}
	out, err := a.periods.TransitionPeriod(r.Context(), domain.PeriodTransition{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, FiscalPeriodID: periodID, Module: module, To: to,
		AllowedGroupID: req.AllowedGroupID, Reason: req.Reason, Actor: p.UserID, LockWait: a.lockWait(),
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type reopenRequestBody struct {
	LedgerID    string `json:"ledgerId"`
	Reason      string `json:"reason"`
	ExpiresDays int    `json:"expiresDays"`
}

func (a *API) requestReopen(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.periods == nil {
		fail(w, r, missingSlice("period control"))
		return
	}
	if err := requireKey(r.Header.Get(idempotencyHeader)); err != nil {
		fail(w, r, err)
		return
	}
	var req reopenRequestBody
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	module, err := parseModule(chi.URLParam(r, "module"))
	if err != nil {
		fail(w, r, err)
		return
	}
	periodID := chi.URLParam(r, "fiscalPeriodId")
	if !ids.Valid(p.LegalEntityID) || !ids.Valid(periodID) || !ids.Valid(req.LedgerID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "legalEntityId, fiscalPeriodId and ledgerId must be UUIDs"))
		return
	}
	days := req.ExpiresDays
	if days == 0 {
		days = a.reopenDays()
	}
	if days < 1 || days > 30 {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "expiresDays must be between 1 and 30"))
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, req.LedgerID) {
		return
	}
	out, err := a.periods.RequestReopen(r.Context(), domain.StoredReopen{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, FiscalPeriodID: periodID, Module: module,
		Reason: req.Reason, RequestedBy: p.UserID, Required: a.reopenApprovers(), ExpiresDays: days,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) approveReopen(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.periods == nil {
		fail(w, r, missingSlice("period control"))
		return
	}
	id := chi.URLParam(r, "requestId")
	if !ids.Valid(id) {
		fail(w, r, &domain.Error{Code: domain.CodeNoPeriod, Detail: "reopen request not found"})
		return
	}
	out, err := a.periods.ApproveReopen(r.Context(), id, p.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) rejectReopen(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.periods == nil {
		fail(w, r, missingSlice("period control"))
		return
	}
	id := chi.URLParam(r, "requestId")
	if !ids.Valid(id) {
		fail(w, r, &domain.Error{Code: domain.CodeNoPeriod, Detail: "reopen request not found"})
		return
	}
	out, err := a.periods.RejectReopen(r.Context(), id, p.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return tenant.Principal{}, false
	}
	return p, true
}
