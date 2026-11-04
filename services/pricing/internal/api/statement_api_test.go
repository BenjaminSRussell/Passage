package api_test

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// RebateStatementInput mirrors the Postgres read: 404 for an unknown agreement, 422 for a
// period key the agreement does not accrue by, an empty input for a period with no activity.
func (f *fakeStore) RebateStatementInput(_ context.Context, agreementID, periodKey string) (domain.RebateAgreement, domain.StatementInput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agreements[agreementID]
	if !ok {
		return a, domain.StatementInput{}, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "no rebate agreement "+agreementID)
	}
	if e := domain.ValidStatementPeriod(a, periodKey); e != nil {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, e.Code, e.Detail)
		p.Errors = e.Fields
		return a, domain.StatementInput{}, p
	}
	p, ok := f.periods[agreementID+"|"+periodKey]
	if !ok {
		return a, domain.StatementInput{Lines: []domain.StatementLine{}}, nil
	}
	in := domain.StatementInput{Period: *p, Lines: []domain.StatementLine{}}
	rows := []store.Accrual{}
	for _, acc := range f.accruals {
		if acc.AgreementID == agreementID && acc.PeriodKey == periodKey {
			rows = append(rows, acc)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].InvoiceLineID < rows[j].InvoiceLineID })
	for _, acc := range rows {
		if acc.Status == domain.StatusSettled {
			in.Settled = in.Settled.Add(acc.Accrued)
		}
		l := store.StatementLineOf(acc)
		in.RowsAccrued = in.RowsAccrued.Add(acc.Accrued)
		if l.Source() != domain.SourceRetroDelta {
			in.RowsBasisValue, in.RowsBasisQty = in.RowsBasisValue.Add(acc.BasisValue), in.RowsBasisQty.Add(acc.BasisQty)
		}
		in.Lines = append(in.Lines, l)
	}
	in.LineCount = len(rows)
	return a, in, nil
}

func getRaw(h http.Handler, path, duties string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	if duties != "" {
		req.Header.Set("X-Passage-Duties", duties)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// statementFixture accrues 3,000 and 3,000 into 2026-Q2 of a tiered quarterly agreement (1%, 2.5% from 5,000).
func statementFixture(t *testing.T) (*fakeStore, http.Handler, string) {
	t.Helper()
	fs := newFake()
	h := router(fs)
	body := `{"legalEntityId":"` + legalEntity + `","name":"Tiered","customerId":"00000000-0000-0000-0005-000000000009",
		"basis":"NET_AMOUNT","period":"QUARTER","tiers":[{"from":"0","ratePct":"1"},{"from":"5000","ratePct":"2.5"}],"currency":"USD",
		"validFrom":"2026-01-01","validTo":"2026-12-31"}`
	code, created := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, body)
	if code != http.StatusCreated {
		t.Fatalf("create agreement = %d %v", code, created)
	}
	aid := created["id"].(string)
	for _, l := range [][2]string{{"L1", "2026-04-02"}, {"L2", "2026-05-09"}} {
		if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/accruals", allDuties,
			fmt.Sprintf(`{"agreementId":"%s","invoiceLineId":"%s","invoiceDate":"%s","value":"3000","currency":"USD"}`, aid, l[0], l[1])); code != http.StatusCreated {
			t.Fatalf("accrue %s = %d %v", l[0], code, out)
		}
	}
	return fs, h, aid
}

func TestRebateStatement_HeaderLinesAndFooter(t *testing.T) {
	_, h, aid := statementFixture(t)
	code, st := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/"+aid+"/statement?period=2026-Q2", "pricing.rebate.view", "")
	if code != http.StatusOK {
		t.Fatalf("statement = %d %v", code, st)
	}
	// 6,000 reaches the 2.5% tier: 150.00 owed on the period, 30 + 120 accrued on the two lines.
	want := map[string]any{"agreementId": aid, "period": "2026-Q2", "currency": "USD", "partyRole": "CUSTOMER", "basisValue": "6000",
		"targetAmount": "150.00", "accruedAmount": "150.00", "settledAmount": "0.00", "openAmount": "150.00", "reconciled": true,
		"lineCount": float64(2), "linesTruncated": false}
	for k, v := range want {
		if st[k] != v {
			t.Errorf("%s = %v, want %v", k, st[k], v)
		}
	}
	tier := st["tier"].(map[string]any)
	if tier["from"] != "5000" || tier["ratePct"] != "2.5" {
		t.Errorf("tier = %v, want the 2.5%% tier from 5000", tier)
	}
	lines := st["lines"].([]any)
	if len(lines) != 2 || lines[0].(map[string]any)["amount"] != "30.00" || lines[1].(map[string]any)["amount"] != "120.00" {
		t.Fatalf("lines = %v, want L1 30.00 and L2 120.00", lines)
	}
}

