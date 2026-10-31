package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

// Duties of open-item clearing (PRD §2.11.17): "Clear open items", "Reset
// clearings" and "Maintain clearing rules". Reading uses the journal read duty.
const (
	dutyClearingWrite = "ledger.clearing.write"
	dutyClearingReset = "ledger.clearing.reset"
	dutyClearingRules = "ledger.clearing.rules"

	maxToleranceName = 60
	maxResetComment  = 255
)

// registerOpenItems mounts open-item accounts, open items, manual clearing and
// tolerance groups (FR-11.19, FR-11.20).
func (a *API) registerOpenItems(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/open-item-accounts", a.listOpenItemAccounts)
	r.With(authz.RequireDuty(dutyAccountWrite)).Put("/ledgers/{ledger}/open-item-accounts/{account}", a.putOpenItemAccount)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/tolerance-groups", a.listToleranceGroups)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/tolerance-groups/{code}", a.getToleranceGroup)
	r.With(authz.RequireDuty(dutyClearingRules)).Put("/ledgers/{ledger}/tolerance-groups/{code}", a.putToleranceGroup)
	r.With(authz.RequireDuty(dutyRead)).Get("/open-items", a.listOpenItems)
	r.With(authz.RequireDuty(dutyClearingWrite)).Post("/clearings:simulate", a.simulateClearing)
	r.With(authz.RequireDuty(dutyClearingWrite)).Post("/clearings", a.createClearing)
	r.With(authz.RequireDuty(dutyRead)).Get("/clearings", a.listClearings)
	r.With(authz.RequireDuty(dutyRead)).Get("/clearings/{id}", a.getClearing)
	r.With(authz.RequireDuty(dutyClearingReset)).Post("/clearings/{id}/reset", a.resetClearing)
}

// pathLedger checks the caller and reads the {ledger} path segment.
func pathLedger(w http.ResponseWriter, r *http.Request) (string, bool) {
	if _, ok := principal(w, r); !ok {
		return "", false
	}
	return ledgerParam(w, r)
}

func (a *API) listOpenItemAccounts(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListOpenItemAccounts(r.Context(), ledgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type openItemAccountRequest struct {
	Enabled *bool `json:"enabled"`
}

// putOpenItemAccount switches open-item management of an account on or off:
// 201 for the first setting of the account in the ledger, 200 after.
func (a *API) putOpenItemAccount(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	number := chi.URLParam(r, "account")
	var req openItemAccountRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !accountNumberRe.MatchString(number) {
		fields["account"] = "must be 1 to 20 characters of A-Z, a-z, 0-9, '.', '_' or '-'"
	}
	if req.Enabled == nil {
		fields["enabled"] = "is required"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	acc, created, err := a.store.PutOpenItemAccount(r.Context(), ledgerID, number, *req.Enabled)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, acc)
}

// --- tolerance groups ---------------------------------------------------------

type toleranceRequest struct {
	Name              string `json:"name"`
	DebitLimit        string `json:"debitLimit"`
	CreditLimit       string `json:"creditLimit"`
	PercentLimit      string `json:"percentLimit"`
	DifferenceAccount string `json:"differenceAccount"`
	Active            *bool  `json:"active"`
}

func writeTolerance(w http.ResponseWriter, status int, t store.ToleranceGroup) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(t.Version)))
	httpx.JSON(w, status, t)
}

func (a *API) listToleranceGroups(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListToleranceGroups(r.Context(), ledgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getToleranceGroup(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	code := chi.URLParam(r, "code")
	if !domain.ValidToleranceCode(code) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeToleranceNotFound, "tolerance group not found"))
		return
	}
	t, err := a.store.GetToleranceGroup(r.Context(), ledgerID, code)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeTolerance(w, http.StatusOK, t)
}

// decimalText normalizes a non-negative decimal string with at most 6 decimals.
func decimalText(raw string, max decimal.Decimal) (string, bool) {
	d, err := decimal.NewFromString(raw)
	if err != nil || d.IsNegative() || d.GreaterThan(max) || d.Exponent() < -6 {
		return "", false
	}
	return d.String(), true
}

