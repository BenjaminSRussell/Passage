package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

// slice5 fakes the cost transaction store. Balances are what the test sets;
// the fake records the query the API built, which is what the API decides
// (ledger resolution, as-of, site). Summing is the store's, tested against
// Postgres in store/costtxn_test.go.
type slice5 struct {
	costTxns     map[string]store.CostTransaction
	recorded     [][]store.CostTransaction
	balances     []domain.Balance
	balanceQuery store.BalanceQuery
	txnFilter    store.CostTxnFilter
	txnAfter     string
	txnPage      store.CostTxnPage
}

func (f *slice5) RecordCostTransactions(_ context.Context, in []store.CostTransaction) (int, error) {
	if f.costTxns == nil {
		f.costTxns = map[string]store.CostTransaction{}
	}
	f.recorded = append(f.recorded, in)
	n := 0
	for _, c := range in {
		if _, ok := f.costTxns[c.SourceKey]; !ok {
			f.costTxns[c.SourceKey] = c
			n++
		}
	}
	return n, nil
}

func (f *slice5) CostTransactionRecorded(_ context.Context, sourceKey string) (bool, error) {
	_, ok := f.costTxns[sourceKey]
	return ok, nil
}

func (f *slice5) ValueBalances(_ context.Context, q store.BalanceQuery) ([]domain.Balance, error) {
	f.balanceQuery = q
	return f.balances, nil
}

func (f *slice5) ListCostTransactions(_ context.Context, flt store.CostTxnFilter, after string, _ int) (store.CostTxnPage, error) {
	f.txnFilter, f.txnAfter = flt, after
	return f.txnPage, nil
}

const reportDuties = allDuties + "," + dutyCostTxnPost

func mustDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func costItem(le, key, kind, qty, amount string) string {
	return `{"sourceKey":"` + key + `","legalEntityId":"` + le + `","productId":"22222222-2222-4222-8222-222222222222",` +
		`"siteId":"44444444-4444-4444-8444-444444444444","kind":"` + kind + `","qty":"` + qty + `","amount":"` + amount +
		`","currency":"EUR","financialDate":"2026-03-31"}`
}

func TestCostTransactions_RecordCountsOnlyNewSourceKeys(t *testing.T) {
	h, f := server()
	le := testLE
	body := `{"items":[` + costItem(le, "gr-1", "FINANCIAL", "100", "1000.00") + `,` + costItem(le, "ppv-1", "PPV", "100", "50.00") + `]}`
	rec := call(h, http.MethodPost, "/internal/v1/costing/cost-transactions", body, reportDuties, nil)
	m, _ := decodeMap(rec)
	if rec.Code != http.StatusCreated || m["received"] != float64(2) || m["inserted"] != float64(2) {
		t.Fatalf("record status %d body %v", rec.Code, m)
	}
	if got := f.recorded[0][0]; got.Qty != "100" || got.Amount != "1000" || got.LegalEntityID != le {
		t.Errorf("store row = %+v, want normalised decimals", got)
	}
	rec = call(h, http.MethodPost, "/internal/v1/costing/cost-transactions", body, reportDuties, nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusCreated || m["inserted"] != float64(0) {
		t.Errorf("replay status %d body %v, want inserted 0", rec.Code, m)
	}
	if rec := call(h, http.MethodPost, "/internal/v1/costing/cost-transactions", body, allDuties, nil); rec.Code != http.StatusForbidden {
		t.Errorf("without %s: status %d, want 403", dutyCostTxnPost, rec.Code)
	}
	scoped := map[string]string{"X-Passage-Legal-Entity": ids.New()}
	if rec := call(h, http.MethodPost, "/internal/v1/costing/cost-transactions", body, reportDuties, scoped); rec.Code != http.StatusForbidden {
		t.Errorf("another legal entity's rows: status %d, want 403", rec.Code)
	}
	if got := len(f.recorded); got != 2 {
		t.Errorf("store called %d times, want 2 (refused calls never reach it)", got)
	}
}

func TestCostTransactions_ValidatesEachItem(t *testing.T) {
	h, _ := server()
	le := testLE
	tests := []struct {
		name, items, field string
	}{
		{"legal entity must be a UUID", strings.Replace(costItem(le, "a", "FINANCIAL", "1", "1"), le, "nope", 1), "items[0].legalEntityId"},
		{"unknown kind", costItem(le, "a", "RECEIPT", "1", "1"), "items[0].kind"},
		{"value-only kind carries no quantity", costItem(le, "a", "REVALUATION", "5", "1"), "items[0].qty"},
		{"amount finer than numeric(19,4)", costItem(le, "a", "FINANCIAL", "1", "1.00005"), "items[0].amount"},
		{"amount too large", costItem(le, "a", "FINANCIAL", "1", "1000000000000000"), "items[0].amount"},
		{"quantity not a decimal", costItem(le, "a", "FINANCIAL", "x", "1"), "items[0].qty"},
		{"bad date", strings.Replace(costItem(le, "a", "FINANCIAL", "1", "1"), "2026-03-31", "yesterday", 1), "items[0].financialDate"},
		{"optional ids are UUIDs", strings.Replace(costItem(le, "a", "FINANCIAL", "1", "1"), `"kind"`, `"warehouseId":"w1","kind"`, 1), "items[0].warehouseId"},
		{"repeated source key", costItem(le, "a", "FINANCIAL", "1", "1") + `,` + costItem(le, "a", "FINANCIAL", "1", "1"), "items[1].sourceKey"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(h, http.MethodPost, "/internal/v1/costing/cost-transactions", `{"items":[`+tc.items+`]}`, reportDuties, nil)
			m, _ := decodeMap(rec)
			errs, _ := m["errors"].(map[string]any)
			if _, ok := errs[tc.field]; rec.Code != http.StatusBadRequest || !ok {
				t.Fatalf("status %d errors %v, want 400 naming %s", rec.Code, errs, tc.field)
			}
		})
	}
	if rec := call(h, http.MethodPost, "/internal/v1/costing/cost-transactions", `{"items":[]}`, reportDuties, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("empty batch status %d, want 400", rec.Code)
	}
}

