package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
)

// LedgerBalances reads a ledger's trial balance. ledgerclient.Client and
// ledgerclient.Fake implement it.
type LedgerBalances interface {
	Balances(ctx context.Context, q ledgerclient.BalanceQuery) (ledgerclient.AccountBalances, error)
}

// WithLedger gives the API the ledger the unit balances are collected from.
func (a *API) WithLedger(l LedgerBalances) *API {
	a.ledger = l
	return a
}

// MaxCollectRates bounds the rates of one collection request.
const MaxCollectRates = 100

type collectRateBody struct {
	Currency string `json:"currency"`
	Average  string `json:"average"`
	Closing  string `json:"closing"`
}

type collectBody struct {
	Period string            `json:"period"`
	Rates  []collectRateBody `json:"rates"`
}

type collectionResponse struct {
	ID        string    `json:"id"`
	GroupID   string    `json:"groupId"`
	Period    string    `json:"period"`
	Version   int       `json:"version"`
	Currency  string    `json:"currency"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	LineCount *int      `json:"lineCount,omitempty"`
	Unmapped  *int      `json:"unmappedCount,omitempty"`
}

type collectLineBody struct {
	Unit                 string `json:"unit"`
	MainAccount          string `json:"mainAccount"`
	ConsolidationAccount string `json:"consolidationAccount,omitempty"`
	AccountType          string `json:"accountType,omitempty"`
	UnitCurrency         string `json:"unitCurrency"`
	UnitAmount           string `json:"unitAmount"`
	Rate                 string `json:"rate"`
	GroupAmount          string `json:"groupAmount"`
}

type collectionDetailResponse struct {
	collectionResponse
	Lines []collectLineBody `json:"lines"`
}

type tbRowBody struct {
	Account string            `json:"account"`
	Type    string            `json:"accountType"`
	Total   string            `json:"total"`
	Units   map[string]string `json:"units"`
}

type trialBalanceResponse struct {
	collectionResponse
	Rows            []tbRowBody       `json:"rows"`
	Unmapped        []collectLineBody `json:"unmapped"`
	Total           string            `json:"total"`
	UnitDifferences map[string]string `json:"unitDifferences"`
}

func (a *API) collectorReady(w http.ResponseWriter, r *http.Request) bool { return a.groupsReady(w, r) }

// collect reads each unit's ledger balances for the period, maps them to
// consolidation accounts and translates them into the group currency, then
// stores the result as the next version of the group's collection (FR-40.8).
func (a *API) collect(w http.ResponseWriter, r *http.Request) {
	if !a.collectorReady(w, r) {
		return
	}
	groupID, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	if a.ledger == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "the ledger is not configured for this service"))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > maxIdempotencyKey {
		validation(w, r, map[string]string{"Idempotency-Key": "header is required, at most 255 characters"})
		return
	}
	var body collectBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !periodText.MatchString(body.Period) {
		fields["period"] = "must be YYYY-MM"
	}
	if len(body.Rates) > MaxCollectRates {
		fields["rates"] = fmt.Sprintf("must contain at most %d rows", MaxCollectRates)
	}
	rates := map[string]domain.CollectRate{}
	f := decimals{}
	for i, rt := range body.Rates {
		if !currencyCode.MatchString(rt.Currency) {
			fields[fmt.Sprintf("rates[%d].currency", i)] = "must be an ISO 4217 code"
			continue
		}
		rates[rt.Currency] = domain.CollectRate{
			Average: f.get(fmt.Sprintf("rates[%d].average", i), rt.Average, true),
			Closing: f.get(fmt.Sprintf("rates[%d].closing", i), rt.Closing, true),
		}
	}
	for k, v := range f {
		fields[k] = v
	}
	if !validation(w, r, fields) {
		return
	}
	group, err := a.groups.GetGroup(r.Context(), groupID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if len(group.Units) == 0 {
		httpx.Fail(w, r, problemFrom(&domain.Error{Code: domain.CodeCollectionError, Detail: "the group has no units to collect"}))
		return
	}
	mapping, err := a.groups.GetMapping(r.Context(), groupID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	income := map[string]bool{}
	for _, m := range mapping {
		income[m.MainAccount] = m.Type == domain.AccountIncome
	}
	ledgers := make([]domain.UnitLedger, 0, len(group.Units))
	for _, u := range group.Units {
		ul, err := a.readUnit(r.Context(), u, body.Period, income)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		ledgers = append(ledgers, ul)
	}
	lines, err := domain.Collect(group.Currency, mapping, ledgers, rates)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	inputs := struct {
		Period string                        `json:"period"`
		Rates  map[string]domain.CollectRate `json:"rates"`
		Units  []domain.UnitLedger           `json:"units"`
	}{body.Period, rates, ledgers}
	c, replayed, err := a.groups.CreateCollection(r.Context(), groupID, body.Period, lines, inputs, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.collections.Add(1)
	}
	w.Header().Set("Location", "/v1/consol/collections/"+c.ID)
	n, unmapped := len(lines), 0
	for _, l := range lines {
		if !l.Mapped() {
			unmapped++
		}
	}
	out := toCollectionResponse(c.ID, c.GroupID, c.Period, c.Version, c.Currency, c.CreatedBy, c.CreatedAt)
	out.LineCount, out.Unmapped = &n, &unmapped
	httpx.JSON(w, http.StatusCreated, out)
}

// readUnit reads one unit's balances: the balance sheet cumulative up to the
// period and the income accounts from the start of the fiscal year (taken as
// the calendar year of the period) so closed years do not carry over.
func (a *API) readUnit(ctx context.Context, u domain.Unit, period string, income map[string]bool) (domain.UnitLedger, error) {
	query := ledgerclient.BalanceQuery{LegalEntityID: u.LegalEntityID, LedgerID: u.LedgerID, PeriodTo: period}
	cumulative, err := a.ledger.Balances(ctx, query)
	if err != nil {
		return domain.UnitLedger{}, &domain.Error{Code: domain.CodeNoLedger, Detail: fmt.Sprintf("the ledger of unit %s could not be read: %v", u.ID, err)}
	}
	if cumulative.Currency != "" && cumulative.Currency != u.Currency {
		return domain.UnitLedger{}, &domain.Error{Code: domain.CodeCollectionError, Detail: fmt.Sprintf("unit %s is set up in %s but its ledger is in %s", u.ID, u.Currency, cumulative.Currency)}
	}
	ul := domain.UnitLedger{UnitID: u.ID, Currency: u.Currency}
	for _, row := range cumulative.Rows {
		if !income[row.Account] {
			ul.Rows = append(ul.Rows, domain.LedgerRow{Account: row.Account, Net: row.Net()})
		}
	}
	hasIncome := false
	for _, v := range income {
		hasIncome = hasIncome || v
	}
	if hasIncome {
		query.PeriodFrom = period[:4] + "-01"
		ytd, err := a.ledger.Balances(ctx, query)
		if err != nil {
			return domain.UnitLedger{}, &domain.Error{Code: domain.CodeNoLedger, Detail: fmt.Sprintf("the ledger of unit %s could not be read: %v", u.ID, err)}
		}
		for _, row := range ytd.Rows {
			if income[row.Account] {
				ul.Rows = append(ul.Rows, domain.LedgerRow{Account: row.Account, Net: row.Net()})
			}
		}
	}
	return ul, nil
}

func toCollectionResponse(id, groupID, period string, version int, currency, by string, at time.Time) collectionResponse {
	return collectionResponse{ID: id, GroupID: groupID, Period: period, Version: version, Currency: currency, CreatedBy: by, CreatedAt: at.UTC()}
}

func (a *API) listCollections(w http.ResponseWriter, r *http.Request) {
	if !a.collectorReady(w, r) {
		return
	}
	groupID, ok := pathID(w, r, "id", domain.CodeGroupNotFound)
	if !ok {
		return
	}
	list, err := a.groups.ListCollections(r.Context(), groupID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := struct {
		Items []collectionResponse `json:"items"`
	}{Items: make([]collectionResponse, 0, len(list))}
	for _, c := range list {
		out.Items = append(out.Items, toCollectionResponse(c.ID, c.GroupID, c.Period, c.Version, c.Currency, c.CreatedBy, c.CreatedAt))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func lineBody(l domain.CollectLine, groupCurrency string) collectLineBody {
	return collectLineBody{Unit: l.UnitID, MainAccount: l.MainAccount, ConsolidationAccount: l.ConsolidationAccount, AccountType: l.Type,
		UnitCurrency: l.UnitCurrency, UnitAmount: l.UnitAmount.StringFixed(int32(money.MinorUnits(l.UnitCurrency))),
		Rate: l.Rate.String(), GroupAmount: l.GroupAmount.StringFixed(int32(money.MinorUnits(groupCurrency)))}
}

func (a *API) getCollection(w http.ResponseWriter, r *http.Request) {
	if !a.collectorReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", domain.CodeCollectionGone)
	if !ok {
		return
	}
	d, err := a.groups.GetCollection(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := collectionDetailResponse{collectionResponse: toCollectionResponse(d.ID, d.GroupID, d.Period, d.Version, d.Currency, d.CreatedBy, d.CreatedAt), Lines: make([]collectLineBody, 0, len(d.Lines))}
	for _, l := range d.Lines {
		out.Lines = append(out.Lines, lineBody(l, d.Currency))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// getTrialBalance returns the consolidated trial balance of a collection: one
// row per consolidation account with each unit's contribution (drill to
// entity), the accounts without a mapping, and each unit's translation
// difference, which the consolidation run books as CTA.
func (a *API) getTrialBalance(w http.ResponseWriter, r *http.Request) {
	if !a.collectorReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", domain.CodeCollectionGone)
	if !ok {
		return
	}
	d, err := a.groups.GetCollection(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	tb := domain.BuildTrialBalance(d.Lines)
	places := int32(money.MinorUnits(d.Currency))
	out := trialBalanceResponse{
		collectionResponse: toCollectionResponse(d.ID, d.GroupID, d.Period, d.Version, d.Currency, d.CreatedBy, d.CreatedAt),
		Rows:               make([]tbRowBody, 0, len(tb.Rows)), Unmapped: make([]collectLineBody, 0, len(tb.Unmapped)),
		Total: tb.Total.StringFixed(places), UnitDifferences: map[string]string{},
	}
	for _, row := range tb.Rows {
		units := make(map[string]string, len(row.Units))
		for u, v := range row.Units {
			units[u] = v.StringFixed(places)
		}
		out.Rows = append(out.Rows, tbRowBody{Account: row.Account, Type: row.Type, Total: row.Total.StringFixed(places), Units: units})
	}
	for _, l := range tb.Unmapped {
		out.Unmapped = append(out.Unmapped, lineBody(l, d.Currency))
	}
	for u, v := range tb.UnitDifference {
		out.UnitDifferences[u] = v.StringFixed(places)
	}
	httpx.JSON(w, http.StatusOK, out)
}
