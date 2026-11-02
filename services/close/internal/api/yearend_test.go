package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/close/internal/api"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

const runsPath = "/v1/close/year-end-runs"

type memoryRuns struct {
	runs      []*domain.YearEndRun
	periods   []domain.Period
	completed int
}

func (m *memoryRuns) YearPeriods(context.Context, string, string, string) ([]domain.Period, error) {
	return m.periods, nil
}

func (m *memoryRuns) latest() *domain.YearEndRun {
	if len(m.runs) == 0 {
		return nil
	}
	return m.runs[len(m.runs)-1]
}

func (m *memoryRuns) find(id string) *domain.YearEndRun {
	for _, r := range m.runs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (m *memoryRuns) BeginYearEnd(_ context.Context, in domain.YearEndRun) (domain.YearEndRun, bool, error) {
	latest := m.latest()
	switch {
	case latest == nil && in.SupersedesRunID == "":
		in.Sequence = 1
	case latest != nil && latest.Status == domain.RunPending && latest.RequestHash == in.RequestHash:
		return *latest, false, nil
	case latest != nil && in.SupersedesRunID == latest.ID && latest.Status == domain.RunCompleted:
		in.Sequence = latest.Sequence + 1
	case latest != nil && in.SupersedesRunID == "" && latest.RequestHash == in.RequestHash:
		return *latest, false, nil
	default:
		return domain.YearEndRun{}, false, &domain.Error{Code: domain.CodeIdempotencyReuse, Detail: "different request"}
	}
	in.ID = testkit.TenantID(50 + len(m.runs))
	in.Status = domain.RunPending
	in.Journals = map[string]string{}
	m.runs = append(m.runs, &in)
	return in, true, nil
}

func (m *memoryRuns) SetYearEndProposals(_ context.Context, id string, proposals domain.YearEnd) (domain.YearEndRun, error) {
	run := m.find(id)
	if run.Proposals == nil {
		run.Proposals = &proposals
	}
	return *run, nil
}

func (m *memoryRuns) SetYearEndJournal(_ context.Context, id, name, journalID string) (domain.YearEndRun, error) {
	run := m.find(id)
	run.Journals[name] = journalID
	return *run, nil
}

func (m *memoryRuns) CompleteYearEnd(_ context.Context, id string) (domain.YearEndRun, error) {
	run := m.find(id)
	if run.Status != domain.RunCompleted {
		m.completed++
	}
	run.Status = domain.RunCompleted
	return *run, nil
}

func (m *memoryRuns) YearEndRun(_ context.Context, id string) (domain.YearEndRun, error) {
	if run := m.find(id); run != nil {
		return *run, nil
	}
	return domain.YearEndRun{}, &domain.Error{Code: domain.CodeNoRun, Detail: "year-end run not found"}
}

type chartLedger struct {
	*ledgerclient.Fake
	info       ledgerclient.Info
	accounts   map[string]ledgerclient.ChartAccount
	cumulative []ledgerclient.BalanceRow
	thisYear   []ledgerclient.BalanceRow
	queries    []ledgerclient.TrialBalanceQuery
}

func (c *chartLedger) Ledger(context.Context, string, string) (ledgerclient.Info, error) {
	return c.info, nil
}

func (c *chartLedger) Accounts(context.Context, string, string) (map[string]ledgerclient.ChartAccount, error) {
	return c.accounts, nil
}

func (c *chartLedger) TrialBalance(_ context.Context, q ledgerclient.TrialBalanceQuery) (ledgerclient.TrialBalance, error) {
	c.queries = append(c.queries, q)
	if q.PeriodFrom != "" {
		return ledgerclient.TrialBalance{Currency: "USD", Rows: c.thisYear}, nil
	}
	return ledgerclient.TrialBalance{Currency: "USD", Rows: c.cumulative}, nil
}

func net(account string, amount int64, dims map[string]string) ledgerclient.BalanceRow {
	return ledgerclient.BalanceRow{Account: account, Dimensions: dims, Net: decimal.NewFromInt(amount)}
}

func closedYear() []domain.Period {
	var periods []domain.Period
	for month := time.January; month <= time.December; month++ {
		end := time.Date(2026, month+1, 0, 0, 0, 0, 0, time.UTC)
		periods = append(periods, domain.Period{
			FiscalPeriodID: testkit.TenantID(100 + int(month)), End: end,
			Rows: []domain.Row{{Module: domain.ModuleGL, Status: domain.StatusOnHold}, {Module: domain.ModuleAP, Status: domain.StatusClosed}},
		})
	}
	return periods
}

type yearEndFixture struct {
	runs   *memoryRuns
	ledger *chartLedger
	router http.Handler
}

func newYearEnd() yearEndFixture {
	f := yearEndFixture{
		runs: &memoryRuns{periods: closedYear()},
		ledger: &chartLedger{
			Fake: ledgerclient.NewFake(),
			info: ledgerclient.Info{LegalEntityID: testkit.TenantID(9), ChartCode: "COA", Currency: "USD", DimensionAttributes: []string{"CC"}},
			accounts: map[string]ledgerclient.ChartAccount{
				"1000": {Type: "asset", Postable: true}, "3000": {Type: "equity", Postable: true}, "3100": {Type: "equity", Postable: true},
				"3900": {Type: "equity", Postable: false}, "4000": {Type: "revenue", Postable: true}, "5000": {Type: "expense", Postable: true},
				"9000": {Type: "statistical", Postable: true},
			},
			cumulative: []ledgerclient.BalanceRow{net("1000", 100, nil), net("4000", -100, nil)},
			thisYear:   []ledgerclient.BalanceRow{net("4000", -100, nil)},
		},
	}
	a := api.New(fakeStore{}).WithLedgers(fakeLedger{owner: testkit.TenantID(9)}).WithYearEnd(f.runs, f.ledger)
	r := httpx.NewRouter()
	a.Register(r)
	f.router = r
	return f
}

func yearEndBody() string {
	return `{"ledgerId":"` + testkit.TenantID(7) + `","fiscalYear":"2026","retainedEarningsAccount":"3000"}`
}

func (f yearEndFixture) send(method, target, payload, duties string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(payload))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("X-Passage-Legal-Entity", testkit.TenantID(9))
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f yearEndFixture) run(payload string) *httptest.ResponseRecorder {
	return f.send(http.MethodPost, runsPath, payload, "close.yearend.run")
}

