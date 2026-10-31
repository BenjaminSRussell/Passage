package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

// dutyClearingRun is "Run automatic clearing" (PRD §2.11.17); maintaining the rules
// is dutyClearingRules and reading uses the journal read duty.
const (
	dutyClearingRun = "ledger.clearing.run"

	maxAutoRuleName = 80
)

// registerAutoClearing mounts automatic clearing rules and runs (FR-11.20).
func (a *API) registerAutoClearing(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/auto-clearing-rules", a.listAutoRules)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/auto-clearing-rules/{id}", a.getAutoRule)
	r.With(authz.RequireDuty(dutyClearingRules)).Put("/ledgers/{ledger}/auto-clearing-rules/{id}", a.putAutoRule)
	r.With(authz.RequireDuty(dutyClearingRun)).Post("/auto-clearing:run", a.runAutoClearing)
	r.With(authz.RequireDuty(dutyRead)).Get("/auto-clearing-runs", a.listAutoRuns)
	r.With(authz.RequireDuty(dutyRead)).Get("/auto-clearing-runs/{id}", a.getAutoRun)
}

type autoRuleRequest struct {
	Name           string   `json:"name"`
	AccountFrom    string   `json:"accountFrom"`
	AccountTo      string   `json:"accountTo"`
	Criteria       []string `json:"criteria"`
	AllowTolerance bool     `json:"allowTolerance"`
	ToleranceGroup string   `json:"toleranceGroup"`
	Sequence       int      `json:"sequence"`
	Active         *bool    `json:"active"`
}

func writeAutoRule(w http.ResponseWriter, status int, rule store.AutoClearingRule) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(rule.Version)))
	httpx.JSON(w, status, rule)
}

func ruleID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAutoRuleNotFound, "automatic clearing rule not found"))
		return "", false
	}
	return id, true
}

func (a *API) listAutoRules(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListAutoClearingRules(r.Context(), ledgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getAutoRule(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	id, ok := ruleID(w, r)
	if !ok {
		return
	}
	rule, err := a.store.GetAutoClearingRule(r.Context(), ledgerID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeAutoRule(w, http.StatusOK, rule)
}

// putAutoRule creates (201) or replaces (200, checked against If-Match when sent) a rule.
func (a *API) putAutoRule(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	id, ok := ruleID(w, r)
	if !ok {
		return
	}
	var req autoRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if n := utf8.RuneCountInString(req.Name); n < 1 || n > maxAutoRuleName {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxAutoRuleName)
	}
	if !accountNumberRe.MatchString(req.AccountFrom) {
		fields["accountFrom"] = "must be 1 to 20 characters of A-Z, a-z, 0-9, '.', '_' or '-'"
	}
	if !accountNumberRe.MatchString(req.AccountTo) {
		fields["accountTo"] = "must be 1 to 20 characters of A-Z, a-z, 0-9, '.', '_' or '-'"
	}
	if fields["accountFrom"] == "" && fields["accountTo"] == "" && req.AccountFrom > req.AccountTo {
		fields["accountTo"] = "must not be before accountFrom"
	}
	if n := len(req.Criteria); n < 1 || n > domain.MaxAutoCriteria {
		fields["criteria"] = fmt.Sprintf("give 1 to %d criteria", domain.MaxAutoCriteria)
	} else {
		seen := map[string]bool{}
		for _, c := range req.Criteria {
			if !domain.ValidCriterion(c) || seen[c] {
				fields["criteria"] = "must be distinct: assignment, source_line_ref, document_id, amount_txn, posting_date or dim.{ATTRIBUTE}"
				break
			}
			seen[c] = true
		}
	}
	switch {
	case req.AllowTolerance && !domain.ValidToleranceCode(req.ToleranceGroup):
		fields["toleranceGroup"] = "is required with allowTolerance: 1 to 10 characters of A-Z, 0-9 or underscore"
	case !req.AllowTolerance && req.ToleranceGroup != "":
		fields["toleranceGroup"] = "is only used with allowTolerance"
	}
	if req.Sequence == 0 {
		req.Sequence = 100
	}
	if req.Sequence < 1 || req.Sequence > 9999 {
		fields["sequence"] = "must be 1 to 9999"
	}
	version, versionOK := parseIfMatch(r.Header.Get("If-Match"))
	if !versionOK {
		fields["If-Match"] = `must be the rule version as an entity tag, for example "3"`
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	rule, created, err := a.store.PutAutoClearingRule(r.Context(), ledgerID, id, version, store.AutoClearingRule{Name: req.Name,
		AccountFrom: req.AccountFrom, AccountTo: req.AccountTo, Criteria: req.Criteria, AllowTolerance: req.AllowTolerance,
		ToleranceGroup: req.ToleranceGroup, Sequence: req.Sequence, Active: active})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeAutoRule(w, status, rule)
}

type autoRunRequest struct {
	LedgerID     string `json:"ledgerId"`
	RuleID       string `json:"ruleId"`
	Mode         string `json:"mode"`
	ClearingDate string `json:"clearingDate"`
}

// runAutoClearing simulates (200, nothing stored) or clears (201 with the stored
// run, 200 when the Idempotency-Key replays one).
func (a *API) runAutoClearing(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req autoRunRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := store.AutoClearingInput{LedgerID: req.LedgerID, RuleID: req.RuleID, Mode: req.Mode}
	if !ids.Valid(req.LedgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	if req.RuleID != "" && !ids.Valid(req.RuleID) {
		fields["ruleId"] = "must be a UUID"
	}
	if req.Mode != "simulate" && req.Mode != "clear" {
		fields["mode"] = "must be simulate or clear"
	}
	in.ClearingDate = a.today()
	if req.ClearingDate != "" {
		d, ok := parseDate(req.ClearingDate)
		if !ok {
			fields["clearingDate"] = "must be a date as YYYY-MM-DD"
		}
		in.ClearingDate = d
	}
	if req.Mode == "clear" {
		in.Key = idempotencyKey(r, fields)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	// The hash covers the body as sent: a repeat without clearingDate on a later day
	// still replays the stored run instead of conflicting with it.
	hash := sha256.Sum256([]byte(strings.Join([]string{req.LedgerID, req.RuleID, req.Mode, req.ClearingDate}, "|")))
	in.Hash, in.TraceID, in.Today = hash[:], traceID(r), a.today()
	run, created, err := a.store.RunAutoClearing(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, run)
}

func (a *API) listAutoRuns(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ledgerID := r.URL.Query().Get("ledger")
	if !ids.Valid(ledgerID) {
		validationFailed(w, r, map[string]string{"ledger": "is required and must be a UUID"})
		return
	}
	items, err := a.store.ListAutoClearingRuns(r.Context(), ledgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getAutoRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAutoRunNotFound, "automatic clearing run not found"))
		return
	}
	run, err := a.store.GetAutoClearingRun(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}
