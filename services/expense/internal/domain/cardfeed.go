package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Card feed formats accepted by ParseCardFeed (FR-43.13). Visa VCF 4.0 is not
// parsed in this slice; see NOT-IMPLEMENTED.md.
const (
	FeedCSV = "CSV"
	FeedOFX = "OFX"
)

// maxMerchantLen matches the expense line merchant limit.
const maxMerchantLen = 200

// ImportedTxn is one parsed card transaction, not yet assigned to a worker.
// Amount is a positive spend; refunds are negative. Line is the file line of a
// CSV row, or the 1-based position of an OFX STMTTRN block.
type ImportedTxn struct {
	Line      int
	CardLast4 string
	TxnDate   time.Time
	PostDate  time.Time
	Merchant  string
	MCC       string
	Amount    decimal.Decimal
	Currency  string
}

// RowError explains why one feed row was rejected. Line is numbered as in ImportedTxn.
type RowError struct {
	Line   int
	Reason string
}

// Feed is the result of parsing one file. FileHash keys the import so the same
// file is never imported twice (EXP-CARD-002).
type Feed struct {
	Format       string
	FileHash     string
	Transactions []ImportedTxn
	Rejected     []RowError
}

// ErrUnsupportedFormat is returned for a format other than CSV or OFX.
var ErrUnsupportedFormat = errors.New("expense: unsupported card feed format")

// ErrMissingColumns is returned when a CSV feed lacks a required column.
var ErrMissingColumns = errors.New("expense: card feed CSV is missing required columns")

// ErrMalformedFeed is returned when an OFX file holds no OFX tags at all.
var ErrMalformedFeed = errors.New("expense: card feed is not a readable OFX statement")

var csvRequired = []string{"card_last4", "txn_date", "post_date", "merchant", "amount", "currency"}

// ParseCardFeed parses a CSV or OFX card statement. Bad rows are collected in
// Rejected and do not stop the file; a file with no usable header is an error.
// Nothing is rounded: an amount with more decimals than its currency allows is
// rejected rather than silently changed.
func ParseCardFeed(format string, data []byte) (Feed, error) {
	sum := sha256.Sum256(data)
	feed := Feed{Format: strings.ToUpper(format), FileHash: hex.EncodeToString(sum[:])}
	switch feed.Format {
	case FeedCSV:
		return parseCSV(feed, data)
	case FeedOFX:
		return parseOFX(feed, data)
	default:
		return feed, ErrUnsupportedFormat
	}
}