func lineAmounts(s ledgerclient.Stored) map[string]string {
	out := map[string]string{}
	for _, l := range s.Lines {
		side := "D"
		if l.Credit {
			side = "C"
		}
		out[l.Account] = side + l.Amount.String()
	}
	return out
}

func TestYearEnd_PostsLedgerDerivedClosingAndOpeningEntriesAndCompletesOnce(t *testing.T) {
	f := newYearEnd()
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var run domain.YearEndRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	posted := f.ledger.Entries()
	if len(posted) != 2 || run.Status != domain.RunCompleted || run.Journals[domain.JournalClosing] != posted[0].ID || run.Journals[domain.JournalOpening] != posted[1].ID {
		t.Fatalf("run %+v posted %d entries", run, len(posted))
	}
	closing, opening := posted[0], posted[1]
	if closing.PostingDate != "2026-12-31" || closing.SourceType != "year_end_closing" || closing.Currency != "USD" {
		t.Fatalf("closing entry %+v", closing.Entry)
	}
	if got := lineAmounts(closing); got["4000"] != "D100" || got["3000"] != "C100" || len(got) != 2 {
		t.Fatalf("closing lines %v", got)
	}
	if opening.PostingDate != "2027-01-01" || opening.SourceType != "year_end_opening" {
		t.Fatalf("opening entry %+v", opening.Entry)
	}
	if got := lineAmounts(opening); got["1000"] != "D100" || got["3000"] != "C100" || len(got) != 2 {
		t.Fatalf("opening lines %v", got)
	}
	if run.ClosingDate != "2026-12-31" || run.OpeningDate != "2027-01-01" {
		t.Fatalf("dates %s %s must come from the fiscal periods", run.ClosingDate, run.OpeningDate)
	}
	if rec.Header().Get("Location") != runsPath+"/"+run.ID {
		t.Fatalf("location %q", rec.Header().Get("Location"))
	}
	if len(f.ledger.queries) != 2 || f.ledger.queries[0].PeriodTo != "2026-12" || f.ledger.queries[1].PeriodFrom != "2026-01" || f.ledger.queries[0].LegalEntityID != testkit.TenantID(9) {
		t.Fatalf("balances must be read per period range from the ledger: %+v", f.ledger.queries)
	}
}

