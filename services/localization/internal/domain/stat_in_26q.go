package domain

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// IN_26Q: the quarterly TDS statement (Form 26Q, non-salary) as the
// caret-delimited text file the NSDL/Protean FVU validates. Every line starts
// with a running line number (column 1) and a record type (column 2). Columns
// are numbered from 1 and sit at the FVU positions:
//
//	FH (18 columns) 3 NS1, 4 R, 5 creation date, 7 D, 8 TAN, 9 batch count
//	BH (47+)        3 batch no, 4 challan count, 5 form 26Q, 13 TAN, 15 deductor
//	                PAN (or PANNOTREQD), 16 assessment year, 17 financial year,
//	                18 quarter, 47 total deposit as per challans
//	CD (39+)        4 challan serial, 13 challan no, 17 BSR code, 19 deposit
//	                date, 22 TDS, 23 surcharge, 24 cess, 25 interest, 26 fee,
//	                27 total deposit = 22+23+24+25+26+39, 39 penalty and others
//	DD (33+)        10 deductee PAN, 13 name, 22 amount paid, 23 payment date,
//	                33 section code
//
// A DD record belongs to the CD record above it. Dates are DDMMYYYY; years are
// YYYYYY ("202526"). Rules: TAN four letters, five digits, a letter; PAN five
// letters, four digits, a letter with a valid holder-type fourth letter (a
// deductee may instead carry PANAPPLIED, PANNOTAVBL or PANINVALID); BSR code
// seven digits; challan no five digits; challan and batch amounts in whole
// rupees; section codes the three-character FVU codes of the 26Q annexure;
// payment dates inside the quarter; deposit dates on or after 1 April of the
// year before the financial year. Totals: FH batch count, BH challan count and
// BH total match the records.

var (
	tan26Q        = regexp.MustCompile(`^[A-Z]{4}\d{5}[A-Z]$`)
	pan26Q        = regexp.MustCompile(`^[A-Z]{3}[PCHFATBLJG][A-Z]\d{4}[A-Z]$`)
	bsr26Q        = regexp.MustCompile(`^\d{7}$`)
	challanNo26Q  = regexp.MustCompile(`^\d{5}$`)
	year26Q       = regexp.MustCompile(`^(\d{4})(\d{2})$`)
	count26Q      = regexp.MustCompile(`^\d{1,9}$`)
	quarterStart  = map[string]time.Month{"Q1": time.April, "Q2": time.July, "Q3": time.October, "Q4": time.January}
	panPlaceholds = map[string]bool{"PANAPPLIED": true, "PANNOTAVBL": true, "PANINVALID": true}
	section26Q    = map[string]bool{
		"193": true, "194": true, "94A": true, "94B": true, "4BB": true, "94C": true, "94D": true,
		"4EE": true, "94F": true, "94G": true, "94H": true, "4IA": true, "4IB": true, "94J": true,
		"94L": true, "4BA": true, "4DA": true, "2AA": true, "LBB": true, "9IA": true, "LBC": true, "4IC": true,
	}
	record26Q = map[string]int{"FH": 18, "BH": 47, "CD": 39, "DD": 33}
	oneRupee  = decimal.NewFromInt(1)
)

const (
	date26Q      = "02012006"
	deducteeName = 75
	panNotReqd   = "PANNOTREQD"
)

type batch26Q struct {
	no, challans, nCD int
	tan, quarter      string
	total, sumCD      decimal.Decimal
	start             time.Time
}

func col26Q(c []string, n int) string { return c[n-1] }

func rupees(s string) (decimal.Decimal, bool) {
	d, ok := parseAmount(s, twoDecimals)
	return d, ok && !d.IsNegative() && d.Mod(oneRupee).IsZero()
}

func count(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, count26Q.MatchString(s) && err == nil
}

