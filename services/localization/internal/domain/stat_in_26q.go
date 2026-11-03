package domain

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// IN_26Q: the quarterly TDS statement (Form 26Q, non-salary) as the
// caret-delimited text file the NSDL/Protean FVU validates. Records, each
// starting with a running line number and a record type:
//
//	FH file header:      line^FH^NS1^R^creationDate^fileSeq^D^TAN^batchCount
//	BH batch header:     line^BH^batchNo^challanCount^deducteeCount^26Q^TAN^deductorPAN^FY^quarter^totalTDS
//	CD challan detail:   line^CD^batchNo^challanSerial^BSRcode^depositDate^challanNo^tdsDeposited
//	DD deductee detail:  line^DD^batchNo^challanSerial^deducteeSerial^PAN^name^section^paymentDate^amountPaid^tdsDeducted
//
// Dates are DDMMYYYY. FVU prescribes more columns per record than are listed;
// these are the ones that carry identity, period and money. Field rules: TAN
// four letters, five digits, a letter; PAN five letters, four digits, a
// letter with a valid holder-type fourth letter (a deductee may instead carry
// PANAPPLIED or PANNOTAVBL); BSR code seven digits; challan serial five
// digits; financial year YYYY-YY and consecutive; quarter Q1 to Q4 (April
// start); payment dates inside the quarter; deposit dates not before it.
// Totals: counts in FH and BH match the records, and per challan the deducted
// TDS equals the deposited TDS, which sums to the batch total.

var (
	tan26Q        = regexp.MustCompile(`^[A-Z]{4}\d{5}[A-Z]$`)
	pan26Q        = regexp.MustCompile(`^[A-Z]{3}[PCHFATBLJG][A-Z]\d{4}[A-Z]$`)
	bsr26Q        = regexp.MustCompile(`^\d{7}$`)
	challanNo26Q  = regexp.MustCompile(`^\d{5}$`)
	fy26Q         = regexp.MustCompile(`^(\d{4})-(\d{2})$`)
	section26Q    = regexp.MustCompile(`^(193|194[A-Z]{0,2}|196[A-D])$`)
	quarterStart  = map[string]time.Month{"Q1": time.April, "Q2": time.July, "Q3": time.October, "Q4": time.January}
	panPlaceholds = map[string]bool{"PANAPPLIED": true, "PANNOTAVBL": true}
)

const (
	date26Q      = "02012006"
	deducteeName = 75
)

type chal26Q struct {
	deposited decimal.Decimal
	deducted  decimal.Decimal
}

