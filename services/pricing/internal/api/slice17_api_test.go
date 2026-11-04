package api_test

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// FR-26.10: a campaign's name, validity, target group and budget change at the version; the
// new validity gates the next resolve; invalid terms and stale versions are refused.
func TestCampaign_Revise(t *testing.T) {
	h := router(newFake())
	id := createCampaign(t, h, "RETAIL")["id"].(string)
	activeP1Record(t, h, "9", "")
	activeP1Record(t, h, "8", id)
	if got := resolvedPrice(t, h, "RETAIL", "2026-03-15"); got != "8" {
		t.Fatalf("before the edit: price %s, want 8", got)
	}
	url := "/v1/pricing/campaigns/" + id + ":revise"
	code, out := call(t, h, http.MethodPost, url, campaignDuties,
		`{"version":1,"name":"Spring push II","validTo":"2026-03-10","budget":"7500.25","targetCustomerGroup":""}`, "Idempotency-Key", "rev-1")
	if code != http.StatusOK || out["name"] != "Spring push II" || out["validTo"] != "2026-03-10" || out["budget"] != "7500.25" ||
		out["targetCustomerGroup"] != "" || out["version"] != float64(2) || out["currency"] != "USD" {
		t.Fatalf("revise = %d %v", code, out)
	}
	if got := resolvedPrice(t, h, "RETAIL", "2026-03-15"); got != "9" {
		t.Fatalf("after the campaign was shortened: price %s, want the plain 9", got)
	}
	if got := resolvedPrice(t, h, "RETAIL", "2026-03-05"); got != "8" {
		t.Fatalf("inside the shortened validity: price %s, want 8", got)
	}
	if code, out := call(t, h, http.MethodPost, url, campaignDuties, `{"version":1,"name":"Stale"}`); code != http.StatusPreconditionFailed || out["code"] != "conflict_version" {
		t.Fatalf("stale revise = %d %v; want 412", code, out)
	}
	for name, body := range map[string]string{
		"nothing to change": `{"version":2}`,
		"no version":        `{"name":"x"}`,
		"bad date":          `{"version":2,"validFrom":"soon"}`,
		"bad budget":        `{"version":2,"budget":"lots"}`,
	} {
		if code, out := call(t, h, http.MethodPost, url, campaignDuties, body); code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d %v; want 422", name, code, out)
		}
	}
	code, out = call(t, h, http.MethodPost, url, campaignDuties, `{"version":2,"budget":"-1","validTo":"2026-02-01","name":""}`)
	errs, _ := out["errors"].(map[string]any)
	if code != http.StatusUnprocessableEntity || errs["budget"] == nil || errs["validity"] == nil || errs["name"] == nil {
		t.Fatalf("invalid terms = %d %v; want 422 naming budget, validity and name", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/campaigns/"+noCampaign+":revise", campaignDuties, `{"version":1,"name":"x"}`); code != http.StatusNotFound || out["code"] != "PRC-0041" {
		t.Fatalf("unknown campaign = %d %v; want 404 PRC-0041", code, out)
	}
	if code, out := call(t, h, http.MethodPost, url, "pricing.view", `{"version":2,"name":"x"}`); code != http.StatusForbidden {
		t.Fatalf("revise without the duty = %d %v; want 403", code, out)
	}
}

