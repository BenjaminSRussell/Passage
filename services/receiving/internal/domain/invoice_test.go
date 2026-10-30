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
		ID: "vi-1", LegalEntityID: "le-1", LedgerID: "l-1", FiscalPeriodID: "fp-1", PostingDate: date, InvoiceDate: date, Currency: "USD",
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

func TestHoldInvoice_ReservesQuantityWithoutPostingAndPostHeldInvoiceMatchesPostInvoice(t *testing.T) {
	received := func() *domain.GrirItem { return grirItem("3", "3.3333") }
	split := func(item *domain.GrirItem) domain.VendorInvoice {
		in := invoiceFor(item, "1", "3.40", august15)
		in.Lines = append(in.Lines, in.Lines[0], in.Lines[0])
		return in
	}
	open := domain.FakePeriods{Status: domain.PeriodOpen}

	direct, directLedger := received(), &domain.FakeLedger{}
	if _, err := domain.PostInvoice(context.Background(), split(direct), open, directLedger, testProfile); err != nil {
		t.Fatal(err)
	}

	held := received()
	hold, err := domain.HoldInvoice(split(held), testProfile)
	if err != nil {
		t.Fatal(err)
	}
	if hold.LedgerEntryID != "" || !held.InvoicedQty.Equal(dec("3")) || !held.OpenAmount().IsZero() {
		t.Fatalf("hold %+v item %+v: want the quantity reserved and nothing posted", hold, held)
	}
	wantCleared := []decimal.Decimal{dec("3.33"), dec("3.33"), dec("3.3399")}
	for i, c := range hold.GrirCleared {
		if !c.Equal(wantCleared[i]) {
			t.Fatalf("clearings %v, want %v", hold.GrirCleared, wantCleared)
		}
	}

	stored := make([]domain.HeldLine, len(hold.GrirCleared))
	for i, c := range hold.GrirCleared {
		stored[i] = domain.HeldLine{Qty: dec("1"), UnitPrice: dec("3.40"), Clearing: c}
	}
	releaseLedger := &domain.FakeLedger{}
	doc := split(held)
	if _, err := domain.PostHeldInvoice(context.Background(), doc, stored, open, releaseLedger, testProfile); err != nil {
		t.Fatal(err)
	}
	if len(releaseLedger.Entries) != 1 || len(directLedger.Entries) != 1 {
		t.Fatalf("entries %d and %d", len(releaseLedger.Entries), len(directLedger.Entries))
	}
	a, b := directLedger.Entries[0], releaseLedger.Entries[0]
	if a.IdempotencyKey != b.IdempotencyKey || len(a.Lines) != len(b.Lines) {
		t.Fatalf("released entry %+v differs from the direct one %+v", b, a)
	}
	for i := range a.Lines {
		if a.Lines[i].Account != b.Lines[i].Account || !a.Lines[i].Amount.Value.Equal(b.Lines[i].Amount.Value) {
			t.Fatalf("line %d: released %+v, direct %+v", i, b.Lines[i], a.Lines[i])
		}
	}
	if got := releaseLedger.Balance("2000"); !got.Equal(dec("-10.20")) {
		t.Fatalf("vendor control %s, want -10.20 (3 x 3.40)", got)
	}
}

func TestHoldInvoice_ValuesTheDifferenceFromTheReceipt(t *testing.T) {
	item := grirItem("10", "5")
	entry, err := domain.HoldInvoice(invoiceFor(item, "10", "5.10", august15), testProfile)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"2100": "50", "1400": "1", "2000": "-51"}
	for _, l := range entry.Lines {
		if !l.Amount.Value.Equal(dec(want[l.Account])) {
			t.Fatalf("%s = %s, want %s", l.Account, l.Amount.Value, want[l.Account])
		}
	}
}

func TestHoldInvoice_RejectionsLeaveTheItemUntouchedAndAClosedPeriodIsCheckedOnRelease(t *testing.T) {
	item := grirItem("10", "5")
	if _, err := domain.HoldInvoice(invoiceFor(item, "11", "5", august15), testProfile); codeOf(t, err) != domain.CodeOverInvoice || !item.InvoicedQty.IsZero() {
		t.Fatalf("over-invoice err %v item %+v", err, item)
	}
	if _, err := domain.HoldInvoice(domain.VendorInvoice{ID: "vi-1", Currency: "USD"}, testProfile); codeOf(t, err) != domain.CodeInvalidReceipt {
		t.Fatalf("empty invoice err %v", err)
	}
	if _, err := domain.HoldInvoice(invoiceFor(item, "10", "5", august15), testProfile); err != nil {
		t.Fatalf("a hold does not consult the period: %v", err)
	}
	ledger := &domain.FakeLedger{}
	_, err := domain.PostHeldInvoice(context.Background(), invoiceFor(item, "10", "5", august15), []domain.HeldLine{{Qty: dec("10"), UnitPrice: dec("5"), Clearing: dec("50")}},
		domain.FakePeriods{Status: domain.PeriodClosed}, ledger, testProfile)
	if codeOf(t, err) != domain.CodePeriodClosed || len(ledger.Entries) != 0 {
		t.Fatalf("release into a closed period err %v entries %d", err, len(ledger.Entries))
	}
}
