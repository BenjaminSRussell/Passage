package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

// Journal workspace duties (§2.11.17). Posting a batch needs dutyPost ("Post
// ledger journals"); posting a validated batch of a journal name without
// workflow also needs store.DutyPostDirect ("without approval").
const (
	dutyJournalWrite   = "ledger.journal.write"
	dutyJournalApprove = "ledger.journal.approve"
	maxJournalNameName = 60
)

var (
	journalNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)
	batchStatuses = map[string]bool{domain.BatchDraft: true, domain.BatchValidated: true, domain.BatchInApproval: true,
		domain.BatchApproved: true, domain.BatchRejected: true, domain.BatchPosting: true, domain.BatchPosted: true,
		domain.BatchCancelled: true}
)

// registerBatches mounts journal names and the manual journal lifecycle.
// Lifecycle actions are sub-paths (/submit, /approve, ...) like
// /journal-entries/{id}/reverse, because chi cannot route a colon suffix after
// a path parameter.
func (a *API) registerBatches(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/journal-names", a.listJournalNames)
	r.With(authz.RequireDuty(dutyRead)).Get("/journal-names/{code}", a.getJournalName)
	r.With(authz.RequireDuty(dutySetupWrite)).Put("/journal-names/{code}", a.putJournalName)
	r.With(authz.RequireDuty(dutyRead)).Get("/journal-batches", a.listBatches)
	r.With(authz.RequireDuty(dutyJournalWrite)).Post("/journal-batches", a.createBatch)
	r.With(authz.RequireDuty(dutyRead)).Get("/journal-batches/{id}", a.getBatch)
	r.With(authz.RequireDuty(dutyJournalWrite)).Patch("/journal-batches/{id}", a.patchBatch)
	r.With(authz.RequireDuty(dutyJournalWrite)).Post("/journal-batches/{id}/submit", a.submitBatch)
	r.With(authz.RequireDuty(dutyJournalApprove)).Post("/journal-batches/{id}/approve", a.approveBatch)
	r.With(authz.RequireDuty(dutyJournalApprove)).Post("/journal-batches/{id}/reject", a.rejectBatch)
	r.With(authz.RequireAnyDuty(dutyPost, dutyBatchPost)).Post("/journal-batches/{id}/post", a.postBatch)
	r.With(authz.RequireDuty(dutyJournalWrite)).Post("/journal-batches/{id}/cancel", a.cancelBatch)
}

// --- journal names -----------------------------------------------------------

type putJournalNameRequest struct {
	Name                   string `json:"name"`
	RequiresApproval       bool   `json:"requiresApproval"`
	SelfApprovalAllowed    bool   `json:"selfApprovalAllowed"`
	ReversalReasonRequired bool   `json:"reversalReasonRequired"`
	AllowReconAccounts     bool   `json:"allowReconAccounts"`
	LinesLimit             *int   `json:"linesLimit"`
	Active                 *bool  `json:"active"`
	// PostingRestriction is "none" (default) or "own_journals_only"; DescriptionLanguage is
	// "legal_entity" (default) or "user" (PRD 2.11).
	PostingRestriction  string `json:"postingRestriction"`
	DescriptionLanguage string `json:"descriptionLanguage"`
}

func (a *API) listJournalNames(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	items, err := a.store.ListJournalNames(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getJournalName(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	code := chi.URLParam(r, "code")
	if !journalNameRe.MatchString(code) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "LEDGER_JOURNAL_NAME_NOT_FOUND", "journal name not found"))
		return
	}
	n, err := a.store.GetJournalName(r.Context(), code)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeTagged(w, http.StatusOK, n.LockVersion, n)
}

