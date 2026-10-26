package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	testTenant = "00000000-0000-0000-0000-000000000001"
	dutyHeader = "expense.report.write"
)

func serve(t *testing.T, path, body, duties string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpx.NewRouter()
	New(nil).Register(router)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testTenant)
	if duties != "" {
		req.Header.Set(tenant.HeaderDuties, duties)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

const report = `{
  "submitDate": "2026-03-20",
  "lines": [
    {"id": "L1", "category": "MEALS", "txnDate": "2026-03-10", "amount": "80.00", "merchant": "Bistro", "hasReceipt": true},
    {"id": "L2", "category": "TRAVEL", "txnDate": "2026-03-10", "amount": "30.00", "merchant": "Air", "paidBy": "COMPANY_CARD"},
    {"id": "L3", "category": "ALCOHOL", "txnDate": "2026-03-11", "amount": "12.00", "merchant": "Bar", "hasReceipt": true}
  ]
}`

func TestEvaluateReportFlagsPerRule(t *testing.T) {
	rec := serve(t, "/v1/expense/report-evaluations", report, dutyHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Submittable bool `json:"submittable"`
		Results     []struct {
			LineID    string `json:"lineId"`
			RuleID    string `json:"ruleId"`
			Outcome   string `json:"outcome"`
			Code      string `json:"code"`
			Satisfied bool   `json:"satisfied"`
		} `json:"results"`
		Totals struct {
			Total        string `json:"total"`
			Reimbursable string `json:"reimbursable"`
			CompanyPaid  string `json:"companyPaid"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// L1 meals 80 without attendees needs justification; L1 has a receipt so no receipt flag.
	// L2 is 30 with no receipt: receipt required. L3 alcohol is blocked.
	want := map[string]string{"L1/EXP-MEAL-075": "REQUIRE_JUSTIFICATION", "L2/EXP-RCPT-025": "REQUIRE_RECEIPT", "L3/EXP-ALC-001": "BLOCK"}
	if len(got.Results) != len(want) {
		t.Fatalf("results %+v", got.Results)
	}
	codes := map[string]string{"REQUIRE_JUSTIFICATION": "EXP-POL-001", "REQUIRE_RECEIPT": "EXP-REC-001", "BLOCK": "EXP-POL-001"}
	for _, r := range got.Results {
		if want[r.LineID+"/"+r.RuleID] != r.Outcome || codes[r.Outcome] != r.Code || r.Satisfied {
			t.Errorf("unexpected %s %s %s %s satisfied=%v", r.LineID, r.RuleID, r.Outcome, r.Code, r.Satisfied)
		}
	}
	if got.Submittable {
		t.Fatal("report with a block must not be submittable")
	}
	if got.Totals.Total != "122.00" || got.Totals.Reimbursable != "92.00" || got.Totals.CompanyPaid != "30.00" {
		t.Fatalf("totals %+v", got.Totals)
	}
}

func TestEvaluateRejectsBadLines(t *testing.T) {
	body := `{"submitDate": "2026-13-40", "lines": [{"id": "", "txnDate": "x", "amount": "-1", "paidBy": "CASH"}]}`
	rec := serve(t, "/v1/expense/report-evaluations", body, dutyHeader)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	for _, field := range []string{"submitDate", "lines[0].id", "lines[0].txnDate", "lines[0].amount", "lines[0].paidBy"} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("error does not name %s: %s", field, rec.Body.String())
		}
	}
}

func TestEvaluateRejectsRepeatedLineIDs(t *testing.T) {
	body := `{"submitDate": "2026-03-20", "lines": [
	  {"id": "L1", "category": "TRAVEL", "txnDate": "2026-03-10", "amount": "5"},
	  {"id": "L1", "category": "TRAVEL", "txnDate": "2026-03-10", "amount": "6"}]}`
	rec := serve(t, "/v1/expense/report-evaluations", body, dutyHeader)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "L1") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSettleAdvancePreview(t *testing.T) {
	// AC-43.9: 1,000.00 advance against an 820.00 report.
	body := `{"advances": [{"id": "EXP-ADV-1", "paidOn": "2026-03-01", "paid": "1000"}], "reimbursableTotal": "820"}`
	rec := serve(t, "/v1/expense/cash-advance-settlements", body, dutyHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Applications []struct {
			AdvanceID, Applied, OpenAfter, Status string
		} `json:"applications"`
		Applied         string `json:"applied"`
		APInvoiceAmount string `json:"apInvoiceAmount"`
		Code            string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Applications) != 1 || got.Applications[0].OpenAfter != "180.00" || got.Applications[0].Status != "PARTIALLY_SETTLED" ||
		got.Applied != "820.00" || got.APInvoiceAmount != "0.00" || got.Code != "EXP-ADV-003" {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestSettleAdvanceValidation(t *testing.T) {
	body := `{"advances": [{"id": "A", "paidOn": "2026-03-01", "paid": "100", "cleared": "150"},
	  {"id": "A", "paidOn": "March", "paid": "x"}], "reimbursableTotal": "-1"}`
	rec := serve(t, "/v1/expense/cash-advance-settlements", body, dutyHeader)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"advances[0].cleared", "advances[1].id", "advances[1].paidOn", "advances[1].paid", "reimbursableTotal"} {
		if !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("error does not name %s: %s", field, rec.Body.String())
		}
	}
}

func TestTravelVarianceWithinTolerance(t *testing.T) {
	rec := serve(t, "/v1/expense/travel-variances", `{"estimatedTotal": "2000", "actualTotal": "2200"}`, dutyHeader)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"requiresJustification":false`) || strings.Contains(rec.Body.String(), "EXP-TR-001") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTravelVariance(t *testing.T) {
	rec := serve(t, "/v1/expense/travel-variances", `{"estimatedTotal": "2000", "actualTotal": "2300", "tolerancePct": "10"}`, dutyHeader)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"requiresJustification":true`) || !strings.Contains(rec.Body.String(), "EXP-TR-001") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestEndpointsRequireDuty(t *testing.T) {
	for _, path := range []string{"/v1/expense/report-evaluations", "/v1/expense/cash-advance-settlements", "/v1/expense/travel-variances"} {
		rec := serve(t, path, `{}`, "expense.read")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
			t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body.String())
		}
	}
}
