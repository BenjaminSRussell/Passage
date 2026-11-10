// Package gs1 parses GS1 element strings from GS1-128, GS1 DataMatrix and GS1
// QR scans (BR-32.1): application identifiers, mod-10 check digits, YYMMDD
// dates with the GS1 century window, and implied-decimal measures. It is pure
// and has no I/O.
package gs1

import (
	"strconv"
	"strings"
	"time"
)

// GS is the group separator that stands for FNC1 inside an element string.
const GS = "\x1d"

const (
	CodeNoMatch    = "MOB-0101"
	CodeCheckDigit = "MOB-0102"
	CodeMalformed  = "MOB-0103"
	CodeUnknownAI  = "MOB-0103"
	CodeBadDate    = "MOB-0104"
)

// Error is one parse failure; Position counts from the start of the scan
// including any symbology identifier.
type Error struct {
	Code     string `json:"code"`
	AI       string `json:"ai,omitempty"`
	Position int    `json:"position"`
	Message  string `json:"message"`
}

// Result is a parsed element string. Dates are YYYY-MM-DD and measures are
// decimal strings. Order holds the AIs in the order they were read.
type Result struct {
	Symbology string            `json:"symbology,omitempty"`
	Fields    map[string]string `json:"fields"`
	Order     []string          `json:"order"`
	Errors    []Error           `json:"errors"`
	OK        bool              `json:"ok"`
}

// Options tune a parse. A zero Now means the current time.
type Options struct {
	FNC1Substitute string
	Now            time.Time
}

type charset int

const (
	numeric charset = iota
	set82
)

type kind int

const (
	fixed kind = iota
	fixedDate
	variable
)

type def struct {
	kind    kind
	length  int
	charset charset
	check   bool
}

var table = buildTable()

func buildTable() map[string]def {
	t := map[string]def{
		"00":  {fixed, 18, numeric, true},
		"01":  {fixed, 14, numeric, true},
		"02":  {fixed, 14, numeric, true},
		"10":  {variable, 20, set82, false},
		"11":  {fixedDate, 6, numeric, false},
		"12":  {fixedDate, 6, numeric, false},
		"13":  {fixedDate, 6, numeric, false},
		"15":  {fixedDate, 6, numeric, false},
		"16":  {fixedDate, 6, numeric, false},
		"17":  {fixedDate, 6, numeric, false},
		"20":  {fixed, 2, numeric, false},
		"21":  {variable, 20, set82, false},
		"30":  {variable, 8, numeric, false},
		"37":  {variable, 8, numeric, false},
		"240": {variable, 30, set82, false},
		"241": {variable, 30, set82, false},
		"250": {variable, 30, set82, false},
		"251": {variable, 30, set82, false},
		"400": {variable, 30, set82, false},
		"410": {fixed, 13, numeric, true},
		"411": {fixed, 13, numeric, true},
		"412": {fixed, 13, numeric, true},
		"413": {fixed, 13, numeric, true},
		"414": {fixed, 13, numeric, true},
		"420": {variable, 20, set82, false},
	}
	for i := 91; i <= 99; i++ {
		t[strconv.Itoa(i)] = def{variable, 90, set82, false}
	}
	return t
}

var measureFamilies = map[string]bool{
	"310": true, "311": true, "312": true, "313": true, "314": true, "315": true, "316": true,
	"320": true, "321": true, "322": true, "323": true, "324": true, "325": true,
}

const set82Specials = "!\"%&'()*+,-./:;<=>?_"

// GS1Symbologies carry GS1 element strings: GS1-128, GS1 DataMatrix, GS1 QR.
var GS1Symbologies = []string{"C1", "d2", "Q3"}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// InSet82 reports whether s is within GS1 AI encodable character set 82.
func InSet82(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isDigit(c) && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && strings.IndexByte(set82Specials, c) < 0 {
			return false
		}
	}
	return true
}

// Mod10Valid verifies a GS1 check digit: weights 3 and 1 from the right,
// excluding the check digit itself.
func Mod10Valid(digits string) bool {
	if len(digits) < 2 || !allDigits(digits) {
		return false
	}
	payload := digits[:len(digits)-1]
	sum := 0
	for i := 0; i < len(payload); i++ {
		d := int(payload[len(payload)-1-i] - '0')
		if i%2 == 0 {
			sum += d * 3
		} else {
			sum += d
		}
	}
	return (10-sum%10)%10 == int(digits[len(digits)-1]-'0')
}

// ToGTIN14 pads a GTIN-8/12/13 to GTIN-14 for lookup.
func ToGTIN14(gtin string) string {
	if len(gtin) >= 14 {
		return gtin
	}
	return strings.Repeat("0", 14-len(gtin)) + gtin
}

