package domain

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Credit limit types (credit_profile.limit_type).
const (
	LimitNone               = "NONE"
	LimitBalance            = "BALANCE"
	LimitBalancePackingSlip = "BALANCE_PACKING_SLIP"
	LimitBalanceAll         = "BALANCE_ALL"
)

// Rule outcomes. FAIL places a CREDIT hold; WARN is shown only (BR-27.3).
const (
	RulePass = "PASS"
	RuleWarn = "WARN"
	RuleFail = "FAIL"
)

const youngCustomerDays = 90

// youngCustomerThreshold is the CR-6 order amount limit per currency. Currencies
// without an entry do not apply CR-6 until FX rules are wired in (wave 12).
var youngCustomerThreshold = map[string]decimal.Decimal{
	"USD": decimal.NewFromInt(50000),
	"EUR": decimal.NewFromInt(45000),
}

// CreditProfile is the per-customer, per-entity credit configuration (FR-27.7).
type CreditProfile struct {
	Limit             decimal.Decimal `json:"limit"`
	Currency          string          `json:"currency"`
	LimitType         string          `json:"limitType"`
	TolerancePct      decimal.Decimal `json:"tolerancePct"`
	ToleranceAmount   decimal.Decimal `json:"toleranceAmount"`
	OldestItemDaysMax int             `json:"oldestItemDaysMax"`
	MaxDunningLevel   int             `json:"maxDunningLevel"`
	ReviewDate        *time.Time      `json:"reviewDate,omitempty"`
	CustomerSince     *time.Time      `json:"customerSince,omitempty"`
	MandatoryCheck    bool            `json:"mandatoryCheck"`
}

// Exposure is the customer's open position in the profile currency. Until the
// receivables projection exists (waves 29/30) the AR figures are supplied with
// the profile; DeliveredNotInvoiced and OpenOrders (with their other-currency
// lists) are computed by sales from its orders and never stored.
// Every amount is non-negative; Unapplied is cash received but not yet applied.
type Exposure struct {
	OpenAR               decimal.Decimal `json:"openAr"`
	Unapplied            decimal.Decimal `json:"unapplied"`
	DeliveredNotInvoiced decimal.Decimal `json:"deliveredNotInvoiced"`
	OpenOrders           decimal.Decimal `json:"openOrders"`
	// OpenOrdersOtherCurrencies are the customer's open orders in currencies
	// other than the profile's, which OpenOrders leaves out for want of an FX
	// rate (w12). Computed by sales like OpenOrders, never stored.
	OpenOrdersOtherCurrencies []CurrencyAmount `json:"openOrdersOtherCurrencies,omitempty"`
	// DeliveredNotInvoicedOtherCurrencies is the delivered, not yet invoiced
	// value of orders in other currencies, left out of DeliveredNotInvoiced for
	// want of an FX rate. Computed by sales, never stored.
	DeliveredNotInvoicedOtherCurrencies []CurrencyAmount `json:"deliveredNotInvoicedOtherCurrencies,omitempty"`
	OverdueAR                           decimal.Decimal  `json:"overdueAr"`
	OldestItemDays                      int              `json:"oldestItemDays"`
	DunningLevel                        int              `json:"dunningLevel"`
}

// CurrencyAmount is an amount in a named currency.
type CurrencyAmount struct {
	Currency string          `json:"currency"`
	Amount   decimal.Decimal `json:"amount"`
}

// CreditInput is everything one credit check needs. Profile is nil when the
// customer has no profile for the entity. Order is the order amount in
// OrderCurrency.
type CreditInput struct {
	Profile         *CreditProfile
	EntityMandatory bool
	Exposure        Exposure
	Order           decimal.Decimal
	OrderCurrency   string
	AsOf            time.Time
}

// RuleResult is one evaluated rule with the value compared and its threshold.
// Note explains a result that is not a plain comparison.
type RuleResult struct {
	ID        string `json:"id"`
	Result    string `json:"result"`
	Value     string `json:"value"`
	Threshold string `json:"threshold"`
	Note      string `json:"note,omitempty"`
}

// CreditEvaluation lists every rule that applied and the overall outcome.
type CreditEvaluation struct {
	Rules   []RuleResult `json:"rules"`
	Outcome string       `json:"outcome"`
}

