package domain

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Scoring limits (FR-17.19): at most 20 criteria per RFQ, weights up to
// numeric(9,4), scores whole numbers on a 1–10 scale.
const (
	MaxScoringCriteria = 20
	maxCriterionName   = 100
	MinScore           = 1
	MaxScore           = 10
)

var maxWeight = decimal.RequireFromString("99999.9999")

// ScoringCriterion is one weighted criterion of an RFQ's scoring group.
type ScoringCriterion struct {
	ID     string          `json:"id"`
	Seq    int             `json:"seq"`
	Name   string          `json:"name"`
	Weight decimal.Decimal `json:"weight"`
}

// VendorScore is the buyer's 1–10 score of one invited vendor's reply on one
// criterion. A vendor replies to an RFQ as a whole, so the score covers every
// line the vendor quoted.
type VendorScore struct {
	VendorID    string `json:"vendorId"`
	CriterionID string `json:"criterionId"`
	Score       int    `json:"score"`
}

// ValidateCriteria checks the scoring group an RFQ is created with: at most
// 20 criteria, each with a unique name of up to 100 characters and a positive
// weight of at most 4 decimals. A WEIGHTED_SCORE RFQ needs at least one.
func ValidateCriteria(rule string, in []ScoringCriterion) error {
	if len(in) > MaxScoringCriteria {
		return fail(CodeValidation, "an RFQ may have at most %d scoring criteria", MaxScoringCriteria)
	}
	if rule == RankWeightedScore && len(in) == 0 {
		return fail(CodeValidation, "ranking by %s needs at least one scoring criterion", RankWeightedScore)
	}
	seen := make(map[string]bool, len(in))
	for i, c := range in {
		name := strings.TrimSpace(c.Name)
		if name == "" || len(name) > maxCriterionName {
			return fail(CodeValidation, "criterion %d needs a name of 1 to %d characters", i+1, maxCriterionName)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fail(CodeValidation, "criterion %q appears more than once", name)
		}
		seen[key] = true
		if !c.Weight.IsPositive() || c.Weight.GreaterThan(maxWeight) || !c.Weight.Equal(c.Weight.Round(4)) {
			return fail(CodeValidation, "criterion %q needs a positive weight up to %s with at most 4 decimals", name, maxWeight)
		}
	}
	return nil
}

// SealedAt reports whether a sealed RFQ's replies are still sealed at now:
// they open at the reply deadline (BR-17.16).
func SealedAt(r RFQ, now time.Time) bool { return r.Sealed && now.Before(r.ReplyDeadline) }

// CheckSealedView enforces BR-17.16: the replies, scores and ranking of a
// sealed RFQ cannot be read, compared, scored or awarded before the reply
// deadline (PRC-RFQ-004). The two-person early opening is not built.
func CheckSealedView(r RFQ, now time.Time) error {
	if SealedAt(r, now) {
		return fail(CodeRFQSealed, "sealed replies for RFQ %s cannot be viewed before %s",
			r.Number, r.ReplyDeadline.UTC().Format(time.RFC3339))
	}
	return nil
}

// CheckSealedReply rejects, rather than flags, a late reply to a sealed RFQ (BR-17.16).
func CheckSealedReply(r RFQ, late bool) error {
	if r.Sealed && late {
		return fail(CodeRFQSealedLate, "RFQ %s is sealed; a reply after the deadline %s is rejected",
			r.Number, r.ReplyDeadline.UTC().Format(time.RFC3339))
	}
	return nil
}

// Redact hides what a sealed RFQ must not show at now: before the deadline
// the replies and scores are dropped and RepliesSealed is set. ReplyCount
// always counts the replies, so the buyer sees that quotes have arrived.
func Redact(r RFQ, now time.Time) RFQ {
	r.ReplyCount = len(r.Replies)
	r.RepliesSealed = SealedAt(r, now)
	if r.RepliesSealed {
		r.Replies, r.Scores = []RFQReply{}, []VendorScore{}
	}
	return r
}

// CheckScoring validates the buyer's scores of one invited vendor (FR-17.19):
// the RFQ must be SENT, REPLIES_OPEN or CLOSED and, when sealed, past its
// deadline; it must have criteria; every score names one of them once and
// lies on 1–10.
func CheckScoring(r RFQ, vendorID string, scores []VendorScore, now time.Time) error {
	if r.Status != RFQSent && r.Status != RFQRepliesOpen && r.Status != RFQClosed {
		return fail(CodeRFQState, "an RFQ in %s cannot be scored", r.Status)
	}
	if err := CheckSealedView(r, now); err != nil {
		return err
	}
	invitedVendor := false
	for _, inv := range r.Invitations {
		invitedVendor = invitedVendor || inv.VendorID == vendorID
	}
	if !invitedVendor {
		return fail(CodeRFQNotInvited, "vendor %s was not invited to RFQ %s", vendorID, r.Number)
	}
	if len(r.Criteria) == 0 {
		return fail(CodeValidation, "RFQ %s has no scoring criteria", r.Number)
	}
	if len(scores) == 0 {
		return fail(CodeValidation, "at least one score is required")
	}
	known := make(map[string]bool, len(r.Criteria))
	for _, c := range r.Criteria {
		known[c.ID] = true
	}
	seen := make(map[string]bool, len(scores))
	for _, s := range scores {
		if !known[s.CriterionID] {
			return fail(CodeValidation, "criterion %s is not on RFQ %s", s.CriterionID, r.Number)
		}
		if seen[s.CriterionID] {
			return fail(CodeValidation, "criterion %s is scored twice", s.CriterionID)
		}
		seen[s.CriterionID] = true
		if s.Score < MinScore || s.Score > MaxScore {
			return fail(CodeValidation, "the score for criterion %s must be from %d to %d", s.CriterionID, MinScore, MaxScore)
		}
	}
	return nil
}

// WeightedScores returns each scored vendor's weighted score:
// Σ(weight × score) / Σ(weight) over all criteria, rounded to 4 decimals. A
// criterion the vendor was not scored on counts as 0; a vendor with no score
// at all is absent from the map.
func WeightedScores(criteria []ScoringCriterion, scores []VendorScore) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	total := decimal.Zero
	weight := make(map[string]decimal.Decimal, len(criteria))
	for _, c := range criteria {
		total = total.Add(c.Weight)
		weight[c.ID] = c.Weight
	}
	if !total.IsPositive() {
		return out
	}
	sum := map[string]decimal.Decimal{}
	for _, s := range scores {
		w, ok := weight[s.CriterionID]
		if !ok {
			continue
		}
		sum[s.VendorID] = sum[s.VendorID].Add(w.Mul(decimal.NewFromInt(int64(s.Score))))
	}
	for v, s := range sum {
		out[v] = s.DivRound(total, 4)
	}
	return out
}
