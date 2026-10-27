package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func decodeBody(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
}

const perDiemBody = `{
  "rate": {"country": "US", "city": "DEN", "currency": "USD", "lodging": "150.00", "mealsIncidentals": "80.00"},
  "days": [
    {"date": "2026-03-01", "lodging": true},
    {"date": "2026-03-02", "lodging": true, "breakfastProvided": true},
    {"date": "2026-03-03", "breakfastProvided": true}
  ]
}`

func TestPerDiemComputation(t *testing.T) {
	rec := serve(t, "/v1/expense/per-diem-computations", perDiemBody, dutyHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Currency string `json:"currency"`
		Lines    []struct {
			Partial bool   `json:"partial"`
			MIE     string `json:"mie"`
			Total   string `json:"total"`
		} `json:"lines"`
		Totals struct {
			Lodging string `json:"lodging"`
			MIE     string `json:"mie"`
			Total   string `json:"total"`
		} `json:"totals"`
	}
	decodeBody(t, rec.Body.String(), &got)
	// Day 1 partial 60.00; day 2 full less breakfast 64.00; day 3 partial 60.00
	// less breakfast at 20% of the full rate (16.00) = 44.00.
	if got.Totals.MIE != "168.00" || got.Totals.Lodging != "300.00" || got.Totals.Total != "468.00" {
		t.Fatalf("totals %+v", got.Totals)
	}
	if len(got.Lines) != 3 || got.Lines[0].MIE != "60.00" || got.Lines[1].MIE != "64.00" || got.Lines[2].MIE != "44.00" {
		t.Fatalf("lines %+v", got.Lines)
	}
	if !got.Lines[0].Partial || got.Lines[1].Partial || !got.Lines[2].Partial {
		t.Fatalf("partial flags %+v, want first and last day only", got.Lines)
	}
}

func TestPerDiemFormatsAtCurrencyMinorUnit(t *testing.T) {
	body := `{"rate": {"currency": "JPY", "lodging": "12000", "mealsIncidentals": "5001"}, "days": [{"date": "2026-03-01", "lodging": true}]}`
	rec := serve(t, "/v1/expense/per-diem-computations", body, dutyHeader)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":"15751"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPerDiemRefusesRepeatedDays(t *testing.T) {
	body := `{"rate": {"currency": "USD", "lodging": "150", "mealsIncidentals": "80"},
  "days": [{"date": "2026-03-01"}, {"date": "2026-03-01"}]}`
	rec := serve(t, "/v1/expense/per-diem-computations", body, dutyHeader)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"days"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPerDiemRefusesBadInput(t *testing.T) {
	body := `{"rate": {"country": "usa", "currency": "usd", "lodging": "-1", "mealsIncidentals": "80"}, "days": []}`
	rec := serve(t, "/v1/expense/per-diem-computations", body, dutyHeader)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"rate.country", "rate.currency", "rate.lodging", "days"} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("problem lacks field %s: %s", field, rec.Body.String())
		}
	}
}