func TestRebateStatement_SettledReducesOpen(t *testing.T) {
	fs, h, aid := statementFixture(t)
	for k, a := range fs.accruals {
		if a.InvoiceLineID == "L1" {
			a.Status = domain.StatusSettled
			fs.accruals[k] = a
		}
	}
	_, st := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/"+aid+"/statement?period=2026-Q2", "pricing.rebate.view", "")
	if st["settledAmount"] != "30.00" || st["openAmount"] != "120.00" || st["accruedAmount"] != "150.00" {
		t.Fatalf("footer = settled %v open %v accrued %v; want 30.00, 120.00, 150.00", st["settledAmount"], st["openAmount"], st["accruedAmount"])
	}
}

func TestRebateStatement_QuietPeriodIsAnEmptyStatement(t *testing.T) {
	_, h, aid := statementFixture(t)
	code, st := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/"+aid+"/statement?period=2026-Q3", "pricing.rebate.view", "")
	if code != http.StatusOK || st["accruedAmount"] != "0.00" || st["openAmount"] != "0.00" || st["reconciled"] != true || len(st["lines"].([]any)) != 0 {
		t.Fatalf("quiet period = %d %v; want an empty, reconciled statement", code, st)
	}
	if st["tier"] != nil {
		// Zero cumulative basis reaches the first tier (from 0); that is the tier in force.
		if st["tier"].(map[string]any)["from"] != "0" {
			t.Fatalf("tier = %v", st["tier"])
		}
	}
}

func TestRebateStatement_RejectsBadPeriodUnknownAgreementAndMissingDuty(t *testing.T) {
	_, h, aid := statementFixture(t)
	path := "/v1/pricing/rebates/agreements/" + aid + "/statement"
	for _, q := range []string{"", "?period=2026-04", "?period=2026", "?period=2026-Q5"} {
		if code, out := call(t, h, http.MethodGet, path+q, "pricing.rebate.view", ""); code != http.StatusUnprocessableEntity {
			t.Errorf("period %q = %d %v; a quarterly agreement takes only 2026-Qn, want 422", q, code, out)
		}
	}
	if code, out := call(t, h, http.MethodGet, path+"?period=2026-Q2&format=pdf", "pricing.rebate.view", ""); code != http.StatusUnprocessableEntity {
		t.Errorf("format pdf = %d %v, want 422", code, out)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/not-a-uuid/statement?period=2026-Q2", "pricing.rebate.view", ""); code != http.StatusNotFound {
		t.Errorf("malformed id must be 404, got %d", code)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/00000000-0000-0000-0005-999999999999/statement?period=2026-Q2", "pricing.rebate.view", ""); code != http.StatusNotFound {
		t.Errorf("unknown agreement must be 404, got %d", code)
	}
	if code, _ := call(t, h, http.MethodGet, path+"?period=2026-Q2", "pricing.view", ""); code != http.StatusForbidden {
		t.Errorf("pricing.view alone must not read a rebate statement, got %d", code)
	}
}

