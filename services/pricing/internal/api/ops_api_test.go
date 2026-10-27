package api_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	expireURL     = "/v1/pricing/condition-records:expire"
	workspaceURL  = "/v1/pricing/workspace"
	settlementURL = "/v1/pricing/commission/settlement-export"
	importURL     = "/v1/pricing/journals:import-csv"
)

// callRaw is call for responses that are not JSON (the settlement CSV).
func callRaw(t *testing.T, h http.Handler, method, path, duties string) (int, string, http.Header) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	if duties != "" {
		req.Header.Set("X-Passage-Duties", duties)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

// activeOpsRecord creates and activates a PRICE record valid validFrom..validTo.
func activeOpsRecord(t *testing.T, h http.Handler, customer, validFrom, validTo string) string {
	t.Helper()
	body := fmt.Sprintf(`{"legalEntityId":%q,"type":"PRICE","customerKind":"customer","customerKey":%q,"productKind":"product",
		"productKey":"P1","currency":"USD","unit":"EA","fromQuantity":"0","amount":"10","validFrom":%q,"validTo":%q}`, legalEntity, customer, validFrom, validTo)
	code, rec := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, body)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, rec)
	}
	id := rec["id"].(string)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+id+":activate", allDuties, `{"version":1}`); code != http.StatusOK {
		t.Fatalf("activate = %d %v", code, out)
	}
	return id
}

// BR-26.9: the nightly job expires the ACTIVE records whose validity ended before asOf, keeps
// the one still valid on asOf (validity is inclusive), and a second run expires nothing.
func TestExpireRecords_ExpiresEndedValidityOnce(t *testing.T) {
	fs := newFake()
	h := router(fs)
	ended := activeOpsRecord(t, h, "C1", "2026-01-01", "2026-03-19")
	lastDay := activeOpsRecord(t, h, "C2", "2026-01-01", "2026-03-20")
	body := fmt.Sprintf(`{"legalEntityId":%q,"asOf":"2026-03-20"}`, legalEntity)

	code, out := call(t, h, http.MethodPost, expireURL, allDuties, body)
	if code != http.StatusOK || out["expired"] != float64(1) || fmt.Sprint(out["recordIds"]) != "["+ended+"]" {
		t.Fatalf("expire = %d %v; want only %s", code, out, ended)
	}
	if fs.records[ended].Status != domain.StatusExpired || fs.records[ended].Version != 3 {
		t.Fatalf("ended record = %s v%d; want EXPIRED v3", fs.records[ended].Status, fs.records[ended].Version)
	}
	if fs.records[lastDay].Status != domain.StatusActive {
		t.Fatalf("a record valid through asOf must stay ACTIVE, got %s", fs.records[lastDay].Status)
	}
	if code, out := call(t, h, http.MethodPost, expireURL, allDuties, body); code != http.StatusOK || out["expired"] != float64(0) {
		t.Fatalf("second run = %d %v; want nothing expired", code, out)
	}
}

func TestExpireRecords_ValidationAndDuty(t *testing.T) {
	h := router(newFake())
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format(time.DateOnly)
	future := time.Now().UTC().AddDate(0, 0, 3).Format(time.DateOnly)
	cases := map[string]struct{ body, field string }{
		"malformed entity": {`{"legalEntityId":"nope","asOf":"2026-03-20"}`, "legalEntityId"},
		"malformed date":   {fmt.Sprintf(`{"legalEntityId":%q,"asOf":"yesterday"}`, legalEntity), "asOf"},
		"future date":      {fmt.Sprintf(`{"legalEntityId":%q,"asOf":%q}`, legalEntity, future), "asOf"},
	}
	for name, tc := range cases {
		code, out := call(t, h, http.MethodPost, expireURL, allDuties, tc.body)
		if errs, _ := out["errors"].(map[string]any); code != http.StatusUnprocessableEntity || errs[tc.field] == nil {
			t.Errorf("%s: %d %v; want 422 naming %s", name, code, out, tc.field)
		}
	}
	if code, out := call(t, h, http.MethodPost, expireURL, allDuties, fmt.Sprintf(`{"legalEntityId":%q,"asOf":%q}`, legalEntity, tomorrow)); code != http.StatusOK {
		t.Fatalf("tomorrow (entities east of UTC) must be accepted, got %d %v", code, out)
	}
	if code, _ := call(t, h, http.MethodPost, expireURL, "pricing.view", fmt.Sprintf(`{"legalEntityId":%q,"asOf":"2026-03-20"}`, legalEntity)); code != http.StatusForbidden {
		t.Fatalf("a viewer must not run expiry, got %d", code)
	}
}

