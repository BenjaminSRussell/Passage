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

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("want *domain.Error, got %v", err)
	}
	return de.Code
}

func receiptWith(line domain.ReceiptLine) domain.Receipt {
	line.UnitPrice = money.Must("10.00", "USD")
	return domain.Receipt{ID: "r-1", LegalEntityID: "le-1", Date: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), State: domain.Arrived, Lines: []domain.ReceiptLine{line}}
}

func TestRegister_OverDelivery(t *testing.T) {
	tests := []struct {
		name     string
		qty, pct string
		wantCode string
	}{
		{"exact remaining", "100", "0", ""},
		{"above remaining with no allowance", "101", "0", domain.CodeOverDelivery},
		{"inside allowance", "105", "5", ""},
		{"just above allowance", "105.01", "5", domain.CodeOverDelivery},
		{"zero quantity", "0", "0", domain.CodeInvalidReceipt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := receiptWith(domain.ReceiptLine{LineNo: 1, ProductID: "p", Qty: dec(tc.qty), OrderedRemaining: dec("100"), OverDeliveryPct: dec(tc.pct)})
			next, err := r.Register(nil)
			if tc.wantCode == "" {
				if err != nil || next.State != domain.Registered {
					t.Fatalf("got %v state %s", err, next.State)
				}
				return
			}
			if got := codeOf(t, err); got != tc.wantCode {
				t.Fatalf("code %s, want %s", got, tc.wantCode)
			}
			if next.State != domain.Arrived {
				t.Fatalf("nothing may be recorded on rejection, state %s", next.State)
			}
		})
	}
}

func serialLine(qty string, serials ...string) domain.ReceiptLine {
	return domain.ReceiptLine{LineNo: 1, ProductID: "p", Qty: dec(qty), OrderedRemaining: dec("3"), SerialTracked: true, Tracking: domain.Tracking{Serials: serials}}
}

func TestRegister_DuplicateSerial(t *testing.T) {
	_, err := receiptWith(serialLine("3", "S1", "S2", "S2")).Register(nil)
	if got := codeOf(t, err); got != domain.CodeDuplicateSerial {
		t.Fatalf("code %s", got)
	}
	corrected, err := receiptWith(serialLine("2", "S1", "S2")).Register(nil)
	if err != nil || len(corrected.Lines[0].Tracking.Serials) != 2 {
		t.Fatalf("corrected registration: %v %+v", err, corrected.Lines)
	}
	recorded := map[domain.SerialKey]struct{}{{ProductID: "p", Serial: "S2"}: {}}
	_, err = receiptWith(serialLine("2", "S1", "S2")).Register(recorded)
	if got := codeOf(t, err); got != domain.CodeDuplicateSerial {
		t.Fatalf("already recorded serial: code %s", got)
	}
	recorded = map[domain.SerialKey]struct{}{{ProductID: "other", Serial: "S2"}: {}}
	if _, err = receiptWith(serialLine("2", "S1", "S2")).Register(recorded); err != nil {
		t.Fatalf("same serial on another product must pass: %v", err)
	}
}

func TestRegister_SerialCountMustMatchQuantity(t *testing.T) {
	for name, line := range map[string]domain.ReceiptLine{
		"too few":        serialLine("3", "S1", "S2"),
		"fractional qty": serialLine("2.5", "S1", "S2"),
		"empty serial":   serialLine("2", "S1", ""),
		"no serials":     serialLine("1"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := receiptWith(line).Register(nil)
			if err == nil {
				t.Fatal("expected a rejection")
			}
		})
	}
}

func TestRegister_BatchDimensionsRecorded(t *testing.T) {
	line := domain.ReceiptLine{LineNo: 1, ProductID: "p", Qty: dec("5"), OrderedRemaining: dec("5"), BatchTracked: true, Tracking: domain.Tracking{Batch: "B-7"}}
	next, err := receiptWith(line).Register(nil)
	if err != nil || next.Lines[0].Tracking.Batch != "B-7" {
		t.Fatalf("batch not recorded: %v %+v", err, next.Lines)
	}
	line.Tracking.Batch = ""
	if _, err := receiptWith(line).Register(nil); codeOf(t, err) != domain.CodeInvalidReceipt {
		t.Fatalf("missing batch must be rejected: %v", err)
	}
}