func TestRebateStatement_CSVCarriesLinesAndFooterRows(t *testing.T) {
	_, h, aid := statementFixture(t)
	rec := getRaw(h, "/v1/pricing/rebates/agreements/"+aid+"/statement?period=2026-Q2&format=csv", "pricing.rebate.view")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "rebate-statement-"+aid+"-2026-Q2.csv") {
		t.Fatalf("csv = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
	}
	got := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	want := []string{
		"rowType,period,invoiceLineId,invoiceDate,basisValue,basisQuantity,amount,currency,status,source",
		"LINE,2026-Q2,L1,2026-04-02,3000,0,30.00,USD,ACCRUED,INVOICE",
		"LINE,2026-Q2,L2,2026-05-09,3000,0,120.00,USD,ACCRUED,INVOICE",
		"TOTAL_ACCRUED,2026-Q2,,,,,150.00,USD,,",
		"TOTAL_SETTLED,2026-Q2,,,,,0.00,USD,,",
		"OPEN,2026-Q2,,,,,150.00,USD,,",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("csv =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRebateStatement_CSVNeutralisesFormulaInvoiceIds(t *testing.T) {
	fs, h, aid := statementFixture(t)
	for k, a := range fs.accruals {
		if a.InvoiceLineID == "L1" {
			a.InvoiceLineID = "=HYPERLINK(\"http://x\")"
			fs.accruals[k] = a
		}
	}
	rec := getRaw(h, "/v1/pricing/rebates/agreements/"+aid+"/statement?period=2026-Q2&format=csv", "pricing.rebate.view")
	if strings.Contains(rec.Body.String(), "\n=HYPERLINK") || strings.Contains(rec.Body.String(), ",=HYPERLINK") || !strings.Contains(rec.Body.String(), "'=HYPERLINK") {
		t.Fatalf("a formula-looking invoice line id must be written with a leading apostrophe:\n%s", rec.Body.String())
	}
}

func TestPriceJournalAuditExtract(t *testing.T) {
	fs := newFake()
	h := router(fs)
	id := activeRecord(t, h, "7.41")
	body := fmt.Sprintf(journalCreateBody, legalEntity, "Raise 5 percent", "true", fmt.Sprintf(updateLine, id, 2, "5"))
	_, created := call(t, h, http.MethodPost, "/v1/pricing/journals", allDuties, body)
	jid := created["id"].(string)
	path := "/v1/pricing/journals/" + jid + "/audit-extract"

	if code, out := call(t, h, http.MethodGet, path, "pricing.view", ""); code != http.StatusConflict || out["code"] != "PRC-0064" {
		t.Fatalf("an OPEN journal has no audit extract: got %d %v, want 409 PRC-0064", code, out)
	}
	call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":validate", allDuties, "")
	call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":post", allDuties, "")

	rec := getRaw(h, path, "pricing.view")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	rows := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want header and one line:\n%s", len(rows), rec.Body.String())
	}
	header, line := strings.Split(rows[0], ","), strings.Split(rows[1], ",")
	if len(header) != len(line) {
		t.Fatalf("header has %d columns, the line %d", len(header), len(line))
	}
	cell := func(name string) string {
		for i, h := range header {
			if h == name {
				return line[i]
			}
		}
		t.Fatalf("no column %s", name)
		return ""
	}
	for name, want := range map[string]string{"journalId": jid, "journalStatus": "POSTED", "lineNo": "1", "op": "UPDATE", "recordId": id,
		"beforeAmount": "7.41", "afterAmount": "7.99", "beforeStatus": "ACTIVE", "afterVersion": "3", "type": "PRICE", "adjustPct": "5"} {
		if got := cell(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	code, js := call(t, h, http.MethodGet, path+"?format=json", "pricing.view", "")
	lines, _ := js["lines"].([]any)
	if code != http.StatusOK || js["status"] != "POSTED" || len(lines) != 1 || lines[0].(map[string]any)["after"].(map[string]any)["amount"] != "7.99" {
		t.Fatalf("json = %d %v", code, js)
	}

	// A reversed journal still has its extract, and the reversal links both ways.
	call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":reverse", allDuties, "")
	_, again := call(t, h, http.MethodGet, path+"?format=json", "pricing.view", "")
	if again["status"] != "REVERSED" || again["reversedByJournalId"] == nil {
		t.Fatalf("reversed extract = %v", again)
	}
	if code, _ := call(t, h, http.MethodGet, path+"?format=xml", "pricing.view", ""); code != http.StatusUnprocessableEntity {
		t.Errorf("format xml must be 422, got %d", code)
	}
	if code, _ := call(t, h, http.MethodGet, path, "pricing.rebate.view", ""); code != http.StatusForbidden {
		t.Errorf("pricing.view is required, got %d", code)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/journals/00000000-0000-0000-0004-999999999999/audit-extract", "pricing.view", ""); code != http.StatusNotFound {
		t.Errorf("unknown journal must be 404, got %d", code)
	}
}

// A retroactive delta row stores the period's cumulative basis; the statement shows it as a
// RETRO_DELTA line with no basis of its own and keeps it out of the basis check.
func TestRebateStatement_RetroDeltaLineHasNoBasisOfItsOwn(t *testing.T) {
	fs, h, aid := statementFixture(t)
	fs.accruals["delta"] = store.Accrual{ID: "delta", AgreementID: aid, PeriodKey: "2026-Q2", InvoiceLineID: "retro:run-1:2026-Q2",
		InvoiceDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), BasisValue: decimal.NewFromInt(6000), Accrued: decimal.Zero,
		Currency: "USD", Status: "ACCRUED", Retroactive: true}
	code, st := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/"+aid+"/statement?period=2026-Q2", "pricing.rebate.view", "")
	if code != http.StatusOK || st["reconciled"] != true {
		t.Fatalf("statement = %d %v; a zero delta keeps the period reconciled", code, st)
	}
	var delta map[string]any
	for _, l := range st["lines"].([]any) {
		if m := l.(map[string]any); m["invoiceLineId"] == "retro:run-1:2026-Q2" {
			delta = m
		}
	}
	if delta == nil || delta["source"] != "RETRO_DELTA" || delta["basisValue"] != nil || delta["basisQuantity"] != nil {
		t.Fatalf("delta line = %v; want source RETRO_DELTA and no basis", delta)
	}
	rec := getRaw(h, "/v1/pricing/rebates/agreements/"+aid+"/statement?period=2026-Q2&format=csv", "pricing.rebate.view")
	if !strings.Contains(rec.Body.String(), "LINE,2026-Q2,retro:run-1:2026-Q2,2026-06-01,,,0.00,USD,ACCRUED,RETRO_DELTA\n") {
		t.Fatalf("csv delta row missing or carries a basis:\n%s", rec.Body.String())
	}
}

