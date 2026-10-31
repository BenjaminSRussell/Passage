package domain

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Drill level 2 page sizes (BR-15.4): 500 by default (REPORTING_PAGE_SIZE),
// never more than 1,000.
const (
	DefaultLinePage = 500
	MaxLinePage     = 1000
)

// CodeLineInvalid is a slice-local code for a journal line the read model
// refuses (malformed date, period, amounts or source link); it is not in the
// PRD catalog yet.
const CodeLineInvalid = "REPORT-0028"

// ErrNoSuchKey means a drill asked for a balance key the cell does not have.
var ErrNoSuchKey = errors.New("finreport: no such balance key")

// ErrBadCursor means a drill-down cursor could not be decoded.
var ErrBadCursor = errors.New("finreport: malformed drill-down cursor")

// JournalLine is one ledger line in the reporting read model (FR-15.7). Debit
// and credit are at the currency type's scale; the line belongs to the balance
// (ledger, period, account, combination, currency type). ClosingEntry marks
// lines of closing entries, which income statements and cash flows leave out
// (BR-15.2). SourceURI is a Passage route ("/ap/invoices/…") or an http(s) URL
// of the source document; TraceID is the posting's trace.
type JournalLine struct {
	LedgerID        string
	LegalEntityID   string
	PostingSequence int64
	LineNumber      int
	PostingDate     string // YYYY-MM-DD
	PeriodYear      int
	PeriodNo        int
	Account         string
	Combination     string
	CurrencyType    string
	Voucher         string
	Debit           decimal.Decimal
	Credit          decimal.Decimal
	ClosingEntry    bool
	SourceURI       string
	TraceID         string
}

// Net is debit minus credit, the amount the line adds to its balance.
func (l JournalLine) Net() decimal.Decimal { return l.Debit.Sub(l.Credit) }

// Period is the line's period label, the form balance keys use.
func (l JournalLine) Period() string { return Period{Year: l.PeriodYear, No: l.PeriodNo}.Label() }

// Validate checks a line before it enters the read model.
func (l JournalLine) Validate() error {
	switch {
	case l.PostingSequence < 1:
		return errf(CodeLineInvalid, "posting sequence must be positive, got %d", l.PostingSequence)
	case l.LineNumber < 1:
		return errf(CodeLineInvalid, "line number must be positive, got %d", l.LineNumber)
	case l.PeriodNo < 0 || l.PeriodYear < 1:
		return errf(CodeLineInvalid, "period %d-%d is not a fiscal period", l.PeriodYear, l.PeriodNo)
	case l.Account == "" || l.Combination == "":
		return errf(CodeLineInvalid, "account and combination are required")
	case l.CurrencyType != "acct" && l.CurrencyType != "rpt" && l.CurrencyType != "txn":
		return errf(CodeLineInvalid, "currency type %q is not acct, rpt or txn", l.CurrencyType)
	case l.Debit.Exponent() < -6 || l.Credit.Exponent() < -6:
		return errf(CodeLineInvalid, "amounts carry at most six decimal places")
	}
	if _, err := time.Parse(time.DateOnly, l.PostingDate); err != nil {
		return errf(CodeLineInvalid, "posting date %q is not YYYY-MM-DD", l.PostingDate)
	}
	if !SafeSourceURI(l.SourceURI) {
		return errf(CodeLineInvalid, "source %q is neither a Passage route nor an http(s) URL", l.SourceURI)
	}
	return nil
}

