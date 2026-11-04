package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func statementURL(rep, query string) string {
	return "/v1/pricing/commission/reps/" + rep + "/statement?legalEntityId=" + legalEntity + "&" + query
}

// statementRaw reads a statement as a caller scoped to legalEntity with pricing.commission.view.
func statementRaw(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", "pricing.commission.view")
	req.Header.Set("X-Passage-Legal-Entity", legalEntity)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// postedCommission posts INV-1 (10,000.00 at 5%: R1 300.00, R2 200.00) and a credit note of 2,000.00
// against it (R1 -60.00, R2 -40.00) in March 2027.
func postedCommission(t *testing.T) (*fakeStore, http.Handler) {
	t.Helper()
	fs := newFake()
	h := router(fs)
	sg := seedCommission(t, h)
	for _, body := range []string{posting(sg, "INV-1", "", "10000.00"), posting(sg, "CN-1", "INV-1", "-2000.00")} {
		if code, out := call(t, h, http.MethodPost, postingsURL, allDuties, body); code != http.StatusCreated {
			t.Fatalf("post = %d %v", code, out)
		}
	}
	return fs, h
}

func TestCommissionStatement_LinesAndFooterPerCurrency(t *testing.T) {
	_, h := postedCommission(t)
	code, st := call(t, h, http.MethodGet, statementURL("R1", "period=2027-03"), "pricing.commission.view", "", inEntity...)
	if code != http.StatusOK {
		t.Fatalf("statement = %d %v", code, st)
	}
	if st["repId"] != "R1" || st["period"] != "2027-03" || st["lineCount"] != float64(2) || st["linesTruncated"] != false {
		t.Fatalf("header = %v", st)
	}
	totals := st["totals"].([]any)
	if len(totals) != 1 {
		t.Fatalf("totals = %v, want one currency", totals)
	}
	tot := totals[0].(map[string]any)
	if tot["currency"] != "USD" || tot["earnedAmount"] != "300.00" || tot["creditedAmount"] != "-60.00" || tot["netAmount"] != "240.00" {
		t.Fatalf("totals = %v, want earned 300.00 credited -60.00 net 240.00", tot)
	}
	lines := st["lines"].([]any)
	if len(lines) != 2 || lines[0].(map[string]any)["repId"] != "R1" {
		t.Fatalf("lines = %v, want R1's two transactions only", lines)
	}
}

func TestCommissionStatement_QuietMonthIsEmptyAndOtherRepSeesNothing(t *testing.T) {
	_, h := postedCommission(t)
	for _, q := range []string{statementURL("R1", "period=2027-04"), statementURL("R9", "period=2027-03")} {
		code, st := call(t, h, http.MethodGet, q, "pricing.commission.view", "", inEntity...)
		if code != http.StatusOK || st["lineCount"] != float64(0) || len(st["lines"].([]any)) != 0 || len(st["totals"].([]any)) != 0 {
			t.Errorf("%s = %d %v; want an empty statement", q, code, st)
		}
	}
}

func TestCommissionStatement_Validation(t *testing.T) {
	_, h := postedCommission(t)
	for _, c := range []struct{ name, url string }{
		{"missing period", statementURL("R1", "")},
		{"bad period", statementURL("R1", "period=2027-13")},
		{"bad format", statementURL("R1", "period=2027-03&format=pdf")},
		{"missing legal entity", "/v1/pricing/commission/reps/R1/statement?period=2027-03"},
		{"long rep id", statementURL(strings.Repeat("x", 65), "period=2027-03")},
		{"rep id with a colon", statementURL("worker:7", "period=2027-03")},
		{"rep id dot", statementURL(".", "period=2027-03")},
	} {
		if code, out := call(t, h, http.MethodGet, c.url, "pricing.commission.view", "", inEntity...); code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %v, want 422", c.name, code, out)
		}
	}
	for _, duties := range []string{"pricing.view", "pricing.commission.maintain"} {
		if code, _ := call(t, h, http.MethodGet, statementURL("R1", "period=2027-03"), duties, "", inEntity...); code != http.StatusForbidden {
			t.Errorf("duty %s alone = %d, want 403", duties, code)
		}
	}
}

