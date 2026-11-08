package domain

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// BaselineWeeks is the history window a promotion baseline is averaged over (FR-26.20).
const BaselineWeeks = 13

// ErrNoBaseline means effectiveness cannot be measured against a zero or missing baseline.
var ErrNoBaseline = errors.New("promotion baseline must be greater than zero to measure effectiveness")

// PromotionPlan plans a promotion on one product (FR-26.20; AX TAMMerchandisingEvent,
// TAMPromotionPlanning). BaselineWeekly holds the units sold in each of the prior 13
// weeks, oldest first. EventCategory and EventType classify the merchandising event;
// CustomerScope names the customer or customer group it targets (empty means all).
type PromotionPlan struct {
	ID            string
	LegalEntityID string
	Name          string
	EventCategory string
	EventType     string
	CustomerScope string
	FundID        string
	// CampaignID links the plan to a campaign of the same legal entity and currency; empty means none.
	CampaignID     string
	ProductID      string
	Currency       string
	Start          time.Time
	End            time.Time
	BaselineWeekly []decimal.Decimal
	LiftPct        decimal.Decimal
	PlannedSpend   decimal.Decimal
	Actuals        *PromotionActuals
}

// PromotionActuals are the units and spend posted inside the promotion window.
type PromotionActuals struct {
	Units decimal.Decimal
	Weeks int
	Spend decimal.Decimal
}

// Baseline is the average weekly units over the 13-week history, rounded to 6 places.
func Baseline(weekly []decimal.Decimal) (decimal.Decimal, error) {
	if len(weekly) != BaselineWeeks {
		return decimal.Zero, errors.New("baseline needs exactly 13 weekly values")
	}
	sum := decimal.Zero
	for _, w := range weekly {
		if w.IsNegative() {
			return decimal.Zero, errors.New("baseline weekly units must not be negative")
		}
		sum = sum.Add(w)
	}
	return sum.Div(decimal.NewFromInt(BaselineWeeks)).Round(6), nil
}

// PlanWeeks is the number of started weeks in the promotion window, inclusive of both ends.
func PlanWeeks(start, end time.Time) int {
	days := int(end.Sub(start).Hours()/24) + 1
	if days < 1 {
		return 0
	}
	return (days + 6) / 7
}

// ForecastUnits is the planned unit volume: baseline × weeks × (1 + lift%), rounded to 6 places.
func ForecastUnits(baseline, liftPct decimal.Decimal, weeks int) decimal.Decimal {
	factor := decimal.NewFromInt(1).Add(liftPct.Div(decimal.NewFromInt(100)))
	return baseline.Mul(decimal.NewFromInt(int64(weeks))).Mul(factor).Round(6)
}

// Effectiveness is (actual weekly average − baseline) / baseline in percent, rounded to 4 places.
func Effectiveness(baseline decimal.Decimal, a PromotionActuals) (decimal.Decimal, error) {
	if !baseline.IsPositive() {
		return decimal.Zero, ErrNoBaseline
	}
	if a.Weeks < 1 {
		return decimal.Zero, errors.New("actuals must cover at least one week")
	}
	avg := a.Units.Div(decimal.NewFromInt(int64(a.Weeks)))
	return avg.Sub(baseline).Div(baseline).Mul(decimal.NewFromInt(100)).Round(4), nil
}

// ValidatePromotion returns field errors for a promotion plan; an empty map means valid.
func ValidatePromotion(p PromotionPlan) map[string]string {
	fields := map[string]string{}
	if len(p.Name) < 1 || len(p.Name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	for field, v := range map[string]string{"eventCategory": p.EventCategory, "eventType": p.EventType, "customerScope": p.CustomerScope} {
		if len(v) > 64 {
			fields[field] = "must be at most 64 characters"
		}
	}
	if len(p.ProductID) < 1 || len(p.ProductID) > 64 {
		fields["productId"] = "must be 1 to 64 characters"
	}
	if !fundCurrencyPat.MatchString(p.Currency) {
		fields["currency"] = "must be three upper-case letters"
	}
	if p.End.Before(p.Start) {
		fields["endDate"] = "must not be before startDate"
	}
	if _, err := Baseline(p.BaselineWeekly); err != nil {
		fields["baselineWeeklyUnits"] = err.Error()
	}
	if p.LiftPct.LessThan(decimal.NewFromInt(-100)) {
		fields["liftPct"] = "must be at least -100"
	}
	if p.PlannedSpend.IsNegative() {
		fields["plannedSpend"] = "must not be negative"
	}
	return fields
}

// CodePromoCampaignKilled: a promotion plan cannot be linked to a campaign that is killed.
const CodePromoCampaignKilled = "PRC-0069"

// CheckPromotionCampaign reports why a promotion may not belong to campaign c: the campaign
// must be in the promotion's legal entity and currency and the promotion window must lie
// inside the campaign's validity (field errors), and the campaign must not be killed
// (PRC-0069). Later revisions of the campaign are not re-checked against existing plans.
func CheckPromotionCampaign(c Campaign, p PromotionPlan) (map[string]string, error) {
	fields := map[string]string{}
	if c.LegalEntityID != p.LegalEntityID {
		fields["campaignId"] = "must belong to the promotion's legal entity"
	} else if c.Currency != p.Currency {
		fields["currency"] = "must match the campaign currency " + c.Currency
	} else {
		if p.Start.Before(c.ValidFrom) {
			fields["startDate"] = "must not be before the campaign starts on " + c.ValidFrom.Format("2006-01-02")
		}
		if p.End.After(c.ValidTo) {
			fields["endDate"] = "must not be after the campaign ends on " + c.ValidTo.Format("2006-01-02")
		}
	}
	if len(fields) > 0 {
		return fields, nil
	}
	if c.Killed {
		return nil, &RuleError{Code: CodePromoCampaignKilled, Status: 409, Message: "campaign " + c.Name + " is killed"}
	}
	return nil, nil
}
