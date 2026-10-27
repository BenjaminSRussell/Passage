package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var sealedDeadline = time.Date(2026, 11, 1, 17, 0, 0, 0, time.UTC)

func sealedRFQ() RFQ {
	return RFQ{
		Number: "RFQ-00000001", Status: RFQRepliesOpen, Sealed: true, ReplyDeadline: sealedDeadline,
		Invitations: []RFQInvitation{{VendorID: "v1"}, {VendorID: "v2"}, {VendorID: "v3"}},
		Replies: []RFQReply{
			{ID: "r1", VendorID: "v1", UnitPrice: d("10")}, {ID: "r2", VendorID: "v2", UnitPrice: d("11")}, {ID: "r3", VendorID: "v3", UnitPrice: d("12")},
		},
		Criteria: []ScoringCriterion{{ID: "c1", Name: "Price", Weight: d("2")}, {ID: "c2", Name: "Quality", Weight: d("1")}},
		Scores:   []VendorScore{{VendorID: "v1", CriterionID: "c1", Score: 5}},
	}
}

// AC-17.14: at 16:59Z the compare view is refused with PRC-RFQ-004; at
// 17:00:01Z (and at 17:00 itself) the replies are visible.
func TestCheckSealedView_AC17_14(t *testing.T) {
	r := sealedRFQ()
	tests := []struct {
		name string
		now  time.Time
		code string
	}{
		{"16:59 is sealed", sealedDeadline.Add(-time.Minute), CodeRFQSealed},
		{"one nanosecond early is sealed", sealedDeadline.Add(-time.Nanosecond), CodeRFQSealed},
		{"at the deadline opens", sealedDeadline, ""},
		{"17:00:01 opens", sealedDeadline.Add(time.Second), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeOf(CheckSealedView(r, tc.now)); got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
		})
	}
	r.Sealed = false
	if err := CheckSealedView(r, sealedDeadline.Add(-time.Hour)); err != nil {
		t.Fatalf("an open RFQ is always visible: %v", err)
	}
}

func TestRedact(t *testing.T) {
	early := Redact(sealedRFQ(), sealedDeadline.Add(-time.Minute))
	if !early.RepliesSealed || len(early.Replies) != 0 || len(early.Scores) != 0 || early.ReplyCount != 3 {
		t.Fatalf("before the deadline: sealed %v, %d replies, %d scores, count %d; want sealed, none, none, 3",
			early.RepliesSealed, len(early.Replies), len(early.Scores), early.ReplyCount)
	}
	if len(early.Criteria) != 2 {
		t.Fatal("criteria are not secret and stay visible")
	}
	late := Redact(sealedRFQ(), sealedDeadline.Add(time.Second))
	if late.RepliesSealed || len(late.Replies) != 3 || len(late.Scores) != 1 || late.ReplyCount != 3 {
		t.Fatalf("after the deadline: %+v", late)
	}
	open := sealedRFQ()
	open.Sealed = false
	if got := Redact(open, sealedDeadline.Add(-time.Hour)); got.RepliesSealed || len(got.Replies) != 3 {
		t.Fatalf("an open RFQ is never redacted: %+v", got)
	}
}

func TestCheckSealedReply(t *testing.T) {
	r := sealedRFQ()
	if got := codeOf(CheckSealedReply(r, true)); got != CodeRFQSealedLate {
		t.Fatalf("late sealed reply: code %q, want %q", got, CodeRFQSealedLate)
	}
	if err := CheckSealedReply(r, false); err != nil {
		t.Fatalf("on-time sealed reply refused: %v", err)
	}
	r.Sealed = false
	if err := CheckSealedReply(r, true); err != nil {
		t.Fatalf("a late reply to an open RFQ is flagged, not refused: %v", err)
	}
}

