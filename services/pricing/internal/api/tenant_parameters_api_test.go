package api_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const tenantParametersURL = "/v1/pricing/tenant-parameters"

func TestTenantParameters_DefaultsSetVersionAndDuties(t *testing.T) {
	h := router(newFake())
	status, out := call(t, h, http.MethodGet, tenantParametersURL, "pricing.view", "")
	if status != http.StatusOK || out["cacheTtlSeconds"] != float64(0) || out["campaignKillSwitchEffect"] != "NEXT_RESOLVE" || out["version"] != float64(0) {
		t.Fatalf("defaults = %d %v", status, out)
	}
	body := `{"cacheTtlSeconds":30,"campaignKillSwitchEffect":"IMMEDIATE_CACHE_FLUSH","version":0}`
	if status, _ := call(t, h, http.MethodPut, tenantParametersURL, "pricing.view,pricing.maintain", body); status != http.StatusForbidden {
		t.Fatalf("set without pricing.admin = %d, want 403", status)
	}
	if status, _ := call(t, h, http.MethodGet, tenantParametersURL, "", ""); status != http.StatusForbidden {
		t.Fatalf("get without duty = %d, want 403", status)
	}
	status, out = call(t, h, http.MethodPut, tenantParametersURL, "pricing.admin", body)
	if status != http.StatusOK || out["cacheTtlSeconds"] != float64(30) || out["campaignKillSwitchEffect"] != "IMMEDIATE_CACHE_FLUSH" || out["version"] != float64(1) {
		t.Fatalf("set = %d %v", status, out)
	}
	status, out = call(t, h, http.MethodGet, tenantParametersURL, "pricing.view", "")
	if status != http.StatusOK || out["version"] != float64(1) || out["campaignKillSwitchEffect"] != "IMMEDIATE_CACHE_FLUSH" {
		t.Fatalf("read after set = %d %v", status, out)
	}
	if status, out = call(t, h, http.MethodPut, tenantParametersURL, "pricing.admin", body); status != http.StatusPreconditionFailed || out["code"] != "conflict_version" {
		t.Fatalf("stale set = %d %v", status, out)
	}
}

func TestTenantParameters_Validation(t *testing.T) {
	h := router(newFake())
	for field, body := range map[string]string{
		"cacheTtlSeconds":          `{"cacheTtlSeconds":9,"campaignKillSwitchEffect":"NEXT_RESOLVE","version":0}`,
		"cacheTtlSeconds ":         `{"cacheTtlSeconds":601,"campaignKillSwitchEffect":"NEXT_RESOLVE","version":0}`,
		"cacheTtlSeconds  ":        `{"campaignKillSwitchEffect":"NEXT_RESOLVE","version":0}`,
		"campaignKillSwitchEffect": `{"cacheTtlSeconds":0,"campaignKillSwitchEffect":"NOW","version":0}`,
		"version":                  `{"cacheTtlSeconds":0,"campaignKillSwitchEffect":"NEXT_RESOLVE","version":-1}`,
	} {
		status, out := call(t, h, http.MethodPut, tenantParametersURL, "pricing.admin", body)
		fields, _ := out["errors"].(map[string]any)
		key := field
		for key[len(key)-1] == ' ' {
			key = key[:len(key)-1]
		}
		if status != http.StatusUnprocessableEntity || fields[key] == nil {
			t.Errorf("%s: %d %v; want 422 naming the field", key, status, out)
		}
	}
	// 0 and the bounds are accepted.
	for _, ttl := range []int{0, 10, 600} {
		body := fmt.Sprintf(`{"cacheTtlSeconds":%d,"campaignKillSwitchEffect":"NEXT_RESOLVE","version":%d}`, ttl, map[int]int{0: 0, 10: 1, 600: 2}[ttl])
		if status, out := call(t, h, http.MethodPut, tenantParametersURL, "pricing.admin", body); status != http.StatusOK {
			t.Errorf("ttl %d: %d %v", ttl, status, out)
		}
	}
}

const (
	surchargeSimulate = `{"legalEntityId":"%s","customerId":"C1","currency":"%s","pricingDate":"2026-06-01",
		"lines":[{"productId":"P1","unit":"EA","quantity":"1"}],
		"records":[
			{"id":"p","type":"PRICE","customerKind":"customer","customerKey":"C1","productKind":"product","productKey":"P1","currency":"%s","amount":"10.00","validFrom":"2026-01-01","validTo":"2026-12-31"},
			{"id":"s","type":"SURCHARGE","customerKind":"customer","customerKey":"C1","productKind":"all","currency":"%s","discPct1":"5","validFrom":"2026-01-01","validTo":"2026-12-31"}]}`
)