// BR-26.8 reversed: a CLOSED agreement reopens with a later last day; anything else is refused.
func TestRebateLifecycle_Reopen(t *testing.T) {
	h := router(newFake())
	id := newAgreementID(t, h)
	url := agreementsURL + "/" + id + ":reopen"
	if code, out := call(t, h, http.MethodPost, url, allDuties, `{"version":1,"validTo":"2027-03-31"}`); code != http.StatusConflict || out["code"] != "PRC-0031" {
		t.Fatalf("reopen an ACTIVE agreement = %d %v; want 409 PRC-0031", code, out)
	}
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":close", allDuties, `{"version":1,"closeDate":"2026-09-30"}`); code != http.StatusOK {
		t.Fatalf("close = %d %v", code, out)
	}
	for name, body := range map[string]string{
		"no version":      `{"validTo":"2027-03-31"}`,
		"no date":         `{"version":2}`,
		"bad date":        `{"version":2,"validTo":"later"}`,
		"not after close": `{"version":2,"validTo":"2026-09-30"}`,
	} {
		if code, out := call(t, h, http.MethodPost, url, allDuties, body); code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d %v; want 422", name, code, out)
		}
	}
	if code, out := call(t, h, http.MethodPost, url, allDuties, `{"version":1,"validTo":"2027-03-31"}`); code != http.StatusPreconditionFailed {
		t.Fatalf("stale reopen = %d %v; want 412", code, out)
	}
	if code, out := call(t, h, http.MethodPost, url, "pricing.rebate.view", `{"version":2,"validTo":"2027-03-31"}`); code != http.StatusForbidden {
		t.Fatalf("reopen without the duty = %d %v; want 403", code, out)
	}
	code, out := call(t, h, http.MethodPost, url, allDuties, `{"version":2,"validTo":"2027-03-31"}`)
	if code != http.StatusOK || out["status"] != "ACTIVE" || out["validTo"] != "2027-03-31" || out["version"] != float64(3) {
		t.Fatalf("reopen = %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":3,"name":"Back again"}`); code != http.StatusOK {
		t.Fatalf("revise after reopen = %d %v; want 200", code, out)
	}
	if code, _ := call(t, h, http.MethodPost, agreementsURL+"/not-a-uuid:reopen", allDuties, `{"version":1,"validTo":"2027-03-31"}`); code != http.StatusNotFound {
		t.Fatalf("reopen bad id = %d; want 404", code)
	}
}

// §5.26.14: the trade agreements export is CSV with a header, one row per matching record, the
// list's filters and the view duty.
func TestRecords_ExportCSV(t *testing.T) {
	h := router(newFake())
	id := createCampaign(t, h, "")["id"].(string)
	activeP1Record(t, h, "9", "")
	activeP1Record(t, h, "8.5", id)
	code, body, hdr := callRaw(t, h, http.MethodGet, "/v1/pricing/condition-records:export", "pricing.view")
	if code != http.StatusOK || !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") || !strings.Contains(hdr.Get("Content-Disposition"), "trade-agreements-") ||
		hdr.Get("X-Passage-Truncated") != "" {
		t.Fatalf("export = %d %q %v", code, body, hdr)
	}
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil || len(rows) != 3 {
		t.Fatalf("export rows = %v err %v; want a header and two records", rows, err)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	for _, name := range []string{"recordId", "type", "productKey", "amount", "validFrom", "validTo", "campaignId", "status", "version"} {
		if _, ok := col[name]; !ok {
			t.Fatalf("header %v lacks %s", rows[0], name)
		}
	}
	if rows[1][col["type"]] != "PRICE" || rows[1][col["productKey"]] != "P1" || rows[1][col["status"]] != "ACTIVE" {
		t.Fatalf("first row = %v", rows[1])
	}
	_, body, _ = callRaw(t, h, http.MethodGet, "/v1/pricing/condition-records:export?campaignId="+id, "pricing.view")
	if rows, _ := csv.NewReader(strings.NewReader(body)).ReadAll(); len(rows) != 2 || rows[1][col["campaignId"]] != id {
		t.Fatalf("campaign export = %v; want the campaign's record only", rows)
	}
	_, body, _ = callRaw(t, h, http.MethodGet, "/v1/pricing/condition-records:export?status=DRAFT", "pricing.view")
	if rows, _ := csv.NewReader(strings.NewReader(body)).ReadAll(); len(rows) != 1 {
		t.Fatalf("export of no records = %v; want the header only", rows)
	}
	if code, _, _ := callRaw(t, h, http.MethodGet, "/v1/pricing/condition-records:export?status=bogus", "pricing.view"); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad filter = %d; want 422", code)
	}
	if code, _, _ := callRaw(t, h, http.MethodGet, "/v1/pricing/condition-records:export", ""); code != http.StatusForbidden {
		t.Fatalf("export without the duty = %d; want 403", code)
	}
}

func exportEntry(id, customerKey, productKey, amount string) store.Entry {
	return store.Entry{LegalEntityID: "le-1", Record: domain.Record{ID: id, Version: 1, Status: domain.StatusActive, Type: domain.TypePrice,
		CustomerKind: domain.CustomerKindCustomer, CustomerKey: customerKey, ProductKind: domain.ProductKindProduct, ProductKey: productKey,
		Currency: "USD", Unit: "EA", Amount: decimal.RequireFromString(amount), DiscPct1: decimal.RequireFromString("-2.5"),
		ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ValidTo: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)}}
}

// Text a spreadsheet would run as a formula is neutralised with a leading apostrophe; numbers,
// negative ones included, are written as they are.
func TestRecords_ExportGuardsFormulas(t *testing.T) {
	f := newFake()
	f.records["r1"] = exportEntry("r1", "=HYPERLINK(\"http://evil\",\"x\")", "@SUM(A1)", "-4.25")
	f.records["r2"] = exportEntry("r2", "\t=cmd", "\r+1", "12.50")
	code, body, _ := callRaw(t, router(f), http.MethodGet, "/v1/pricing/condition-records:export", "pricing.view")
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if code != http.StatusOK || err != nil || len(rows) != 3 {
		t.Fatalf("export = %d %q err %v", code, body, err)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	want := [][2]string{
		{rows[1][col["customerKey"]], "'=HYPERLINK(\"http://evil\",\"x\")"}, {rows[1][col["productKey"]], "'@SUM(A1)"},
		{rows[1][col["amount"]], "-4.25"}, {rows[1][col["discPct1"]], "-2.5"},
		{rows[2][col["customerKey"]], "'\t=cmd"}, {rows[2][col["productKey"]], "'\r+1"}, {rows[2][col["amount"]], "12.5"},
	}
	for i, w := range want {
		if w[0] != w[1] {
			t.Fatalf("cell %d = %q; want %q (rows %q)", i, w[0], w[1], rows)
		}
	}
}

// More matching records than the cap: the file holds MaxExportRows rows and says so in
// X-Passage-Truncated; exactly the cap is not truncated.
func TestRecords_ExportTruncatesAtTheCap(t *testing.T) {
	f := newFake()
	for i := 0; i < store.MaxExportRows; i++ {
		id := fmt.Sprintf("r%06d", i)
		f.records[id] = exportEntry(id, "C1", "P1", "1")
	}
	h := router(f)
	code, body, hdr := callRaw(t, h, http.MethodGet, "/v1/pricing/condition-records:export", "pricing.view")
	if code != http.StatusOK || hdr.Get("X-Passage-Truncated") != "" || strings.Count(body, "\n") != store.MaxExportRows+1 {
		t.Fatalf("export of exactly the cap = %d, %d lines, truncated %q", code, strings.Count(body, "\n"), hdr.Get("X-Passage-Truncated"))
	}
	f.records["r999999"] = exportEntry("r999999", "C1", "P1", "1")
	code, body, hdr = callRaw(t, h, http.MethodGet, "/v1/pricing/condition-records:export", "pricing.view")
	if code != http.StatusOK || hdr.Get("X-Passage-Truncated") != "true" || strings.Count(body, "\n") != store.MaxExportRows+1 || strings.Contains(body, "r999999") {
		t.Fatalf("export over the cap = %d, %d lines, truncated %q", code, strings.Count(body, "\n"), hdr.Get("X-Passage-Truncated"))
	}
}

// A store failure before the first row is an ordinary problem response, not an empty CSV.
func TestRecords_ExportStoreFailure(t *testing.T) {
	f := newFake()
	f.exportErr = errors.New("database down")
	code, body, hdr := callRaw(t, router(f), http.MethodGet, "/v1/pricing/condition-records:export", "pricing.view")
	if code != http.StatusInternalServerError || strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") {
		t.Fatalf("export with a failing store = %d %q %v; want 500 problem", code, body, hdr)
	}
}
