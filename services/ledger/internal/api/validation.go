package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

// registerValidationRules mounts the user-defined validation rules (FR-11.21).
func (a *API) registerValidationRules(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/validation-rules", a.listValidationRules)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/validation-rules/{id}", a.getValidationRule)
	r.With(authz.RequireDuty(dutyRulesWrite)).Put("/ledgers/{ledger}/validation-rules/{id}", a.putValidationRule)
}

type validationRuleRequest struct {
	Sequence     int                    `json:"sequence"`
	Name         string                 `json:"name"`
	Level        string                 `json:"level"`
	Severity     string                 `json:"severity"`
	Prerequisite []domain.RuleCondition `json:"prerequisite"`
	Checks       []domain.RuleCondition `json:"checks"`
	Message      string                 `json:"message"`
	Active       bool                   `json:"active"`
}

func writeValidationRule(w http.ResponseWriter, status int, v domain.ValidationRule) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(v.LockVersion)))
	httpx.JSON(w, status, v)
}

func validationRuleID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeValidationRuleNotFound, "validation rule not found"))
		return "", false
	}
	return id, true
}

func (a *API) listValidationRules(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListValidationRules(r.Context(), ledgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getValidationRule(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	id, ok := validationRuleID(w, r)
	if !ok {
		return
	}
	v, err := a.store.GetValidationRule(r.Context(), ledgerID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeValidationRule(w, http.StatusOK, v)
}

// putValidationRule creates (201) or replaces (200, checked against If-Match when
// sent) a validation rule. Field rules are checked in the store against the
// ledger's dimension attributes (422 LEDGER_VALIDATION_RULE_INVALID).
func (a *API) putValidationRule(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	var req validationRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		fields["id"] = "must be a UUID"
	}
	lock, lockOK := parseIfMatch(r.Header.Get("If-Match"))
	if !lockOK {
		fields["If-Match"] = `must be the rule lockVersion as an entity tag, for example "3"`
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	v, created, err := a.store.PutValidationRule(r.Context(), ledgerID, id, lock, domain.ValidationRule{
		Sequence: req.Sequence, Name: req.Name, Level: req.Level, Severity: req.Severity,
		Prerequisite: req.Prerequisite, Checks: req.Checks, Message: req.Message, Active: req.Active})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeValidationRule(w, status, v)
}