const (
	repLE  = "11111111-1111-4111-8111-111111111111"
	repP1  = "22222222-2222-4222-8222-222222222222"
	repS1  = "44444444-4444-4444-8444-444444444444"
	repS2  = "55555555-5555-4555-8555-555555555555"
	ifrsID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	gaapID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

// reportFixture: the AC-20.12 split valuation at s1 (DOMESTIC 1,000.00, IMPORT
// 700.00) plus 10 units received but not invoiced at s2 (physical 50.00).
func reportFixture(f *fakeStore) {
	f.balances = []domain.Balance{
		{ProductID: repP1, SiteID: repS1, ValuationType: "DOMESTIC", Currency: "EUR", Qty: mustDec("100"), PhysicalValue: decimal.Zero, FinancialValue: mustDec("1000")},
		{ProductID: repP1, SiteID: repS1, ValuationType: "IMPORT", Currency: "EUR", Qty: mustDec("50"), PhysicalValue: decimal.Zero, FinancialValue: mustDec("700")},
		{ProductID: repP1, SiteID: repS2, Currency: "EUR", Qty: mustDec("10"), PhysicalValue: mustDec("50"), FinancialValue: decimal.Zero},
	}
	f.ledgers = []store.ValuationLedger{
		{ID: ifrsID, LegalEntityID: repLE, Code: "IFRS", Method: "WEIGHTED_AVG_PERIODIC", Currency: "EUR", Primary: true},
		{ID: gaapID, LegalEntityID: repLE, Code: "USGAAP", Method: "FIFO", Currency: "USD"},
	}
}

func rows(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, _ := m["rows"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestValueReport_GroupsSplitValuationAndFormats(t *testing.T) {
	h, f := server()
	reportFixture(f)
	rec := call(h, http.MethodGet, "/v1/costing/value-report?legalEntityId="+repLE+"&asOf=2026-03-31&groupBy=valuationType", "", allDuties, nil)
	m, _ := decodeMap(rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %v", rec.Code, m)
	}
	total, _ := m["total"].(map[string]any)
	if m["currency"] != "EUR" || m["valuationLedgerCode"] != "IFRS" || total["value"] != "1750.00" || total["physicalValue"] != "50.00" ||
		total["financialValue"] != "1700.00" || total["qty"] != "160" {
		t.Fatalf("header and total = %v / %v", m, total)
	}
	got := map[string]string{}
	for _, r := range rows(t, m) {
		got[r["key"].(map[string]any)["valuationType"].(string)] = r["value"].(string)
	}
	if got["DOMESTIC"] != "1000.00" || got["IMPORT"] != "700.00" || got[""] != "50.00" {
		t.Errorf("AC-20.12 by valuation type = %v, want DOMESTIC 1000.00 and IMPORT 700.00", got)
	}
	if f.balanceQuery.ValuationLedgerID != "" || f.balanceQuery.AsOf != "2026-03-31" || f.balanceQuery.LegalEntityID != repLE {
		t.Errorf("store query = %+v, want the primary valuation as of 2026-03-31", f.balanceQuery)
	}
	// Default grouping is by site; an empty groupBy is one total row.
	for path, want := range map[string]int{"": 2, "&groupBy=": 1} {
		rec := call(h, http.MethodGet, "/v1/costing/value-report?legalEntityId="+repLE+"&asOf=2026-03-31"+path, "", allDuties, nil)
		m, _ := decodeMap(rec)
		if n := len(rows(t, m)); rec.Code != http.StatusOK || n != want {
			t.Errorf("groupBy %q: status %d rows %d, want %d", path, rec.Code, n, want)
		}
	}
}

func TestValueReport_SelectsTheValuationLedger(t *testing.T) {
	h, f := server()
	reportFixture(f)
	base := "/v1/costing/value-report?legalEntityId=" + repLE + "&asOf=2026-03-31&siteId=" + repS1 + "&valuationLedgerId="
	// The primary ledger's rows are the default valuation.
	rec := call(h, http.MethodGet, base+ifrsID, "", allDuties, nil)
	m, _ := decodeMap(rec)
	if rec.Code != http.StatusOK || f.balanceQuery.ValuationLedgerID != "" || f.balanceQuery.SiteID != repS1 || m["valuationLedgerId"] != ifrsID {
		t.Errorf("primary ledger: status %d query %+v body %v", rec.Code, f.balanceQuery, m)
	}
	// A group ledger in USD holding EUR balances is refused, never mixed.
	rec = call(h, http.MethodGet, base+gaapID, "", allDuties, nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusUnprocessableEntity || problemCode(t, m) != domain.CodeReportCurrency || f.balanceQuery.ValuationLedgerID != gaapID {
		t.Errorf("group ledger with EUR balances: status %d code %q query %+v", rec.Code, problemCode(t, m), f.balanceQuery)
	}
	f.balances = nil
	rec = call(h, http.MethodGet, base+gaapID, "", allDuties, nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusOK || m["currency"] != "USD" || len(rows(t, m)) != 0 {
		t.Errorf("empty group ledger: status %d body %v, want USD and no rows", rec.Code, m)
	}
	for name, id := range map[string]string{"unknown ledger": ids.New()} {
		rec := call(h, http.MethodGet, base+id, "", allDuties, nil)
		if m, _ := decodeMap(rec); rec.Code != http.StatusNotFound || problemCode(t, m) != store.CodeLedgerNotFound {
			t.Errorf("%s: status %d code %q", name, rec.Code, problemCode(t, m))
		}
	}
	otherLE := ids.New()
	other := "/v1/costing/value-report?legalEntityId=" + otherLE + "&asOf=2026-03-31&valuationLedgerId=" + gaapID
	if rec := call(h, http.MethodGet, other, "", allDuties, scopedTo(otherLE)); rec.Code != http.StatusNotFound {
		t.Errorf("ledger of another legal entity: status %d, want 404", rec.Code)
	}
}

func TestValueReport_Refusals(t *testing.T) {
	h, f := server()
	reportFixture(f)
	tests := []struct {
		name, query string
		status      int
		code        string
	}{
		{"missing asOf", "legalEntityId=" + repLE, http.StatusBadRequest, "COM-0001"},
		{"bad legal entity", "legalEntityId=x&asOf=2026-03-31", http.StatusBadRequest, "COM-0001"},
		{"unknown grouping", "legalEntityId=" + repLE + "&asOf=2026-03-31&groupBy=colour", http.StatusUnprocessableEntity, domain.CodeReportGroupBy},
		{"ledger is not a grouping", "legalEntityId=" + repLE + "&asOf=2026-03-31&groupBy=valuationLedger", http.StatusUnprocessableEntity, domain.CodeReportGroupBy},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(h, http.MethodGet, "/v1/costing/value-report?"+tc.query, "", allDuties, nil)
			if m, _ := decodeMap(rec); rec.Code != tc.status || problemCode(t, m) != tc.code {
				t.Fatalf("status %d code %q, want %d %s", rec.Code, problemCode(t, m), tc.status, tc.code)
			}
		})
	}
	scoped := map[string]string{"X-Passage-Legal-Entity": ids.New()}
	if rec := call(h, http.MethodGet, "/v1/costing/value-report?legalEntityId="+repLE+"&asOf=2026-03-31", "", allDuties, scoped); rec.Code != http.StatusForbidden {
		t.Errorf("out of scope: status %d, want 403", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/value-report?legalEntityId="+repLE+"&asOf=2026-03-31", "", "costing.version.write", nil); rec.Code != http.StatusForbidden {
		t.Errorf("without costing.read: status %d, want 403", rec.Code)
	}
}

func TestValueReport_ShowsSubCentAmountsInFull(t *testing.T) {
	h, f := server()
	f.balances = []domain.Balance{{ProductID: repP1, SiteID: repS1, Currency: "EUR", Qty: mustDec("0"), PhysicalValue: decimal.Zero, FinancialValue: mustDec("0.005")}}
	rec := call(h, http.MethodGet, "/v1/costing/value-report?legalEntityId="+repLE+"&asOf=2026-03-31", "", allDuties, nil)
	m, _ := decodeMap(rec)
	if total, _ := m["total"].(map[string]any); total["value"] != "0.005" {
		t.Errorf("residual value = %v, want 0.005 (not rounded away)", total["value"])
	}
}

func TestValueReport_TieOut(t *testing.T) {
	h, f := server()
	reportFixture(f)
	tie := func(gl string) (int, map[string]any) {
		body := `{"legalEntityId":"` + repLE + `","asOf":"2026-03-31","groupBy":["site"],"glBalances":[` + gl + `]}`
		rec := call(h, http.MethodPost, "/v1/costing/value-report:tie-out", body, allDuties, nil)
		m, _ := decodeMap(rec)
		return rec.Code, m
	}
	site := func(id, amount string) string {
		return `{"dimensions":{"site":"` + id + `"},"balance":"` + amount + `"}`
	}
	// The GL balance is the whole inventory value, physical included.
	status, m := tie(site(repS1, "1700") + "," + site(strings.ToUpper(repS2), "50"))
	if status != http.StatusOK || m["ties"] != true || m["differences"] != float64(0) || m["totalDiff"] != "0.00" {
		t.Errorf("exact ledger should tie: %d %v", status, m)
	}
	status, m = tie(site(repS1, "1650") + "," + site(repS2, "50") + "," + site(ids.New(), "12.34"))
	if status != http.StatusOK || m["ties"] != false || m["differences"] != float64(2) || m["totalDiff"] != "37.66" {
		t.Errorf("a 50.00 shortfall and an unexplained ledger site: %d %v", status, m)
	}
	if status, m := tie(`{"dimensions":{"product":"` + repP1 + `"},"balance":"1"}`); status != http.StatusUnprocessableEntity || problemCode(t, m) != domain.CodeTieOutDimension {
		t.Errorf("ledger keyed by another dimension: %d %q", status, problemCode(t, m))
	}
	if status, m := tie(site(repS1, "1") + "," + site(repS1, "2")); status != http.StatusUnprocessableEntity || problemCode(t, m) != domain.CodeTieOutDuplicate {
		t.Errorf("duplicate ledger key: %d %q", status, problemCode(t, m))
	}
	if status, m := tie(site(repS1, "1.00001")); status != http.StatusBadRequest {
		t.Errorf("balance finer than the stored scale: %d %v", status, m)
	}
	if status, _ := tie(""); status != http.StatusBadRequest {
		t.Errorf("no ledger balances: %d, want 400", status)
	}
}

func TestCostTransactions_ListIsTheDrillDown(t *testing.T) {
	h, f := server()
	reportFixture(f)
	f.txnPage = store.CostTxnPage{Items: []store.CostTransaction{{ID: ids.New(), SourceKey: "gr-1", Kind: "FINANCIAL"}}, Next: "2026-03-01|" + ids.New()}
	path := "/v1/costing/cost-transactions?legalEntityId=" + repLE + "&productId=" + repP1 + "&siteId=" + repS1 + "&kind=FINANCIAL&asOf=2026-03-31&valuationLedgerId=" + gaapID
	rec := call(h, http.MethodGet, path, "", allDuties, nil)
	m, _ := decodeMap(rec)
	if items, _ := m["items"].([]any); rec.Code != http.StatusOK || len(items) != 1 || m["nextCursor"] == "" {
		t.Fatalf("list status %d body %v", rec.Code, m)
	}
	want := store.CostTxnFilter{LegalEntityID: repLE, ValuationLedgerID: gaapID, ProductID: repP1, SiteID: repS1, Kind: "FINANCIAL", AsOf: "2026-03-31"}
	if f.txnFilter != want {
		t.Errorf("filter = %+v, want %+v", f.txnFilter, want)
	}
	next := m["nextCursor"].(string)
	if rec := call(h, http.MethodGet, "/v1/costing/cost-transactions?legalEntityId="+repLE+"&cursor="+next, "", allDuties, nil); rec.Code != http.StatusOK || f.txnAfter != f.txnPage.Next || f.txnFilter.ValuationLedgerID != "" {
		t.Errorf("next page: status %d after %q filter %+v", rec.Code, f.txnAfter, f.txnFilter)
	}
	badCursor := base64.RawURLEncoding.EncodeToString([]byte("yesterday|" + ids.New()))
	for name, q := range map[string]string{
		"missing legal entity":   "kind=FINANCIAL",
		"unknown kind":           "legalEntityId=" + repLE + "&kind=RECEIPT",
		"cursor with a bad date": "legalEntityId=" + repLE + "&cursor=" + badCursor,
	} {
		if rec := call(h, http.MethodGet, "/v1/costing/cost-transactions?"+q, "", allDuties, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
}
