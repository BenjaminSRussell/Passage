package domain

import (
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// RFQ statuses (FR-17.4).
const (
	RFQDraft       = "DRAFT"
	RFQSent        = "SENT"
	RFQRepliesOpen = "REPLIES_OPEN"
	RFQClosed      = "CLOSED"
	RFQAwarded     = "AWARDED"
	RFQCancelled   = "CANCELLED"
)

// RFQ actions, the custom methods on /rfqs/{id}.
const (
	RFQActionSend   = "send"
	RFQActionClose  = "close"
	RFQActionCancel = "cancel"
	RFQActionAward  = "award"
)

// RFQActions lists every RFQ custom method.
var RFQActions = []string{RFQActionSend, RFQActionClose, RFQActionCancel, RFQActionAward}

// Ranking rules for the compare view (rfq_default_ranking, FR-17.4).
// WEIGHTED_SCORE ranks by the buyer's weighted vendor scores over the RFQ's
// scoring criteria (FR-17.19).
const (
	RankLowestPrice   = "LOWEST_PRICE"
	RankLeadTime      = "LEAD_TIME"
	RankWeightedScore = "WEIGHTED_SCORE"
)

// Stable problem codes raised by RFQ rules (listed in the service README).
const (
	// CodeRFQState: the RFQ's status does not allow the action or a reply.
	CodeRFQState = "PRC-RFQ-STATE"
	// CodeRFQNotInvited: a reply names a vendor the RFQ was not sent to.
	CodeRFQNotInvited = "PRC-RFQ-002"
	// CodeRFQLate is the PRD warning for a reply received after the deadline
	// (BR-17.11); awarding such a reply without an override reason is refused
	// with the same code.
	CodeRFQLate = "PRC-RFQ-003"
	// CodeRFQAward: the award breaks an AwardRFQ rule (line, reply, validity).
	CodeRFQAward = "PRC-RFQ-005"
	// CodeRFQLateRevision: a reply after the deadline cannot replace the
	// vendor's on-time reply to the same line.
	CodeRFQLateRevision = "PRC-RFQ-006"
	// CodeRFQSealed is PRC-RFQ-004 (BR-17.16): a sealed RFQ's replies cannot
	// be read, scored, compared or awarded before the reply deadline.
	CodeRFQSealed = "PRC-RFQ-004"
	// CodeRFQSealedLate: a late reply to a sealed RFQ is rejected, not flagged (BR-17.16).
	CodeRFQSealedLate = "PRC-RFQ-007"
)

// ReasonLate is the exclusion reason of a reply left out of the ranking (BR-17.11).
const ReasonLate = "LATE"

// rfqNext is the RFQ state machine: action -> from status -> to status.
var rfqNext = map[string]map[string]string{
	RFQActionSend:   {RFQDraft: RFQSent},
	RFQActionClose:  {RFQSent: RFQClosed, RFQRepliesOpen: RFQClosed},
	RFQActionCancel: {RFQDraft: RFQCancelled, RFQSent: RFQCancelled, RFQRepliesOpen: RFQCancelled, RFQClosed: RFQCancelled},
	RFQActionAward:  {RFQRepliesOpen: RFQAwarded, RFQClosed: RFQAwarded},
}

// RFQLine is one requested item; a line names a product, a description or both.
type RFQLine struct {
	ID           string          `json:"id"`
	LineNo       int             `json:"lineNo"`
	ProductID    string          `json:"productId,omitempty"`
	Description  string          `json:"description,omitempty"`
	Qty          decimal.Decimal `json:"qty"`
	Unit         string          `json:"unit"`
	RequiredDate *Day            `json:"requiredDate,omitempty"`
}

// RFQInvitation records that a vendor was asked to reply.
type RFQInvitation struct {
	VendorID string    `json:"vendorId"`
	Status   string    `json:"status"`
	SentAt   time.Time `json:"sentAt"`
}

// RFQReply is one vendor's price for one line. Late is set when the reply
// arrived after the deadline (BR-17.11); Awarded when the reply won the line,
// with OverrideReason when a late reply was awarded.
type RFQReply struct {
	ID             string          `json:"id"`
	LineID         string          `json:"lineId"`
	VendorID       string          `json:"vendorId"`
	UnitPrice      decimal.Decimal `json:"unitPrice"`
	PriceUnit      decimal.Decimal `json:"priceUnit"`
	Currency       string          `json:"currency"`
	LeadTimeDays   int             `json:"leadTimeDays"`
	ValidUntil     *Day            `json:"validUntil,omitempty"`
	Notes          string          `json:"notes,omitempty"`
	ReceivedAt     time.Time       `json:"receivedAt"`
	Late           bool            `json:"late"`
	Awarded        bool            `json:"awarded"`
	OverrideReason string          `json:"overrideReason,omitempty"`
}

// RFQ is the request for quotation aggregate. Version is the ETag value.
// A Sealed RFQ hides its replies and scores until the reply deadline
// (BR-17.16): RepliesSealed is then set, Replies and Scores are empty and
// ReplyCount still counts the replies. OpenedAt and OpenedBy log the first
// time the sealed replies were shown after the deadline.
type RFQ struct {
	ID            string             `json:"id"`
	Number        string             `json:"number"`
	LegalEntityID string             `json:"legalEntityId"`
	Status        string             `json:"status"`
	ReplyDeadline time.Time          `json:"replyDeadline"`
	RankingRule   string             `json:"rankingRule"`
	Sealed        bool               `json:"sealed"`
	RepliesSealed bool               `json:"repliesSealed"`
	ReplyCount    int                `json:"replyCount"`
	OpenedAt      *time.Time         `json:"openedAt,omitempty"`
	OpenedBy      string             `json:"openedBy,omitempty"`
	Version       int                `json:"version"`
	Lines         []RFQLine          `json:"lines"`
	Invitations   []RFQInvitation    `json:"invitations"`
	Replies       []RFQReply         `json:"replies"`
	Criteria      []ScoringCriterion `json:"criteria"`
	Scores        []VendorScore      `json:"scores"`
	CreatedAt     time.Time          `json:"createdAt"`
	UpdatedAt     time.Time          `json:"updatedAt"`
}

// Award gives one RFQ line to one reply. OverrideReason is required to award a
// late reply (BR-17.11).
type Award struct {
	LineID         string `json:"lineId"`
	ReplyID        string `json:"replyId"`
	OverrideReason string `json:"overrideReason,omitempty"`
}

// RankedReply is one candidate in the compare view. UnitPrice is per unit of
// measure (price divided by price unit); Total is unit price × line quantity.
// WeightedScore is the vendor's weighted score when the RFQ has scoring
// criteria and the vendor was scored (FR-17.19).
type RankedReply struct {
	Rank          int              `json:"rank"`
	ReplyID       string           `json:"replyId"`
	VendorID      string           `json:"vendorId"`
	UnitPrice     decimal.Decimal  `json:"unitPrice"`
	Currency      string           `json:"currency"`
	LeadTimeDays  int              `json:"leadTimeDays"`
	ValidUntil    *Day             `json:"validUntil,omitempty"`
	Total         decimal.Decimal  `json:"total"`
	WeightedScore *decimal.Decimal `json:"weightedScore,omitempty"`
}

// ExcludedReply is a reply kept out of the ranking, with the reason.
type ExcludedReply struct {
	ReplyID  string `json:"replyId"`
	VendorID string `json:"vendorId"`
	Reason   string `json:"reason"`
}

// NextRFQStatus applies an action to a status, or refuses it (state machine).
func NextRFQStatus(status, action string) (string, error) {
	to, ok := rfqNext[action][status]
	if !ok {
		return "", fail(CodeRFQState, "an RFQ in %s cannot %s", status, action)
	}
	return to, nil
}

// CanRecordReply reports whether vendors may still reply in this status.
func CanRecordReply(status string) error {
	if status == RFQSent || status == RFQRepliesOpen {
		return nil
	}
	return fail(CodeRFQState, "replies are accepted only while the RFQ is SENT or REPLIES_OPEN, not %s", status)
}

// IsLate reports whether a reply received at receivedAt misses the deadline.
// A reply at the deadline instant is on time.
func IsLate(deadline, receivedAt time.Time) bool { return receivedAt.After(deadline) }

// CheckRevision refuses a late reply that would overwrite the vendor's on-time
// reply to the same line: the on-time price stays the one that ranks. A late
// reply may replace an earlier late one.
func CheckRevision(existing *RFQReply, late bool) error {
	if existing != nil && !existing.Late && late {
		return fail(CodeRFQLateRevision, "vendor %s already replied to this line before the deadline; a late revision cannot replace it", existing.VendorID)
	}
	return nil
}

// ValidRankingRule checks a ranking rule name.
func ValidRankingRule(rule string) error {
	switch rule {
	case RankLowestPrice, RankLeadTime, RankWeightedScore:
		return nil
	}
	return fail(CodeValidation, "ranking rule must be %s, %s or %s", RankLowestPrice, RankLeadTime, RankWeightedScore)
}

// RankLine ranks the replies to one line for a quantity (FR-17.4) without
// vendor scores; see RankLineScored.
func RankLine(qty decimal.Decimal, replies []RFQReply, rule string) ([]RankedReply, []ExcludedReply, error) {
	return RankLineScored(qty, replies, rule, nil)
}

// RankLineScored ranks the replies to one line for a quantity (FR-17.4). Late
// replies are excluded (BR-17.11). The order is deterministic: LOWEST_PRICE
// sorts by unit price then lead time; LEAD_TIME by lead time then unit price;
// WEIGHTED_SCORE by the vendor's weighted score, highest first, an unscored
// vendor after every scored one, then unit price and lead time (FR-17.19).
// Vendor id and reply id break remaining ties. scores holds the weighted score
// of each scored vendor (WeightedScores).
func RankLineScored(qty decimal.Decimal, replies []RFQReply, rule string, scores map[string]decimal.Decimal) ([]RankedReply, []ExcludedReply, error) {
	if err := ValidRankingRule(rule); err != nil {
		return nil, nil, err
	}
	type cand struct {
		reply  RFQReply
		unit   decimal.Decimal
		score  decimal.Decimal
		scored bool
	}
	var kept []cand
	excluded := []ExcludedReply{}
	for _, r := range replies {
		if r.Late {
			excluded = append(excluded, ExcludedReply{ReplyID: r.ID, VendorID: r.VendorID, Reason: ReasonLate})
			continue
		}
		score, scored := scores[r.VendorID]
		kept = append(kept, cand{reply: r, unit: PerUnit(r), score: score, scored: scored})
	}
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if rule == RankLeadTime && a.reply.LeadTimeDays != b.reply.LeadTimeDays {
			return a.reply.LeadTimeDays < b.reply.LeadTimeDays
		}
		if rule == RankWeightedScore {
			if a.scored != b.scored {
				return a.scored
			}
			if !a.score.Equal(b.score) {
				return a.score.GreaterThan(b.score)
			}
		}
		if !a.unit.Equal(b.unit) {
			return a.unit.LessThan(b.unit)
		}
		if a.reply.LeadTimeDays != b.reply.LeadTimeDays {
			return a.reply.LeadTimeDays < b.reply.LeadTimeDays
		}
		if a.reply.VendorID != b.reply.VendorID {
			return a.reply.VendorID < b.reply.VendorID
		}
		return a.reply.ID < b.reply.ID
	})
	ranked := make([]RankedReply, 0, len(kept))
	for i, c := range kept {
		rr := RankedReply{
			Rank: i + 1, ReplyID: c.reply.ID, VendorID: c.reply.VendorID, UnitPrice: c.unit,
			Currency: c.reply.Currency, LeadTimeDays: c.reply.LeadTimeDays, ValidUntil: c.reply.ValidUntil,
			Total: c.unit.Mul(qty).Round(4),
		}
		if c.scored {
			score := c.score
			rr.WeightedScore = &score
		}
		ranked = append(ranked, rr)
	}
	return ranked, excluded, nil
}

