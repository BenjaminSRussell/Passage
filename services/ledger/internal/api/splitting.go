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

// registerSplitting mounts the document splitting rules (FR-11.23).
func (a *API) registerSplitting(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/splitting-rules", a.listSplittingRules)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/splitting-rules/{id}", a.getSplittingRule)
	r.With(authz.RequireDuty(dutyRulesWrite)).Put("/ledgers/{ledger}/splitting-rules/{id}", a.putSplittingRule)
}

type splittingRequest struct {
	SourceType                 string            `json:"sourceType"`
	SplittingDimensions        []string          `json:"splittingDimensions"`
	ZeroBalanceClearingAccount string            `json:"zeroBalanceClearingAccount"`
	DefaultSplittingValue      map[string]string `json:"defaultSplittingValue"`
	Active                     bool              `json:"active"`
}

func writeSplitting(w http.ResponseWriter, status int, rule domain.SplittingRule) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(rule.Version)))
	httpx.JSON(w, status, rule)
}

func (a *API) listSplittingRules(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListSplittingRules(r.Context(), ledgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func splittingID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSplittingNotFound, "splitting rule not found"))
		return "", false
	}
	return id, true
}

func (a *API) getSplittingRule(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	id, ok := splittingID(w, r)
	if !ok {
		return
	}
	rule, err := a.store.GetSplittingRule(r.Context(), ledgerID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeSplitting(w, http.StatusOK, rule)
}

// putSplittingRule creates (201) or replaces (200, checked against If-Match when sent) the
// splitting rule of one source type. Definition rules are checked in the store against the
// ledger's dimension attributes and chart (422 LEDGER_SPLITTING_RULE_INVALID).
func (a *API) putSplittingRule(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	var req splittingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		fields["id"] = "must be a UUID"
	}
	version, versionOK := parseIfMatch(r.Header.Get("If-Match"))
	if !versionOK {
		fields["If-Match"] = `must be the splitting rule version as an entity tag, for example "3"`
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if req.SplittingDimensions == nil {
		req.SplittingDimensions = []string{}
	}
	if req.DefaultSplittingValue == nil {
		req.DefaultSplittingValue = map[string]string{}
	}
	rule, created, err := a.store.PutSplittingRule(r.Context(), ledgerID, id, version, domain.SplittingRule{SourceType: req.SourceType,
		SplittingDimensions: req.SplittingDimensions, ZeroBalanceClearingAccount: req.ZeroBalanceClearingAccount,
		DefaultSplittingValue: req.DefaultSplittingValue, Active: req.Active})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeSplitting(w, status, rule)
}
