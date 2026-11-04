package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
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
	CodeCountFrozen     = "INV-CNT-002" // stock is frozen by an open count sheet
	CodeCountRecount    = "INV-CNT-006" // a recount is outstanding on the sheet
	CodeCountFourEyes   = "INV-CNT-007" // the approver also counted a line of the sheet
	CodeCountNotReady   = "INV-CNT-008" // the sheet must be approved (or submitted) first
	CodeCountBusy       = "INV-CNT-009" // a sheet the movement must flag for recount is locked by another action; retry
)

const (
	// RecountBySupervisor and RecountByMovement name who flagged a line for recount.
	RecountBySupervisor = "SUPERVISOR"
	RecountByMovement   = "MOVEMENT"
	// MaxCountAgeDays bounds the last-count-age plan filter.
	MaxCountAgeDays = 3650
)

// Count sheet statuses. A sheet is OPEN while counters enter quantities;
// submitting it makes it COUNTED (entries stop), a supervisor APPROVES it, and
// posting is final. A sheet that is not POSTED can be CANCELLED, which lifts
// its freeze.
const (
	CountOpen      = "OPEN"
	CountCounted   = "COUNTED"
	CountApproved  = "APPROVED"
	CountPosted    = "POSTED"
	CountCancelled = "CANCELLED"
)

// CountSheetStatuses lists every count sheet status.
var CountSheetStatuses = []string{CountOpen, CountCounted, CountApproved, CountPosted, CountCancelled}

// ABC classes a plan can select.
var CountABCClasses = []string{"A", "B", "C"}

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
	// RecountRequested is set by a supervisor; the counter must enter the line
	// again before the sheet can be submitted or posted.
	RecountRequested bool
	RecountReason    string
	RecountRounds    int
	// RecountSource is who asked: RecountBySupervisor, or RecountByMovement when
	// stock of the row moved after the line was counted (BR-16.8).
	RecountSource string
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
	SubmittedBy   string
	JournalID     string
	FourEyes      bool // posting needs a separate approval by someone who did not count
	Freeze        bool // movements on the sheet's rows are refused while it is not final
	RecountLines  int  // lines with an outstanding recount
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
	FreezeStock    bool   // INV-CNT-002: refuse movements on counted rows while a sheet is open
	FourEyes       bool   // the approver of a sheet must not be one of its counters
	LocationFrom   string // inclusive location range; empty means no bound
	LocationTo     string
	ABCClass       string          // select products of this class in the latest ABC run; empty means all
	SamplePct      decimal.Decimal // zero means every selected row
	// MinDaysSinceCount selects only rows whose last posted count is at least this
	// many days old, or that were never counted; zero means no age filter.
	MinDaysSinceCount int
	// IncludeVendorOwned adds vendor-owned (consignment) rows to the sheets; their
	// variance posts quantity only, with no value and no GL entry (BR-16.14).
	IncludeVendorOwned bool
	CreatedAt          time.Time
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
	if p.LocationFrom != "" && p.LocationTo != "" && p.LocationFrom > p.LocationTo {
		return fail(CodeInvalid, "location range start is after its end")
	}
	if utf8.RuneCountInString(p.LocationFrom) > maxDimensionValue || utf8.RuneCountInString(p.LocationTo) > maxDimensionValue {
		return fail(CodeInvalid, "location bounds are longer than %d characters", maxDimensionValue)
	}
	if p.ABCClass != "" && !slices.Contains(CountABCClasses, p.ABCClass) {
		return fail(CodeInvalid, "ABC class must be A, B or C")
	}
	if p.MinDaysSinceCount < 0 || p.MinDaysSinceCount > MaxCountAgeDays {
		return fail(CodeInvalid, "days since last count must be between 1 and %d (empty means every row)", MaxCountAgeDays)
	}
	if p.SamplePct.IsNegative() || p.SamplePct.GreaterThan(decimal.NewFromInt(100)) || !p.SamplePct.Equal(p.SamplePct.Truncate(6)) {
		return fail(CodeInvalid, "sample percent must be between 0 and 100 with at most 6 decimals")
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

// RequireStatus rejects an action on a sheet whose status is not one of want.
func RequireStatus(status string, want ...string) error {
	if !slices.Contains(want, status) {
		return fail(CodeCountSheetState, "count sheet in status %s cannot be changed this way", status)
	}
	return nil
}

// CheckFourEyes enforces separation of duties on an approval: the approver may
// not be one of the people who entered a count on the sheet. An approver with
// no identity cannot be separated from anyone and is refused.
func CheckFourEyes(approver string, lines []CountLine) error {
	if approver == "" {
		return fail(CodeCountFourEyes, "the approver must be identified to approve a four-eyes count sheet")
	}
	for _, l := range lines {
		if l.CountedBy == approver {
			return fail(CodeCountFourEyes, "line %d was counted by the approver; another supervisor must approve this sheet", l.LineNo)
		}
	}
	return nil
}

// OutstandingRecounts counts the lines still flagged for recount.
func OutstandingRecounts(lines []CountLine) int {
	n := 0
	for _, l := range lines {
		if l.RecountRequested {
			n++
		}
	}
	return n
}

// SampleKeys picks the sample of a plan for one sheet: the ceil(pct percent)
// keys with the smallest SHA-256 of seed and key. The seed is the sheet id, so
// a sheet's sample can be recomputed for audit while successive sheets of the
// same plan count different rows instead of the same subset every time. pct
// of zero (or 100) keeps every key. The result is the set of kept keys.
func SampleKeys(seed string, keys []string, pct decimal.Decimal) map[string]bool {
	keep := map[string]bool{}
	if pct.IsZero() || pct.GreaterThanOrEqual(decimal.NewFromInt(100)) || len(keys) == 0 {
		for _, k := range keys {
			keep[k] = true
		}
		return keep
	}
	n := int(decimal.NewFromInt(int64(len(keys))).Mul(pct).Div(decimal.NewFromInt(100)).Ceil().IntPart())
	type ranked struct{ key, digest string }
	rs := make([]ranked, 0, len(keys))
	for _, k := range keys {
		sum := sha256.Sum256([]byte(seed + "|" + k))
		rs = append(rs, ranked{k, hex.EncodeToString(sum[:])})
	}
	slices.SortFunc(rs, func(a, b ranked) int { return strings.Compare(a.digest, b.digest) })
	for _, r := range rs[:min(n, len(rs))] {
		keep[r.key] = true
	}
	return keep
}