// putToleranceGroup creates (201) or replaces (200, checked against If-Match when sent) a group.
func (a *API) putToleranceGroup(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	code := chi.URLParam(r, "code")
	var req toleranceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !domain.ValidToleranceCode(code) {
		fields["code"] = "must be 1 to 10 characters of A-Z, 0-9 or underscore"
	}
	if n := utf8.RuneCountInString(req.Name); n < 1 || n > maxToleranceName {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxToleranceName)
	}
	maxLimit := decimal.NewFromInt(100000000000)
	debitLimit, debitOK := decimalText(req.DebitLimit, maxLimit)
	if !debitOK {
		fields["debitLimit"] = "must be a non-negative decimal with at most 6 decimals"
	}
	creditLimit, creditOK := decimalText(req.CreditLimit, maxLimit)
	if !creditOK {
		fields["creditLimit"] = "must be a non-negative decimal with at most 6 decimals"
	}
	if req.PercentLimit == "" {
		req.PercentLimit = "0"
	}
	pct, pctOK := decimalText(req.PercentLimit, decimal.NewFromInt(100))
	if !pctOK {
		fields["percentLimit"] = "must be a decimal from 0 to 100 with at most 6 decimals"
	}
	if !accountNumberRe.MatchString(req.DifferenceAccount) {
		fields["differenceAccount"] = "must be 1 to 20 characters of A-Z, a-z, 0-9, '.', '_' or '-'"
	}
	version, versionOK := parseIfMatch(r.Header.Get("If-Match"))
	if !versionOK {
		fields["If-Match"] = `must be the tolerance group version as an entity tag, for example "3"`
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	t, created, err := a.store.PutToleranceGroup(r.Context(), ledgerID, code, version, store.ToleranceGroup{Name: req.Name,
		DebitLimit: debitLimit, CreditLimit: creditLimit, PercentLimit: pct, DifferenceAccount: req.DifferenceAccount, Active: active})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeTolerance(w, status, t)
}

// --- open items ----------------------------------------------------------------

var itemStatuses = map[string]bool{store.ItemOpen: true, store.ItemCleared: true, store.ItemResetOpen: true, store.ItemVoided: true}

