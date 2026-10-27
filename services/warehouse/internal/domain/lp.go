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