func TestTransition(t *testing.T) {
	all := []domain.ReceiptState{domain.Arrived, domain.Registered, domain.Posted, domain.Corrected, domain.Cancelled}
	allowed := map[[2]domain.ReceiptState]bool{
		{domain.Arrived, domain.Registered}: true, {domain.Arrived, domain.Cancelled}: true,
		{domain.Registered, domain.Posted}: true, {domain.Registered, domain.Cancelled}: true,
		{domain.Posted, domain.Corrected}: true, {domain.Posted, domain.Cancelled}: true,
	}
	for _, from := range all {
		for _, to := range all {
			_, err := domain.Receipt{ID: "r", State: from}.Transition(to, false)
			if (err == nil) != allowed[[2]domain.ReceiptState{from, to}] {
				t.Errorf("%s -> %s: err=%v, allowed=%v", from, to, err, allowed[[2]domain.ReceiptState{from, to}])
			}
		}
	}
	if _, err := (domain.Receipt{ID: "r", State: domain.Posted}).Transition(domain.Cancelled, true); err == nil {
		t.Fatal("an invoiced posted receipt must not be cancellable")
	}
}

func TestReceiptPost_LedgerFailureKeepsStateAndWrapsError(t *testing.T) {
	boom := errors.New("ledger down")
	r := receiptWith(domain.ReceiptLine{LineNo: 1, ProductID: "p", Qty: dec("1"), OrderedRemaining: dec("1")})
	r.State = domain.Registered
	next, items, err := r.Post(context.Background(), &domain.FakeLedger{Err: boom}, testProfile)
	if !errors.Is(err, boom) || next.State != domain.Registered || items != nil {
		t.Fatalf("got state %s items %v err %v", next.State, items, err)
	}
}

func TestReceiptPost_StoresLedgerEntryID(t *testing.T) {
	r := receiptWith(domain.ReceiptLine{LineNo: 1, ProductID: "p", Qty: dec("1"), OrderedRemaining: dec("1")})
	r.State = domain.Registered
	next, _, err := r.Post(context.Background(), &domain.FakeLedger{}, testProfile)
	if err != nil || next.LedgerEntryID != "fake-entry-1" {
		t.Fatalf("got id %q err %v", next.LedgerEntryID, err)
	}
}

func postedReceipt(invoiced string) domain.Receipt {
	r := receiptWith(domain.ReceiptLine{LineNo: 1, ProductID: "p", Qty: dec("100"), InvoicedQty: dec(invoiced), OrderedRemaining: dec("100"), OverDeliveryPct: dec("5")})
	r.State = domain.Posted
	r.Lines[0].ClearedAmount = dec(invoiced).Mul(dec("10"))
	return r
}

