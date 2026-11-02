package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/close/internal/api"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

const runsPath = "/v1/close/year-end-runs"

type memoryRuns struct {
	run       *domain.YearEndRun
	completed int
}

func (m *memoryRuns) BeginYearEnd(_ context.Context, in domain.YearEndRun) (domain.YearEndRun, bool, error) {
	if m.run == nil {
		in.ID = testkit.TenantID(50)
		in.Status = domain.RunPending
		m.run = &in
		return in, true, nil
	}
	if m.run.RequestHash != in.RequestHash {
		return domain.YearEndRun{}, false, &domain.Error{Code: domain.CodeIdempotencyReuse, Detail: "different request"}
	}
	return *m.run, false, nil
}

func (m *memoryRuns) CompleteYearEnd(_ context.Context, _, closing, opening string) (domain.YearEndRun, error) {
	if m.run.Status != domain.RunCompleted {
		m.completed++
	}
	m.run.Status, m.run.ClosingJournalID, m.run.OpeningJournalID = domain.RunCompleted, closing, opening
	return *m.run, nil
}

func (m *memoryRuns) YearEndRun(context.Context, string) (domain.YearEndRun, error) {
	if m.run == nil {
		return domain.YearEndRun{}, &domain.Error{Code: domain.CodeNoRun, Detail: "year-end run not found"}
	}
	return *m.run, nil
}

type yearEndFixture struct {
	runs   *memoryRuns
	ledger *ledgerclient.Fake
	router http.Handler
}

func newYearEnd() yearEndFixture {
	f := yearEndFixture{runs: &memoryRuns{}, ledger: ledgerclient.NewFake()}
	a := api.New(fakeStore{}).WithLedgers(fakeLedger{owner: testkit.TenantID(9)}).WithYearEnd(f.runs, f.ledger)
	r := httpx.NewRouter()
	a.Register(r)
	f.router = r
	return f
}

