package domain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func settlementFor(amount, currency string) domain.Settlement {
	return domain.Settlement{
		ID: "st-1", Kind: domain.SettlementPlanRow, LegalEntityID: "le-1", LedgerID: "l-1", FiscalPeriodID: "fp-1", PostingDate: august15,
		Currency: currency, DebitAccount: "2100", Amount: dec(amount),
	}
}

func TestPostSettlement_DebitsTheSourceAccountAndCreditsVendorControl(t *testing.T) {
	ledger := &domain.FakeLedger{}

	entry, err := domain.PostSettlement(context.Background(), settlementFor("1000.50", "USD"), domain.FakePeriods{Status: domain.PeriodOpen}, ledger, testProfile)

	if err != nil {
		t.Fatal(err)
	}
	if entry.IdempotencyKey != "post:receiving:settlement:st-1:plan_row" || ledger.Balance("2100").String() != "1000.5" || ledger.Balance("2000").String() != "-1000.5" {
		t.Fatalf("key %s grir %s vendor %s", entry.IdempotencyKey, ledger.Balance("2100"), ledger.Balance("2000"))
	}
	if !entry.DocumentDate.Equal(august15) {
		t.Fatalf("document date %v, want the posting date when none is given", entry.DocumentDate)
	}
}

func TestPostSettlement_ZeroDecimalCurrencyAcceptsWholeAmountsOnly(t *testing.T) {
	ledger := &domain.FakeLedger{}
	periods := domain.FakePeriods{Status: domain.PeriodOpen}

	if _, err := domain.PostSettlement(context.Background(), settlementFor("1000", "JPY"), periods, ledger, testProfile); err != nil {
		t.Fatal(err)
	}
	_, err := domain.PostSettlement(context.Background(), settlementFor("1000.5", "JPY"), periods, ledger, testProfile)

	if codeOf(t, err) != domain.CodeInvalidReceipt || len(ledger.Entries) != 1 {
		t.Fatalf("err %v entries %d", err, len(ledger.Entries))
	}
}

func TestPostSettlement_Rejections(t *testing.T) {
	boom := errors.New("down")
	tests := []struct {
		name    string
		mutate  func(*domain.Settlement)
		periods domain.PeriodControl
		ledger  *domain.FakeLedger
		code    string
		err     error
	}{
		{"zero amount", func(s *domain.Settlement) { s.Amount = dec("0") }, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, domain.CodeInvalidReceipt, nil},
		{"negative amount", func(s *domain.Settlement) { s.Amount = dec("-5") }, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, domain.CodeInvalidReceipt, nil},
		{"no debit account", func(s *domain.Settlement) { s.DebitAccount = "" }, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, domain.CodeInvalidReceipt, nil},
		{"no fiscal period", func(s *domain.Settlement) { s.FiscalPeriodID = "" }, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, domain.CodeInvalidReceipt, nil},
		{"closed period", func(*domain.Settlement) {}, domain.FakePeriods{Status: domain.PeriodClosed}, &domain.FakeLedger{}, domain.CodePeriodClosed, nil},
		{"period service down fails closed", func(*domain.Settlement) {}, domain.FakePeriods{Err: boom}, &domain.FakeLedger{}, "", boom},
		{"ledger down", func(*domain.Settlement) {}, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{Err: boom}, "", boom},
		{"ledger period closed", func(*domain.Settlement) {}, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{Err: domain.ErrPeriodClosed}, domain.CodePeriodClosed, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := settlementFor("10.00", "USD")
			tc.mutate(&s)

			_, err := domain.PostSettlement(context.Background(), s, tc.periods, tc.ledger, testProfile)

			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("err %v, want %v", err, tc.err)
			}
			if tc.code != "" && codeOf(t, err) != tc.code {
				t.Fatalf("err %v, want code %s", err, tc.code)
			}
			if len(tc.ledger.Entries) != 0 {
				t.Fatalf("rejected settlement reached the ledger")
			}
		})
	}
}
