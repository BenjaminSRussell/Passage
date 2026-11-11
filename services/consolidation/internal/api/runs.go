package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/store"
)

// RunStore persists consolidation runs. internal/store implements it over
// Postgres with row-level security and the outbox.
type RunStore interface {
	CreateRun(ctx context.Context, in domain.RunInput, res domain.RunResult, collectionID, key string) (store.RunRecord, bool, error)
	ListRuns(ctx context.Context, f store.RunFilter, after string, limit int) (store.RunPage, error)
	GetRun(ctx context.Context, id string) (store.RunDetail, error)
	GetStatements(ctx context.Context, id string) (store.RunStatements, error)
}

const (
	maxIdempotencyKey = 255
	defaultRunPage    = 50
	maxRunPage        = 200
)

type runUnitBody struct {
	ID string `json:"id"`
	eliminationRequest
	OpeningNetAssets string `json:"openingNetAssets"`
	ClosingNetAssets string `json:"closingNetAssets"`
}

type startRunBody struct {
	GroupID  string        `json:"groupId"`
	Period   string        `json:"period"`
	Currency string        `json:"currency"`
	Units    []runUnitBody `json:"units"`
	// CollectionID derives the units of the run from a stored collection of the group's balances instead of taking them
	// from the request; Units must then be empty. IC supplies the pair amounts a trial balance cannot attribute to one partner.
	CollectionID string          `json:"collectionId"`
	IC           []icOverrideRow `json:"ic"`
	// OpeningRates gives the opening rate of a currency when no collection of the unit's previous fiscal year end holds it.
	OpeningRates []openingRateRow `json:"openingRates"`
}

type icOverrideRow struct {
	Unit         string `json:"unit"`
	ICSales      string `json:"icSales"`
	ICCost       string `json:"icCost"`
	UnsoldShare  string `json:"unsoldShare"`
	ICReceivable string `json:"icReceivable"`
	PayableGroup string `json:"payableGroup"`
}

type openingRateRow struct {
	Currency string `json:"currency"`
	Rate     string `json:"rate"`
}

type runResponse struct {
	ID               string    `json:"id"`
	GroupID          string    `json:"groupId"`
	Period           string    `json:"period"`
	Currency         string    `json:"currency"`
	CollectionID     string    `json:"collectionId,omitempty"`
	Version          int       `json:"version"`
	Status           string    `json:"status"`
	InputsHash       string    `json:"inputsHash"`
	CreatedBy        string    `json:"createdBy"`
	UnitCount        int       `json:"unitCount"`
	EliminationCount int       `json:"eliminationCount"`
	CTAAmount        string    `json:"ctaAmount"`
	CreatedAt        time.Time `json:"createdAt"`
}

type balanceBody struct {
	Unit    string `json:"unit"`
	Account string `json:"account"`
	Amount  string `json:"amount"`
}

type unitEntryBody struct {
	Unit    string `json:"unit"`
	Rule    string `json:"rule"`
	Account string `json:"account"`
	Debit   string `json:"debit"`
	Credit  string `json:"credit"`
}

type runDetailResponse struct {
	runResponse
	Balances     []balanceBody   `json:"balances"`
	Eliminations []unitEntryBody `json:"eliminations"`
}

type statementLineBody struct {
	Statement string `json:"statement"`
	Line      string `json:"line"`
	Amount    string `json:"amount"`
}

type runStatementsResponse struct {
	runResponse
	Statements []statementLineBody `json:"statements"`
}