func daysInMonth(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// ExpandYear expands YY into the year within -49/+50 of the reference year.
func ExpandYear(yy, refYear int) int {
	start := refYear - 49
	offset := (((yy - start%100) % 100) + 100) % 100
	return start + offset
}

// ParseDate converts YYMMDD to YYYY-MM-DD; DD=00 means the last day of the month.
func ParseDate(yymmdd string, refYear int) (string, bool) {
	yy, _ := strconv.Atoi(yymmdd[0:2])
	mm, _ := strconv.Atoi(yymmdd[2:4])
	dd, _ := strconv.Atoi(yymmdd[4:6])
	if mm < 1 || mm > 12 {
		return "", false
	}
	year := ExpandYear(yy, refYear)
	if dd == 0 {
		dd = daysInMonth(year, mm)
	}
	if dd < 1 || dd > daysInMonth(year, mm) {
		return "", false
	}
	return time.Date(year, time.Month(mm), dd, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), true
}

// ImplyDecimals applies the implied decimal point of a measure AI.
func ImplyDecimals(digits string, decimals int) string {
	trim := func(s string) string {
		t := strings.TrimLeft(s, "0")
		if t == "" && s != "" {
			return "0"
		}
		return t
	}
	if decimals == 0 {
		return trim(digits)
	}
	return trim(digits[:len(digits)-decimals]) + "." + digits[len(digits)-decimals:]
}

func hasSymbologyID(raw string) bool {
	if len(raw) < 3 || raw[0] != ']' {
		return false
	}
	c := raw[1]
	if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
		return false
	}
	m := raw[2]
	return isDigit(m) || (m >= 'A' && m <= 'Z') || (m >= 'a' && m <= 'z')
}

type matched struct {
	ai      string
	def     def
	measure bool
}

func matchAI(s string, pos int) (matched, bool) {
	rest := s[pos:]
	if len(rest) >= 4 && measureFamilies[rest[:3]] && rest[3] >= '0' && rest[3] <= '5' {
		return matched{ai: rest[:4], measure: true}, true
	}
	for _, n := range []int{3, 2} {
		if len(rest) < n {
			continue
		}
		if d, ok := table[rest[:n]]; ok {
			return matched{ai: rest[:n], def: d}, true
		}
	}
	return matched{}, false
}

// StartsWithAI reports whether raw begins with a known AI, which is how an
// unprefixed GS1 string is recognised.
func StartsWithAI(raw string) bool {
	_, ok := matchAI(raw, 0)
	return ok
}

// Parse parses a GS1 element string, with or without a symbology identifier.
// Check-digit, date and charset failures are collected rather than returned
// one at a time, so a device can show every problem at once.
func Parse(raw string, opts Options) Result {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	refYear := now.UTC().Year()
	body, offset, symbology := raw, 0, ""
	if hasSymbologyID(raw) {
		symbology, body, offset = raw[1:3], raw[3:], 3
	}
	s := body
	if sub := opts.FNC1Substitute; sub != "" && sub != GS {
		s = strings.ReplaceAll(s, sub, GS)
	}
	res := Result{Symbology: symbology, Fields: map[string]string{}, Order: []string{}, Errors: []Error{}, OK: true}
	fail := func(code, ai string, at int, msg string) {
		res.Errors = append(res.Errors, Error{Code: code, AI: ai, Position: at, Message: msg})
		res.OK = false
	}
	seen := map[string]string{}
	set := func(ai, value, rawValue string, at int) {
		if prev, dup := seen[ai]; dup {
			if prev != rawValue {
				fail(CodeMalformed, ai, at, "AI "+ai+" appears twice with different values.")
			}
			return
		}
		seen[ai] = rawValue
		res.Fields[ai] = value
		res.Order = append(res.Order, ai)
	}
	if strings.ReplaceAll(s, GS, "") == "" {
		fail(CodeMalformed, "", offset, "The barcode holds no GS1 element string.")
		return res
	}
	pos := 0
	for pos < len(s) {
		if s[pos:pos+1] == GS {
			pos++
			continue
		}
		at := pos + offset
		m, ok := matchAI(s, pos)
		if !ok {
			fail(CodeUnknownAI, "", at, "Unknown GS1 application identifier at position "+strconv.Itoa(at)+".")
			break
		}
		pos += len(m.ai)
		if m.measure {
			end := min(pos+6, len(s))
			data := s[pos:end]
			pos = end
			if len(data) != 6 || !allDigits(data) {
				fail(CodeMalformed, m.ai, at, "AI "+m.ai+" needs 6 digits.")
				break
			}
			set(m.ai, ImplyDecimals(data, int(m.ai[3]-'0')), data, at)
			continue
		}
		d := m.def
		if d.kind == variable {
			end := strings.Index(s[pos:], GS)
			value := s[pos:]
			if end >= 0 {
				value = s[pos : pos+end]
			}
			pos += len(value)
			if value == "" || len(value) > d.length {
				fail(CodeMalformed, m.ai, at, "AI "+m.ai+" must be 1 to "+strconv.Itoa(d.length)+" characters.")
				continue
			}
			if (d.charset == numeric && !allDigits(value)) || (d.charset == set82 && !InSet82(value)) {
				fail(CodeMalformed, m.ai, at, "AI "+m.ai+" has characters outside its charset.")
				continue
			}
			set(m.ai, value, value, at)
			continue
		}
		end := min(pos+d.length, len(s))
		value := s[pos:end]
		pos = end
		if len(value) != d.length || !allDigits(value) {
			fail(CodeMalformed, m.ai, at, "AI "+m.ai+" needs "+strconv.Itoa(d.length)+" digits.")
			break
		}
		if d.kind == fixedDate {
			iso, valid := ParseDate(value, refYear)
			if !valid {
				fail(CodeBadDate, m.ai, at, "Invalid date "+value+" for AI "+m.ai+".")
				continue
			}
			set(m.ai, iso, value, at)
			continue
		}
		if d.check && !Mod10Valid(value) {
			fail(CodeCheckDigit, m.ai, at, "Check digit invalid for AI "+m.ai+".")
			continue
		}
		set(m.ai, value, value, at)
	}
	return res
}
