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

func (m *memoryRuns) latestLive() *domain.YearEndRun {
	var live *domain.YearEndRun
	for _, r := range m.runs {
		if r.Status != domain.RunAbandoned {
			live = r
		}
	}
	return live
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
	reuse := &domain.Error{Code: domain.CodeIdempotencyReuse, Detail: "different request"}
	latest := m.latestLive()
	switch {
	case latest == nil:
		if in.SupersedesRunID != "" {
			return domain.YearEndRun{}, false, reuse
		}
	case latest.Status == domain.RunPending:
		if latest.RequestHash != in.RequestHash || latest.SupersedesRunID != in.SupersedesRunID {
			return domain.YearEndRun{}, false, reuse
		}
		return *latest, false, nil
	case in.SupersedesRunID == latest.ID:
	case in.SupersedesRunID == latest.SupersedesRunID && latest.RequestHash == in.RequestHash:
		return *latest, false, nil
	default:
		return domain.YearEndRun{}, false, reuse
	}
	in.Sequence = len(m.runs) + 1
	in.ID = testkit.TenantID(50 + len(m.runs))
	in.Status = domain.RunPending
	in.Journals = map[string]string{}
	m.runs = append(m.runs, &in)
	return in, true, nil
}

func (m *memoryRuns) DispatchYearEnd(_ context.Context, id string, proposals *domain.YearEnd) (domain.YearEndRun, error) {
	run := m.find(id)
	run.Dispatched = true
	if run.Proposals == nil {
		run.Proposals = proposals
	}
	return *run, nil
}

func (m *memoryRuns) CompletedYearEnd(_ context.Context, _, _, _, requestHash, supersedesRunID string) (domain.YearEndRun, bool, error) {
	latest := m.latestLive()
	if latest == nil {
		return domain.YearEndRun{}, false, nil
	}
	return *latest, latest.Status == domain.RunCompleted && latest.RequestHash == requestHash && latest.SupersedesRunID == supersedesRunID, nil
}

func (m *memoryRuns) SetYearEndJournal(_ context.Context, id, name, journalID string) (domain.YearEndRun, error) {
	run := m.find(id)
	run.Journals[name] = journalID
	return *run, nil
}

func (m *memoryRuns) AbandonYearEnd(_ context.Context, id string) (domain.YearEndRun, error) {
	run := m.find(id)
	if run.Status == domain.RunPending && len(run.Journals) == 0 {
		run.Status = domain.RunAbandoned
	}
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
	info          ledgerclient.Info
	accounts      map[string]ledgerclient.ChartAccount
	cumulative    []ledgerclient.BalanceRow
	thisYear      []ledgerclient.BalanceRow
	queries       []ledgerclient.TrialBalanceQuery
	chartReads    int
	balanceErr    error
	balanceCurren string
}

func (c *chartLedger) Ledger(context.Context, string, string) (ledgerclient.Info, error) {
	return c.info, nil
}

func (c *chartLedger) Accounts(context.Context, string, string) (map[string]ledgerclient.ChartAccount, error) {
	c.chartReads++
	return c.accounts, nil
}

func (c *chartLedger) TrialBalance(_ context.Context, q ledgerclient.TrialBalanceQuery) (ledgerclient.TrialBalance, error) {
	c.queries = append(c.queries, q)
	if c.balanceErr != nil {
		return ledgerclient.TrialBalance{}, c.balanceErr
	}
	currency := "USD"
	if c.balanceCurren != "" {
		currency = c.balanceCurren
	}
	if q.PeriodFrom != "" {
		return ledgerclient.TrialBalance{Currency: currency, Rows: c.thisYear}, nil
	}
	return ledgerclient.TrialBalance{Currency: currency, Rows: c.cumulative}, nil
}

type cumulativeLedger struct{ *chartLedger }

