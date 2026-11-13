package store

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

func intradayInput(t *testing.T, accountID string, data []byte) IntradayImport {
	t.Helper()
	sum := sha256.Sum256(data)
	parsed, err := domain.ParseCAMT052(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return IntradayImport{BankAccountID: accountID, FileHash: hex.EncodeToString(sum[:]), Parsed: parsed[0]}
}

func sampleReport(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../testdata/camt052/sample-08.xml")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestImportIntradayReport_StoresReplaysAndRefuses(t *testing.T) {
	s, pool := newStore(t)
	ctx := ctxFor(1, "")
	acct := activeAccount(t, s, ctx, ids.New())
	data := sampleReport(t)

	res, err := s.ImportIntradayReport(ctx, intradayInput(t, acct.ID, data))
	if err != nil || res.Replayed || len(res.Report.Balances) != 3 || res.Report.ReportID != "RPT-20261022-1400" {
		t.Fatalf("first import = %+v, %v", res, err)
	}
	again, err := s.ImportIntradayReport(ctx, intradayInput(t, acct.ID, data))
	if err != nil || !again.Replayed || again.Report.ID != res.Report.ID || len(again.Report.Balances) != 3 {
		t.Fatalf("same bytes = %+v, %v; want a replay", again, err)
	}
	if n := count(t, pool, `select count(*) from intraday_report`); n != 1 {
		t.Fatalf("%d reports stored, want 1", n)
	}
	// The same report id with other balances is a conflict, never an overwrite.
	other := strings.Replace(string(data), "1250.50", "1300.00", 1)
	if _, err := s.ImportIntradayReport(ctx, intradayInput(t, acct.ID, []byte(other))); problemCode(t, err) != CodeReportConflict {
		t.Fatalf("same id, other content: %v, want %s", err, CodeReportConflict)
	}
	// A report for another account number is refused (TRS-0409).
	foreign := strings.Replace(string(data), "DE89370400440532013000", "GB82WEST12345698765432", 1)
	if _, err := s.ImportIntradayReport(ctx, intradayInput(t, acct.ID, []byte(foreign))); problemCode(t, err) != domain.CodeStatementAccount {
		t.Fatalf("other account: %v, want %s", err, domain.CodeStatementAccount)
	}
	// A report in another currency is refused (TRS-0408).
	usd := strings.ReplaceAll(strings.Replace(string(data), "RPT-20261022-1400", "RPT-USD", 1), `"EUR"`, `"USD"`)
	usd = strings.Replace(usd, "<Ccy>EUR</Ccy>", "<Ccy>USD</Ccy>", 1)
	if _, err := s.ImportIntradayReport(ctx, intradayInput(t, acct.ID, []byte(usd))); problemCode(t, err) != domain.CodeStatementCurrency {
		t.Fatalf("other currency: %v, want %s", err, domain.CodeStatementCurrency)
	}
	if _, err := s.ImportIntradayReport(ctx, intradayInput(t, ids.New(), data)); problemCode(t, err) != CodeAccountNotFound {
		t.Fatalf("unknown account: %v, want %s", err, CodeAccountNotFound)
	}
	// A tenant that does not own the account cannot read or write its reports.
	if _, err := s.ImportIntradayReport(ctxFor(2, ""), intradayInput(t, acct.ID, data)); problemCode(t, err) != CodeAccountNotFound {
		t.Fatalf("other tenant: %v, want %s", err, CodeAccountNotFound)
	}
}

func TestCashPositionAccounts_ReturnsTheLatestStatementAndTheLatestReport(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	le := ids.New()
	acct := activeAccount(t, s, ctx, le)
	// A draft account is not part of the position.
	if _, err := s.CreateBankAccount(ctx, NewBankAccount{LegalEntityID: le, AccountCode: "DRAFT-1", IBAN: "GB82WEST12345698765432", Currency: "GBP", MainAccountID: ids.New()}, ids.New()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CashPositionAccounts(ctx, le); err != nil || len(got) != 1 || got[0].Statement != nil || got[0].Intraday != nil || got[0].Opening != nil {
		t.Fatalf("before any file: %+v, %v", got, err)
	}
	// The migrated opening balance is read in the account's currency (minor units to an amount).
	if _, _, err := s.ImportOpeningBalances(ctx, OpeningImport{LegalEntityID: le, BatchKey: "cutover", AsOf: "2026-10-20",
		Items: []OpeningItem{{Reference: "AX-OPS", BankAccountID: acct.ID, Currency: acct.Currency, BalanceMinor: 98050}}}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CashPositionAccounts(ctx, le); err != nil || got[0].Opening == nil || got[0].Opening.AsOf != "2026-10-20" || got[0].Opening.Amount.String() != "980.5" {
		t.Fatalf("opening = %+v, %v", got[0].Opening, err)
	}
	if _, err := s.ImportStatement(ctx, importInput(t, acct.ID, camt("STMT-A", 0, 2, ""))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportIntradayReport(ctx, intradayInput(t, acct.ID, sampleReport(t))); err != nil {
		t.Fatal(err)
	}
	later := strings.ReplaceAll(string(sampleReport(t)), "2026-10-22T14:00:00", "2026-10-23T01:30:00")
	later = strings.Replace(later, "RPT-20261022-1400", "RPT-20261023-0130", 1)
	later = strings.Replace(later, "1250.50", "1400.00", 1)
	if _, err := s.ImportIntradayReport(ctx, intradayInput(t, acct.ID, []byte(later))); err != nil {
		t.Fatal(err)
	}
	got, err := s.CashPositionAccounts(ctx, le)
	if err != nil || len(got) != 1 {
		t.Fatalf("accounts = %+v, %v", got, err)
	}
	if got[0].Statement == nil || got[0].Statement.ClosingDate != "2026-10-22" || got[0].Statement.ClosingAmount.String() != "1002.5" {
		t.Fatalf("statement = %+v", got[0].Statement)
	}
	if got[0].Intraday == nil || got[0].Intraday.ReportID != "RPT-20261023-0130" || !got[0].Intraday.CreatedAt.Equal(time.Date(2026, 10, 23, 1, 30, 0, 0, time.UTC)) {
		t.Fatalf("intraday = %+v", got[0].Intraday)
	}
	pos := domain.Position(domain.PositionInput{Account: "OPS-01", Currency: "EUR", Statement: got[0].Statement, Intraday: got[0].Intraday}, time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC), domain.DefaultStaleHours)
	if pos.Source != domain.SourceIntraday || pos.Booked.String() != "1400" {
		t.Fatalf("the report from 01:30 on 23 October is newer than the end of the day the statement closed on: %+v", pos)
	}
	// Another legal entity sees nothing.
	if other, err := s.CashPositionAccounts(ctx, ids.New()); err != nil || len(other) != 0 {
		t.Fatalf("other legal entity: %+v, %v", other, err)
	}
}
