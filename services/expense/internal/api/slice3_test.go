package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestEvaluateBlocksCategoryNeedingRequisition(t *testing.T) {
	body := `{"submitDate": "2026-03-20", "requisitionCategories": ["AIRFARE"], "lines": [
	  {"id": "L1", "category": "AIRFARE", "txnDate": "2026-03-10", "amount": "20.00", "merchant": "Air"},
	  {"id": "L2", "category": "MEALS", "txnDate": "2026-03-10", "amount": "20.00", "merchant": "Cafe"}]}`
	rec := serve(t, "/v1/expense/report-evaluations", body, dutyHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got evaluationResponse
	decodeBody(t, rec.Body.String(), &got)
	if got.Submittable || len(got.Results) != 1 {
		t.Fatalf("got %+v, want one blocking result", got)
	}
	if r := got.Results[0]; r.LineID != "L1" || r.RuleID != "EXP-TR-002" || r.Code != "EXP-TR-002" || r.Outcome != "BLOCK" || r.Satisfied {
		t.Fatalf("result %+v", r)
	}

	linked := strings.Replace(body, `"requisitionCategories"`, `"hasRequisition": true, "requisitionCategories"`, 1)
	rec = serve(t, "/v1/expense/report-evaluations", linked, dutyHeader)
	decodeBody(t, rec.Body.String(), &got)
	if !got.Submittable || len(got.Results) != 0 {
		t.Fatalf("linked report: %+v, want submittable with no results", got)
	}
}

func TestEvaluateRefusesBadRequisitionCategories(t *testing.T) {
	body := `{"submitDate": "2026-03-20", "requisitionCategories": [""], "lines": [
	  {"id": "L1", "category": "AIRFARE", "txnDate": "2026-03-10", "amount": "20.00"}]}`
	rec := serve(t, "/v1/expense/report-evaluations", body, dutyHeader)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"requisitionCategories[0]"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLodgingCheck(t *testing.T) {
	tests := []struct {
		name, body                    string
		wantCap                       string
		wantOver, wantSatisfied, hasR bool
	}{
		{"at the cap", `{"amount": "450.00", "nights": 2, "lodgingRate": "150.00"}`, "450.00", false, true, false},
		{"over without justification", `{"lineId": "L7", "amount": "450.01", "nights": 2, "lodgingRate": "150.00"}`, "450.00", true, false, true},
		{"over with justification", `{"amount": "500", "nights": 2, "lodgingRate": "150", "justification": "Conference rate"}`, "450.00", true, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, "/v1/expense/lodging-checks", tc.body, dutyHeader)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var got struct {
				CapPerNight string `json:"capPerNight"`
				Cap         string `json:"cap"`
				OverCap     bool   `json:"overCap"`
				Satisfied   bool   `json:"satisfied"`
				Result      *struct {
					RuleID  string `json:"ruleId"`
					Outcome string `json:"outcome"`
					Code    string `json:"code"`
				} `json:"result"`
			}
			decodeBody(t, rec.Body.String(), &got)
			if got.CapPerNight != "225.00" || got.Cap != tc.wantCap || got.OverCap != tc.wantOver || got.Satisfied != tc.wantSatisfied || (got.Result != nil) != tc.hasR {
				t.Fatalf("got %s", rec.Body.String())
			}
			if got.Result != nil && (got.Result.RuleID != "EXP-LODG-150" || got.Result.Outcome != "REQUIRE_JUSTIFICATION" || got.Result.Code != "EXP-POL-001") {
				t.Fatalf("result %+v", got.Result)
			}
		})
	}
}

func TestLodgingCheckRefusesBadInput(t *testing.T) {
	rec := serve(t, "/v1/expense/lodging-checks", `{"amount": "-1", "nights": 0, "lodgingRate": "x"}`, dutyHeader)
	body := rec.Body.String()
	if rec.Code != http.StatusBadRequest || !strings.Contains(body, `"amount"`) || !strings.Contains(body, `"nights"`) || !strings.Contains(body, `"lodgingRate"`) {
		t.Fatalf("status %d: %s", rec.Code, body)
	}
}

func TestMileageComputation(t *testing.T) {
	rec := serve(t, "/v1/expense/mileage-computations", `{"distance": "12.5", "rate": "0.705"}`, dutyHeader)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"amount":"8.81"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	rec = serve(t, "/v1/expense/mileage-computations", `{"distance": "100", "rate": "0.70"}`, dutyHeader)
	if !strings.Contains(rec.Body.String(), `"amount":"70.00"`) {
		t.Fatalf("IRS example: %s", rec.Body.String())
	}
}

