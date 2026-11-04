package api

import (
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

func csvRecords(t *testing.T, body string) [][]string {
	t.Helper()
	body = strings.TrimPrefix(body, "\xEF\xBB\xBF")
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v: %s", err, body)
	}
	return recs
}

func TestGetAccount_ReturnsETagAndNotFound(t *testing.T) {
	fs := &fakeStore{accounts: []store.Account{{Number: "6010", Name: "Freight", LockVersion: 4}}}
	h := server(fs)
	rec := call(h, http.MethodGet, "/v1/ledger/charts/STD/accounts/6010", "", dutyRead, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` || decode(t, rec)["lockVersion"] != float64(4) || decode(t, rec)["chartCode"] != "STD" {
		t.Fatalf("status %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/charts/STD/accounts/9999", "", dutyAccountLookup, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing account: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/charts/std/accounts/6010", "", dutyRead, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("lowercase chart: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/charts/STD/accounts/6010", "", dutyAccountWrite, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("write duty must not read: %d", rec.Code)
	}
}

func TestExportAccounts_CSV(t *testing.T) {
	sus := "2026-06-01"
	fs := &fakeStore{accounts: []store.Account{
		{ChartCode: "STD", Number: "1000", Name: "Cash, bank", Type: "asset", Postable: true, ReconType: "bank"},
		{ChartCode: "STD", Number: "6010", Name: "=HYPERLINK(\"x\")", Type: "expense", SuspendedFrom: &sus, ReconType: "none"},
	}}
	h := server(fs)
	rec := call(h, http.MethodGet, "/v1/ledger/charts/STD/accounts?format=csv", "", dutyRead, nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "chart-STD-accounts.csv") || !strings.HasPrefix(rec.Body.String(), "\xEF\xBB\xBF") {
		t.Fatalf("status %d headers %v", rec.Code, rec.Header())
	}
	recs := csvRecords(t, rec.Body.String())
	if len(recs) != 3 || recs[1][2] != "Cash, bank" || recs[1][4] != "true" || recs[2][2] != `'=HYPERLINK("x")` || recs[2][5] != sus || rec.Header().Get(headerExportTruncated) != "" {
		t.Fatalf("records: %v", recs)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/charts/STD/accounts?format=xml", "", dutyRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad format: %d", rec.Code)
	}
}