type runListResponse struct {
	Items      []runResponse `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

func toRunResponse(rec store.RunRecord) runResponse {
	return runResponse{
		ID: rec.ID, GroupID: rec.GroupID, Period: rec.Period, Currency: rec.Currency, CollectionID: rec.CollectionID, Version: rec.Version, Status: rec.Status,
		InputsHash: rec.InputsHash, CreatedBy: rec.CreatedBy, UnitCount: rec.UnitCount, EliminationCount: rec.EliminationCount,
		CTAAmount: rec.CTA.StringFixed(money.MinorUnits(rec.Currency)), CreatedAt: rec.CreatedAt.UTC(),
	}
}

// allEntityScope admits only callers that see every legal entity: no
// legal-entity claim and either the all-entity duty (reads) or tenant.administer
// (the write path drops the all-entity duty). A run spans a group's entities,
// and group membership is not stored yet, so a legal-entity-scoped caller
// would otherwise read or create consolidated figures of entities it cannot see.
func allEntityScope(w http.ResponseWriter, r *http.Request) bool {
	p, _ := tenant.FromContext(r.Context())
	if p.LegalEntityID == "" && (dbx.LegalEntityScopes(p) == "*" || tenant.IsTenantAdmin(p)) {
		return true
	}
	httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, domain.CodeRunScope, "consolidation runs need all-entity scope: a caller scoped to legal entities cannot use them until group membership is stored"))
	return false
}

func runID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeRunNotFound, "no consolidation run with id "+id))
		return "", false
	}
	return id, true
}

// startRun computes the run with the existing rules and stores it as the next
// version of its group, period and currency.
func (a *API) startRun(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) || !allEntityScope(w, r) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > maxIdempotencyKey {
		validation(w, r, map[string]string{"Idempotency-Key": "header is required, at most 255 characters"})
		return
	}
	var body startRunBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(body.Units) > domain.MaxRunUnits {
		validation(w, r, map[string]string{"units": fmt.Sprintf("must contain at most %d units", domain.MaxRunUnits)})
		return
	}
	if body.CollectionID != "" {
		a.startRunFromCollection(w, r, body, key)
		return
	}
	fields := map[string]string{}
	in := domain.RunInput{GroupID: body.GroupID, Period: body.Period, Currency: body.Currency}
	if !currencyCode.MatchString(body.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	for i, u := range body.Units {
		at := func(name string) string { return fmt.Sprintf("units[%d].%s", i, name) }
		if u.Currency == "" {
			u.Currency = body.Currency
		}
		pair, pf := toPair(u.eliminationRequest)
		for k, v := range pf {
			fields[at(k)] = v
		}
		f := decimals{}
		unit := domain.RunUnit{
			ID: u.ID, Pair: pair,
			OpeningNetAssets: f.get("openingNetAssets", u.OpeningNetAssets, true),
			ClosingNetAssets: f.get("closingNetAssets", u.ClosingNetAssets, true),
		}
		if u.Rates.Opening == "" {
			f["rates.opening"] = "is required"
		}
		for k, v := range f {
			fields[at(k)] = v
		}
		in.Units = append(in.Units, unit)
	}
	if !validation(w, r, fields) {
		return
	}
	res, err := domain.ComputeRun(in)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	rec, replayed, err := a.runs.CreateRun(r.Context(), in, res, "", key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.runsCompleted.Add(1)
	}
	w.Header().Set("Location", "/v1/consol/runs/"+rec.ID)
	httpx.JSON(w, http.StatusCreated, toRunResponse(rec))
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) || !allEntityScope(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	filter := store.RunFilter{GroupID: strings.TrimSpace(q.Get("groupId")), Period: q.Get("period")}
	if filter.Period != "" && !periodText.MatchString(filter.Period) {
		fields["period"] = "must be YYYY-MM"
	}
	after := q.Get("after")
	if after != "" && !ids.Valid(after) {
		fields["after"] = "must be the nextCursor of the previous page"
	}
	limit := defaultRunPage
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxRunPage {
			fields["limit"] = fmt.Sprintf("must be a whole number from 1 to %d", maxRunPage)
		}
		limit = n
	}
	if !validation(w, r, fields) {
		return
	}
	page, err := a.runs.ListRuns(r.Context(), filter, after, limit)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := runListResponse{Items: make([]runResponse, 0, len(page.Items)), NextCursor: page.Next}
	for _, rec := range page.Items {
		out.Items = append(out.Items, toRunResponse(rec))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) || !allEntityScope(w, r) {
		return
	}
	id, ok := runID(w, r)
	if !ok {
		return
	}
	d, err := a.runs.GetRun(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	places := money.MinorUnits(d.Currency)
	out := runDetailResponse{runResponse: toRunResponse(d.RunRecord), Balances: make([]balanceBody, 0, len(d.Balances)), Eliminations: make([]unitEntryBody, 0, len(d.Entries))}
	for _, b := range d.Balances {
		out.Balances = append(out.Balances, balanceBody{Unit: b.UnitID, Account: b.Account, Amount: b.Amount.StringFixed(places)})
	}
	for _, e := range d.Entries {
		out.Eliminations = append(out.Eliminations, unitEntryBody{Unit: e.UnitID, Rule: e.RuleCode, Account: e.Account, Debit: e.Debit.StringFixed(places), Credit: e.Credit.StringFixed(places)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getRunStatements(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) || !allEntityScope(w, r) {
		return
	}
	id, ok := runID(w, r)
	if !ok {
		return
	}
	st, err := a.runs.GetStatements(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	places := money.MinorUnits(st.Currency)
	out := runStatementsResponse{runResponse: toRunResponse(st.RunRecord), Statements: make([]statementLineBody, 0, len(st.Lines))}
	for _, l := range st.Lines {
		out.Statements = append(out.Statements, statementLineBody{Statement: l.Statement, Line: l.Code, Amount: l.Amount.StringFixed(places)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

// startRunFromCollection derives the run's units from a stored collection (FR-40.10) and stores the run as the next version of
// its group, period and currency, recording the collection it came from.
func (a *API) startRunFromCollection(w http.ResponseWriter, r *http.Request, body startRunBody, key string) {
	if a.groups == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "group storage is not configured"))
		return
	}
	fields := map[string]string{}
	if !ids.Valid(body.CollectionID) {
		fields["collectionId"] = "must be a collection id"
	}
	if len(body.Units) > 0 {
		fields["units"] = "must be empty when the run is derived from a collection"
	}
	f := decimals{}
	ic := map[string]domain.ICOverride{}
	for i, o := range body.IC {
		at := func(name string) string { return fmt.Sprintf("ic[%d].%s", i, name) }
		if !ids.Valid(o.Unit) {
			fields[at("unit")] = "must be a unit id of the group"
			continue
		}
		var ov domain.ICOverride
		opt := func(name, raw string, into **decimal.Decimal) {
			if raw == "" {
				return
			}
			d := f.get(at(name), raw, true)
			*into = &d
		}
		opt("icSales", o.ICSales, &ov.ICSales)
		opt("icCost", o.ICCost, &ov.ICCost)
		opt("unsoldShare", o.UnsoldShare, &ov.UnsoldShare)
		opt("icReceivable", o.ICReceivable, &ov.ICReceivable)
		opt("payableGroup", o.PayableGroup, &ov.PayableGroup)
		ic[o.Unit] = ov
	}
	openingRates := map[string]decimal.Decimal{}
	for i, o := range body.OpeningRates {
		if !currencyCode.MatchString(o.Currency) {
			fields[fmt.Sprintf("openingRates[%d].currency", i)] = "must be an ISO 4217 code"
			continue
		}
		openingRates[o.Currency] = f.get(fmt.Sprintf("openingRates[%d].rate", i), o.Rate, true)
	}
	for k, v := range f {
		fields[k] = v
	}
	if !validation(w, r, fields) {
		return
	}
	c, err := a.groups.GetCollection(r.Context(), body.CollectionID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if body.GroupID != "" && body.GroupID != c.GroupID || body.Period != "" && body.Period != c.Period || body.Currency != "" && body.Currency != c.Currency {
		validation(w, r, map[string]string{"collectionId": "belongs to group " + c.GroupID + ", period " + c.Period + " in " + c.Currency + "; leave groupId, period and currency out or make them match"})
		return
	}
	group, err := a.groups.GetGroup(r.Context(), c.GroupID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	terms, err := a.groups.GetUnitTerms(r.Context(), c.GroupID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	in := domain.DeriveInput{GroupCurrency: c.Currency, Period: c.Period, Units: group.Units, Lines: c.Lines, Rates: c.Rates,
		Terms: map[string]domain.UnitTerms{}, Opening: map[string]domain.OpeningBasis{}, OpeningRates: openingRates, IC: ic}
	for id, t := range terms {
		in.Terms[id] = t.UnitTerms
	}
	// Each unit's opening comes from the collection at the end of its previous fiscal year, when the group has one.
	byPeriod := map[string]*store.CollectionDetail{}
	for _, u := range group.Units {
		end, err := a.previousYearEnd(r.Context(), u, c.Period)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		prev, seen := byPeriod[end]
		if !seen {
			got, ok, err := a.groups.LatestCollection(r.Context(), c.GroupID, end)
			if err != nil {
				httpx.Fail(w, r, problemFrom(err))
				return
			}
			if ok {
				prev = &got
			}
			byPeriod[end] = prev
		}
		if prev == nil {
			continue
		}
		var lines []domain.CollectLine
		for _, l := range prev.Lines {
			if l.UnitID == u.ID {
				lines = append(lines, l)
			}
		}
		if closing, _, ok := domain.NetAssets(lines); ok {
			basis := domain.OpeningBasis{NetAssets: closing}
			if rate, ok := prev.Rates[u.Currency]; ok {
				basis.Rate = rate.Closing
			}
			in.Opening[u.ID] = basis
		}
	}
	units, err := domain.RunUnits(in)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	run := domain.RunInput{GroupID: c.GroupID, Period: c.Period, Currency: c.Currency, Units: units}
	res, err := domain.ComputeRun(run)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	rec, replayed, err := a.runs.CreateRun(r.Context(), run, res, c.ID, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.runsCompleted.Add(1)
	}
	w.Header().Set("Location", "/v1/consol/runs/"+rec.ID)
	httpx.JSON(w, http.StatusCreated, toRunResponse(rec))
}
