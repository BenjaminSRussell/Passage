package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func rfqCode(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestNextRFQStatus_Table(t *testing.T) {
	cases := []struct {
		status, action, want string
		wantCode             string
	}{
		{RFQDraft, RFQActionSend, RFQSent, ""},
		{RFQSent, RFQActionSend, "", CodeRFQState},
		{RFQSent, RFQActionClose, RFQClosed, ""},
		{RFQRepliesOpen, RFQActionClose, RFQClosed, ""},
		{RFQRepliesOpen, RFQActionAward, RFQAwarded, ""},
		{RFQClosed, RFQActionAward, RFQAwarded, ""},
		{RFQSent, RFQActionAward, "", CodeRFQState},
		{RFQDraft, RFQActionCancel, RFQCancelled, ""},
		{RFQSent, RFQActionCancel, RFQCancelled, ""},
		{RFQClosed, RFQActionCancel, RFQCancelled, ""},
		{RFQAwarded, RFQActionCancel, "", CodeRFQState},
		{RFQAwarded, RFQActionAward, "", CodeRFQState},
		{RFQDraft, RFQActionClose, "", CodeRFQState},
		{RFQCancelled, RFQActionSend, "", CodeRFQState},
	}
	for _, c := range cases {
		t.Run(c.status+"/"+c.action, func(t *testing.T) {
			got, err := NextRFQStatus(c.status, c.action)
			if c.wantCode != "" {
				if rfqCode(err) != c.wantCode {
					t.Fatalf("code = %q, want %q (err %v)", rfqCode(err), c.wantCode, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestCanRecordReply_OnlyWhileOpen(t *testing.T) {
	for status, ok := range map[string]bool{RFQDraft: false, RFQSent: true, RFQRepliesOpen: true,
		RFQClosed: false, RFQAwarded: false, RFQCancelled: false} {
		if err := CanRecordReply(status); (err == nil) != ok {
			t.Errorf("CanRecordReply(%s) err = %v, want accepted=%v", status, err, ok)
		}
	}
}

func TestIsLate_AfterDeadlineOnly(t *testing.T) {
	deadline := time.Date(2026, 11, 1, 17, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		at   time.Time
		want bool
	}{
		"before":   {deadline.Add(-time.Second), false},
		"exact":    {deadline, false},
		"after":    {deadline.Add(time.Second), true},
		"next-day": {deadline.AddDate(0, 0, 1), true},
	}
	for name, c := range cases {
		if got := IsLate(deadline, c.at); got != c.want {
			t.Errorf("%s: IsLate = %v, want %v", name, got, c.want)
		}
	}
}

func TestCheckRevision_LateCannotReplaceOnTime(t *testing.T) {
	onTime, late := &RFQReply{VendorID: "v1"}, &RFQReply{VendorID: "v1", Late: true}
	cases := []struct {
		name     string
		existing *RFQReply
		late     bool
		wantCode string
	}{
		{"first reply on time", nil, false, ""},
		{"first reply late", nil, true, ""},
		{"on-time revision of on-time", onTime, false, ""},
		{"late revision of on-time", onTime, true, CodeRFQLateRevision},
		{"late revision of late", late, true, ""},
	}
	for _, c := range cases {
		if got := rfqCode(CheckRevision(c.existing, c.late)); got != c.wantCode {
			t.Errorf("%s: code %q, want %q", c.name, got, c.wantCode)
		}
	}
}

func rfqReply(id, line, vendor, price string, lead int, late bool) RFQReply {
	return RFQReply{ID: id, LineID: line, VendorID: vendor, UnitPrice: decimal.RequireFromString(price),
		PriceUnit: decimal.NewFromInt(1), Currency: "USD", LeadTimeDays: lead, Late: late}
}

func TestRankLine_Table(t *testing.T) {
	qty := decimal.NewFromInt(100)
	replies := []RFQReply{
		rfqReply("r1", "L", "v1", "10.00", 5, false),
		rfqReply("r2", "L", "v2", "9.50", 14, false),
		rfqReply("r3", "L", "v3", "9.50", 7, false),
		rfqReply("r4", "L", "v4", "1.00", 1, true), // late: excluded whatever its price
	}
	cases := []struct {
		rule string
		want []string
	}{
		{RankLowestPrice, []string{"r3", "r2", "r1"}}, // price ties broken by lead time (7 before 14)
		{RankLeadTime, []string{"r1", "r3", "r2"}},    // lead times 5, 7, 14
	}
	for _, c := range cases {
		ranked, excluded, err := RankLine(qty, replies, c.rule)
		if err != nil {
			t.Fatalf("%s: %v", c.rule, err)
		}
		var got []string
		for _, r := range ranked {
			got = append(got, r.ReplyID)
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: order %v, want %v", c.rule, got, c.want)
		}
		if len(excluded) != 1 || excluded[0].ReplyID != "r4" || excluded[0].Reason != ReasonLate {
			t.Errorf("%s: excluded %+v, want r4 LATE", c.rule, excluded)
		}
		if !ranked[0].Total.Equal(ranked[0].UnitPrice.Mul(qty)) {
			t.Errorf("%s: total %s is not unit price × qty", c.rule, ranked[0].Total)
		}
	}
}

func TestRankLine_PriceUnitNormalises(t *testing.T) {
	// 100.00 per 100 pieces is 1.00 each; 1.50 each is dearer.
	per100 := RFQReply{ID: "a", VendorID: "v1", UnitPrice: decimal.NewFromInt(100), PriceUnit: decimal.NewFromInt(100)}
	each := RFQReply{ID: "b", VendorID: "v2", UnitPrice: decimal.RequireFromString("1.50"), PriceUnit: decimal.NewFromInt(1)}
	ranked, _, err := RankLine(decimal.NewFromInt(10), []RFQReply{each, per100}, RankLowestPrice)
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].ReplyID != "a" {
		t.Errorf("per-100 price should rank first, got %v", ranked)
	}
}

func TestRankLine_RejectsUnknownRule(t *testing.T) {
	if _, _, err := RankLine(decimal.NewFromInt(1), nil, "WEIGHTED_SCORE"); rfqCode(err) != CodeValidation {
		t.Errorf("unknown rule must be a validation error, got %v", err)
	}
}

// Property: ranking never returns a late reply, keeps every reply exactly once
// (ranked or excluded), numbers ranks 1..n and orders unit prices ascending for
// LOWEST_PRICE.
func TestRankLine_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 25).Draw(t, "n")
		var replies []RFQReply
		for i := 0; i < n; i++ {
			cents := rapid.Int64Range(0, 100000).Draw(t, "cents")
			lead := rapid.IntRange(0, 60).Draw(t, "lead")
			late := rapid.Bool().Draw(t, "late")
			replies = append(replies, RFQReply{ID: fmt.Sprintf("r%02d", i), LineID: "L",
				VendorID: fmt.Sprintf("v%02d", i), UnitPrice: decimal.New(cents, -2),
				PriceUnit: decimal.NewFromInt(1), LeadTimeDays: lead, Late: late})
		}
		qty := decimal.NewFromInt(rapid.Int64Range(1, 1000).Draw(t, "qty"))
		ranked, excluded, err := RankLine(qty, replies, RankLowestPrice)
		if err != nil {
			t.Fatal(err)
		}
		if len(ranked)+len(excluded) != n {
			t.Fatalf("ranked %d + excluded %d != %d replies", len(ranked), len(excluded), n)
		}
		for i, r := range ranked {
			if r.Rank != i+1 {
				t.Fatalf("rank %d at position %d", r.Rank, i)
			}
			if i > 0 && r.UnitPrice.LessThan(ranked[i-1].UnitPrice) {
				t.Fatalf("unit prices not ascending at %d", i)
			}
		}
		for _, e := range excluded {
			if e.Reason != ReasonLate {
				t.Fatalf("excluded reply %s without LATE reason", e.ReplyID)
			}
		}
	})
}

func rfqAwardFixture(status string) RFQ {
	expired, valid := Day{time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)}, Day{time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)}
	return RFQ{Status: status,
		Lines: []RFQLine{{ID: "L1", LineNo: 1}, {ID: "L2", LineNo: 2}},
		Replies: []RFQReply{
			{ID: "R1", LineID: "L1", VendorID: "v1", ValidUntil: &valid},
			{ID: "R2", LineID: "L1", VendorID: "v2", Late: true},
			{ID: "R3", LineID: "L2", VendorID: "v2"},
			{ID: "R4", LineID: "L2", VendorID: "v3", ValidUntil: &expired},
		}}
}

// awardDay is "today" for the award table: R1 is valid through it, R4 expired the day before.
var awardDay = Day{time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)}

