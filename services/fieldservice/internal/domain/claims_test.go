package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// AC-45.13: a 180.00 part and 1.5 labor hours at a vendor rate of 60.00 claim 270.00;
// a vendor that does not cover labor (rate 0) is claimed the part alone.
func TestClaimableAmount_PartPlusLaborAtVendorRate(t *testing.T) {
	cases := []struct {
		part, hours, rate, currency, want string
	}{
		{"180.00", "1.5", "60.00", "USD", "270"},
		{"180.00", "1.5", "0", "USD", "180"},
		{"0.125", "0", "0", "USD", "0.12"},  // half-even to cents
		{"0.135", "0", "0", "USD", "0.14"},  // half-even to cents
		{"1000.5", "0", "0", "JPY", "1000"}, // JPY has no minor unit
		{"1.2345", "0", "0", "KWD", "1.234"},
	}
	for _, tc := range cases {
		got := ClaimableAmount(dec(tc.part), dec(tc.hours), dec(tc.rate), tc.currency)
		if !got.Equal(dec(tc.want)) {
			t.Errorf("ClaimableAmount(%s, %s, %s, %s) = %s, want %s", tc.part, tc.hours, tc.rate, tc.currency, got, tc.want)
		}
	}
}

// Only WARRANTY part cost and WARRANTY labor hours go on a claim; billable,
// agreement and travel lines do not.
func TestWarrantyShare_TakesWarrantyPartsAndLabor(t *testing.T) {
	p := Posting{Lines: []PostedLine{
		{Kind: KindPart, Coverage: CoverWarranty, Qty: dec("1"), WarrantyCost: dec("180")},
		{Kind: KindLabor, Coverage: CoverWarranty, Qty: dec("1.5")},
		{Kind: KindLabor, Coverage: CoverBillable, Qty: dec("2"), Amount: dec("200")},
		{Kind: KindTravel, Coverage: CoverWarranty, Qty: dec("1"), WarrantyCost: dec("40")},
		{Kind: KindPart, Coverage: CoverAgreement, Qty: dec("1")},
	}}
	part, hours, ok := WarrantyShare(p)
	if !ok || !part.Equal(dec("180")) || !hours.Equal(dec("1.5")) {
		t.Fatalf("share = %s, %s, %v", part, hours, ok)
	}
	if _, _, ok := WarrantyShare(Posting{Lines: []PostedLine{{Kind: KindLabor, Coverage: CoverBillable, Qty: dec("1")}}}); ok {
		t.Fatal("an order without warranty lines has a warranty share")
	}
}

func TestCheckSubmit_WindowReturnAndState(t *testing.T) {
	completed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		status string
		today  time.Time
		ret    bool
		retID  string
		want   string
	}{
		{"last day of the window", ClaimDraft, time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC), false, "", ""},
		{"day after the window", ClaimDraft, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), false, "", CodeClaimWindow},
		{"return required and missing", ClaimDraft, completed, true, "", CodeClaimReturn},
		{"return required and recorded", ClaimDraft, completed, true, "RMA-7", ""},
		{"second submission", ClaimSubmitted, completed, false, "", CodeClaimState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeOf(CheckSubmit(tc.status, completed, tc.today, 30, tc.ret, tc.retID)); got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
		})
	}
	err := CheckSubmit(ClaimDraft, completed, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), 30, false, "")
	if err == nil || err.Error() != "SVC-SWC-001: claim window for the vendor ended on 2026-10-31" {
		t.Fatalf("window message = %v", err)
	}
}

func claimOf(amounts ...string) []ClaimLine {
	out := make([]ClaimLine, 0, len(amounts))
	for _, a := range amounts {
		out = append(out, ClaimLine{ClaimedAmount: dec(a), AcceptedAmount: decimal.Zero})
	}
	return out
}

func amounts(values ...string) []decimal.Decimal {
	out := make([]decimal.Decimal, 0, len(values))
	for _, v := range values {
		out = append(out, dec(v))
	}
	return out
}

