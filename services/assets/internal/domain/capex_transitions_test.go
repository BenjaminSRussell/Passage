package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestCapexTransitions_Table(t *testing.T) {
	cases := []struct {
		name     string
		from, to CapexStatus
		want     bool
	}{
		{"draft to submitted", CapexDraft, CapexSubmitted, true},
		{"submitted to in approval", CapexSubmitted, CapexInApproval, true},
		{"in approval to approved", CapexInApproval, CapexApproved, true},
		{"in approval to rejected", CapexInApproval, CapexRejected, true},
		{"in approval to postponed", CapexInApproval, CapexPostponed, true},
		{"postponed back to draft", CapexPostponed, CapexDraft, true},
		{"approved to released", CapexApproved, CapexReleased, true},
		{"released to closed", CapexReleased, CapexClosed, true},
		{"draft cannot skip approval", CapexDraft, CapexApproved, false},
		{"in approval cannot return to draft", CapexInApproval, CapexDraft, false},
		{"closed is terminal", CapexClosed, CapexDraft, false},
		{"rejected is terminal", CapexRejected, CapexDraft, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanTransitionCapex(tc.from, tc.to); got != tc.want {
				t.Fatalf("CanTransitionCapex(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
			err := TransitionCapex(tc.from, tc.to)
			if (err == nil) != tc.want {
				t.Fatalf("TransitionCapex(%s, %s) error = %v, want error %v", tc.from, tc.to, err, !tc.want)
			}
			if err != nil && ErrorCode(err) != CodeCapexState {
				t.Fatalf("code = %q, want %q", ErrorCode(err), CodeCapexState)
			}
		})
	}
}

func TestApprovalLevel_AC37_11(t *testing.T) {
	cases := []struct {
		amount string
		want   string
	}{
		{"1000.00", "PlantManager"},
		{"50000.00", "PlantManager"},
		{"50000.01", "CFO"},
		{"320000.00", "CFO"},
		{"500000.00", "CFO"},
		{"500000.01", "Board"},
	}
	for _, tc := range cases {
		if got := ApprovalLevel(decimal.RequireFromString(tc.amount)); got != tc.want {
			t.Errorf("ApprovalLevel(%s) = %s, want %s", tc.amount, got, tc.want)
		}
	}
}

func TestNeedsReapproval_BR37_12(t *testing.T) {
	// The limit is the lower of 10% of the approved amount and 10,000.
	cases := []struct {
		approved, next string
		want           bool
	}{
		{"200000.00", "210000.00", false}, // +10,000 is at the 10,000 cap (not above)
		{"200000.00", "210000.01", true},  // above the cap
		{"50000.00", "55000.00", false},   // +5,000 is exactly 10%
		{"50000.00", "55000.01", true},    // above 10%, below the 10,000 cap
		{"500000.00", "499000.00", false}, // decrease never re-approves
	}
	for _, tc := range cases {
		got := NeedsReapproval(decimal.RequireFromString(tc.approved), decimal.RequireFromString(tc.next))
		if got != tc.want {
			t.Errorf("NeedsReapproval(%s, %s) = %v, want %v", tc.approved, tc.next, got, tc.want)
		}
	}
}
