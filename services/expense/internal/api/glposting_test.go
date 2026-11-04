package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/expense/internal/api"
	"github.com/BenjaminSRussell/passage/services/expense/internal/store"
)

const (
	glEntity = "00000000-0000-0000-0000-0000000000a1"
	glLedger = "00000000-0000-0000-0000-0000000000b1"
	glOther  = "00000000-0000-0000-0000-0000000000a2"
)

type fakeBooks struct {
	due    []store.GLReport
	booked map[string]string
}

func (f *fakeBooks) DueGLReports(_ context.Context, le string, limit int) ([]store.GLReport, bool, error) {
	var out []store.GLReport
	for _, g := range f.due {
		if _, done := f.booked[g.ID]; !done && g.LegalEntityID == le {
			out = append(out, g)
		}
	}
	return out, false, nil
}

func (f *fakeBooks) MarkGLBooked(_ context.Context, id, entryID string) (bool, error) {
	if _, done := f.booked[id]; done {
		return false, nil
	}
	f.booked[id] = entryID
	return true, nil
}

func glReport(id string, byCategory map[string]int64) store.GLReport {
	g := store.GLReport{ID: id, LegalEntityID: glEntity, Currency: "USD", Status: "APPROVED", ApprovedOn: "2026-03-04", ByCategory: map[string]decimal.Decimal{}}
	total := decimal.Zero
	for c, v := range byCategory {
		g.ByCategory[c] = decimal.NewFromInt(v)
		total = total.Add(decimal.NewFromInt(v))
	}
	g.Reimbursable = total
	return g
}

func postGL(t *testing.T, h http.Handler, body, duties, entity string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/expense/gl-postings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
	req.Header.Set(tenant.HeaderDuties, duties)
	req.Header.Set(tenant.HeaderLegalEntity, entity)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type glResult struct {
	Items []struct {
		ReportID, Amount, PostingDate, JournalEntryID string
	} `json:"items"`
	Failed []struct{ ReportID, Code string } `json:"failed"`
}

func TestPostGL_BooksEachApprovedReportOnceAndHandsItToPayablesOnlyThen(t *testing.T) {
	profiles, err := api.ParseGLProfiles(`[{"legalEntityId":"` + glEntity + `","ledgerId":"` + glLedger + `","apControlAccount":"2100","expenseAccounts":{"TRAVEL":"6100","MEALS":"6200"}}]`)
	if err != nil {
		t.Fatal(err)
	}
	const good, unmapped, refused = "00000000-0000-0000-0000-0000000000c1", "00000000-0000-0000-0000-0000000000c2", "00000000-0000-0000-0000-0000000000c3"
	books := &fakeBooks{booked: map[string]string{}, due: []store.GLReport{
		glReport(good, map[string]int64{"TRAVEL": 80, "MEALS": 20}),
		glReport(unmapped, map[string]int64{"GIFTS": 15}),
	}}
	ledger := ledgerclient.NewFake()
	router := httpx.NewRouter()
	api.New(nil).WithGL(ledger, books, profiles).Register(router)

	if rec := postGL(t, router, `{}`, "expense.report.write", glEntity); rec.Code != http.StatusForbidden {
		t.Fatalf("without expense.gl.post: %d %s", rec.Code, rec.Body)
	}
	if rec := postGL(t, router, `{"legalEntityId":"`+glOther+`"}`, "expense.gl.post", glEntity); rec.Code != http.StatusForbidden {
		t.Fatalf("another legal entity: %d %s", rec.Code, rec.Body)
	}
	for pass := 1; pass <= 2; pass++ {
		rec := postGL(t, router, `{}`, "expense.gl.post", glEntity)
		if rec.Code != http.StatusOK {
			t.Fatalf("pass %d: %d %s", pass, rec.Code, rec.Body)
		}
		var out glResult
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		wantBooked := 1
		if pass == 2 {
			wantBooked = 0
		}
		if len(out.Items) != wantBooked || len(out.Failed) != 1 || out.Failed[0].ReportID != unmapped || out.Failed[0].Code != api.CodeGLNotMapped {
			t.Fatalf("pass %d = %s; want the mapped report booked once and the unmapped one failed", pass, rec.Body)
		}
		if pass == 1 && (out.Items[0].ReportID != good || out.Items[0].Amount != "100.00" || out.Items[0].PostingDate != "2026-03-04" || books.booked[good] != out.Items[0].JournalEntryID) {
			t.Fatalf("booked = %+v; want report %s for 100.00 on its approval day", out.Items[0], good)
		}
	}
	entries := ledger.Entries()
	if len(entries) != 1 {
		t.Fatalf("ledger entries = %d; want one", len(entries))
	}
	e := entries[0]
	if e.IdempotencyKey != "expense-report-"+good || e.DocumentID != good || e.SourceType != "expense_report" || e.LedgerID != glLedger || e.PostingDate != "2026-03-04" || len(e.Lines) != 3 {
		t.Fatalf("entry = %+v", e.Entry)
	}
	got := map[string]string{}
	for _, l := range e.Lines {
		side := "D"
		if l.Credit {
			side = "C"
		}
		got[side+l.Account] = l.Amount.StringFixed(2)
	}
	if got["D6100"] != "80.00" || got["D6200"] != "20.00" || got["C2100"] != "100.00" {
		t.Fatalf("lines = %v; want Dr 6100 80, Dr 6200 20, Cr AP control 2100 100", got)
	}

	books.due = append(books.due, glReport(refused, map[string]int64{"TRAVEL": 5}))
	ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	rec := postGL(t, router, `{}`, "expense.gl.post", glEntity)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ledgerclient.CodePeriodClosed) || books.booked[refused] != "" {
		t.Fatalf("refused post: %d %s; want it listed with the ledger code and left due", rec.Code, rec.Body)
	}
	ledger.FailPosts(errors.New("connection reset"))
	if rec := postGL(t, router, `{}`, "expense.gl.post", glEntity); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), api.CodeGLUnavailable) {
		t.Fatalf("ledger down: %d %s", rec.Code, rec.Body)
	}
}

