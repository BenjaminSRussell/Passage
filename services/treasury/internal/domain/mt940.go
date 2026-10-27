package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// FormatMT940 is the Statement.Format of a parsed MT940 statement.
const FormatMT940 = "MT940"

var (
	mt940TagRe     = regexp.MustCompile(`^:(\d{2}[A-Z]?):(.*)$`)
	mt940BalanceRe = regexp.MustCompile(`^([CD])(\d{6})([A-Z]{3})(\d+(?:,\d*)?)$`)
	// :61: value date, optional entry date MMDD, mark (C, D, RC, RD), optional
	// funds code, amount with decimal comma, transaction type (N, F, S) and
	// identification code, then the reference part.
	mt940EntryRe = regexp.MustCompile(`^(\d{6})(\d{4})?(RC|RD|C|D)([A-Z])?(\d+,\d*)([NFS])([A-Z0-9]{3})(.*)$`)
	mt940SubRe   = regexp.MustCompile(`\?(\d{2})`)
)

// mt940Field is one tagged field; continuation lines are joined with newlines.
type mt940Field struct {
	tag   string
	value string
	line  int
}

// ParseMT940 parses SWIFT MT940 customer statements into one Statement per :20:
// block (FR-36.2). Tags :20:, :25:, :28C:, :60F:/:60M:, :61:, :86: and
// :62F:/:62M: are read; other tags are ignored. Errors are TRS-0493 with the
// line number. Debits (D) and credit reversals (RC) are negative; credits (C)
// and debit reversals (RD) are positive. The :86: block after a :61: is the
// line's remittance reference (structured ?20-?29 subfields, or the whole
// text) and, when structured, gives the counterparty (?32/?33). Banks reuse
// :20: across statements, so the statement id is :20:/:28C: when :28C: is
// present.
func ParseMT940(data []byte) ([]Statement, error) {
	fields, err := mt940Fields(data)
	if err != nil {
		return nil, err
	}
	var out []Statement
	var cur *Statement
	var ref string
	var refLine int
	finish := func() error {
		if cur == nil {
			return nil
		}
		if cur.OpeningDate == "" || cur.ClosingDate == "" {
			return formatError(refLine, "statement "+ref+" needs :60F:/:60M: and :62F:/:62M: balances")
		}
		if cur.Account == "" {
			return formatError(refLine, "statement "+ref+" needs a :25: account identification")
		}
		if err := CheckBalances(*cur); err != nil {
			return err
		}
		out = append(out, *cur)
		cur = nil
		return nil
	}
	for _, f := range fields {
		if f.tag == "20" {
			if err := finish(); err != nil {
				return nil, err
			}
			ref, refLine = strings.TrimSpace(f.value), f.line
			if ref == "" {
				return nil, formatError(f.line, ":20: transaction reference is required")
			}
			cur = &Statement{Format: FormatMT940, StatementID: ref}
			continue
		}
		if cur == nil {
			return nil, formatError(f.line, ":"+f.tag+": appears before :20:")
		}
		if err := cur.applyMT940Field(f); err != nil {
			return nil, err
		}
	}
	if err := finish(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, formatError(0, "no :20: statement in the MT940 file")
	}
	return out, nil
}

// mt940Fields reads tagged fields and drops the SWIFT block delimiters
// ({1:...}{2:...}{4: and -}).
func mt940Fields(data []byte) ([]mt940Field, error) {
	var out []mt940Field
	for i, raw := range strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n") {
		no := i + 1
		line := strings.TrimRight(raw, " \t")
		if m := mt940TagRe.FindStringSubmatch(line); m != nil {
			out = append(out, mt940Field{tag: m[1], value: m[2], line: no})
			continue
		}
		if strings.TrimSpace(line) == "" || line == "-" || line == "-}" || strings.HasPrefix(line, "{") {
			continue
		}
		if len(out) == 0 {
			return nil, formatError(no, "text before the first tag")
		}
		last := &out[len(out)-1]
		last.value += "\n" + line
	}
	return out, nil
}

func (st *Statement) applyMT940Field(f mt940Field) error {
	switch f.tag {
	case "25":
		// 35x: an IBAN, or bank code/account number as the bank writes it.
		st.Account = NormalizeIBAN(f.value)
	case "28C", "28":
		num, _, _ := strings.Cut(strings.TrimSpace(f.value), "/")
		n, err := strconv.Atoi(strings.TrimSpace(num))
		if err != nil || n < 0 {
			return formatError(f.line, ":28C: statement number must be a non-negative integer")
		}
		st.Sequence = n
		st.StatementID += "/" + strings.TrimSpace(f.value)
	case "60F", "60M":
		date, ccy, amt, err := mt940Balance(f.value)
		if err != nil {
			return formatError(f.line, ":60: "+err.Error())
		}
		st.OpeningDate, st.OpeningAmount, st.Currency = date, amt, ccy
	case "62F", "62M":
		if st.OpeningDate == "" {
			return formatError(f.line, ":62: appears before the :60: opening balance")
		}
		date, ccy, amt, err := mt940Balance(f.value)
		if err != nil {
			return formatError(f.line, ":62: "+err.Error())
		}
		if ccy != st.Currency {
			return formatError(f.line, fmt.Sprintf(":62: currency %s differs from the opening currency %s", ccy, st.Currency))
		}
		st.ClosingDate, st.ClosingAmount = date, amt
	case "61":
		if len(st.Lines) >= MaxEntries {
			return formatError(f.line, fmt.Sprintf("more than %d entries", MaxEntries))
		}
		line, err := st.mt940Entry(f)
		if err != nil {
			return err
		}
		st.Lines = append(st.Lines, line)
	case "86":
		if len(st.Lines) == 0 {
			return nil // statement-level information, not tied to an entry
		}
		applyMT940Info(&st.Lines[len(st.Lines)-1], f.value)
	}
	return nil
}

