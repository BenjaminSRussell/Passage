package domain

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// MX_DIOT: the SAT DIOT batch file (Declaracion Informativa de Operaciones con
// Terceros) in the 2025 layout: a pipe-delimited UTF-8 TXT, one line per third
// party, no header, exactly 54 fields. Fields 1-7 identify the third party
// (tipoTercero, tipoOperacion, RFC, numIdFiscal, nombreExtranjero, pais,
// jurisdiccion); fields 8-17 are the value of the acts by region (north border,
// south border, 16%, customs import of goods, import of intangibles and
// services) as value/returns pairs; 18-27 IVA acreditable (exclusively taxed /
// by proportion) per region; 28-47 IVA no acreditable (four columns per
// region); 48-53 withheld IVA and the exempt, 0% and non-object acts; field 54
// is the 01/02 manifest that the vouchers had fiscal effects. Amount fields are
// optional, whole pesos of at most 14 digits. The pre-2025 24-column layout is
// not accepted.

const (
	diotFields       = 54
	diotFirstAmount  = 8
	diotLastAmount   = 53
	diotManifest     = 54
	diotRegions      = 5
	diotRegion16     = 2
	diotMaxDigits    = 14
	rfcAlphabet      = "0123456789ABCDEFGHIJKLMN&OPQRSTUVWXYZ Ñ"
	moralRFCLen      = 12
	fisicaRFCLen     = 13
	diotIDMax        = 40
	diotNameMax      = 300
	diotJurisdiction = 300
	globalRFC        = "XAXX010101000"
)

var (
	diotRFCShape = regexp.MustCompile(`^[A-ZÑ&]{3,4}\d{6}[A-Z0-9]{3}$`)
	diotCountry  = regexp.MustCompile(`^[A-Z]{3}$`)
	diotAmount   = regexp.MustCompile(`^\d{1,14}$`)
	diotTipoOper = map[string]map[string]bool{
		"04": {"02": true, "03": true, "06": true, "08": true, "85": true},
		"05": {"02": true, "03": true, "07": true},
		"15": {"87": true},
	}
	genericRFCs = map[string]bool{"XAXX010101000": true, "XEXX010101000": true}
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

func diotCol(n int) string { return "col" + strconv.Itoa(n) }

// ValidateDIOT checks a DIOT batch file; it returns every failure.
func ValidateDIOT(raw []byte) []FieldError {
	var errs fieldErrs
	if strings.TrimSpace(string(raw)) == "" {
		errs.add("file", "has no third-party lines")
		return errs
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		at := func(col string) string { return "line" + strconv.Itoa(i+1) + "." + col }
		cols := strings.Split(line, "|")
		if len(cols) != diotFields {
			errs.add(at("columns"), "has %d pipe-delimited columns, want %d", len(cols), diotFields)
			continue
		}
		diotThird(&errs, at, cols)
		key := cols[0] + "|" + cols[1] + "|" + cols[2] + "|" + cols[3]
		if seen[key] {
			errs.add(at("tercero"), "repeats a third party already declared for this tipoOperacion")
		}
		seen[key] = true
		diotAmounts(&errs, at, cols)
		if m := cols[diotManifest-1]; m != "01" && m != "02" {
			errs.add(at(diotCol(diotManifest)), "%q must be 01 or 02", m)
		}
	}
	return errs
}

func diotThird(errs *fieldErrs, at func(string) string, cols []string) {
	tipoT, tipoO, rfc, idFiscal, name, country, jurisdiction := cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], cols[6]
	allowed, known := diotTipoOper[tipoT]
	if !known {
		errs.add(at("tipoTercero"), "%q must be 04, 05 or 15", tipoT)
		return
	}
	if !allowed[tipoO] {
		errs.add(at("tipoOperacion"), "%q is not a tipoOperacion for tipoTercero %s", tipoO, tipoT)
	}
	switch tipoT {
	case "04":
		if !validRFC(rfc) || genericRFCs[rfc] {
			errs.add(at("rfc"), "%q must be a valid RFC with a correct check character", rfc)
		}
		if idFiscal+name+country+jurisdiction != "" {
			errs.add(at("numIdFiscal"), "numIdFiscal, nombreExtranjero, pais and jurisdiccion must be empty for a national supplier")
		}
	case "15":
		if rfc != globalRFC {
			errs.add(at("rfc"), "must be %s for global operations", globalRFC)
		}
		if idFiscal+name+country+jurisdiction != "" {
			errs.add(at("numIdFiscal"), "numIdFiscal, nombreExtranjero, pais and jurisdiccion must be empty for global operations")
		}
	case "05":
		if rfc != "" && !validRFC(rfc) {
			errs.add(at("rfc"), "%q must be empty or a valid RFC", rfc)
		}
		if idFiscal == "" || len([]rune(idFiscal)) > diotIDMax {
			errs.add(at("numIdFiscal"), "is required, at most %d characters", diotIDMax)
		}
		if name == "" || len([]rune(name)) > diotNameMax {
			errs.add(at("nombreExtranjero"), "is required, at most %d characters", diotNameMax)
		}
		if !diotCountry.MatchString(country) {
			errs.add(at("pais"), "%q must be a three-letter country code from the SAT catalogue", country)
		}
		if (country == "ZZZ") != (jurisdiction != "") || len([]rune(jurisdiction)) > diotJurisdiction {
			errs.add(at("jurisdiccion"), "is required (at most %d characters) when pais is ZZZ and empty otherwise", diotJurisdiction)
		}
	}
}

func diotAmounts(errs *fieldErrs, at func(string) string, cols []string) {
	v := map[int]decimal.Decimal{}
	for n := diotFirstAmount; n <= diotLastAmount; n++ {
		text := cols[n-1]
		if text == "" {
			continue
		}
		if !diotAmount.MatchString(text) {
			errs.add(at(diotCol(n)), "%q must be whole pesos, not negative, at most %d digits", text, diotMaxDigits)
			continue
		}
		v[n] = decimal.RequireFromString(text)
	}
	for r := range diotRegions {
		base, returns := diotFirstAmount+2*r, diotFirstAmount+2*r+1
		if v[returns].GreaterThan(v[base]) {
			errs.add(at(diotCol(returns)), "returns %s exceed the value of the acts %s", v[returns], v[base])
		}
		for _, n := range []int{18 + 2*r, 19 + 2*r} {
			if v[n].IsPositive() && !v[base].IsPositive() {
				errs.add(at(diotCol(n)), "IVA acreditable needs a value of the acts above zero in column %d", base)
			}
		}
	}
	base16 := diotFirstAmount + 2*diotRegion16
	credit := v[22].Add(v[23]).Add(v[36]).Add(v[37]).Add(v[38]).Add(v[39])
	if limit := v[base16].Mul(decimal.RequireFromString("0.16")).Ceil(); credit.GreaterThan(limit) {
		errs.add(at(diotCol(22)), "IVA acreditable and no acreditable %s exceed 16%% of the base (%s)", credit, limit)
	}
	paid := decimal.Zero
	for _, n := range []int{8, 10, 12, 14, 16, 49, 50, 51, 52, 53} {
		paid = paid.Add(v[n])
	}
	if paid.IsZero() {
		errs.add(at("importe"), "line declares no value of acts")
	}
}
