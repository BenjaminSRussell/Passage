package domain

import (
	"fmt"
	"regexp"

	"github.com/shopspring/decimal"
)

// Message-catalog codes for trade funds and promotion planning (§5.26.18).
const (
	CodeFundClosed     = "PRC-0011"
	CodeFundOverBudget = "PRC-0012"
	CodeFundNotFound   = "PRC-0039"
	CodePromoNotFound  = "PRC-0040"
	// CodeFundAmount is the shared validation code: a usage amount must be positive.
	CodeFundAmount = CodeValidation
)

// Usage sources a fund is charged with (FR-26.19: campaign accruals, rebate accruals, claims, deductions).
const (
	UsageCampaignAccrual = "CAMPAIGN_ACCRUAL"
	UsageRebateAccrual   = "REBATE_ACCRUAL"
	UsageClaim           = "CLAIM"
	UsageDeduction       = "DEDUCTION"
)

var (
	periodPattern   = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
	fundCurrencyPat = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Fund is a trade promotion fund: a budget per period that usage is charged against (BR-26.15).
// Version increments on every revise, close and reopen (optimistic concurrency).
type Fund struct {
	ID              string
	LegalEntityID   string
	Name            string
	Type            string
	Currency        string
	CustomerScope   string
	Owner           string
	BudgetPerPeriod decimal.Decimal
	AllowOverspend  bool
	Closed          bool
	Version         int
}

// RuleError is a fund or promotion rule violation with its message-catalog code and HTTP status.
type RuleError struct {
	Code    string
	Status  int
	Message string
}

func (e *RuleError) Error() string { return e.Code + ": " + e.Message }

// ValidPeriod reports whether p is a fiscal period key in YYYY-MM form.
func ValidPeriod(p string) bool { return periodPattern.MatchString(p) }

// ValidUsageSource reports whether s is one of the fund usage sources.
func ValidUsageSource(s string) bool {
	switch s {
	case UsageCampaignAccrual, UsageRebateAccrual, UsageClaim, UsageDeduction:
		return true
	}
	return false
}

// ValidateFund returns field errors for a fund definition; an empty map means valid.
func ValidateFund(f Fund) map[string]string {
	fields := map[string]string{}
	if len(f.Name) < 1 || len(f.Name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if len(f.Type) < 1 || len(f.Type) > 32 {
		fields["type"] = "must be 1 to 32 characters"
	}
	if !fundCurrencyPat.MatchString(f.Currency) {
		fields["currency"] = "must be three upper-case letters"
	}
	if len(f.Owner) < 1 || len(f.Owner) > 64 {
		fields["owner"] = "must be 1 to 64 characters"
	}
	if len(f.CustomerScope) > 64 {
		fields["customerScope"] = "must be at most 64 characters"
	}
	if f.BudgetPerPeriod.IsNegative() {
		fields["budgetPerPeriod"] = "must not be negative"
	}
	return fields
}

func overBudget(f Fund, excess decimal.Decimal) error {
	return &RuleError{Code: CodeFundOverBudget, Status: 422,
		Message: fmt.Sprintf("fund %s budget exceeded by %s", f.Name, excess.StringFixed(2))}
}

func fundClosed(f Fund) error {
	return &RuleError{Code: CodeFundClosed, Status: 409, Message: fmt.Sprintf("fund %s is closed", f.Name)}
}

// CheckAttribution applies BR-26.15 to one usage attribution of amount against the
// usage already recorded in the period. A closed fund rejects everything (PRC-0011);
// an overspend beyond budget is rejected unless the fund allows it (PRC-0012).
func CheckAttribution(f Fund, usedInPeriod, amount decimal.Decimal) error {
	if !amount.IsPositive() {
		return &RuleError{Code: CodeFundAmount, Status: 422, Message: "attribution amount must be greater than zero"}
	}
	if f.Closed {
		return fundClosed(f)
	}
	after := usedInPeriod.Add(amount)
	if !f.AllowOverspend && after.GreaterThan(f.BudgetPerPeriod) {
		return overBudget(f, after.Sub(f.BudgetPerPeriod))
	}
	return nil
}

// CheckRevision applies BR-26.15 to a budget change. A closed fund is frozen (PRC-0011);
// without overspend the new budget must still cover the busiest period's usage (PRC-0012),
// so lowering a budget can never leave usage above it.
func CheckRevision(f Fund, budget decimal.Decimal, allowOverspend bool, maxPeriodUsage decimal.Decimal) error {
	if f.Closed {
		return fundClosed(f)
	}
	if budget.IsNegative() {
		return &RuleError{Code: CodeValidation, Status: 422, Message: "budget per period must not be negative"}
	}
	if !allowOverspend && maxPeriodUsage.GreaterThan(budget) {
		return overBudget(f, maxPeriodUsage.Sub(budget))
	}
	return nil
}

// CheckPromotionFund reports why a promotion may not draw on fund f: the fund must belong
// to the same legal entity and currency (field errors) and must be open (PRC-0011).
func CheckPromotionFund(f Fund, p PromotionPlan) (map[string]string, error) {
	fields := map[string]string{}
	if f.LegalEntityID != p.LegalEntityID {
		fields["fundId"] = "must belong to the promotion's legal entity"
	} else if f.Currency != p.Currency {
		fields["currency"] = "must match the fund currency " + f.Currency
	}
	if len(fields) > 0 {
		return fields, nil
	}
	if f.Closed {
		return nil, fundClosed(f)
	}
	return nil, nil
}

// Remaining is the budget left in a period. It is negative only when overspend is allowed.
func Remaining(f Fund, usedInPeriod decimal.Decimal) decimal.Decimal {
	return f.BudgetPerPeriod.Sub(usedInPeriod)
}
