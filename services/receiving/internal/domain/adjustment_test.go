package domain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func originalLines() []domain.InvoiceLineState {
	return []domain.InvoiceLineState{
		{LineNo: 1, Amount: dec("104.00")},
		{LineNo: 2, Amount: dec("50.00"), StandardCost: true, Credited: dec("20.00"), Debited: dec("5.00")},
	}
}

func adjustment(kind string, lines ...domain.AdjustmentLine) domain.InvoiceAdjustment {
	return domain.InvoiceAdjustment{
		ID: "adj-1", Kind: kind, LegalEntityID: "le-1", LedgerID: "l-1", FiscalPeriodID: "fp-1", PostingDate: august15, DocumentDate: august15,
		Currency: "USD", Original: originalLines(), Lines: lines,
	}
}

func TestPostInvoiceAdjustment_DebitPostsPriceDifferenceAndCreditsVendor(t *testing.T) {
	ledger := &domain.FakeLedger{}
	in := adjustment(domain.AdjustmentDebit, domain.AdjustmentLine{InvoiceLineNo: 1, Amount: dec("3.50")}, domain.AdjustmentLine{InvoiceLineNo: 2, Amount: dec("1.25")})

	entry, err := domain.PostInvoiceAdjustment(context.Background(), in, domain.FakePeriods{Status: domain.PeriodOpen}, ledger, testProfile)

	if err != nil {
		t.Fatal(err)
	}
	if entry.LedgerEntryID != "fake-entry-1" || entry.IdempotencyKey != "post:receiving:invoiceadjustment:adj-1" {
		t.Fatalf("entry %+v", entry)
	}
	if ledger.Balance("1400").String() != "3.5" || ledger.Balance("5200").String() != "1.25" || ledger.Balance("2000").String() != "-4.75" || !ledger.Balance("2100").IsZero() {
		t.Fatalf("inventory %s ppv %s vendor %s grir %s", ledger.Balance("1400"), ledger.Balance("5200"), ledger.Balance("2000"), ledger.Balance("2100"))
	}
}

func TestPostInvoiceAdjustment_CreditReversesTheDifferenceAndDebitsVendor(t *testing.T) {
	ledger := &domain.FakeLedger{}
	in := adjustment(domain.AdjustmentCredit, domain.AdjustmentLine{InvoiceLineNo: 1, Amount: dec("10.00")}, domain.AdjustmentLine{InvoiceLineNo: 2, Amount: dec("2.50")})

	if _, err := domain.PostInvoiceAdjustment(context.Background(), in, domain.FakePeriods{Status: domain.PeriodOpen}, ledger, testProfile); err != nil {
		t.Fatal(err)
	}

	if ledger.Balance("1400").String() != "-10" || ledger.Balance("5200").String() != "-2.5" || ledger.Balance("2000").String() != "12.5" {
		t.Fatalf("inventory %s ppv %s vendor %s", ledger.Balance("1400"), ledger.Balance("5200"), ledger.Balance("2000"))
	}
}

func TestPostInvoiceAdjustment_CreditCeilingIsInvoicedPlusDebitsMinusCredits(t *testing.T) {
	tests := []struct {
		name   string
		lineNo int
		amount []string
		code   string
	}{
		{"line 1 exactly its invoiced amount", 1, []string{"104.00"}, ""},
		{"line 1 one cent over", 1, []string{"104.01"}, domain.CodeOverCredit},
		{"line 2 exactly 50 + 5 - 20 = 35", 2, []string{"35.00"}, ""},
		{"line 2 one cent over 35", 2, []string{"35.01"}, domain.CodeOverCredit},
		{"two lines of one request add up against the same invoice line", 2, []string{"20.00", "15.01"}, domain.CodeOverCredit},
		{"two lines of one request that fit", 2, []string{"20.00", "15.00"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ledger := &domain.FakeLedger{}
			var lines []domain.AdjustmentLine
			for _, a := range tc.amount {
				lines = append(lines, domain.AdjustmentLine{InvoiceLineNo: tc.lineNo, Amount: dec(a)})
			}

			_, err := domain.PostInvoiceAdjustment(context.Background(), adjustment(domain.AdjustmentCredit, lines...), domain.FakePeriods{Status: domain.PeriodOpen}, ledger, testProfile)

			if tc.code == "" {
				if err != nil || len(ledger.Entries) != 1 {
					t.Fatalf("err %v entries %d", err, len(ledger.Entries))
				}
				return
			}
			if got := codeOf(t, err); got != tc.code || len(ledger.Entries) != 0 {
				t.Fatalf("code %s entries %d", got, len(ledger.Entries))
			}
		})
	}
}

func TestPostInvoiceAdjustment_DebitHasNoCeiling(t *testing.T) {
	ledger := &domain.FakeLedger{}
	in := adjustment(domain.AdjustmentDebit, domain.AdjustmentLine{InvoiceLineNo: 2, Amount: dec("1000.00")})

	if _, err := domain.PostInvoiceAdjustment(context.Background(), in, domain.FakePeriods{Status: domain.PeriodOpen}, ledger, testProfile); err != nil {
		t.Fatal(err)
	}
}

