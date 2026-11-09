package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var amendDeadline = time.Date(2026, 3, 5, 17, 0, 0, 0, time.UTC)

func sentRFQ() RFQ {
	return RFQ{Number: "RFQ-1", Status: RFQSent, AmendmentsAllowed: true, ReplyDeadline: amendDeadline, RankingRule: RankLowestPrice}
}

func code(err error) string {
	if de, ok := err.(*Error); ok {
		return de.Code
	}
	return ""
}

func TestCheckAmendment(t *testing.T) {
	before := amendDeadline.Add(-time.Hour)
	after := amendDeadline.Add(time.Hour)
	later := amendDeadline.Add(24 * time.Hour)
	earlier := amendDeadline.Add(-24 * time.Hour)
	crit := []ScoringCriterion{{Name: "Price", Weight: decimal.NewFromInt(1)}}
	scored := sentRFQ()
	scored.Scores = []VendorScore{{VendorID: "v", CriterionID: "c", Score: 5}}
	sealed := sentRFQ()
	sealed.Sealed = true
	noAmend := sentRFQ()
	noAmend.AmendmentsAllowed = false
	closed := sentRFQ()
	closed.Status = RFQClosed
	weighted := sentRFQ()
	weighted.RankingRule = RankWeightedScore
	replied := sentRFQ()
	replied.Status, replied.Replies = RFQRepliesOpen, []RFQReply{{VendorID: "v"}}
	sealedReplied := sealed
	sealedReplied.Status, sealedReplied.Replies = RFQRepliesOpen, []RFQReply{{VendorID: "v"}}

	tests := []struct {
		name string
		rfq  RFQ
		in   AmendmentInput
		now  time.Time
		want string
	}{
		{"note before the deadline", sentRFQ(), AmendmentInput{Summary: "Spec clarified"}, before, ""},
		{"new later deadline", sentRFQ(), AmendmentInput{Summary: "x", ReplyDeadline: &later}, before, ""},
		{"deadline passed, new deadline given", sentRFQ(), AmendmentInput{Summary: "x", ReplyDeadline: &later}, after, ""},
		{"deadline passed, none given", sentRFQ(), AmendmentInput{Summary: "x"}, after, CodeRFQAmend},
		{"earlier deadline", sentRFQ(), AmendmentInput{Summary: "x", ReplyDeadline: &earlier}, before, CodeRFQAmend},
		{"same deadline", sentRFQ(), AmendmentInput{Summary: "x", ReplyDeadline: &amendDeadline}, before, CodeRFQAmend},
		{"blank summary", sentRFQ(), AmendmentInput{Summary: "  "}, before, CodeRFQAmend},
		{"type without amendments", noAmend, AmendmentInput{Summary: "x"}, before, CodeRFQAmend},
		{"closed RFQ", closed, AmendmentInput{Summary: "x"}, before, CodeRFQState},
		{"sealed RFQ opened", sealed, AmendmentInput{Summary: "x", ReplyDeadline: &later}, after, CodeRFQAmend},
		{"sealed RFQ before the deadline", sealed, AmendmentInput{Summary: "x", ReplyDeadline: &later}, before, ""},
		{"criteria replaced", sentRFQ(), AmendmentInput{Summary: "x", ReplaceCriteria: true, Criteria: crit}, before, ""},
		{"criteria once open replies are readable", replied, AmendmentInput{Summary: "x", ReplaceCriteria: true, Criteria: crit}, before, CodeRFQAmend},
		{"deadline moved once open replies are readable", replied, AmendmentInput{Summary: "x", ReplyDeadline: &later}, before, ""},
		{"criteria on a sealed RFQ with unreadable replies", sealedReplied, AmendmentInput{Summary: "x", ReplaceCriteria: true, Criteria: crit}, before, ""},
		{"criteria on a sealed RFQ past its deadline", sealedReplied, AmendmentInput{Summary: "x", ReplyDeadline: &later, ReplaceCriteria: true, Criteria: crit}, after, CodeRFQAmend},
		{"criteria after scoring", scored, AmendmentInput{Summary: "x", ReplaceCriteria: true, Criteria: crit}, before, CodeRFQAmend},
		{"cleared criteria on a weighted RFQ", weighted, AmendmentInput{Summary: "x", ReplaceCriteria: true}, before, CodeValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := code(CheckAmendment(tc.rfq, tc.in, tc.now)); got != tc.want {
				t.Fatalf("code %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCheckEdit(t *testing.T) {
	now := amendDeadline.Add(-48 * time.Hour)
	draft := RFQ{Number: "RFQ-1", Status: RFQDraft}
	required := draft
	required.SealedRequired = true
	in := RFQEdit{ReplyDeadline: amendDeadline, RankingRule: RankLowestPrice}
	sealedIn := in
	sealedIn.Sealed = true
	past := in
	past.ReplyDeadline = now.Add(-time.Minute)
	noCrit := in
	noCrit.RankingRule = RankWeightedScore
	sent := draft
	sent.Status = RFQSent
	for _, tc := range []struct {
		name string
		rfq  RFQ
		in   RFQEdit
		want string
	}{
		{"draft", draft, in, ""},
		{"sent RFQ", sent, in, CodeRFQState},
		{"past deadline", draft, past, CodeValidation},
		{"seal required and kept", required, sealedIn, ""},
		{"seal required and cleared", required, in, CodeRFQEdit},
		{"weighted without criteria", draft, noCrit, CodeValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := code(CheckEdit(tc.rfq, tc.in, now)); got != tc.want {
				t.Fatalf("code %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRFQTypeValidate(t *testing.T) {
	ok := RFQType{Code: "SEALED-1", Name: "Sealed", RankingRule: RankLowestPrice, Status: RFQTypeActive}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RFQType){
		"lower-case code":     func(t *RFQType) { t.Code = "sealed" },
		"code too long":       func(t *RFQType) { t.Code = "ABCDEFGHIJKLMNOPQRSTU" },
		"blank name":          func(t *RFQType) { t.Name = " " },
		"unknown status":      func(t *RFQType) { t.Status = "GONE" },
		"unknown ranking":     func(t *RFQType) { t.RankingRule = "CHEAPEST" },
		"weighted no default": func(t *RFQType) { t.RankingRule = RankWeightedScore },
	} {
		bad := ok
		mutate(&bad)
		if bad.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