func TestPerDiemRequiresDuty(t *testing.T) {
	rec := serve(t, "/v1/expense/per-diem-computations", perDiemBody, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

const agingBody = `{
  "asOf": "2026-02-01",
  "advances": [
    {"id": "ADV-1", "tripEnd": "2026-01-01", "paid": "1000", "settled": "820"},
    {"id": "ADV-2", "tripEnd": "2026-01-01", "paid": "500", "settled": "500"},
    {"id": "ADV-3", "tripEnd": "2026-01-01", "paid": "300", "settled": "100", "writtenOff": "200"},
    {"id": "ADV-4", "tripEnd": "2026-01-05", "paid": "200"}
  ]
}`

func TestCashAdvanceAgingBlocksWhenOverdue(t *testing.T) {
	rec := serve(t, "/v1/expense/cash-advance-aging", agingBody, dutyHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		NewAdvancesBlocked bool   `json:"newAdvancesBlocked"`
		Code               string `json:"code"`
		Overdue            []struct {
			ID          string `json:"id"`
			DueDate     string `json:"dueDate"`
			DaysPastDue int    `json:"daysPastDue"`
			Open        string `json:"open"`
		} `json:"overdue"`
	}
	decodeBody(t, rec.Body.String(), &got)
	// ADV-2 is settled and ADV-3 written off; ADV-4 is due 2026-02-04, still in time.
	if !got.NewAdvancesBlocked || got.Code != "EXP-ADV-002" || len(got.Overdue) != 1 || got.Overdue[0].ID != "ADV-1" {
		t.Fatalf("aging %+v", got)
	}
	if got.Overdue[0].DueDate != "2026-01-31" || got.Overdue[0].DaysPastDue != 1 || got.Overdue[0].Open != "180.00" {
		t.Fatalf("overdue detail %+v", got.Overdue[0])
	}
}

func TestCashAdvanceAgingNotBlockedOmitsCode(t *testing.T) {
	body := `{"asOf": "2026-01-15", "advances": [{"id": "ADV-1", "tripEnd": "2026-01-01", "paid": "1000"}]}`
	rec := serve(t, "/v1/expense/cash-advance-aging", body, dutyHeader)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "EXP-ADV-002") ||
		!strings.Contains(rec.Body.String(), `"newAdvancesBlocked":false`) || !strings.Contains(rec.Body.String(), `"overdue":[]`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCashAdvanceAgingRejectsOverClearedAndDuplicates(t *testing.T) {
	body := `{"asOf": "2026-02-01", "advances": [
    {"id": "ADV-1", "tripEnd": "2026-01-01", "paid": "100", "settled": "80", "returned": "30"},
    {"id": "ADV-1", "tripEnd": "2026-01-01", "paid": "100"}]}`
	rec := serve(t, "/v1/expense/cash-advance-aging", body, dutyHeader)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "advances[0].paid") ||
		!strings.Contains(rec.Body.String(), "advances[1].id") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCashAdvanceAgingRejectsBadLimit(t *testing.T) {
	body := `{"asOf": "2026-02-01", "limitDays": 400, "advances": []}`
	rec := serve(t, "/v1/expense/cash-advance-aging", body, dutyHeader)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limitDays") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequisitionCheckWarnsAndCloses(t *testing.T) {
	body := `{
  "trip": {"from": "2026-03-10", "to": "2026-03-14"},
  "travelDates": ["2026-03-09", "2026-03-11", "2026-03-15"],
  "lastTravelDate": "2026-03-14",
  "asOf": "2026-05-20",
  "reportPosted": false
}`
	rec := serve(t, "/v1/expense/travel-requisition-checks", body, dutyHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		OutOfRangeDates []string `json:"outOfRangeDates"`
		CloseDate       string   `json:"closeDate"`
		CloseDue        bool     `json:"closeDue"`
	}
	decodeBody(t, rec.Body.String(), &got)
	if len(got.OutOfRangeDates) != 2 || got.OutOfRangeDates[0] != "2026-03-09" || got.OutOfRangeDates[1] != "2026-03-15" {
		t.Fatalf("out of range %v", got.OutOfRangeDates)
	}
	if !got.CloseDue || got.CloseDate != "2026-05-13" {
		t.Fatalf("requisition 67 days after last travel must be close-due on 2026-05-13: %+v", got)
	}
}

func TestRequisitionCheckDefaultsLastTravelToWindowEnd(t *testing.T) {
	body := `{"trip": {"from": "2026-03-10", "to": "2026-03-14"}, "travelDates": ["2026-03-11"], "asOf": "2026-05-12"}`
	rec := serve(t, "/v1/expense/travel-requisition-checks", body, dutyHeader)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"closeDate":"2026-05-13"`) ||
		!strings.Contains(rec.Body.String(), `"closeDue":false`) || !strings.Contains(rec.Body.String(), `"outOfRangeDates":[]`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequisitionCheckRejectsInvertedWindow(t *testing.T) {
	body := `{"trip": {"from": "2026-03-14", "to": "2026-03-10"}, "travelDates": [], "lastTravelDate": "2026-03-14", "asOf": "2026-03-15"}`
	rec := serve(t, "/v1/expense/travel-requisition-checks", body, dutyHeader)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "trip.to") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

const cardImportDuty = "expense.card.import"

const csvFeed = `card_last4,txn_date,post_date,merchant,mcc,amount,currency
4242,2026-03-02,2026-03-03,ACME AIR,3000,412.50,USD
4242,2026-03-04,2026-03-04,,5812,20.00,USD
`

func TestCardFeedParseReturnsHashAndRejects(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"format": "CSV", "content": csvFeed})
	rec := serve(t, "/v1/expense/card-transactions:parse", string(payload), cardImportDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		FileHash     string `json:"fileHash"`
		Transactions []struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"transactions"`
		Rejected []struct {
			Line int `json:"line"`
		} `json:"rejected"`
	}
	decodeBody(t, rec.Body.String(), &got)
	if len(got.FileHash) != 64 || len(got.Transactions) != 1 || got.Transactions[0].Amount != "412.50" {
		t.Fatalf("feed %+v", got)
	}
	if len(got.Rejected) != 1 || got.Rejected[0].Line != 3 {
		t.Fatalf("rejected %+v", got.Rejected)
	}
}

func TestCardFeedParseRefusesUnknownFormat(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"format": "VCF", "content": "x"})
	rec := serve(t, "/v1/expense/card-transactions:parse", string(payload), cardImportDuty)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "format") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCardFeedParseRefusesMissingColumns(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"format": "CSV", "content": "a,b\n1,2\n"})
	rec := serve(t, "/v1/expense/card-transactions:parse", string(payload), cardImportDuty)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "content") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCardFeedParseRequiresCardImportDuty(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"format": "CSV", "content": csvFeed})
	rec := serve(t, "/v1/expense/card-transactions:parse", string(payload), dutyHeader)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d with only %s, want 403", rec.Code, dutyHeader)
	}
}

func TestCardFeedParseRefusesMalformedOFX(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"format": "OFX", "content": "plain text"})
	rec := serve(t, "/v1/expense/card-transactions:parse", string(payload), cardImportDuty)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not an OFX statement") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCardFeedParseKeepsCurrencyMinorUnits(t *testing.T) {
	feed := "card_last4,txn_date,post_date,merchant,amount,currency\n1111,2026-03-02,2026-03-02,SUSHI,1200,JPY\n"
	payload, _ := json.Marshal(map[string]string{"format": "csv", "content": feed})
	rec := serve(t, "/v1/expense/card-transactions:parse", string(payload), cardImportDuty)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"amount":"1200"`) || !strings.Contains(rec.Body.String(), `"format":"CSV"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}
