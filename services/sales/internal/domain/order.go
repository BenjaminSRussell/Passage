package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Order and hold vocabulary (BR-27.4, BR-27.5).
const (
	OrderDraft     = "DRAFT"
	OrderOpen      = "OPEN"
	OrderConfirmed = "CONFIRMED"

	// OrderTypeJournal orders never reserve or consume ATP (BR-27.7).
	OrderTypeJournal = "JOURNAL"

	HoldCredit      = "CREDIT"
	HoldFraud       = "FRAUD"
	HoldManual      = "MANUAL"
	HoldPriceReview = "PRICE_REVIEW"

	HoldActive   = "ACTIVE"
	HoldReleased = "RELEASED"

	// CreditReleaseDays is how long a CREDIT release stays valid (BR-27.4).
	CreditReleaseDays = 7
	quantityScale     = 6
	lineScale         = 4
)

// CanConfirm reports whether an order in status may be confirmed.
func CanConfirm(status string) bool { return status == OrderDraft || status == OrderOpen }

// BlocksConfirm reports whether an active hold of holdType stops confirmation.
// CREDIT and FRAUD block confirmation and release; MANUAL and PRICE_REVIEW block
// release only (BR-27.4).
func BlocksConfirm(holdType string) bool { return holdType == HoldCredit || holdType == HoldFraud }

// ValidateQuantity requires a positive quantity with at most six decimal places.
func ValidateQuantity(q decimal.Decimal) error {
	if !q.IsPositive() {
		return errors.New("quantity must be greater than 0")
	}
	if !q.Round(quantityScale).Equal(q) {
		return errors.New("quantity allows at most 6 decimal places")
	}
	return nil
}

// LineNet returns quantity × unit price less the percentage discount, rounded
// once to four places with banker's rounding.
func LineNet(qty, price, discPct decimal.Decimal) (decimal.Decimal, error) {
	if err := ValidateQuantity(qty); err != nil {
		return decimal.Zero, err
	}
	if price.IsNegative() {
		return decimal.Zero, errors.New("unit price must not be negative")
	}
	if discPct.IsNegative() || discPct.GreaterThan(decimal.NewFromInt(100)) {
		return decimal.Zero, errors.New("discount must be between 0 and 100 percent")
	}
	hundred := decimal.NewFromInt(100)
	net := qty.Mul(price).Mul(hundred.Sub(discPct)).Div(hundred)
	return net.RoundBank(lineScale), nil
}

// ReleaseCovers reports whether a CREDIT release given for heldAmount still
// covers current: an increase above 5% re-triggers CR-1 (BR-27.4).
func ReleaseCovers(heldAmount, current decimal.Decimal) bool {
	// An increase of more than 5% re-triggers CR-1.
	limit := heldAmount.Mul(decimal.New(105, -2))
	return current.LessThanOrEqual(limit)
}

// ReleaseValidUntil is the end of a CREDIT release's validity window.
func ReleaseValidUntil(releasedAt time.Time) time.Time {
	return releasedAt.Add(CreditReleaseDays * 24 * time.Hour)
}

// FormatOrderNumber renders the SO- document number for sequence value seq.
func FormatOrderNumber(seq int64) string { return fmt.Sprintf("SO-%07d", seq) }

// RequestHash fingerprints an intake payload so replays can be told apart from
// reuse of a key with a different body (BR-27.10).
func RequestHash(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