type batch26Q struct {
	no, challans, deductees int
	tan, quarter            string
	total                   decimal.Decimal
	start                   time.Time
	cd                      map[string]*chal26Q
	nCD, nDD                int
	sumCD                   decimal.Decimal
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
		want := map[string]int{"FH": 9, "BH": 11, "CD": 8, "DD": 11}[c[1]]
		if want == 0 || len(c) != want {
			errs.add(at("record"), "%q record must be FH, BH, CD or DD with the layout's column count", c[1])
			continue
		}
		if (i == 0) != (c[1] == "FH") {
			errs.add(at("record"), "FH must be the first line and appear once")
			continue
		}
		switch c[1] {
		case "FH":
			if c[2] != "NS1" || c[3] != "R" || c[6] != "D" {
				errs.add(at("header"), "must carry NS1, R and D in columns 3, 4 and 7")
			}
			if _, ok := validDate(date26Q, c[4]); !ok {
				errs.add(at("creationDate"), "%q must be DDMMYYYY", c[4])
			}
			if !tan26Q.MatchString(c[7]) {
				errs.add(at("tan"), "%q is not a valid TAN", c[7])
			}
			fhTAN = c[7]
			fhBatches, _ = strconv.Atoi(c[8])
		case "BH":
			cur = header26Q(&errs, at, c)
			batches = append(batches, cur)
		case "CD", "DD":
			if cur == nil || c[2] != strconv.Itoa(cur.no) {
				errs.add(at("batchNo"), "%q does not match the open batch", c[2])
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
		if b.nDD != b.deductees {
			errs.add("BH.deducteeCount", "%d does not match the %d deductee records", b.deductees, b.nDD)
		}
		if !b.sumCD.Equal(b.total) {
			errs.add("BH.totalTDS", "%s does not equal the deposited TDS %s", b.total, b.sumCD)
		}
		for serial, ch := range b.cd {
			if !ch.deducted.Equal(ch.deposited) {
				errs.add("CD"+serial, "deducted TDS %s does not equal the deposited %s", ch.deducted, ch.deposited)
			}
		}
	}
	return errs
}

func header26Q(errs *fieldErrs, at func(string) string, c []string) *batch26Q {
	b := &batch26Q{cd: map[string]*chal26Q{}, tan: c[6], quarter: c[9]}
	b.no, _ = strconv.Atoi(c[2])
	b.challans, _ = strconv.Atoi(c[3])
	b.deductees, _ = strconv.Atoi(c[4])
	if b.no < 1 {
		errs.add(at("batchNo"), "%q must be a positive number", c[2])
	}
	if c[5] != "26Q" {
		errs.add(at("form"), "%q must be 26Q", c[5])
	}
	if !tan26Q.MatchString(c[6]) {
		errs.add(at("tan"), "%q is not a valid TAN", c[6])
	}
	if !pan26Q.MatchString(c[7]) {
		errs.add(at("deductorPAN"), "%q is not a valid PAN", c[7])
	}
	m := fy26Q.FindStringSubmatch(c[8])
	month, quarterOK := quarterStart[c[9]]
	if !quarterOK {
		errs.add(at("quarter"), "%q must be Q1, Q2, Q3 or Q4", c[9])
	}
	if m == nil || (atoi(m[1])+1)%100 != atoi(m[2]) {
		errs.add(at("financialYear"), "%q must be YYYY-YY over consecutive years", c[8])
	} else if quarterOK {
		year := atoi(m[1])
		if month == time.January {
			year++
		}
		b.start = time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	}
	var ok bool
	if b.total, ok = parseAmount(c[10], twoDecimals); !ok || b.total.IsNegative() {
		errs.add(at("totalTDS"), "%q must be rupees with at most two decimals", c[10])
	}
	return b
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func challan26Q(errs *fieldErrs, at func(string) string, c []string, b *batch26Q) {
	b.nCD++
	if !bsr26Q.MatchString(c[4]) {
		errs.add(at("bsrCode"), "%q must be seven digits", c[4])
	}
	if d, ok := validDate(date26Q, c[5]); !ok {
		errs.add(at("depositDate"), "%q must be DDMMYYYY", c[5])
	} else if !b.start.IsZero() && d.Before(b.start) {
		errs.add(at("depositDate"), "%q is before the quarter starts", c[5])
	}
	if !challanNo26Q.MatchString(c[6]) {
		errs.add(at("challanNo"), "%q must be five digits", c[6])
	}
	amt, ok := parseAmount(c[7], twoDecimals)
	if !ok || amt.IsNegative() {
		errs.add(at("tdsDeposited"), "%q must be rupees with at most two decimals", c[7])
		return
	}
	if _, dup := b.cd[c[3]]; dup {
		errs.add(at("challanSerial"), "%q repeats", c[3])
		return
	}
	b.cd[c[3]] = &chal26Q{deposited: amt}
	b.sumCD = b.sumCD.Add(amt)
}

func deductee26Q(errs *fieldErrs, at func(string) string, c []string, b *batch26Q) {
	b.nDD++
	ch := b.cd[c[3]]
	if ch == nil {
		errs.add(at("challanSerial"), "%q matches no challan record before it", c[3])
	}
	if !pan26Q.MatchString(c[5]) && !panPlaceholds[c[5]] {
		errs.add(at("pan"), "%q must be a valid PAN, PANAPPLIED or PANNOTAVBL", c[5])
	}
	if c[6] == "" || len(c[6]) > deducteeName {
		errs.add(at("name"), "is required, at most %d characters", deducteeName)
	}
	if !section26Q.MatchString(c[7]) {
		errs.add(at("section"), "%q is not a non-salary TDS section", c[7])
	}
	if d, ok := validDate(date26Q, c[8]); !ok {
		errs.add(at("paymentDate"), "%q must be DDMMYYYY", c[8])
	} else if !b.start.IsZero() && (d.Before(b.start) || !d.Before(b.start.AddDate(0, 3, 0))) {
		errs.add(at("paymentDate"), "%q is outside the quarter %s", c[8], b.quarter)
	}
	paid, okP := parseAmount(c[9], twoDecimals)
	tds, okT := parseAmount(c[10], twoDecimals)
	switch {
	case !okP || paid.IsNegative():
		errs.add(at("amountPaid"), "%q must be rupees with at most two decimals", c[9])
	case !okT || tds.IsNegative():
		errs.add(at("tdsDeducted"), "%q must be rupees with at most two decimals", c[10])
	case tds.GreaterThan(paid):
		errs.add(at("tdsDeducted"), "%s exceeds the amount paid %s", tds, paid)
	case ch != nil:
		ch.deducted = ch.deducted.Add(tds)
	}
}