func TestPostGL_RefusesWithoutLedgerOrProfileAndAnUnbalancedReport(t *testing.T) {
	router := httpx.NewRouter()
	api.New(nil).Register(router)
	if rec := postGL(t, router, `{}`, "expense.gl.post", glEntity); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), api.CodeGLNotConfigured) {
		t.Fatalf("no ledger: %d %s", rec.Code, rec.Body)
	}
	const odd = "00000000-0000-0000-0000-0000000000c4"
	g := glReport(odd, map[string]int64{"TRAVEL": 50})
	g.Reimbursable = decimal.NewFromInt(60)
	books := &fakeBooks{booked: map[string]string{}, due: []store.GLReport{g}}
	profiles, _ := api.ParseGLProfiles(`[{"legalEntityId":"` + glEntity + `","ledgerId":"` + glLedger + `","apControlAccount":"2100","expenseAccounts":{"TRAVEL":"6100"}}]`)
	router = httpx.NewRouter()
	api.New(nil).WithGL(ledgerclient.NewFake(), books, profiles).Register(router)
	if rec := postGL(t, router, `{}`, "expense.gl.post", glOther); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), api.CodeGLNoProfile) {
		t.Fatalf("no profile: %d %s", rec.Code, rec.Body)
	}
	if rec := postGL(t, router, `{}`, "expense.gl.post", glEntity); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), api.CodeGLLinesMismatch) {
		t.Fatalf("lines not adding up: %d %s", rec.Code, rec.Body)
	}
}

func TestParseGLProfiles_RefusesIncompleteProfiles(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":      `{`,
		"no accounts":   `[{"legalEntityId":"` + glEntity + `","ledgerId":"` + glLedger + `","apControlAccount":"2100"}]`,
		"no control":    `[{"legalEntityId":"` + glEntity + `","ledgerId":"` + glLedger + `","expenseAccounts":{"TRAVEL":"6100"}}]`,
		"bad ledger":    `[{"legalEntityId":"` + glEntity + `","ledgerId":"x","apControlAccount":"2100","expenseAccounts":{"TRAVEL":"6100"}}]`,
		"unknown field": `[{"legalEntityId":"` + glEntity + `","ledgerId":"` + glLedger + `","apControlAccount":"2100","expenseAccounts":{"TRAVEL":"6100"},"bank":"1010"}]`,
		"entity twice":  `[{"legalEntityId":"` + glEntity + `","ledgerId":"` + glLedger + `","apControlAccount":"2100","expenseAccounts":{"TRAVEL":"6100"}},{"legalEntityId":"` + glEntity + `","ledgerId":"` + glLedger + `","apControlAccount":"2100","expenseAccounts":{"TRAVEL":"6100"}}]`,
		"long account":  `[{"legalEntityId":"` + glEntity + `","ledgerId":"` + glLedger + `","apControlAccount":"2100","expenseAccounts":{"TRAVEL":"610000000000000000000"}}]`,
	} {
		if _, err := api.ParseGLProfiles(raw); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}