// putJournalName creates or replaces a journal name: 201 when created, 200 when replaced.
func (a *API) putJournalName(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	code := chi.URLParam(r, "code")
	tag, tagOK := replaceTag(w, r)
	if !tagOK {
		return
	}
	var req putJournalNameRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !journalNameRe.MatchString(code) {
		fields["code"] = "must be 1 to 20 characters of A-Z, a-z, 0-9, underscore or dash"
	}
	name := strings.TrimSpace(req.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxJournalNameName {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxJournalNameName)
	}
	limit := domain.DefaultBatchLinesLimit
	if req.LinesLimit != nil {
		limit = *req.LinesLimit
		if limit < 1 || limit > domain.MaxLinesPerEntry {
			fields["linesLimit"] = fmt.Sprintf("must be 1 to %d (one batch posts one entry)", domain.MaxLinesPerEntry)
		}
	}
	restriction, language := req.PostingRestriction, req.DescriptionLanguage
	if restriction == "" {
		restriction = domain.PostingNone
	}
	if restriction != domain.PostingNone && restriction != domain.PostingOwnJournalsOnly {
		fields["postingRestriction"] = "must be none or own_journals_only"
	}
	if language == "" {
		language = domain.DescriptionLanguageLE
	}
	if language != domain.DescriptionLanguageLE && language != domain.DescriptionLanguageUser {
		fields["descriptionLanguage"] = "must be legal_entity or user"
	}
	if req.SelfApprovalAllowed && !req.RequiresApproval {
		fields["selfApprovalAllowed"] = "applies only to journal names that require approval"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	n, created, err := a.store.PutJournalName(r.Context(), code, tag, store.JournalName{Name: name, RequiresApproval: req.RequiresApproval,
		SelfApprovalAllowed: req.SelfApprovalAllowed, ReversalReasonRequired: req.ReversalReasonRequired,
		AllowReconAccounts: req.AllowReconAccounts, LinesLimit: limit, Active: active,
		PostingRestriction: restriction, DescriptionLanguage: language})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeTagged(w, status, n.LockVersion, n)
}

// --- journal batches ---------------------------------------------------------

// batchLineDTO is one manual journal line on the wire.
type batchLineDTO struct {
	Account       string            `json:"account"`
	Dimensions    map[string]string `json:"dimensions"`
	DebitCredit   string            `json:"debitCredit"`
	Amount        amountDTO         `json:"amount"`
	Text          string            `json:"text"`
	SourceLineRef string            `json:"sourceLineRef"`
	Quantity      string            `json:"quantity,omitempty"`
	Unit          string            `json:"unit,omitempty"`
}

type batchRequest struct {
	LedgerID     string         `json:"ledgerId"`
	JournalName  string         `json:"journalName"`
	Description  string         `json:"description"`
	PostingDate  string         `json:"postingDate"`
	DocumentDate string         `json:"documentDate"`
	Lines        []batchLineDTO `json:"lines"`
}

// batchPatchRequest edits a batch; absent fields keep their value. An empty
// documentDate clears it.
type batchPatchRequest struct {
	Description  *string         `json:"description"`
	PostingDate  *string         `json:"postingDate"`
	DocumentDate *string         `json:"documentDate"`
	Lines        *[]batchLineDTO `json:"lines"`
}

// checkBatchLines validates line shape and scale and converts to store lines.
// Rule checks (accounts, dimensions, currency, balance, periods) run at submit
// and post; the journal name's lines limit is checked by the store.
func checkBatchLines(lines []batchLineDTO, fields map[string]string) []store.BatchLine {
	if len(lines) == 0 {
		fields["lines"] = "at least one line is required"
		return nil
	}
	if len(lines) > domain.MaxLinesPerEntry {
		fields["lines"] = fmt.Sprintf("at most %d lines per batch", domain.MaxLinesPerEntry)
		return nil
	}
	out := make([]store.BatchLine, 0, len(lines))
	for i, l := range lines {
		prefix := "lines[" + strconv.Itoa(i) + "]."
		if !accountNumberRe.MatchString(l.Account) {
			fields[prefix+"account"] = "must be 1 to 20 characters of A-Z, a-z, 0-9, '.', '_' or '-'"
		}
		if l.DebitCredit != domain.SideDebit && l.DebitCredit != domain.SideCredit {
			fields[prefix+"debitCredit"] = "must be D or C"
		}
		currencyOK := currencyRe.MatchString(l.Amount.Currency)
		if !currencyOK {
			fields[prefix+"amount.currency"] = "must be an ISO 4217 alpha-3 code"
		}
		amount, err := decimal.NewFromString(l.Amount.Value)
		switch {
		case err != nil || amount.IsNegative():
			fields[prefix+"amount.value"] = "must be a non-negative decimal string; use the opposite side"
		case currencyOK && !amount.Round(money.MinorUnits(l.Amount.Currency)).Equal(amount):
			fields[prefix+"amount.value"] = "has more decimals than the currency allows"
		case amount.GreaterThanOrEqual(maxBatchAmount):
			fields[prefix+"amount.value"] = "is too large"
		}
		for attr, value := range l.Dimensions {
			if !dimAttrRe.MatchString(attr) || value == "" || utf8.RuneCountInString(value) > 20 {
				fields[prefix+"dimensions"] = "attribute names are A-Z with digits or underscores; values are 1 to 20 characters"
			}
		}
		if utf8.RuneCountInString(l.Text) > maxTextLength {
			fields[prefix+"text"] = fmt.Sprintf("must be at most %d characters", maxTextLength)
		}
		if utf8.RuneCountInString(l.SourceLineRef) > maxSourceRefLength {
			fields[prefix+"sourceLineRef"] = fmt.Sprintf("must be at most %d characters", maxSourceRefLength)
		}
		quantity := checkLineQuantity(fields, prefix, l.Quantity, l.Unit)
		out = append(out, store.BatchLine{Account: l.Account, Dimensions: l.Dimensions, DebitCredit: l.DebitCredit,
			Amount: amount.String(), Currency: l.Amount.Currency, Text: l.Text, SourceLineRef: l.SourceLineRef, Quantity: quantity, Unit: l.Unit})
	}
	return out
}

// maxBatchAmount keeps a line inside numeric(20,6).
var maxBatchAmount = decimal.New(1, 14)

func (a *API) listBatches(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	if ledger := q.Get("ledger"); ledger != "" && !ids.Valid(ledger) {
		fields["ledger"] = "must be a UUID"
	}
	if status := q.Get("status"); status != "" && !batchStatuses[status] {
		fields["status"] = "must be a journal batch status"
	}
	if name := q.Get("journalName"); name != "" && !journalNameRe.MatchString(name) {
		fields["journalName"] = "must be a journal name code"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	page, ok := pageParams(w, r, cursorTimestamp, cursorUUID)
	if !ok {
		return
	}
	batches, err := a.store.ListBatches(r.Context(), store.BatchFilter{LedgerID: q.Get("ledger"), Status: q.Get("status"),
		JournalName: q.Get("journalName"), Page: page})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	batches, next := trimPage(batches, page.Limit, func(b store.Batch) []string {
		return []string{b.CreatedAt.UTC().Format(time.RFC3339Nano), b.ID}
	})
	httpx.JSON(w, http.StatusOK, pageBody(batches, next))
}

func (a *API) createBatch(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req batchRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := store.BatchInput{LedgerID: req.LedgerID, JournalName: req.JournalName, Description: req.Description}
	if !ids.Valid(req.LedgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	if !journalNameRe.MatchString(req.JournalName) {
		fields["journalName"] = "must be a journal name code (1 to 20 characters of A-Z, a-z, 0-9, underscore or dash)"
	}
	if utf8.RuneCountInString(req.Description) > maxTextLength {
		fields["description"] = fmt.Sprintf("must be at most %d characters", maxTextLength)
	}
	if d, ok := parseDate(req.PostingDate); ok {
		in.PostingDate = d
	} else {
		fields["postingDate"] = "must be a date as YYYY-MM-DD"
	}
	if req.DocumentDate != "" {
		if d, ok := parseDate(req.DocumentDate); ok {
			in.DocumentDate = &d
		} else {
			fields["documentDate"] = "must be a date as YYYY-MM-DD"
		}
	}
	in.Lines = checkBatchLines(req.Lines, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, err := a.store.CreateBatch(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeBatch(w, http.StatusCreated, created)
}

// writeBatch renders a batch with its version as ETag (If-Match on changes).
func writeBatch(w http.ResponseWriter, status int, b store.Batch) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(b.Version)))
	httpx.JSON(w, status, b)
}

// batchTarget reads the batch id and the optional If-Match version. It writes
// the response and returns false when either is malformed.
func batchTarget(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	if _, ok := principal(w, r); !ok {
		return "", 0, false
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeBatchNotFound, "journal batch not found"))
		return "", 0, false
	}
	version, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		validationFailed(w, r, map[string]string{"If-Match": `must be the batch version as an entity tag, for example "3"`})
		return "", 0, false
	}
	return id, version, true
}

