package domain

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

// CodeBAI2Malformed is the message-catalog code for a file the BAI2 parser rejects (AR-0004).
const CodeBAI2Malformed = "AR-0004"

// DefaultLockboxTypeCodes are the BAI2 transaction type codes read as lockbox
// check credits when a bank profile does not name its own: 116 "Item in
// Lockbox Deposit", one detail per check. 115 "Lockbox Deposit" is a deposit
// total and is left out by default so a file that carries both is not counted
// twice. Bank profiles override the set; codes outside it are kept as
// non-payment transactions.
var DefaultLockboxTypeCodes = []string{"116"}

// CreditTypeCode reports whether a BAI2 detail type code is a credit (100-399).
// Debit codes (400-699) can never be a customer payment.
func CreditTypeCode(code string) bool {
	return typeCode.MatchString(code) && code >= "100" && code <= "399"
}

// ParseError points at the record that made a BAI2 file unusable.
type ParseError struct {
	Line    int
	Message string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("Line %d: %s", e.Line, e.Message)
}

// BAI2Transaction is one type-16 detail record.
type BAI2Transaction struct {
	Line        int
	TypeCode    string
	Amount      decimal.Decimal
	FundsType   string
	BankRef     string
	CustomerRef string
	Text        string
}

// BAI2Account is one type-03 account identifier and its detail records.
type BAI2Account struct {
	Number       string
	Currency     string
	Transactions []BAI2Transaction
}

// BAI2File is a parsed and structurally validated BAI2 lockbox or bank file.
type BAI2File struct {
	Accounts []BAI2Account
}

// LockboxCheck is one check credit read from a BAI2 file: one payment (FR-30.3).
type LockboxCheck struct {
	Line        int
	AccountNo   string
	Currency    string
	Amount      decimal.Decimal
	BankRef     string
	CustomerRef string
	Text        string
}

var (
	cents        = regexp.MustCompile(`^[0-9]+$`)
	typeCode     = regexp.MustCompile(`^[0-9]{3}$`)
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
)

// ParseBAI2 reads a BAI2 file (record types 01, 02, 03, 16, 49, 88, 98, 99).
// It checks the record order, the record counts in the 49, 98 and 99 trailers,
// the number of accounts and groups, and the cents format of amounts. The first
// problem found is returned as a *ParseError with its line number. A 16 record
// may carry funds-type detail (S: three amounts; V: value date and time; D: a
// count and that many day/amount pairs) before the bank and customer references.
// The text of a 16 or 88 record runs to the end of the line, so its closing
// '/' is optional.
func ParseBAI2(content string) (BAI2File, error) {
	var f BAI2File
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")

	var (
		total, groupCount, accountCount, groups, accountsInGroup int
		groupCurrency                                            string
		inGroup, inAccount, sawTrailer                           bool
		last                                                     int
		lastCode                                                 string
	)
	for i, raw := range lines {
		line := i + 1
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		if sawTrailer {
			return BAI2File{}, &ParseError{Line: line, Message: "record after file trailer 99"}
		}
		code, _, _ := strings.Cut(text, ",")
		if !strings.HasSuffix(text, "/") && code != "16" && code != "88" {
			return BAI2File{}, &ParseError{Line: line, Message: "record is not terminated by '/'"}
		}
		fields := strings.Split(strings.TrimSuffix(text, "/"), ",")
		last = line
		total++
		if total == 1 && code != "01" {
			return BAI2File{}, &ParseError{Line: line, Message: "file must start with record 01"}
		}
		if inGroup {
			groupCount++
		}
		if inAccount {
			accountCount++
		}
		if code != "88" && code != "16" {
			lastCode = code
		}
		switch code {
		case "01":
			if total != 1 {
				return BAI2File{}, malformed(line, code)
			}
		case "02":
			if inGroup {
				return BAI2File{}, malformed(line, code)
			}
			inGroup, groupCount, accountsInGroup = true, 1, 0
			groups++
			groupCurrency = field(fields, 6)
		case "03":
			if !inGroup || inAccount {
				return BAI2File{}, malformed(line, code)
			}
			number := field(fields, 1)
			currency := field(fields, 2)
			if currency == "" {
				currency = groupCurrency
			}
			if number == "" || !currencyCode.MatchString(currency) {
				return BAI2File{}, &ParseError{Line: line, Message: "account identifier needs an account number and currency"}
			}
			f.Accounts = append(f.Accounts, BAI2Account{Number: number, Currency: currency})
			inAccount, accountCount, accountsInGroup = true, 1, accountsInGroup+1
		case "16":
			if !inAccount || len(fields) < 3 || !typeCode.MatchString(fields[1]) || !cents.MatchString(fields[2]) {
				return BAI2File{}, malformed(line, code)
			}
			refAt, ok := afterFundsType(fields)
			if !ok {
				return BAI2File{}, &ParseError{Line: line, Message: "detail record 16 has malformed funds-type fields"}
			}
			acct := &f.Accounts[len(f.Accounts)-1]
			acct.Transactions = append(acct.Transactions, BAI2Transaction{
				Line: line, TypeCode: fields[1], Amount: centsToDecimal(fields[2]), FundsType: field(fields, 3),
				BankRef: field(fields, refAt), CustomerRef: field(fields, refAt+1), Text: rest(fields, refAt+2),
			})
			lastCode = code
		case "88":
			// A continuation extends the record before it. Only the text of a
			// 16 detail is kept; continuations of headers and trailers add no data.
			if lastCode == "" {
				return BAI2File{}, malformed(line, code)
			}
			if lastCode == "16" {
				acct := &f.Accounts[len(f.Accounts)-1]
				tx := &acct.Transactions[len(acct.Transactions)-1]
				tx.Text = strings.TrimSpace(tx.Text + " " + rest(fields, 1))
			}
		case "49":
			if !inAccount || !countMatches(fields, 2, accountCount) {
				return BAI2File{}, &ParseError{Line: line, Message: "account trailer 49 record count does not match"}
			}
			inAccount = false
		case "98":
			if !inGroup || !countMatches(fields, 3, groupCount) || !countMatches(fields, 2, accountsInGroup) {
				return BAI2File{}, &ParseError{Line: line, Message: "group trailer 98 account or record count does not match"}
			}
			inGroup = false
		case "99":
			if inGroup || inAccount || !countMatches(fields, 2, groups) || !countMatches(fields, 3, total) {
				return BAI2File{}, &ParseError{Line: line, Message: "file trailer 99 group or record count does not match"}
			}
			sawTrailer = true
		default:
			return BAI2File{}, malformed(line, code)
		}
	}
	if !sawTrailer {
		return BAI2File{}, &ParseError{Line: last, Message: "file has no trailer 99"}
	}
	return f, nil
}

