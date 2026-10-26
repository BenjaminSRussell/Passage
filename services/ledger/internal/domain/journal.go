// Package domain holds the ledger's pure rules: entry validation, dimension
// combination hashing, balance aggregation and reversal. It performs no I/O.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	// MaxLinesPerEntry is the FR-11.5 limit (max_lines_per_entry default).
	MaxLinesPerEntry = 10000
	// MaxDimensionAttributes is the FR-11.2 limit per ledger.
	MaxDimensionAttributes = 20
	// SideDebit and SideCredit are the JournalLine debit_credit values.
	SideDebit  = "D"
	SideCredit = "C"
	// ReconNone is the reconciliation_account_type of an ordinary account.
	ReconNone = "none"
	// ManualSourceSystem and ManualSourceType identify a manually keyed journal
	// (journal workspace or a user posting directly). BR-11.8 protects
	// reconciliation accounts from these; subledgers post to them freely.
	ManualSourceSystem = "ledger"
	ManualSourceType   = "manual_journal"
)

var dimValueRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,20}$`)

// Violation is one rule failure. Codes are the BR-11.9 catalogue; LineIndex is
// nil for entry-level violations.
type Violation struct {
	Code      string `json:"code"`
	Field     string `json:"field,omitempty"`
	LineIndex *int   `json:"lineIndex,omitempty"`
	Message   string `json:"message"`
}

// RejectedError carries every violation of a request that was refused (422).
type RejectedError struct{ Violations []Violation }

func (e *RejectedError) Error() string {
	if len(e.Violations) == 0 {
		return "entry rejected"
	}
	return fmt.Sprintf("entry rejected: %s: %s", e.Violations[0].Code, e.Violations[0].Message)
}

// Account is the subset of a main account that posting rules read.
type Account struct {
	ID            string
	Number        string
	Postable      bool
	SuspendedFrom *time.Time
	ReconType     string
}

// Rules is the ledger configuration that posting rules read.
type Rules struct {
	// ChartCode scopes account numbers; it is part of every combination hash.
	ChartCode           string
	Currency            string
	DimensionAttributes []string
	FirstOpenDate       *time.Time
	FutureDays          int
	// Manual is true when the entry is a manual journal (BR-11.8): lines on
	// reconciliation accounts are then refused.
	Manual bool
}

// IsManualSource reports whether a source system/type pair is a manual journal.
func IsManualSource(system, sourceType string) bool {
	return system == ManualSourceSystem && sourceType == ManualSourceType
}

// LineInput is one requested line. Amount is already parsed and non-negative;
// the caller checks sign and syntax.
type LineInput struct {
	Account       string
	Dimensions    map[string]string
	Side          string
	Amount        decimal.Decimal
	Currency      string
	Text          string
	SourceLineRef string
}

// ValidLine is a line that passed every line-level rule.
type ValidLine struct {
	Index         int
	Account       Account
	Dimensions    map[string]string
	Side          string
	Amount        decimal.Decimal
	Text          string
	SourceLineRef string
	CombinationID []byte
	DisplayName   string
}

// Result is the outcome of validating an entry. Debit and Credit are the sums
// of all parsed lines, valid or not, so the caller can report totals.
type Result struct {
	// Currency is the ledger currency the totals are in.
	Currency   string
	Lines      []ValidLine
	Debit      decimal.Decimal
	Credit     decimal.Decimal
	Violations []Violation
}

// Valid reports whether the entry may be posted.
func (r Result) Valid() bool { return len(r.Violations) == 0 }

// PeriodKey maps a posting date to its YYYY-MM period (a stand-in for the
// fiscal calendar of wave 6 until it is wired in).
func PeriodKey(date time.Time) string { return date.Format("2006-01") }

// CheckPostingDate applies BR-11.4: no posting before the first open date, and
// no posting further ahead than the ledger's future window.
func CheckPostingDate(r Rules, date, today time.Time) []Violation {
	var out []Violation
	if r.FirstOpenDate != nil && date.Before(*r.FirstOpenDate) {
		out = append(out, Violation{Code: "LEDGER_PERIOD_CLOSED", Field: "postingDate",
			Message: fmt.Sprintf("posting date %s is before the first open date %s", date.Format(time.DateOnly), r.FirstOpenDate.Format(time.DateOnly))})
	}
	if date.After(today.AddDate(0, 0, r.FutureDays)) {
		out = append(out, Violation{Code: "LEDGER_DATE_INVALID", Field: "postingDate",
			Message: fmt.Sprintf("posting date %s is more than %d days ahead", date.Format(time.DateOnly), r.FutureDays)})
	}
	return out
}

// ValidateEntry checks an entry against the ledger rules and returns every
// violation in one pass (no short-circuit across lines, BR-11.17 spirit).
func ValidateEntry(r Rules, accounts map[string]Account, postingDate, today time.Time, lines []LineInput) Result {
	res := Result{Currency: r.Currency, Debit: decimal.Zero, Credit: decimal.Zero}
	res.Violations = append(res.Violations, CheckPostingDate(r, postingDate, today)...)
	if len(lines) == 0 {
		res.Violations = append(res.Violations, Violation{Code: "LEDGER_LINE_EMPTY", Field: "lines", Message: "entry has no lines"})
		return res
	}
	if len(lines) > MaxLinesPerEntry {
		res.Violations = append(res.Violations, Violation{Code: "LEDGER_LINE_LIMIT", Field: "lines",
			Message: fmt.Sprintf("entry has %d lines; the limit is %d", len(lines), MaxLinesPerEntry)})
		return res
	}
	allowed := make(map[string]bool, len(r.DimensionAttributes))
	for _, a := range r.DimensionAttributes {
		allowed[a] = true
	}
	minor := money.MinorUnits(r.Currency)
	for i, in := range lines {
		idx := i
		fail := func(code, field, msg string) {
			res.Violations = append(res.Violations, Violation{Code: code, Field: field, LineIndex: &idx, Message: msg})
		}
		ok := true
		acct, found := accounts[in.Account]
		switch {
		case !found:
			fail("LEDGER_ACCOUNT_NOT_FOUND", "lines.account", fmt.Sprintf("account %s does not exist", in.Account))
			ok = false
		default:
			if !acct.Postable {
				fail("LEDGER_ACCOUNT_NOT_POSTABLE", "lines.account", fmt.Sprintf("account %s is a total or reporting account", acct.Number))
				ok = false
			}
			if acct.SuspendedFrom != nil && !postingDate.Before(*acct.SuspendedFrom) {
				fail("LEDGER_ACCOUNT_SUSPENDED", "lines.account", fmt.Sprintf("account %s is suspended from %s", acct.Number, acct.SuspendedFrom.Format(time.DateOnly)))
				ok = false
			}
			if r.Manual && acct.ReconType != "" && acct.ReconType != ReconNone {
				fail("LEDGER_RECON_ACCOUNT_MANUAL", "lines.account", fmt.Sprintf("account %s accepts subledger postings only", acct.Number))
				ok = false
			}
		}
		if in.Currency != r.Currency {
			fail("LEDGER_CURRENCY_MISMATCH", "lines.amount.currency", fmt.Sprintf("line currency %s differs from ledger currency %s", in.Currency, r.Currency))
			ok = false
		}
		if !in.Amount.Equal(in.Amount.Round(minor)) {
			fail("LEDGER_AMOUNT_SCALE", "lines.amount.value", fmt.Sprintf("amount %s has more decimals than %s allows (%d)", in.Amount.String(), r.Currency, minor))
			ok = false
		}
		if in.Amount.IsZero() && in.Text == "" {
			fail("LEDGER_LINE_EMPTY", "lines.amount.value", "line has no amount and no text")
			ok = false
		}
		// An empty value means "blank" and is dropped so the stored dimensions
		// match the combination hash, which treats blanks the same way.
		dims := make(map[string]string, len(in.Dimensions))
		keys := make([]string, 0, len(in.Dimensions))
		for k, v := range in.Dimensions {
			if v == "" {
				continue
			}
			dims[k] = v
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := dims[k]
			switch {
			case !allowed[k]:
				fail("LEDGER_DIMENSION_INVALID", "lines.dimensions."+k, fmt.Sprintf("dimension %s is not defined on this ledger", k))
				ok = false
			case !dimValueRe.MatchString(v):
				fail("LEDGER_DIMENSION_INVALID", "lines.dimensions."+k, fmt.Sprintf("value %q is not a valid %s", v, k))
				ok = false
			}
		}
		if in.Side == SideCredit {
			res.Credit = res.Credit.Add(in.Amount)
		} else {
			res.Debit = res.Debit.Add(in.Amount)
		}
		if !ok {
			continue
		}
		id, display := CombinationID(r.DimensionAttributes, r.ChartCode, acct.Number, dims)
		res.Lines = append(res.Lines, ValidLine{Index: idx, Account: acct, Dimensions: dims, Side: in.Side,
			Amount: in.Amount, Text: in.Text, SourceLineRef: in.SourceLineRef, CombinationID: id, DisplayName: display})
	}
	if !res.Debit.Equal(res.Credit) {
		res.Violations = append(res.Violations, Violation{Code: "LEDGER_UNBALANCED", Field: "lines",
			Message: fmt.Sprintf("entry does not balance in %s: debits %s, credits %s", r.Currency,
				res.Debit.StringFixed(minor), res.Credit.StringFixed(minor))})
	}
	return res
}

// CombinationID returns the SHA-256 of the canonical tuple (chart, account, then
// each ledger attribute name=value in ledger order; blanks are empty) and a
// display name such as 1010-D100-C200 (FR-11.4). Attribute names and the chart
// are part of the hash, so the same values under different attribute lists or
// charts never collide on dimension_combination.
func CombinationID(attrs []string, chart, account string, dims map[string]string) ([]byte, string) {
	h := sha256.New()
	h.Write([]byte(chart + "|" + account))
	parts := []string{account}
	for _, a := range attrs {
		v := dims[a]
		h.Write([]byte{0x1f})
		h.Write([]byte(a + "=" + v))
		if v != "" {
			parts = append(parts, v)
		}
	}
	return h.Sum(nil), strings.Join(parts, "-")
}

// BalanceDelta is one projection row change for a posting.
type BalanceDelta struct {
	MainAccountID string
	Combination   []byte
	PeriodKey     string
	Debit         decimal.Decimal
	Credit        decimal.Decimal
	LineCount     int64
}

// Key orders deltas the way the projection's primary key does (BR-11.5).
func (d BalanceDelta) Key() string { return d.MainAccountID + "|" + hex.EncodeToString(d.Combination) }

// AggregateBalances folds valid lines into one delta per (account, combination)
// for one period, sorted by primary key so upserts lock in a fixed order.
func AggregateBalances(periodKey string, lines []ValidLine) []BalanceDelta {
	byKey := map[string]*BalanceDelta{}
	for _, l := range lines {
		d := BalanceDelta{MainAccountID: l.Account.ID, Combination: l.CombinationID, PeriodKey: periodKey}
		k := d.Key()
		if byKey[k] == nil {
			d.Debit, d.Credit = decimal.Zero, decimal.Zero
			byKey[k] = &d
		}
		agg := byKey[k]
		if l.Side == SideCredit {
			agg.Credit = agg.Credit.Add(l.Amount)
		} else {
			agg.Debit = agg.Debit.Add(l.Amount)
		}
		agg.LineCount++
	}
	out := make([]BalanceDelta, 0, len(byKey))
	for _, d := range byKey {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// CheckReversalDate applies FR-11.8: a reversal may not be dated before the
// entry it reverses; the usual posting-date rules (BR-11.4) apply as well.
func CheckReversalDate(r Rules, original, reversal, today time.Time) []Violation {
	out := CheckPostingDate(r, reversal, today)
	if reversal.Before(original) {
		out = append(out, Violation{Code: "LEDGER_DATE_INVALID", Field: "postingDate",
			Message: fmt.Sprintf("reversal date %s is before the original posting date %s", reversal.Format(time.DateOnly), original.Format(time.DateOnly))})
	}
	return out
}

// ReversalSide returns the opposite side; a reversing line keeps the amount.
func ReversalSide(side string) string {
	if side == SideCredit {
		return SideDebit
	}
	return SideCredit
}

// Net returns debit minus credit, the projection's signed net.
func Net(debit, credit decimal.Decimal) decimal.Decimal { return debit.Sub(credit) }

// HashPayload returns the SHA-256 of the canonical JSON form of v. Used to
// detect Idempotency-Key reuse with a different body (BR-11.6).
func HashPayload(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}
