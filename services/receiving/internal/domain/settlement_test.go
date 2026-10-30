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

type recordingPeriods struct{ got *domain.PeriodQuery }

func (r recordingPeriods) Resolve(_ context.Context, q domain.PeriodQuery) (domain.ResolvedPeriod, error) {
	*r.got = q
	return domain.ResolvedPeriod{FiscalPeriodID: "fp-1", Status: domain.PeriodOpen}, nil
}

func (r recordingPeriods) AP(_ context.Context, q domain.PeriodQuery) (domain.PeriodStatus, error) {
	*r.got = q
	return domain.PeriodOpen, nil
}

func TestPostSettlementAndAdjustment_PeriodQueryCarriesTheDocumentsLegalEntity(t *testing.T) {
	var got domain.PeriodQuery
	periods := recordingPeriods{got: &got}

	if _, err := domain.PostSettlement(context.Background(), settlementFor("10.00", "USD"), periods, &domain.FakeLedger{}, testProfile); err != nil || got.LegalEntityID != "le-1" {
		t.Fatalf("settlement: err %v query %+v", err, got)
	}
	got = domain.PeriodQuery{}
	in := adjustment(domain.AdjustmentDebit, domain.AdjustmentLine{InvoiceLineNo: 1, Amount: dec("3.50")})
	in.LegalEntityID = "le-2"
	if _, err := domain.PostInvoiceAdjustment(context.Background(), in, periods, &domain.FakeLedger{}, testProfile); err != nil || got.LegalEntityID != "le-2" {
		t.Fatalf("adjustment: err %v query %+v", err, got)
	}
}

func TestResolveFiscalPeriod(t *testing.T) {
	boom := errors.New("close down")
	q := domain.PeriodQuery{LegalEntityID: "le-1", LedgerID: "l-1", PostingDate: august15}
	tests := []struct {
		name     string
		periods  domain.FakePeriods
		supplied string
		want     string
		code     string
		err      error
	}{
		{name: "derived", periods: domain.FakePeriods{Status: domain.PeriodOpen, PeriodID: "fp-9"}, want: "fp-9"},
		{name: "matching supplied", periods: domain.FakePeriods{Status: domain.PeriodOpen, PeriodID: "fp-9"}, supplied: "fp-9", want: "fp-9"},
		{name: "different supplied", periods: domain.FakePeriods{Status: domain.PeriodOpen, PeriodID: "fp-9"}, supplied: "fp-1", code: domain.CodePeriodUnresolved},
		{name: "no period", periods: domain.FakePeriods{ResolveErr: domain.ErrFiscalPeriodNotFound}, code: domain.CodePeriodUnresolved},
		{name: "ambiguous", periods: domain.FakePeriods{ResolveErr: domain.ErrFiscalPeriodAmbiguous}, code: domain.CodePeriodUnresolved},
		{name: "unavailable", periods: domain.FakePeriods{ResolveErr: boom}, err: boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ResolveFiscalPeriod(context.Background(), tc.periods, q, tc.supplied)
			var de *domain.Error
			switch {
			case tc.err != nil && !errors.Is(err, tc.err), tc.code != "" && (!errors.As(err, &de) || de.Code != tc.code), tc.code == "" && tc.err == nil && err != nil, got != tc.want:
				t.Fatalf("got %q err %v, want %q code %q err %v", got, err, tc.want, tc.code, tc.err)
			}
		})
	}
}
