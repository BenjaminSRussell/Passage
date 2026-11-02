package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

type stubRates struct {
	rates map[string]string
	err   error
	calls int
}

func (s *stubRates) Rate(_ context.Context, _, from, to string, _ time.Time) (decimal.Decimal, error) {
	s.calls++
	if s.err != nil {
		return decimal.Decimal{}, s.err
	}
	rate, ok := s.rates[from+to]
	if !ok {
		return decimal.Decimal{}, ErrRateUnavailable
	}
	return decimal.RequireFromString(rate), nil
}

func setupMixedCurrencySet(t *testing.T) (setEnv, Ledger) {
	t.Helper()
	e := setupLedgerSet(t)
	eur, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "EUR", Name: "Euro", ChartCode: "STD", Currency: "EUR", DimensionAttributes: []string{"DEPT"}, FuturePostingDays: 366})
	if err != nil {
		t.Fatalf("create euro ledger: %v", err)
	}
	set, err := e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: "MIXED", Name: "Mixed currencies", LeadingLedgerID: e.ledger.ID,
		ParallelLedgerIDs: []string{eur.ID, e.tax.ID},
		Members:           []SetMember{{LedgerID: eur.ID, CalendarCode: strPtr("FISCAL_EU"), AccountingPrinciple: strPtr("IFRS"), ReportingCurrency: strPtr("USD")}}})
	if err != nil {
		t.Fatalf("create mixed set: %v", err)
	}
	e.set = set
	return e, eur
}

func TestPostgres_LedgerSetMembersKeepCalendarPrincipleAndReportingCurrency(t *testing.T) {
	e, eur := setupMixedCurrencySet(t)
	got, err := e.store.GetLedgerSet(e.ctx, e.set.ID)
	if err != nil || len(got.Members) != 3 || len(got.LedgerIDs) != 3 {
		t.Fatalf("get: %+v %v", got, err)
	}
	for _, m := range got.Members {
		if m.LedgerID == eur.ID {
			if m.CalendarCode == nil || *m.CalendarCode != "FISCAL_EU" || *m.AccountingPrinciple != "IFRS" || *m.ReportingCurrency != "USD" {
				t.Errorf("euro member: %+v", m)
			}
			continue
		}
		if m.CalendarCode != nil || m.AccountingPrinciple != nil || m.ReportingCurrency != nil {
			t.Errorf("member %s without settings: %+v", m.LedgerID, m)
		}
	}
	listed, _, err := e.store.ListLedgerSets(e.ctx, "")
	if err != nil || len(listed) != 2 {
		t.Fatalf("list: %+v %v", listed, err)
	}
}

func TestPostgres_MixedCurrencySetTranslatesLinesAndEachLedgerBalances(t *testing.T) {
	e, eur := setupMixedCurrencySet(t)
	ctx := WithRateSource(e.ctx, &stubRates{rates: map[string]string{"USDEUR": "0.915"}})
	in := vendorInvoice("", "MIX-1", "100.00")
	in.Lines = []domain.LineInput{
		ledgerLine("6010", domain.SideDebit, "33.33"),
		ledgerLine("6010", domain.SideDebit, "33.33"),
		ledgerLine("6010", domain.SideDebit, "33.34"),
		ledgerLine("2100", domain.SideCredit, "100.00"),
	}
	for i := range 3 {
		in.Lines[i].Dimensions = map[string]string{"DEPT": "D100"}
	}
	out, err := e.store.PostSetEntry(ctx, e.set.ID, in)
	if err != nil || len(out.Entries) != 3 {
		t.Fatalf("post: %d entries, %v", len(out.Entries), err)
	}
	for _, entry := range out.Entries {
		want := map[string]string{e.ledger.ID: "100.00", e.tax.ID: "100.00", eur.ID: "91.50"}[entry.LedgerID]
		if entry.Totals.Debit != want || entry.Totals.Credit != want {
			t.Errorf("ledger %s totals %+v, want %s each side", entry.LedgerID, entry.Totals, want)
		}
		if entry.LedgerID == eur.ID && entry.Totals.Currency != "EUR" {
			t.Errorf("euro entry currency %s", entry.Totals.Currency)
		}
	}
	tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: eur.ID})
	if err != nil || !tb.Balanced || tb.Currency != "EUR" {
		t.Fatalf("euro trial balance: %+v %v", tb, err)
	}
}

