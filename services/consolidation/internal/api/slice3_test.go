package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/consolidation/internal/api"
)

const (
	dividendsPath     = "/v1/consol/dividends:preview"
	reclassPath       = "/v1/consol/reclassification-runs:preview"
	groupJournalsPath = "/v1/consol/group-journals:preview"
)

const dividendBody = `{"currency":"USD","ownershipPct":"80","rate":"1.10","amount":"50000"}`

func TestPreviewDividends_E6(t *testing.T) {
	a := api.New(nil)
	rec := do(a, dividendsPath, tenantID, api.DutyRun, dividendBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Entries  []entryOut
		Totals   struct{ Debit, Credit string }
		Rounding string
		Balanced bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []entryOut{
		{"E6", "DIVIDEND_INCOME", "44000.00", "0.00"},
		{"E6", "NCI_EQUITY", "11000.00", "0.00"},
		{"E6", "DIVIDENDS_DECLARED", "0.00", "55000.00"},
	}
	if !reflect.DeepEqual(got.Entries, want) || got.Totals.Debit != "55000.00" || got.Totals.Credit != "55000.00" || got.Rounding != "0.00" || !got.Balanced {
		t.Fatalf("response = %+v", got)
	}

	rec = do(a, dividendsPath, tenantID, api.DutyRun, `{"currency":"USD","ownershipPct":"50","rate":"1","amount":"10.005"}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if got.Rounding != "0.01" || got.Entries[len(got.Entries)-1] != (entryOut{"E6", "ROUNDING", "0.01", "0.00"}) || !got.Balanced {
		t.Fatalf("rounding line = %+v", got)
	}

	mrec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(mrec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(mrec.Body.String(), "consolidation_dividend_previews_total 2") {
		t.Fatalf("metrics = %s", mrec.Body.String())
	}
}

func TestPreviewDividends_Errors(t *testing.T) {
	runErrors(t, dividendsPath, []errCase{
		{"no rate", `{"currency":"USD","ownershipPct":"80","amount":"1"}`, api.DutyRun, http.StatusBadRequest, "COM-0001", "rate"},
		{"bad currency", strings.Replace(dividendBody, `"USD"`, `"usd"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "currency"},
		{"ownership above 100", strings.Replace(dividendBody, `"80"`, `"120"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", ""},
		{"zero dividend", strings.Replace(dividendBody, `"50000"`, `"0"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", ""},
		{"view duty only", dividendBody, api.DutyView, http.StatusForbidden, "", ""},
	})
}

// reclassBody is AC-40.13 plus a group-level sign-based rule: V1 has a debit
// balance of USD 5,000.00 and consolidated cash is negative.
const reclassBody = `{
  "currency":"USD","periodEnd":"2026-11-30",
  "rules":[
    {"code":"CASH","level":"Group","type":"SignBased","order":2,"version":1,"sourceAccount":"CASH","targetAccount":"BANK_OVERDRAFTS"},
    {"code":"VDB","level":"Entity","type":"DebitBalanceVendors","order":1,"version":3,"sourceAccount":"VENDOR_RECLASS","targetAccount":"OTHER_RECEIVABLES"}
  ],
  "openItems":[
    {"party":"V1","account":"VENDOR_RECLASS","amount":"5000.00"},
    {"party":"V2","account":"VENDOR_RECLASS","amount":"-800.00"}
  ],
  "balances":[{"account":"CASH","amount":"-250.00"}]
}`

func TestPreviewReclassification_AC4013(t *testing.T) {
	rec := do(api.New(nil), reclassPath, tenantID, api.DutyRun, reclassBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	type lineOut struct {
		Rule, Level, Account, Party, Debit, Credit string
		Reverses                                   bool
	}
	var got struct {
		DryRun       bool
		Applied      []string
		ReversalDate string
		Lines        []lineOut
		Totals       struct{ Debit, Credit string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []lineOut{
		{"VDB", "Entity", "OTHER_RECEIVABLES", "V1", "5000.00", "0.00", true},
		{"VDB", "Entity", "VENDOR_RECLASS", "V1", "0.00", "5000.00", true},
		{"CASH", "Group", "CASH", "", "250.00", "0.00", false},
		{"CASH", "Group", "BANK_OVERDRAFTS", "", "0.00", "250.00", false},
	}
	if !got.DryRun || !reflect.DeepEqual(got.Applied, []string{"VDB", "CASH"}) || got.ReversalDate != "2026-12-01" ||
		!reflect.DeepEqual(got.Lines, want) || got.Totals.Debit != "5250.00" || got.Totals.Credit != "5250.00" {
		t.Fatalf("response = %+v", got)
	}

	none := strings.Replace(reclassBody, `"5000.00"`, `"-5000.00"`, 1)
	rec = do(api.New(nil), reclassPath, tenantID, api.DutyRun, strings.Replace(none, `"-250.00"`, `"250.00"`, 1))
	got.ReversalDate = ""
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || len(got.Lines) != 0 || len(got.Applied) != 0 || got.ReversalDate != "" {
		t.Fatalf("nothing to move: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPreviewReclassification_Errors(t *testing.T) {
	runErrors(t, reclassPath, []errCase{
		{"no rules", `{"currency":"USD","periodEnd":"2026-11-30","rules":[]}`, api.DutyRun, http.StatusBadRequest, "COM-0001", "rules"},
		{"bad period end", strings.Replace(reclassBody, `"2026-11-30"`, `"30/11/2026"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "periodEnd"},
		{"no target", strings.Replace(reclassBody, `"targetAccount":"BANK_OVERDRAFTS"`, `"targetAccount":""`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "rules[0].targetAccount"},
		{"wrong level", strings.Replace(reclassBody, `"level":"Group"`, `"level":"Entity"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", ""},
		{"unknown type", strings.Replace(reclassBody, `"SignBased"`, `"FSRemap"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", ""},
		{"bad amount", strings.Replace(reclassBody, `"-800.00"`, `"-8e2"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "openItems[1].amount"},
		{"no party", strings.Replace(reclassBody, `"party":"V2"`, `"party":" "`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "openItems[1].party"},
		{"bad due date", strings.Replace(reclassBody, `"party":"V2",`, `"party":"V2","dueDate":"soon",`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "openItems[1].dueDate"},
		{"journal duty is not run", reclassBody, api.DutyJournal, http.StatusForbidden, "", ""},
	})
}

// journalBody is a USD 120,000.00 group-level audit accrual carried forward in
// a fiscal year that starts in October.
const journalBody = `{
  "periodKey":"2026-11","documentType":"Adjustment","currency":"USD",
  "reason":"Accrue group audit fee","attachmentRef":"att-77","carryForward":true,"fiscalYearStartMonth":10,
  "lines":[{"fsItem":"AUDIT_FEES","debit":"120000.00"},{"fsItem":"ACCRUALS","credit":"120000.00"}]
}`

type journalOut struct {
	Level                string
	Total                string
	Balanced             bool
	ApprovalThreshold    string
	ApprovalRequired     bool
	Periods              []string
	ContinuesPastYearEnd bool
	Warnings             []struct{ Code, Message string }
}

func TestPreviewGroupJournal_ApprovalAndCarryForward(t *testing.T) {
	rec := do(api.New(nil), groupJournalsPath, tenantID, api.DutyJournal, journalBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got journalOut
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	periods := []string{"2026-11", "2026-12", "2027-01", "2027-02", "2027-03", "2027-04", "2027-05", "2027-06", "2027-07", "2027-08", "2027-09"}
	if got.Level != "Group" || got.Total != "120000.00" || !got.Balanced || got.ApprovalThreshold != "100000.00" || !got.ApprovalRequired ||
		!reflect.DeepEqual(got.Periods, periods) || got.ContinuesPastYearEnd || len(got.Warnings) != 1 || got.Warnings[0].Code != "CNS-0909" {
		t.Fatalf("response = %+v", got)
	}

	small := strings.NewReplacer(`"120000.00"`, `"900.00"`, `"carryForward":true`, `"carryForward":false`, `"documentType":"Adjustment"`, `"documentType":"CoI","unitId":"S1"`).Replace(journalBody)
	rec = do(api.New(nil), groupJournalsPath, tenantID, api.DutyJournal, small)
	got = journalOut{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if got.Level != "Unit" || got.ApprovalRequired || len(got.Warnings) != 0 || !reflect.DeepEqual(got.Periods, []string{"2026-11"}) {
		t.Fatalf("small unit journal = %+v", got)
	}

	cumulative := strings.Replace(journalBody, `"Adjustment"`, `"Cumulative"`, 1)
	rec = do(api.New(nil), groupJournalsPath, tenantID, api.DutyJournal, strings.Replace(cumulative, `"120000.00"`, `"5.00"`, 2))
	got = journalOut{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.ContinuesPastYearEnd {
		t.Fatalf("cumulative: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPreviewGroupJournal_Errors(t *testing.T) {
	runErrors(t, groupJournalsPath, []errCase{
		{"unbalanced", strings.Replace(journalBody, `"credit":"120000.00"`, `"credit":"119000.00"`, 1), api.DutyJournal, http.StatusUnprocessableEntity, "CNS-0903", ""},
		{"no reason", strings.Replace(journalBody, `"Accrue group audit fee"`, `""`, 1), api.DutyJournal, http.StatusBadRequest, "COM-0001", "reason"},
		{"no attachment", strings.Replace(journalBody, `"att-77"`, `" "`, 1), api.DutyJournal, http.StatusBadRequest, "COM-0001", "attachmentRef"},
		{"unknown document type", strings.Replace(journalBody, `"Adjustment"`, `"Manual"`, 1), api.DutyJournal, http.StatusBadRequest, "COM-0001", "documentType"},
		{"bad period", strings.Replace(journalBody, `"2026-11"`, `"2026-13"`, 1), api.DutyJournal, http.StatusBadRequest, "COM-0001", "periodKey"},
		{"one line", strings.Replace(journalBody, `,{"fsItem":"ACCRUALS","credit":"120000.00"}`, ``, 1), api.DutyJournal, http.StatusBadRequest, "COM-0001", "lines"},
		{"both sides", strings.Replace(journalBody, `"fsItem":"ACCRUALS","credit"`, `"fsItem":"ACCRUALS","debit":"1","credit"`, 1), api.DutyJournal, http.StatusBadRequest, "COM-0001", "lines[1].debit"},
		{"sub-cent", strings.Replace(journalBody, `"120000.00"`, `"120000.001"`, 2), api.DutyJournal, http.StatusBadRequest, "COM-0001", ""},
		{"fiscal year month", strings.Replace(journalBody, `"fiscalYearStartMonth":10`, `"fiscalYearStartMonth":13`, 1), api.DutyJournal, http.StatusBadRequest, "COM-0001", "fiscalYearStartMonth"},
		{"zero threshold", strings.Replace(journalBody, `"currency":"USD",`, `"currency":"USD","approvalThreshold":"0",`, 1), api.DutyJournal, http.StatusBadRequest, "COM-0001", "approvalThreshold"},
		{"run duty is not journal", journalBody, api.DutyRun, http.StatusForbidden, "", ""},
	})
}