func TestCheckScoring(t *testing.T) {
	after := sealedDeadline.Add(time.Hour)
	ok := []VendorScore{{VendorID: "v1", CriterionID: "c1", Score: 7}, {VendorID: "v1", CriterionID: "c2", Score: 10}}
	tests := []struct {
		name   string
		mutate func(*RFQ)
		vendor string
		scores []VendorScore
		now    time.Time
		code   string
	}{
		{"valid after the deadline", nil, "v1", ok, after, ""},
		{"closed RFQ can be scored", func(r *RFQ) { r.Status = RFQClosed }, "v1", ok, after, ""},
		{"sealed before the deadline", nil, "v1", ok, sealedDeadline.Add(-time.Hour), CodeRFQSealed},
		{"open RFQ before the deadline", func(r *RFQ) { r.Sealed = false }, "v1", ok, sealedDeadline.Add(-time.Hour), ""},
		{"draft RFQ", func(r *RFQ) { r.Status = RFQDraft }, "v1", ok, after, CodeRFQState},
		{"awarded RFQ", func(r *RFQ) { r.Status = RFQAwarded }, "v1", ok, after, CodeRFQState},
		{"uninvited vendor", nil, "v9", ok, after, CodeRFQNotInvited},
		{"no criteria", func(r *RFQ) { r.Criteria = nil }, "v1", ok, after, CodeValidation},
		{"no scores", nil, "v1", nil, after, CodeValidation},
		{"unknown criterion", nil, "v1", []VendorScore{{CriterionID: "zz", Score: 5}}, after, CodeValidation},
		{"criterion twice", nil, "v1", []VendorScore{{CriterionID: "c1", Score: 5}, {CriterionID: "c1", Score: 6}}, after, CodeValidation},
		{"score 11", nil, "v1", []VendorScore{{CriterionID: "c1", Score: 11}}, after, CodeValidation},
		{"score 0", nil, "v1", []VendorScore{{CriterionID: "c1", Score: 0}}, after, CodeValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := sealedRFQ()
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			if got := codeOf(CheckScoring(r, tc.vendor, tc.scores, tc.now)); got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestValidateCriteria(t *testing.T) {
	c := func(name, weight string) ScoringCriterion { return ScoringCriterion{Name: name, Weight: d(weight)} }
	many := make([]ScoringCriterion, MaxScoringCriteria+1)
	for i := range many {
		many[i] = c(string(rune('A'+i)), "1")
	}
	tests := []struct {
		name string
		rule string
		in   []ScoringCriterion
		ok   bool
	}{
		{"no criteria for lowest price", RankLowestPrice, nil, true},
		{"weighted score needs criteria", RankWeightedScore, nil, false},
		{"positive weights", RankWeightedScore, []ScoringCriterion{c("Price", "3"), c("Quality", "1.5")}, true},
		{"blank name", RankLowestPrice, []ScoringCriterion{c("  ", "1")}, false},
		{"duplicate name ignoring case", RankLowestPrice, []ScoringCriterion{c("Price", "1"), c("price", "2")}, false},
		{"zero weight", RankLowestPrice, []ScoringCriterion{c("Price", "0")}, false},
		{"negative weight", RankLowestPrice, []ScoringCriterion{c("Price", "-1")}, false},
		{"weight with 5 decimals", RankLowestPrice, []ScoringCriterion{c("Price", "1.00001")}, false},
		{"weight above numeric(9,4)", RankLowestPrice, []ScoringCriterion{c("Price", "100000")}, false},
		{"21 criteria", RankLowestPrice, many, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCriteria(tc.rule, tc.in)
			if (err == nil) != tc.ok {
				t.Fatalf("ok = %v, err = %v", err == nil, err)
			}
			if err != nil && codeOf(err) != CodeValidation {
				t.Fatalf("code = %q", codeOf(err))
			}
		})
	}
}

func TestWeightedScores(t *testing.T) {
	criteria := []ScoringCriterion{{ID: "c1", Weight: d("2")}, {ID: "c2", Weight: d("1")}}
	scores := []VendorScore{
		{VendorID: "a", CriterionID: "c1", Score: 10},
		{VendorID: "a", CriterionID: "c2", Score: 5},
		{VendorID: "b", CriterionID: "c1", Score: 6},
		{VendorID: "b", CriterionID: "zz", Score: 10}, // a criterion not on the RFQ is ignored
	}
	got := WeightedScores(criteria, scores)
	if want := d("8.3333"); !got["a"].Equal(want) { // (2×10 + 1×5) / 3
		t.Fatalf("vendor a = %s, want %s", got["a"], want)
	}
	if want := d("4"); !got["b"].Equal(want) { // c2 unscored counts 0: 12 / 3
		t.Fatalf("vendor b = %s, want %s", got["b"], want)
	}
	if _, ok := got["c"]; ok {
		t.Fatal("an unscored vendor has no score")
	}
	if len(WeightedScores(nil, scores)) != 0 {
		t.Fatal("no criteria must give no scores")
	}
}

func TestRankLineScored_WeightedScore(t *testing.T) {
	replies := []RFQReply{
		{ID: "r1", VendorID: "cheap", UnitPrice: d("9"), PriceUnit: d("1"), Currency: "USD"},
		{ID: "r2", VendorID: "good", UnitPrice: d("10"), PriceUnit: d("1"), Currency: "USD"},
		{ID: "r3", VendorID: "unscored", UnitPrice: d("1"), PriceUnit: d("1"), Currency: "USD"},
		{ID: "r4", VendorID: "tied", UnitPrice: d("9.5"), PriceUnit: d("1"), Currency: "USD"},
		{ID: "r5", VendorID: "late", UnitPrice: d("1"), PriceUnit: d("1"), Currency: "USD", Late: true},
	}
	scores := map[string]decimal.Decimal{"cheap": d("4"), "good": d("9"), "tied": d("9"), "late": d("10")}
	ranked, excluded, err := RankLineScored(d("2"), replies, RankWeightedScore, scores)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range ranked {
		order = append(order, r.VendorID)
	}
	// good and tied score 9; the lower price (tied, 9.5) wins the tie; the
	// unscored vendor ranks last despite the lowest price; late is excluded.
	if want := []string{"tied", "good", "cheap", "unscored"}; len(order) != 4 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] || order[3] != want[3] {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if ranked[0].WeightedScore == nil || !ranked[0].WeightedScore.Equal(d("9")) || ranked[3].WeightedScore != nil {
		t.Fatalf("weighted scores not reported: %+v", ranked)
	}
	if len(excluded) != 1 || excluded[0].VendorID != "late" {
		t.Fatalf("excluded = %+v", excluded)
	}
	byPrice, _, _ := RankLineScored(d("2"), replies, RankLowestPrice, scores)
	if byPrice[0].VendorID != "unscored" {
		t.Fatalf("LOWEST_PRICE ignores scores: first = %s", byPrice[0].VendorID)
	}
}
