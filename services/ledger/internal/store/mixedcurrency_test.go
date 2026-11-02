package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
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
		return decimal.Decimal{}, &RateRejected{Code: CodeRateMissing, Detail: "no rate"}
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
		ParallelLedgerIDs: []string{eur.ID, e.tax.ID}})
	if err != nil {
		t.Fatalf("create mixed set: %v", err)
	}
	e.set = set
	return e, eur
}

func dimensioned(l domain.LineInput, ref string) domain.LineInput {
	l.Dimensions = map[string]string{"DEPT": "D100"}
	l.SourceLineRef = ref
	return l
}

func TestPostgres_LedgerSettingsAreVersionedAuditedAndClearable(t *testing.T) {
	e, eur := setupMixedCurrencySet(t)
	if eur.Version != 1 || eur.CalendarCode != nil {
		t.Fatalf("a new ledger has no settings at version 1: %+v", eur)
	}
	updated, err := e.store.UpdateLedgerSettings(e.ctx, eur.ID, 1, LedgerSettings{CalendarCode: strPtr("FISCAL_EU"), AccountingPrinciple: strPtr("IFRS"), ReportingCurrency: strPtr("USD")})
	if err != nil || updated.Version != 2 || *updated.CalendarCode != "FISCAL_EU" || *updated.AccountingPrinciple != "IFRS" || *updated.ReportingCurrency != "USD" {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if _, err := e.store.UpdateLedgerSettings(e.ctx, eur.ID, 1, LedgerSettings{CalendarCode: strPtr("OTHER")}); problemCode(err) != CodeVersionConflict {
		t.Fatalf("a stale version: %v", err)
	}
	kept, err := e.store.UpdateLedgerSettings(e.ctx, eur.ID, 0, LedgerSettings{CalendarCode: strPtr("")})
	if err != nil || kept.CalendarCode != nil || *kept.AccountingPrinciple != "IFRS" || kept.Version != 3 {
		t.Fatalf("an empty string clears one field and keeps the others: %+v %v", kept, err)
	}
	got, err := e.store.GetLedger(e.ctx, eur.ID)
	if err != nil || got.Version != 3 || got.ReportingCurrency == nil {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := e.store.UpdateLedgerSettings(e.ctx, eur.ID, 0, LedgerSettings{AccountingPrinciple: strPtr("GAAP")}); err == nil {
		t.Fatal("the database must refuse a principle outside the PRD list")
	}
	var events int
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from outbox where type = $1 and payload::text like '%ledger_settings%'`, ledgerevents.RulesChanged).Scan(&events)
	})
	if err != nil || events != 2 {
		t.Fatalf("each change announces rules.changed: %d events, %v", events, err)
	}
	if _, err := e.store.UpdateLedgerSettings(principalCtx(e.tenant, "00000000-0000-0000-0000-0000000000aa"), eur.ID, 0, LedgerSettings{CalendarCode: strPtr("X")}); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("another legal entity: %v", err)
	}
}

func TestPostgres_MixedCurrencySetTranslatesOnceAndEachLedgerBalances(t *testing.T) {
	e, eur := setupMixedCurrencySet(t)
	rates := &stubRates{rates: map[string]string{"USDEUR": "0.915"}}
	ctx := WithRateSource(e.ctx, rates)
	in := vendorInvoice("", "MIX-1", "100.00")
	in.Lines = []domain.LineInput{
		dimensioned(ledgerLine("6010", domain.SideDebit, "33.33"), "first"),
		dimensioned(ledgerLine("6010", domain.SideDebit, "33.33"), "second"),
		dimensioned(ledgerLine("6010", domain.SideDebit, "33.34"), "third"),
		ledgerLine("2100", domain.SideCredit, "100.00"),
	}
	out, err := e.store.PostSetEntry(ctx, e.set.ID, in)
	if err != nil || len(out.Entries) != 3 {
		t.Fatalf("post: %d entries, %v", len(out.Entries), err)
	}
	if rates.calls != 1 {
		t.Errorf("one lookup per currency, made before the transaction: %d", rates.calls)
	}
	for _, entry := range out.Entries {
		want := map[string]string{e.ledger.ID: "100.00", e.tax.ID: "100.00", eur.ID: "91.50"}[entry.LedgerID]
		if entry.Totals.Debit != want || entry.Totals.Credit != want {
			t.Errorf("ledger %s totals %+v, want %s each side", entry.LedgerID, entry.Totals, want)
		}
		for _, l := range entry.Lines {
			flagged := l.RoundingAdjustment
			if flagged != (entry.LedgerID == eur.ID && l.SourceLineRef == "third") {
				t.Errorf("ledger %s line %s: roundingAdjustment %v", entry.LedgerID, l.SourceLineRef, flagged)
			}
			if flagged && l.Amount != "30.50" {
				t.Errorf("the largest debit absorbs the cent: %s", l.Amount)
			}
		}
	}
	reloaded, err := e.store.GetEntry(e.ctx, out.Entries[0].ID)
	if err != nil || len(reloaded.Lines) == 0 {
		t.Fatalf("reload: %v", err)
	}
}

func TestPostgres_MixedCurrencyCommonLinesMayBalanceOnlyWithLedgerLines(t *testing.T) {
	e, eur := setupMixedCurrencySet(t)
	ctx := WithRateSource(e.ctx, &stubRates{rates: map[string]string{"USDEUR": "0.915"}})
	in := vendorInvoice("", "MIX-ONESIDED", "100.00")
	in.CanPostLedgerSpecific = true
	in.Lines = []domain.LineInput{
		dimensioned(ledgerLine("6010", domain.SideDebit, "33.33"), "first"),
		dimensioned(ledgerLine("6010", domain.SideDebit, "33.33"), "second"),
		dimensioned(ledgerLine("6010", domain.SideDebit, "33.34"), "third"),
	}
	euroCredit := domain.LineInput{Account: "2100", Side: domain.SideCredit, Amount: decimal.RequireFromString("91.50"), Currency: "EUR"}
	in.LedgerLines = map[string][]domain.LineInput{
		e.ledger.ID: {ledgerLine("2100", domain.SideCredit, "100.00")},
		e.tax.ID:    {ledgerLine("2100", domain.SideCredit, "100.00")},
		eur.ID:      {euroCredit},
	}
	out, err := e.store.PostSetEntry(ctx, e.set.ID, in)
	if err != nil || len(out.Entries) != 3 {
		t.Fatalf("an unbalanced common set whose net translates to 91.50 must balance with its ledger lines: %v", err)
	}
	for _, entry := range out.Entries {
		if entry.LedgerID == eur.ID && (entry.Totals.Debit != "91.50" || entry.Totals.Credit != "91.50") {
			t.Errorf("euro totals %+v", entry.Totals)
		}
	}
}

func TestTranslateLines_AbsorbsTheRoundingDifferenceOnTheCreditSide(t *testing.T) {
	ctx := WithRateSource(context.Background(), &stubRates{rates: map[string]string{"USDEUR": "0.915"}})
	lines := []domain.LineInput{
		ledgerLine("6010", domain.SideDebit, "100.00"),
		ledgerLine("2100", domain.SideCredit, "33.33"),
		ledgerLine("2100", domain.SideCredit, "33.33"),
		ledgerLine("2100", domain.SideCredit, "33.34"),
	}
	out, err := translateLines(ctx, Ledger{Code: "EUR", Currency: "EUR"}, "USD", time.Now(), lines)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"91.50", "30.50", "30.50", "30.50"} {
		if out[i].Amount.StringFixed(2) != want || out[i].RoundingAdjustment != (i == 3) || out[i].Currency != "EUR" {
			t.Errorf("line %d: %s adjusted %v", i, out[i].Amount, out[i].RoundingAdjustment)
		}
	}
	if lines[3].Amount.String() != "33.34" || lines[3].RoundingAdjustment {
		t.Error("the caller's lines must not change")
	}
}

func TestTranslateLines_FallsBackToTheLighterSideWhenTheHeavierLineIsTooSmall(t *testing.T) {
	ctx := WithRateSource(context.Background(), &stubRates{rates: map[string]string{"USDEUR": "0.5"}})
	lines := []domain.LineInput{
		ledgerLine("6010", domain.SideDebit, "0.01"),
		ledgerLine("6010", domain.SideDebit, "0.01"),
		ledgerLine("2100", domain.SideCredit, "0.02"),
	}
	out, err := translateLines(ctx, Ledger{Code: "EUR", Currency: "EUR"}, "USD", time.Now(), lines)
	if err != nil {
		t.Fatal(err)
	}
	if out[2].Amount.StringFixed(2) != "0.02" || !out[2].RoundingAdjustment || out[0].RoundingAdjustment || out[0].Amount.StringFixed(2) != "0.01" {
		t.Fatalf("the credit takes the cent: %+v", out)
	}
}

func TestPostgres_MixedCurrencySetMapsRateFailuresAndWritesNothing(t *testing.T) {
	e, _ := setupMixedCurrencySet(t)
	for name, tc := range map[string]struct {
		ctx  context.Context
		want string
	}{
		"no rate for the pair": {WithRateSource(e.ctx, &stubRates{rates: map[string]string{}}), CodeRateMissing},
		"no rate source":       {e.ctx, CodeRateMissing},
		"rate date in future":  {WithRateSource(e.ctx, &stubRates{err: &RateRejected{Code: "FX_RATE_DATE_FUTURE", Detail: "future"}}), "FX_RATE_DATE_FUTURE"},
		"fx refuses the call":  {WithRateSource(e.ctx, &stubRates{err: &RateRefused{Status: 403}}), CodeFXMisconfigure},
		"fx unreachable":       {WithRateSource(e.ctx, &stubRates{err: errors.New("connection refused")}), CodeFXUnavailable},
	} {
		_, err := e.store.PostSetEntry(tc.ctx, e.set.ID, vendorInvoice("", "MIX-FAIL-"+name, "100.00"))
		if problemCode(err) != tc.want {
			t.Errorf("%s: want %s, got %v", name, tc.want, err)
		}
		if name == "no rate for the pair" && (err == nil || !strings.Contains(err.Error(), "spot")) {
			t.Errorf("the message names the rate type: %v", err)
		}
	}
	if n := e.entryCount(t); n != 0 {
		t.Fatalf("a failed rate must leave nothing posted, got %d entries", n)
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