func TestCommissionStatement_CSVLinesAndFooterRows(t *testing.T) {
	_, h := postedCommission(t)
	rec := statementRaw(h, statementURL("R2", "period=2027-03&format=csv"))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "commission-statement-2027-03.csv") {
		t.Fatalf("csv = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
	}
	rows := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(rows) != 6 || !strings.HasPrefix(rows[0], "rowType,period,repId,invoiceLineId,") {
		t.Fatalf("csv =\n%s\nwant a header, two lines and three footer rows", rec.Body.String())
	}
	for i, want := range []string{"TOTAL_EARNED,2027-03,R2,", "TOTAL_CREDITED,2027-03,R2,", "TOTAL_NET,2027-03,R2,"} {
		if !strings.HasPrefix(rows[3+i], want) {
			t.Errorf("footer row %d = %q, want prefix %q", i, rows[3+i], want)
		}
	}
	if !strings.Contains(rows[3], ",200.00,USD,") || !strings.Contains(rows[4], ",-40.00,USD,") || !strings.Contains(rows[5], ",160.00,USD,") {
		t.Errorf("footer = %v, want 200.00, -40.00, 160.00", rows[3:])
	}
	if !strings.HasPrefix(rows[1], "LINE,2027-03,R2,INV-1,") || !strings.HasPrefix(rows[2], "LINE,2027-03,R2,CN-1,INV-1,") {
		t.Errorf("lines = %v", rows[1:3])
	}
}

func TestCommissionStatement_CSVNeutralisesFormulaIds(t *testing.T) {
	fs, h := postedCommission(t)
	txns := fs.commission().txns
	txns[0].InvoiceLineID, txns[0].CustomerID = "=HYPERLINK(\"http://x\")", "@cmd"
	rec := statementRaw(h, statementURL("R1", "period=2027-03&format=csv"))
	body := rec.Body.String()
	if strings.Contains(body, ",=HYPERLINK") || strings.Contains(body, ",@cmd") || !strings.Contains(body, "'=HYPERLINK") {
		t.Fatalf("csv does not neutralise formula-like ids:\n%s", body)
	}
}

// The JSON statement stops at 5,000 lines but its totals still cover every transaction; the CSV has them all.
func TestCommissionStatement_JSONTruncatesCSVDoesNot(t *testing.T) {
	fs, h := postedCommission(t)
	c := fs.commission()
	base := c.txns[0]
	for i := 0; i < 5001; i++ {
		x := base
		x.RepID, x.Amount, x.InvoiceDate = "BULK", decimal.RequireFromString("1.00"), time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
		c.txns = append(c.txns, x)
	}
	code, st := call(t, h, http.MethodGet, statementURL("BULK", "period=2027-03"), "pricing.commission.view", "", inEntity...)
	if code != http.StatusOK || st["linesTruncated"] != true || len(st["lines"].([]any)) != 5000 || st["lineCount"] != float64(5001) ||
		st["totals"].([]any)[0].(map[string]any)["netAmount"] != "5001.00" {
		t.Fatalf("json = %d truncated %v lines %d count %v; want 5000 of 5001 with a net of 5001.00", code, st["linesTruncated"], len(st["lines"].([]any)), st["lineCount"])
	}
	rec := statementRaw(h, statementURL("BULK", "period=2027-03&format=csv"))
	if n := strings.Count(rec.Body.String(), "\nLINE,"); n != 5001 {
		t.Fatalf("csv has %d lines, want all 5001", n)
	}
}

// A read that fails after the 200 aborts the connection rather than ending a short file cleanly.
func TestCommissionStatement_MidStreamFailureAbortsTheConnection(t *testing.T) {
	fs, h := postedCommission(t)
	fs.streamErr = errors.New("connection reset by database")
	func() {
		defer func() {
			if v := recover(); v != http.ErrAbortHandler {
				t.Fatalf("recovered %v, want http.ErrAbortHandler", v)
			}
		}()
		statementRaw(h, statementURL("R1", "period=2027-03&format=csv"))
	}()
	if rec := statementRaw(h, statementURL("R1", "period=2027-03")); rec.Code != http.StatusInternalServerError {
		t.Fatalf("json after a failed read = %d, want 500", rec.Code)
	}
}

// A legal entity outside the caller's scope is refused, not shown as a quiet month (row security
// would hide its rows); the CSV is refused before anything is written.
func TestCommissionStatement_LegalEntityOutsideScopeIs403(t *testing.T) {
	_, h := postedCommission(t)
	outside := []string{"X-Passage-Legal-Entity", otherEntity}
	for _, format := range []string{"json", "csv"} {
		code, out := call(t, h, http.MethodGet, statementURL("R1", "period=2027-03&format="+format), "pricing.commission.view", "", outside...)
		if code != http.StatusForbidden || out["code"] != "AUTH-0003" {
			t.Errorf("%s outside the scope = %d %v; want 403 AUTH-0003", format, code, out)
		}
	}
	if code, st := call(t, h, http.MethodGet, statementURL("R1", "period=2027-03"), "pricing.commission.view", "", inEntity...); code != http.StatusOK || st["lineCount"] != float64(2) {
		t.Fatalf("in scope = %d %v; want R1's two lines", code, st)
	}
}
