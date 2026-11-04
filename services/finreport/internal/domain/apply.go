package domain

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// YearEndClosingSource is the ledger source type of the year-end closing
// entries; their lines are closing entries (BR-15.2).
const YearEndClosingSource = "year_end_closing"

// CloseSourceSystem is the source system the close service posts with. The
// ledger itself trusts a year-end closing entry only when the close service
// posted it (posted_by_service); the event carries the caller-supplied source
// system, so the consumer asks for both until the ledger publishes the poster.
const CloseSourceSystem = "close"

// passageScheme is how ledger-built source links name a Passage route
// (passage://ledger/journal-batches/{id}); the read model stores the route.
const passageScheme = "passage://"

var periodKeyPattern = regexp.MustCompile(`^([0-9]{4})-(0[1-9]|1[0-2])$`)

// SourceLine is a posted ledger line as the ledger's journal-lines inquiry
// returns it (FR-11.14), reduced to what the read model keeps. AmountAcct is
// positive; DebitCredit ("D" or "C") gives the side. Negative marks a negative
// posting (storno, BR-11.16): the line reduces the opposite side instead of
// adding to its own. ReversalOfEntryID is set on the lines of a reversing entry.
type SourceLine struct {
	LineNumber        int               `json:"lineNumber"`
	Account           string            `json:"account"`
	Dimensions        map[string]string `json:"dimensions,omitempty"`
	DebitCredit       string            `json:"debitCredit"`
	AmountAcct        decimal.Decimal   `json:"amountAcct"`
	Negative          bool              `json:"negative,omitempty"`
	ReversalOfEntryID string            `json:"reversalOfEntryId,omitempty"`
	SourceURI         string            `json:"sourceUri,omitempty"`
	TraceID           string            `json:"traceId,omitempty"`
}

// PostedEntry is one journal.posted event with its lines, ready for the read
// model. PeriodKey is the ledger's YYYY-MM period; until the period calendar
// API exists (wave 14) the fiscal period is that calendar year and month.
// ReversesClosing is set by the store when the entry reverses an entry it
// applied as a closing entry, so a year-end re-run takes the closing amounts
// back out of closing_net.
type PostedEntry struct {
	LedgerID          string       `json:"ledgerId"`
	LegalEntityID     string       `json:"legalEntityId"`
	EntryID           string       `json:"entryId"`
	PostingSequence   int64        `json:"postingSequence"`
	PostingDate       string       `json:"postingDate"`
	PeriodKey         string       `json:"periodKey"`
	Voucher           string       `json:"voucher"`
	SourceSystem      string       `json:"sourceSystem"`
	SourceType        string       `json:"sourceType"`
	ReversalOfEntryID string       `json:"reversalOfEntryId,omitempty"`
	ReversesClosing   bool         `json:"-"`
	Lines             []SourceLine `json:"lines"`
}

// Closing reports whether the entry's lines are closing entries: a year-end
// closing posted by the close service, or the reversal of one.
func (e PostedEntry) Closing() bool {
	return (e.SourceSystem == CloseSourceSystem && e.SourceType == YearEndClosingSource) || e.ReversesClosing
}

// BalanceDelta is the change one entry makes to one reporting_balance row.
type BalanceDelta struct {
	PeriodYear  int
	PeriodNo    int
	Quarter     int
	Account     string
	Combination string
	Dims        map[string]string
	Net         decimal.Decimal
	ClosingNet  decimal.Decimal
}

// Combination is the canonical key of a dimension set: ATTR=value pairs sorted
// by attribute and joined with ";", or "-" when the line has no dimension.
func Combination(dims map[string]string) string {
	if len(dims) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(dims))
	for k := range dims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + dims[k]
	}
	return strings.Join(parts, ";")
}

// ReadModelSourceURI is the drill-down link the read model keeps for a ledger
// source link: a passage:// link becomes its Passage route, a safe link stays,
// and anything else is dropped. A link never refuses an entry: the journal is
// the truth and an entry the read model skips would stop its ledger.
func ReadModelSourceURI(s string) string {
	if rest, ok := strings.CutPrefix(s, passageScheme); ok {
		s = "/" + rest
	}
	if !SafeSourceURI(s) {
		return ""
	}
	return s
}