func TestYearEnd_RequestSuppliedAmountsAccountsAndDatesAreRejected(t *testing.T) {
	forged := map[string]string{
		"balances":     `"balances":[{"account":"1000","class":"bs","amount":"999999.00"},{"account":"4000","class":"pl","amount":"-999999.00"}]`,
		"closing date": `"closingDate":"2026-06-30"`,
		"opening date": `"openingDate":"2026-07-01"`,
		"currency":     `"currency":"EUR"`,
		"postings":     `"postings":[{"account":"1000","amount":"5.00"}]`,
	}
	for name, extra := range forged {
		t.Run(name, func(t *testing.T) {
			f := newYearEnd()
			payload := strings.TrimSuffix(yearEndBody(), "}") + "," + extra + "}"
			rec := f.run(payload)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
			if len(f.runs.runs) != 0 || len(f.ledger.Entries()) != 0 {
				t.Fatal("a request carrying caller-supplied figures must record nothing and post nothing")
			}
		})
	}
}

func TestYearEnd_PostedAmountsFollowTheLedgerNotTheRequest(t *testing.T) {
	f := newYearEnd()
	f.ledger.cumulative = []ledgerclient.BalanceRow{net("1000", 250, nil), net("4000", -250, nil)}
	f.ledger.thisYear = []ledgerclient.BalanceRow{net("4000", -250, nil)}
	if rec := f.run(yearEndBody()); rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	posted := f.ledger.Entries()
	if got := lineAmounts(posted[0]); got["3000"] != "C250" {
		t.Fatalf("closing lines %v must carry the ledger's 250", got)
	}
}

func TestYearEnd_DimensionsTravelOnTheLedgerLines(t *testing.T) {
	f := newYearEnd()
	dims := map[string]string{"CC": "A"}
	f.ledger.cumulative = []ledgerclient.BalanceRow{net("1000", 100, dims), net("4000", -100, dims)}
	f.ledger.thisYear = []ledgerclient.BalanceRow{net("4000", -100, dims)}
	rec := f.run(strings.Replace(yearEndBody(), `"3000"`, `"3000","retainDimensions":true`, 1))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	for _, s := range f.ledger.Entries() {
		for _, l := range s.Lines {
			if l.Dimensions["CC"] != "A" {
				t.Fatalf("line %+v lost its dimension", l)
			}
		}
	}
	if q := f.ledger.queries[0]; len(q.GroupBy) != 1 || q.GroupBy[0] != "CC" {
		t.Fatalf("balances must be grouped by the ledger's dimension attributes: %+v", q)
	}
}

func TestYearEnd_RepeatReturnsTheStoredRunWithoutPostingAgain(t *testing.T) {
	f := newYearEnd()
	f.run(yearEndBody())
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusOK || len(f.ledger.Entries()) != 2 || f.runs.completed != 1 {
		t.Fatalf("got %d, %d entries, %d completions", rec.Code, len(f.ledger.Entries()), f.runs.completed)
	}
}

func TestYearEnd_LedgerOutageLeavesARunThatTheRetryFinishesWithoutDuplicates(t *testing.T) {
	f := newYearEnd()
	f.ledger.FailPosts(errors.New("ledger down"))
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusServiceUnavailable || f.runs.latest().Status != domain.RunPending || f.runs.completed != 0 {
		t.Fatalf("got %d %s, run %+v", rec.Code, rec.Body, f.runs.latest())
	}
	f.ledger.FailPosts(nil)
	rec = f.run(yearEndBody())
	if rec.Code != http.StatusOK || len(f.ledger.Entries()) != 2 || f.runs.latest().Status != domain.RunCompleted {
		t.Fatalf("retry got %d %s with %d entries", rec.Code, rec.Body, len(f.ledger.Entries()))
	}
}

func TestYearEnd_LedgerRejectionIsReportedAsUnprocessable(t *testing.T) {
	f := newYearEnd()
	f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"serviceCode":"CLOSE-0008"`) || !strings.Contains(rec.Body.String(), ledgerclient.CodePeriodClosed) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if f.runs.latest().Status != domain.RunPending {
		t.Fatalf("a rejected posting must leave the run pending, got %s", f.runs.latest().Status)
	}
}

func TestYearEnd_PostingStillInProgressAsksForARetry(t *testing.T) {
	f := newYearEnd()
	f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusConflict, Code: ledgerclient.CodeInProgress})
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusConflict || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"serviceCode":"CLOSE-0002"`) {
		t.Fatalf("got %d %s retry-after %q", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
	}
}

