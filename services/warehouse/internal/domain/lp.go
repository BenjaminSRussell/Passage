package domain

import (
	"fmt"
	"strconv"
)

// MaxLPDepth is the deepest license plate nesting allowed (FR-31.2). A root LP is depth 1.
const MaxLPDepth = 5

// MaxLPIDLength is the longest external license plate id (FR-31.2).
const MaxLPIDLength = 40

// ValidLPID reports whether id is an acceptable license plate id: 1 to 40
// characters of letters, digits, '.', '_' or '-'.
func ValidLPID(id string) bool {
	if len(id) == 0 || len(id) > MaxLPIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// NestDepth returns the depth of a child placed under a parent at parentDepth.
// A child that would reach depth 6 is refused with WMS-0104.
func NestDepth(parentDepth int) (int, error) {
	child := parentDepth + 1
	if child > MaxLPDepth {
		return 0, errorf(CodeNestDepth, "license plate nesting would exceed %d levels", MaxLPDepth)
	}
	return child, nil
}

// SSCC18 builds an 18-digit SSCC from an extension digit, a GS1 company prefix
// and a serial reference. The company prefix and serial together fill 16 digits,
// and the GS1 mod-10 check digit is appended (weights 3 and 1 from the right).
func SSCC18(extension int, companyPrefix, serial string) (string, error) {
	if extension < 0 || extension > 9 {
		return "", fmt.Errorf("extension digit must be 0 to 9, got %d", extension)
	}
	if !allDigits(companyPrefix) || !allDigits(serial) || len(companyPrefix)+len(serial) != 16 {
		return "", fmt.Errorf("company prefix and serial reference must be digits totalling 16")
	}
	body := strconv.Itoa(extension) + companyPrefix + serial
	return body + strconv.Itoa(gs1Check(body)), nil
}

// ValidSSCC reports whether s is 18 digits with a correct GS1 check digit.
func ValidSSCC(s string) bool {
	return len(s) == 18 && allDigits(s) && gs1Check(s[:17]) == int(s[17]-'0')
}

// gs1Check computes the mod-10 check digit for body. The rightmost body digit
// has weight 3, and the weights alternate 3 and 1 moving left.
func gs1Check(body string) int {
	sum := 0
	for i := 0; i < len(body); i++ {
		d := int(body[len(body)-1-i] - '0')
		if i%2 == 0 {
			sum += 3 * d
		} else {
			sum += d
		}
	}
	return (10 - sum%10) % 10
}

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

// Codes of the GS1 settings and of generated pallet identity.
const (
	// CodeGS1NotSet is a receipt that needs an SSCC or a license plate id the warehouse must generate, before the
	// tenant's GS1 company prefix is set (422).
	CodeGS1NotSet = "WMS-GS1-NOT-SET"
	// CodeGS1Locked is a change of the company prefix after SSCCs were issued under the old one (409).
	CodeGS1Locked = "WMS-GS1-LOCKED"
	// CodeGS1Exhausted is a prefix whose serial references are all used (409).
	CodeGS1Exhausted = "WMS-GS1-EXHAUSTED"
)

// ValidCompanyPrefix reports whether s is a GS1 company prefix: 7 to 11 digits.
func ValidCompanyPrefix(s string) bool { return len(s) >= 7 && len(s) <= 11 && allDigits(s) }

// SerialCapacity is how many serial references a company prefix has: the SSCC's 16 digits after the extension are the
// prefix and the serial, so a 7-digit prefix has 10^9 serials and an 11-digit one 10^5.
func SerialCapacity(companyPrefix string) int64 {
	n := int64(1)
	for i := 0; i < 16-len(companyPrefix); i++ {
		n *= 10
	}
	return n
}

// SSCCForSerial builds the SSCC of the n-th pallet of a company prefix (n from 1): the extension digit, the prefix,
// the serial reference padded with zeros to fill 16 digits, and the check digit.
func SSCCForSerial(extension int, companyPrefix string, n int64) (string, error) {
	if !ValidCompanyPrefix(companyPrefix) {
		return "", errorf(CodeGS1NotSet, "the GS1 company prefix is not valid")
	}
	if n < 1 || n >= SerialCapacity(companyPrefix) {
		return "", errorf(CodeGS1Exhausted, "the GS1 company prefix %s has no serial reference left", companyPrefix)
	}
	serial := strconv.FormatInt(n, 10)
	width := 16 - len(companyPrefix)
	for len(serial) < width {
		serial = "0" + serial
	}
	return SSCC18(extension, companyPrefix, serial)
}
