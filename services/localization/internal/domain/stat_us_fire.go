package domain

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// IRS FIRE 1099-NEC (US_1099_FIRE). The layout is Publication 1220 (Rev.
// 05-2026, tax year 2025): fixed 750-position records, T (transmitter), then
// per issuer an A record, its B (payee) records and a C (end of issuer) record,
// optional K (state totals) records, and a closing F record. Positions below
// are Pub 1220's 1-based field positions.

const fireRecordLen = 750

// fireAmountCodes are the 18 payment amount codes in Pub 1220 order; B record
// amount k sits at 55+12k and C control total k at 16+18k.
const fireAmountCodes = "123456789ABCDEFGHJ"

// 1099-NEC amount codes (Pub 1220, Issuer "A" record): 1 nonemployee
// compensation, 4 federal income tax withheld.
var necAmountCodes = map[byte]bool{'1': true, '4': true}

var (
	fireTIN      = regexp.MustCompile(`^\d{9}$`)
	fireZIP      = regexp.MustCompile(`^(\d{9}|\d{5} {4})$`)
	fireState    = regexp.MustCompile(`^[A-Z]{2}$`)
	fireTCC      = regexp.MustCompile(`^[0-9A-Z]{5}$`)
	fireLower    = regexp.MustCompile(`[a-z]`)
	fireAllDigit = regexp.MustCompile(`^\d+$`)
)

// Positions Pub 1220 marks "Blank" per record type (the NEC part of B included).
var fireBlanks = map[byte][][2]int{
	'T': {{21, 27}, {281, 295}, {409, 499}, {508, 517}, {705, 739}, {741, 748}},
	'A': {{7, 11}, {46, 51}, {240, 499}, {508, 748}},
	'B': {{45, 54}, {271, 286}, {408, 447}, {499, 499}, {508, 543}, {545, 546}, {548, 662}},
	'C': {{10, 15}, {340, 499}, {508, 748}},
	'F': {{31, 49}, {58, 499}, {508, 748}},
}

func pos(rec string, from, to int) string { return rec[from-1 : to] }

// splitFIRERecords cuts a file into 750-position records: either CR/LF in
// positions 749-750 of every record, or unterminated records back to back.
func splitFIRERecords(raw []byte) ([]string, error) {
	if bytes.Contains(raw, []byte("\r\n")) {
		parts := strings.Split(string(raw), "\r\n")
		if parts[len(parts)-1] != "" {
			return nil, fmt.Errorf("the last record is not terminated by CR/LF")
		}
		parts = parts[:len(parts)-1]
		for i, p := range parts {
			if len(p) != fireRecordLen-2 {
				return nil, fmt.Errorf("record %d is %d positions, want %d", i+1, len(p)+2, fireRecordLen)
			}
			parts[i] = p + "  "
		}
		return parts, nil
	}
	if bytes.ContainsAny(raw, "\r\n") {
		return nil, fmt.Errorf("a record separator must be CR/LF in positions 749-750")
	}
	if len(raw) == 0 || len(raw)%fireRecordLen != 0 {
		return nil, fmt.Errorf("file length %d is not a multiple of %d", len(raw), fireRecordLen)
	}
	out := make([]string, 0, len(raw)/fireRecordLen)
	for i := 0; i < len(raw); i += fireRecordLen {
		out = append(out, string(raw[i:i+fireRecordLen]))
	}
	return out, nil
}

