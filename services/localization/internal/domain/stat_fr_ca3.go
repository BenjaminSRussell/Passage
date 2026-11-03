package domain

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// FR_CA3: the boxes of the DGFiP monthly VAT return 3310-CA3 as a semicolon
// CSV of code;value rows. The EDI-TVA envelope (EDIFACT VATDEC) is not
// encoded: DGFiP publishes the CA3 boxes and their arithmetic, which this
// validator enforces. Rows: siren (nine digits, Luhn), periode (YYYY-MM), then
// the boxes ligne01 and ligne02 (bases), ligne08, ligne09 and ligne9B (VAT at
// 20%, 5.5% and 10%), ligne16 (total gross VAT), ligne19, ligne20 and ligne22
// (deductible VAT on fixed assets, on other goods and services, credit
// carried forward), ligne23 (total deductible), ligne25 (VAT credit) and
// ligne28 (net VAT due). All boxes are whole euros, not negative. Cross-checks:
// 16 = 08 + 09 + 9B, 23 = 19 + 20 + 22, 28 = max(16 - 23, 0), 25 = max(23 - 16, 0).

var (
	ca3SIREN   = regexp.MustCompile(`^\d{9}$`)
	ca3Periode = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
	ca3Boxes   = []string{"ligne01", "ligne02", "ligne08", "ligne09", "ligne9B", "ligne16", "ligne19", "ligne20", "ligne22", "ligne23", "ligne25", "ligne28"}
)

// ValidateCA3 checks a CA3 file; it returns every failure.
func ValidateCA3(raw []byte) []FieldError {
	var errs fieldErrs
	rows := map[string]string{}
	for i, line := range strings.Split(strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), "\n") {
		code, value, ok := strings.Cut(line, ";")
		if !ok || strings.Contains(value, ";") {
			errs.add("row"+strconv.Itoa(i+1), "must be code;value")
			continue
		}
		if _, dup := rows[code]; dup {
			errs.add(code, "appears more than once")
		}
		rows[code] = strings.TrimSpace(value)
	}
	if s := rows["siren"]; !ca3SIREN.MatchString(s) || !luhnValid(s) {
		errs.add("siren", "%q must be nine digits with a valid Luhn check digit", s)
	}
	if p := rows["periode"]; !ca3Periode.MatchString(p) {
		errs.add("periode", "%q must be YYYY-MM", p)
	}
	known := map[string]bool{"siren": true, "periode": true}
	v := map[string]decimal.Decimal{}
	for _, b := range ca3Boxes {
		known[b] = true
		text, present := rows[b]
		if !present {
			errs.add(b, "is required")
			continue
		}
		d, ok := parseAmount(text, wholeNumber)
		if !ok || d.IsNegative() {
			errs.add(b, "%q must be whole euros, not negative", text)
			continue
		}
		v[b] = d
	}
	for code := range rows {
		if !known[code] {
			errs.add(code, "box not covered by this validator")
		}
	}
	if len(v) != len(ca3Boxes) {
		return errs
	}
	if !v["ligne16"].Equal(v["ligne08"].Add(v["ligne09"]).Add(v["ligne9B"])) {
		errs.add("ligne16", "must equal ligne08 + ligne09 + ligne9B")
	}
	if !v["ligne23"].Equal(v["ligne19"].Add(v["ligne20"]).Add(v["ligne22"])) {
		errs.add("ligne23", "must equal ligne19 + ligne20 + ligne22")
	}
	if !v["ligne28"].Equal(decimal.Max(v["ligne16"].Sub(v["ligne23"]), decimal.Zero)) {
		errs.add("ligne28", "must equal ligne16 - ligne23 when positive, else 0")
	}
	if !v["ligne25"].Equal(decimal.Max(v["ligne23"].Sub(v["ligne16"]), decimal.Zero)) {
		errs.add("ligne25", "must equal ligne23 - ligne16 when positive, else 0")
	}
	return errs
}