func TestWorkspace_ReturnsTiles(t *testing.T) {
	fs := &opsFake{fakeStore: newFake(), summary: store.WorkspaceSummary{Month: "2027-03", ActiveRecords: 140, DraftRecords: 3,
		ExpiringIn30Days: 4, JournalsToPost: 2, LiveCampaigns: 1, ClaimsAwaitingApproval: 1,
		AccrualsThisMonth: []store.AccrualTotal{{PartyRole: domain.PartyCustomer, Currency: "USD", Amount: decimal.RequireFromString("600.00")}}}}
	code, out := call(t, router(fs), http.MethodGet, workspaceURL+"?legalEntityId="+legalEntity+"&asOf=2027-03-20", "pricing.view", "")
	if code != http.StatusOK {
		t.Fatalf("workspace = %d %v", code, out)
	}
	if out["month"] != "2027-03" || out["activeRecords"] != float64(140) || out["expiringIn30Days"] != float64(4) ||
		out["journalsToPost"] != float64(2) || out["liveCampaigns"] != float64(1) || out["claimsAwaitingApproval"] != float64(1) || out["asOf"] != "2027-03-20" {
		t.Fatalf("tiles = %v", out)
	}
	accruals := out["accrualsThisMonth"].([]any)
	if len(accruals) != 1 || accruals[0].(map[string]any)["amount"] != "600" || accruals[0].(map[string]any)["partyRole"] != "CUSTOMER" {
		t.Fatalf("accruals = %v", accruals)
	}
	if fs.summaryLE != legalEntity || fs.summaryAsOf.Format(time.DateOnly) != "2027-03-20" {
		t.Fatalf("store saw %q %v", fs.summaryLE, fs.summaryAsOf)
	}
}

func TestWorkspace_AllEntitiesTodayAndValidation(t *testing.T) {
	fs := &opsFake{fakeStore: newFake(), summary: store.WorkspaceSummary{AccrualsThisMonth: []store.AccrualTotal{}}}
	h := router(fs)
	code, out := call(t, h, http.MethodGet, workspaceURL, "pricing.view", "")
	if code != http.StatusOK || fs.summaryLE != "" || fs.summaryAsOf.Format(time.DateOnly) != time.Now().UTC().Format(time.DateOnly) {
		t.Fatalf("no filter = %d %v, store saw %q %v; want every entity today", code, out, fs.summaryLE, fs.summaryAsOf)
	}
	if code, _ := call(t, h, http.MethodGet, workspaceURL+"?legalEntityId=x", "pricing.view", ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed legalEntityId must be 422, got %d", code)
	}
	if code, _ := call(t, h, http.MethodGet, workspaceURL+"?asOf=03/20/2027", "pricing.view", ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed asOf must be 422, got %d", code)
	}
	if code, _ := call(t, h, http.MethodGet, workspaceURL, "pricing.rebate.view", ""); code != http.StatusForbidden {
		t.Fatalf("the workspace needs pricing.view, got %d", code)
	}
}

func TestSettlementExport_IsCSVPerRep(t *testing.T) {
	fs := &opsFake{fakeStore: newFake(), rows: []domain.SettlementRow{
		{RepID: "R1", Currency: "USD", Amount: decimal.RequireFromString("100"), Transactions: 3},
		{RepID: "R2", Currency: "USD", Amount: decimal.RequireFromString("40"), Transactions: 2},
	}}
	path := settlementURL + "?legalEntityId=" + legalEntity + "&from=2027-03-01&to=2027-03-31&target=PAYROLL"
	code, raw, hdr := callRaw(t, router(fs), http.MethodGet, path, "pricing.commission.view")
	if code != http.StatusOK || !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") ||
		!strings.Contains(hdr.Get("Content-Disposition"), "commission-settlement-PAYROLL-2027-03-01-2027-03-31.csv") {
		t.Fatalf("export = %d %q %q", code, hdr.Get("Content-Type"), hdr.Get("Content-Disposition"))
	}
	recs, err := csv.NewReader(bytes.NewReader([]byte(raw))).ReadAll()
	if err != nil || len(recs) != 3 {
		t.Fatalf("csv = %q, %v", raw, err)
	}
	if strings.Join(recs[1], ",") != "PAYROLL,R1,USD,100.00,3,2027-03-01,2027-03-31" || recs[2][3] != "40.00" {
		t.Fatalf("rows = %q", recs[1:])
	}
	if fs.settleLE != legalEntity || fs.settleFrom.Format(time.DateOnly) != "2027-03-01" || fs.settleTo.Format(time.DateOnly) != "2027-03-31" {
		t.Fatalf("store saw %q %v..%v", fs.settleLE, fs.settleFrom, fs.settleTo)
	}
	// Without a target the export goes to AP, the commission_settlement_target default.
	code, raw, _ = callRaw(t, router(fs), http.MethodGet, settlementURL+"?legalEntityId="+legalEntity+"&from=2027-03-01&to=2027-03-31", "pricing.commission.view")
	if code != http.StatusOK || !strings.Contains(raw, "\nAP,R1,") {
		t.Fatalf("default target = %d %q", code, raw)
	}
}

