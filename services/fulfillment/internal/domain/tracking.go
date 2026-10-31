package domain

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/shopspring/decimal"
)

// Errors of the tracking-dimension rules (BR-28.10).
var (
	// ErrTrackingRequired means a tracked line has fewer batch or serial units
	// than it ships (FUL-0006).
	ErrTrackingRequired = errors.New("FUL-0006: batch or serial required")
	// ErrTrackingQty means the entries of a line cover more than was picked
	// (FUL-0035).
	ErrTrackingQty = errors.New("FUL-0035: tracked quantity exceeds picked quantity")
	// ErrTrackingEntry means one entry is malformed (FUL-0050).
	ErrTrackingEntry = errors.New("FUL-0050: invalid tracking entry")
)

// TrackingEntry is one batch or serial taken from one location for a line. A
// serial entry is always one unit.
type TrackingEntry struct {
	BatchNo      string
	SerialNo     string
	LocationCode string
	Qty          decimal.Decimal
}

var trackingCode = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]{0,63}$`)

// ValidateTracking checks the entries of one line: each names a batch or a
// serial (or both), codes are well formed, a serial is one unit and appears once
// on the line, and the same batch and location pair is not listed twice.
func ValidateTracking(entries []TrackingEntry) error {
	serials := map[string]bool{}
	pairs := map[string]bool{}
	for i, e := range entries {
		if e.BatchNo == "" && e.SerialNo == "" {
			return fmt.Errorf("%w: entry %d needs a batch or a serial number", ErrTrackingEntry, i)
		}
		for _, c := range []string{e.BatchNo, e.SerialNo} {
			if c != "" && !trackingCode.MatchString(c) {
				return fmt.Errorf("%w: entry %d code %q is not valid", ErrTrackingEntry, i, c)
			}
		}
		if e.LocationCode != "" && !trackingCode.MatchString(e.LocationCode) {
			return fmt.Errorf("%w: entry %d location %q is not valid", ErrTrackingEntry, i, e.LocationCode)
		}
		if !e.Qty.IsPositive() {
			return fmt.Errorf("%w: entry %d quantity must be positive", ErrTrackingEntry, i)
		}
		if e.SerialNo != "" {
			if !e.Qty.Equal(decimal.NewFromInt(1)) {
				return fmt.Errorf("%w: entry %d serial %s is one unit", ErrTrackingEntry, i, e.SerialNo)
			}
			if serials[e.SerialNo] {
				return fmt.Errorf("%w: serial %s is listed twice", ErrTrackingEntry, e.SerialNo)
			}
			serials[e.SerialNo] = true
			continue
		}
		key := e.BatchNo + "|" + e.LocationCode
		if pairs[key] {
			return fmt.Errorf("%w: batch %s at %q is listed twice", ErrTrackingEntry, e.BatchNo, e.LocationCode)
		}
		pairs[key] = true
	}
	return nil
}

// TrackedQty is the sum of the entry quantities.
func TrackedQty(entries []TrackingEntry) decimal.Decimal {
	total := decimal.Zero
	for _, e := range entries {
		total = total.Add(e.Qty)
	}
	return total
}

// CheckTrackedWithinPicked refuses entries (unshipped plus already shipped) that
// cover more than the picked quantity.
func CheckTrackedWithinPicked(tracked, picked decimal.Decimal) error {
	if tracked.GreaterThan(picked) {
		return fmt.Errorf("%w: %s tracked, %s picked", ErrTrackingQty, tracked, picked)
	}
	return nil
}

// CheckTrackedForPost enforces BR-28.10 at posting: a tracking-required line
// must have exactly the quantity it ships now covered by entries.
func CheckTrackedForPost(required bool, shipping, tracked decimal.Decimal) error {
	if required && !tracked.Equal(shipping) {
		return fmt.Errorf("%w: open entries cover %s units, the slip ships %s", ErrTrackingRequired, tracked, shipping)
	}
	return nil
}