func putThresholds(t *testing.T, h http.Handler, thresholds string, version int) {
	t.Helper()
	put := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":false,%s"priceSearchMode":"FIRST_MATCH","smallOrderThresholds":%s,"version":%d}`,
		legalEntity, allValues[:len(allValues)-len(`"priceSearchMode":"FIRST_MATCH","smallOrderThresholds":{},`)], thresholds, version)
	if status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", put, inEntity...); status != http.StatusOK {
		t.Fatalf("set thresholds %s = %d %v", thresholds, status, out)
	}
}

func TestSimulate_SmallOrderThresholdFromTheLegalEntity(t *testing.T) {
	h := router(newFake())
	sim := func(cur string) map[string]any {
		_, out := call(t, h, http.MethodPost, "/v1/pricing/simulate?explain=false", allDuties, fmt.Sprintf(surchargeSimulate, legalEntity, cur, cur, cur))
		return out
	}
	// The reference table: 250 USD, 200 EUR, none for GBP.
	if out := sim("USD"); out["surcharge"] != "0.5" {
		t.Fatalf("USD reference threshold: surcharge = %v; want 0.5", out["surcharge"])
	}
	if out := sim("GBP"); out["surcharge"] != "0" && out["surcharge"] != nil {
		t.Fatalf("GBP has no reference threshold: surcharge = %v; want none", out["surcharge"])
	}
	// The legal entity lowers USD below the order, adds GBP, and leaves EUR on the reference.
	putThresholds(t, h, `{"USD":"5","GBP":"50.00"}`, 0)
	if out := sim("USD"); out["surcharge"] != "0" && out["surcharge"] != nil {
		t.Fatalf("USD threshold 5: surcharge = %v; want none (order net 10 is not below it)", out["surcharge"])
	}
	if out := sim("GBP"); out["surcharge"] != "0.5" {
		t.Fatalf("GBP threshold 50: surcharge = %v; want 0.5", out["surcharge"])
	}
	if out := sim("EUR"); out["surcharge"] != "0.5" {
		t.Fatalf("EUR keeps the reference threshold: surcharge = %v; want 0.5", out["surcharge"])
	}
	// 0 switches the surcharge off.
	putThresholds(t, h, `{"USD":"0"}`, 1)
	if out := sim("USD"); out["surcharge"] != "0" && out["surcharge"] != nil {
		t.Fatalf("USD threshold 0: surcharge = %v; want none", out["surcharge"])
	}
}

func TestParameters_ThresholdAndSearchModeValidation(t *testing.T) {
	h := router(newFake())
	base := allValues[:len(allValues)-len(`"priceSearchMode":"FIRST_MATCH","smallOrderThresholds":{},`)]
	for field, extra := range map[string]string{
		"priceSearchMode":            `"priceSearchMode":"ANY","smallOrderThresholds":{},`,
		"smallOrderThresholds[usd]":  `"priceSearchMode":"FIRST_MATCH","smallOrderThresholds":{"usd":"5"},`,
		"smallOrderThresholds[USD]":  `"priceSearchMode":"FIRST_MATCH","smallOrderThresholds":{"USD":"-1"},`,
		"smallOrderThresholds[EUR]":  `"priceSearchMode":"FIRST_MATCH","smallOrderThresholds":{"EUR":"1.1234567"},`,
		"smallOrderThresholds[GBP]":  `"priceSearchMode":"FIRST_MATCH","smallOrderThresholds":{"GBP":"many"},`,
		"smallOrderThresholds":       `"priceSearchMode":"FIRST_MATCH",`,
		"priceSearchMode (missing) ": `"smallOrderThresholds":{},`,
	} {
		body := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":false,%s%s"version":0}`, legalEntity, base, extra)
		status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", body, inEntity...)
		fields, _ := out["errors"].(map[string]any)
		key := field
		if i := len(key) - len(" (missing) "); i > 0 && key[i:] == " (missing) " {
			key = key[:i]
		}
		if status != http.StatusUnprocessableEntity || fields[key] == nil {
			t.Errorf("%s: %d %v; want 422 naming %s", field, status, out, key)
		}
	}
}

func putSearchMode(t *testing.T, h http.Handler, mode string) {
	t.Helper()
	base := allValues[:len(allValues)-len(`"priceSearchMode":"FIRST_MATCH","smallOrderThresholds":{},`)]
	put := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":false,%s"priceSearchMode":"%s","smallOrderThresholds":{},"version":0}`, legalEntity, base, mode)
	if status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", put, inEntity...); status != http.StatusOK {
		t.Fatalf("set search mode %s = %d %v", mode, status, out)
	}
}

