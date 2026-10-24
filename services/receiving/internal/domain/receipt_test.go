package domain_test

import (
	"context"
	"errors"
	"testing"
	"time"

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
