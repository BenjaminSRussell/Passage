// Package domain holds the purchasing rules as pure functions: trade agreement
// price resolution, PO totals, approval routing and reapproval rules. It does
// no I/O so every rule is tested without a database.
package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// CodeValidation is the platform validation code used for malformed input.
const CodeValidation = "COM-0001"

// Error is a domain rule violation carrying a stable code (PRC-*).
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func fail(code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Day is a calendar date (no time of day) serialized as YYYY-MM-DD.
type Day struct{ time.Time }

const dayLayout = "2006-01-02"

// ParseDay parses a YYYY-MM-DD date.
func ParseDay(s string) (Day, error) {
	t, err := time.Parse(dayLayout, s)
	if err != nil {
		return Day{}, err
	}
	return Day{t.UTC()}, nil
}

// String renders the date as YYYY-MM-DD.
func (d Day) String() string { return d.Format(dayLayout) }

// MarshalJSON encodes the date as a string.
func (d Day) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON decodes a YYYY-MM-DD string.
func (d *Day) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := ParseDay(s)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}