// ValidateFIRE1099NEC checks an IRS FIRE file of 1099-NEC returns against the
// Pub 1220 record layouts: record order and sequence numbers, fixed lengths,
// blank and numeric fields, required fields, upper case, type of return NE
// with amount codes 1 and 4, payee counts in T, C and F, and the C control
// totals equal to the sums of the B payment amounts.
func ValidateFIRE1099NEC(raw []byte) []FieldError {
	var errs fieldErrs
	recs, err := splitFIRERecords(raw)
	if err != nil {
		errs.add("file", "%s", err)
		return errs
	}
	if len(recs) < 5 || recs[0][0] != 'T' || recs[len(recs)-1][0] != 'F' {
		errs.add("file", "must start with a T record, end with an F record and hold A, B and C records between")
		return errs
	}
	at := func(i int, from, to int) string {
		return fmt.Sprintf("record %d (%c) %d-%d", i+1, recs[i][0], from, to)
	}
	year := pos(recs[0], 2, 5)
	if !fireAllDigit.MatchString(year) {
		errs.add(at(0, 2, 5), "payment year %q must be four digits", year)
	}
	for i, r := range recs {
		if want := fmt.Sprintf("%08d", i+1); pos(r, 500, 507) != want {
			errs.add(at(i, 500, 507), "record sequence number %q, want %s", pos(r, 500, 507), want)
		}
		if pos(r, 749, 750) != "  " {
			errs.add(at(i, 749, 750), "must be blank or CR/LF")
		}
		for _, b := range fireBlanks[r[0]] {
			if strings.TrimSpace(pos(r, b[0], b[1])) != "" {
				errs.add(at(i, b[0], b[1]), "must be blank")
			}
		}
		check := r
		if r[0] == 'T' { // the contact e-mail may be lower case
			check = r[:358] + r[408:]
		}
		if fireLower.MatchString(check) {
			errs.add(at(i, 1, 750), "alpha characters must be upper case")
		}
	}
	validateFIRET(&errs, recs[0], at)

	payees, issuers := 0, 0
	i := 1
	for i < len(recs)-1 {
		if recs[i][0] != 'A' {
			errs.add(at(i, 1, 1), "expected an A record, found %q", recs[i][0])
			return errs
		}
		issuers++
		codes := validateFIRERecordA(&errs, recs[i], i, year, at)
		i++
		var totals [len(fireAmountCodes)]int64
		group := 0
		for i < len(recs)-1 && recs[i][0] == 'B' {
			validateFIRERecordB(&errs, recs[i], i, year, codes, &totals, at)
			group++
			i++
		}
		if group == 0 {
			errs.add(at(i, 1, 1), "an A record needs at least one B record")
		}
		if i >= len(recs)-1 || recs[i][0] != 'C' {
			errs.add(at(i, 1, 1), "expected the C record closing the issuer")
			return errs
		}
		validateFIRERecordC(&errs, recs[i], i, group, totals, at)
		payees += group
		i++
		for i < len(recs)-1 && recs[i][0] == 'K' {
			i++
		}
	}
	t, f, last := recs[0], recs[len(recs)-1], len(recs)-1
	if n, err := strconv.Atoi(pos(t, 296, 303)); err != nil || n != payees {
		errs.add(at(0, 296, 303), "total number of payees %q, the file holds %d B records", pos(t, 296, 303), payees)
	}
	if a := pos(f, 2, 9); a != "00000000" && a != fmt.Sprintf("%08d", issuers) {
		errs.add(at(last, 2, 9), "number of A records %q, the file holds %d", a, issuers)
	}
	if pos(f, 10, 30) != strings.Repeat("0", 21) {
		errs.add(at(last, 10, 30), "must be zeros")
	}
	if p := pos(f, 50, 57); strings.TrimSpace(p) != "" && p != fmt.Sprintf("%08d", payees) {
		errs.add(at(last, 50, 57), "total number of payees %q, the file holds %d B records", p, payees)
	}
	return errs
}

