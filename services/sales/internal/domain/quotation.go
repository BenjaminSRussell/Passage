package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Quotation states (BR-27.5). A quotation is CONVERTED once any line has been
// converted to an order; its remaining OPEN lines may still convert while the
// quotation is valid (AC-27.6 converts lines 1 and 3 of three).
const (
	QuoteDraft     = "DRAFT"
	QuoteSent      = "SENT"
	QuoteConverted = "CONVERTED"
	QuoteLost      = "LOST"
	QuoteExpired   = "EXPIRED"
	QuoteCancelled = "CANCELLED"

	// Quotation line states.
	QuoteLineOpen      = "OPEN"
	QuoteLineConverted = "CONVERTED"

	// QuotationExpiryDays is the default validity of a quotation
	// (quotation_expiry_days, FR-27.1).
	QuotationExpiryDays = 30
	// MaxQuotationExpiryDays is the upper bound of quotation_expiry_days.
	MaxQuotationExpiryDays = 365
	// DefaultProbabilityPct is the default win probability
	// (quotation_probability_default).
	DefaultProbabilityPct = 50
)

var (
	// ErrQuotationState is returned when the quotation's status does not allow
	// the action.
	ErrQuotationState = errors.New("quotation status does not allow this action")
	// ErrQuotationExpired is returned when the quotation's validity has passed.
	ErrQuotationExpired = errors.New("quotation has expired")
	// ErrLineSelection is returned when requested line numbers are not open
	// lines of the quotation.
	ErrLineSelection = errors.New("requested lines are not open on this quotation")
)

// IsQuotationStatus reports whether s is a quotation status (BR-27.5).
func IsQuotationStatus(s string) bool {
	switch s {
	case QuoteDraft, QuoteSent, QuoteConverted, QuoteLost, QuoteExpired, QuoteCancelled:
		return true
	}
	return false
}

// expired reports whether a quotation valid through validUntil has expired on
// day today. Days are compared as UTC calendar dates, so a quotation is valid
// through its validUntil day.
func expired(validUntil, today time.Time) bool { return DayOf(today).After(DayOf(validUntil)) }

// CheckSend returns nil when a quotation may move to SENT on day today: it is
// a DRAFT and its validity has not passed.
func CheckSend(status string, validUntil, today time.Time) error {
	if status != QuoteDraft {
		return ErrQuotationState
	}
	if expired(validUntil, today) {
		return ErrQuotationExpired
	}
	return nil
}

// CheckLose returns nil when a quotation may be marked LOST: it is a DRAFT or
// SENT. A partly converted quotation is CONVERTED and cannot be lost.
func CheckLose(status string) error {
	if status != QuoteDraft && status != QuoteSent {
		return ErrQuotationState
	}
	return nil
}

// CheckCancel returns nil when a quotation may be CANCELLED: it is a DRAFT or
// SENT. A partly converted quotation is CONVERTED and keeps its orders.
func CheckCancel(status string) error {
	if status != QuoteDraft && status != QuoteSent {
		return ErrQuotationState
	}
	return nil
}

// CheckChange returns nil when a quotation may be changed on day today: a DRAFT
// or SENT quotation whose validity has not passed may take a new validity or
// probability; only a DRAFT may take new lines (a sent offer's prices are what
// the customer holds).
func CheckChange(status string, validUntil, today time.Time, changesLines bool) error {
	switch {
	case status == QuoteDraft:
	case status == QuoteSent && !changesLines:
	default:
		return ErrQuotationState
	}
	if expired(validUntil, today) {
		return ErrQuotationExpired
	}
	return nil
}

// CheckConvertible returns nil when a quotation may convert lines on day today:
// it was sent (or already partly converted) and is still valid.
func CheckConvertible(status string, validUntil, today time.Time) error {
	if status != QuoteSent && status != QuoteConverted {
		return ErrQuotationState
	}
	if expired(validUntil, today) {
		return ErrQuotationExpired
	}
	return nil
}

// QuotationValidUntil returns the validity date of a quotation issued on day
// issued, days days later (FR-27.1 default +30 days).
func QuotationValidUntil(issued time.Time, days int) time.Time {
	return DayOf(issued).AddDate(0, 0, days)
}

// DayOf truncates t to midnight UTC of its calendar day.
func DayOf(t time.Time) time.Time { return dateOf(t) }

// ValidateProbability requires a whole percentage from 0 to 100.
func ValidateProbability(p int) error {
	if p < 0 || p > 100 {
		return errors.New("must be from 0 to 100")
	}
	return nil
}

// SelectLines returns the line numbers to convert. An empty request selects all
// open lines. Otherwise every requested number must be an open line and appear
// once. The result is sorted ascending.
func SelectLines(openLines, requested []int) ([]int, error) {
	open := make(map[int]bool, len(openLines))
	for _, n := range openLines {
		open[n] = true
	}
	if len(requested) == 0 {
		if len(openLines) == 0 {
			return nil, fmt.Errorf("%w: no open lines remain", ErrLineSelection)
		}
		out := append([]int(nil), openLines...)
		sort.Ints(out)
		return out, nil
	}
	seen := make(map[int]bool, len(requested))
	out := make([]int, 0, len(requested))
	for _, n := range requested {
		if !open[n] || seen[n] {
			return nil, fmt.Errorf("%w: line %d", ErrLineSelection, n)
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

// SumNets adds line nets for a quotation or order total.
func SumNets(nets []decimal.Decimal) decimal.Decimal {
	total := decimal.Zero
	for _, n := range nets {
		total = total.Add(n)
	}
	return total
}

// FormatQuotationNumber renders the SQ- document number for sequence value seq.
func FormatQuotationNumber(seq int64) string { return fmt.Sprintf("SQ-%07d", seq) }
