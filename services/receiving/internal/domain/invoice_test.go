package domain_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

var testProfile = domain.PostingProfile{Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000"}

func grirItem(qty, unitPrice string) *domain.GrirItem {
	amount := money.Amount{Value: dec(qty).Mul(dec(unitPrice)), Currency: "USD"}
	return &domain.GrirItem{ReceiptID: "r-1", LineNo: 1, ReceivedQty: dec(qty), ReceivedAmount: amount, ClearedAmount: money.Amount{Value: decimal.Zero, Currency: "USD"}}
}

func invoiceFor(item *domain.GrirItem, qty, price string, date time.Time) domain.VendorInvoice {
	return domain.VendorInvoice{
		ID: "vi-1", LegalEntityID: "le-1", PostingDate: date, InvoiceDate: date, Currency: "USD",
		Lines: []domain.PostLine{{Item: item, Qty: dec(qty), UnitPrice: dec(price)}},
	}
}

var august15 = time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)

func TestPostInvoice_ClosedPeriodRejected(t *testing.T) {
	for _, status := range []domain.PeriodStatus{domain.PeriodClosed, domain.PeriodOnHold, domain.PeriodPermanentlyClosed} {
		t.Run(string(status), func(t *testing.T) {
			item := grirItem("10", "5")
			ledger := &domain.FakeLedger{}
			_, err := domain.PostInvoice(context.Background(), invoiceFor(item, "10", "5", august15), domain.FakePeriods{Status: status}, ledger, testProfile)
			if got := codeOf(t, err); got != domain.CodePeriodClosed {
				t.Fatalf("code %s", got)
			}
			if len(ledger.Entries) != 0 || !item.InvoicedQty.IsZero() || !item.ClearedAmount.Value.IsZero() {
				t.Fatalf("rejection must not touch ledger or GR/IR: %+v %+v", ledger.Entries, item)
			}
		})
	}
}

func TestPostInvoice_RejectionsLeaveEverythingUntouched(t *testing.T) {
	boom := errors.New("down")
	tests := []struct {
		name    string
		periods domain.PeriodControl
		ledger  *domain.FakeLedger
		qty     string
		wantErr func(error) bool
	}{
		{"period service unavailable fails closed", domain.FakePeriods{Err: boom}, &domain.FakeLedger{}, "10", func(e error) bool { return errors.Is(e, boom) }},
		{"over-invoice beyond received", domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, "11", func(e error) bool { return codeOf(t, e) == domain.CodeOverInvoice }},
		{"credit quantity", domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, "-1", func(e error) bool { return codeOf(t, e) == domain.CodeInvalidReceipt }},
		{"ledger failure", domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{Err: boom}, "10", func(e error) bool { return errors.Is(e, boom) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item := grirItem("10", "5")
			_, err := domain.PostInvoice(context.Background(), invoiceFor(item, tc.qty, "5", august15), tc.periods, tc.ledger, testProfile)
			if err == nil || !tc.wantErr(err) {
				t.Fatalf("unexpected error %v", err)
			}
			if len(tc.ledger.Entries) != 0 || !item.InvoicedQty.IsZero() || !item.ClearedAmount.Value.IsZero() {
				t.Fatalf("GR/IR must be untouched: %+v", item)
			}
		})
	}
}

func TestPostInvoice_RejectsSplitLinesThatExceedReceived(t *testing.T) {
	item := grirItem("10", "5")
	in := invoiceFor(item, "6", "5", august15)
	in.Lines = append(in.Lines, in.Lines[0])
	_, err := domain.PostInvoice(context.Background(), in, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, testProfile)
	if got := codeOf(t, err); got != domain.CodeOverInvoice {
		t.Fatalf("code %s", got)
	}
}

func TestPostInvoice_BalancedAndVariancePosting(t *testing.T) {
	tests := []struct {
		name         string
		standard     bool
		price        string
		wantVariance string
		wantAccount  string
	}{
		{"price equals receipt", false, "5", "0", ""},
		{"actual cost variance goes to inventory", false, "5.10", "1", "1400"},
		{"standard cost variance goes to PPV", true, "4.90", "-1", "5200"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item := grirItem("10", "5")
			in := invoiceFor(item, "10", tc.price, august15)
			in.Lines[0].StandardCost = tc.standard
			ledger := &domain.FakeLedger{}
			entry, err := domain.PostInvoice(context.Background(), in, domain.FakePeriods{Status: domain.PeriodOpen}, ledger, testProfile)
			if err != nil {
				t.Fatal(err)
			}
			sum := decimal.Zero
			for _, l := range entry.Lines {
				sum = sum.Add(l.Amount.Value)
			}
			if !sum.IsZero() {
				t.Fatalf("entry not balanced: %s", sum)
			}
			if entry.IdempotencyKey != "post:receiving:vendorinvoice:vi-1:invoice" {
				t.Fatalf("key %s", entry.IdempotencyKey)
			}
			if got := ledger.Balance("2100"); !got.Equal(dec("50")) {
				t.Fatalf("GR/IR cleared %s, want 50", got)
			}
			if tc.wantAccount != "" && !ledger.Balance(tc.wantAccount).Equal(dec(tc.wantVariance)) {
				t.Fatalf("%s variance %s, want %s", tc.wantAccount, ledger.Balance(tc.wantAccount), tc.wantVariance)
			}
			if !item.OpenAmount().IsZero() || !item.InvoicedQty.Equal(dec("10")) {
				t.Fatalf("item not fully cleared: %+v", item)
			}
		})
	}
}

func TestPostInvoice_SplitLinesExhaustingItemClearExactRemainder(t *testing.T) {
	item := grirItem("3", "1")
	in := invoiceFor(item, "1", "1", august15)
	in.Lines = append(in.Lines, in.Lines[0], in.Lines[0])
	if _, err := domain.PostInvoice(context.Background(), in, domain.FakePeriods{Status: domain.PeriodOpen}, &domain.FakeLedger{}, testProfile); err != nil {
		t.Fatal(err)
	}
	if !item.OpenAmount().IsZero() || !item.InvoicedQty.Equal(dec("3")) {
		t.Fatalf("open %s invoiced %s, want 0 and 3", item.OpenAmount(), item.InvoicedQty)
	}
}

func TestPostInvoice_CurrencyMismatchRejected(t *testing.T) {
	item := grirItem("10", "5")
	in := invoiceFor(item, "10", "5", august15)
	in.Currency = "EUR"
	ledger := &domain.FakeLedger{}
	_, err := domain.PostInvoice(context.Background(), in, domain.FakePeriods{Status: domain.PeriodOpen}, ledger, testProfile)
	if got := codeOf(t, err); got != domain.CodeInvalidReceipt {
		t.Fatalf("code %s", got)
	}
	if len(ledger.Entries) != 0 || !item.InvoicedQty.IsZero() {
		t.Fatalf("rejection must not touch ledger or GR/IR")
	}
}
