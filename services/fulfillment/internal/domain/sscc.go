// Package domain holds the pure fulfillment rules: SSCC and GS1-128 handling,
// release grouping, pick and pack quantities, the shipment state machine and
// the packing-slip COGS posting matrix. It performs no I/O.
package domain

import (
	"errors"
	"fmt"
	"strconv"
)

// SSCCLength is the number of digits in an SSCC-18 (GS1 application identifier 00).
const SSCCLength = 18

var (
	ErrSSCCFormat  = errors.New("sscc must be 18 digits")
	ErrCheckDigit  = errors.New("check digit is wrong")
	ErrPrefix      = errors.New("gs1 company prefix must be 6 to 12 digits")
	ErrSerialRange = errors.New("serial reference does not fit the company prefix")
	ErrExtension   = errors.New("extension digit must be 0 to 9")
)

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// CheckDigit returns the GS1 mod-10 check digit for body, the digits before the
// check digit. Weights 3 and 1 alternate starting from the rightmost digit.
func CheckDigit(body string) (int, error) {
	if !allDigits(body) {
		return 0, fmt.Errorf("check digit body: %w", ErrSSCCFormat)
	}
	sum := 0
	for i := 0; i < len(body); i++ {
		d := int(body[len(body)-1-i] - '0')
		if i%2 == 0 {
			sum += 3 * d
		} else {
			sum += d
		}
	}
	return (10 - sum%10) % 10, nil
}

// ValidCheckedDigits reports whether s is all digits and its last digit is the
// GS1 check digit of the preceding digits. It is used for SSCC and GTIN values.
func ValidCheckedDigits(s string) error {
	if !allDigits(s) || len(s) < 2 {
		return ErrSSCCFormat
	}
	want, _ := CheckDigit(s[:len(s)-1])
	if got := int(s[len(s)-1] - '0'); got != want {
		return ErrCheckDigit
	}
	return nil
}

// SSCC builds an SSCC-18 from the extension digit, the GS1 company prefix and
// the serial reference. The serial is zero-padded to the width the prefix
// leaves: 18 - 1 (check) - 1 (extension) - len(prefix).
func SSCC(extension int, prefix string, serial uint64) (string, error) {
	if extension < 0 || extension > 9 {
		return "", ErrExtension
	}
	if !allDigits(prefix) || len(prefix) < 6 || len(prefix) > 12 {
		return "", ErrPrefix
	}
	width := SSCCLength - 2 - len(prefix)
	if serial >= pow10(width) {
		return "", ErrSerialRange
	}
	body := strconv.Itoa(extension) + prefix + fmt.Sprintf("%0*d", width, serial)
	cd, err := CheckDigit(body)
	if err != nil {
		return "", err
	}
	return body + strconv.Itoa(cd), nil
}

// ValidSSCC reports whether s is a well-formed SSCC-18 with a correct check digit.
func ValidSSCC(s string) error {
	if len(s) != SSCCLength {
		return ErrSSCCFormat
	}
	return ValidCheckedDigits(s)
}

func pow10(n int) uint64 {
	v := uint64(1)
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}