func TestYearEnd_SameYearWithADifferentRequestConflicts(t *testing.T) {
	f := newYearEnd()
	f.run(yearEndBody())
	rec := f.run(strings.Replace(yearEndBody(), `"3000"`, `"3100"`, 1))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), domain.CodeIdempotencyReuse) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestYearEnd_SupersedesReversesThePriorEntriesAndPostsAgainFromTheLedger(t *testing.T) {
	f := newYearEnd()
	first := f.run(yearEndBody())
	var prior domain.YearEndRun
	if err := json.Unmarshal(first.Body.Bytes(), &prior); err != nil {
		t.Fatal(err)
	}
	f.ledger.cumulative = []ledgerclient.BalanceRow{net("1000", 180, nil), net("4000", -180, nil)}
	f.ledger.thisYear = []ledgerclient.BalanceRow{net("4000", -180, nil)}

	rec := f.run(strings.TrimSuffix(yearEndBody(), "}") + `,"supersedesRunId":"` + prior.ID + `"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var rerun domain.YearEndRun
	if err := json.Unmarshal(rec.Body.Bytes(), &rerun); err != nil {
		t.Fatal(err)
	}
	if rerun.Sequence != 2 || rerun.SupersedesRunID != prior.ID || rerun.Status != domain.RunCompleted {
		t.Fatalf("rerun %+v", rerun)
	}
	posted := f.ledger.Entries()
	if len(posted) != 6 {
		t.Fatalf("want 2 originals, 2 reversals and 2 new entries, got %d", len(posted))
	}
	reversed := map[string]bool{}
	for _, s := range posted {
		if s.ReversalOf != "" {
			reversed[s.ReversalOf] = true
		}
	}
	if !reversed[prior.Journals[domain.JournalClosing]] || !reversed[prior.Journals[domain.JournalOpening]] {
		t.Fatalf("both prior entries must be reversed, got %v", reversed)
	}
	if rerun.Journals[domain.JournalReverseClosing] == "" || rerun.Journals[domain.JournalReverseOpening] == "" {
		t.Fatalf("rerun must record its reversals: %+v", rerun.Journals)
	}
	if got := lineAmounts(posted[4]); got["3000"] != "C180" {
		t.Fatalf("the re-run must post the ledger's current 180, got %v", got)
	}
	again := f.run(strings.TrimSuffix(yearEndBody(), "}") + `,"supersedesRunId":"` + prior.ID + `"}`)
	if again.Code == http.StatusCreated || len(f.ledger.Entries()) != 6 {
		t.Fatalf("repeating the re-run got %d with %d entries", again.Code, len(f.ledger.Entries()))
	}
}

func TestYearEnd_RerunWithoutNamingTheCompletedRunConflicts(t *testing.T) {
	f := newYearEnd()
	f.run(yearEndBody())
	f.ledger.cumulative = []ledgerclient.BalanceRow{net("1000", 180, nil), net("4000", -180, nil)}
	rec := f.run(strings.Replace(yearEndBody(), `"3000"`, `"3100"`, 1))
	if rec.Code != http.StatusConflict || len(f.ledger.Entries()) != 2 {
		t.Fatalf("got %d with %d entries", rec.Code, len(f.ledger.Entries()))
	}
}

func TestYearEnd_PreconditionsRefuseBeforeAnythingIsRecorded(t *testing.T) {
	foreign := newYearEnd()
	foreign.ledger.info.LegalEntityID = testkit.TenantID(10)

	open := newYearEnd()
	open.runs.periods[11].Rows = append(open.runs.periods[11].Rows, domain.Row{Module: domain.ModuleAR, Status: domain.StatusOpen})

	none := newYearEnd()
	none.runs.periods = nil

	notEquity := newYearEnd()
	notPostable := newYearEnd()
	statistical := newYearEnd()
	unknownAccount := newYearEnd()
	unknownAccount.ledger.cumulative = append(unknownAccount.ledger.cumulative, net("7777", 5, nil))
	unclassifiable := newYearEnd()
	unclassifiable.ledger.cumulative = append(unclassifiable.ledger.cumulative, net("9000", 5, nil))

	tests := []struct {
		name     string
		f        yearEndFixture
		payload  string
		want     int
		wantBody string
	}{
		{"ledger of another legal entity", foreign, yearEndBody(), http.StatusForbidden, "AUTH-0003"},
		{"period still open", open, yearEndBody(), http.StatusConflict, domain.CodeInvalidTransition},
		{"no periods registered", none, yearEndBody(), http.StatusNotFound, domain.CodeNoPeriod},
		{"retained earnings is not equity", notEquity, strings.Replace(yearEndBody(), `"3000"`, `"1000"`, 1), http.StatusBadRequest, "postable equity"},
		{"retained earnings is not postable", notPostable, strings.Replace(yearEndBody(), `"3000"`, `"3900"`, 1), http.StatusBadRequest, "postable equity"},
		{"retained earnings not in the chart", statistical, strings.Replace(yearEndBody(), `"3000"`, `"3999"`, 1), http.StatusBadRequest, "postable equity"},
		{"balance on an account outside the chart", unknownAccount, yearEndBody(), http.StatusBadRequest, domain.CodeLedgerData},
		{"balance on an account that cannot be closed", unclassifiable, yearEndBody(), http.StatusBadRequest, domain.CodeLedgerData},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.f.run(tc.payload)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s, want %d containing %q", rec.Code, rec.Body, tc.want, tc.wantBody)
			}
			if len(tc.f.ledger.Entries()) != 0 {
				t.Fatal("a refused request must not post")
			}
		})
	}
}

func TestYearEnd_Validation(t *testing.T) {
	tests := []struct {
		name, payload, duties string
		want                  int
		wantBody              string
	}{
		{"missing duty", yearEndBody(), "close.yearend.read", http.StatusForbidden, "AUTH-0003"},
		{"bad fiscal year", strings.Replace(yearEndBody(), `"2026"`, `"FY2026"`, 1), "close.yearend.run", http.StatusBadRequest, `"fiscalYear"`},
		{"bad ledger", strings.Replace(yearEndBody(), testkit.TenantID(7), "nope", 1), "close.yearend.run", http.StatusBadRequest, `"ledgerId"`},
		{"missing retained earnings", strings.Replace(yearEndBody(), `"3000"`, `""`, 1), "close.yearend.run", http.StatusBadRequest, `"retainedEarningsAccount"`},
		{"bad supersedes", strings.TrimSuffix(yearEndBody(), "}") + `,"supersedesRunId":"x"}`, "close.yearend.run", http.StatusBadRequest, `"supersedesRunId"`},
		{"unknown field", `{"x":1}`, "close.yearend.run", http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newYearEnd()
			rec := f.send(http.MethodPost, runsPath, tc.payload, tc.duties)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s, want %d containing %q", rec.Code, rec.Body, tc.want, tc.wantBody)
			}
			if len(f.ledger.Entries()) != 0 || len(f.runs.runs) != 0 {
				t.Fatal("a refused request must not record a run or post")
			}
		})
	}
}

func TestYearEnd_NotConfiguredIsUnavailable(t *testing.T) {
	r := httpx.NewRouter()
	api.New(fakeStore{}).Register(r)
	f := yearEndFixture{router: r}
	for _, rec := range []*httptest.ResponseRecorder{
		f.run(yearEndBody()),
		f.send(http.MethodGet, runsPath+"/"+testkit.TenantID(50), "", "close.yearend.read"),
	} {
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
	}
}

func TestYearEnd_GetReturnsTheRunOr404(t *testing.T) {
	f := newYearEnd()
	missing := f.send(http.MethodGet, runsPath+"/"+testkit.TenantID(50), "", "close.yearend.read")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "CLOSE-0007") {
		t.Fatalf("got %d %s", missing.Code, missing.Body)
	}
	if bad := f.send(http.MethodGet, runsPath+"/nope", "", "close.yearend.read"); bad.Code != http.StatusNotFound {
		t.Fatalf("malformed id got %d", bad.Code)
	}
	f.run(yearEndBody())
	found := f.send(http.MethodGet, runsPath+"/"+testkit.TenantID(50), "", "close.yearend.read")
	if found.Code != http.StatusOK || !strings.Contains(found.Body.String(), `"status":"completed"`) {
		t.Fatalf("got %d %s", found.Code, found.Body)
	}
	if denied := f.send(http.MethodGet, runsPath+"/"+testkit.TenantID(50), "", "close.yearend.run"); denied.Code != http.StatusForbidden {
		t.Fatalf("run duty must not read, got %d", denied.Code)
	}
}
