package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// registerAdvancedRules mounts the advanced rule structures (FR-11.16).
func (a *API) registerAdvancedRules(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/advanced-rules", a.listAdvancedRules)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/advanced-rules/{id}", a.getAdvancedRule)
	r.With(authz.RequireDuty(dutyStructureWrite)).Put("/ledgers/{ledger}/advanced-rules/{id}", a.putAdvancedRule)
}

type advancedRuleRequest struct {
	StructureID string            `json:"structureId"`
	Name        string            `json:"name"`
	Priority    int               `json:"priority"`
	Accounts    []string          `json:"accounts"`
	Dimensions  map[string]string `json:"dimensions"`
	Segments    []string          `json:"segments"`
	Constraints []constraintBody  `json:"constraints"`
}

func writeAdvancedRule(w http.ResponseWriter, status int, v domain.AdvancedRule) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(v.LockVersion)))
	httpx.JSON(w, status, v)
}

func (a *API) listAdvancedRules(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	structureID := r.URL.Query().Get("structureId")
	if structureID != "" && !ids.Valid(structureID) {
		validationFailed(w, r, map[string]string{"structureId": "must be a UUID"})
		return
	}
	items, err := a.store.ListAdvancedRules(r.Context(), ledgerID, structureID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getAdvancedRule(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeAdvancedRuleNotFound, "advanced rule not found"))
		return
	}
	v, err := a.store.GetAdvancedRule(r.Context(), ledgerID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeAdvancedRule(w, http.StatusOK, v)
}

// putAdvancedRule creates (201) or replaces (200, checked against If-Match when sent) an
// advanced rule of a draft structure version. Definition problems are 422
// LEDGER_ADVANCED_RULE_INVALID with every violation.
func (a *API) putAdvancedRule(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	var req advancedRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		fields["id"] = "must be a UUID"
	}
	if !ids.Valid(req.StructureID) {
		fields["structureId"] = "must be the UUID of an account structure version"
	}
	lock, lockOK := parseIfMatch(r.Header.Get("If-Match"))
	if !lockOK {
		fields["If-Match"] = `must be the rule lockVersion as an entity tag, for example "3"`
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	v, created, err := a.store.PutAdvancedRule(r.Context(), ledgerID, id, lock, domain.AdvancedRule{
		StructureID: req.StructureID, Name: req.Name, Priority: req.Priority, Accounts: req.Accounts,
		Dimensions: req.Dimensions, Segments: req.Segments, Constraints: toDomainConstraints(req.Constraints)})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeAdvancedRule(w, status, v)
}