// dateOf truncates an instant to its UTC date.
func dateOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// EvaluateCredit applies rules CR-1..CR-6 of BR-27.3 and records every rule it
// evaluated. Any FAIL makes the outcome FAIL, otherwise any WARN makes it WARN.
//
// BR-27.3 converts the order amount to the profile currency at the wave 12
// spot rate. Until that rate source is wired, an order in another currency
// than the profile fails CR-1 with a note instead of being compared across
// currencies, so a credit manager reviews it.
func EvaluateCredit(in CreditInput) CreditEvaluation {
	rules := []RuleResult{}
	add := func(id, result string, value, threshold decimal.Decimal) {
		rules = append(rules, RuleResult{ID: id, Result: result, Value: value.String(), Threshold: threshold.String()})
	}
	p := in.Profile
	mandatory := in.EntityMandatory
	if p != nil {
		mandatory = p.MandatoryCheck
	}
	reviewPassed := p != nil && p.ReviewDate != nil && dateOf(*p.ReviewDate).Before(dateOf(in.AsOf))
	if mandatory && (p == nil || reviewPassed) {
		add("CR-4", RuleFail, decimal.Zero, decimal.Zero)
	} else {
		add("CR-4", RulePass, decimal.Zero, decimal.Zero)
	}
	if p == nil {
		return finish(rules)
	}
	e := in.Exposure
	foreign := in.OrderCurrency != "" && in.OrderCurrency != p.Currency
	if p.LimitType != LimitNone && foreign {
		rules = append(rules, RuleResult{ID: "CR-1", Result: RuleFail, Value: in.Order.String() + " " + in.OrderCurrency,
			Threshold: p.Limit.String() + " " + p.Currency, Note: "order currency differs from the credit profile currency; no FX rate is available to convert it"})
	} else if p.LimitType != LimitNone {
		exposure := CountedBalance(*p, e).Add(in.Order)
		threshold := LimitThreshold(*p)
		result := RulePass
		switch {
		case exposure.GreaterThan(threshold):
			result = RuleFail
		case exposure.GreaterThan(p.Limit):
			result = RuleWarn
		}
		r := RuleResult{ID: "CR-1", Result: result, Value: exposure.String(), Threshold: threshold.String()}
		// The exposure is understated by amounts that cannot be converted; a
		// pass is shown as a warning so the gap is not silent.
		var gaps []string
		if (p.LimitType == LimitBalancePackingSlip || p.LimitType == LimitBalanceAll) && len(e.DeliveredNotInvoicedOtherCurrencies) > 0 {
			gaps = append(gaps, "delivered, not invoiced goods not counted for want of an FX rate:"+currencyList(e.DeliveredNotInvoicedOtherCurrencies))
		}
		if p.LimitType == LimitBalanceAll && len(e.OpenOrdersOtherCurrencies) > 0 {
			gaps = append(gaps, "open orders not counted for want of an FX rate:"+currencyList(e.OpenOrdersOtherCurrencies))
		}
		if len(gaps) > 0 {
			if result == RulePass {
				r.Result = RuleWarn
			}
			r.Note = strings.Join(gaps, "; ")
		}
		rules = append(rules, r)
	}
	if e.OldestItemDays > p.OldestItemDaysMax {
		add("CR-2", RuleFail, decimal.NewFromInt(int64(e.OldestItemDays)), decimal.NewFromInt(int64(p.OldestItemDaysMax)))
	} else {
		add("CR-2", RulePass, decimal.NewFromInt(int64(e.OldestItemDays)), decimal.NewFromInt(int64(p.OldestItemDaysMax)))
	}
	if e.DunningLevel >= p.MaxDunningLevel {
		add("CR-3", RuleFail, decimal.NewFromInt(int64(e.DunningLevel)), decimal.NewFromInt(int64(p.MaxDunningLevel)))
	} else {
		add("CR-3", RulePass, decimal.NewFromInt(int64(e.DunningLevel)), decimal.NewFromInt(int64(p.MaxDunningLevel)))
	}
	quarter := e.OpenAR.Mul(decimal.New(25, -2))
	if e.OverdueAR.IsPositive() && e.OverdueAR.GreaterThan(quarter) {
		add("CR-5", RuleWarn, e.OverdueAR, quarter)
	} else {
		add("CR-5", RulePass, e.OverdueAR, quarter)
	}
	orderCurrency := in.OrderCurrency
	if orderCurrency == "" {
		orderCurrency = p.Currency
	}
	if p.CustomerSince != nil && in.AsOf.Sub(*p.CustomerSince) < youngCustomerDays*24*time.Hour {
		if limit, ok := youngCustomerThreshold[orderCurrency]; ok {
			result := RulePass
			if in.Order.GreaterThan(limit) {
				result = RuleFail
			}
			add("CR-6", result, in.Order, limit)
		}
	}
	return finish(rules)
}