// Checks returns the transactions whose type code is a lockbox check credit
// (codes is the bank profile's list, or DefaultLockboxTypeCodes) and whose
// amount is positive. Each is a candidate payment.
func (f BAI2File) Checks(codes []string) []LockboxCheck {
	want := map[string]bool{}
	for _, c := range codes {
		want[c] = true
	}
	var out []LockboxCheck
	for _, a := range f.Accounts {
		for _, t := range a.Transactions {
			if want[t.TypeCode] && t.Amount.IsPositive() {
				out = append(out, LockboxCheck{Line: t.Line, AccountNo: a.Number, Currency: a.Currency, Amount: t.Amount,
					BankRef: t.BankRef, CustomerRef: t.CustomerRef, Text: t.Text})
			}
		}
	}
	return out
}

// afterFundsType returns the index of the bank reference in a 16 record, past
// the funds-type detail fields, or false when that detail is malformed.
func afterFundsType(fields []string) (int, bool) {
	switch strings.ToUpper(field(fields, 3)) {
	case "", "0", "1", "2", "Z":
		return 4, true
	case "S":
		return 7, true
	case "V":
		return 6, true
	case "D":
		n := field(fields, 4)
		if !cents.MatchString(n) || len(n) > 3 {
			return 0, false
		}
		var count int
		fmt.Sscan(n, &count)
		return 5 + 2*count, true
	}
	return 0, false
}

func malformed(line int, code string) *ParseError {
	return &ParseError{Line: line, Message: "malformed BAI2 record type " + code}
}

func field(fields []string, i int) string {
	if i < len(fields) {
		return strings.TrimSpace(fields[i])
	}
	return ""
}

func rest(fields []string, from int) string {
	if from >= len(fields) {
		return ""
	}
	return strings.TrimSpace(strings.Join(fields[from:], ","))
}

func countMatches(fields []string, i, want int) bool {
	return cents.MatchString(field(fields, i)) && field(fields, i) == fmt.Sprint(want)
}

// centsToDecimal converts a validated digit string of minor units to a decimal amount.
func centsToDecimal(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d.Shift(-2)
}