// ValidateForm26Q checks a 26Q file; it returns every failure.
func ValidateForm26Q(raw []byte) []FieldError {
	var errs fieldErrs
	text := strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if strings.TrimSpace(text) == "" {
		errs.add("file", "is empty")
		return errs
	}
	var fhTAN string
	var fhBatches int
	var batches []*batch26Q
	var cur *batch26Q
	for i, line := range strings.Split(text, "\n") {
		at := func(col string) string { return "line" + strconv.Itoa(i+1) + "." + col }
		c := strings.Split(line, "^")
		if len(c) < 2 || c[0] != strconv.Itoa(i+1) {
			errs.add(at("lineNo"), "line numbers must run 1, 2, 3 in order")
			continue
		}
		want := record26Q[c[1]]
		if want == 0 || len(c) < want || (c[1] == "FH" && len(c) != want) {
			errs.add(at("record"), "%q record must be FH, BH, CD or DD with at least the layout's %d columns (FH exactly 18)", c[1], want)
			continue
		}
		if (i == 0) != (c[1] == "FH") {
			errs.add(at("record"), "FH must be the first line and appear once")
			continue
		}
		switch c[1] {
		case "FH":
			if col26Q(c, 3) != "NS1" || col26Q(c, 4) != "R" || col26Q(c, 7) != "D" {
				errs.add(at("header"), "must carry NS1, R and D in columns 3, 4 and 7")
			}
			if _, ok := validDate(date26Q, col26Q(c, 5)); !ok {
				errs.add(at("creationDate"), "%q must be DDMMYYYY", col26Q(c, 5))
			}
			if fhTAN = col26Q(c, 8); !tan26Q.MatchString(fhTAN) {
				errs.add(at("tan"), "%q is not a valid TAN", fhTAN)
			}
			var ok bool
			if fhBatches, ok = count(col26Q(c, 9)); !ok {
				errs.add(at("batchCount"), "%q must be a count", col26Q(c, 9))
			}
		case "BH":
			cur = header26Q(&errs, at, c)
			batches = append(batches, cur)
		default:
			if cur == nil || col26Q(c, 3) != strconv.Itoa(cur.no) {
				errs.add(at("batchNo"), "%q does not match the open batch", col26Q(c, 3))
				continue
			}
			if c[1] == "CD" {
				challan26Q(&errs, at, c, cur)
			} else {
				deductee26Q(&errs, at, c, cur)
			}
		}
	}
	if len(batches) == 0 {
		errs.add("batches", "the file has no batch header")
	}
	if fhBatches != len(batches) {
		errs.add("FH.batchCount", "%d does not match the %d batch headers", fhBatches, len(batches))
	}
	for _, b := range batches {
		if b.tan != fhTAN {
			errs.add("BH.tan", "%q differs from the file header TAN %q", b.tan, fhTAN)
		}
		if b.nCD != b.challans {
			errs.add("BH.challanCount", "%d does not match the %d challan records", b.challans, b.nCD)
		}
		if !b.sumCD.Equal(b.total) {
			errs.add("BH.totalDeposit", "%s does not equal the deposited total %s", b.total, b.sumCD)
		}
	}
	return errs
}

