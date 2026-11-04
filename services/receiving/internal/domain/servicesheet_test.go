package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func TestPostServiceSheet_DebitsExpenseAndCreditsGRIR(t *testing.T) {
	in := domain.ServiceAccrual{
		ID: "sheet-1", LegalEntityID: "le-1", LedgerID: "l-1", FiscalPeriodID: "fp-1",
		PostingDate: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), Currency: "EUR",
		Lines: []domain.ServiceAccrualLine{
			{LineNo: 1, Qty: dec("10"), UnitPrice: money.Amount{Value: dec("25"), Currency: "EUR"}},
			{LineNo: 2, Qty: dec("2"), UnitPrice: money.Amount{Value: dec("40"), Currency: "EUR"}},
		},
	}
	profile := domain.PostingProfile{GRIR: "2100", Expense: "6000"}
	ledger := &domain.FakeLedger{}
	entry, items, err := domain.PostServiceSheet(context.Background(), in, domain.FakePeriods{Status: domain.PeriodOpen}, ledger, profile)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Source.DocumentType != "serviceentrysheet" || entry.IdempotencyKey != "post:receiving:serviceentrysheet:sheet-1:accrual" {
		t.Fatalf("entry %+v", entry)
	}
	if len(items) != 2 || !items[0].ReceivedAmount.Value.Equal(dec("250")) || !items[1].ReceivedAmount.Value.Equal(dec("80")) {
		t.Fatalf("items %+v", items)
	}
	if got := ledger.Balance("6000"); !got.Equal(dec("330")) {
		t.Fatalf("expense %s, want 330", got)
	}
	if got := ledger.Balance("2100"); !got.Equal(dec("-330")) {
		t.Fatalf("GR/IR %s, want -330", got)
	}
	if len(ledger.Entries) != 1 || entry.LedgerEntryID == "" {
		t.Fatalf("entry %+v ledger %+v", entry, ledger.Entries)
	}
}

func TestPostServiceSheet_ClosedPeriodPostsNothing(t *testing.T) {
	in := domain.ServiceAccrual{
		ID: "sheet-1", LegalEntityID: "le-1", LedgerID: "l-1", FiscalPeriodID: "fp-1",
		PostingDate: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), Currency: "EUR",
		Lines: []domain.ServiceAccrualLine{{LineNo: 1, Qty: dec("1"), UnitPrice: money.Amount{Value: dec("10"), Currency: "EUR"}}},
	}
	ledger := &domain.FakeLedger{}
	_, _, err := domain.PostServiceSheet(context.Background(), in, domain.FakePeriods{Status: domain.PeriodClosed}, ledger, domain.PostingProfile{GRIR: "2100", Expense: "6000"})
	if codeOf(t, err) != domain.CodePeriodClosed || len(ledger.Entries) != 0 {
		t.Fatalf("err %v entries %d", err, len(ledger.Entries))
	}
}