func TestCorrect_RespectsInvoicedQuantityAndPostsTheDelta(t *testing.T) {
	tests := []struct {
		name     string
		invoiced string
		delta    string
		wantCode string
		wantQty  string
		wantAmt  string
	}{
		{"credit down to the invoiced quantity", "40", "-60", "", "40", "400.00"},
		{"credit below the invoiced quantity", "40", "-61", domain.CodeCorrectionBelow, "", ""},
		{"credit to zero", "0", "-100", domain.CodeInvalidReceipt, "", ""},
		{"debit inside the allowance", "0", "5", "", "105", "1050.00"},
		{"debit past the allowance", "0", "6", domain.CodeOverDelivery, "", ""},
		{"zero delta", "0", "0", domain.CodeInvalidReceipt, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ledger := &domain.FakeLedger{}
			id, changes, err := postedReceipt(tc.invoiced).Correct(context.Background(), ledger, testProfile, 3, time.Time{}, map[int]decimal.Decimal{1: dec(tc.delta)})
			if tc.wantCode != "" {
				if got := codeOf(t, err); got != tc.wantCode || len(ledger.Entries) != 0 {
					t.Fatalf("code %s with %d ledger entries, want %s and none", got, len(ledger.Entries), tc.wantCode)
				}
				return
			}
			if err != nil || id == "" || !changes[0].Qty.Equal(dec(tc.wantQty)) || changes[0].Amount.Value.StringFixed(2) != tc.wantAmt {
				t.Fatalf("id=%q changes=%+v err=%v", id, changes, err)
			}
			entry := ledger.Entries[0]
			if entry.IdempotencyKey != "post:receiving:productreceipt:r-1:correction:3" || !entry.Lines[0].Amount.Value.Equal(dec(tc.delta).Mul(dec("10"))) || !entry.Lines[1].Amount.Value.Equal(dec(tc.delta).Mul(dec("-10"))) {
				t.Fatalf("entry %+v", entry)
			}
		})
	}
}

func TestCorrect_RefusesUnpostedUnknownAndSerialLines(t *testing.T) {
	ledger := &domain.FakeLedger{}
	arrived := postedReceipt("0")
	arrived.State = domain.Registered
	if _, _, err := arrived.Correct(context.Background(), ledger, testProfile, 1, time.Time{}, map[int]decimal.Decimal{1: dec("1")}); codeOf(t, err) != domain.CodeInvalidTransition {
		t.Fatalf("an unposted receipt cannot be corrected: %v", err)
	}
	if _, _, err := postedReceipt("0").Correct(context.Background(), ledger, testProfile, 1, time.Time{}, map[int]decimal.Decimal{2: dec("1")}); codeOf(t, err) != domain.CodeInvalidReceipt {
		t.Fatalf("an unknown line must be refused: %v", err)
	}
	serial := postedReceipt("0")
	serial.Lines[0].SerialTracked = true
	if _, _, err := serial.Correct(context.Background(), ledger, testProfile, 1, time.Time{}, map[int]decimal.Decimal{1: dec("-1")}); codeOf(t, err) != domain.CodeInvalidReceipt {
		t.Fatalf("a serial-tracked line cannot be corrected by quantity: %v", err)
	}
	if _, _, err := postedReceipt("0").Correct(context.Background(), &domain.FakeLedger{Err: domain.ErrPeriodClosed}, testProfile, 1, time.Time{}, map[int]decimal.Decimal{1: dec("1")}); codeOf(t, err) != domain.CodePeriodClosed {
		t.Fatalf("a closed ledger period must map to the period code: %v", err)
	}
	if len(ledger.Entries) != 0 {
		t.Fatalf("refused corrections reached the ledger")
	}
}

func TestCancel_ReversesOnlyPostedReceiptsWithoutInvoices(t *testing.T) {
	ledger := &domain.FakeLedger{}
	id, err := postedReceipt("0").Cancel(context.Background(), ledger, testProfile, time.Time{})
	if err != nil || id == "" || len(ledger.Entries) != 1 || ledger.Entries[0].Lines[0].Amount.Value.String() != "-1000" || ledger.Balance("2100").String() != "1000" {
		t.Fatalf("id=%q entries=%+v err=%v", id, ledger.Entries, err)
	}
	if _, err := postedReceipt("1").Cancel(context.Background(), ledger, testProfile, time.Time{}); codeOf(t, err) != domain.CodeInvalidTransition || len(ledger.Entries) != 1 {
		t.Fatalf("an invoiced receipt must not be cancelled: %v", err)
	}
	registered := postedReceipt("0")
	registered.State = domain.Registered
	if id, err := registered.Cancel(context.Background(), ledger, testProfile, time.Time{}); err != nil || id != "" || len(ledger.Entries) != 1 {
		t.Fatalf("a registered receipt cancels without a ledger entry: id=%q err=%v", id, err)
	}
	cancelled := postedReceipt("0")
	cancelled.State = domain.Cancelled
	if _, err := cancelled.Cancel(context.Background(), ledger, testProfile, time.Time{}); codeOf(t, err) != domain.CodeInvalidTransition {
		t.Fatalf("a cancelled receipt cannot be cancelled again: %v", err)
	}
}