func TestPostgres_MixedCurrencySetRejectsAMissingRateAndWritesNothing(t *testing.T) {
	e, _ := setupMixedCurrencySet(t)
	for name, ctx := range map[string]context.Context{
		"no rate for the pair": WithRateSource(e.ctx, &stubRates{rates: map[string]string{}}),
		"no rate source":       e.ctx,
	} {
		_, err := e.store.PostSetEntry(ctx, e.set.ID, vendorInvoice("", "MIX-MISSING-"+name, "100.00"))
		if problemCode(err) != CodeFXRateMissing {
			t.Errorf("%s: want %s, got %v", name, CodeFXRateMissing, err)
		}
	}
	if n := e.entryCount(t); n != 0 {
		t.Fatalf("a missing rate must leave nothing posted, got %d entries", n)
	}
}

func TestPostgres_MixedCurrencySetReportsAnUnreachableRateService(t *testing.T) {
	e, _ := setupMixedCurrencySet(t)
	ctx := WithRateSource(e.ctx, &stubRates{err: errors.New("connection refused")})
	_, err := e.store.PostSetEntry(ctx, e.set.ID, vendorInvoice("", "MIX-DOWN", "100.00"))
	if problemCode(err) != CodeFXUnavailable {
		t.Fatalf("want %s, got %v", CodeFXUnavailable, err)
	}
	if n := e.entryCount(t); n != 0 {
		t.Fatalf("got %d entries", n)
	}
}

func TestPostgres_SameCurrencySetNeverAsksForARate(t *testing.T) {
	e := setupLedgerSet(t)
	rates := &stubRates{err: errors.New("must not be called")}
	if _, err := e.store.PostSetEntry(WithRateSource(e.ctx, rates), e.set.ID, vendorInvoice("", "MIX-SAME", "10.00")); err != nil || rates.calls != 0 {
		t.Fatalf("calls %d, err %v", rates.calls, err)
	}
}

func TestPostgres_MixedCurrencyLedgerLinesStayInTheLedgersOwnCurrency(t *testing.T) {
	e, eur := setupMixedCurrencySet(t)
	ctx := WithRateSource(e.ctx, &stubRates{rates: map[string]string{"USDEUR": "0.9"}})
	in := vendorInvoice("", "MIX-LL", "100.00")
	in.CanPostLedgerSpecific = true
	usd := domain.LineInput{Account: "6010", Side: domain.SideDebit, Amount: decimal.RequireFromString("5.00"), Currency: "USD"}
	in.LedgerLines = map[string][]domain.LineInput{eur.ID: {usd}}
	_, err := e.store.PostSetEntry(ctx, e.set.ID, in)
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_CURRENCY_MISMATCH" || !strings.HasPrefix(rej.Violations[0].Message, "ledger EUR:") {
		t.Fatalf("a ledger line in the document currency on the euro ledger: %v", err)
	}
	euro := domain.LineInput{Account: "6010", Side: domain.SideDebit, Amount: decimal.RequireFromString("4.50"), Currency: "EUR", Dimensions: map[string]string{"DEPT": "D100"}}
	credit := domain.LineInput{Account: "2100", Side: domain.SideCredit, Amount: decimal.RequireFromString("4.50"), Currency: "EUR"}
	in.LedgerLines = map[string][]domain.LineInput{eur.ID: {euro, credit}}
	out, err := e.store.PostSetEntry(ctx, e.set.ID, in)
	if err != nil || len(out.Entries) != 3 {
		t.Fatalf("post: %v", err)
	}
	for _, entry := range out.Entries {
		if entry.LedgerID == eur.ID && entry.Totals.Debit != "94.50" {
			t.Errorf("euro debit %s, want 90.00 translated plus 4.50", entry.Totals.Debit)
		}
	}
}

func TestPostgres_SingleLedgerPostTakesOnlyItsOwnLedgerLines(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice(e.ledger.ID, "SINGLE-LL-1", "100.00")
	in.LedgerLines = map[string][]domain.LineInput{e.tax.ID: {ledgerLine("6010", domain.SideDebit, "1.00")}}
	_, err := e.store.PostEntry(e.ctx, in)
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != CodeLedgerLinesOwnLedgerOnly {
		t.Fatalf("want %s, got %v", CodeLedgerLinesOwnLedgerOnly, err)
	}
	if n := e.entryCount(t); n != 0 {
		t.Fatalf("got %d entries", n)
	}
	in = vendorInvoice(e.ledger.ID, "SINGLE-LL-2", "100.00")
	in.LedgerLines = map[string][]domain.LineInput{e.ledger.ID: {ledgerLine("6010", domain.SideDebit, "5.00"), ledgerLine("2100", domain.SideCredit, "5.00")}}
	out, err := e.store.PostEntry(e.ctx, in)
	if err != nil || len(out.Entries[0].Lines) != 4 || out.Entries[0].Totals.Debit != "105.00" {
		t.Fatalf("own ledger lines: %+v %v", out, err)
	}
}