func TestCreateRecord_SearchNextDefaultsFromTheSearchMode(t *testing.T) {
	h := router(newFake())
	create := func(extra string) map[string]any {
		body := fmt.Sprintf(`{"legalEntityId":"%s","type":"PRICE","customerKind":"customer","customerKey":"C1","productKind":"product","productKey":"P1",
			"currency":"USD","fromQuantity":"0","amount":"9.00",%s"validFrom":"2026-01-01","validTo":"2026-12-31"}`, legalEntity, extra)
		status, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, body)
		if status != http.StatusCreated {
			t.Fatalf("create = %d %v", status, out)
		}
		return out
	}
	if out := create(""); out["searchNext"] != false {
		t.Fatalf("FIRST_MATCH default: searchNext = %v; want false", out["searchNext"])
	}
	putSearchMode(t, h, "SEARCH_NEXT")
	if out := create(""); out["searchNext"] != true {
		t.Fatalf("SEARCH_NEXT default: searchNext = %v; want true", out["searchNext"])
	}
	// An explicit value always wins.
	if out := create(`"searchNext":false,`); out["searchNext"] != false {
		t.Fatalf("explicit false under SEARCH_NEXT: searchNext = %v; want false", out["searchNext"])
	}
}

func TestJournal_CreateLineSearchNextDefaultsFromTheSearchMode(t *testing.T) {
	f := newFake()
	h := router(f)
	putSearchMode(t, h, "SEARCH_NEXT")
	body := fmt.Sprintf(`{"legalEntityId":"%s","name":"J","lines":[
		{"op":"CREATE","record":{"legalEntityId":"%s","type":"PRICE","customerKind":"customer","customerKey":"C1","productKind":"product","productKey":"P1","currency":"USD","amount":"9.00","validFrom":"2026-01-01","validTo":"2026-12-31"}},
		{"op":"CREATE","record":{"legalEntityId":"%s","type":"PRICE","customerKind":"customer","customerKey":"C2","productKind":"product","productKey":"P1","currency":"USD","amount":"9.00","searchNext":false,"validFrom":"2026-01-01","validTo":"2026-12-31"}}]}`,
		legalEntity, legalEntity, legalEntity)
	status, out := call(t, h, http.MethodPost, "/v1/pricing/journals", allDuties, body)
	if status != http.StatusCreated {
		t.Fatalf("create journal = %d %v", status, out)
	}
	j := f.journals[out["id"].(string)]
	if j == nil || len(j.Lines) != 2 || !j.Lines[0].Record.SearchNext || j.Lines[1].Record.SearchNext {
		t.Fatalf("stored lines = %+v; want searchNext true (defaulted) then false (explicit)", j)
	}
}

// A CSV CREATE row with an empty searchNext cell takes the legal entity's price_search_mode; a
// cell with a value keeps it.
func TestJournalCSVImport_SearchNextCellDefaultsFromTheSearchMode(t *testing.T) {
	f := newFake()
	h := router(f)
	putSearchMode(t, h, "SEARCH_NEXT")
	body := csvHeader + "\n" +
		"CREATE,,,,PRICE,customer,C1,product,P1,USD,EA,0,9.00,2026-01-01,2026-12-31,,\n" +
		"CREATE,,,,PRICE,customer,C2,product,P1,USD,EA,0,9.00,2026-01-01,2026-12-31,false,\n"
	code, out := importJournal(t, h, "legalEntityId="+legalEntity+"&name=Csv", body)
	if code != http.StatusCreated {
		t.Fatalf("import = %d %v", code, out)
	}
	j := f.journals[out["id"].(string)]
	if j == nil || len(j.Lines) != 2 || !j.Lines[0].Record.SearchNext || j.Lines[1].Record.SearchNext {
		t.Fatalf("stored lines = %+v; want searchNext true (empty cell) then false (explicit)", j)
	}
}

// The parameter pages read past the replica's cache: a change another replica made (here
// straight into the store) shows at once with its version, so the next save is not a 412.
func TestParameterPages_ReadPastTheReplicaCache(t *testing.T) {
	f := newFake()
	h := router(store.NewCache(f, time.Minute))
	if status, out := call(t, h, http.MethodGet, tenantParametersURL, "pricing.view", ""); status != http.StatusOK || out["version"] != float64(0) {
		t.Fatalf("first read = %d %v", status, out)
	}
	if status, out := call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+legalEntity, "pricing.view", "", inEntity...); status != http.StatusOK || out["version"] != float64(0) {
		t.Fatalf("first read = %d %v", status, out)
	}
	if _, err := f.SetTenantParameters(context.Background(), store.TenantParameters{CacheTTLSeconds: 30, CampaignKillSwitchEffect: "NEXT_RESOLVE"}, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.SetParameters(context.Background(), store.Parameters{LegalEntityID: legalEntity}, 0, ""); err != nil {
		t.Fatal(err)
	}
	if status, out := call(t, h, http.MethodGet, tenantParametersURL, "pricing.view", ""); status != http.StatusOK || out["version"] != float64(1) {
		t.Fatalf("read after another replica's change = %d %v; want version 1", status, out)
	}
	if status, out := call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+legalEntity, "pricing.view", "", inEntity...); status != http.StatusOK || out["version"] != float64(1) {
		t.Fatalf("read after another replica's change = %d %v; want version 1", status, out)
	}
}