// PerUnit converts a reply price to one unit of measure.
func PerUnit(r RFQReply) decimal.Decimal {
	if r.PriceUnit.Sign() <= 0 {
		return r.UnitPrice
	}
	return r.UnitPrice.Div(r.PriceUnit)
}

// CheckAward validates awards against the RFQ (FR-17.4 AwardRFQ, BR-17.11):
// the RFQ must be awardable, each line is awarded at most once, the reply must
// answer that line and still be valid on today, and a late reply needs an
// override reason. The awarded quantity is the RFQ line quantity, so it never
// exceeds it.
func CheckAward(r RFQ, awards []Award, today Day) error {
	if _, err := NextRFQStatus(r.Status, RFQActionAward); err != nil {
		return err
	}
	if len(awards) == 0 {
		return fail(CodeRFQAward, "at least one award is required")
	}
	lines := make(map[string]int, len(r.Lines))
	for _, l := range r.Lines {
		lines[l.ID] = l.LineNo
	}
	replies := make(map[string]RFQReply, len(r.Replies))
	for _, rp := range r.Replies {
		replies[rp.ID] = rp
	}
	seen := make(map[string]bool, len(awards))
	for _, a := range awards {
		lineNo, ok := lines[a.LineID]
		if !ok {
			return fail(CodeRFQAward, "line %s is not on this RFQ", a.LineID)
		}
		if seen[a.LineID] {
			return fail(CodeRFQAward, "line %d is awarded twice", lineNo)
		}
		seen[a.LineID] = true
		rp, ok := replies[a.ReplyID]
		if !ok || rp.LineID != a.LineID {
			return fail(CodeRFQAward, "reply %s does not answer line %d", a.ReplyID, lineNo)
		}
		if rp.ValidUntil != nil && rp.ValidUntil.Before(today.Time) {
			return fail(CodeRFQAward, "the reply from vendor %s to line %d expired on %s", rp.VendorID, lineNo, rp.ValidUntil)
		}
		if rp.Late && strings.TrimSpace(a.OverrideReason) == "" {
			return fail(CodeRFQLate, "the reply from vendor %s to line %d arrived after the deadline; give an override reason to award it", rp.VendorID, lineNo)
		}
	}
	return nil
}