func (a *API) listOpenItems(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.OpenItemFilter{LedgerID: q.Get("ledger"), Account: q.Get("account"), Status: q.Get("status"),
		Assignment: q.Get("assignment"), Cursor: q.Get("cursor"), Dimensions: map[string]string{}, Limit: defaultLinePage}
	if !ids.Valid(f.LedgerID) {
		fields["ledger"] = "is required and must be a UUID"
	}
	if f.Account != "" && !accountNumberRe.MatchString(f.Account) {
		fields["account"] = "must be 1 to 20 characters of letters, digits, dot, underscore or dash"
	}
	if f.Status != "" && !itemStatuses[f.Status] {
		fields["status"] = "must be open, cleared, reset_open or voided"
	}
	if utf8.RuneCountInString(f.Assignment) > 50 {
		fields["assignment"] = "must be at most 50 characters"
	}
	if f.Cursor != "" {
		if n, err := strconv.ParseInt(f.Cursor, 10, 64); err != nil || n < 0 {
			fields["cursor"] = "is not a cursor returned by this endpoint"
		}
	}
	for key, values := range q {
		attr, ok := strings.CutPrefix(key, "dim.")
		if !ok {
			continue
		}
		if !dimAttrRe.MatchString(attr) {
			fields[key] = "the attribute must be A-Z, digits or underscore, starting with a letter"
			continue
		}
		if len(values) != 1 || values[0] == "" || utf8.RuneCountInString(values[0]) > 20 {
			fields[key] = "must be given once, 1 to 20 characters"
			continue
		}
		f.Dimensions[attr] = values[0]
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPageSize {
			fields["limit"] = "must be an integer from 1 to 1000"
		} else {
			f.Limit = n
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	page, err := a.store.ListOpenItems(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

// --- clearing ------------------------------------------------------------------

type clearingRequest struct {
	LedgerID       string   `json:"ledgerId"`
	ItemIDs        []string `json:"itemIds"`
	ClearingDate   string   `json:"clearingDate"`
	ToleranceGroup string   `json:"toleranceGroup"`
	ResidualItem   bool     `json:"residualItem"`
}

// clearingInput validates a clearing request. The hash covers the request with
// the item ids sorted, so the same selection in another order replays.
func (a *API) clearingInput(w http.ResponseWriter, r *http.Request, keyed bool) (store.ClearingInput, bool) {
	var in store.ClearingInput
	if _, ok := principal(w, r); !ok {
		return in, false
	}
	var req clearingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return in, false
	}
	fields := map[string]string{}
	if keyed {
		in.Key = idempotencyKey(r, fields)
	}
	if !ids.Valid(req.LedgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	if n := len(req.ItemIDs); n < domain.MinClearingItems || n > domain.MaxClearingItems {
		fields["itemIds"] = fmt.Sprintf("select %d to %d open items", domain.MinClearingItems, domain.MaxClearingItems)
	} else {
		seen := map[string]bool{}
		for _, id := range req.ItemIDs {
			if !ids.Valid(id) || seen[id] {
				fields["itemIds"] = "must be distinct UUIDs"
				break
			}
			seen[id] = true
		}
	}
	date, dateOK := parseDate(req.ClearingDate)
	if !dateOK {
		fields["clearingDate"] = "must be a date as YYYY-MM-DD"
	}
	if req.ToleranceGroup != "" && !domain.ValidToleranceCode(req.ToleranceGroup) {
		fields["toleranceGroup"] = "must be 1 to 10 characters of A-Z, 0-9 or underscore"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return in, false
	}
	sorted := append([]string(nil), req.ItemIDs...)
	sort.Strings(sorted)
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%t", req.LedgerID, strings.Join(sorted, ","), req.ClearingDate, req.ToleranceGroup, req.ResidualItem)))
	in.LedgerID, in.ItemIDs, in.ClearingDate, in.ToleranceGroup, in.ResidualItem = req.LedgerID, req.ItemIDs, date, req.ToleranceGroup, req.ResidualItem
	in.Hash, in.TraceID, in.Today = hash[:], traceID(r), a.today()
	return in, true
}

func (a *API) simulateClearing(w http.ResponseWriter, r *http.Request) {
	in, ok := a.clearingInput(w, r, false)
	if !ok {
		return
	}
	pv, err := a.store.SimulateClearing(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, pv)
}

// createClearing clears the items: 201 with the new clearing document, 200 when
// the Idempotency-Key replays an earlier one.
func (a *API) createClearing(w http.ResponseWriter, r *http.Request) {
	in, ok := a.clearingInput(w, r, true)
	if !ok {
		return
	}
	doc, created, err := a.store.ClearItems(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, doc)
}

func (a *API) listClearings(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ledgerID, status := r.URL.Query().Get("ledger"), r.URL.Query().Get("status")
	fields := map[string]string{}
	if !ids.Valid(ledgerID) {
		fields["ledger"] = "is required and must be a UUID"
	}
	if status != "" && status != "cleared" && status != "reset" {
		fields["status"] = "must be cleared or reset"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	items, err := a.store.ListClearings(r.Context(), ledgerID, status)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func clearingID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if _, ok := principal(w, r); !ok {
		return "", false
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeClearingNotFound, "clearing document not found"))
		return "", false
	}
	return id, true
}

func (a *API) getClearing(w http.ResponseWriter, r *http.Request) {
	id, ok := clearingID(w, r)
	if !ok {
		return
	}
	doc, err := a.store.GetClearing(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, doc)
}

type resetRequest struct {
	ReasonCode string `json:"reasonCode"`
	Comment    string `json:"comment"`
}

// resetClearing resets a clearing as a whole. The reason code (a shared ledger
// reason code, FR-11.15) is required; it is also the reason of the reversal of
// the difference entry when the clearing posted one.
func (a *API) resetClearing(w http.ResponseWriter, r *http.Request) {
	id, ok := clearingID(w, r)
	if !ok {
		return
	}
	var req resetRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	key := idempotencyKey(r, fields)
	if !domain.ValidReasonCode(req.ReasonCode) {
		fields["reasonCode"] = "is required: 1 to 10 characters of A-Z, 0-9 or underscore"
	}
	if utf8.RuneCountInString(req.Comment) > maxResetComment {
		fields["comment"] = fmt.Sprintf("must be at most %d characters", maxResetComment)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	doc, err := a.store.ResetClearing(r.Context(), store.ResetInput{ID: id, Key: key, ReasonCode: req.ReasonCode,
		Comment: req.Comment, TraceID: traceID(r), Today: a.today()})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, doc)
}
