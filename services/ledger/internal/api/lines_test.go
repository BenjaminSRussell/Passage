package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

func (f *fakeStore) ListLines(_ context.Context, in store.LineFilter) (store.LinePage, error) {
	f.lineFilters = append(f.lineFilters, in)
	return f.linePage, f.err
}

const (
	linesPath = "/v1/ledger/journal-lines"
	lineTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
)

func TestLineCursor_TableDriven(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		wantOK bool
		seq    int64
		line   int
	}{
		{"empty is first page", "", true, 0, 0},
		{"well formed", "42.7", true, 42, 7},
		{"no dot", "427", false, 0, 0},
		{"letters", "a.b", false, 0, 0},
		{"negative", "-1.2", false, 0, 0},
		{"too long sequence", "1234567890123456789.1", false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lineCursor(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("lineCursor(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if !ok || got == nil {
				return
			}
			if got.PostingSequence != tt.seq || got.LineNumber != tt.line {
				t.Fatalf("lineCursor(%q) = %+v, want seq %d line %d", tt.in, got, tt.seq, tt.line)
			}
			if formatCursor(got) != tt.in {
				t.Fatalf("formatCursor round trip = %q, want %q", formatCursor(got), tt.in)
			}
		})
	}
}

func TestListLines_ValidationStopsBeforeStore(t *testing.T) {
	tests := []struct {
		name, query, field string
	}{
		{"missing ledger", "account_from=1000", "ledger"},
		{"ledger not a uuid", "ledger=nope", "ledger"},
		{"bad account", "ledger=" + testLedger + "&account_from=60%2010", "account_from"},
		{"bad date", "ledger=" + testLedger + "&date_from=2026-13-40", "date_from"},
		{"inverted date range", "ledger=" + testLedger + "&date_from=2026-03-31&date_to=2026-03-01", "date_to"},
		{"bad dimension name", "ledger=" + testLedger + "&dim.bad-name=X", "dim.bad-name"},
		{"empty dimension value", "ledger=" + testLedger + "&dim.DEPT=", "dim.DEPT"},
		{"repeated dimension", "ledger=" + testLedger + "&dim.DEPT=D100&dim.DEPT=D200", "dim.DEPT"},
		{"voucher too long", "ledger=" + testLedger + "&voucher=" + strings.Repeat("V", 31), "voucher"},
		{"limit above page cap", "ledger=" + testLedger + "&limit=5000", "limit"},
		{"limit zero", "ledger=" + testLedger + "&limit=0", "limit"},
		{"documentId not a uuid", "ledger=" + testLedger + "&documentId=doc-1", "documentId"},
		{"malformed cursor", "ledger=" + testLedger + "&cursor=oops", "cursor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call(server(fs), http.MethodGet, linesPath+"?"+tt.query, "", dutyRead, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if len(fs.lineFilters) != 0 {
				t.Fatalf("store called for an invalid request")
			}
			errs, _ := decode(t, rec)["errors"].(map[string]any)
			if _, ok := errs[tt.field]; !ok {
				t.Fatalf("errors = %v, want a %q entry", errs, tt.field)
			}
		})
	}
}

func TestListLines_RequiresReadDuty(t *testing.T) {
	fs := &fakeStore{}
	rec := call(server(fs), http.MethodGet, linesPath+"?ledger="+testLedger, "", dutyBalanceRead+","+dutyPost, nil)
	if rec.Code != http.StatusForbidden || len(fs.lineFilters) != 0 {
		t.Fatalf("without %s: status %d, store calls %d", dutyRead, rec.Code, len(fs.lineFilters))
	}
}

func TestListLines_PassesFilterAndReturnsCursor(t *testing.T) {
	fs := &fakeStore{linePage: store.LinePage{
		Items: []store.JournalLine{{ID: "l1", Account: "6010", Amount: "10.00", Currency: "USD", DebitCredit: "D", TraceID: lineTrace}},
		Next:  &store.LineCursor{PostingSequence: 7, LineNumber: 2},
	}}
	q := url.Values{
		"ledger": {testLedger}, "account_from": {"6000"}, "account_to": {"6999"}, "dim.DEPT": {"D100"}, "dim.CC": {"C1"},
		"date_from": {"2026-01-01"}, "date_to": {"2026-01-31"}, "voucher": {"GL-000042"}, "source_system": {"ap"},
		"cursor": {"12.3"}, "limit": {"2"},
	}
	rec := call(server(fs), http.MethodGet, linesPath+"?"+q.Encode(), "", dutyRead, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got := fs.lineFilters[0]
	if got.LedgerID != testLedger || got.AccountFrom != "6000" || got.AccountTo != "6999" ||
		got.DateFrom != "2026-01-01" || got.DateTo != "2026-01-31" || got.Voucher != "GL-000042" || got.SourceSystem != "ap" {
		t.Fatalf("filter = %+v", got)
	}
	if len(got.Dimensions) != 2 || got.Dimensions["DEPT"] != "D100" || got.Dimensions["CC"] != "C1" {
		t.Fatalf("dimensions = %v", got.Dimensions)
	}
	if got.After == nil || got.After.PostingSequence != 12 || got.After.LineNumber != 3 || got.Limit != 2 {
		t.Fatalf("cursor/limit = %+v / %d", got.After, got.Limit)
	}
	var body struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.NextCursor != "7.2" || body.Items[0]["traceId"] != lineTrace || body.Items[0]["amount"] != "10.00" {
		t.Fatalf("body = %+v", body)
	}
}

func TestListLines_LastPageOmitsCursorAndUsesDefaultLimit(t *testing.T) {
	fs := &fakeStore{}
	rec := call(server(fs), http.MethodGet, linesPath+"?ledger="+testLedger, "", dutyRead, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if fs.lineFilters[0].Limit != defaultLinePage || fs.lineFilters[0].After != nil {
		t.Fatalf("filter = %+v, want limit %d and no cursor", fs.lineFilters[0], defaultLinePage)
	}
	body := decode(t, rec)
	if _, ok := body["nextCursor"]; ok {
		t.Fatalf("last page must omit nextCursor: %v", body)
	}
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("an empty page must carry items: [], got %v", body["items"])
	}
}

func TestListLines_UnknownLedgerIs404(t *testing.T) {
	fs := &fakeStore{err: httpx.NewProblem(http.StatusNotFound, "LEDGER_NOT_FOUND", "ledger not found")}
	rec := call(server(fs), http.MethodGet, linesPath+"?ledger="+testLedger, "", dutyRead, nil)
	if rec.Code != http.StatusNotFound || decode(t, rec)["code"] != "LEDGER_NOT_FOUND" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestListLines_PassesDocumentIDAndAcceptsTheLookupDuty(t *testing.T) {
	fs := &fakeStore{}
	rec := call(server(fs), http.MethodGet, linesPath+"?ledger="+testLedger+"&documentId="+testDoc, "", dutyEntryLookup, nil)
	if rec.Code != http.StatusOK || len(fs.lineFilters) != 1 || fs.lineFilters[0].DocumentID != testDoc {
		t.Fatalf("status %d filters %+v", rec.Code, fs.lineFilters)
	}
}