func yearEndBody(profit string) string {
	return `{"ledgerId":"` + testkit.TenantID(7) + `","fiscalYear":"FY2026","currency":"USD","closingDate":"2026-12-31","openingDate":"2027-01-01","retainedEarningsAccount":"3000","balances":[` +
		`{"account":"1000","class":"bs","amount":"` + profit + `"},{"account":"4000","class":"pl","amount":"-` + profit + `"}]}`
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

func TestYearEnd_PostsClosingAndOpeningEntriesAndCompletesOnce(t *testing.T) {
	f := newYearEnd()
	rec := f.send(http.MethodPost, runsPath, yearEndBody("100.00"), "close.yearend.run")
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var run domain.YearEndRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	posted := f.ledger.Entries()
	if len(posted) != 2 || run.Status != domain.RunCompleted || run.ClosingJournalID != posted[0].ID || run.OpeningJournalID != posted[1].ID {
		t.Fatalf("run %+v posted %d entries", run, len(posted))
	}
	closing := posted[0]
	if closing.PostingDate != "2026-12-31" || closing.SourceType != "year_end_closing" || len(closing.Lines) != 2 {
		t.Fatalf("closing entry %+v", closing.Entry)
	}
	if posted[1].PostingDate != "2027-01-01" || posted[1].SourceType != "year_end_opening" {
		t.Fatalf("opening entry %+v", posted[1].Entry)
	}
	if rec.Header().Get("Location") != runsPath+"/"+run.ID {
		t.Fatalf("location %q", rec.Header().Get("Location"))
	}
}

func TestYearEnd_RepeatReturnsTheStoredRunWithoutPostingAgain(t *testing.T) {
	f := newYearEnd()
	f.send(http.MethodPost, runsPath, yearEndBody("100.00"), "close.yearend.run")
	rec := f.send(http.MethodPost, runsPath, yearEndBody("100.00"), "close.yearend.run")
	if rec.Code != http.StatusOK || len(f.ledger.Entries()) != 2 || f.runs.completed != 1 {
		t.Fatalf("got %d, %d entries, %d completions", rec.Code, len(f.ledger.Entries()), f.runs.completed)
	}
}

func TestYearEnd_LedgerOutageLeavesARunThatTheRetryFinishesWithoutDuplicates(t *testing.T) {
	f := newYearEnd()
	f.ledger.FailPosts(errors.New("ledger down"))
	rec := f.send(http.MethodPost, runsPath, yearEndBody("100.00"), "close.yearend.run")
	if rec.Code != http.StatusServiceUnavailable || f.runs.run.Status != domain.RunPending || f.runs.completed != 0 {
		t.Fatalf("got %d %s, run %+v", rec.Code, rec.Body, f.runs.run)
	}
	f.ledger.FailPosts(nil)
	rec = f.send(http.MethodPost, runsPath, yearEndBody("100.00"), "close.yearend.run")
	if rec.Code != http.StatusOK || len(f.ledger.Entries()) != 2 || f.runs.run.Status != domain.RunCompleted {
		t.Fatalf("retry got %d %s with %d entries", rec.Code, rec.Body, len(f.ledger.Entries()))
	}
}

func TestYearEnd_LedgerRejectionIsReportedAsUnprocessable(t *testing.T) {
	f := newYearEnd()
	f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	rec := f.send(http.MethodPost, runsPath, yearEndBody("100.00"), "close.yearend.run")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"serviceCode":"CLOSE-0008"`) || !strings.Contains(rec.Body.String(), ledgerclient.CodePeriodClosed) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if f.runs.run.Status != domain.RunPending {
		t.Fatalf("a rejected posting must leave the run pending, got %s", f.runs.run.Status)
	}
}

func TestYearEnd_SameYearWithADifferentBodyConflicts(t *testing.T) {
	f := newYearEnd()
	f.send(http.MethodPost, runsPath, yearEndBody("100.00"), "close.yearend.run")
	rec := f.send(http.MethodPost, runsPath, yearEndBody("200.00"), "close.yearend.run")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), domain.CodeIdempotencyReuse) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestYearEnd_Validation(t *testing.T) {
	unbalanced := strings.Replace(yearEndBody("100.00"), `"amount":"-100.00"`, `"amount":"-90.00"`, 1)
	tests := []struct {
		name, payload, duties string
		want                  int
		wantBody              string
	}{
		{"missing duty", yearEndBody("1.00"), "close.yearend.read", http.StatusForbidden, "AUTH-0003"},
		{"bad currency", strings.Replace(yearEndBody("1.00"), "USD", "usd", 1), "close.yearend.run", http.StatusBadRequest, `"currency"`},
		{"bad date", strings.Replace(yearEndBody("1.00"), "2026-12-31", "12/31/2026", 1), "close.yearend.run", http.StatusBadRequest, `"closingDate"`},
		{"bad fiscal year", strings.Replace(yearEndBody("1.00"), "FY2026", "FY 2026", 1), "close.yearend.run", http.StatusBadRequest, `"fiscalYear"`},
		{"balances that do not sum to zero", unbalanced, "close.yearend.run", http.StatusBadRequest, "do not sum to zero"},
		{"unknown field", `{"x":1}`, "close.yearend.run", http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newYearEnd()
			rec := f.send(http.MethodPost, runsPath, tc.payload, tc.duties)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s, want %d containing %q", rec.Code, rec.Body, tc.want, tc.wantBody)
			}
			if len(f.ledger.Entries()) != 0 || f.runs.run != nil {
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
		f.send(http.MethodPost, runsPath, yearEndBody("1.00"), "close.yearend.run"),
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
	f.send(http.MethodPost, runsPath, yearEndBody("100.00"), "close.yearend.run")
	found := f.send(http.MethodGet, runsPath+"/"+testkit.TenantID(50), "", "close.yearend.read")
	if found.Code != http.StatusOK || !strings.Contains(found.Body.String(), `"status":"completed"`) {
		t.Fatalf("got %d %s", found.Code, found.Body)
	}
	if denied := f.send(http.MethodGet, runsPath+"/"+testkit.TenantID(50), "", "close.yearend.run"); denied.Code != http.StatusForbidden {
		t.Fatalf("run duty must not read, got %d", denied.Code)
	}
}