func validateFIRET(errs *fieldErrs, t string, at func(int, int, int) string) {
	if c := t[5]; c != ' ' && c != 'P' {
		errs.add(at(0, 6, 6), "prior year data indicator must be P or blank")
	}
	checkFIRETIN(errs, pos(t, 7, 15), at(0, 7, 15))
	if !fireTCC.MatchString(pos(t, 16, 20)) {
		errs.add(at(0, 16, 20), "transmitter control code must be five alphanumeric characters")
	}
	if c := t[27]; c != ' ' && c != 'T' {
		errs.add(at(0, 28, 28), "test file indicator must be T or blank")
	}
	if c := t[28]; c != ' ' && c != '1' {
		errs.add(at(0, 29, 29), "foreign entity indicator must be 1 or blank")
	}
	required(errs, t, 0, at, [][2]int{{30, 69}, {110, 149}, {190, 229}, {230, 269}, {304, 343}, {344, 358}})
	if !fireState.MatchString(pos(t, 270, 271)) {
		errs.add(at(0, 270, 271), "company state must be a postal abbreviation")
	}
	if !fireZIP.MatchString(pos(t, 272, 280)) {
		errs.add(at(0, 272, 280), "company ZIP code must be nine digits or five digits left justified")
	}
	switch t[517] {
	case 'I':
	case 'V':
		required(errs, t, 0, at, [][2]int{{519, 558}})
	default:
		errs.add(at(0, 518, 518), "vendor indicator must be V or I")
	}
}

func validateFIRERecordA(errs *fieldErrs, a string, i int, year string, at func(int, int, int) string) map[byte]bool {
	if pos(a, 2, 5) != year {
		errs.add(at(i, 2, 5), "payment year %q differs from the T record %q", pos(a, 2, 5), year)
	}
	if c := a[5]; c != ' ' && c != '1' {
		errs.add(at(i, 6, 6), "combined federal/state indicator must be 1 or blank")
	}
	checkFIRETIN(errs, pos(a, 12, 20), at(i, 12, 20))
	if c := a[24]; c != ' ' && c != '1' {
		errs.add(at(i, 25, 25), "last filing indicator must be 1 or blank")
	}
	if pos(a, 26, 27) != "NE" {
		errs.add(at(i, 26, 27), "type of return %q: this validator covers Form 1099-NEC (NE) only", pos(a, 26, 27))
	}
	codes := map[byte]bool{}
	field := pos(a, 28, 45)
	used := strings.TrimRight(field, " ")
	switch {
	case used == "" || strings.Contains(used, " "):
		errs.add(at(i, 28, 45), "amount codes must be left justified and present")
	default:
		prev := -1
		for k := 0; k < len(used); k++ {
			idx := strings.IndexByte(fireAmountCodes, used[k])
			if !necAmountCodes[used[k]] || idx <= prev {
				errs.add(at(i, 28, 45), "amount codes %q must be 1099-NEC codes (1, 4) in ascending order", used)
				break
			}
			prev = idx
			codes[used[k]] = true
		}
	}
	if c := a[51]; c != ' ' && c != '1' {
		errs.add(at(i, 52, 52), "foreign entity indicator must be 1 or blank")
	}
	required(errs, a, i, at, [][2]int{{53, 92}, {134, 173}, {174, 213}})
	if c := a[132]; c != '0' && c != '1' {
		errs.add(at(i, 133, 133), "transfer agent indicator must be 0 or 1")
	}
	if a[51] != '1' {
		if !fireState.MatchString(pos(a, 214, 215)) {
			errs.add(at(i, 214, 215), "issuer state must be a postal abbreviation")
		}
		if !fireZIP.MatchString(pos(a, 216, 224)) {
			errs.add(at(i, 216, 224), "issuer ZIP code must be nine digits or five digits left justified")
		}
	}
	return codes
}