// parseIfMatch reads `"3"`, `W/"3"` or `3`; an absent header is version 0 (no check).
func parseIfMatch(raw string) (int, bool) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "W/")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(strings.Trim(raw, `"`))
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// replaceTag reads If-Match on a PUT that may replace a configuration row. An absent header is 0 (fine
// for a create; the store answers 428 for a replace). A malformed one is a 400 problem.
func replaceTag(w http.ResponseWriter, r *http.Request) (int, bool) {
	tag, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		validationFailed(w, r, map[string]string{"If-Match": `must be the entity tag (lockVersion) of the row as read, for example "3"`})
	}
	return tag, ok
}

// writeTagged renders a configuration row with its lockVersion as the ETag.
func writeTagged(w http.ResponseWriter, status int, lockVersion int, body any) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(lockVersion)))
	httpx.JSON(w, status, body)
}

func (a *API) getBatch(w http.ResponseWriter, r *http.Request) {
	id, _, ok := batchTarget(w, r)
	if !ok {
		return
	}
	b, err := a.store.GetBatch(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeBatch(w, http.StatusOK, b)
}

func (a *API) patchBatch(w http.ResponseWriter, r *http.Request) {
	id, version, ok := batchTarget(w, r)
	if !ok {
		return
	}
	var req batchPatchRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	var in store.BatchPatch
	if req.Description != nil {
		if utf8.RuneCountInString(*req.Description) > maxTextLength {
			fields["description"] = fmt.Sprintf("must be at most %d characters", maxTextLength)
		}
		in.Description = req.Description
	}
	if req.PostingDate != nil {
		if d, ok := parseDate(*req.PostingDate); ok {
			in.PostingDate = &d
		} else {
			fields["postingDate"] = "must be a date as YYYY-MM-DD"
		}
	}
	if req.DocumentDate != nil {
		if *req.DocumentDate == "" {
			in.ClearDocumentDate = true
		} else if d, ok := parseDate(*req.DocumentDate); ok {
			in.DocumentDate = &d
		} else {
			fields["documentDate"] = "must be a date as YYYY-MM-DD, or empty to clear it"
		}
	}
	if req.Lines != nil {
		lines := checkBatchLines(*req.Lines, fields)
		in.Lines = &lines
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	updated, err := a.store.UpdateBatch(r.Context(), id, version, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeBatch(w, http.StatusOK, updated)
}

func (a *API) submitBatch(w http.ResponseWriter, r *http.Request) {
	id, version, ok := batchTarget(w, r)
	if !ok {
		return
	}
	b, err := a.store.SubmitBatch(r.Context(), id, version, a.today())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeBatch(w, http.StatusOK, b)
}

func (a *API) approveBatch(w http.ResponseWriter, r *http.Request) { a.decideBatch(w, r, true) }

func (a *API) rejectBatch(w http.ResponseWriter, r *http.Request) { a.decideBatch(w, r, false) }

func (a *API) decideBatch(w http.ResponseWriter, r *http.Request, approve bool) {
	id, version, ok := batchTarget(w, r)
	if !ok {
		return
	}
	b, err := a.store.DecideBatch(r.Context(), id, version, approve)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeBatch(w, http.StatusOK, b)
}

func (a *API) cancelBatch(w http.ResponseWriter, r *http.Request) {
	id, version, ok := batchTarget(w, r)
	if !ok {
		return
	}
	b, err := a.store.CancelBatch(r.Context(), id, version)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeBatch(w, http.StatusOK, b)
}

// postBatch posts the batch as one entry. Idempotency-Key is required; a
// replay answers 200 with the stored response, a first post 201.
func (a *API) postBatch(w http.ResponseWriter, r *http.Request) {
	id, version, ok := batchTarget(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, _ := principal(w, r)
	res, err := a.store.PostBatch(r.Context(), store.PostBatchInput{ID: id, Version: version, Key: key, Today: a.today(),
		TraceID: traceID(r), CanPostDirect: p.HasDuty(store.DutyPostDirect), CanPostLedgerSpecific: p.HasDuty(store.DutyPostLedgerSpecific),
		Language: acceptLanguage(r)})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if res.Idempotency.Replayed {
		a.replayed.Add(1)
		httpx.JSON(w, http.StatusOK, res)
		return
	}
	a.posted.Add(1)
	httpx.JSON(w, http.StatusCreated, res)
}

// acceptLanguage returns the first language tag of Accept-Language in the form
// templates are stored in ("de", "de-DE"): the language is lower-cased, a
// two-letter region upper-cased, and other subtags (a script such as Hant) are
// dropped, so "de-de" gives "de-DE" and "zh-Hant-TW" gives "zh". Anything else
// gives "" (the default language applies).
func acceptLanguage(r *http.Request) string {
	first, _, _ := strings.Cut(r.Header.Get("Accept-Language"), ",")
	tag, _, _ := strings.Cut(strings.TrimSpace(first), ";")
	parts := strings.Split(strings.TrimSpace(tag), "-")
	tag = strings.ToLower(parts[0])
	if len(parts) > 1 && len(parts[1]) == 2 {
		tag += "-" + strings.ToUpper(parts[1])
	}
	if domain.ValidLanguage(tag) {
		return tag
	}
	return ""
}
