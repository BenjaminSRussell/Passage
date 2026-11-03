package domain

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// MX_DIOT: the SAT DIOT batch file (Declaracion Informativa de Operaciones con
// Terceros), a pipe-delimited TXT with one line per third party and no header.
// SAT publishes no machine schema for it, so the rules encode the layout: the
// eleven leading columns below, in order, with the field-level rules SAT's
// loader applies. Columns: tipoTercero (04 national supplier, 05 foreign
// supplier, 15 global operations), tipoOperacion (03 professional services,
// 06 leasing, 85 other), rfc (required for 04, empty otherwise, mod-11
// homoclave verified), numIdFiscal and nombreExtranjero and pais (required for
// 05, empty for 04), then whole-peso amounts: importe16, importe0,
// importeExento, ivaAcreditable, ivaRetenido. Cross-checks: ivaAcreditable
// does not exceed 16% of importe16 (rounded up), a line pays something, and no
// third party repeats within a tipoOperacion.

const diotColumns = 11

var (
	diotRFCShape  = regexp.MustCompile(`^[A-ZÑ&]{3,4}\d{6}[A-Z0-9]{3}$`)
	diotCountry   = regexp.MustCompile(`^[A-Z]{2}$`)
	diotTipoTerc  = map[string]bool{"04": true, "05": true, "15": true}
	diotTipoOper  = map[string]bool{"03": true, "06": true, "85": true}
	diotAmountCol = []string{"importe16", "importe0", "importeExento", "ivaAcreditable", "ivaRetenido"}
	genericRFCs   = map[string]bool{"XAXX010101000": true, "XEXX010101000": true}
)

const (
	rfcAlphabet  = "0123456789ABCDEFGHIJKLMN&OPQRSTUVWXYZ Ñ"
	moralRFCLen  = 12
	fisicaRFCLen = 13
	diotIDMax    = 40
	diotNameMax  = 43
)

// rfcCheckChar computes the homoclave check character over the first 11 (legal
// entity) or 12 (individual) characters of an RFC.
func rfcCheckChar(body string) byte {
	body = strings.Repeat(" ", 12-len([]rune(body))) + body
	sum := 0
	for i, r := range []rune(body) {
		sum += strings.IndexRune(rfcAlphabet, r) * (13 - i)
	}
	switch d := 11 - sum%11; d {
	case 11:
		return '0'
	case 10:
		return 'A'
	default:
		return byte('0' + d)
	}
}

func validRFC(rfc string) bool {
	if genericRFCs[rfc] {
		return true
	}
	runes := []rune(rfc)
	if !diotRFCShape.MatchString(rfc) || len(runes) < moralRFCLen || len(runes) > fisicaRFCLen {
		return false
	}
	dateAt := len(runes) - 9
	if _, ok := validDate("060102", string(runes[dateAt:dateAt+6])); !ok {
		return false
	}
	return rfcCheckChar(string(runes[:len(runes)-1])) == byte(runes[len(runes)-1])
}

// ValidateDIOT checks a DIOT batch file; it returns every failure.
func ValidateDIOT(raw []byte) []FieldError {
	var errs fieldErrs
	lines := strings.Split(strings.ReplaceAll(strings.TrimRight(string(raw), "\r\n"), "\r\n", "\n"), "\n")
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		errs.add("file", "has no third-party lines")
		return errs
	}
	seen := map[string]bool{}
	for i, line := range lines {
		at := func(col string) string { return "line" + strconv.Itoa(i+1) + "." + col }
		cols := strings.Split(line, "|")
		if len(cols) != diotColumns {
			errs.add(at("columns"), "has %d pipe-delimited columns, want %d", len(cols), diotColumns)
			continue
		}
		tipoT, tipoO, rfc, idFiscal, name, country := cols[0], cols[1], cols[2], cols[3], cols[4], cols[5]
		if !diotTipoTerc[tipoT] {
			errs.add(at("tipoTercero"), "%q must be 04, 05 or 15", tipoT)
		}
		if !diotTipoOper[tipoO] {
			errs.add(at("tipoOperacion"), "%q must be 03, 06 or 85", tipoO)
		}
		switch tipoT {
		case "04":
			if !validRFC(rfc) {
				errs.add(at("rfc"), "%q must be a valid RFC with a correct check character", rfc)
			}
			if idFiscal+name+country != "" {
				errs.add(at("numIdFiscal"), "numIdFiscal, nombreExtranjero and pais must be empty for a national supplier")
			}
		case "05":
			if rfc != "" {
				errs.add(at("rfc"), "must be empty for a foreign supplier")
			}
			if idFiscal == "" || len(idFiscal) > diotIDMax {
				errs.add(at("numIdFiscal"), "is required, at most %d characters", diotIDMax)
			}
			if name == "" || len([]rune(name)) > diotNameMax {
				errs.add(at("nombreExtranjero"), "is required, at most %d characters", diotNameMax)
			}
			if !diotCountry.MatchString(country) {
				errs.add(at("pais"), "%q must be a two-letter country code", country)
			}
		case "15":
			if rfc+idFiscal+name+country != "" {
				errs.add(at("rfc"), "rfc, numIdFiscal, nombreExtranjero and pais must be empty for global operations")
			}
		}
		key := tipoT + "|" + tipoO + "|" + rfc + "|" + idFiscal
		if seen[key] {
			errs.add(at("tercero"), "repeats a third party already declared for this tipoOperacion")
		}
		seen[key] = true

		amounts := map[string]decimal.Decimal{}
		allOK := true
		for j, col := range diotAmountCol {
			d, ok := parseAmount(cols[6+j], wholeNumber)
			if !ok || d.IsNegative() {
				errs.add(at(col), "%q must be a whole number of pesos, not negative", cols[6+j])
				allOK = false
				continue
			}
			amounts[col] = d
		}
		if !allOK {
			continue
		}
		if amounts["importe16"].Add(amounts["importe0"]).Add(amounts["importeExento"]).IsZero() {
			errs.add(at("importe"), "line pays nothing")
		}
		if limit := amounts["importe16"].Mul(decimal.RequireFromString("0.16")).Ceil(); amounts["ivaAcreditable"].GreaterThan(limit) {
			errs.add(at("ivaAcreditable"), "%s exceeds 16%% of importe16 (%s)", amounts["ivaAcreditable"], limit)
		}
	}
	return errs
}
