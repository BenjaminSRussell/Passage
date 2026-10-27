package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

func (f *fakeStore) SetRFQScores(_ context.Context, id string, ifMatch int, vendorID string, scores []domain.VendorScore, now time.Time) (domain.RFQ, error) {
	f.rfqLog.scoredBy, f.rfqLog.scores, f.rfqLog.ifMatch = vendorID, scores, ifMatch
	cur, err := f.checkVersion(id, ifMatch)
	if err != nil {
		return cur, err
	}
	if err := domain.CheckScoring(cur, vendorID, scores, now); err != nil {
		return cur, err
	}
	f.rfq.Scores = append(f.rfq.Scores, scores...)
	f.rfq.Version++
	return *f.rfq, nil
}

func (f *fakeStore) OpenSealedRFQ(_ context.Context, id, userID string, at time.Time) (domain.RFQ, error) {
	f.rfqLog.openings = append(f.rfqLog.openings, userID)
	f.rfq.OpenedAt, f.rfq.OpenedBy = &at, userID
	return *f.rfq, nil
}

const rfqBuyer = "00000000-0000-7000-8000-0000000000b1"

// sealedHarness serves the RFQ fixture sealed, with a price and a delivery
// criterion, at the given clock.
func sealedHarness(t *testing.T, now time.Time) *harness {
	h := rfqHarness(t)
	h.fake.rfq.Sealed = true
	h.fake.rfq.Criteria = []domain.ScoringCriterion{
		{ID: "00000000-0000-7000-8000-00000000c101", Seq: 1, Name: "Quality", Weight: decimal.NewFromInt(2)},
		{ID: "00000000-0000-7000-8000-00000000c102", Seq: 2, Name: "Delivery", Weight: decimal.NewFromInt(1)},
	}
	h.api.now = func() time.Time { return now }
	return h
}