func TestSettlementExport_ValidationAndDuty(t *testing.T) {
	h := router(newFake())
	base := settlementURL + "?legalEntityId=" + legalEntity
	cases := map[string]struct{ path, field string }{
		"NONE is not a target": {base + "&from=2027-03-01&to=2027-03-31&target=NONE", "target"},
		"reversed period":      {base + "&from=2027-03-31&to=2027-03-01", "to"},
		"over a year":          {base + "&from=2026-01-01&to=2027-03-01", "to"},
		"missing from":         {base + "&to=2027-03-01", "from"},
		"malformed entity":     {settlementURL + "?legalEntityId=x&from=2027-03-01&to=2027-03-31", "legalEntityId"},
	}
	for name, tc := range cases {
		code, out := call(t, h, http.MethodGet, tc.path, "pricing.commission.view", "")
		if errs, _ := out["errors"].(map[string]any); code != http.StatusUnprocessableEntity || errs[tc.field] == nil {
			t.Errorf("%s: %d %v; want 422 naming %s", name, code, out, tc.field)
		}
	}
	if code, _, _ := callRaw(t, h, http.MethodGet, base+"&from=2027-03-01&to=2027-03-31", "pricing.view"); code != http.StatusForbidden {
		t.Fatalf("export without pricing.commission.view must be 403, got %d", code)
	}
}

const csvHeader = "op,recordId,expectedVersion,adjustPct,type,customerKind,customerKey,productKind,productKey,currency,unit,fromQuantity,amount,validFrom,validTo,searchNext,exclusive"

