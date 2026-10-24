package domain

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

const (
	TypeGTIN8  = "GTIN8"
	TypeGTIN12 = "GTIN12"
	TypeGTIN13 = "GTIN13"
	TypeGTIN14 = "GTIN14"
	TypeSSCC   = "SSCC"
	TypeUPC    = "UPC"
	TypeCustom = "custom"
)

// CheckDigit returns the GS1 mod-10 check digit for a numeric payload (weights 3,1 from the right; BR-8.7).
func CheckDigit(payload string) (int, error) {
	if payload == "" {
		return 0, errf(CodeGTINCheckDigit, "empty payload")
	}
	sum := 0
	weight := 3
	for i := len(payload) - 1; i >= 0; i-- {
		if payload[i] < '0' || payload[i] > '9' {
			return 0, errf(CodeGTINCheckDigit, "GTIN payload must be numeric")
		}
		sum += int(payload[i]-'0') * weight
		if weight == 3 {
			weight = 1
		} else {
			weight = 3
		}
	}
	return (10 - (sum % 10)) % 10, nil
}

// ValidateGTIN accepts GTIN-8/12/13/14 (and UPC-A as 12) with a correct check digit.
func ValidateGTIN(value string) error {
	v := strings.TrimSpace(value)
	switch len(v) {
	case 8, 12, 13, 14:
	default:
		return errf(CodeGTINCheckDigit, "GTIN "+v+" has an invalid length")
	}
	for _, r := range v {
		if !unicode.IsDigit(r) {
			return errf(CodeGTINCheckDigit, "GTIN "+v+" has an invalid check digit")
		}
	}
	want, err := CheckDigit(v[:len(v)-1])
	if err != nil {
		return err
	}
	got := int(v[len(v)-1] - '0')
	if got != want {
		return errf(CodeGTINCheckDigit, "GTIN "+v+" has an invalid check digit")
	}
	return nil
}

// CanonicalGTIN14 left-pads a valid GTIN to 14 digits for uniqueness (BR-8.7).
func CanonicalGTIN14(value string) (string, error) {
	if err := ValidateGTIN(value); err != nil {
		return "", err
	}
	return strings.Repeat("0", 14-len(value)) + value, nil
}

// AllocateGTIN13 draws the next number from a GS1 company prefix (FR-8.9, AC-8.4).
func AllocateGTIN13(prefix string, next int64) (string, int64, error) {
	p := strings.TrimSpace(prefix)
	if len(p) < 6 || len(p) > 12 {
		return "", next, errf(CodeGTINCheckDigit, "GS1 company prefix must be 6 to 12 digits")
	}
	for _, r := range p {
		if r < '0' || r > '9' {
			return "", next, errf(CodeGTINCheckDigit, "GS1 company prefix must be numeric")
		}
	}
	if next < 0 {
		return "", next, errf(CodeGTINCheckDigit, "next item number is outside the prefix range")
	}
	item := strconv.FormatInt(next, 10)
	payload := p + item
	if len(payload) < 12 {
		payload = p + fmt.Sprintf("%0*d", 12-len(p), next)
	}
	if len(payload) != 12 && len(payload) != 13 {
		return "", next, errf(CodeGTINCheckDigit, "GTIN payload must be 12 or 13 digits")
	}
	d, err := CheckDigit(payload)
	if err != nil {
		return "", next, err
	}
	return payload + strconv.Itoa(d), next + 1, nil
}

// GenerateSSCC builds an SSCC-18 from an extension digit, company prefix and serial.
func GenerateSSCC(extension int, prefix string, serial int64) (string, error) {
	if extension < 0 || extension > 9 {
		return "", errf(CodeGTINCheckDigit, "SSCC extension digit must be 0-9")
	}
	p := strings.TrimSpace(prefix)
	if len(p) < 6 || len(p) > 10 {
		return "", errf(CodeGTINCheckDigit, "SSCC company prefix must be 6 to 10 digits")
	}
	serialWidth := 16 - len(p)
	ref := fmt.Sprintf("%d%s%0*d", extension, p, serialWidth, serial)
	if len(ref) != 17 {
		return "", errf(CodeGTINCheckDigit, "SSCC reference must be 17 digits")
	}
	d, err := CheckDigit(ref)
	if err != nil {
		return "", err
	}
	return ref + strconv.Itoa(d), nil
}

// IdentifierTypeForLength maps a validated GTIN onto its type name.
func IdentifierTypeForLength(value string) string {
	switch len(value) {
	case 8:
		return TypeGTIN8
	case 12:
		return TypeGTIN12
	case 13:
		return TypeGTIN13
	case 14:
		return TypeGTIN14
	case 18:
		return TypeSSCC
	default:
		return TypeCustom
	}
}
