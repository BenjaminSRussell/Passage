package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// A statutory validator checks one regulator output against the regulator's
// published schema or layout and the arithmetic the regulator enforces on
// submission. name is the file name, which some regulators fix (FR FEC).
type statutoryValidator func(name string, raw []byte) []FieldError

var statutoryValidators = map[string]statutoryValidator{
	"UK_MTD_VAT":   validateMTDVATJSON,
	"US_1099_FIRE": func(_ string, raw []byte) []FieldError { return ValidateFIRE1099NEC(raw) },
	"DE_UStVA":     func(_ string, raw []byte) []FieldError { return ValidateUStVA(raw) },
	"FR_FEC":       ValidateFEC,
	"IN_GSTR1":     func(_ string, raw []byte) []FieldError { return ValidateGSTR1(raw) },
	"MX_CFDI40":    func(_ string, raw []byte) []FieldError { return ValidateCFDI40(raw) },
	"CA_GST34":     func(_ string, raw []byte) []FieldError { return ValidateGST34(raw) },
	"MX_DIOT":      func(_ string, raw []byte) []FieldError { return ValidateDIOT(raw) },
	"FR_CA3":       func(_ string, raw []byte) []FieldError { return ValidateCA3(raw) },
	"IN_26Q":       func(_ string, raw []byte) []FieldError { return ValidateForm26Q(raw) },
}

// StatutoryValidatorCodes lists the statutory report codes that have a validator.
func StatutoryValidatorCodes() []string {
	out := make([]string, 0, len(statutoryValidators))
	for c := range statutoryValidators {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// ValidateStatutory runs the validator of a statutory report code over one
// output. It returns every failure; an unknown code is an error (LOC-0505).
func ValidateStatutory(report, name string, raw []byte) ([]FieldError, error) {
	v, ok := statutoryValidators[report]
	if !ok {
		return nil, fail(CodeInvalid, "no validator for statutory report %q", report)
	}
	return v(name, raw), nil
}

func validateMTDVATJSON(_ string, raw []byte) []FieldError {
	var r VATReturn
	if err := json.Unmarshal(raw, &r); err != nil {
		return []FieldError{{Field: "body", Detail: err.Error()}}
	}
	return ValidateMTDVAT(r)
}

// fieldErrs collects the failures of one validation run.
type fieldErrs []FieldError

func (e *fieldErrs) add(field, format string, args ...any) {
	*e = append(*e, FieldError{Field: field, Detail: fmt.Sprintf(format, args...)})
}

// Shared helpers for the statutory validators.

var (
	wholeNumber = regexp.MustCompile(`^-?\d+$`)
	twoDecimals = regexp.MustCompile(`^-?\d+(\.\d{1,2})?$`)
)

// parseAmount parses s when it matches pattern; ok is false otherwise.
func parseAmount(s string, pattern *regexp.Regexp) (decimal.Decimal, bool) {
	s = strings.TrimSpace(s)
	if !pattern.MatchString(s) {
		return decimal.Zero, false
	}
	d, err := decimal.NewFromString(s)
	return d, err == nil
}

func validDate(layout, s string) (time.Time, bool) {
	t, err := time.Parse(layout, s)
	return t, err == nil && t.Format(layout) == s
}

// luhnValid reports whether a digit string passes the Luhn (mod 10) check,
// used by the French SIREN and the Canadian business number.
func luhnValid(digits string) bool {
	sum := 0
	for i := 0; i < len(digits); i++ {
		c := digits[len(digits)-1-i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if i%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return len(digits) > 0 && sum%10 == 0
}
