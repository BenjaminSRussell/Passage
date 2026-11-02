package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const maxMatchParts = 50

var itemTypes = map[string]bool{"ledger": true, "ar": true, "ap": true}

type createRuleRequest struct {
	LegalEntityID   string         `json:"legalEntityId"`
	BankAccountID   *string        `json:"bankAccountId"`
	Name            string         `json:"name"`
	Priority        int            `json:"priority"`
	Weights         domain.Weights `json:"weights"`
	AmountTolerance string         `json:"amountTolerance"`
	DateWindowDays  int            `json:"dateWindowDays"`
	Strict          bool           `json:"strict"`
}

type runRequest struct {
	StatementID   string          `json:"statementId"`
	LegalEntityID string          `json:"legalEntityId"`
	AfterLineNo   int             `json:"afterLineNo"`
	Items         []store.RunItem `json:"items"`
}

type manualMatchRequest struct {
	StatementID   string            `json:"statementId"`
	LegalEntityID string            `json:"legalEntityId"`
	LineNos       []int             `json:"lineNos"`
	Items         []store.MatchItem `json:"items"`
	Reason        string            `json:"reason"`
}

type unmatchRequest struct {
	Reason string `json:"reason"`
}

func (a *API) createReconRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req createRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.BankAccountID != nil && !ids.Valid(*req.BankAccountID) {
		fields["bankAccountId"] = "must be a UUID"
	}
	tolerance, err := decimal.NewFromString(strings.TrimSpace(req.AmountTolerance))
	if err != nil || !tolerance.LessThan(maxAmount) {
		fields["amountTolerance"] = "must be a decimal string"
	}
	rule := domain.Rule{Name: strings.TrimSpace(req.Name), Priority: req.Priority, Weights: req.Weights, Strict: req.Strict,
		Tolerance: domain.Tolerance{Amount: tolerance, DateWindowDays: req.DateWindowDays}}
	for k, v := range rule.Problems() {
		fields[k] = v
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if outsideScope(w, r, req.LegalEntityID) {
		return
	}
	created, err := a.store.CreateReconRule(r.Context(), store.NewReconRule{
		LegalEntityID: req.LegalEntityID, BankAccountID: req.BankAccountID, Rule: rule}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.rulesCreated.Add(1)
	setETag(w, created.Version)
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listReconRules(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le, account := r.URL.Query().Get("legalEntityId"), r.URL.Query().Get("bankAccountId")
	fields := map[string]string{}
	if !ids.Valid(le) {
		fields["legalEntityId"] = "required UUID query parameter"
	}
	if account != "" && !ids.Valid(account) {
		fields["bankAccountId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	items, err := a.store.ListReconRules(r.Context(), le, account)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getReconRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeRuleNotFound, "reconciliation rule not found")
	if !ok {
		return
	}
	rule, err := a.store.GetReconRule(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, rule.Version)
	httpx.JSON(w, http.StatusOK, rule)
}

func (a *API) deactivateReconRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeRuleNotFound, "reconciliation rule not found")
	if !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	rule, err := a.store.DeactivateReconRule(r.Context(), id, version)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, rule.Version)
	httpx.JSON(w, http.StatusOK, rule)
}

func validDay(s string) bool {
	_, err := domain.Day(s)
	return err == nil
}

func validItemID(kind, id string) bool {
	return itemTypes[kind] && len(strings.TrimSpace(id)) >= 1 && len(id) <= 100
}

// runReconciliation applies the account's stored rules to a statement against
// the open items the caller offers (the ledger, AR and AP subledgers own those).
func (a *API) runReconciliation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req runRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.StatementID) {
		fields["statementId"] = "must be a UUID"
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be the UUID of the legal entity the items belong to"
	}
	if req.AfterLineNo < 0 {
		fields["afterLineNo"] = "must not be negative"
	}
	if n := len(req.Items); n < 1 || n > store.MaxRunItems {
		fields["items"] = "1 to " + strconv.Itoa(store.MaxRunItems) + " items"
	}
	seen := map[string]bool{}
	for i, it := range req.Items {
		at := "items[" + strconv.Itoa(i) + "]"
		req.Items[i].Amount = strings.TrimSpace(it.Amount)
		amount, err := decimal.NewFromString(req.Items[i].Amount)
		switch {
		case !validItemID(it.Type, it.ID):
			fields[at] = "type must be ledger, ar or ap and id 1 to 100 characters"
		case seen[it.Type+":"+it.ID]:
			fields[at] = "duplicate item"
		case err != nil || amount.IsZero() || !amount.Abs().LessThan(maxAmount) || !amount.Equal(amount.Round(6)):
			fields[at] = "amount must be a non-zero decimal string"
		case !domain.ValidCurrency(it.Currency):
			fields[at] = "currency must be an ISO 4217 code"
		case !validDay(it.ValueDate):
			fields[at] = "valueDate must be a YYYY-MM-DD date"
		case len(it.Reference) > 140 || len(it.Counterparty) > 140 || len(it.Batch) > 140:
			fields[at] = "reference, counterparty and batch are at most 140 characters"
		}
		seen[it.Type+":"+it.ID] = true
		if len(fields) >= 10 {
			break
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if outsideScope(w, r, req.LegalEntityID) {
		return
	}
	res, err := a.store.RunReconciliation(r.Context(), store.RunInput{
		StatementID: req.StatementID, LegalEntityID: req.LegalEntityID, AfterLineNo: req.AfterLineNo, Items: req.Items}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.reconRuns.Add(1)
	a.autoMatches.Add(int64(len(res.Matches)))
	httpx.JSON(w, http.StatusCreated, res)
}

// createManualMatch matches chosen lines to chosen items (FR-36.3 manual match).
func (a *API) createManualMatch(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req manualMatchRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.StatementID) {
		fields["statementId"] = "must be a UUID"
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be the UUID of the legal entity the items belong to"
	}
	if n := len(req.LineNos); n < 1 || n > maxMatchParts {
		fields["lineNos"] = "1 to " + strconv.Itoa(maxMatchParts) + " line numbers"
	}
	lineSeen := map[int]bool{}
	for _, n := range req.LineNos {
		if n < 1 || lineSeen[n] {
			fields["lineNos"] = "line numbers must be positive and distinct"
		}
		lineSeen[n] = true
	}
	if n := len(req.Items); n < 1 || n > maxMatchParts {
		fields["items"] = "1 to " + strconv.Itoa(maxMatchParts) + " items"
	}
	itemSeen := map[string]bool{}
	for i, it := range req.Items {
		at := "items[" + strconv.Itoa(i) + "]"
		req.Items[i].Amount = strings.TrimSpace(it.Amount)
		amount, err := decimal.NewFromString(req.Items[i].Amount)
		switch {
		case !validItemID(it.Type, it.ID):
			fields[at] = "type must be ledger, ar or ap and id 1 to 100 characters"
		case itemSeen[it.Type+":"+it.ID]:
			fields[at] = "duplicate item"
		case err != nil || amount.IsZero() || !amount.Abs().LessThan(maxAmount) || !amount.Equal(amount.Round(6)):
			fields[at] = "amount must be a non-zero decimal string"
		case !domain.ValidCurrency(it.Currency):
			fields[at] = "currency must be an ISO 4217 code"
		}
		itemSeen[it.Type+":"+it.ID] = true
		if len(fields) >= 10 {
			break
		}
	}
	reason := strings.TrimSpace(req.Reason)
	if !domain.ValidVoidReason(reason) {
		fields["reason"] = "required, 1 to 200 characters"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if outsideScope(w, r, req.LegalEntityID) {
		return
	}
	m, err := a.store.CreateManualMatch(r.Context(), store.NewMatch{
		StatementID: req.StatementID, LegalEntityID: req.LegalEntityID, LineNos: req.LineNos, Items: req.Items, Reason: reason}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.manualMatches.Add(1)
	setETag(w, m.Version)
	httpx.JSON(w, http.StatusCreated, m)
}

func (a *API) listMatches(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	statement := r.URL.Query().Get("statementId")
	if !ids.Valid(statement) {
		validationFailed(w, r, map[string]string{"statementId": "required UUID query parameter"})
		return
	}
	limit, ok := intQuery(w, r, "limit", store.MaxMatchesListed)
	if !ok {
		return
	}
	offset, ok := intQuery(w, r, "offset", 0)
	if !ok {
		return
	}
	if limit < 1 || limit > store.MaxMatchesListed {
		validationFailed(w, r, map[string]string{"limit": "1 to " + strconv.Itoa(store.MaxMatchesListed)})
		return
	}
	items, err := a.store.ListMatches(r.Context(), statement, limit, offset)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	page := map[string]any{"items": items}
	if len(items) == limit {
		page["nextOffset"] = offset + limit
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getMatch(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeMatchNotFound, "reconciliation match not found")
	if !ok {
		return
	}
	m, err := a.store.GetMatch(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, m.Version)
	httpx.JSON(w, http.StatusOK, m)
}

// unmatch dissolves a match; a reason is required.
func (a *API) unmatch(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeMatchNotFound, "reconciliation match not found")
	if !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	var req unmatchRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if !domain.ValidVoidReason(reason) {
		validationFailed(w, r, map[string]string{"reason": "required, 1 to 200 characters"})
		return
	}
	m, err := a.store.Unmatch(r.Context(), id, version, reason)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.unmatches.Add(1)
	setETag(w, m.Version)
	httpx.JSON(w, http.StatusOK, m)
}
