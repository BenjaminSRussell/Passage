package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/config"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

// lineStore answers DrillLines with a canned page and records the query.
type lineStore struct {
	fakeStore
	page  store.LinePage
	err   error
	query *store.LineQuery
	row   string
}

func (f *lineStore) DrillLines(_ context.Context, _ string, row, _ string, q store.LineQuery) (store.LinePage, error) {
	f.query, f.row = &q, row
	return f.page, f.err
}

const linesPath = "/api/v1/reporting/runs/" + runUUID + "/cells/REV/P/lines"

func linesRouter(f *lineStore, cfg config.Reporting) chi.Router {
	r := chi.NewRouter()
	New(f).WithConfig(cfg).Register(r)
	return r
}

type linesBody struct {
	Key              *int    `json:"key"`
	Value            string  `json:"value"`
	Computed         bool    `json:"computed"`
	HWM              int64   `json:"hwm"`
	Limit            int     `json:"limit"`
	SumOfLines       string  `json:"sumOfLines"`
	CumulativeSum    string  `json:"cumulativeSum"`
	NextCursor       *string `json:"nextCursor"`
	LinesTotal       string  `json:"linesTotal"`
	Difference       string  `json:"difference"`
	UnreconciledKeys int     `json:"unreconciledKeys"`
	Reconciled       bool    `json:"reconciled"`
	Items            []struct {
		Key       int    `json:"key"`
		Voucher   string `json:"voucher"`
		Period    string `json:"period"`
		Debit     string `json:"debit"`
		Credit    string `json:"credit"`
		Amount    string `json:"amount"`
		SourceURI string `json:"sourceUri"`
		TraceID   string `json:"traceId"`
		TraceURL  string `json:"traceUrl"`
	} `json:"items"`
}

func TestDrillLines_Page(t *testing.T) {
	line := domain.JournalLine{PostingSequence: 2, LineNumber: 1, PostingDate: "2026-03-05", PeriodYear: 2026, PeriodNo: 3, Account: "4000",
		Combination: "-", CurrencyType: "acct", Voucher: "AP-7", Debit: decimal.Zero, Credit: decimal.RequireFromString("100"),
		SourceURI: "/payables/invoices/INV-1", TraceID: "4bf92f35"}
	next := domain.LineCursor{PostingDate: "2026-03-05", PostingSequence: 2, LineNumber: 1, Key: 0}
	f := &lineStore{page: store.LinePage{
		Cell: domain.Cell{Row: "REV", Column: "P"}, Key: -1, Value: decimal.RequireFromString("135.5"), HWM: 7,
		Items:      []store.LineItem{{JournalLine: line, Key: 0, Amount: decimal.RequireFromString("100")}},
		SumOfLines: decimal.RequireFromString("100"), CumulativeSum: decimal.RequireFromString("110"), Next: &next,
		Reconciliation: domain.Reconciliation{LinesTotal: decimal.RequireFromString("110"), Difference: decimal.RequireFromString("25.5"), UnreconciledKeys: 1},
	}}
	cfg := config.Reporting{PageSize: 250, TraceURL: "https://grafana.example/explore?traceId={traceId}"}
	w := call(linesRouter(f, cfg), http.MethodGet, linesPath, "", nil, DutyLines)
	var out linesBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if f.query.Key != -1 || f.query.Limit != 250 || f.query.After != nil || f.row != "REV" {
		t.Fatalf("query %+v row %s; want whole cell, the configured page size, first page", *f.query, f.row)
	}
	if out.Key != nil || out.Value != "135.500000" || out.HWM != 7 || out.Limit != 250 || out.SumOfLines != "100.000000" || out.CumulativeSum != "110.000000" {
		t.Fatalf("page header %+v", out)
	}
	if out.Reconciled || out.UnreconciledKeys != 1 || out.Difference != "25.500000" || out.LinesTotal != "110.000000" {
		t.Fatalf("a gap must be reported, got %+v", out)
	}
	it := out.Items[0]
	if it.Voucher != "AP-7" || it.Period != "2026-03" || it.Debit != "0.000000" || it.Credit != "100.000000" || it.Amount != "100.000000" ||
		it.SourceURI != "/payables/invoices/INV-1" || it.TraceURL != "https://grafana.example/explore?traceId=4bf92f35" {
		t.Fatalf("item %+v", it)
	}
	if out.NextCursor == nil {
		t.Fatal("nextCursor missing on a page with more lines")
	}
	if got, err := domain.DecodeLineCursor(*out.NextCursor); err != nil || got != next {
		t.Fatalf("nextCursor decodes to %+v, %v", got, err)
	}

	// The cursor and key come back in the query; without a trace template there is no link.
	f.page.Next, f.page.Key = nil, 1
	w = call(linesRouter(f, config.Reporting{}), http.MethodGet, linesPath+"?key=1&limit=1000&cursor="+next.Encode(), "", nil, DutyLines)
	out = linesBody{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if f.query.Key != 1 || f.query.Limit != 1000 || f.query.After == nil || *f.query.After != next {
		t.Fatalf("query %+v", *f.query)
	}
	if out.Key == nil || *out.Key != 1 || out.NextCursor != nil || out.Items[0].TraceURL != "" || out.Items[0].TraceID != "4bf92f35" {
		t.Fatalf("second page %+v", out)
	}
}

func TestDrillLines_Refusals(t *testing.T) {
	cases := []struct {
		name, path string
		duties     []string
		storeErr   error
		want       int
		code       string
	}{
		{"statement viewers need the ledger transactions duty", linesPath, []string{DutyView}, nil, http.StatusForbidden, "AUTH-0003"},
		{"limit zero", linesPath + "?limit=0", []string{DutyLines}, nil, http.StatusBadRequest, "COM-0002"},
		{"limit above 1000", linesPath + "?limit=1001", []string{DutyLines}, nil, http.StatusBadRequest, "COM-0002"},
		{"limit not a number", linesPath + "?limit=all", []string{DutyLines}, nil, http.StatusBadRequest, "COM-0002"},
		{"negative key", linesPath + "?key=-1", []string{DutyLines}, nil, http.StatusBadRequest, "COM-0002"},
		{"forged cursor", linesPath + "?cursor=bm90LWEtY3Vyc29y", []string{DutyLines}, nil, http.StatusBadRequest, "COM-0002"},
		{"run id not a uuid", "/api/v1/reporting/runs/nope/cells/REV/P/lines", []string{DutyLines}, nil, http.StatusNotFound, store.CodeRunNotFound},
		{"unknown cell", linesPath, []string{DutyLines}, httpx.NewProblem(http.StatusNotFound, store.CodeCellNotFound, "no cell"), http.StatusNotFound, store.CodeCellNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &lineStore{err: tc.storeErr}
			w := call(linesRouter(f, config.Reporting{}), http.MethodGet, tc.path, "", nil, tc.duties...)
			if w.Code != tc.want || problemCode(t, w) != tc.code {
				t.Fatalf("status %d code %s, want %d %s: %s", w.Code, problemCode(t, w), tc.want, tc.code, w.Body.String())
			}
			if tc.storeErr == nil && f.query != nil {
				t.Fatalf("refused request reached the store: %+v", *f.query)
			}
		})
	}
}
