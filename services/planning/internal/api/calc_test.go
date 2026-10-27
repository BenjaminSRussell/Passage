package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

const (
	pathBuffer = "/v1/planning/ddmrp/buffers:evaluate"
	pathQuota  = "/v1/planning/quota-arrangements:allocate"
	pathConsum = "/v1/planning/strategies:consume-forecast"
)

func decodeBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}

// assertFieldError checks a 400 validation problem that names field.
func assertFieldError(t *testing.T, rec *httptest.ResponseRecorder, field string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	var p struct {
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Errors[field]; !ok {
		t.Fatalf("errors %v do not name %s", p.Errors, field)
	}
}

func assertDecimal(t *testing.T, field string, got any, want string) {
	t.Helper()
	s, ok := got.(string)
	if !ok {
		t.Fatalf("%s is %v, want decimal string %s", field, got, want)
	}
	if !decimal.RequireFromString(s).Equal(decimal.RequireFromString(want)) {
		t.Fatalf("%s = %s, want %s", field, s, want)
	}
}

// BR-22.11 worked example as a request.
const bufferExample = `{"profile":{"leadTimeFactor":"0.5","variabilityFactor":"0.5","orderCycleDays":7,"minOrderQty":"0"},
 "adu":"10","daf":"1","dltDays":"10","onHand":"90","openSupply":"60","demand":[{"day":0,"qty":"20"}]}`

func TestBufferEvaluate_WorkedExample(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodPost, pathBuffer, bufferExample, "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec.Body.String())
	assertDecimal(t, "plannedQty", body["plannedQty"], "115")
	assertDecimal(t, "priority", body["priority"], "0.5306")
	assertDecimal(t, "netFlow", body["netFlow"], "130")
	if body["zone"] != "yellow" {
		t.Fatalf("zone = %v, want yellow", body["zone"])
	}
	zones := body["zones"].(map[string]any)
	assertDecimal(t, "zones.topOfGreen", zones["topOfGreen"], "245")
	assertDecimal(t, "zones.topOfRed", zones["topOfRed"], "75")
}

func TestBufferEvaluate_ADUFromHistory(t *testing.T) {
	body := `{"profile":{"leadTimeFactor":"0.5","variabilityFactor":"0.5","orderCycleDays":7},
	 "history":[{"day":-2,"qty":"70"},{"day":-1,"qty":"91"},{"day":-3,"qty":"999"}],"pastDays":2,"futureDays":0,
	 "dltDays":"10","onHand":"0"}`
	rec := do(t, &fakeStore{}, http.MethodPost, pathBuffer, body, "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec.Body.String())
	// Window -2..-1 only: (70 + 91) / 2 = 80.5.
	assertDecimal(t, "adu", out["adu"], "80.5")
}

func TestBufferEvaluate_Validation(t *testing.T) {
	tests := map[string]struct{ body, field string }{
		"negative lead time factor": {`{"profile":{"leadTimeFactor":"-1"},"adu":"10","dltDays":"10"}`, "profile.leadTimeFactor"},
		"no usage at all":           {`{"profile":{},"dltDays":"10"}`, "adu"},
		"spike above limit":         {`{"profile":{"spikeThresholdPct":"900"},"adu":"10","dltDays":"10"}`, "profile.spikeThresholdPct"},
		"negative open supply":      {`{"profile":{},"adu":"10","dltDays":"10","openSupply":"-5"}`, "openSupply"},
		"negative order multiple":   {`{"profile":{},"adu":"10","dltDays":"10","orderMultiple":"-5"}`, "orderMultiple"},
		"DLT too long":              {`{"profile":{},"adu":"10","dltDays":"4000"}`, "dltDays"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assertFieldError(t, do(t, &fakeStore{}, http.MethodPost, pathBuffer, tc.body, "planning.read", true, ""), tc.field)
		})
	}
}

// An empty ADU window (no past or future days) cannot give a usage: 422.
func TestBufferEvaluate_EmptyADUWindowIs422(t *testing.T) {
	body := `{"profile":{},"history":[{"day":-1,"qty":"5"}],"pastDays":0,"futureDays":0,"dltDays":"10"}`
	rec := do(t, &fakeStore{}, http.MethodPost, pathBuffer, body, "planning.read", true, "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), codeInput) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestBufferEvaluate_OrderMultipleRoundsThePlannedQty(t *testing.T) {
	body := strings.Replace(bufferExample, `"daf":"1"`, `"daf":"1","orderMultiple":"50"`, 1)
	rec := do(t, &fakeStore{}, http.MethodPost, pathBuffer, body, "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	assertDecimal(t, "plannedQty", decodeBody(t, rec.Body.String())["plannedQty"], "150")
}

func TestBufferEvaluate_RequiresTenantAndDuty(t *testing.T) {
	if rec := do(t, &fakeStore{}, http.MethodPost, pathBuffer, bufferExample, "planning.read", false, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant: status %d", rec.Code)
	}
	if rec := do(t, &fakeStore{}, http.MethodPost, pathBuffer, bufferExample, "", true, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty: status %d", rec.Code)
	}
}

const quotaExample = `{"sources":[
 {"id":"A","type":"vendor","quota":"60","allocatedQty":"300","priority":1},
 {"id":"B","type":"vendor","quota":"40","allocatedQty":"100","priority":2}],
 "qty":"100","split":false}`

func TestQuotaAllocate_WholeGoesToLowestRating(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodPost, pathQuota, quotaExample, "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec.Body.String())
	lines := out["assignments"].([]any)
	if len(lines) != 1 {
		t.Fatalf("assignments = %v", lines)
	}
	line := lines[0].(map[string]any)
	if line["sourceId"] != "B" {
		t.Fatalf("sourceId = %v, want B", line["sourceId"])
	}
	assertDecimal(t, "qty", line["qty"], "100")
	assertDecimal(t, "ratingAfter", line["ratingAfter"], "5")
}

func TestQuotaAllocate_NoUsableSourceIsPLAN0009(t *testing.T) {
	body := `{"sources":[{"id":"A","type":"vendor","quota":"60","blocked":true}],"qty":"10"}`
	rec := do(t, &fakeStore{}, http.MethodPost, pathQuota, body, "planning.read", true, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "PLAN-0009") {
		t.Fatalf("problem lacks PLAN-0009: %s", rec.Body.String())
	}
}

func TestQuotaAllocate_Validation(t *testing.T) {
	tests := map[string]struct{ body, field string }{
		"duplicate id":   {`{"sources":[{"id":"A","type":"vendor","quota":"1"},{"id":"A","type":"vendor","quota":"1"}],"qty":"1"}`, "sources[1].id"},
		"unknown type":   {`{"sources":[{"id":"A","type":"carrier","quota":"1"}],"qty":"1"}`, "sources[0].type"},
		"negative quota": {`{"sources":[{"id":"A","type":"vendor","quota":"-1"}],"qty":"1"}`, "sources[0].quota"},
		"zero quantity":  {`{"sources":[{"id":"A","type":"vendor","quota":"1"}],"qty":"0"}`, "qty"},
		"no sources":     {`{"sources":[],"qty":"1"}`, "sources"},
		"negative split": {`{"sources":[{"id":"A","type":"vendor","quota":"1"}],"qty":"1","minSplitQty":"-1"}`, "minSplitQty"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assertFieldError(t, do(t, &fakeStore{}, http.MethodPost, pathQuota, tc.body, "planning.read", true, ""), tc.field)
		})
	}
}

// With split on, BR-22.12 divides the requirement so the post-allocation
// ratings are equal: A (60, 300) and B (40, 100) with 400 end at 8 each.
func TestQuotaAllocate_SplitEqualisesRatings(t *testing.T) {
	body := strings.Replace(strings.Replace(quotaExample, `"split":false`, `"split":true`, 1), `"qty":"100"`, `"qty":"400"`, 1)
	rec := do(t, &fakeStore{}, http.MethodPost, pathQuota, body, "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	lines := decodeBody(t, rec.Body.String())["assignments"].([]any)
	if len(lines) != 2 {
		t.Fatalf("assignments = %v", lines)
	}
	a, b := lines[0].(map[string]any), lines[1].(map[string]any)
	assertDecimal(t, "A.qty", a["qty"], "180")
	assertDecimal(t, "B.qty", b["qty"], "220")
	assertDecimal(t, "A.ratingAfter", a["ratingAfter"], "8")
	assertDecimal(t, "B.ratingAfter", b["ratingAfter"], "8")
}

// AC-22.14: forecast 100 for November, sales 40 on 20 November, backward 30 days.
const consumeExample = `{"mode":"backward-forward","backwardDays":30,"forwardDays":0,
 "forecast":[{"id":"NOV","startDate":"2026-11-01","endDate":"2026-11-30","qty":"100"}],
 "sales":[{"id":"SO-1","date":"2026-11-20","qty":"40"}]}`

func TestConsumeForecast_AC2214(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodPost, pathConsum, consumeExample, "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec.Body.String())
	assertDecimal(t, "grossQty", out["grossQty"], "100")
	fc := out["forecast"].([]any)[0].(map[string]any)
	assertDecimal(t, "remainingQty", fc["remainingQty"], "60")
	sl := out["sales"].([]any)[0].(map[string]any)
	assertDecimal(t, "consumedQty", sl["consumedQty"], "40")
	assertDecimal(t, "unconsumedQty", sl["unconsumedQty"], "0")
}

func TestConsumeForecast_Validation(t *testing.T) {
	tests := map[string]struct{ body, field string }{
		"unknown mode":     {`{"mode":"sideways"}`, "mode"},
		"bad start date":   {`{"mode":"backward","forecast":[{"id":"x","startDate":"01/11/2026","endDate":"2026-11-30","qty":"1"}]}`, "forecast[0].startDate"},
		"end before start": {`{"mode":"backward","forecast":[{"id":"x","startDate":"2026-11-30","endDate":"2026-11-01","qty":"1"}]}`, "forecast[0].endDate"},
		"negative sales":   {`{"mode":"backward","sales":[{"id":"x","date":"2026-11-01","qty":"-3"}]}`, "sales[0].qty"},
		"window too long":  {`{"mode":"backward","backwardDays":9999}`, "backwardDays"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assertFieldError(t, do(t, &fakeStore{}, http.MethodPost, pathConsum, tc.body, "planning.read", true, ""), tc.field)
		})
	}
}