func TestExportTrialBalance_CSV(t *testing.T) {
	fs := &fakeStore{trial: store.TrialBalance{
		LedgerID: testLedger, Currency: "USD", GroupBy: []string{"DEPT"},
		Rows: []store.BalanceRow{
			{Account: "1000", Name: "Cash", Debit: "100.00", Credit: "40.00", Net: "60.00", LineCount: 3, Dimensions: map[string]string{"DEPT": "D100"},
				Quantities: []store.QuantityTotal{{Unit: "KG", Net: "2.500000"}, {Unit: "PC", Net: "-1.000000"}}},
			{Account: "4000", Name: "Sales", Debit: "0.00", Credit: "60.00", Net: "-60.00", LineCount: 1},
		},
		Debit: "100.00", Credit: "100.00", Net: "0.00", Balanced: true,
	}}
	h := server(fs)
	rec := call(h, http.MethodGet, "/v1/ledger/balances?ledger="+testLedger+"&group_by=DEPT&format=csv", "", dutyBalanceRead, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/csv; charset=utf-8" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	recs := csvRecords(t, rec.Body.String())
	want := []string{"account", "name", "dim.DEPT", "debit", "credit", "net", "line_count", "quantity_net_by_unit"}
	if len(recs) != 4 || strings.Join(recs[0], ",") != strings.Join(want, ",") {
		t.Fatalf("records: %v", recs)
	}
	if strings.Join(recs[1], "|") != "1000|Cash|D100|100.00|40.00|60.00|3|KG=2.500000;PC=-1.000000" ||
		strings.Join(recs[2], "|") != "4000|Sales||0.00|60.00|-60.00|1|" || strings.Join(recs[3], "|") != "TOTAL|||100.00|100.00|0.00||" {
		t.Fatalf("records: %v", recs)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/balances?ledger="+testLedger+"&format=pdf", "", dutyBalanceRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad format: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/balances?format=csv", "", dutyBalanceRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing ledger must still be refused: %d", rec.Code)
	}
}

func TestCSVSafe_TableDriven(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"Cash", "Cash"},
		{"=1+1", "'=1+1"},
		{"+SUM(A1)", "'+SUM(A1)"},
		{"-A1", "'-A1"},
		{"-cmd|' /C calc'!A0", "'-cmd|' /C calc'!A0"},
		{"@SUM(A1)", "'@SUM(A1)"},
		{"\t=1", "'\t=1"},
		{"\r=1", "'\r=1"},
		{"-60.00", "-60.00"},
		{"-1", "-1"},
		{"12.500000", "12.500000"},
		{"2026-03-01", "2026-03-01"},
		{"-", "'-"},
		{"--1", "'--1"},
	}
	for _, tc := range tests {
		if got := csvSafe(tc.in); got != tc.want {
			t.Errorf("csvSafe(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExportLines_CSVPagesAndDimensionColumns(t *testing.T) {
	fs := &fakeStore{linePlan: store.LineExportPlan{DimensionAttributes: []string{"DEPT", "CC", "PROJ"}}, linePages: []store.LinePage{
		{Items: []store.JournalLine{{ID: "l1", EntryID: "e1", Voucher: "JV-1", PostingSequence: 7, PostingDate: "2026-03-01", LineNumber: 1, Account: "1000", DisplayName: "Cash",
			DebitCredit: "D", Amount: "10.00", AmountAcct: "10.00", Currency: "USD", Text: "-cmd", Dimensions: map[string]string{"DEPT": "D100"}}},
			Next: &store.LineCursor{PostingSequence: 7, LineNumber: 1}},
		{Items: []store.JournalLine{{ID: "l2", EntryID: "e1", Voucher: "JV-1", PostingSequence: 7, PostingDate: "2026-03-01", LineNumber: 2, Account: "4000",
			DebitCredit: "C", Amount: "-10.00", AmountAcct: "-10.00", Currency: "USD", Quantity: "-3.000000", Unit: "PC", NegativePosting: true, Dimensions: map[string]string{"CC": "C1"}}}},
	}}
	h := server(fs)
	rec := call(h, http.MethodGet, linesPath+"?ledger="+testLedger+"&format=csv&limit=5&dim.DEPT=D100", "", dutyRead, nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "\xEF\xBB\xBF") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	recs := csvRecords(t, rec.Body.String())
	if len(recs) != 3 || len(fs.lineFilters) != 2 || fs.lineFilters[0].Limit != store.MaxPageSize || fs.lineFilters[1].After == nil {
		t.Fatalf("records %d, store calls %+v", len(recs), fs.lineFilters)
	}
	// The plan sees the same filter as the pages: the dimension filter and the ledger, so its legal-entity
	// check and its cut are those of the JSON read.
	if fs.planFilter == nil || fs.planFilter.LedgerID != testLedger || fs.planFilter.Dimensions["DEPT"] != "D100" || fs.lineFilters[0].Dimensions["DEPT"] != "D100" {
		t.Fatalf("plan filter %+v, page filter %+v", fs.planFilter, fs.lineFilters[0])
	}
	head := recs[0]
	if strings.Join(head[len(head)-3:], ",") != "dim.CC,dim.DEPT,dim.PROJ" {
		t.Fatalf("dimension columns are the ledger's attributes, sorted: %v", head)
	}
	cc, dept := len(head)-3, len(head)-2
	if recs[1][12] != "'-cmd" || recs[1][dept] != "D100" || recs[1][cc] != "" {
		t.Fatalf("row 1: %v", recs[1])
	}
	if recs[2][7] != "-10.00" || recs[2][9] != "-10.00" || recs[2][10] != "-3.000000" || recs[2][18] != "true" || recs[2][cc] != "C1" {
		t.Fatalf("negative amounts must stay numbers: %v", recs[2])
	}
	if rec.Header().Get(headerExportTruncated) != "" {
		t.Fatalf("complete export flagged truncated")
	}
}

func TestExportLines_TruncatesAtThePlannedBoundary(t *testing.T) {
	items := make([]store.JournalLine, store.MaxPageSize)
	for i := range items {
		items[i] = store.JournalLine{PostingSequence: int64(i/10 + 1), LineNumber: i%10 + 1}
	}
	last := items[len(items)-1]
	// Every page repeats, so the store would go on for ever: the export must stop at the cap.
	boundary := &store.LineCursor{PostingSequence: 99, LineNumber: 4}
	fs := &fakeStore{linePlan: store.LineExportPlan{Boundary: boundary},
		linePage: store.LinePage{Items: items, Next: &store.LineCursor{PostingSequence: last.PostingSequence, LineNumber: last.LineNumber}}}
	rec := call(server(fs), http.MethodGet, linesPath+"?ledger="+testLedger+"&format=csv", "", dutyRead, nil)
	if rec.Code != http.StatusOK || rec.Header().Get(headerExportTruncated) != "true" || rec.Header().Get(headerExportNext) != "99.4" {
		t.Fatalf("status %d headers %v", rec.Code, rec.Header())
	}
	// The boundary line (99.4) is the 984th of each page; the first page reaches it.
	if got := len(csvRecords(t, rec.Body.String())) - 1; got != 984 {
		t.Fatalf("rows %d, want the lines up to the boundary (984)", got)
	}
	fs.linePlan = store.LineExportPlan{}
	rec = call(server(fs), http.MethodGet, linesPath+"?ledger="+testLedger+"&format=csv", "", dutyRead, nil)
	if got := len(csvRecords(t, rec.Body.String())) - 1; got != maxExportRows || rec.Header().Get(headerExportTruncated) != "" {
		t.Fatalf("rows %d, want the hard cap %d when lines arrive after the plan", got, maxExportRows)
	}
}

func TestExportLines_RefusedBeforeTheFirstByte(t *testing.T) {
	fs := &fakeStore{planErr: httpx.NewProblem(http.StatusNotFound, "LEDGER_NOT_FOUND", "ledger not found")}
	rec := call(server(fs), http.MethodGet, linesPath+"?ledger="+testLedger+"&format=csv", "", dutyRead, nil)
	if rec.Code != http.StatusNotFound || strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") || len(fs.lineFilters) != 0 {
		t.Fatalf("a ledger outside the caller's scope must be a 404 problem, got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := call(server(&fakeStore{}), http.MethodGet, linesPath+"?ledger=nope&format=csv", "", dutyRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("validation runs before an export: %d", rec.Code)
	}
	if rec := call(server(&fakeStore{}), http.MethodGet, linesPath+"?ledger="+testLedger+"&format=csv", "", dutyAccountWrite, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("an export needs the read duty: %d", rec.Code)
	}
}

func TestExportLines_FailureMidStreamCutsTheConnection(t *testing.T) {
	fs := &fakeStore{err: errors.New("connection reset")}
	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Fatalf("want the ErrAbortHandler panic that cuts the connection, got %v", p)
		}
	}()
	call(server(fs), http.MethodGet, linesPath+"?ledger="+testLedger+"&format=csv", "", dutyRead, nil)
	t.Fatal("a failed page must not end as a short file that looks complete")
}

func TestExportAccounts_TruncatesAtTheRowCap(t *testing.T) {
	page := make([]store.Account, maxAccountPage)
	for i := range page {
		page[i] = store.Account{ChartCode: "STD", Number: "A" + strconv.Itoa(i), Name: "-" + strconv.Itoa(i)}
	}
	// The fake answers every page with the same full page, so the chart never ends.
	rec := call(server(&fakeStore{accounts: page}), http.MethodGet, "/v1/ledger/charts/STD/accounts?format=csv", "", dutyRead, nil)
	recs := csvRecords(t, rec.Body.String())
	if rec.Code != http.StatusOK || len(recs)-1 != maxExportRows || rec.Header().Get(headerExportTruncated) != "true" {
		t.Fatalf("status %d rows %d headers %v", rec.Code, len(recs)-1, rec.Header())
	}
	if want := recs[len(recs)-1][1]; rec.Header().Get(headerExportNext) != want {
		t.Fatalf("next cursor %q, want the last exported number %q", rec.Header().Get(headerExportNext), want)
	}
	if recs[2][2] != "-1" {
		t.Fatalf("a numeric name stays as is: %q", recs[2][2])
	}
}