// reconciled compares the tier's amount on the whole period with the accrual rows, not with
// the period row (which is written as that amount): a row that drifted shows as unreconciled.
func TestRebateStatement_RowsThatDoNotAddUpAreNotReconciled(t *testing.T) {
	fs, h, aid := statementFixture(t)
	for k, a := range fs.accruals {
		if a.InvoiceLineID == "L2" {
			a.Accrued = decimal.RequireFromString("119.99")
			fs.accruals[k] = a
		}
	}
	_, st := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/"+aid+"/statement?period=2026-Q2", "pricing.rebate.view", "")
	if st["reconciled"] != false || st["accruedAmount"] != "150.00" || st["linesAccruedAmount"] != "149.99" || st["targetAmount"] != "150.00" {
		t.Fatalf("drifted rows = %v; want reconciled false, accrued 150.00, lines 149.99", st)
	}
}

// postedJournal posts a two-line journal (5% and -5%) and returns its id and audit path.
func postedJournal(t *testing.T, fs *fakeStore, h http.Handler) (string, string) {
	t.Helper()
	up := activeRecord(t, h, "7.41")
	code, rec := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties,
		strings.Replace(fmt.Sprintf(recordBody, legalEntity, "10.00"), `"productKey":"P1"`, `"productKey":"P2"`, 1))
	if code != http.StatusCreated {
		t.Fatalf("create second record = %d %v", code, rec)
	}
	down := rec["id"].(string)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+down+":activate", allDuties, `{"version":1}`); code != http.StatusOK {
		t.Fatalf("activate second record = %d %v", code, out)
	}
	lines := fmt.Sprintf(updateLine, up, 2, "5") + "," + fmt.Sprintf(updateLine, down, 2, "-5")
	_, created := call(t, h, http.MethodPost, "/v1/pricing/journals", allDuties, fmt.Sprintf(journalCreateBody, legalEntity, "=Two lines", "false", lines))
	jid := created["id"].(string)
	call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":validate", allDuties, "")
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/journals/"+jid+":post", allDuties, ""); code != http.StatusOK {
		t.Fatalf("post = %d %v", code, out)
	}
	return jid, "/v1/pricing/journals/" + jid + "/audit-extract"
}

// The audit extract uses the export's formula guard: text that could run as a formula gets a
// leading apostrophe, numbers (a negative adjustment too) are written as they are.
func TestPriceJournalAuditExtract_FormulaGuardLeavesNumbersAlone(t *testing.T) {
	fs := newFake()
	h := router(fs)
	jid, path := postedJournal(t, fs, h)
	fs.journals[jid].Lines[1].After.Variant = "@SUM(A1)"
	rec := getRaw(h, path, "pricing.view")
	r := csv.NewReader(strings.NewReader(rec.Body.String()))
	rows, err := r.ReadAll()
	if err != nil || len(rows) != 3 {
		t.Fatalf("csv = %v %d rows:\n%s", err, len(rows), rec.Body.String())
	}
	col := map[string]int{}
	for i, name := range rows[0] {
		col[name] = i
	}
	if got := rows[1][col["journalName"]]; got != "'=Two lines" {
		t.Errorf("journalName = %q, want the formula guard", got)
	}
	if got := rows[2][col["variant"]]; got != "'@SUM(A1)" {
		t.Errorf("variant = %q, want the formula guard", got)
	}
	if got := rows[2][col["adjustPct"]]; got != "-5" {
		t.Errorf("adjustPct = %q, want -5 untouched", got)
	}
	if got := rows[2][col["afterAmount"]]; got != "9.50" && got != "9.5" {
		t.Errorf("afterAmount = %q, want 9.5", got)
	}
}

// A read that fails after the 200 aborts the connection rather than ending a short file
// cleanly; a failure before the 200 is an ordinary problem response.
func TestPriceJournalAuditExtract_MidStreamFailureAbortsTheConnection(t *testing.T) {
	fs := newFake()
	h := router(fs)
	_, path := postedJournal(t, fs, h)
	fs.streamErr = errors.New("connection reset by database")
	func() {
		defer func() {
			if v := recover(); v != http.ErrAbortHandler {
				t.Fatalf("recovered %v, want http.ErrAbortHandler", v)
			}
		}()
		getRaw(h, path, "pricing.view")
	}()
	// JSON is written in one piece after the read, so the same failure is a 500 problem.
	if rec := getRaw(h, path+"?format=json", "pricing.view"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("json after a failed read = %d, want 500", rec.Code)
	}
}
