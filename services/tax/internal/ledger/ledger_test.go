package ledger_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/ledger"
)

const (
	entityID = "00000000-0000-0000-0000-0000000000e1"
	ledgerID = "00000000-0000-0000-0000-0000000000a1"
)

var (
	outputKey = domain.MovementKey("VAT19", domain.DirOutput)
	inputKey  = domain.MovementKey("VAT19", domain.DirInput)
	accounts  = map[string]string{outputKey: "2200", inputKey: "1400"}
	january   = domain.Period{From: domain.Day("2026-01-01"), To: domain.Day("2026-01-31")}
)

func post(t *testing.T, f *ledgerclient.Fake, date, key, debit, credit, amount string) {
	t.Helper()
	_, err := f.Post(context.Background(), ledgerclient.Entry{
		LegalEntityID: entityID, LedgerID: ledgerID, DocumentID: key, PostingDate: date, Currency: "EUR", IdempotencyKey: key,
		Lines: []ledgerclient.Line{{Account: debit, Amount: decimal.RequireFromString(amount)}, {Account: credit, Credit: true, Amount: decimal.RequireFromString(amount)}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSettle_ReconcilesToTheLedgerBalances(t *testing.T) {
	fake := ledgerclient.NewFake()
	post(t, fake, "2026-01-10", "sale", "1200", "2200", "150.50")
	post(t, fake, "2026-01-12", "purchase", "1400", "2100", "30.25")
	post(t, fake, "2025-12-20", "december-sale", "1200", "2200", "999.00")
	post(t, fake, "2026-02-02", "february-sale", "1200", "2200", "888.00")
	gl := ledger.GL{Client: fake, LegalEntityID: entityID, LedgerID: ledgerID, Accounts: accounts}
	txns := []domain.Txn{
		{ID: "t1", Code: "VAT19", Direction: domain.DirOutput, TaxDate: domain.Day("2026-01-10"), Amount: domain.D("150.50")},
		{ID: "t2", Code: "VAT19", Direction: domain.DirInput, TaxDate: domain.Day("2026-01-12"), Amount: domain.D("30.25")},
	}

	s, err := domain.Settle(context.Background(), txns, january, 0, gl)
	if err != nil {
		t.Fatal(err)
	}
	if s.OutputTotal.String() != "150.5" || s.InputTotal.String() != "30.25" || s.NetAmount.String() != "120.25" {
		t.Fatalf("unexpected settlement %+v", s)
	}

	txns[0].Amount = domain.D("150.51")
	_, err = domain.Settle(context.Background(), txns, january, 0, gl)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeSettlementMismatch {
		t.Fatalf("one cent off: %v, want %s", err, domain.CodeSettlementMismatch)
	}
}

func TestMovements_RefusesPartialMonths(t *testing.T) {
	gl := ledger.GL{Client: ledgerclient.NewFake(), LegalEntityID: entityID, LedgerID: ledgerID, Accounts: accounts}
	for _, p := range []domain.Period{
		{From: domain.Day("2026-01-02"), To: domain.Day("2026-01-31")},
		{From: domain.Day("2026-01-01"), To: domain.Day("2026-01-30")},
	} {
		_, err := gl.Movements(context.Background(), p.From.Time, p.To.Time)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeValidation {
			t.Errorf("%v..%v: %v, want %s", p.From, p.To, err, domain.CodeValidation)
		}
	}
}

type failingLedger struct{ ledgerclient.Ledger }

func (failingLedger) Balance(context.Context, ledgerclient.BalanceQuery) (ledgerclient.Balance, error) {
	return ledgerclient.Balance{}, &ledgerclient.Error{Op: "balance", Status: 503}
}

func TestMovements_SurfacesLedgerFailure(t *testing.T) {
	gl := ledger.GL{Client: failingLedger{}, LegalEntityID: entityID, LedgerID: ledgerID, Accounts: accounts}
	_, err := gl.Movements(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC))
	var le *ledgerclient.Error
	if !errors.As(err, &le) {
		t.Fatalf("got %v, want the ledger error wrapped", err)
	}
}

func TestJournalsGroupsLinesByEntryAndSourceTypeAndSkipsZeroLines(t *testing.T) {
	line := func(entry, source string, n int, credit bool, amount string) ledgerclient.JournalLine {
		return ledgerclient.JournalLine{EntryID: entry, SourceType: source, PostingDate: "2026-01-05", LineNumber: n, Account: "1000", Credit: credit, Amount: decimal.RequireFromString(amount)}
	}
	got, err := ledger.Journals([]ledgerclient.JournalLine{
		line("e1", "ar", 1, false, "5"), line("e1", "ar", 2, true, "5"), line("e1", "ar", 3, true, "0"),
		line("e2", "", 1, false, "3"), line("e2", "", 2, true, "3"),
	})
	if err != nil || len(got) != 2 || got[0].ID != "ar" || got[1].ID != "GL" || len(got[0].Transactions) != 1 || len(got[0].Transactions[0].Lines) != 2 {
		t.Fatalf("journals %+v err %v", got, err)
	}
	if _, err := ledger.Journals([]ledgerclient.JournalLine{{EntryID: "e", PostingDate: "soon", Amount: decimal.NewFromInt(1)}}); !errors.Is(err, ledger.ErrRead) {
		t.Fatalf("bad date: %v", err)
	}
}
