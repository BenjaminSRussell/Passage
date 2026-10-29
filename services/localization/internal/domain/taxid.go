package domain

import (
	"regexp"
	"strings"
)

var taxIDPatterns = map[string]*regexp.Regexp{
	"GB_VAT":   regexp.MustCompile(`^GB ?(\d{9}|\d{12}|GD\d{3}|HA\d{3})$`),
	"DE_USTID": regexp.MustCompile(`^DE\d{9}$`),
	"FR_VAT":   regexp.MustCompile(`^FR[0-9A-HJ-NP-Z]{2}\d{9}$`),
	"IN_GSTIN": regexp.MustCompile(`^\d{2}[A-Z]{5}\d{4}[A-Z][1-9A-Z]Z[0-9A-Z]$`),
	"MX_RFC":   regexp.MustCompile(`^[A-ZÑ&]{3,4}\d{6}[A-Z0-9]{3}$`),
	"CA_BN":    regexp.MustCompile(`^\d{9}(RT\d{4})?$`),
	"US_EIN":   regexp.MustCompile(`^\d{2}-?\d{7}$`),
}

const gstinBodyLen = 14

// ValidateTaxID checks a tax id against the named scheme. GSTIN also verifies
// its check character (mod-36 Luhn variant over the first 14 characters).
func ValidateTaxID(scheme, value string) error {
	re, ok := taxIDPatterns[scheme]
	if !ok {
		return fail(CodeInvalid, "unknown tax id scheme %q", scheme)
	}
	v := strings.ToUpper(strings.TrimSpace(value))
	if !re.MatchString(v) {
		return fail(CodeInvalid, "%q is not a valid %s", value, scheme)
	}
	if scheme == "IN_GSTIN" && gstinCheckChar(v[:gstinBodyLen]) != v[gstinBodyLen] {
		return fail(CodeInvalid, "%q has a wrong GSTIN check character", value)
	}
	return nil
}

// GSTINCheckChar computes the check character for a 14-character GSTIN body.
func GSTINCheckChar(body string) byte { return gstinCheckChar(body) }

func gstinCheckChar(body string) byte {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	factor, sum := 2, 0
	for i := len(body) - 1; i >= 0; i-- {
		code := strings.IndexByte(alphabet, body[i])
		addend := factor * code
		sum += addend/36 + addend%36
		if factor == 2 {
			factor = 1
		} else {
			factor = 2
		}
	}
	check := (36 - sum%36) % 36
	return alphabet[check]
}