func TestMileageRefusesBadInput(t *testing.T) {
	for _, body := range []string{`{"distance": "-5", "rate": "0.70"}`, `{"distance": "100", "rate": ""}`, `{"distance": "100001", "rate": "0.70"}`, `{"distance": "1", "rate": "101"}`} {
		if rec := serve(t, "/v1/expense/mileage-computations", body, dutyHeader); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", body, rec.Code)
		}
	}
}

const matchBody = `{
  "cards": [
    {"id": "c1", "workerId": "w1", "date": "2026-04-01", "merchant": "Hotel", "amount": "100.00"},
    {"id": "c2", "workerId": "w2", "date": "2026-04-01", "merchant": "Cafe", "amount": "12.00"}
  ],
  "receipts": [
    {"id": "r1", "workerId": "w1", "date": "2026-04-02", "merchant": "hotel", "amount": "99.50"},
    {"id": "r2", "workerId": "w1", "date": "2026-04-01", "merchant": "Cafe", "amount": "12.00"}
  ]
}`

func TestCardMatchOneToOneWithinWorker(t *testing.T) {
	rec := serve(t, "/v1/expense/card-matches", matchBody, dutyHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got cardMatchResponse
	decodeBody(t, rec.Body.String(), &got)
	// c2 belongs to w2 and the only Cafe receipt is w1's, so it stays unassigned.
	if fmt.Sprint(got.Matches) != "[{c1 r1}]" || fmt.Sprint(got.UnmatchedCards) != "[c2]" || fmt.Sprint(got.UnmatchedReceipts) != "[r2]" || got.AutoMatchRate != "0.5000" {
		t.Fatalf("got %s", rec.Body.String())
	}
}

func TestCardMatchHonoursTolerance(t *testing.T) {
	// Narrowing to 0 % leaves the 99.50 receipt outside the window.
	body := strings.Replace(matchBody, `"receipts"`, `"tolerancePct": "0", "receipts"`, 1)
	rec := serve(t, "/v1/expense/card-matches", body, dutyHeader)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"matches":[]`) || !strings.Contains(rec.Body.String(), `"autoMatchRate":"0.0000"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body = strings.Replace(matchBody, `"receipts"`, `"toleranceDays": 0, "receipts"`, 1)
	rec = serve(t, "/v1/expense/card-matches", body, dutyHeader)
	if !strings.Contains(rec.Body.String(), `"matches":[]`) {
		t.Fatalf("0 days should refuse the next-day receipt: %s", rec.Body.String())
	}
}

func TestCardMatchEmptyBatchReturnsArrays(t *testing.T) {
	rec := serve(t, "/v1/expense/card-matches", `{"cards": [], "receipts": []}`, dutyHeader)
	want := `{"matches":[],"unmatchedCards":[],"unmatchedReceipts":[],"autoMatchRate":"0.0000"}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCardMatchRefusesBadInput(t *testing.T) {
	body := `{"tolerancePct": "11", "toleranceDays": 11, "cards": [], "receipts": []}`
	rec := serve(t, "/v1/expense/card-matches", body, dutyHeader)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"tolerancePct"`) || !strings.Contains(rec.Body.String(), `"toleranceDays"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body = `{"cards": [
	  {"id": "c1", "workerId": "w1", "date": "2026-04-01", "merchant": "Hotel", "amount": "1"},
	  {"id": "c1", "workerId": "", "date": "04/01/2026", "merchant": "", "amount": "-1"}], "receipts": []}`
	rec = serve(t, "/v1/expense/card-matches", body, dutyHeader)
	got := rec.Body.String()
	for _, f := range []string{`"cards[1].id"`, `"cards[1].workerId"`, `"cards[1].date"`, `"cards[1].merchant"`, `"cards[1].amount"`} {
		if !strings.Contains(got, f) {
			t.Errorf("missing %s in %s", f, got)
		}
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestCardMatchRefusesOversizedBatch(t *testing.T) {
	items := make([]string, maxMatchItems+1)
	for i := range items {
		items[i] = fmt.Sprintf(`{"id": "c%d", "workerId": "w1", "date": "2026-04-01", "merchant": "M", "amount": "1"}`, i)
	}
	rec := serve(t, "/v1/expense/card-matches", `{"cards": [`+strings.Join(items, ",")+`], "receipts": []}`, dutyHeader)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"cards"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSlice3EndpointsRequireDuty(t *testing.T) {
	for _, path := range []string{"/v1/expense/lodging-checks", "/v1/expense/mileage-computations", "/v1/expense/card-matches"} {
		if rec := serve(t, path, `{}`, "expense.card.import"); rec.Code != http.StatusForbidden {
			t.Errorf("%s with only the card import duty: status %d, want 403", path, rec.Code)
		}
	}
}