// Build turns an entry into read-model lines and balance deltas in the
// accounting currency (currency type "acct"). It refuses an entry that does not
// balance, because the journal balances by construction and a gap here means a
// lost or altered line. A negative posting is stored as a negative amount on
// the side it reduces. Deltas come sorted by (account, combination) so
// concurrent consumers lock rows in one order.
func (e PostedEntry) Build() ([]JournalLine, []BalanceDelta, error) {
	m := periodKeyPattern.FindStringSubmatch(e.PeriodKey)
	if m == nil {
		return nil, nil, errf(CodeLineInvalid, "period key %q is not YYYY-MM", e.PeriodKey)
	}
	year, _ := strconv.Atoi(m[1])
	no, _ := strconv.Atoi(m[2])
	if e.PostingSequence < 1 || len(e.Lines) == 0 {
		return nil, nil, errf(CodeLineInvalid, "entry %s needs a positive posting sequence and at least one line", e.EntryID)
	}
	closing := e.Closing()
	lines := make([]JournalLine, 0, len(e.Lines))
	byKey := map[string]*BalanceDelta{}
	total := decimal.Zero
	for _, l := range e.Lines {
		if l.AmountAcct.IsNegative() {
			return nil, nil, errf(CodeLineInvalid, "line %d: amount must not be negative", l.LineNumber)
		}
		var debit, credit decimal.Decimal
		switch {
		case l.DebitCredit == "D" && !l.Negative:
			debit = l.AmountAcct
		case l.DebitCredit == "C" && !l.Negative:
			credit = l.AmountAcct
		case l.DebitCredit == "D": // negative debit-side posting reduces the credit side
			credit = l.AmountAcct.Neg()
		case l.DebitCredit == "C":
			debit = l.AmountAcct.Neg()
		default:
			return nil, nil, errf(CodeLineInvalid, "line %d: side %q is neither D nor C", l.LineNumber, l.DebitCredit)
		}
		combo := Combination(l.Dimensions)
		jl := JournalLine{
			LedgerID: e.LedgerID, LegalEntityID: e.LegalEntityID, PostingSequence: e.PostingSequence, LineNumber: l.LineNumber,
			PostingDate: e.PostingDate, PeriodYear: year, PeriodNo: no, Account: l.Account, Combination: combo,
			CurrencyType: "acct", Voucher: e.Voucher, Debit: debit, Credit: credit, ClosingEntry: closing,
			SourceURI: ReadModelSourceURI(l.SourceURI), TraceID: l.TraceID,
		}
		if err := jl.Validate(); err != nil {
			return nil, nil, err
		}
		lines = append(lines, jl)
		key := l.Account + "\x00" + combo
		d := byKey[key]
		if d == nil {
			dims := map[string]string{}
			for k, v := range l.Dimensions {
				dims[k] = v
			}
			d = &BalanceDelta{PeriodYear: year, PeriodNo: no, Quarter: (no-1)/3 + 1, Account: l.Account, Combination: combo, Dims: dims}
			byKey[key] = d
		}
		d.Net = d.Net.Add(jl.Net())
		if closing {
			d.ClosingNet = d.ClosingNet.Add(jl.Net())
		}
		total = total.Add(jl.Net())
	}
	if !total.IsZero() {
		return nil, nil, errf(CodeLineInvalid, "entry %s does not balance in the accounting currency: debit minus credit is %s", e.EntryID, total.String())
	}
	deltas := make([]BalanceDelta, 0, len(byKey))
	for _, d := range byKey {
		deltas = append(deltas, *d)
	}
	sort.Slice(deltas, func(i, j int) bool {
		if deltas[i].Account != deltas[j].Account {
			return deltas[i].Account < deltas[j].Account
		}
		return deltas[i].Combination < deltas[j].Combination
	})
	return lines, deltas, nil
}