func TestDecide_BR45_12(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		decision string
		reason   string
		lines    []ClaimLine
		accepted []decimal.Decimal
		want     string
		total    string
	}{
		{"AC-45.13 partial 250 of 270", ClaimSubmitted, DecisionPartial, "", claimOf("270"), amounts("250"), "", "250"},
		{"accepted in full without amounts", ClaimSubmitted, DecisionAccepted, "", claimOf("270", "30"), nil, "", "300"},
		{"accepted must be in full", ClaimSubmitted, DecisionAccepted, "", claimOf("270"), amounts("250"), CodeClaimDecision, ""},
		{"accepted above claimed", ClaimSubmitted, DecisionPartial, "", claimOf("270"), amounts("270.01"), CodeClaimOverAccept, ""},
		{"partial of zero", ClaimSubmitted, DecisionPartial, "", claimOf("270"), amounts("0"), CodeClaimDecision, ""},
		{"partial of everything", ClaimSubmitted, DecisionPartial, "", claimOf("270"), amounts("270"), CodeClaimDecision, ""},
		{"partial needs one amount per line", ClaimSubmitted, DecisionPartial, "", claimOf("270", "30"), amounts("10"), CodeClaimDecision, ""},
		{"negative amount", ClaimSubmitted, DecisionPartial, "", claimOf("270", "30"), amounts("-1", "30"), CodeClaimDecision, ""},
		{"rejection needs a reason", ClaimSubmitted, DecisionRejected, "", claimOf("270"), nil, CodeClaimDecision, ""},
		{"rejection with a reason", ClaimSubmitted, DecisionRejected, "no fault found", claimOf("270"), nil, "", "0"},
		{"rejection accepts nothing", ClaimSubmitted, DecisionRejected, "x", claimOf("270"), amounts("10"), CodeClaimDecision, ""},
		{"decision on a draft", ClaimDraft, DecisionAccepted, "", claimOf("270"), nil, CodeClaimState, ""},
		{"unknown decision", ClaimSubmitted, "MAYBE", "", claimOf("270"), nil, CodeClaimDecision, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines, err := Decide(tc.status, tc.decision, tc.reason, "USD", tc.lines, tc.accepted)
			if got := codeOf(err); got != tc.want {
				t.Fatalf("code = %q, want %q (%v)", got, tc.want, err)
			}
			if err != nil {
				return
			}
			if _, total := ClaimTotals(lines); !total.Equal(dec(tc.total)) {
				t.Fatalf("accepted total = %s, want %s", total, tc.total)
			}
			if tc.lines[0].AcceptedAmount.IsPositive() {
				t.Fatal("Decide changed its input lines")
			}
		})
	}
}

func TestCheckSettle_MatchesReferenceCurrencyAndAcceptedTotal(t *testing.T) {
	cases := []struct {
		name, status, noteRef, noteCur, credit, want string
	}{
		{"AC-45.13 credit note of 250 settles", ClaimPartial, "VR-1", "USD", "250.00", ""},
		{"accepted claim settles", ClaimAccepted, "VR-1", "USD", "250", ""},
		{"other vendor reference", ClaimPartial, "VR-2", "USD", "250", CodeClaimReference},
		{"short credit", ClaimPartial, "VR-1", "USD", "240", CodeClaimCredit},
		{"other currency", ClaimPartial, "VR-1", "EUR", "250", CodeClaimCredit},
		{"before the decision", ClaimSubmitted, "VR-1", "USD", "250", CodeClaimState},
		{"rejected claims are not settled", ClaimRejected, "VR-1", "USD", "0", CodeClaimState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CheckSettle(tc.status, "VR-1", "USD", dec("250"), tc.noteRef, tc.noteCur, dec(tc.credit))
			if code := codeOf(err); code != tc.want {
				t.Fatalf("code = %q, want %q", code, tc.want)
			}
			if err == nil && !got.Equal(dec("250")) {
				t.Fatalf("recovered = %s", got)
			}
		})
	}
	if _, err := CheckSettle(ClaimPartial, "", "USD", dec("250"), "", "USD", dec("250")); codeOf(err) != CodeClaimReference {
		t.Fatalf("a claim without a vendor reference settled: %v", err)
	}
}

func TestCheckCloseClaim(t *testing.T) {
	for status, want := range map[string]string{ClaimRejected: "", ClaimSettled: "", ClaimAccepted: CodeClaimState, ClaimDraft: CodeClaimState, ClaimClosed: CodeClaimState} {
		if got := codeOf(CheckCloseClaim(status)); got != want {
			t.Errorf("close %s = %q, want %q", status, got, want)
		}
	}
}
