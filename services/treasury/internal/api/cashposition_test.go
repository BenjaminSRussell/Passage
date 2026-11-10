package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

type fakeCash struct {
	imported []store.IntradayImport
	replay   bool
	err      error
	accounts []store.PositionAccount
}

func (f *fakeCash) ImportIntradayReport(_ context.Context, in store.IntradayImport) (store.IntradayResult, error) {
	if f.err != nil {
		return store.IntradayResult{}, f.err
	}
	f.imported = append(f.imported, in)
	return store.IntradayResult{Replayed: f.replay, Report: store.IntradayReport{ID: ids.New(), BankAccountID: in.BankAccountID, ReportID: in.Parsed.ReportID, Format: in.Parsed.Format,
		Currency: in.Parsed.Currency, CreatedAtBank: in.Parsed.CreatedAt, Balances: []store.IntradayBalance{{Code: "ITBD", Amount: "1250.5"}}}}, nil
}

func (f *fakeCash) CashPositionAccounts(context.Context, string) ([]store.PositionAccount, error) {
	return f.accounts, nil
}

func cashServer(t *testing.T, c CashPositions, now time.Time) http.Handler {
	t.Helper()
	r := httpx.NewRouter()
	a := New(newFakeStore())
	a.now = func() time.Time { return now }
	if c != nil {
		a.WithCashPositions(c)
	}
	a.Register(r)
	return r
}

func multipartReport(t *testing.T, accountID string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("bankAccountId", accountID)
	part, err := w.CreateFormFile("file", "report.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

func TestImportIntradayReport_StoresReplaysAndRejects(t *testing.T) {
	data, err := os.ReadFile("../../testdata/camt052/sample-08.xml")
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeCash{}
	h := cashServer(t, fc, time.Now())
	acct := ids.New()
	body, ct := multipartReport(t, acct, data)
	rec := call(h, http.MethodPost, "/v1/treasury/intraday-reports/import", body, ct, nil)
	if rec.Code != http.StatusCreated || len(fc.imported) != 1 {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	if got := fc.imported[0]; got.BankAccountID != acct || got.Parsed.ReportID != "RPT-20261022-1400" || len(got.FileHash) != 64 {
		t.Fatalf("import passed %+v", got)
	}
	fc.replay = true
	body, ct = multipartReport(t, acct, data)
	if rec := call(h, http.MethodPost, "/v1/treasury/intraday-reports/import", body, ct, nil); rec.Code != http.StatusOK {
		t.Fatalf("replay: %d, want 200", rec.Code)
	}
	// A camt.053 file is not a camt.052 report.
	st, _ := os.ReadFile("../../testdata/camt053/sample-08.xml")
	body, ct = multipartReport(t, acct, st)
	rec = call(h, http.MethodPost, "/v1/treasury/intraday-reports/import", body, ct, nil)
	if rec.Code != http.StatusUnprocessableEntity || problemCodeOf(t, rec.Body.Bytes()) != domain.CodeStatementFormat {
		t.Fatalf("wrong message type: %d %s", rec.Code, rec.Body)
	}
	body, ct = multipartReport(t, "not-a-uuid", data)
	if rec := call(h, http.MethodPost, "/v1/treasury/intraday-reports/import", body, ct, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad account id: %d, want 400", rec.Code)
	}
	fc.err = httpx.NewProblem(http.StatusConflict, store.CodeReportConflict, "other content")
	body, ct = multipartReport(t, acct, data)
	if rec := call(h, http.MethodPost, "/v1/treasury/intraday-reports/import", body, ct, nil); rec.Code != http.StatusConflict {
		t.Fatalf("store conflict: %d, want 409", rec.Code)
	}
}

func problemCodeOf(t *testing.T, body []byte) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p.Code
}

func TestCashPosition_PicksTheFreshestSourcePerAccountAndTotalsPerCurrency(t *testing.T) {
	now := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	d := decimal.RequireFromString
	fc := &fakeCash{accounts: []store.PositionAccount{
		{Account: store.BankAccount{ID: ids.New(), AccountCode: "EUR-OPS", IBANMasked: "DE** 3000", Currency: "EUR"},
			Statement: &domain.StatementBalance{ClosingDate: "2026-10-21", ClosingAmount: d("1000")},
			Intraday: &domain.IntradayReport{CreatedAt: time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC), Currency: "EUR",
				Balances: []domain.IntradayBalance{{Code: "ITBD", Amount: d("1250.50")}, {Code: "ITAV", Amount: d("51250.50")}}}},
		{Account: store.BankAccount{ID: ids.New(), AccountCode: "EUR-RES", IBANMasked: "DE** 1111", Currency: "EUR"},
			Statement: &domain.StatementBalance{ClosingDate: "2026-10-21", ClosingAmount: d("500")}},
		{Account: store.BankAccount{ID: ids.New(), AccountCode: "USD-OPS", IBANMasked: "US** 4821", Currency: "USD"},
			Statement: &domain.StatementBalance{ClosingDate: "2026-10-10", ClosingAmount: d("90")}},
		{Account: store.BankAccount{ID: ids.New(), AccountCode: "USD-NEW", IBANMasked: "US** 0001", Currency: "USD"}},
	}}
	h := cashServer(t, fc, now)
	rec := call(h, http.MethodGet, "/v1/treasury/cash-position?legalEntityId="+ids.New(), nil, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("position: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		StaleHours int `json:"staleHours"`
		Accounts   []struct {
			AccountCode string `json:"accountCode"`
			Booked      string `json:"booked"`
			Available   string `json:"available"`
			Source      string `json:"source"`
			Stale       bool   `json:"stale"`
		} `json:"accounts"`
		Totals []struct {
			Currency  string `json:"currency"`
			Booked    string `json:"booked"`
			Available string `json:"available"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.StaleHours != 24 || len(got.Accounts) != 4 {
		t.Fatalf("position = %+v", got)
	}
	want := []struct {
		code, booked, available, source string
		stale                           bool
	}{
		{"EUR-OPS", "1250.5", "51250.5", "camt.052", false},
		{"EUR-RES", "500", "500", "camt.053", false},
		{"USD-OPS", "90", "90", "camt.053", true},
		{"USD-NEW", "0", "0", "none", true},
	}
	for i, w := range want {
		a := got.Accounts[i]
		if a.AccountCode != w.code || a.Booked != w.booked || a.Available != w.available || a.Source != w.source || a.Stale != w.stale {
			t.Errorf("account %d = %+v, want %+v", i, a, w)
		}
	}
	if len(got.Totals) != 2 || got.Totals[0].Currency != "EUR" || got.Totals[0].Booked != "1750.5" || got.Totals[0].Available != "51750.5" || got.Totals[1].Currency != "USD" || got.Totals[1].Booked != "90" {
		t.Fatalf("totals = %+v", got.Totals)
	}
	if rec := call(h, http.MethodGet, "/v1/treasury/cash-position", nil, "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing entity: %d, want 400", rec.Code)
	}
}

func TestCashPosition_UnwiredServiceAnswers503(t *testing.T) {
	h := cashServer(t, nil, time.Now())
	if rec := call(h, http.MethodGet, "/v1/treasury/cash-position?legalEntityId="+ids.New(), nil, "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired: %d, want 503", rec.Code)
	}
}