// mt940Balance reads :60: and :62: values: D/C, YYMMDD, currency, amount with a comma.
func mt940Balance(v string) (string, string, decimal.Decimal, error) {
	m := mt940BalanceRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return "", "", decimal.Zero, fmt.Errorf("%q is not D|C YYMMDD CCY amount", v)
	}
	date, err := yymmdd(m[2])
	if err != nil {
		return "", "", decimal.Zero, err
	}
	amt, err := mt940Amount(m[4])
	if err != nil {
		return "", "", decimal.Zero, err
	}
	if m[1] == "D" {
		amt = amt.Neg()
	}
	return date, m[3], amt, nil
}

// mt940Entry reads one :61: line. Only the first line of the value is the
// statement line; a second line holds supplementary details, kept only in
// the raw fragment.
func (st *Statement) mt940Entry(f mt940Field) (Line, error) {
	if st.Currency == "" {
		return Line{}, formatError(f.line, ":61: appears before the :60: opening balance")
	}
	first, _, _ := strings.Cut(f.value, "\n")
	m := mt940EntryRe.FindStringSubmatch(strings.TrimSpace(first))
	if m == nil {
		return Line{}, formatError(f.line, ":61: entry does not match YYMMDD[MMDD](C|D|RC|RD)[funds]amount(N|F|S)code")
	}
	value, err := yymmdd(m[1])
	if err != nil {
		return Line{}, formatError(f.line, ":61: value "+err.Error())
	}
	booking := value
	if m[2] != "" {
		if booking, err = entryDate(m[1], m[2]); err != nil {
			return Line{}, formatError(f.line, ":61: entry "+err.Error())
		}
	}
	amt, err := mt940Amount(m[5])
	if err != nil {
		return Line{}, formatError(f.line, ":61: "+err.Error())
	}
	if m[3] == "D" || m[3] == "RC" {
		amt = amt.Neg()
	}
	customer, bank, _ := strings.Cut(m[8], "//")
	customer = strings.TrimSpace(customer)
	if customer == "NONREF" {
		customer = ""
	}
	return Line{
		LineNo:      len(st.Lines) + 1,
		BookingDate: booking,
		ValueDate:   value,
		Amount:      amt,
		Currency:    st.Currency,
		BTCCode:     "MT940/" + m[6] + m[7],
		EndToEndRef: customer,
		Reference:   strings.TrimSpace(bank),
		RawFragment: ":61:" + f.value,
	}, nil
}

// entryDate gives the :61: entry date (MMDD) the year closest to the value
// date, so an entry booked on 2 January for a 31 December value date falls in
// the next year, and the reverse in the year before.
func entryDate(valueYYMMDD, mmdd string) (string, error) {
	year, _ := strconv.Atoi("20" + valueYYMMDD[:2])
	valueMonth, _ := strconv.Atoi(valueYYMMDD[2:4])
	month, _ := strconv.Atoi(mmdd[:2])
	switch {
	case valueMonth == 12 && month == 1:
		year++
	case valueMonth == 1 && month == 12:
		year--
	}
	d := fmt.Sprintf("%04d-%s-%s", year, mmdd[:2], mmdd[2:])
	if !validDate(d) {
		return "", fmt.Errorf("date %q is not a calendar date", mmdd)
	}
	return d, nil
}

// applyMT940Info reads a :86: block. Structured blocks (German ?nn subfields)
// give the reference from ?20-?29 and the counterparty from ?32/?33; an
// unstructured block is the reference as a whole. Either replaces the bank
// reference of :61:, because the remittance text is what matching reads.
func applyMT940Info(l *Line, v string) {
	l.RawFragment += "\n:86:" + v
	idx := mt940SubRe.FindAllStringSubmatchIndex(strings.ReplaceAll(v, "\n", ""), -1)
	if len(idx) == 0 {
		if t := strings.Join(strings.Fields(v), " "); t != "" {
			l.Reference = t
		}
		return
	}
	v = strings.ReplaceAll(v, "\n", "") // structured subfields wrap at 65 characters mid-word
	var ref, name strings.Builder
	for i, loc := range idx {
		end := len(v)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		code, _ := strconv.Atoi(v[loc[2]:loc[3]])
		text := v[loc[1]:end]
		switch {
		case code >= 20 && code <= 29:
			ref.WriteString(text)
		case code == 32 || code == 33:
			name.WriteString(text)
		}
	}
	if r := strings.TrimSpace(ref.String()); r != "" {
		l.Reference = r
	}
	if n := strings.TrimSpace(name.String()); n != "" {
		l.CounterpartyName = n
	}
}

// mt940Amount reads a SWIFT amount with a decimal comma and no thousands separator.
func mt940Amount(s string) (decimal.Decimal, error) {
	v, err := decimal.NewFromString(strings.ReplaceAll(strings.TrimSpace(s), ",", "."))
	if err != nil || v.IsNegative() {
		return decimal.Zero, fmt.Errorf("amount %q must be a non-negative decimal", s)
	}
	return v, nil
}