func TestPostInvoiceAdjustment_Rejections(t *testing.T) {
	boom := errors.New("down")
	tests := []struct {
		name    string
		mutate  func(*domain.InvoiceAdjustment)
		periods domain.PeriodControl
		ledger  *domain.FakeLedger
		code    string
		err     error
	}{
		{"unknown kind", func(a *domain.InvoiceAdjustment) { a.Kind = "REFUND" }, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, domain.CodeInvalidReceipt, nil},
		{"no lines", func(a *domain.InvoiceAdjustment) { a.Lines = nil }, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, domain.CodeInvalidReceipt, nil},
		{"no fiscal period", func(a *domain.InvoiceAdjustment) { a.FiscalPeriodID = "" }, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, domain.CodeInvalidReceipt, nil},
		{"unknown invoice line", func(a *domain.InvoiceAdjustment) { a.Lines[0].InvoiceLineNo = 9 }, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, domain.CodeInvalidReceipt, nil},
		{"zero amount", func(a *domain.InvoiceAdjustment) { a.Lines[0].Amount = dec("0") }, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, domain.CodeInvalidReceipt, nil},
		{"negative amount", func(a *domain.InvoiceAdjustment) { a.Lines[0].Amount = dec("-1") }, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, domain.CodeInvalidReceipt, nil},
		{"sub-cent amount", func(a *domain.InvoiceAdjustment) { a.Lines[0].Amount = dec("1.005") }, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, domain.CodeInvalidReceipt, nil},
		{"closed period", func(*domain.InvoiceAdjustment) {}, domain.FakePeriods{Status: domain.PeriodClosed}, &domain.FakeLedger{}, domain.CodePeriodClosed, nil},
		{"period service down fails closed", func(*domain.InvoiceAdjustment) {}, domain.FakePeriods{Err: boom}, &domain.FakeLedger{}, "", boom},
		{"ledger period closed", func(*domain.InvoiceAdjustment) {}, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{Err: domain.ErrPeriodClosed}, domain.CodePeriodClosed, nil},
		{"ledger conflict", func(*domain.InvoiceAdjustment) {}, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{Err: domain.ErrLedgerConflict}, domain.CodePostingPending, nil},
		{"ledger down", func(*domain.InvoiceAdjustment) {}, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{Err: boom}, "", boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := adjustment(domain.AdjustmentDebit, domain.AdjustmentLine{InvoiceLineNo: 1, Amount: dec("3.50")})
			tc.mutate(&in)

			_, err := domain.PostInvoiceAdjustment(context.Background(), in, tc.periods, tc.ledger, testProfile)

			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("err %v, want %v", err, tc.err)
			}
			if tc.code != "" && codeOf(t, err) != tc.code {
				t.Fatalf("err %v, want code %s", err, tc.code)
			}
			if len(tc.ledger.Entries) != 0 {
				t.Fatalf("rejected adjustment reached the ledger: %+v", tc.ledger.Entries)
			}
		})
	}
}

func TestPostInvoiceAdjustment_RepeatingTheSameAdjustmentReusesTheLedgerEntry(t *testing.T) {
	ledger := &domain.FakeLedger{}
	in := adjustment(domain.AdjustmentDebit, domain.AdjustmentLine{InvoiceLineNo: 1, Amount: dec("3.50")})
	first, err := domain.PostInvoiceAdjustment(context.Background(), in, domain.FakePeriods{Status: domain.PeriodOpen}, ledger, testProfile)
	if err != nil {
		t.Fatal(err)
	}

	second, err := domain.PostInvoiceAdjustment(context.Background(), in, domain.FakePeriods{Status: domain.PeriodOpen}, ledger, testProfile)

	if err != nil || second.LedgerEntryID != first.LedgerEntryID || len(ledger.Entries) != 1 {
		t.Fatalf("second %+v err %v entries %d", second, err, len(ledger.Entries))
	}
}

func TestPostInvoiceAdjustment_RetryAsTheOtherKindNeverPostsASecondEntry(t *testing.T) {
	ledger := &domain.FakeLedger{}
	debit := adjustment(domain.AdjustmentDebit, domain.AdjustmentLine{InvoiceLineNo: 1, Amount: dec("3.50")})
	credit := adjustment(domain.AdjustmentCredit, domain.AdjustmentLine{InvoiceLineNo: 1, Amount: dec("3.50")})
	if _, err := domain.PostInvoiceAdjustment(context.Background(), debit, domain.FakePeriods{Status: domain.PeriodOpen}, ledger, testProfile); err != nil {
		t.Fatal(err)
	}

	_, err := domain.PostInvoiceAdjustment(context.Background(), credit, domain.FakePeriods{Status: domain.PeriodOpen}, ledger, testProfile)

	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodePostingPending || len(ledger.Entries) != 1 {
		t.Fatalf("err %v, ledger entries %d, want %s and 1", err, len(ledger.Entries), domain.CodePostingPending)
	}
}