func validateFIRERecordB(errs *fieldErrs, b string, i int, year string, codes map[byte]bool, totals *[len(fireAmountCodes)]int64, at func(int, int, int) string) {
	if pos(b, 2, 5) != year {
		errs.add(at(i, 2, 5), "payment year %q differs from the T record %q", pos(b, 2, 5), year)
	}
	if c := b[5]; c != ' ' && c != 'G' && c != 'C' {
		errs.add(at(i, 6, 6), "corrected return indicator must be G, C or blank")
	}
	if c := b[10]; c != ' ' && c != '1' && c != '2' {
		errs.add(at(i, 11, 11), "type of TIN must be 1, 2 or blank")
	}
	checkFIRETIN(errs, pos(b, 12, 20), at(i, 12, 20))
	for k := 0; k < len(fireAmountCodes); k++ {
		from := 55 + 12*k
		v := pos(b, from, from+11)
		if !fireAllDigit.MatchString(v) { // 1099-NEC amounts are never negative
			errs.add(at(i, from, from+11), "payment amount %c must be 12 digits (cents, no sign)", fireAmountCodes[k])
			continue
		}
		n, _ := strconv.ParseInt(v, 10, 64)
		if n != 0 && !codes[fireAmountCodes[k]] {
			errs.add(at(i, from, from+11), "payment amount %c is not an amount code of the A record", fireAmountCodes[k])
		}
		totals[k] += n
	}
	if c := b[286]; c != ' ' && c != '1' {
		errs.add(at(i, 287, 287), "foreign country indicator must be 1 or blank")
	}
	required(errs, b, i, at, [][2]int{{288, 327}, {368, 407}, {448, 487}})
	if b[286] != '1' {
		if !fireState.MatchString(pos(b, 488, 489)) {
			errs.add(at(i, 488, 489), "payee state must be a postal abbreviation")
		}
		if !fireZIP.MatchString(pos(b, 490, 498)) {
			errs.add(at(i, 490, 498), "payee ZIP code must be nine digits or five digits left justified")
		}
	}
	if c := b[543]; c != ' ' && c != '2' {
		errs.add(at(i, 544, 544), "second TIN notice must be 2 or blank")
	}
	if c := b[546]; c != ' ' && c != '1' {
		errs.add(at(i, 547, 547), "direct sales indicator must be 1 or blank")
	}
	for _, f := range [][2]int{{723, 734}, {735, 746}} {
		if !fireAllDigit.MatchString(pos(b, f[0], f[1])) {
			errs.add(at(i, f[0], f[1]), "state and local tax withheld must be zero-filled digits")
		}
	}
	if cf := pos(b, 747, 748); cf != "  " && !fireAllDigit.MatchString(cf) {
		errs.add(at(i, 747, 748), "combined federal/state code must be two digits or blank")
	}
}

func validateFIRERecordC(errs *fieldErrs, c string, i, group int, totals [len(fireAmountCodes)]int64, at func(int, int, int) string) {
	if pos(c, 2, 9) != fmt.Sprintf("%08d", group) {
		errs.add(at(i, 2, 9), "number of payees %q, the issuer has %d B records", pos(c, 2, 9), group)
	}
	for k := 0; k < len(fireAmountCodes); k++ {
		from := 16 + 18*k
		v := pos(c, from, from+17)
		if !fireAllDigit.MatchString(v) {
			errs.add(at(i, from, from+17), "control total %c must be 18 digits", fireAmountCodes[k])
			continue
		}
		if n, _ := strconv.ParseInt(v, 10, 64); n != totals[k] {
			errs.add(at(i, from, from+17), "control total %c is %d cents, the B records sum to %d", fireAmountCodes[k], n, totals[k])
		}
	}
}

func checkFIRETIN(errs *fieldErrs, tin, field string) {
	if !fireTIN.MatchString(tin) || strings.Count(tin, tin[:1]) == len(tin) {
		errs.add(field, "TIN %q must be nine digits and not one repeated digit", tin)
	}
}

func required(errs *fieldErrs, rec string, i int, at func(int, int, int) string, fields [][2]int) {
	for _, f := range fields {
		if v := pos(rec, f[0], f[1]); strings.TrimSpace(v) == "" || v[0] == ' ' {
			errs.add(at(i, f[0], f[1]), "required, left justified")
		}
	}
}