// AC-17.14: at 16:59Z the compare view is 403 PRC-RFQ-004 and the RFQ shows
// no reply prices; at 17:00:01Z the replies are visible and the opening is
// logged once, with the user.
func TestSealedRFQ_AC17_14(t *testing.T) {
	h := sealedHarness(t, rfqDeadline.Add(-time.Minute))
	compare := call{method: "GET", path: "/v1/procurement/rfqs/" + rfqID + "/compare", duties: rfqMaintainDuty, user: rfqBuyer}
	rec := h.do(compare)
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeRFQSealed {
		t.Fatalf("compare before the deadline: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/rfqs/" + rfqID, duties: rfqMaintainDuty, user: rfqBuyer})
	var early domain.RFQ
	if err := json.Unmarshal(rec.Body.Bytes(), &early); err != nil || rec.Code != 200 {
		t.Fatalf("get before the deadline: %d %s", rec.Code, rec.Body)
	}
	if !early.RepliesSealed || len(early.Replies) != 0 || early.ReplyCount != 4 || early.OpenedAt != nil {
		t.Fatalf("before the deadline: sealed %v, %d replies, count %d, opened %v", early.RepliesSealed, len(early.Replies), early.ReplyCount, early.OpenedAt)
	}
	if len(h.fake.rfqLog.openings) != 0 {
		t.Fatal("an opening was logged before the deadline")
	}

	h.api.now = func() time.Time { return rfqDeadline.Add(time.Second) }
	rec = h.do(compare)
	if rec.Code != 200 {
		t.Fatalf("compare after the deadline: %d %s", rec.Code, rec.Body)
	}
	var cmp rfqComparison
	_ = json.Unmarshal(rec.Body.Bytes(), &cmp)
	if len(cmp.Lines) != 2 || len(cmp.Lines[0].Ranked)+len(cmp.Lines[0].Excluded) != 3 {
		t.Fatalf("all three line 1 replies must be visible: %+v", cmp.Lines)
	}
	if got := h.fake.rfqLog.openings; len(got) != 1 || got[0] != rfqBuyer {
		t.Fatalf("openings logged = %v, want one by %s", got, rfqBuyer)
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/rfqs/" + rfqID, duties: rfqMaintainDuty, user: rfqBuyer})
	var after domain.RFQ
	_ = json.Unmarshal(rec.Body.Bytes(), &after)
	if after.RepliesSealed || len(after.Replies) != 4 || after.OpenedAt == nil || after.OpenedBy != rfqBuyer {
		t.Fatalf("after the deadline: %+v", after)
	}
	if len(h.fake.rfqLog.openings) != 1 {
		t.Fatalf("the opening is logged once, got %d", len(h.fake.rfqLog.openings))
	}
}

func TestSealedRFQ_AwardAndScoringWaitForTheDeadline(t *testing.T) {
	h := sealedHarness(t, rfqDeadline.Add(-time.Hour))
	award := `{"awards":[{"lineId":"` + rfqLine1 + `","replyId":"` + replyB1 + `"}]}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/rfqs/" + rfqID + ":award", duties: rfqAwardDuty, body: award, headers: rfqActionHeaders})
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeRFQSealed || h.fake.rfqLog.awardCalls != 0 {
		t.Fatalf("award before the deadline: %d %s (store calls %d)", rec.Code, rec.Body, h.fake.rfqLog.awardCalls)
	}
	score := `{"scores":[{"criterionId":"00000000-0000-7000-8000-00000000c101","score":8}]}`
	rec = h.do(call{method: "PUT", path: "/v1/procurement/rfqs/" + rfqID + "/scores/" + rfqVendorA, duties: rfqAwardDuty, body: score,
		headers: map[string]string{"If-Match": `"3"`}})
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeRFQSealed {
		t.Fatalf("scoring before the deadline: %d %s", rec.Code, rec.Body)
	}
}

func TestScoreRFQVendor(t *testing.T) {
	path := "/v1/procurement/rfqs/" + rfqID + "/scores/" + rfqVendorA
	good := `{"scores":[{"criterionId":"00000000-0000-7000-8000-00000000c101","score":8},{"criterionId":"00000000-0000-7000-8000-00000000c102","score":3}]}`
	v3 := map[string]string{"If-Match": `"3"`}
	tests := []struct {
		name, path, body string
		duties           []string
		headers          map[string]string
		status           int
		code             string
	}{
		{"RFQMaintain may not score", path, good, rfqMaintainDuty, v3, http.StatusForbidden, ""},
		{"no If-Match", path, good, rfqAwardDuty, nil, http.StatusPreconditionRequired, codeIfMatchMissing},
		{"stale If-Match", path, good, rfqAwardDuty, map[string]string{"If-Match": `"2"`}, http.StatusConflict, "PRC-VERSION-CONFLICT"},
		{"score 11", path, `{"scores":[{"criterionId":"00000000-0000-7000-8000-00000000c101","score":11}]}`, rfqAwardDuty, v3, 422, codeInvalid},
		{"no scores", path, `{"scores":[]}`, rfqAwardDuty, v3, 422, codeInvalid},
		{"criterion not a UUID", path, `{"scores":[{"criterionId":"price","score":5}]}`, rfqAwardDuty, v3, 422, codeInvalid},
		{"vendor not a UUID", "/v1/procurement/rfqs/" + rfqID + "/scores/acme", good, rfqAwardDuty, v3, 422, codeInvalid},
		{"criterion not on the RFQ", path, `{"scores":[{"criterionId":"00000000-0000-7000-8000-00000000c1ff","score":5}]}`, rfqAwardDuty, v3, 422, domain.CodeValidation},
		{"uninvited vendor", "/v1/procurement/rfqs/" + rfqID + "/scores/00000000-0000-7000-8000-00000000ff01", good, rfqAwardDuty, v3, 422, domain.CodeRFQNotInvited},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := sealedHarness(t, rfqDeadline.Add(time.Hour))
			rec := h.do(call{method: "PUT", path: tc.path, duties: tc.duties, body: tc.body, headers: tc.headers, user: rfqBuyer})
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.code != "" && problemCode(t, rec) != tc.code {
				t.Fatalf("code %s, want %s", problemCode(t, rec), tc.code)
			}
		})
	}

	// Scoring vendor A above vendor B puts A first under WEIGHTED_SCORE,
	// although B is cheaper on line 1.
	h := sealedHarness(t, rfqDeadline.Add(time.Hour))
	rec := h.do(call{method: "PUT", path: path, duties: rfqAwardDuty, body: good, headers: v3, user: rfqBuyer})
	if rec.Code != 200 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("score A: %d %s", rec.Code, rec.Body)
	}
	if h.fake.rfqLog.scoredBy != rfqVendorA || len(h.fake.rfqLog.scores) != 2 || h.fake.rfqLog.scores[0].VendorID != rfqVendorA {
		t.Fatalf("store got %s %+v", h.fake.rfqLog.scoredBy, h.fake.rfqLog.scores)
	}
	low := `{"scores":[{"criterionId":"00000000-0000-7000-8000-00000000c101","score":4}]}`
	if rec := h.do(call{method: "PUT", path: "/v1/procurement/rfqs/" + rfqID + "/scores/" + rfqVendorB, duties: rfqAwardDuty, body: low,
		headers: map[string]string{"If-Match": `"4"`}, user: rfqBuyer}); rec.Code != 200 {
		t.Fatalf("score B: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/rfqs/" + rfqID + "/compare?rule=WEIGHTED_SCORE", duties: rfqAwardDuty, user: rfqBuyer})
	var cmp rfqComparison
	_ = json.Unmarshal(rec.Body.Bytes(), &cmp)
	if rec.Code != 200 || cmp.RankingRule != domain.RankWeightedScore {
		t.Fatalf("compare: %d %s", rec.Code, rec.Body)
	}
	first := cmp.Lines[0].Ranked
	// A: (2×8 + 1×3) / 3 = 6.3333; B: (2×4) / 3 = 2.6667.
	if len(first) != 2 || first[0].VendorID != rfqVendorA || first[0].WeightedScore == nil || first[0].WeightedScore.String() != "6.3333" ||
		first[1].WeightedScore == nil || first[1].WeightedScore.String() != "2.6667" {
		t.Fatalf("weighted ranking of line 1 = %+v", first)
	}
}

func TestCompareRFQ_WeightedScoreNeedsCriteria(t *testing.T) {
	h := rfqHarness(t)
	rec := h.do(call{method: "GET", path: "/v1/procurement/rfqs/" + rfqID + "/compare?rule=WEIGHTED_SCORE", duties: rfqMaintainDuty})
	if rec.Code != 422 || problemCode(t, rec) != codeInvalid {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestCreateRFQ_SealedWithCriteria(t *testing.T) {
	h := newHarness(t)
	body := `{"legalEntityId":"00000000-0000-7000-8000-0000000001e1","replyDeadline":"2026-03-05T17:00:00Z","rankingRule":"WEIGHTED_SCORE",
		"sealed":true,"criteria":[{"name":" Quality ","weight":"2"},{"name":"Delivery","weight":"1.5"}],
		"lines":[{"description":"Pump","qty":"1","unit":"EA"}]}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/rfqs", duties: rfqMaintainDuty, body: body, headers: idem("k-sealed")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := h.fake.rfqLog.created
	if !got.Sealed || got.RankingRule != domain.RankWeightedScore || len(got.Criteria) != 2 || got.Criteria[0].Name != "Quality" ||
		!got.Criteria[1].Weight.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("created = %+v", got)
	}
	bad := `{"legalEntityId":"00000000-0000-7000-8000-0000000001e1","replyDeadline":"2026-03-05T17:00:00Z",
		"criteria":[{"name":"Quality","weight":"0"}],"lines":[{"description":"Pump","qty":"1","unit":"EA"}]}`
	h = newHarness(t)
	if rec := h.do(call{method: "POST", path: "/v1/procurement/rfqs", duties: rfqMaintainDuty, body: bad, headers: idem("k-bad")}); rec.Code != 422 || h.fake.rfqLog.created != nil {
		t.Fatalf("zero weight: %d %s", rec.Code, rec.Body)
	}
}
