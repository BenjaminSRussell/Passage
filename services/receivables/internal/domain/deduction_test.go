package domain

import (
	"errors"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func errCode(err error) (int, string) {
	var de *Error
	if errors.As(err, &de) {
		return de.Status, de.Code
	}
	return 0, ""
}

// BR-30.12: OPEN -> MATCHED -> APPROVED | DENIED | SPLIT | WRITTEN_OFF; split
// and match only before a claim is attached, approval only after; terminal
// states accept nothing.
func TestCheckDeductionMove(t *testing.T) {
	allowed := map[string][]string{
		DeductionOpen:    {"match", "deny", "split", "write-off"},
		DeductionMatched: {"approve", "deny", "write-off"},
	}
	for _, status := range []string{DeductionOpen, DeductionMatched, DeductionApproved, DeductionDenied, DeductionSplit, DeductionWrittenOff} {
		for _, action := range DeductionActions {
			want := false
			for _, a := range allowed[status] {
				want = want || a == action
			}
			err := CheckDeductionMove(status, action)
			if want && err != nil {
				t.Errorf("%s from %s: %v, want allowed", action, status, err)
			}
			if !want {
				if st, code := errCode(err); st != http.StatusConflict || code != CodeDeductionState {
					t.Errorf("%s from %s: %v, want 409 %s", action, status, err, CodeDeductionState)
				}
			}
		}
	}
	if st, _ := errCode(CheckDeductionMove(DeductionOpen, "refund")); st != http.StatusNotFound {
		t.Errorf("unknown action: want 404")
	}
}

// AC-30.13's 300.00 promo deduction fits a 300.00 accrual; one cent more is AR-0014.
func TestCheckClaimCovers(t *testing.T) {
	if err := CheckClaimCovers(d("300.00"), d("300.00")); err != nil {
		t.Errorf("exact claim: %v", err)
	}
	if _, code := errCode(CheckClaimCovers(d("300.01"), d("300.00"))); code != CodeDeductionExceedsClaim {
		t.Errorf("above the claim: want %s", CodeDeductionExceedsClaim)
	}
}

func TestCheckSplit(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		ok    bool
	}{
		{"exact", []string{"200.00", "100.00"}, true},
		{"three parts", []string{"100", "100", "100.000000"}, true},
		{"one part", []string{"300.00"}, false},
		{"short", []string{"200.00", "99.99"}, false},
		{"over", []string{"200.00", "100.01"}, false},
		{"zero part", []string{"300.00", "0"}, false},
		{"negative part", []string{"400.00", "-100.00"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parts := make([]decimal.Decimal, len(tc.parts))
			for i, p := range tc.parts {
				parts[i] = d(p)
			}
			err := CheckSplit(d("300.00"), parts)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if err != nil {
				if _, code := errCode(err); code != CodeDeductionSplit {
					t.Errorf("code = %s, want %s", code, CodeDeductionSplit)
				}
			}
		})
	}
}

// Any split of a deduction into positive cents that sum to it is accepted
// (Σ children = parent), and moving one cent between the sum and a part breaks it.
func TestCheckSplit_SumProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cents := rapid.SliceOfN(rapid.Int64Range(1, 1_000_000), 2, 8).Draw(t, "cents")
		parts := make([]decimal.Decimal, len(cents))
		total := decimal.Zero
		for i, c := range cents {
			parts[i] = decimal.New(c, -2)
			total = total.Add(parts[i])
		}
		if err := CheckSplit(total, parts); err != nil {
			t.Fatalf("parts summing to %s refused: %v", total, err)
		}
		if err := CheckSplit(total.Add(decimal.New(1, -2)), parts); err == nil {
			t.Fatalf("parts one cent short accepted")
		}
	})
}

// deduction_write_off_limit defaults: 50.00 USD and 45.00 EUR; above it, or in a
// currency without a default, only ARWriteOffApprove may write off.
func TestCheckDeductionWriteOff(t *testing.T) {
	tests := []struct {
		amount, currency string
		approver, ok     bool
	}{
		{"50.00", "USD", false, true},
		{"50.01", "USD", false, false},
		{"50.01", "USD", true, true},
		{"45.00", "EUR", false, true},
		{"45.01", "EUR", false, false},
		{"0.01", "GBP", false, false},
		{"0.01", "GBP", true, true},
	}
	for _, tc := range tests {
		err := CheckDeductionWriteOff(d(tc.amount), tc.currency, tc.approver)
		if tc.ok != (err == nil) {
			t.Errorf("%s %s approver=%v: %v, want ok=%v", tc.amount, tc.currency, tc.approver, err, tc.ok)
		}
		if err != nil {
			if st, code := errCode(err); st != http.StatusForbidden || code != CodeWriteOffTolerance {
				t.Errorf("%s %s: %d %s, want 403 AR-0006", tc.amount, tc.currency, st, code)
			}
		}
	}
}

func TestDeductionWriteOffPostings_Balance(t *testing.T) {
	lines := DeductionWriteOffPostings(d("42.50"))
	balanced(t, lines)
	if lines[0].Account != "6110" || !lines[0].Debit.Equal(d("42.50")) || lines[1].Account != "1200" || !lines[1].Credit.Equal(d("42.50")) {
		t.Errorf("postings = %+v, want Dr 6110 / Cr 1200", lines)
	}
}

func TestDeductionCodeLists(t *testing.T) {
	if !ValidDeductionType("SHORT_SHIP") || ValidDeductionType("short_ship") || ValidDeductionType("") {
		t.Error("deduction types must match the configured codes exactly")
	}
	if !ValidDenialReason("EXPIRED") || ValidDenialReason("LATE") {
		t.Error("denial reasons")
	}
	if !ValidClaimType("PROMOTION_FUND") || ValidClaimType("INVOICE") {
		t.Error("claim types")
	}
	if !ValidDeductionStatus("WRITTEN_OFF") || ValidDeductionStatus("CLOSED") {
		t.Error("statuses")
	}
}