func TestCheckAward_Table(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		awards   []Award
		wantCode string
	}{
		{"ok per line", RFQRepliesOpen, []Award{{LineID: "L1", ReplyID: "R1"}, {LineID: "L2", ReplyID: "R3"}}, ""},
		{"closed is awardable", RFQClosed, []Award{{LineID: "L1", ReplyID: "R1"}}, ""},
		{"no awards", RFQRepliesOpen, nil, CodeRFQAward},
		{"sent is not awardable", RFQSent, []Award{{LineID: "L1", ReplyID: "R1"}}, CodeRFQState},
		{"unknown line", RFQRepliesOpen, []Award{{LineID: "LX", ReplyID: "R1"}}, CodeRFQAward},
		{"line awarded twice", RFQRepliesOpen, []Award{{LineID: "L1", ReplyID: "R1"}, {LineID: "L1", ReplyID: "R1"}}, CodeRFQAward},
		{"reply for another line", RFQRepliesOpen, []Award{{LineID: "L2", ReplyID: "R1"}}, CodeRFQAward},
		{"late reply without reason", RFQRepliesOpen, []Award{{LineID: "L1", ReplyID: "R2"}}, CodeRFQLate},
		{"late reply with reason", RFQRepliesOpen, []Award{{LineID: "L1", ReplyID: "R2", OverrideReason: "only bid"}}, ""},
		{"blank reason is no reason", RFQRepliesOpen, []Award{{LineID: "L1", ReplyID: "R2", OverrideReason: "   "}}, CodeRFQLate},
		{"valid through today", RFQRepliesOpen, []Award{{LineID: "L1", ReplyID: "R1"}}, ""},
		{"expired reply", RFQRepliesOpen, []Award{{LineID: "L2", ReplyID: "R4"}}, CodeRFQAward},
		{"unknown reply", RFQRepliesOpen, []Award{{LineID: "L2", ReplyID: "RX"}}, CodeRFQAward},
		{"awarded RFQ", RFQAwarded, []Award{{LineID: "L1", ReplyID: "R1"}}, CodeRFQState},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckAward(rfqAwardFixture(c.status), c.awards, awardDay)
			if got := rfqCode(err); got != c.wantCode {
				t.Fatalf("code = %q, want %q (err %v)", got, c.wantCode, err)
			}
		})
	}
}