func importJournal(t *testing.T, h http.Handler, query, body string, headers ...string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, importURL+"?"+query, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/csv")
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", "pricing.journal.post,pricing.view")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// FR-26.9 CSV import: CREATE, UPDATE by percentage and DELETE rows become journal lines 1-3
// in file order, with the header (name, smart rounding) from the query.
func TestJournalCSVImport_CreatesJournalInFileOrder(t *testing.T) {
	fs := newFake()
	h := router(fs)
	existing := activeOpsRecord(t, h, "C9", "2026-01-01", "2026-12-31")
	body := "\uFEFF" + csvHeader + "\n" +
		"CREATE,,,,PRICE,customer,C1,product,P1,USD,EA,0,5.25,2026-01-01,2026-12-31,true,false\n" +
		"UPDATE," + existing + ",2,4.5" + strings.Repeat(",", 13) + "\n" +
		"delete," + existing + ",2" + strings.Repeat(",", 14) + "\n"
	code, out := importJournal(t, h, "legalEntityId="+legalEntity+"&name=Spring+import&smartRound=true", body)
	if code != http.StatusCreated || out["name"] != "Spring import" || out["smartRound"] != true || out["lineCount"] != float64(3) || out["status"] != "OPEN" {
		t.Fatalf("import = %d %v; want an OPEN 3-line journal", code, out)
	}
	j := fs.journals[out["id"].(string)]
	l := j.Lines
	if l[0].Op != domain.OpCreate || !l[0].Record.Amount.Equal(decimal.RequireFromString("5.25")) || !l[0].Record.SearchNext || l[0].Record.Unit != "EA" ||
		l[1].Op != domain.OpUpdate || l[1].RecordID != existing || l[1].ExpectedVersion != 2 || !l[1].AdjustPct.Equal(decimal.RequireFromString("4.5")) ||
		l[2].Op != domain.OpDelete || l[2].RecordID != existing {
		t.Fatalf("lines = %+v", l)
	}
}

func TestJournalCSVImport_RefusesBadFiles(t *testing.T) {
	h := router(newFake())
	q := "legalEntityId=" + legalEntity + "&name=Bad"
	cases := map[string]struct {
		csv   string
		field string
	}{
		"unknown column":     {"op,colour\nCREATE,red\n", "csv.header"},
		"repeated column":    {"op,amount,amount\nCREATE,1,2\n", "csv.header"},
		"no op column":       {"type,amount\nPRICE,5\n", "csv.header"},
		"no header at all":   {"", "csv"},
		"ragged row":         {csvHeader + "\nCREATE,1\n", "csv"},
		"bad version":        {csvHeader + "\nUPDATE,00000000-0000-0000-0002-000000000001,one,5" + strings.Repeat(",", 13) + "\n", "rows[1].expectedVersion"},
		"bad boolean":        {csvHeader + "\nCREATE,,,,PRICE,customer,C1,product,P1,USD,EA,0,5,2026-01-01,2026-12-31,maybe,false\n", "rows[1].searchNext"},
		"header only":        {csvHeader + "\n", "lines"},
		"record field error": {csvHeader + "\nCREATE,,,,PRICE,customer,C1,product,P1,USD,EA,0,-5,2026-01-01,2026-12-31,false,false\n", "lines[0].record"},
		"bad op":             {csvHeader + "\nMERGE" + strings.Repeat(",", 16) + "\n", "lines[0].op"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, out := importJournal(t, h, q, tc.csv)
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422: %v", code, out)
			}
			if !strings.Contains(fmt.Sprint(out), tc.field) {
				t.Fatalf("problem must name %q: %v", tc.field, out)
			}
		})
	}
	if code, out := importJournal(t, h, "name=NoEntity", csvHeader+"\nCREATE,,,,PRICE,customer,C1,product,P1,USD,EA,0,5,2026-01-01,2026-12-31,false,false\n"); code != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(out), "legalEntityId") {
		t.Fatalf("missing legalEntityId = %d %v", code, out)
	}
}

// A file with more faulty rows than the cap lists the first 100 and says how many there are.
func TestJournalCSVImport_CapsRowFaults(t *testing.T) {
	var b strings.Builder
	b.WriteString(csvHeader + "\n")
	for i := 0; i < 150; i++ {
		b.WriteString("UPDATE,x,v" + strings.Repeat(",", 14) + "\n")
	}
	code, out := importJournal(t, router(newFake()), "legalEntityId="+legalEntity+"&name=Many", b.String())
	errs, _ := out["errors"].(map[string]any)
	if code != http.StatusUnprocessableEntity || errs["rows[100].expectedVersion"] == nil || errs["rows[101].expectedVersion"] != nil ||
		!strings.Contains(fmt.Sprint(errs["csv"]), "150 rows") {
		t.Fatalf("faults = %d, %d keys, csv=%v", code, len(errs), errs["csv"])
	}
}

func TestJournalCSVImport_RefusesOversizedFilesAndBadFlags(t *testing.T) {
	h := router(newFake())
	big := csvHeader + "\n" + strings.Repeat("DELETE,"+strings.Repeat("x", 400)+",1"+strings.Repeat(",", 14)+"\n", (33<<20)/400)
	if code, out := importJournal(t, h, "legalEntityId="+legalEntity+"&name=Big", big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a file over 32 MB = %d %v; want 413", code, out)
	}
	code, out := importJournal(t, h, "legalEntityId="+legalEntity+"&name=X&smartRound=maybe", csvHeader+"\n")
	if errs, _ := out["errors"].(map[string]any); code != http.StatusUnprocessableEntity || errs["smartRound"] == nil {
		t.Fatalf("smartRound=maybe = %d %v; want 422 naming smartRound", code, out)
	}
}

func TestJournalCSVImport_NeedsJournalPostDuty(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, importURL+"?legalEntityId="+legalEntity+"&name=X", strings.NewReader(csvHeader+"\n"))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", "pricing.maintain")
	rec := httptest.NewRecorder()
	router(newFake()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("import without pricing.journal.post = %d; want 403", rec.Code)
	}
}
