package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var (
	d0 = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	d1 = d0.AddDate(0, 0, 1)
)

func baseReason() *ReasonInfo {
	return &ReasonInfo{Code: "ERR01", AppliesTo: []string{"ledger"}, AlternativePostingDateAllowed: true, Active: true}
}

func TestCheckReversal_TableDriven(t *testing.T) {
	cases := []struct {
		name     string
		policy   ReversalPolicy
		reason   *ReasonInfo
		req      ReversalRequest
		negative bool
		codes    []string
	}{
		{
			name:   "reason missing while required",
			policy: ReversalPolicy{ReasonRequired: true},
			req:    ReversalRequest{OriginalDate: d0, ReversalDate: d0},
			codes:  []string{"LEDGER_REASON_REQUIRED"},
		},
		{
			name:   "reason missing and not required",
			policy: ReversalPolicy{ReasonRequired: false},
			req:    ReversalRequest{OriginalDate: d0, ReversalDate: d0},
		},
		{
			name:   "unknown code",
			policy: ReversalPolicy{ReasonRequired: true},
			req:    ReversalRequest{ReasonCode: "NOPE", OriginalDate: d0, ReversalDate: d0},
			codes:  []string{"LEDGER_REASON_REQUIRED"},
		},
		{
			name:   "inactive code",
			policy: ReversalPolicy{ReasonRequired: true},
			reason: func() *ReasonInfo { r := baseReason(); r.Active = false; return r }(),
			req:    ReversalRequest{ReasonCode: "ERR01", OriginalDate: d0, ReversalDate: d0},
			codes:  []string{"LEDGER_REASON_REQUIRED"},
		},
		{
			name:   "code does not apply to ledger",
			policy: ReversalPolicy{ReasonRequired: true},
			reason: func() *ReasonInfo { r := baseReason(); r.AppliesTo = []string{"bank"}; return r }(),
			req:    ReversalRequest{ReasonCode: "ERR01", OriginalDate: d0, ReversalDate: d0},
			codes:  []string{"LEDGER_REASON_INVALID_MODULE"},
		},
		{
			name:   "comment required and blank",
			policy: ReversalPolicy{ReasonRequired: true},
			reason: func() *ReasonInfo { r := baseReason(); r.RequiresComment = true; return r }(),
			req:    ReversalRequest{ReasonCode: "ERR01", ReasonComment: "   ", OriginalDate: d0, ReversalDate: d0},
			codes:  []string{"LEDGER_REASON_REQUIRED"},
		},
		{
			name:   "comment required and present",
			policy: ReversalPolicy{ReasonRequired: true},
			reason: func() *ReasonInfo { r := baseReason(); r.RequiresComment = true; return r }(),
			req:    ReversalRequest{ReasonCode: "ERR01", ReasonComment: "typo", OriginalDate: d0, ReversalDate: d0},
		},
		{
			name:   "alternative date not allowed and date differs",
			policy: ReversalPolicy{ReasonRequired: true},
			reason: func() *ReasonInfo { r := baseReason(); r.AlternativePostingDateAllowed = false; return r }(),
			req:    ReversalRequest{ReasonCode: "ERR01", OriginalDate: d0, ReversalDate: d1},
			codes:  []string{"LEDGER_DATE_INVALID"},
		},
		{
			name:   "alternative date not allowed and date equal",
			policy: ReversalPolicy{ReasonRequired: true},
			reason: func() *ReasonInfo { r := baseReason(); r.AlternativePostingDateAllowed = false; return r }(),
			req:    ReversalRequest{ReasonCode: "ERR01", OriginalDate: d0, ReversalDate: d0},
		},
		{
			name:   "negative code while ledger disabled",
			policy: ReversalPolicy{ReasonRequired: true, NegativeEnabled: false},
			reason: func() *ReasonInfo { r := baseReason(); r.NegativePostingAllowed = true; return r }(),
			req:    ReversalRequest{ReasonCode: "ERR01", OriginalDate: d0, ReversalDate: d0},
			codes:  []string{"LEDGER_NEGATIVE_POSTING_DISABLED"},
		},
		{
			name:     "negative code while ledger enabled",
			policy:   ReversalPolicy{ReasonRequired: true, NegativeEnabled: true},
			reason:   func() *ReasonInfo { r := baseReason(); r.NegativePostingAllowed = true; return r }(),
			req:      ReversalRequest{ReasonCode: "ERR01", OriginalDate: d0, ReversalDate: d0},
			negative: true,
		},
		{
			name:   "plain code is never negative",
			policy: ReversalPolicy{ReasonRequired: true, NegativeEnabled: true},
			reason: baseReason(),
			req:    ReversalRequest{ReasonCode: "ERR01", OriginalDate: d0, ReversalDate: d0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			neg, v := CheckReversal(tc.policy, tc.reason, tc.req)
			if neg != tc.negative {
				t.Fatalf("negative = %v, want %v", neg, tc.negative)
			}
			if len(v) != len(tc.codes) {
				t.Fatalf("violations = %+v, want codes %v", v, tc.codes)
			}
			for i, c := range tc.codes {
				if v[i].Code != c {
					t.Fatalf("violation %d code = %s, want %s", i, v[i].Code, c)
				}
			}
		})
	}
}

func TestValidReasonCode(t *testing.T) {
	cases := map[string]bool{
		"ERR01": true, "A_B": true, "0": true, "": false, "ERRORCODE11": false,
		"err": false, "A-B": false, "A B": false,
	}
	for code, want := range cases {
		if got := ValidReasonCode(code); got != want {
			t.Errorf("ValidReasonCode(%q) = %v, want %v", code, got, want)
		}
	}
}

func TestValidReasonModule(t *testing.T) {
	if !ValidReasonModule("inventory_counting") || ValidReasonModule("payroll") {
		t.Fatal("reason module set mismatch")
	}
}

func TestAggregateBalances_NegativeLineReducesOriginalSide(t *testing.T) {
	key := []byte{1}
	amt := decimal.RequireFromString("100")
	// A reversal of a debit of 100, posted as a negative posting: credit side
	// line flagged Negative must reduce the debit projection by 100.
	lines := []ValidLine{
		{Side: SideCredit, Amount: amt, Negative: true, CombinationID: key, DisplayName: "A"},
	}
	agg := AggregateBalances("2026-03", lines)
	if len(agg) != 1 {
		t.Fatalf("aggregates = %d, want 1", len(agg))
	}
	if a := agg[0]; !a.Debit.Equal(amt.Neg()) || !a.Credit.IsZero() {
		t.Fatalf("debit=%s credit=%s, want debit -100 credit 0", a.Debit, a.Credit)
	}
}
