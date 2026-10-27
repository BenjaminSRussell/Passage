package api

import (
	"net/http"
	"testing"
)

// fr201Txns is the FR-20.1 dataset as request JSON.
const fr201Txns = `[
  {"id":"R1","kind":"RECEIPT","qty":"100","value":"1000.00","financialDate":"2026-01-05"},
  {"id":"R2","kind":"RECEIPT","qty":"100","value":"1200.00","financialDate":"2026-01-20"},
  {"id":"I1","kind":"ISSUE","qty":"150","financialDate":"2026-01-25"}]`

func TestPreviewSettlements_MarkedReceiptFirst(t *testing.T) {
	h, _ := server()
	body := `{"method":"FIFO","currency":"EUR","transactions":[
	  {"id":"R1","kind":"RECEIPT","qty":"100","value":"1000.00","financialDate":"2026-01-05"},
	  {"id":"R2","kind":"RECEIPT","qty":"100","value":"1200.00","financialDate":"2026-01-20"},
	  {"id":"I1","kind":"ISSUE","qty":"150","financialDate":"2026-01-25","markedReceiptId":"R2"}]}`
	rec := call(h, http.MethodPost, "/v1/costing/settlements:preview", body, allDuties, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if m, _ := decodeMap(rec); m["issueCosts"].(map[string]any)["I1"] != "1700.00" {
		t.Errorf("response = %v, want I1 at 1700.00", m)
	}
}

func TestPreviewSettlements_MarkOnReceiptRefused(t *testing.T) {
	h, _ := server()
	body := `{"method":"FIFO","currency":"EUR","transactions":[
	  {"id":"R1","kind":"RECEIPT","qty":"100","value":"1000.00","financialDate":"2026-01-05","markedReceiptId":"R1"}]}`
	rec := call(h, http.MethodPost, "/v1/costing/settlements:preview", body, allDuties, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestPreviewSettlements_SplitValuation(t *testing.T) {
	h, _ := server()
	body := `{"method":"WEIGHTED_AVG_RUNNING","currency":"EUR","transactions":[
	  {"id":"D1","kind":"RECEIPT","qty":"100","value":"1000.00","financialDate":"2026-01-05","valuationType":"DOMESTIC"},
	  {"id":"M1","kind":"RECEIPT","qty":"100","value":"1400.00","financialDate":"2026-01-20","valuationType":"IMPORT"},
	  {"id":"I1","kind":"ISSUE","qty":"50","financialDate":"2026-01-25","valuationType":"IMPORT"}]}`
	rec := call(h, http.MethodPost, "/v1/costing/settlements:preview", body, allDuties, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m, _ := decodeMap(rec)
	if m["issueCosts"].(map[string]any)["I1"] != "700.00" {
		t.Errorf("issue cost = %v, want 700.00", m["issueCosts"])
	}
	by := m["remainingByValuationType"].(map[string]any)
	if by["DOMESTIC"] != "1000.00" || by["IMPORT"] != "700.00" {
		t.Errorf("remaining by type = %v", by)
	}
}

func TestPreviewSettlements_UntypedIssueWithTwoTypesRefused(t *testing.T) {
	h, _ := server()
	body := `{"method":"FIFO","currency":"EUR","transactions":[
	  {"id":"D1","kind":"RECEIPT","qty":"100","value":"1000.00","financialDate":"2026-01-05","valuationType":"DOMESTIC"},
	  {"id":"M1","kind":"RECEIPT","qty":"100","value":"1400.00","financialDate":"2026-01-20","valuationType":"IMPORT"},
	  {"id":"I1","kind":"ISSUE","qty":"50","financialDate":"2026-01-25"}]}`
	rec := call(h, http.MethodPost, "/v1/costing/settlements:preview", body, allDuties, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if m, _ := decodeMap(rec); m["code"] != "CST-VAL-001" {
		t.Errorf("code = %v, want CST-VAL-001", m["code"])
	}
}

func TestPreviewValuationLedgers_AC2011(t *testing.T) {
	h, _ := server()
	body := `{"ledgers":[
	  {"id":"IFRS","method":"WEIGHTED_AVG_PERIODIC","currency":"EUR","rate":"1","primary":true},
	  {"id":"GROUP","method":"FIFO","currency":"USD","rate":"1.10"}],
	  "transactions":` + fr201Txns + `}`
	rec := call(h, http.MethodPost, "/v1/costing/valuation-ledgers:preview", body, allDuties, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m, _ := decodeMap(rec)
	ledgers := m["ledgers"].([]any)
	if len(ledgers) != 2 {
		t.Fatalf("ledgers = %v", ledgers)
	}
	primary := ledgers[0].(map[string]any)
	group := ledgers[1].(map[string]any)
	if primary["issueCosts"].(map[string]any)["I1"] != "1650.00" || primary["primary"] != true {
		t.Errorf("primary = %v", primary)
	}
	if group["issueCosts"].(map[string]any)["I1"] != "1760.00" || group["currency"] != "USD" {
		t.Errorf("group = %v", group)
	}
}

func TestPreviewValuationLedgers_Refusals(t *testing.T) {
	h, _ := server()
	tests := []struct {
		name string
		body string
		want int
	}{
		{"no primary is a domain refusal", `{"ledgers":[{"id":"G","method":"FIFO","currency":"USD","rate":"1.1"}],"transactions":` + fr201Txns + `}`, http.StatusUnprocessableEntity},
		{"zero rate is a field error", `{"ledgers":[{"id":"P","method":"FIFO","currency":"EUR","rate":"0","primary":true}],"transactions":` + fr201Txns + `}`, http.StatusBadRequest},
		{"unknown method is a field error", `{"ledgers":[{"id":"P","method":"HIFO","currency":"EUR","rate":"1","primary":true}],"transactions":` + fr201Txns + `}`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(h, http.MethodPost, "/v1/costing/valuation-ledgers:preview", tc.body, allDuties, nil)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestPreviewAging_TiesToRemainingValue(t *testing.T) {
	h, _ := server()
	body := `{"method":"WEIGHTED_AVG_RUNNING","currency":"EUR","asOf":"2026-01-31","transactions":` + fr201Txns + `}`
	rec := call(h, http.MethodPost, "/v1/costing/aging:preview", body, allDuties, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m, _ := decodeMap(rec)
	if m["tiesToRemaining"] != true || m["totalValue"] != "550.00" || m["remainingValue"] != "550.00" {
		t.Errorf("aging = %v", m)
	}
	buckets := m["buckets"].([]any)
	if len(buckets) != 5 {
		t.Fatalf("buckets = %d, want the 5 defaults", len(buckets))
	}
	if first := buckets[0].(map[string]any); first["label"] != "0-30" || first["value"] != "550.00" {
		t.Errorf("first bucket = %v", first)
	}
}

func TestPreviewAging_CustomBucketsAndAsOfGuard(t *testing.T) {
	h, _ := server()
	body := `{"method":"FIFO","currency":"EUR","asOf":"2026-01-31","bucketBounds":[7],"transactions":` + fr201Txns + `}`
	rec := call(h, http.MethodPost, "/v1/costing/aging:preview", body, allDuties, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if m, _ := decodeMap(rec); len(m["buckets"].([]any)) != 2 {
		t.Errorf("custom buckets = %v, want 2", m["buckets"])
	}
	late := `{"method":"FIFO","currency":"EUR","asOf":"2026-01-10","transactions":` + fr201Txns + `}`
	rec = call(h, http.MethodPost, "/v1/costing/aging:preview", late, allDuties, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("transactions after asOf status %d, want 400", rec.Code)
	}
}

func TestValuationPreviews_RequireDuty(t *testing.T) {
	h, _ := server()
	for _, path := range []string{"/v1/costing/valuation-ledgers:preview", "/v1/costing/aging:preview"} {
		if rec := call(h, http.MethodPost, path, `{}`, "", nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s status %d, want 403", path, rec.Code)
		}
	}
}

func TestPreviewValuationLedgers_NoPrimaryCode(t *testing.T) {
	h, _ := server()
	body := `{"ledgers":[{"id":"G","method":"FIFO","currency":"USD","rate":"1.1"}],"transactions":` + fr201Txns + `}`
	rec := call(h, http.MethodPost, "/v1/costing/valuation-ledgers:preview", body, allDuties, nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusUnprocessableEntity || m["code"] != "CST-VLG-001" {
		t.Errorf("status %d code %v, want 422 CST-VLG-001", rec.Code, m["code"])
	}
}

func TestValuationPreviews_FieldErrors(t *testing.T) {
	h, _ := server()
	tests := []struct {
		name, path, body, field string
	}{
		{"ledger fallback", "/v1/costing/valuation-ledgers:preview",
			`{"ledgers":[{"id":"P","method":"FIFO","currency":"EUR","rate":"1","primary":true,"fallback":"GUESS"}],"transactions":` + fr201Txns + `}`,
			"ledgers[0].fallback"},
		{"aging fallback", "/v1/costing/aging:preview",
			`{"method":"FIFO","currency":"EUR","asOf":"2026-01-31","fallback":"GUESS","transactions":` + fr201Txns + `}`, "fallback"},
		{"descending bucket bounds", "/v1/costing/aging:preview",
			`{"method":"FIFO","currency":"EUR","asOf":"2026-01-31","bucketBounds":[90,30],"transactions":` + fr201Txns + `}`, "bucketBounds"},
		{"too many bucket bounds", "/v1/costing/aging:preview",
			`{"method":"FIFO","currency":"EUR","asOf":"2026-01-31","bucketBounds":[1,2,3,4,5,6,7,8,9,10,11,12,13],"transactions":` + fr201Txns + `}`, "bucketBounds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(h, http.MethodPost, tc.path, tc.body, allDuties, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
			}
			m, _ := decodeMap(rec)
			if errs, _ := m["errors"].(map[string]any); errs[tc.field] == nil {
				t.Errorf("errors = %v, want a %s entry", m["errors"], tc.field)
			}
		})
	}
}

func TestPreviewAging_SplitByValuationType(t *testing.T) {
	h, _ := server()
	body := `{"method":"WEIGHTED_AVG_RUNNING","currency":"EUR","asOf":"2026-01-31","transactions":[
	  {"id":"D1","kind":"RECEIPT","qty":"100","value":"1000.00","financialDate":"2026-01-05","valuationType":"DOMESTIC"},
	  {"id":"M1","kind":"RECEIPT","qty":"100","value":"1400.00","financialDate":"2026-01-20","valuationType":"IMPORT"},
	  {"id":"I1","kind":"ISSUE","qty":"50","financialDate":"2026-01-25","valuationType":"IMPORT"}]}`
	rec := call(h, http.MethodPost, "/v1/costing/aging:preview", body, allDuties, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m, _ := decodeMap(rec)
	if m["totalValue"] != "1700.00" || m["tiesToRemaining"] != true {
		t.Errorf("aging = %v, want 1700.00 tying to the remaining value", m)
	}
	types := m["valuationTypes"].([]any)
	if len(types) != 2 {
		t.Fatalf("valuationTypes = %v", types)
	}
	imp := types[1].(map[string]any)
	if imp["valuationType"] != "IMPORT" || imp["totalValue"] != "700.00" {
		t.Errorf("IMPORT aging = %v, want 700.00", imp)
	}
}

func TestPreviewAging_UnsplitOmitsValuationTypes(t *testing.T) {
	h, _ := server()
	body := `{"method":"FIFO","currency":"EUR","asOf":"2026-01-31","transactions":` + fr201Txns + `}`
	rec := call(h, http.MethodPost, "/v1/costing/aging:preview", body, allDuties, nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusOK || m["valuationTypes"] != nil {
		t.Errorf("status %d valuationTypes %v, want 200 and none", rec.Code, m["valuationTypes"])
	}
}