func parseCSV(feed Feed, data []byte) (Feed, error) {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if err != nil {
		return feed, ErrMissingColumns
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, c := range csvRequired {
		if _, ok := idx[c]; !ok {
			return feed, ErrMissingColumns
		}
	}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		var perr *csv.ParseError
		if errors.As(err, &perr) {
			// A malformed row (for example a stray quote) is one rejected row,
			// never the silent end of the file.
			feed.Rejected = append(feed.Rejected, RowError{Line: perr.StartLine, Reason: "row is not valid CSV: " + perr.Err.Error()})
			continue
		}
		if err != nil {
			return feed, err
		}
		line, _ := r.FieldPos(0)
		get := func(col string) string {
			i, ok := idx[col]
			if ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		txn, reason := buildTxn(line, get("card_last4"), get("txn_date"), get("post_date"),
			get("merchant"), get("mcc"), get("amount"), get("currency"))
		if reason != "" {
			feed.Rejected = append(feed.Rejected, RowError{Line: line, Reason: reason})
			continue
		}
		feed.Transactions = append(feed.Transactions, txn)
	}
	return feed, nil
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

func isCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func buildTxn(line int, last4, txnDate, postDate, merchant, mcc, amount, currency string) (ImportedTxn, string) {
	if len(last4) != 4 || !isDigits(last4) {
		return ImportedTxn{}, "card_last4 must be four digits"
	}
	td, err := time.Parse(dateLayout, txnDate)
	if err != nil {
		return ImportedTxn{}, "txn_date must be YYYY-MM-DD"
	}
	pd, err := time.Parse(dateLayout, postDate)
	if err != nil {
		return ImportedTxn{}, "post_date must be YYYY-MM-DD"
	}
	if pd.Before(td) {
		return ImportedTxn{}, "post_date must not be before txn_date"
	}
	if merchant == "" || len(merchant) > maxMerchantLen {
		return ImportedTxn{}, "merchant is required and at most 200 characters"
	}
	if mcc != "" && (len(mcc) != 4 || !isDigits(mcc)) {
		return ImportedTxn{}, "mcc must be four digits when present"
	}
	cur := strings.ToUpper(currency)
	if !isCurrencyCode(cur) {
		return ImportedTxn{}, "currency must be a three-letter code"
	}
	amt, err := decimal.NewFromString(amount)
	if err != nil {
		return ImportedTxn{}, "amount is not a decimal"
	}
	if amt.IsZero() {
		return ImportedTxn{}, "amount must not be zero"
	}
	if !amt.Equal(amt.Round(money.MinorUnits(cur))) {
		return ImportedTxn{}, "amount has more decimals than " + cur + " allows"
	}
	return ImportedTxn{
		Line: line, CardLast4: last4, TxnDate: td, PostDate: pd, Merchant: merchant,
		MCC: mcc, Amount: amt, Currency: cur,
	}, ""
}

const dateLayout = "2006-01-02"

// ofxTag matches an SGML leaf or block tag: <TAG>value (value runs to the next
// '<' or end of line). Block tags have an empty value. It reads both OFX 1.x
// SGML and OFX 2.x XML.
var ofxTag = regexp.MustCompile(`<(/?[A-Za-z0-9.]+)>([^<\r\n]*)`)

func parseOFX(feed Feed, data []byte) (Feed, error) {
	matches := ofxTag.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		return feed, ErrMalformedFeed
	}
	// ACCTID and CURDEF apply to the statement that follows them, so a file with
	// several card accounts takes each block's own account and currency.
	var acct, curdef string
	var block map[string]string
	n := 0
	for _, m := range matches {
		tag, val := strings.ToUpper(m[1]), strings.TrimSpace(m[2])
		switch {
		case tag == "STMTTRN":
			block = map[string]string{}
		case tag == "/STMTTRN" && block != nil:
			n++
			feed.addOFX(n, block, acct, curdef)
			block = nil
		case block != nil:
			block[tag] = val
		case tag == "ACCTID":
			acct = val
		case tag == "CURDEF":
			curdef = val
		}
	}
	return feed, nil
}

func (f *Feed) addOFX(n int, b map[string]string, acct, curdef string) {
	last4 := ""
	if len(acct) >= 4 {
		last4 = acct[len(acct)-4:]
	}
	cur := b["CURRENCY"]
	if cur == "" {
		cur = curdef
	}
	// OFX signs spend negative; the stored amount is a positive spend.
	amount := ""
	if amt, err := decimal.NewFromString(strings.TrimPrefix(b["TRNAMT"], "+")); err == nil {
		amount = amt.Neg().String()
	}
	posted := ofxDate(b["DTPOSTED"])
	// DTUSER is the date the cardholder made the purchase; without it the
	// posting date stands in.
	used := ofxDate(b["DTUSER"])
	if used == "" {
		used = posted
	}
	var reason string
	txn := ImportedTxn{}
	if amount == "" {
		reason = "TRNAMT is not a decimal"
	} else {
		txn, reason = buildTxn(n, last4, used, posted, b["NAME"], b["SIC"], amount, cur)
	}
	if reason != "" {
		f.Rejected = append(f.Rejected, RowError{Line: n, Reason: reason})
		return
	}
	f.Transactions = append(f.Transactions, txn)
}

// ofxDate converts an OFX date YYYYMMDD[HHMMSS[.XXX][[offset:TZ]]] to YYYY-MM-DD,
// or "" when it is too short.
func ofxDate(v string) string {
	if len(v) < 8 {
		return ""
	}
	return v[0:4] + "-" + v[4:6] + "-" + v[6:8]
}