// SafeSourceURI reports whether a source link can be offered to a browser: an
// empty value, an absolute Passage route ("/…", not "//…"), or an http or https
// URL with a host. Anything else (javascript:, data:, relative paths) is
// refused so a drill-down link can never run script.
func SafeSourceURI(s string) bool {
	if s == "" {
		return true
	}
	if strings.HasPrefix(s, "/") {
		return !strings.HasPrefix(s, "//") && !strings.Contains(s, `\`)
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// LineCursor is the keyset position of the last line of a page, in BR-15.4
// order: (posting date, posting sequence, line number), then the balance key
// position. Within one ledger the posting sequence and line number identify a
// line; the key position orders the rare line listed under two keys of one
// cell (a total row that adds the same row twice).
type LineCursor struct {
	PostingDate     string
	PostingSequence int64
	LineNumber      int
	Key             int
}

// Encode renders the position as an opaque URL-safe cursor.
func (c LineCursor) Encode() string {
	raw := fmt.Sprintf("%s|%d|%d|%d", c.PostingDate, c.PostingSequence, c.LineNumber, c.Key)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeLineCursor parses a cursor produced by Encode.
func DecodeLineCursor(cursor string) (LineCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return LineCursor{}, ErrBadCursor
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 4 {
		return LineCursor{}, ErrBadCursor
	}
	if _, err := time.Parse(time.DateOnly, parts[0]); err != nil {
		return LineCursor{}, ErrBadCursor
	}
	seq, err1 := strconv.ParseInt(parts[1], 10, 64)
	line, err2 := strconv.Atoi(parts[2])
	key, err3 := strconv.Atoi(parts[3])
	if err1 != nil || err2 != nil || err3 != nil || seq < 0 || line < 0 || key < 0 {
		return LineCursor{}, ErrBadCursor
	}
	return LineCursor{PostingDate: parts[0], PostingSequence: seq, LineNumber: line, Key: key}, nil
}

// LineScope is what drill level 2 reads for one cell, or for one balance key
// of it (FR-15.7): the balance keys, whether closing entries are left out, and
// the run's high-water mark, so lines posted after the run never appear under
// it (BR-15.6). Value is the amount the lines must sum to: the cell value, or
// the selected key's amount. Each key keeps its own sign (Key.Credit).
type LineScope struct {
	Keys           []Key
	ExcludeClosing bool
	HWM            int64
	Value          decimal.Decimal
}

// NewLineScope builds the scope of a stored cell of a run evaluated with def
// at high-water mark hwm. key selects one balance key by its position in the
// level 1 list; a negative key means the whole cell. Every key is kept, in
// order, even one naming the same balance as another: a total that adds a row
// twice counts its lines twice, and the drill shows them under both keys.
//
// Cells stored before keys carried their sign have no Credit flag; for a row
// that is not a total every key has the row's sign, so it is taken from the
// row. (A total of such a run that mixes signs then shows as unreconciled.)
func NewLineScope(def Definition, cell Cell, hwm int64, key int) (LineScope, error) {
	var row *Row
	for i := range def.Rows {
		if def.Rows[i].Code == cell.Row {
			row = &def.Rows[i]
			break
		}
	}
	if row == nil {
		return LineScope{}, fmt.Errorf("row %s is not in definition %s version %d", cell.Row, def.Code, def.Version)
	}
	keys := append([]Key(nil), cell.Keys...)
	if row.Kind != RowTotal && row.NormalBalance == "credit" {
		for i := range keys {
			keys[i].Credit = true
		}
	}
	scope := LineScope{ExcludeClosing: ExcludesClosing(def.Kind), HWM: hwm, Value: cell.Value, Keys: keys}
	if key >= 0 {
		if key >= len(keys) {
			return LineScope{}, fmt.Errorf("%w: cell %s/%s has %d balance keys, key %d does not exist", ErrNoSuchKey, cell.Row, cell.Column, len(keys), key)
		}
		scope.Keys = keys[key : key+1]
		scope.Value = keys[key].Amount
	}
	return scope, nil
}

// Signed is a line's net as key i shows it: -net on credit keys (BR-15.3).
func (s LineScope) Signed(i int, net decimal.Decimal) decimal.Decimal {
	if s.Keys[i].Credit {
		return net.Neg()
	}
	return net
}

// Reconciliation compares the lines of a scope with its balance keys.
// LinesTotal is the signed sum of every line in scope; Difference is Value
// minus LinesTotal; UnreconciledKeys counts keys whose lines do not sum to the
// key amount (a gap in the read model, or dimension security filtering lines).
type Reconciliation struct {
	LinesTotal       decimal.Decimal
	Difference       decimal.Decimal
	UnreconciledKeys int
}

// Reconciled is true when every key and the total tie exactly (BR-15.4).
func (r Reconciliation) Reconciled() bool {
	return r.UnreconciledKeys == 0 && r.Difference.IsZero()
}

// Reconcile takes the net sum of each key's in-scope lines, aligned with
// s.Keys, and compares it with the key amounts and the scope value.
func (s LineScope) Reconcile(netByKey []decimal.Decimal) Reconciliation {
	out := Reconciliation{LinesTotal: decimal.Zero}
	for i, k := range s.Keys {
		got := decimal.Zero
		if i < len(netByKey) {
			got = s.Signed(i, netByKey[i])
		}
		if !got.Equal(k.Amount) {
			out.UnreconciledKeys++
		}
		out.LinesTotal = out.LinesTotal.Add(got)
	}
	out.Difference = s.Value.Sub(out.LinesTotal)
	return out
}
