package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
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
	r.With(authz.RequireDuty(dutyRead)).Get("/ledger-sets", a.listLedgerSets)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledger-sets/{setId}", a.getLedgerSet)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{id}/account-mappings", a.listAccountMappings)
	r.With(authz.RequireAnyDuty(dutyPost, dutySetPost)).Post("/ledger-sets/{setId}/journal-entries", a.postSetEntry)
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
	if len(req.ParallelLedgerIDs) == 0 {
		fields["parallelLedgerIds"] = "must contain at least one parallel ledger"
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
	id := chi.URLParam(r, "setId")
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

func (a *API) listLedgerSets(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	after, ok := pageCursor(w, r)
	if !ok {
		return
	}
	sets, next, err := a.store.ListLedgerSets(r.Context(), after)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writePage(w, sets, next)
}

func (a *API) listAccountMappings(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "LEDGER_NOT_FOUND", "ledger not found"))
		return
	}
	after, ok := pageCursor(w, r)
	if !ok {
		return
	}
	mappings, next, err := a.store.ListAccountMappings(r.Context(), id, after)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writePage(w, mappings, next)
}

func pageCursor(w http.ResponseWriter, r *http.Request) (string, bool) {
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && !ids.Valid(cursor) {
		validationFailed(w, r, map[string]string{"cursor": "is not a cursor returned by this endpoint"})
		return "", false
	}
	return cursor, true
}

func writePage[T any](w http.ResponseWriter, items []T, next string) {
	body := map[string]any{"items": items}
	if next != "" {
		body["nextCursor"] = next
	}
	httpx.JSON(w, http.StatusOK, body)
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

type ledgerLinesDTO struct {
	LedgerID string    `json:"ledgerId"`
	Lines    []lineDTO `json:"lines"`
}

type setEntryRequest struct {
	DocumentID   string           `json:"documentId"`
	PostingDate  string           `json:"postingDate"`
	DocumentDate *string          `json:"documentDate"`
	Description  string           `json:"description"`
	Source       sourceDTO        `json:"source"`
	Lines        []lineDTO        `json:"lines"`
	LedgerLines  []ledgerLinesDTO `json:"ledgerLines"`
}

type setEntryPayload struct {
	postEntryRequest
	LedgerLines []ledgerLinesDTO `json:"ledgerLines,omitempty"`
}

func ledgerLinesInput(body []ledgerLinesDTO, fields map[string]string) map[string][]domain.LineInput {
	out := make(map[string][]domain.LineInput, len(body))
	for i, group := range body {
		prefix := fmt.Sprintf("ledgerLines[%d]", i)
		id, ok := ids.Canonical(group.LedgerID)
		if !ok {
			fields[prefix+".ledgerId"] = "must be a UUID"
		}
		if _, dup := out[id]; dup && ok {
			fields[prefix+".ledgerId"] = "each ledger may appear once"
		}
		if len(group.Lines) == 0 {
			fields[prefix+".lines"] = "at least one line is required"
		}
		lines := make([]domain.LineInput, len(group.Lines))
		for j, l := range group.Lines {
			lines[j] = lineInput(fmt.Sprintf("%s.lines[%d]", prefix, j), l, fields)
		}
		out[id] = lines
	}
	return out
}

func canonicalGroups(groups []ledgerLinesDTO) []ledgerLinesDTO {
	out := slices.Clone(groups)
	for i := range out {
		if id, ok := ids.Canonical(out[i].LedgerID); ok {
			out[i].LedgerID = id
		}
	}
	slices.SortFunc(out, func(x, y ledgerLinesDTO) int { return strings.Compare(x.LedgerID, y.LedgerID) })
	return out
}

func (a *API) postSetEntry(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	setID := chi.URLParam(r, "setId")
	if !ids.Valid(setID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSetNotFound, "ledger set not found"))
		return
	}
	var body setEntryRequest
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req := postEntryRequest{LedgerID: setID, DocumentID: body.DocumentID, PostingDate: body.PostingDate,
		DocumentDate: body.DocumentDate, Description: body.Description, Source: body.Source, Lines: body.Lines}
	in, fields := a.toPostInput(req, r)
	if len(body.LedgerLines) > 0 && len(body.Lines) == 0 {
		delete(fields, "lines")
	}
	in.LedgerLines = ledgerLinesInput(body.LedgerLines, fields)
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	hash, err := domain.HashPayload(setEntryPayload{postEntryRequest: req, LedgerLines: canonicalGroups(body.LedgerLines)})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in.Key, in.Hash, in.Actor = key, hash, p.UserID
	in.CanPostLedgerSpecific = p.HasDuty(store.DutyPostLedgerSpecific)
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