func TestCorrect_LeavesNoRoundingResidualWhenQuantityEqualsInvoiced(t *testing.T) {
	r := receiptWith(domain.ReceiptLine{LineNo: 1, ProductID: "p", Qty: dec("3"), InvoicedQty: dec("1"), ClearedAmount: dec("0.33"), OrderedRemaining: dec("3")})
	r.State = domain.Posted
	r.Lines[0].UnitPrice = money.Amount{Value: dec("0.335"), Currency: "USD"}
	ledger := &domain.FakeLedger{}
	_, changes, err := r.Correct(context.Background(), ledger, testProfile, 1, time.Time{}, map[int]decimal.Decimal{1: dec("-2")})
	if err != nil || changes[0].Amount.Value.String() != "0.33" {
		t.Fatalf("changes=%+v err=%v, want the amount to equal the cleared 0.33, not the rounded 0.34", changes, err)
	}
	if got := ledger.Entries[0].Lines[0].Amount.Value.String(); got != "-0.67" {
		t.Fatalf("inventory delta %s, want -0.67 (1.00 received minus 0.33 kept)", got)
	}
}

func TestAdjustments_PostingDateDefaultsToReceiptDateAndNeverPrecedesIt(t *testing.T) {
	ledger := &domain.FakeLedger{}
	r := postedReceipt("0")
	if _, _, err := r.Correct(context.Background(), ledger, testProfile, 1, time.Time{}, map[int]decimal.Decimal{1: dec("1")}); err != nil || !ledger.Entries[0].PostingDate.Equal(r.Date) {
		t.Fatalf("default posting date: %v %+v", err, ledger.Entries)
	}
	later := r.Date.AddDate(0, 0, 5)
	if _, err := r.Cancel(context.Background(), ledger, testProfile, later); err != nil || !ledger.Entries[1].PostingDate.Equal(later) {
		t.Fatalf("explicit posting date: %v %+v", err, ledger.Entries)
	}
	before := r.Date.AddDate(0, 0, -1)
	if _, _, err := r.Correct(context.Background(), ledger, testProfile, 2, before, map[int]decimal.Decimal{1: dec("1")}); codeOf(t, err) != domain.CodeInvalidReceipt {
		t.Fatalf("a posting date before the receipt date must be refused: %v", err)
	}
	if _, err := r.Cancel(context.Background(), ledger, testProfile, before); codeOf(t, err) != domain.CodeInvalidReceipt || len(ledger.Entries) != 2 {
		t.Fatalf("cancel with an early date must be refused without posting: %v", err)
	}
}

func TestFakeLedger_ReplaysIdenticalBodiesAndRefusesChangedOnes(t *testing.T) {
	ledger := &domain.FakeLedger{}
	entry := domain.JournalEntry{IdempotencyKey: "k", Lines: []domain.JournalLine{{Account: "1400", Amount: money.Must("5.00", "USD")}, {Account: "2100", Amount: money.Must("-5.00", "USD")}}}
	first, err := ledger.Post(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ledger.Post(context.Background(), entry)
	if err != nil || again != first || len(ledger.Entries) != 1 || ledger.Balance("2100").String() != "-5" {
		t.Fatalf("replay id=%q err=%v entries=%d GR/IR=%s, want the same id, one entry and -5", again, err, len(ledger.Entries), ledger.Balance("2100"))
	}
	entry.Lines[0].Amount = money.Must("6.00", "USD")
	if _, err := ledger.Post(context.Background(), entry); !errors.Is(err, domain.ErrLedgerConflict) || len(ledger.Entries) != 1 {
		t.Fatalf("a changed body under the same key must conflict without posting: %v", err)
	}
}
