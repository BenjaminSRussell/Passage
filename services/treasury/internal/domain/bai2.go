package domain

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// FormatBAI2 is the Statement.Format of a parsed BAI2 account.
const FormatBAI2 = "BAI2"

// BAI2 type codes used for the account balances (03 record).
const (
	bai2OpeningLedger = "010"
	bai2ClosingLedger = "015"
)

// bai2Record is one logical record: a physical record plus its 88
// continuations. no is the physical line of the record, for error messages.
type bai2Record struct {
	code   string
	fields []string // after the record code
	text   string   // 16 only: the free text, which may hold commas and slashes
	no     int
}

// bai2Account is one 03 account group while its 16 detail records are read.
type bai2Account struct {
	number   string
	currency string
	asOf     string
	opening  *decimal.Decimal
	closing  *decimal.Decimal
	lines    []Line
	no       int
}

// ParseBAI2 parses a BAI2 cash management file into one Statement per 03
// account group (FR-36.2). Each physical line is a record; 88 records continue
// the record before them. Amounts are implied-decimal minor units of the
// account currency (ISO 4217 minor unit, platform/money); the as-of date of the
// account's 02 group is the booking date of its details, and a funds type V
// detail carries its own value date. Structural problems are TRS-0493 with the
// line number. BAI2 has no statement id, so the id is the account number plus
// the as-of date. Control totals (49/98/99) are not checked: the opening +
// details = closing invariant (TRS-0494) is.
func ParseBAI2(data []byte) ([]Statement, error) {
	records, err := bai2Records(data)
	if err != nil {
		return nil, err
	}
	var (
		asOf, groupCcy string
		seenHead       bool
		cur            *bai2Account
		accounts       []*bai2Account
	)
	for _, rec := range records {
		switch rec.code {
		case "01":
			seenHead = true
		case "02":
			if !seenHead {
				return nil, formatError(rec.no, "02 group header before the 01 file header")
			}
			if len(rec.fields) < 4 {
				return nil, formatError(rec.no, "02 group header needs an as-of date")
			}
			d, err := yymmdd(rec.fields[3])
			if err != nil {
				return nil, formatError(rec.no, "as-of "+err.Error())
			}
			asOf, groupCcy, cur = d, "USD", nil // BAI2: a defaulted currency is US dollars
			if len(rec.fields) > 5 && strings.TrimSpace(rec.fields[5]) != "" {
				groupCcy = strings.TrimSpace(rec.fields[5])
			}
		case "03":
			if asOf == "" {
				return nil, formatError(rec.no, "03 account record before a 02 group header")
			}
			if len(rec.fields) < 1 || strings.TrimSpace(rec.fields[0]) == "" {
				return nil, formatError(rec.no, "03 account record needs an account number")
			}
			ccy := groupCcy
			if len(rec.fields) > 1 && strings.TrimSpace(rec.fields[1]) != "" {
				ccy = strings.TrimSpace(rec.fields[1])
			}
			if !ValidCurrency(ccy) {
				return nil, formatError(rec.no, fmt.Sprintf("currency %q is not ISO 4217", ccy))
			}
			cur = &bai2Account{number: NormalizeIBAN(rec.fields[0]), currency: ccy, asOf: asOf, no: rec.no}
			accounts = append(accounts, cur)
			if len(rec.fields) > 2 {
				if err := cur.readBalances(rec.fields[2:], rec.no); err != nil {
					return nil, err
				}
			}
		case "16":
			if cur == nil {
				return nil, formatError(rec.no, "16 transaction record outside an 03 account group")
			}
			if len(cur.lines) >= MaxEntries {
				return nil, formatError(rec.no, fmt.Sprintf("more than %d entries", MaxEntries))
			}
			line, err := cur.detail(rec)
			if err != nil {
				return nil, err
			}
			cur.lines = append(cur.lines, line)
		case "49":
			cur = nil // account trailer: a later 16 needs a new 03
		case "98", "99":
			// group and file trailers: control totals are not checked
		default:
			return nil, formatError(rec.no, fmt.Sprintf("unknown BAI2 record code %q", rec.code))
		}
	}
	if !seenHead || len(accounts) == 0 {
		return nil, formatError(0, "a BAI2 file needs a 01 file header and at least one 03 account record")
	}
	out := make([]Statement, 0, len(accounts))
	for _, a := range accounts {
		st, err := a.statement()
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// bai2Records reads one record per physical line and folds 88 continuations
// into the record before them. Every record but a 16 ends at its first "/";
// a 16 ends after the text field, which may itself contain "/", so only a
// trailing "/" is dropped there. Continued 16 text is joined with a space.
func bai2Records(data []byte) ([]bai2Record, error) {
	var out []bai2Record
	for i, raw := range strings.Split(strings.ReplaceAll(string(data), "\r", ""), "\n") {
		no := i + 1
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		code, rest, _ := strings.Cut(line, ",")
		code = strings.TrimSpace(code)
		if code == "88" {
			if len(out) == 0 {
				return nil, formatError(no, "88 continuation before any record")
			}
			prev := &out[len(out)-1]
			if prev.code == "16" {
				if t := strings.TrimSpace(strings.TrimSuffix(rest, "/")); t != "" {
					prev.text = strings.TrimSpace(prev.text + " " + t)
				}
				continue
			}
			body, _, _ := strings.Cut(rest, "/")
			prev.fields = append(prev.fields, strings.Split(body, ",")...)
			continue
		}
		rec := bai2Record{code: code, no: no}
		if code == "16" {
			rec.fields, rec.text = split16(strings.TrimSuffix(rest, "/"))
		} else {
			body, _, _ := strings.Cut(rest, "/")
			rec.fields = strings.Split(body, ",")
		}
		out = append(out, rec)
	}
	return out, nil
}

// split16 separates the positional fields of a 16 record (type code, amount,
// funds type and its fields, bank reference, customer reference) from the
// trailing free text, which may contain commas.
func split16(body string) ([]string, string) {
	parts := strings.Split(body, ",")
	n := 3 // type code, amount, funds type
	if len(parts) >= 3 {
		n += fundsFieldCount(parts, 2)
	}
	n += 2 // bank reference, customer reference
	if len(parts) <= n {
		return parts, ""
	}
	return parts[:n], strings.TrimSpace(strings.Join(parts[n:], ","))
}

// fundsFieldCount is the number of fields that follow the funds type at
// fields[i]: S has immediate, one-day and two-or-more-day amounts; V a value
// date and time; D a count followed by that many day/amount pairs. 0, 1, 2, Z
// and blank have none.
func fundsFieldCount(fields []string, i int) int {
	switch strings.ToUpper(strings.TrimSpace(fields[i])) {
	case "S":
		return 3
	case "V":
		return 2
	case "D":
		if i+1 < len(fields) {
			if k, err := strconv.Atoi(strings.TrimSpace(fields[i+1])); err == nil && k >= 0 {
				return 1 + 2*k
			}
		}
		return 1
	default:
		return 0
	}
}

// readBalances walks the summary groups of a 03 record: type code, amount,
// item count, funds type and the funds type's own fields.
func (a *bai2Account) readBalances(fields []string, no int) error {
	for i := 0; i < len(fields); {
		code := strings.TrimSpace(fields[i])
		if code == "" {
			break // trailing empty field before the terminator
		}
		if len(code) != 3 || !isDigits(code) {
			return formatError(no, fmt.Sprintf("03 type code %q must be three digits", code))
		}
		amount := ""
		if i+1 < len(fields) {
			amount = strings.TrimSpace(fields[i+1])
		}
		if code == bai2OpeningLedger || code == bai2ClosingLedger {
			if amount == "" {
				return formatError(no, "03 balance "+code+" has no amount")
			}
			amt, err := bai2Amount(amount, a.currency)
			if err != nil {
				return formatError(no, err.Error())
			}
			if code == bai2OpeningLedger {
				a.opening = &amt
			} else {
				a.closing = &amt
			}
		}
		next := i + 4 // type code, amount, item count, funds type
		if i+3 < len(fields) {
			next += fundsFieldCount(fields, i+3)
		}
		i = next
	}
	return nil
}

// detail reads one 16 record. Codes 100 to 399 are credits and 400 to 699
// debits; other codes (loan detail, non-monetary, custom) are refused because
// their sign is not defined.
func (a *bai2Account) detail(rec bai2Record) (Line, error) {
	f := rec.fields
	if len(f) < 2 {
		return Line{}, formatError(rec.no, "16 record needs a type code and an amount")
	}
	code := strings.TrimSpace(f[0])
	n, err := strconv.Atoi(code)
	if err != nil || len(code) != 3 || n < 100 || n > 699 {
		return Line{}, formatError(rec.no, fmt.Sprintf("16 type code %q is not a credit (1xx-3xx) or debit (4xx-6xx) transaction code", code))
	}
	raw := strings.TrimSpace(f[1])
	if raw == "" || strings.ContainsAny(raw, "+-") {
		return Line{}, formatError(rec.no, "16 amount must be an unsigned integer in minor units")
	}
	amt, err := bai2Amount(raw, a.currency)
	if err != nil {
		return Line{}, formatError(rec.no, err.Error())
	}
	if n >= 400 {
		amt = amt.Neg()
	}
	value := a.asOf
	refAt := 3
	if len(f) > 2 {
		if strings.EqualFold(strings.TrimSpace(f[2]), "V") && len(f) > 3 && strings.TrimSpace(f[3]) != "" {
			if value, err = yymmdd(f[3]); err != nil {
				return Line{}, formatError(rec.no, "16 value "+err.Error())
			}
		}
		refAt += fundsFieldCount(f, 2)
	}
	field := func(i int) string {
		if i < len(f) {
			return strings.TrimSpace(f[i])
		}
		return ""
	}
	bankRef, customerRef := field(refAt), field(refAt+1)
	reference := rec.text
	if reference == "" {
		reference = bankRef
	}
	rawFragment := "16," + strings.Join(f, ",")
	if rec.text != "" {
		rawFragment += "," + rec.text
	}
	return Line{
		LineNo:      len(a.lines) + 1,
		BookingDate: a.asOf,
		ValueDate:   value,
		Amount:      amt,
		Currency:    a.currency,
		BTCCode:     "BAI2/" + code,
		EndToEndRef: customerRef,
		Reference:   reference,
		RawFragment: rawFragment,
	}, nil
}

func (a *bai2Account) statement() (Statement, error) {
	if a.opening == nil || a.closing == nil {
		return Statement{}, formatError(a.no, "03 record for account "+a.number+" needs 010 opening and 015 closing ledger balances")
	}
	st := Statement{
		Format:        FormatBAI2,
		StatementID:   a.number + "-" + a.asOf,
		Account:       a.number,
		Currency:      a.currency,
		OpeningDate:   a.asOf,
		OpeningAmount: *a.opening,
		ClosingDate:   a.asOf,
		ClosingAmount: *a.closing,
		Lines:         a.lines,
	}
	if err := CheckBalances(st); err != nil {
		return Statement{}, err
	}
	return st, nil
}

// bai2Amount converts an implied-decimal integer (minor units; balances may
// carry a sign) into a decimal in currency units.
func bai2Amount(s, currency string) (decimal.Decimal, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return decimal.Zero, fmt.Errorf("amount %q is not an integer in minor units", s)
	}
	return decimal.New(n, -money.MinorUnits(currency)), nil
}

// yymmdd reads the YYMMDD dates of BAI2 and MT940 as YYYY-MM-DD (years 2000-2099).
func yymmdd(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) != 6 || !isDigits(s) {
		return "", fmt.Errorf("date %q must be YYMMDD", s)
	}
	d := "20" + s[:2] + "-" + s[2:4] + "-" + s[4:]
	if !validDate(d) {
		return "", fmt.Errorf("date %q is not a calendar date", s)
	}
	return d, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