func (c cumulativeLedger) TrialBalance(_ context.Context, q ledgerclient.TrialBalanceQuery) (ledgerclient.TrialBalance, error) {
	type cell struct {
		account string
		dims    string
	}
	sums := map[cell]ledgerclient.BalanceRow{}
	for _, e := range c.Entries() {
		period := e.PostingDate[:len("2006-01")]
		if e.LedgerID != q.LedgerID || (q.PeriodTo != "" && period > q.PeriodTo) || (q.PeriodFrom != "" && period < q.PeriodFrom) {
			continue
		}
		for _, l := range e.Lines {
			dims := map[string]string{}
			for _, attribute := range q.GroupBy {
				if v := l.Dimensions[attribute]; v != "" {
					dims[attribute] = v
				}
			}
			key := cell{l.Account, domain.DimensionKey(dims)}
			row := sums[key]
			row.Account, row.Dimensions = l.Account, dims
			if l.Credit {
				row.Net = row.Net.Sub(l.Amount)
			} else {
				row.Net = row.Net.Add(l.Amount)
			}
			sums[key] = row
		}
	}
	out := ledgerclient.TrialBalance{Currency: "USD"}
	for _, row := range sums {
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

func net(account string, amount int64, dims map[string]string) ledgerclient.BalanceRow {
	return ledgerclient.BalanceRow{Account: account, Dimensions: dims, Net: decimal.NewFromInt(amount)}
}

func closedYear() []domain.Period {
	var periods []domain.Period
	for month := time.January; month <= time.December; month++ {
		periods = append(periods, domain.Period{
			FiscalPeriodID: testkit.TenantID(100 + int(month)),
			Start:          time.Date(2026, month, 1, 0, 0, 0, 0, time.UTC),
			End:            time.Date(2026, month+1, 0, 0, 0, 0, 0, time.UTC),
			Rows:           []domain.Row{{Module: domain.ModuleGL, Status: domain.StatusOnHold}, {Module: domain.ModuleAP, Status: domain.StatusClosed}, {Module: domain.ModuleAll, Status: domain.StatusClosed}},
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
	return newYearEndOn(func(l *chartLedger) api.YearEndLedger { return l })
}

func newYearEndOn(wrap func(*chartLedger) api.YearEndLedger) yearEndFixture {
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
	a := api.New(fakeStore{}).WithLedgers(fakeLedger{owner: testkit.TenantID(9)}).WithYearEnd(f.runs, wrap(f.ledger))
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

func (f yearEndFixture) supersede(priorID string) *httptest.ResponseRecorder {
	return f.run(strings.TrimSuffix(yearEndBody(), "}") + `,"supersedesRunId":"` + priorID + `"}`)
}

func decodeRun(t *testing.T, rec *httptest.ResponseRecorder) domain.YearEndRun {
	t.Helper()
	var run domain.YearEndRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	return run
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

func proposalAmounts(entries []domain.Entry) map[string]string {
	out := map[string]string{}
	for _, e := range entries {
		out[e.Account] = e.Amount.String()
	}
	return out
}

func TestYearEnd_PostsOnlyTheLedgerDerivedClosingEntryAndKeepsTheOpeningProposalUnposted(t *testing.T) {
	f := newYearEnd()
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	run := decodeRun(t, rec)
	posted := f.ledger.Entries()
	if len(posted) != 1 || run.Status != domain.RunCompleted || run.Journals[domain.JournalClosing] != posted[0].ID || len(run.Journals) != 1 {
		t.Fatalf("run %+v posted %d entries", run, len(posted))
	}
	closing := posted[0]
	if closing.PostingDate != "2026-12-31" || closing.SourceType != "year_end_closing" || closing.Currency != "USD" || !closing.PostClosed {
		t.Fatalf("closing entry %+v", closing.Entry)
	}
	if got := lineAmounts(closing); got["4000"] != "D100" || got["3000"] != "C100" || len(got) != 2 {
		t.Fatalf("closing lines %v", got)
	}
	if got := proposalAmounts(run.Proposals.Opening); got["1000"] != "100" || got["3000"] != "-100" || len(got) != 2 {
		t.Fatalf("the opening proposal is kept for verification: %v", got)
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
	if f.ledger.chartReads != 1 {
		t.Fatalf("the chart must be read once per request, got %d", f.ledger.chartReads)
	}
}

func seed(t *testing.T, l *ledgerclient.Fake, key, date string, debit, credit string, amount int64, dim string) {
	t.Helper()
	dims := map[string]string{"CC": dim}
	value := decimal.NewFromInt(amount)
	if _, err := l.Post(context.Background(), ledgerclient.Entry{
		LegalEntityID: testkit.TenantID(9), LedgerID: testkit.TenantID(7), DocumentID: key, PostingDate: date, Currency: "USD", IdempotencyKey: key, SourceType: "seed",
		Lines: []ledgerclient.Line{{Account: debit, Amount: value, Dimensions: dims}, {Account: credit, Credit: true, Amount: value, Dimensions: dims}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestYearEnd_RunLeavesBalanceSheetDimensionsUntouchedAndZeroesProfitAndLossOnACumulativeLedger(t *testing.T) {
	var ledger cumulativeLedger
	f := newYearEndOn(func(c *chartLedger) api.YearEndLedger {
		ledger = cumulativeLedger{c}
		return ledger
	})
	seed(t, f.ledger.Fake, "prior-capital", "2025-06-30", "1000", "3100", 500, "A")
	seed(t, f.ledger.Fake, "sale", "2026-03-15", "1000", "4000", 100, "A")
	seed(t, f.ledger.Fake, "cost", "2026-09-10", "5000", "1000", 30, "B")
	query := ledgerclient.TrialBalanceQuery{LegalEntityID: testkit.TenantID(9), LedgerID: testkit.TenantID(7), PeriodTo: "2026-12", GroupBy: []string{"CC"}}
	balances := func(from string) map[string]decimal.Decimal {
		t.Helper()
		q := query
		q.PeriodFrom = from
		tb, err := ledger.TrialBalance(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]decimal.Decimal{}
		for _, row := range tb.Rows {
			out[row.Account+"|"+row.Dimensions["CC"]] = row.Net
		}
		return out
	}
	before := balances("")

	rec := f.run(strings.Replace(yearEndBody(), `"3000"`, `"3000","retainDimensions":true`, 1))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if len(f.ledger.Entries()) != 4 {
		t.Fatalf("three seed postings and exactly one closing entry expected, got %d", len(f.ledger.Entries()))
	}
	after := balances("")
	for _, key := range []string{"1000|A", "1000|B", "3100|A"} {
		if !after[key].Equal(before[key]) {
			t.Errorf("%s moved from %s to %s: a closing run must not change balance sheet accounts other than retained earnings", key, before[key], after[key])
		}
	}
	if !after["3000|A"].Equal(decimal.NewFromInt(-100)) || !after["3000|B"].Equal(decimal.NewFromInt(30)) {
		t.Errorf("retained earnings per dimension after the run: A %s, B %s", after["3000|A"], after["3000|B"])
	}
	for _, key := range []string{"4000|A", "5000|B"} {
		if !after[key].IsZero() {
			t.Errorf("%s must be zero as of the closing date, got %s", key, after[key])
		}
	}
	if thisYear := balances("2026-01"); !thisYear["4000|A"].IsZero() || !thisYear["5000|B"].IsZero() {
		t.Errorf("profit and loss of the year must net to zero, got %v", thisYear)
	}
	total := decimal.Zero
	for _, v := range after {
		total = total.Add(v)
	}
	if !total.IsZero() {
		t.Errorf("the ledger no longer balances: %s", total)
	}
	for _, e := range f.ledger.Entries() {
		if e.PostingDate > "2026-12-31" {
			t.Errorf("nothing may be posted after the year: %+v", e.Entry)
		}
	}
}

func TestYearEnd_PostsUnderThePreconditionPlanYearEnforcesOnALedgerThatRefusesClosedPeriods(t *testing.T) {
	f := newYearEnd()
	f.ledger.RefuseClosedThrough("2026-12-31")
	control := ledgerclient.Entry{
		LegalEntityID: testkit.TenantID(9), LedgerID: testkit.TenantID(7), DocumentID: "d", PostingDate: "2026-12-31", Currency: "USD", IdempotencyKey: "control",
		Lines: []ledgerclient.Line{{Account: "1000", Amount: decimal.NewFromInt(1)}, {Account: "3000", Credit: true, Amount: decimal.NewFromInt(1)}},
	}
	if _, err := f.ledger.Post(context.Background(), control); ledgerclient.CodeOf(err) != ledgerclient.CodePeriodClosed {
		t.Fatalf("the fake must refuse a closed period without the duty, got %v", err)
	}
	first := f.run(yearEndBody())
	if first.Code != http.StatusCreated {
		t.Fatalf("closing into the closed last period got %d %s", first.Code, first.Body)
	}
	rerun := f.supersede(decodeRun(t, first).ID)
	if rerun.Code != http.StatusCreated {
		t.Fatalf("reversing and closing again into the closed last period got %d %s", rerun.Code, rerun.Body)
	}
	if got := len(f.ledger.Entries()); got != 3 {
		t.Fatalf("closing, reversal and closing again expected, got %d", got)
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
	if rec.Code != http.StatusOK || len(f.ledger.Entries()) != 1 || f.runs.completed != 1 {
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
	if rec.Code != http.StatusOK || len(f.ledger.Entries()) != 1 || f.runs.latest().Status != domain.RunCompleted {
		t.Fatalf("retry got %d %s with %d entries", rec.Code, rec.Body, len(f.ledger.Entries()))
	}
}

func TestYearEnd_RetryAfterAnOutageResendsTheFrozenProposalsEvenWhenTheLedgerMoved(t *testing.T) {
	f := newYearEnd()
	f.ledger.FailPosts(errors.New("ledger down"))
	if rec := f.run(yearEndBody()); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if !f.runs.latest().Dispatched || proposalAmounts(f.runs.latest().Proposals.Closing)["3000"] != "-100" {
		t.Fatalf("the first derivation is frozen before the post is sent: %+v", f.runs.latest())
	}
	f.ledger.cumulative = []ledgerclient.BalanceRow{net("1000", 250, nil), net("4000", -250, nil)}
	f.ledger.thisYear = []ledgerclient.BalanceRow{net("4000", -250, nil)}
	f.ledger.FailPosts(nil)
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusOK || len(f.ledger.Entries()) != 1 {
		t.Fatalf("retry got %d %s with %d entries", rec.Code, rec.Body, len(f.ledger.Entries()))
	}
	if got := lineAmounts(f.ledger.Entries()[0]); got["3000"] != "C100" {
		t.Fatalf("the retry must resend the frozen 100, got %v", got)
	}
}

func TestYearEnd_LostAnswerRetryRecordsTheEntryTheLedgerAlreadyHolds(t *testing.T) {
	f := newYearEnd()
	f.ledger.LoseAnswers(errors.New("timeout"))
	if rec := f.run(yearEndBody()); rec.Code != http.StatusServiceUnavailable || len(f.ledger.Entries()) != 1 {
		t.Fatalf("got %d %s with %d entries", rec.Code, rec.Body, len(f.ledger.Entries()))
	}
	if latest := f.runs.latest(); latest.Status != domain.RunPending || len(latest.Journals) != 0 {
		t.Fatalf("the answer was lost, so no journal is recorded yet: %+v", latest)
	}
	f.ledger.LoseAnswers(nil)
	rec := f.run(yearEndBody())
	run := decodeRun(t, rec)
	if rec.Code != http.StatusOK || run.Status != domain.RunCompleted || len(f.ledger.Entries()) != 1 || run.Journals[domain.JournalClosing] != f.ledger.Entries()[0].ID {
		t.Fatalf("retry got %d %s with %d entries", rec.Code, rec.Body, len(f.ledger.Entries()))
	}
}

func TestYearEnd_LostAnswerRetryOnACumulativeLedgerStillRecordsTheEntryInsteadOfFindingNothingToClose(t *testing.T) {
	var ledger cumulativeLedger
	f := newYearEndOn(func(c *chartLedger) api.YearEndLedger {
		ledger = cumulativeLedger{c}
		return ledger
	})
	seed(t, f.ledger.Fake, "sale", "2026-03-15", "1000", "4000", 100, "A")
	f.ledger.LoseAnswers(errors.New("timeout"))
	if rec := f.run(yearEndBody()); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	f.ledger.LoseAnswers(nil)
	seed(t, f.ledger.Fake, "late-sale", "2026-11-02", "1000", "4000", 40, "A")
	rec := f.run(yearEndBody())
	run := decodeRun(t, rec)
	closing := f.ledger.Entries()[1]
	if rec.Code != http.StatusOK || len(f.ledger.Entries()) != 3 || run.Journals[domain.JournalClosing] != closing.ID || run.Status != domain.RunCompleted {
		t.Fatalf("retry got %d %s with %d entries", rec.Code, rec.Body, len(f.ledger.Entries()))
	}
	if got := lineAmounts(closing); got["3000"] != "C100" {
		t.Fatalf("the frozen body is what the ledger holds, got %v", got)
	}
}

func TestYearEnd_DefiniteRefusalAfterAnEarlierDispatchKeepsTheRunPending(t *testing.T) {
	f := newYearEnd()
	f.ledger.LoseAnswers(errors.New("timeout"))
	f.run(yearEndBody())
	f.ledger.LoseAnswers(nil)
	f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusUnprocessableEntity || f.runs.latest().Status != domain.RunPending {
		t.Fatalf("an earlier attempt may have been accepted, so the run stays pending: %d %s %+v", rec.Code, rec.Body, f.runs.latest())
	}
}

func TestYearEnd_OnlyDefiniteRefusalsAbandonAFirstDispatch(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusConflict} {
		f := newYearEnd()
		f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: status, Code: "X"})
		f.run(yearEndBody())
		if got := f.runs.latest().Status; got != domain.RunPending {
			t.Errorf("status %d must leave the run pending, got %s", status, got)
		}
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity} {
		f := newYearEnd()
		f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: status, Code: "X"})
		f.run(yearEndBody())
		if got := f.runs.latest().Status; got != domain.RunAbandoned {
			t.Errorf("status %d is a definite refusal, got %s", status, got)
		}
	}
}

func TestYearEnd_RepeatOfACompletedRequestReturnsTheStoredRunEvenIfAPeriodReopened(t *testing.T) {
	f := newYearEnd()
	first := decodeRun(t, f.run(yearEndBody()))
	f.runs.periods[11].Rows = append(f.runs.periods[11].Rows, domain.Row{Module: domain.ModuleAR, Status: domain.StatusOpen})
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusOK || decodeRun(t, rec).ID != first.ID || len(f.ledger.Entries()) != 1 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestYearEnd_RerunOnACumulativeLedgerKeepsTheBalanceSheetAndZeroesProfitAndLoss(t *testing.T) {
	var ledger cumulativeLedger
	f := newYearEndOn(func(c *chartLedger) api.YearEndLedger {
		ledger = cumulativeLedger{c}
		return ledger
	})
	seed(t, f.ledger.Fake, "prior-capital", "2025-06-30", "1000", "3100", 500, "A")
	seed(t, f.ledger.Fake, "sale", "2026-03-15", "1000", "4000", 100, "A")
	body := strings.Replace(yearEndBody(), `"3000"`, `"3000","retainDimensions":true`, 1)
	first := decodeRun(t, f.run(body))
	seed(t, f.ledger.Fake, "late-sale", "2026-11-02", "1000", "4000", 40, "A")
	balances := func(from string) map[string]decimal.Decimal {
		t.Helper()
		tb, err := ledger.TrialBalance(context.Background(), ledgerclient.TrialBalanceQuery{LegalEntityID: testkit.TenantID(9), LedgerID: testkit.TenantID(7), PeriodFrom: from, PeriodTo: "2026-12", GroupBy: []string{"CC"}})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]decimal.Decimal{}
		for _, row := range tb.Rows {
			out[row.Account+"|"+row.Dimensions["CC"]] = row.Net
		}
		return out
	}
	before := balances("")
	rec := f.run(strings.TrimSuffix(body, "}") + `,"supersedesRunId":"` + first.ID + `"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	after := balances("")
	for _, key := range []string{"1000|A", "3100|A"} {
		if !after[key].Equal(before[key]) {
			t.Errorf("%s moved from %s to %s across the re-run", key, before[key], after[key])
		}
	}
	if !after["3000|A"].Equal(decimal.NewFromInt(-140)) {
		t.Errorf("retained earnings must carry the whole year's 140 after the re-run, got %s", after["3000|A"])
	}
	if !after["4000|A"].IsZero() || !balances("2026-01")["4000|A"].IsZero() {
		t.Errorf("profit and loss must be zero, got %s", after["4000|A"])
	}
}

func TestYearEnd_LedgerRejectionAbandonsTheRunSoANewRequestCanStart(t *testing.T) {
	f := newYearEnd()
	f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"serviceCode":"CLOSE-0008"`) || !strings.Contains(rec.Body.String(), ledgerclient.CodePeriodClosed) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if f.runs.latest().Status != domain.RunAbandoned {
		t.Fatalf("a rejection with nothing posted must abandon the run, got %s", f.runs.latest().Status)
	}
	f.ledger.FailPosts(nil)
	f.ledger.cumulative = []ledgerclient.BalanceRow{net("1000", 180, nil), net("4000", -180, nil)}
	f.ledger.thisYear = []ledgerclient.BalanceRow{net("4000", -180, nil)}
	retry := f.run(strings.Replace(yearEndBody(), `"3000"`, `"3100"`, 1))
	if retry.Code != http.StatusCreated {
		t.Fatalf("a new request after the abandoned run got %d %s", retry.Code, retry.Body)
	}
	run := decodeRun(t, retry)
	if run.Sequence != 2 || run.Status != domain.RunCompleted || len(f.ledger.Entries()) != 1 {
		t.Fatalf("run %+v with %d entries", run, len(f.ledger.Entries()))
	}
	if got := lineAmounts(f.ledger.Entries()[0]); got["3100"] != "C180" {
		t.Fatalf("the new run must post the ledger's current 180, got %v", got)
	}
}

func TestYearEnd_RejectionAfterAReversalPostedKeepsTheRunPendingForTheRetry(t *testing.T) {
	f := newYearEnd()
	prior := decodeRun(t, f.run(yearEndBody()))
	f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	rec := f.supersede(prior.ID)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if latest := f.runs.latest(); latest.Status != domain.RunPending || latest.Journals[domain.JournalReverseClosing] == "" {
		t.Fatalf("the reversal posted, so the run must stay pending: %+v", latest)
	}
	f.ledger.FailPosts(nil)
	if retry := f.supersede(prior.ID); retry.Code != http.StatusOK || len(f.ledger.Entries()) != 3 {
		t.Fatalf("retry got %d %s with %d entries", retry.Code, retry.Body, len(f.ledger.Entries()))
	}
}

func TestYearEnd_LedgerAuthorizationFailureIsReportedNotRetryableAndNotAbandoned(t *testing.T) {
	f := newYearEnd()
	f.ledger.balanceErr = &ledgerclient.Error{Op: "trial-balance", Status: http.StatusForbidden, Code: "AUTH-0003"}
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "403") || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if f.runs.latest().Status != domain.RunPending || len(f.ledger.Entries()) != 0 {
		t.Fatalf("run %+v", f.runs.latest())
	}
}

func TestYearEnd_TrialBalanceInAnotherCurrencyIsRefused(t *testing.T) {
	f := newYearEnd()
	f.ledger.balanceCurren = "EUR"
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "EUR") || len(f.ledger.Entries()) != 0 {
		t.Fatalf("got %d %s with %d entries", rec.Code, rec.Body, len(f.ledger.Entries()))
	}
}

func TestYearEnd_PostingStillInProgressAsksForARetry(t *testing.T) {
	f := newYearEnd()
	f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusConflict, Code: ledgerclient.CodeInProgress})
	rec := f.run(yearEndBody())
	if rec.Code != http.StatusConflict || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"serviceCode":"CLOSE-0002"`) {
		t.Fatalf("got %d %s retry-after %q", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
	}
	if f.runs.latest().Status != domain.RunPending {
		t.Fatalf("a posting in progress must leave the run pending, got %s", f.runs.latest().Status)
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

func TestYearEnd_SupersedesReversesThePriorClosingEntryAndPostsAgainFromTheLedger(t *testing.T) {
	f := newYearEnd()
	prior := decodeRun(t, f.run(yearEndBody()))
	f.ledger.cumulative = []ledgerclient.BalanceRow{net("1000", 180, nil), net("4000", -180, nil)}
	f.ledger.thisYear = []ledgerclient.BalanceRow{net("4000", -180, nil)}

	rec := f.supersede(prior.ID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	rerun := decodeRun(t, rec)
	if rerun.Sequence != 2 || rerun.SupersedesRunID != prior.ID || rerun.Status != domain.RunCompleted {
		t.Fatalf("rerun %+v", rerun)
	}
	posted := f.ledger.Entries()
	if len(posted) != 3 {
		t.Fatalf("want the original, its reversal and the new closing entry, got %d", len(posted))
	}
	if posted[1].ReversalOf != prior.Journals[domain.JournalClosing] || !posted[1].PostClosed || posted[1].PostingDate != "2026-12-31" {
		t.Fatalf("the prior closing entry must be reversed on its own date: %+v", posted[1])
	}
	if rerun.Journals[domain.JournalReverseClosing] != posted[1].ID || len(rerun.Journals) != 2 {
		t.Fatalf("rerun must record its reversal: %+v", rerun.Journals)
	}
	if got := lineAmounts(posted[2]); got["3000"] != "C180" {
		t.Fatalf("the re-run must post the ledger's current 180, got %v", got)
	}
	again := f.supersede(prior.ID)
	if again.Code != http.StatusOK || decodeRun(t, again).ID != rerun.ID || len(f.ledger.Entries()) != 3 {
		t.Fatalf("repeating the completed re-run got %d %s with %d entries", again.Code, again.Body, len(f.ledger.Entries()))
	}
}

func TestYearEnd_SupersedesMismatchOnAnUnfinishedRerunConflicts(t *testing.T) {
	f := newYearEnd()
	prior := decodeRun(t, f.run(yearEndBody()))
	f.ledger.FailPosts(errors.New("ledger down"))
	if rec := f.supersede(prior.ID); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	f.ledger.FailPosts(nil)
	if rec := f.run(yearEndBody()); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), domain.CodeIdempotencyReuse) {
		t.Fatalf("the same request without supersedesRunId must not adopt the unfinished re-run: %d %s", rec.Code, rec.Body)
	}
}

func TestYearEnd_RerunWithoutNamingTheCompletedRunConflicts(t *testing.T) {
	f := newYearEnd()
	f.run(yearEndBody())
	f.ledger.cumulative = []ledgerclient.BalanceRow{net("1000", 180, nil), net("4000", -180, nil)}
	rec := f.run(strings.Replace(yearEndBody(), `"3000"`, `"3100"`, 1))
	if rec.Code != http.StatusConflict || len(f.ledger.Entries()) != 1 {
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

	gap := newYearEnd()
	gap.runs.periods = append(append([]domain.Period{}, gap.runs.periods[:5]...), gap.runs.periods[6:]...)

	noDecember := newYearEnd()
	noDecember.runs.periods = noDecember.runs.periods[:11]

	overlap := newYearEnd()
	overlap.runs.periods = append([]domain.Period{}, overlap.runs.periods...)
	overlap.runs.periods[3].End = overlap.runs.periods[3].End.AddDate(0, 0, 5)

	permanentlyClosedLast := newYearEnd()
	permanentlyClosedLast.runs.periods[11].Rows = []domain.Row{{Module: domain.ModuleAll, Status: domain.StatusPermanentlyClosed}}

	noAllRow := newYearEnd()
	noAllRow.runs.periods[11].Rows = []domain.Row{{Module: domain.ModuleAP, Status: domain.StatusClosed}}

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
		{"gap between periods", gap, yearEndBody(), http.StatusNotFound, domain.CodeNoPeriod},
		{"december not registered", noDecember, yearEndBody(), http.StatusNotFound, domain.CodeNoPeriod},
		{"overlapping periods", overlap, yearEndBody(), http.StatusConflict, domain.CodeAmbiguousPeriod},
		{"last period permanently closed", permanentlyClosedLast, yearEndBody(), http.StatusConflict, domain.CodeInvalidTransition},
		{"last period has no all-modules row", noAllRow, yearEndBody(), http.StatusConflict, domain.CodeInvalidTransition},
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
