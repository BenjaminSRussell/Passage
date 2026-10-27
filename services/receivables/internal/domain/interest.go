package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Interest rules (FR-30.8, BR-30.5). Pure functions, no I/O.
const (
	BasisAct365 = "ACT_365"
	BasisAct360 = "ACT_360"

	CodeInterestRate = "AR-INTEREST-RATE"
)

// ValidBasis reports whether b is a supported day-count basis.
func ValidBasis(b string) bool { return b == BasisAct365 || b == BasisAct360 }

// InterestAmount is open × annualRate × days / basis, rounded to the minor unit.
func InterestAmount(open, annualRate decimal.Decimal, days int, basis string) decimal.Decimal {
	divisor := int64(365)
	if basis == BasisAct360 {
		divisor = 360
	}
	return open.Mul(annualRate).Mul(decimal.NewFromInt(int64(days))).Div(decimal.NewFromInt(divisor)).Round(2)
}

// InterestCandidate is one item charged interest for a period.
type InterestCandidate struct {
	OpenItemID     string          `json:"openItemId"`
	CustomerID     string          `json:"customerId"`
	DocumentNumber string          `json:"documentNumber"`
	Currency       string          `json:"currency"`
	Principal      decimal.Decimal `json:"principal"`
	Days           int             `json:"days"`
	Amount         decimal.Decimal `json:"amount"`
}

// PlanInterest computes interest for overdue items as of asOf. Interest runs
// from due date + grace (or from the item's last interest calculation, so no
// day is charged twice) to asOf; disputed and promised items are excluded, and
// amounts below the 5.00 minimum are excluded as well. Interest and fee items
// never accrue interest.
func PlanInterest(asOf time.Time, graceDays int, annualRate decimal.Decimal, basis string, items []DunnableItem) ([]InterestCandidate, []Exclusion) {
	minimum := decimal.NewFromInt(5)
	var out []InterestCandidate
	var excluded []Exclusion
	for _, it := range items {
		if it.SourceType == "INTEREST" || it.SourceType == "FEE" {
			continue
		}
		from := it.DueDate.AddDate(0, 0, graceDays)
		if it.InterestedTo != nil && it.InterestedTo.After(from) {
			from = *it.InterestedTo
		}
		days := daysBetween(from, asOf)
		if days <= 0 {
			continue
		}
		if it.HasDispute {
			excluded = append(excluded, Exclusion{OpenItemID: it.ID, CustomerID: it.CustomerID, Reason: ReasonDispute})
			continue
		}
		if it.PromiseDate != nil && PromiseHolds(*it.PromiseDate, asOf) {
			excluded = append(excluded, Exclusion{OpenItemID: it.ID, CustomerID: it.CustomerID, Reason: ReasonPromise})
			continue
		}
		amount := InterestAmount(it.OpenAmount, annualRate, days, basis)
		if amount.LessThan(minimum) {
			excluded = append(excluded, Exclusion{OpenItemID: it.ID, CustomerID: it.CustomerID, Reason: ReasonBelowMinimum})
			continue
		}
		out = append(out, InterestCandidate{OpenItemID: it.ID, CustomerID: it.CustomerID, DocumentNumber: it.DocumentNumber,
			Currency: it.Currency, Principal: it.OpenAmount, Days: days, Amount: amount})
	}
	return out, excluded
}