func header26Q(errs *fieldErrs, at func(string) string, c []string) *batch26Q {
	b := &batch26Q{tan: col26Q(c, 13), quarter: col26Q(c, 18)}
	var ok bool
	if b.no, ok = count(col26Q(c, 3)); !ok || b.no < 1 {
		errs.add(at("batchNo"), "%q must be a positive number", col26Q(c, 3))
	}
	if b.challans, ok = count(col26Q(c, 4)); !ok {
		errs.add(at("challanCount"), "%q must be a count", col26Q(c, 4))
	}
	if col26Q(c, 5) != "26Q" {
		errs.add(at("form"), "%q must be 26Q", col26Q(c, 5))
	}
	if !tan26Q.MatchString(b.tan) {
		errs.add(at("tan"), "%q is not a valid TAN", b.tan)
	}
	if pan := col26Q(c, 15); !pan26Q.MatchString(pan) && pan != panNotReqd {
		errs.add(at("deductorPAN"), "%q must be a valid PAN or %s", pan, panNotReqd)
	}
	fy := year26Q.FindStringSubmatch(col26Q(c, 17))
	ay := year26Q.FindStringSubmatch(col26Q(c, 16))
	month, quarterOK := quarterStart[b.quarter]
	if !quarterOK {
		errs.add(at("quarter"), "%q must be Q1, Q2, Q3 or Q4", b.quarter)
	}
	consecutive := func(m []string) bool { return m != nil && (atoi(m[1])+1)%100 == atoi(m[2]) }
	if !consecutive(fy) {
		errs.add(at("financialYear"), "%q must be YYYYYY over consecutive years, e.g. 202526", col26Q(c, 17))
	} else {
		if !consecutive(ay) || atoi(ay[1]) != atoi(fy[1])+1 {
			errs.add(at("assessmentYear"), "%q must be the year after the financial year %s", col26Q(c, 16), col26Q(c, 17))
		}
		if quarterOK {
			year := atoi(fy[1])
			if month == time.January {
				year++
			}
			b.start = time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		}
	}
	if b.total, ok = rupees(col26Q(c, 47)); !ok {
		errs.add(at("totalDeposit"), "%q must be whole rupees", col26Q(c, 47))
	}
	return b
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func challan26Q(errs *fieldErrs, at func(string) string, c []string, b *batch26Q) {
	b.nCD++
	if !bsr26Q.MatchString(col26Q(c, 17)) {
		errs.add(at("bsrCode"), "%q must be seven digits", col26Q(c, 17))
	}
	if d, ok := validDate(date26Q, col26Q(c, 19)); !ok {
		errs.add(at("depositDate"), "%q must be DDMMYYYY", col26Q(c, 19))
	} else if !b.start.IsZero() && d.Before(deposits26QFrom(b.start)) {
		errs.add(at("depositDate"), "%q is before 1 April of the previous financial year", col26Q(c, 19))
	}
	if !challanNo26Q.MatchString(col26Q(c, 13)) {
		errs.add(at("challanNo"), "%q must be five digits", col26Q(c, 13))
	}
	parts := map[int]decimal.Decimal{}
	for _, n := range []int{22, 23, 24, 25, 26, 27, 39} {
		d, ok := rupees(col26Q(c, n))
		if !ok {
			errs.add(at("amount"+strconv.Itoa(n)), "%q must be whole rupees", col26Q(c, n))
			return
		}
		parts[n] = d
	}
	sum := parts[22].Add(parts[23]).Add(parts[24]).Add(parts[25]).Add(parts[26]).Add(parts[39])
	if !sum.Equal(parts[27]) {
		errs.add(at("totalDeposit"), "%s must equal columns 22+23+24+25+26+39 (%s)", parts[27], sum)
	}
	b.sumCD = b.sumCD.Add(parts[27])
}

func deposits26QFrom(qs time.Time) time.Time {
	fyStart := qs
	if qs.Month() == time.January {
		fyStart = qs.AddDate(-1, 3, 0)
	}
	return time.Date(fyStart.Year()-1, time.April, 1, 0, 0, 0, 0, time.UTC)
}

func deductee26Q(errs *fieldErrs, at func(string) string, c []string, b *batch26Q) {
	if b.nCD == 0 {
		errs.add(at("challan"), "has no challan record before it")
	}
	if pan := col26Q(c, 10); !pan26Q.MatchString(pan) && !panPlaceholds[pan] {
		errs.add(at("pan"), "%q must be a valid PAN, PANAPPLIED, PANNOTAVBL or PANINVALID", pan)
	}
	if name := col26Q(c, 13); name == "" || len([]rune(name)) > deducteeName {
		errs.add(at("name"), "is required, at most %d characters", deducteeName)
	}
	if s := col26Q(c, 33); !section26Q[s] {
		errs.add(at("section"), "%q is not a 26Q FVU section code", s)
	}
	if d, ok := validDate(date26Q, col26Q(c, 23)); !ok {
		errs.add(at("paymentDate"), "%q must be DDMMYYYY", col26Q(c, 23))
	} else if !b.start.IsZero() && (d.Before(b.start) || !d.Before(b.start.AddDate(0, 3, 0))) {
		errs.add(at("paymentDate"), "%q is outside the quarter %s", col26Q(c, 23), b.quarter)
	}
	if paid, ok := parseAmount(col26Q(c, 22), twoDecimals); !ok || paid.IsNegative() {
		errs.add(at("amountPaid"), "%q must be rupees with at most two decimals", col26Q(c, 22))
	}
}
