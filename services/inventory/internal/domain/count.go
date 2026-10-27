package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// Count problem codes. INV-CNT-001 is from the message catalog (3.16.18); the
// others are service codes.
const (
	CodeCountApproval   = "INV-CNT-001" // variance above tolerance: approval required
	CodeCountSheetState = "INV-CNT-003" // sheet is not in a state that allows the action
	CodeCountNotFound   = "INV-CNT-004" // unknown count plan, sheet or line
	CodeCountNothing    = "INV-CNT-005" // a sheet is posted with no counted line
)

// Count sheet statuses. A sheet is OPEN until posted; posting is final.
const (
	CountOpen   = "OPEN"
	CountPosted = "POSTED"
)

// CountSheetStatuses lists every count sheet status.
var CountSheetStatuses = []string{CountOpen, CountPosted}

// Default plan tolerances (3.16.13: count_tolerance_qty_pct 5, count_tolerance_value 100.00).
var (
	DefaultTolerancePct   = decimal.NewFromInt(5)
	DefaultToleranceValue = decimal.NewFromInt(100)
)

// CountLine is one projection row to count: Expected is the book quantity when
// the sheet was generated; Counted is what the counter entered (nil until
// counted); PostedVariance is counted minus the book quantity at posting.
type CountLine struct {
	ID             string
	LineNo         int
	ProductID      string
	Dims           Dimensions
	Expected       decimal.Decimal
	Counted        *decimal.Decimal
	CountedBy      string
	CountedAt      *time.Time
	PostedVariance *decimal.Decimal
}

// CountSheet is a generated count of one plan's stock.
type CountSheet struct {
	ID            string
	LegalEntityID string
	PlanID        string
	PlanName      string
	Number        string
	Status        string
	ApprovedBy    string
	JournalID     string
	Blind         bool // expected quantities are hidden from counters while OPEN
	Version       int
	LineCount     int
	CountedCount  int
	CreatedAt     time.Time
	PostedAt      *time.Time
	Lines         []CountLine
}

// CountPlan generates count sheets for one legal entity, optionally one
// warehouse (FR-16.9). Tolerances are percent of the book quantity and an
// absolute value in the plan currency.
type CountPlan struct {
	ID             string
	LegalEntityID  string
	Name           string
	Warehouse      string // warehouse dimension value; empty means every warehouse
	Currency       string
	Unit           string
	TolerancePct   decimal.Decimal
	ToleranceValue decimal.Decimal
	Blind          bool
	CreatedAt      time.Time
}

// ValidatePlan checks a count plan definition.
func ValidatePlan(p CountPlan) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(p.Name)); n == 0 || n > 120 {
		return fail(CodeInvalid, "count plan needs a name of 1 to 120 characters")
	}
	if utf8.RuneCountInString(p.Warehouse) > maxDimensionValue {
		return fail(CodeInvalid, "warehouse is longer than %d characters", maxDimensionValue)
	}
	if len(p.Currency) != 3 {
		return fail(CodeInvalid, "currency must be an ISO 4217 code")
	}
	if p.Unit == "" {
		return fail(CodeInvalid, "count plan needs an inventory unit")
	}
	if p.TolerancePct.IsNegative() || p.TolerancePct.GreaterThan(decimal.NewFromInt(100)) || !p.TolerancePct.Equal(p.TolerancePct.Truncate(6)) {
		return fail(CodeInvalid, "tolerance percent must be between 0 and 100 with at most 6 decimals")
	}
	if p.ToleranceValue.IsNegative() || !p.ToleranceValue.Equal(p.ToleranceValue.Truncate(6)) {
		return fail(CodeInvalid, "tolerance value must be zero or more with at most 6 decimals")
	}
	return nil
}

// ValidateCounted checks a counted quantity: never negative, within the unit precision.
func ValidateCounted(q decimal.Decimal, maxScale int32) error {
	if q.IsNegative() || !q.Equal(q.Truncate(maxScale)) {
		return fail(CodeInvalid, "counted quantity must be zero or more with at most %d decimals", maxScale)
	}
	return nil
}

// Variance is the quantity the count adds to (or removes from) the book quantity.
func Variance(book, counted decimal.Decimal) decimal.Decimal {
	return counted.Sub(book)
}

// ExceedsTolerance reports whether a variance needs supervisor approval (FR-16.9):
// the percentage of the book quantity or the absolute value at unitCost is
// strictly above the plan tolerance. A variance against zero book quantity is
// 100 percent.
func ExceedsTolerance(variance, book, unitCost, tolPct, tolValue decimal.Decimal) bool {
	if variance.IsZero() {
		return false
	}
	pct := decimal.NewFromInt(100)
	if !book.IsZero() {
		pct = variance.Abs().Div(book.Abs()).Mul(decimal.NewFromInt(100))
	}
	value := variance.Abs().Mul(unitCost.Abs())
	return pct.GreaterThan(tolPct) || value.GreaterThan(tolValue)
}

// RequireOpen rejects an action on a sheet that is no longer OPEN.
func RequireOpen(status string) error {
	if status != CountOpen {
		return fail(CodeCountSheetState, "count sheet in status %s cannot be changed", status)
	}
	return nil
}
