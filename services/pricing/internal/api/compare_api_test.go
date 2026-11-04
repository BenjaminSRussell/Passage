package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/api"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const compareRecords = `[
	{"id":"price","type":"PRICE","customerKind":"all","productKind":"all","currency":"USD","amount":"100.00","validFrom":"2026-01-01","validTo":"2026-12-31"},
	{"id":"line8","type":"LINE_DISC","customerKind":"all","productKind":"all","currency":"USD","amount":"0","discPct1":"8","validFrom":"2026-01-01","validTo":"2026-12-31"},
	{"id":"multi12","type":"MULTILINE_DISC","customerKind":"all","productKind":"all","currency":"USD","amount":"0","discPct1":"12","validFrom":"2026-01-01","validTo":"2026-12-31"}]`

const compareBody = `{"legalEntityId":"%s","customerId":"C1","currency":"USD","pricingDate":"2026-06-01",
	"lines":[{"productId":"P1","productGroupId":"G1","unit":"EA","quantity":"1"}],
	"procedureIdA":"%s","procedureIdB":"%s","records":%s}`

const (
	stackedProcedure = `{"legalEntityId":"%s","name":"AX_STANDARD","steps":[{"stepNo":10,"conditionType":"PRICE"},{"stepNo":20,"conditionType":"LINE_DISC"},{"stepNo":30,"conditionType":"MULTILINE_DISC"}]}`
	bestProcedure    = `{"legalEntityId":"%s","name":"AX_STANDARD","steps":[{"stepNo":10,"conditionType":"PRICE"},{"stepNo":20,"conditionType":"LINE_DISC","exclusionGroup":"DISC","exclusionMode":"BEST"},{"stepNo":30,"conditionType":"MULTILINE_DISC","exclusionGroup":"DISC","exclusionMode":"BEST"}]}`
)

func TestCompare_TwoVersionsOverHTTP(t *testing.T) {
	h := router(newFake())
	_, v1 := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, fmt.Sprintf(stackedProcedure, legalEntity))
	_, v2 := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, fmt.Sprintf(bestProcedure, legalEntity))
	a, b := v1["id"].(string), v2["id"].(string)

	code, out := call(t, h, http.MethodPost, "/v1/pricing/compare", "pricing.view", fmt.Sprintf(compareBody, legalEntity, a, b, compareRecords))
	if code != http.StatusOK {
		t.Fatalf("compare = %d %v", code, out)
	}
	cmp := out["comparison"].(map[string]any)
	line := cmp["lines"].([]any)[0].(map[string]any)
	if cmp["identical"] != false || cmp["changedLines"] != float64(1) || cmp["orderNetDelta"] != "7.04" ||
		line["lineNetA"] != "80.96" || line["lineNetB"] != "88" || line["changed"] != true {
		t.Fatalf("comparison = %v; want 80.96 -> 88, order net delta 7.04", cmp)
	}
	pa, pb := out["a"].(map[string]any)["procedure"].(map[string]any), out["b"].(map[string]any)["procedure"].(map[string]any)
	if pa["version"] != float64(1) || pb["version"] != float64(2) || out["a"].(map[string]any)["explainRef"] == out["b"].(map[string]any)["explainRef"] {
		t.Fatalf("each side must name its own version and explain reference: %v %v", pa, pb)
	}
	if out["a"].(map[string]any)["lines"].([]any)[0].(map[string]any)["trace"] == nil {
		t.Fatal("explain is on by default")
	}
	if _, out := call(t, h, http.MethodPost, "/v1/pricing/compare?explain=false", "pricing.view", fmt.Sprintf(compareBody, legalEntity, a, b, compareRecords)); out["a"].(map[string]any)["lines"].([]any)[0].(map[string]any)["trace"] != nil {
		t.Fatal("explain=false must leave the trace out")
	}
}

func TestCompare_StoredRecordsWhenNoneInline(t *testing.T) {
	fs := newFake()
	h := router(fs)
	_, v1 := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, fmt.Sprintf(stackedProcedure, legalEntity))
	_, v2 := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, fmt.Sprintf(bestProcedure, legalEntity))
	_, created := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(recordBody, legalEntity, "95.00"))
	call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+created["id"].(string)+":activate", allDuties, `{"version":1}`)
	body := fmt.Sprintf(`{"legalEntityId":"%s","customerId":"C1","currency":"USD","pricingDate":"2026-06-01","lines":[{"productId":"P1","unit":"EA","quantity":"1"}],"procedureIdA":"%s","procedureIdB":"%s"}`,
		legalEntity, v1["id"], v2["id"])
	code, out := call(t, h, http.MethodPost, "/v1/pricing/compare", "pricing.view", body)
	if code != http.StatusOK || out["comparison"].(map[string]any)["identical"] != true || out["a"].(map[string]any)["orderNet"] != "95" {
		t.Fatalf("stored active record priced under both versions = %d %v; want identical at 95", code, out)
	}
}

func TestCompare_ValidationNotFoundAndDuty(t *testing.T) {
	h := router(newFake())
	_, v1 := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, fmt.Sprintf(stackedProcedure, legalEntity))
	a := v1["id"].(string)
	other := "00000000-0000-0000-0001-0000000000ff"
	_, v2 := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, fmt.Sprintf(stackedProcedure, other))

	code, out := call(t, h, http.MethodPost, "/v1/pricing/compare", "pricing.view", fmt.Sprintf(compareBody, legalEntity, a, a, compareRecords))
	if errs, _ := out["errors"].(map[string]any); code != http.StatusUnprocessableEntity || errs["procedureIdB"] == nil {
		t.Fatalf("same version twice = %d %v; want 422 on procedureIdB", code, out)
	}
	code, out = call(t, h, http.MethodPost, "/v1/pricing/compare", "pricing.view", fmt.Sprintf(compareBody, "x", "y", "", compareRecords))
	if errs, _ := out["errors"].(map[string]any); code != http.StatusUnprocessableEntity || errs["legalEntityId"] == nil || errs["procedureIdA"] == nil || errs["procedureIdB"] == nil {
		t.Fatalf("malformed ids = %d %v; want 422 on all three", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/compare", "pricing.view", fmt.Sprintf(compareBody, legalEntity, a, noCampaign, compareRecords)); code != http.StatusNotFound || out["code"] != "PRC-0042" {
		t.Fatalf("unknown version = %d %v; want 404 PRC-0042", code, out)
	}
	code, out = call(t, h, http.MethodPost, "/v1/pricing/compare", "pricing.view", fmt.Sprintf(compareBody, legalEntity, a, v2["id"], compareRecords))
	if errs, _ := out["errors"].(map[string]any); code != http.StatusUnprocessableEntity || errs["procedureIdB"] == nil {
		t.Fatalf("version of another legal entity = %d %v; want 422", code, out)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/compare", "pricing.maintain", fmt.Sprintf(compareBody, legalEntity, a, a, compareRecords)); code != http.StatusForbidden {
		t.Fatalf("without pricing.view = %d; want 403", code)
	}
}

func TestPriceList_CustomerGroupAndAllScopes(t *testing.T) {
	h := router(newFake())
	mk := func(kind, key, amount string) {
		body := fmt.Sprintf(`{"legalEntityId":"%s","type":"PRICE","customerKind":"%s","customerKey":"%s","productKind":"product","productKey":"P1","currency":"USD","fromQuantity":"0","amount":"%s","validFrom":"2026-01-01","validTo":"2026-12-31"}`,
			legalEntity, kind, key, amount)
		_, c := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, body)
		call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+c["id"].(string)+":activate", allDuties, `{"version":1}`)
	}
	mk("all", "", "100")
	mk("customer_group", "PG-A", "95")
	mk("customer", "C1", "90")
	mk("customer", "C2", "50")
	url := "/v1/pricing/price-list?legalEntityId=" + legalEntity + "&customerId=C1&customerGroupId=PG-A&currency=USD&asOf=2026-06-01&type=PRICE"
	code, out := call(t, h, http.MethodGet, url, "pricing.view", "")
	items, _ := out["items"].([]any)
	if code != http.StatusOK || len(items) != 3 {
		t.Fatalf("price list = %d %v; want the customer, group and all records", code, out)
	}
	scopes := []any{items[0].(map[string]any)["scope"], items[1].(map[string]any)["scope"], items[2].(map[string]any)["scope"]}
	if scopes[0] != "CUSTOMER" || scopes[1] != "GROUP" || scopes[2] != "ALL" || items[0].(map[string]any)["amount"] != "90" {
		t.Fatalf("scopes = %v, first %v; want CUSTOMER, GROUP, ALL with 90 first", scopes, items[0])
	}
	if _, out := call(t, h, http.MethodGet, "/v1/pricing/price-list?legalEntityId="+legalEntity+"&customerId=C1&currency=USD&asOf=2026-06-01", "pricing.view", ""); len(out["items"].([]any)) != 2 {
		t.Fatalf("without a group the group record drops out: %v", out)
	}
	if _, out := call(t, h, http.MethodGet, "/v1/pricing/price-list?legalEntityId="+legalEntity+"&customerId=C1&currency=USD&asOf=2027-06-01", "pricing.view", ""); len(out["items"].([]any)) != 0 {
		t.Fatalf("outside validity the list is empty: %v", out)
	}
}

func TestPriceList_ValidationAndDuty(t *testing.T) {
	h := router(newFake())
	code, out := call(t, h, http.MethodGet, "/v1/pricing/price-list?legalEntityId=x&currency=usd&asOf=June&type=NOPE", "pricing.view", "")
	errs, _ := out["errors"].(map[string]any)
	if code != http.StatusUnprocessableEntity || errs["legalEntityId"] == nil || errs["customerId"] == nil || errs["currency"] == nil || errs["asOf"] == nil || errs["type"] == nil {
		t.Fatalf("invalid query = %d %v; want 422 naming five fields", code, out)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/price-list?legalEntityId="+legalEntity+"&customerId=C1&currency=USD&asOf=2026-06-01", "pricing.maintain", ""); code != http.StatusForbidden {
		t.Fatalf("without pricing.view = %d; want 403", code)
	}
}

func TestCompare_RejectsOversizedInlineRecordsAndIgnoredFields(t *testing.T) {
	h := router(newFake())
	_, v1 := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, fmt.Sprintf(stackedProcedure, legalEntity))
	_, v2 := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, fmt.Sprintf(bestProcedure, legalEntity))
	a, b := v1["id"].(string), v2["id"].(string)

	one := `{"type":"PRICE","customerKind":"all","productKind":"all","currency":"USD","amount":"1","validFrom":"2026-01-01","validTo":"2026-12-31"}`
	many := "[" + strings.TrimSuffix(strings.Repeat(one+",", api.MaxInlineRecords+1), ",") + "]"
	code, out := call(t, h, http.MethodPost, "/v1/pricing/compare", "pricing.view", fmt.Sprintf(compareBody, legalEntity, a, b, many))
	if errs, _ := out["errors"].(map[string]any); code != http.StatusUnprocessableEntity || errs["records"] == nil {
		t.Fatalf("%d inline records = %d %v; want 422 on records", api.MaxInlineRecords+1, code, out)
	}
	atCap := "[" + strings.TrimSuffix(strings.Repeat(one+",", api.MaxInlineRecords), ",") + "]"
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/compare?explain=false", "pricing.view", fmt.Sprintf(compareBody, legalEntity, a, b, atCap)); code != http.StatusOK {
		t.Fatalf("%d inline records = %d %v; want 200", api.MaxInlineRecords, code, out)
	}
	code, out = call(t, h, http.MethodPost, "/v1/pricing/simulate", "pricing.view",
		`{"customerId":"C1","currency":"USD","pricingDate":"2026-06-01","lines":[{"productId":"P1","unit":"EA","quantity":"1"}],"records":`+many+`}`)
	if errs, _ := out["errors"].(map[string]any); code != http.StatusUnprocessableEntity || errs["records"] == nil {
		t.Fatalf("simulate with %d inline records = %d %v; want 422 on records", api.MaxInlineRecords+1, code, out)
	}

	withProcedure := strings.Replace(fmt.Sprintf(compareBody, legalEntity, a, b, compareRecords), `"customerId"`, `"procedure":"AX_STANDARD","salesChannel":"WEB","customerId"`, 1)
	code, out = call(t, h, http.MethodPost, "/v1/pricing/compare", "pricing.view", withProcedure)
	if errs, _ := out["errors"].(map[string]any); code != http.StatusUnprocessableEntity || errs["procedure"] == nil || errs["salesChannel"] == nil {
		t.Fatalf("procedure and salesChannel on compare = %d %v; want 422 naming both", code, out)
	}
}

func TestPriceList_TruncatesAtTheCap(t *testing.T) {
	fs := newFake()
	h := router(fs)
	for i := 0; i <= api.MaxPriceListItems; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0003-%012d", i)
		fs.records[id] = store.Entry{LegalEntityID: legalEntity, Record: domain.Record{ID: id, Version: 2, Status: domain.StatusActive, Type: domain.TypePrice,
			CustomerKind: domain.CustomerKindAll, ProductKind: domain.ProductKindProduct, ProductKey: fmt.Sprintf("P%05d", i), Currency: "USD", Unit: "EA",
			FromQuantity: decimal.Zero, Amount: decimal.RequireFromString("10"), ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ValidTo: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)}}
	}
	code, out := call(t, h, http.MethodGet, "/v1/pricing/price-list?legalEntityId="+legalEntity+"&customerId=C1&currency=USD&asOf=2026-06-01", "pricing.view", "")
	items, _ := out["items"].([]any)
	if code != http.StatusOK || len(items) != api.MaxPriceListItems || out["truncated"] != true {
		t.Fatalf("price list over the cap = %d, %d items, truncated %v; want %d items and truncated", code, len(items), out["truncated"], api.MaxPriceListItems)
	}
	if last := items[len(items)-1].(map[string]any); last["productKey"] != fmt.Sprintf("P%05d", api.MaxPriceListItems-1) || last["status"] != "ACTIVE" || last["version"] != float64(2) {
		t.Fatalf("the list keeps its product order up to the cap and the stored status and version: last = %v", last)
	}
	delete(fs.records, fmt.Sprintf("00000000-0000-0000-0003-%012d", 0))
	if _, out := call(t, h, http.MethodGet, "/v1/pricing/price-list?legalEntityId="+legalEntity+"&customerId=C1&currency=USD&asOf=2026-06-01", "pricing.view", ""); out["truncated"] != false {
		t.Fatalf("a list at the cap is not truncated: %v", out["truncated"])
	}
}
