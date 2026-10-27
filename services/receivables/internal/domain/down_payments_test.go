package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func balanced(t *testing.T, lines []Posting) {
	t.Helper()
	debit, credit := decimal.Zero, decimal.Zero
	for _, l := range lines {
		debit, credit = debit.Add(l.Debit), credit.Add(l.Credit)
	}
	if !debit.Equal(credit) {
		t.Fatalf("unbalanced postings %+v", lines)
	}
}

func TestPostings_AccountsPerKind(t *testing.T) {
	amt := d("2000.00")
	tests := []struct {
		name          string
		lines         []Posting
		debit, credit string
	}{
		{"down payment received", ReceiptPostings(KindDownPayment, amt), AccountBank, AccountDownPayment},
		{"deposit received", ReceiptPostings(KindSecurityDeposit, amt), AccountBank, AccountSecurityDeposit},
		// AC-30.15: clearing posts Dr 2190 2,000.00 / Cr 1200 2,000.00.
		{"down payment cleared", ClearingPostings(KindDownPayment, amt), AccountDownPayment, AccountReceivable},
		{"deposit offset", ClearingPostings(KindSecurityDeposit, amt), AccountSecurityDeposit, AccountReceivable},
		{"deposit released", ReleasePostings(KindSecurityDeposit, amt), AccountSecurityDeposit, AccountBank},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			balanced(t, tc.lines)
			if tc.lines[0].Account != tc.debit || !tc.lines[0].Debit.Equal(amt) || tc.lines[1].Account != tc.credit || !tc.lines[1].Credit.Equal(amt) {
				t.Fatalf("lines = %+v, want Dr %s / Cr %s %s", tc.lines, tc.debit, tc.credit, amt)
			}
		})
	}
}

func TestClearingAmount(t *testing.T) {
	tests := []struct {
		name                 string
		remaining, open, req string
		want, code           string
	}{
		// AC-30.15: 2,000.00 down payment against a 5,000.00 invoice clears 2,000.00.
		{"proposal bounded by the down payment", "2000", "5000", "", "2000", ""},
		{"proposal bounded by the item", "2000", "500", "", "500", ""},
		{"partial request", "2000", "5000", "750.50", "750.50", ""},
		{"request above the proposal is AR-0017", "2000", "5000", "2000.01", "", CodeClearingExceeded},
		{"zero request", "2000", "5000", "0", "", CodeClearingExceeded},
		{"nothing remains", "0", "5000", "", "", CodeClearingExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req *decimal.Decimal
			if tc.req != "" {
				v := d(tc.req)
				req = &v
			}
			got, err := ClearingAmount(d(tc.remaining), d(tc.open), req)
			if tc.code != "" {
				if err == nil || err.Code != tc.code {
					t.Fatalf("err = %v, want %s", err, tc.code)
				}
				return
			}
			if err != nil || !got.Equal(d(tc.want)) {
				t.Fatalf("got %s, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestClearingProposal_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := decimal.New(rapid.Int64Range(-1000, 1_000_000).Draw(t, "remaining"), -2)
		o := decimal.New(rapid.Int64Range(-1000, 1_000_000).Draw(t, "open"), -2)
		got := ClearingProposal(r, o)
		if got.IsNegative() || (got.IsPositive() && (got.GreaterThan(r) || got.GreaterThan(o))) {
			t.Fatalf("proposal %s for remaining %s and open %s", got, r, o)
		}
		if r.IsPositive() && o.IsPositive() && !got.Equal(decimal.Min(r, o)) {
			t.Fatalf("proposal %s, want min(%s, %s)", got, r, o)
		}
	})
}

func TestDownPaymentMoves(t *testing.T) {
	tests := []struct {
		name string
		got  bool
		want bool
	}{
		{"receive a request", ReceiveAllowed(DownRequested), true},
		{"receive twice", ReceiveAllowed(DownReceived), false},
		{"cancel a request", CancelAllowed(DownRequested), true},
		{"cancel once received", CancelAllowed(DownReceived), false},
		{"clear when received", ClearAllowed(DownReceived), true},
		{"clear a request", ClearAllowed(DownRequested), false},
		{"release a deposit", ReleaseAllowed(KindSecurityDeposit, DownReceived), true},
		{"release a down payment", ReleaseAllowed(KindDownPayment, DownReceived), false},
		{"release a released deposit", ReleaseAllowed(KindSecurityDeposit, DownReleased), false},
		{"down payment clears an invoice", ClearableSource(KindDownPayment, "INVOICE"), true},
		{"down payment never clears a fee", ClearableSource(KindDownPayment, "FEE"), false},
		{"deposit offsets a fee", ClearableSource(KindSecurityDeposit, "FEE"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %v want %v", tc.got, tc.want)
			}
		})
	}
	if StatusAfterClearing(d("2000"), d("2000")) != DownCleared || StatusAfterClearing(d("2000"), d("1999.99")) != DownReceived {
		t.Fatal("a down payment is CLEARED only when nothing remains")
	}
}