func finish(rules []RuleResult) CreditEvaluation {
	outcome := RulePass
	for _, r := range rules {
		if r.Result == RuleFail {
			return CreditEvaluation{Rules: rules, Outcome: RuleFail}
		}
		if r.Result == RuleWarn {
			outcome = RuleWarn
		}
	}
	return CreditEvaluation{Rules: rules, Outcome: outcome}
}

func currencyList(amounts []CurrencyAmount) string {
	out := ""
	for _, o := range amounts {
		out += " " + o.Amount.String() + " " + o.Currency
	}
	return out
}

// CountedBalance is the balance CR-1 compares with the limit before an order is
// added: open AR less unapplied cash (a credit the customer already paid, so it
// lowers the balance), plus delivered-not-invoiced goods for the packing-slip
// and all-in limit types and open orders for the all-in type.
func CountedBalance(p CreditProfile, e Exposure) decimal.Decimal {
	base := e.OpenAR.Sub(e.Unapplied)
	if p.LimitType == LimitBalancePackingSlip || p.LimitType == LimitBalanceAll {
		base = base.Add(e.DeliveredNotInvoiced)
	}
	if p.LimitType == LimitBalanceAll {
		base = base.Add(e.OpenOrders)
	}
	return base
}

// LimitThreshold is the balance above which CR-1 fails: the limit with the
// larger of the percentage and the amount tolerance.
func LimitThreshold(p CreditProfile) decimal.Decimal {
	byPct := p.Limit.Mul(decimal.NewFromInt(100).Add(p.TolerancePct)).Div(decimal.NewFromInt(100))
	byAmount := p.Limit.Add(p.ToleranceAmount)
	return decimal.Max(byPct, byAmount)
}

// Utilization statuses of the credit exposure report.
const (
	UtilNoLimit       = "NO_LIMIT"
	UtilWithin        = "WITHIN"
	UtilOverLimit     = "OVER_LIMIT"
	UtilOverTolerance = "OVER_TOLERANCE"
)

// Utilization is how much of a customer's credit limit the exposure uses.
type Utilization struct {
	// Balance is CountedBalance; Threshold the limit with tolerance.
	Balance   decimal.Decimal
	Threshold decimal.Decimal
	// Pct is Balance as a percent of the limit to two places; nil when the
	// profile has no limit (type NONE or a limit of 0).
	Pct *decimal.Decimal
	// Status is NO_LIMIT, WITHIN (at or under the limit), OVER_LIMIT (above the
	// limit, within the tolerance: CR-1 warns) or OVER_TOLERANCE (CR-1 fails).
	Status string
	// Understated is true when amounts in other currencies that the limit type
	// counts were left out for want of an FX rate, so Balance is a floor.
	Understated bool
}

// Utilize measures a customer's exposure against the profile's limit.
func Utilize(p CreditProfile, e Exposure) Utilization {
	u := Utilization{Balance: CountedBalance(p, e), Threshold: LimitThreshold(p), Status: UtilNoLimit}
	counted := p.LimitType == LimitBalancePackingSlip || p.LimitType == LimitBalanceAll
	u.Understated = (counted && len(e.DeliveredNotInvoicedOtherCurrencies) > 0) ||
		(p.LimitType == LimitBalanceAll && len(e.OpenOrdersOtherCurrencies) > 0)
	if p.LimitType == LimitNone || !p.Limit.IsPositive() {
		return u
	}
	pct := u.Balance.Mul(decimal.NewFromInt(100)).Div(p.Limit).RoundBank(2)
	u.Pct = &pct
	switch {
	case u.Balance.GreaterThan(u.Threshold):
		u.Status = UtilOverTolerance
	case u.Balance.GreaterThan(p.Limit):
		u.Status = UtilOverLimit
	default:
		u.Status = UtilWithin
	}
	return u
}
