package domain

import (
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/shopspring/decimal"
)

// Data-quality rule types (FR-46.16).
const (
	RuleCompleteness = "COMPLETENESS"
	RuleValidity     = "VALIDITY"
	RuleUniqueness   = "UNIQUENESS"
	RuleReferential  = "REFERENTIAL"
	RuleFreshness    = "FRESHNESS"

	SeverityLow    = "LOW"
	SeverityMedium = "MEDIUM"
	SeverityHigh   = "HIGH"

	RuleActive  = "ACTIVE"
	RuleRetired = "RETIRED"

	// CodeDQBelow is the ANA-0109 warning raised when a rule scores below threshold.
	CodeDQBelow = "ANA-0109"
	// DefaultDQThresholdPct is the default pass-rate threshold (fail below 98%).
	DefaultDQThresholdPct = 98
	// DQEscalateAfter is the consecutive below-threshold count that escalates to the steward (BR-46.15).
	DQEscalateAfter = 3
	// DQMuteDays is how long a muted rule stays silent (scorecard action "mute rule for 7 days").
	DQMuteDays = 7

	maxDQExpression = 2048
)

var (
	ruleIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)
	assetPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$`)
)

// DQRule is one data-quality rule on a catalog asset (dq_rule). Version is the
// optimistic-concurrency version the API exposes as the ETag.
type DQRule struct {
	RuleID       string          `json:"ruleId"`
	AssetFQN     string          `json:"assetFqn"`
	Type         string          `json:"ruleType"`
	Expression   string          `json:"expression"`
	ThresholdPct decimal.Decimal `json:"thresholdPct"`
	Severity     string          `json:"severity"`
	OwnerUserID  string          `json:"ownerUserId"`
	Status       string          `json:"status"`
	MuteUntil    *time.Time      `json:"muteUntil,omitempty"`
	Version      int             `json:"version"`
}

// Validate checks the rule definition. Validity expressions must compile as regular expressions.
func (r DQRule) Validate() error {
	fields := map[string]string{}
	if !ruleIDPattern.MatchString(r.RuleID) {
		fields["ruleId"] = "lowercase letters, digits and underscores, 2-63 characters"
	}
	if !assetPattern.MatchString(r.AssetFQN) {
		fields["assetFqn"] = "dotted lowercase asset name"
	}
	if !IsRuleType(r.Type) {
		fields["ruleType"] = "COMPLETENESS, VALIDITY, UNIQUENESS, REFERENTIAL or FRESHNESS"
	}
	if r.Expression == "" || len(r.Expression) > maxDQExpression {
		fields["expression"] = "required, at most 2048 bytes"
	} else if r.Type == RuleValidity {
		if _, err := regexp.Compile(r.Expression); err != nil {
			fields["expression"] = "validity expression must be a valid regular expression"
		}
	}
	if r.ThresholdPct.IsNegative() || r.ThresholdPct.GreaterThan(decimal.NewFromInt(100)) || r.ThresholdPct.Exponent() < -2 {
		fields["thresholdPct"] = "must be between 0 and 100 with at most two decimals"
	}
	if !slices.Contains([]string{SeverityLow, SeverityMedium, SeverityHigh}, r.Severity) {
		fields["severity"] = "LOW, MEDIUM or HIGH"
	}
	if !ids.Valid(r.OwnerUserID) {
		fields["ownerUserId"] = "must be a UUID"
	}
	if r.Status != RuleActive && r.Status != RuleRetired {
		fields["status"] = "ACTIVE or RETIRED"
	}
	if len(fields) > 0 {
		return validation(fields)
	}
	return nil
}

// IsRuleID reports whether id follows the rule id rule.
func IsRuleID(id string) bool { return ruleIDPattern.MatchString(id) }

// IsRuleType reports whether t is one of the five rule types.
func IsRuleType(t string) bool {
	return slices.Contains([]string{RuleCompleteness, RuleValidity, RuleUniqueness, RuleReferential, RuleFreshness}, t)
}

// IsAssetFQN reports whether fqn is a dotted lowercase catalog asset name.
func IsAssetFQN(fqn string) bool { return assetPattern.MatchString(fqn) }

// DefaultThreshold is the default pass-rate threshold as a decimal.
func DefaultThreshold() decimal.Decimal { return decimal.NewFromInt(DefaultDQThresholdPct) }

// PassPercent is passed/total as a percentage, rounded half-even to two places.
// An empty population passes: nothing can fail.
func PassPercent(total, failed int64) (decimal.Decimal, error) {
	if total < 0 || failed < 0 || failed > total {
		return decimal.Zero, errors.New("dq: failed rows must be between 0 and the population")
	}
	if total == 0 {
		return decimal.NewFromInt(100), nil
	}
	passed := decimal.NewFromInt(total - failed)
	return passed.Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(total)).RoundBank(2), nil
}

// Score is one evaluation of one rule on one asset for one day (dq_score).
// BelowStreak counts consecutive below-threshold evaluations (BR-46.15).
type Score struct {
	AssetFQN    string          `json:"assetFqn"`
	RuleID      string          `json:"ruleId"`
	Date        string          `json:"date"`
	PassPct     decimal.Decimal `json:"passPct"`
	FailedRows  int64           `json:"failedRows"`
	TotalRows   int64           `json:"totalRows"`
	Below       bool            `json:"below"`
	BelowStreak int             `json:"belowStreak"`
}

// Evaluate scores a rule against counts from the asset. Below means the exact
// pass rate is strictly under the rule threshold. It is decided before rounding,
// so 97.995% is below a 98% threshold even though it displays as 98.00%.
func Evaluate(r DQRule, date string, total, failed int64) (Score, error) {
	pass, err := PassPercent(total, failed)
	if err != nil {
		return Score{}, err
	}
	below := false
	if total > 0 {
		passed100 := decimal.NewFromInt(total - failed).Mul(decimal.NewFromInt(100))
		below = passed100.LessThan(r.ThresholdPct.Mul(decimal.NewFromInt(total)))
	}
	return Score{
		AssetFQN: r.AssetFQN, RuleID: r.RuleID, Date: date,
		PassPct: pass, FailedRows: failed, TotalRows: total, Below: below,
	}, nil
}

// FreshnessPasses reports whether the newest ingested row is within maxAge of now.
func FreshnessPasses(lastIngested, now time.Time, maxAge time.Duration) bool {
	return now.Sub(lastIngested) <= maxAge
}

// NextBelowStreak counts consecutive below-threshold evaluations. A passing
// evaluation resets the count.
func NextBelowStreak(previous int, below bool) int {
	if !below {
		return 0
	}
	return previous + 1
}

// Routing says who hears about one evaluation.
type Routing struct {
	OwnerTask         bool
	StewardEscalation bool
}

// Notified is what earlier evaluations already sent: the owner's task for this
// day, and the steward escalation for the current below-threshold run.
type Notified struct {
	OwnerToday      bool
	StewardInStreak bool
}

// Route decides notifications (FR-46.16, BR-46.15). A muted rule still scores
// but never notifies. A below-threshold rule creates one owner task per rule
// per day, however often it is evaluated that day; the third consecutive
// failure escalates to the catalog steward once per below-threshold run.
func Route(below bool, streak int, muted bool, sent Notified) Routing {
	if !below || muted {
		return Routing{}
	}
	return Routing{
		OwnerTask:         !sent.OwnerToday,
		StewardEscalation: streak >= DQEscalateAfter && !sent.StewardInStreak,
	}
}

// TaskKey is the idempotency key for the owner's daily task: once per rule per day.
func TaskKey(ruleID, date string) string {
	return "dq-task:" + ruleID + ":" + date
}

// MuteUntil returns the end of a mute window that starts at now.
func MuteUntil(now time.Time) time.Time { return now.AddDate(0, 0, DQMuteDays) }

// IsMuted reports whether a mute window is still open at now. A rule without a
// mute window is not muted.
func IsMuted(until *time.Time, now time.Time) bool { return until != nil && until.After(now) }
