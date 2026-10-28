package api

import (
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const maxParallelLedgers = 20

func (a *API) registerLedgerSets(r chi.Router) {
	r.With(authz.RequireDuty(dutySetupWrite)).Post("/ledger-sets", a.createLedgerSet)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledger-sets/{id}", a.getLedgerSet)
	r.With(authz.RequireDuty(dutyPost)).Post("/ledger-sets/{id}/journal-entries", a.postSetEntry)
	r.With(authz.RequireDuty(dutySetupWrite)).Post("/ledgers/{id}/account-mappings", a.createAccountMapping)
}

type createLedgerSetRequest struct {
	Code              string   `json:"code"`
	Name              string   `json:"name"`
	LeadingLedgerID   string   `json:"leadingLedgerId"`
	ParallelLedgerIDs []string `json:"parallelLedgerIds"`
}

func (a *API) createLedgerSet(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req createLedgerSetRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !codeRe.MatchString(req.Code) {
		fields["code"] = "must be 1 to 10 characters of A-Z or 0-9"
	}
	if n := utf8.RuneCountInString(req.Name); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if !ids.Valid(req.LeadingLedgerID) {
		fields["leadingLedgerId"] = "must be a UUID"
	}
	if len(req.ParallelLedgerIDs) > maxParallelLedgers {
		fields["parallelLedgerIds"] = "at most 20 parallel ledgers"
	}
	for _, id := range req.ParallelLedgerIDs {
		if !ids.Valid(id) {
			fields["parallelLedgerIds"] = "each entry must be a UUID"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	set, err := a.store.CreateLedgerSet(r.Context(), store.LedgerSetInput{Code: req.Code, Name: req.Name,
		LeadingLedgerID: req.LeadingLedgerID, ParallelLedgerIDs: req.ParallelLedgerIDs})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, set)
}

func (a *API) getLedgerSet(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSetNotFound, "ledger set not found"))
		return
	}
	set, err := a.store.GetLedgerSet(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, set)
}

type accountMappingRequest struct {
	SourceAccount string `json:"sourceAccount"`
	TargetAccount string `json:"targetAccount"`
	EffectiveFrom string `json:"effectiveFrom"`
}

func (a *API) createAccountMapping(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "LEDGER_NOT_FOUND", "ledger not found"))
		return
	}
	var req accountMappingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !accountNumberRe.MatchString(req.SourceAccount) {
		fields["sourceAccount"] = "must be 1 to 20 characters of A-Z, a-z, 0-9, '.', '_' or '-'"
	}
	if !accountNumberRe.MatchString(req.TargetAccount) {
		fields["targetAccount"] = "must be 1 to 20 characters of A-Z, a-z, 0-9, '.', '_' or '-'"
	}
	if req.SourceAccount == req.TargetAccount {
		fields["targetAccount"] = "must differ from sourceAccount"
	}
	if _, ok := parseDate(req.EffectiveFrom); !ok {
		fields["effectiveFrom"] = "must be a date as YYYY-MM-DD"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	m, err := a.store.CreateAccountMapping(r.Context(), store.AccountMapping{LedgerID: id, SourceAccount: req.SourceAccount,
		TargetAccount: req.TargetAccount, EffectiveFrom: req.EffectiveFrom})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, m)
}

func (a *API) postSetEntry(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	setID := chi.URLParam(r, "id")
	if !ids.Valid(setID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSetNotFound, "ledger set not found"))
		return
	}
	var req postEntryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.LedgerID = setID
	in, fields := a.toPostInput(req, r)
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	hash, err := domain.HashPayload(req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in.Key, in.Hash, in.Actor = key, hash, p.UserID
	res, err := a.store.PostSetEntry(r.Context(), setID, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if res.Idempotency.Replayed {
		a.replayed.Add(1)
		httpx.JSON(w, http.StatusOK, res)
		return
	}
	a.posted.Add(int64(len(res.Entries)))
	httpx.JSON(w, http.StatusCreated, res)
}
