package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

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
	CreateRun(ctx context.Context, in domain.RunInput, res domain.RunResult, key string) (store.RunRecord, bool, error)
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
}

type runResponse struct {
	ID               string    `json:"id"`
	GroupID          string    `json:"groupId"`
	Period           string    `json:"period"`
	Currency         string    `json:"currency"`
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
		ID: rec.ID, GroupID: rec.GroupID, Period: rec.Period, Currency: rec.Currency, Version: rec.Version, Status: rec.Status,
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
	rec, replayed, err := a.runs.CreateRun(r.Context(), in, res, key)
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
