package ledgerclient

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const (
	CodeIdempotencyConflict = "LEDGER_IDEMPOTENCY_CONFLICT"
	CodeAlreadyReversed     = "LEDGER_ALREADY_REVERSED"
	CodeEntryNotFound       = "LEDGER_ENTRY_NOT_FOUND"
	CodePeriodClosed        = "LEDGER_PERIOD_CLOSED"
)

// Fake is an in-memory Ledger for tests: it enforces balance, idempotency per key and one reversal per entry.
type Fake struct {
	mu       sync.Mutex
	entries  []Stored
	byKey    map[string]int
	reversed map[string]string
	postErr  error
	lostErr  error
	ledgers  map[string]string
	closedTo string
}

type Stored struct {
	ID         string
	ReversalOf string
	Entry
}

func NewFake() *Fake {
	return &Fake{byKey: map[string]int{}, reversed: map[string]string{}, ledgers: map[string]string{}}
}

// FailPosts makes every Post return err until it is called with nil.
func (f *Fake) FailPosts(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postErr = err
}

// LoseAnswers makes every Post that the ledger accepts return err instead of the entry id until it is called with nil.
func (f *Fake) LoseAnswers(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lostErr = err
}

// RefuseClosedThrough makes Post and Reverse refuse, with LEDGER_PERIOD_CLOSED, any entry dated on or before date (YYYY-MM-DD)
// unless it carries PostClosed, the way the ledger refuses a closed period without duty ledger.period.postclosed.
func (f *Fake) RefuseClosedThrough(date string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedTo = date
}

func (f *Fake) refusesClosed(date string, postClosed bool) bool {
	return f.closedTo != "" && date <= f.closedTo && !postClosed
}

func (f *Fake) Entries() []Stored {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.entries)
}

func (f *Fake) Post(_ context.Context, e Entry) (string, error) {
	e, err := e.Rounded()
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postErr != nil {
		return "", f.postErr
	}
	if f.refusesClosed(e.PostingDate, e.PostClosed) {
		return "", &Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: CodePeriodClosed}
	}
	id, err := f.store("post", e, "")
	if err == nil && f.lostErr != nil {
		return "", f.lostErr
	}
	return id, err
}

func (f *Fake) store(op string, e Entry, reversalOf string) (string, error) {
	key := e.LegalEntityID + "|" + e.IdempotencyKey
	if i, seen := f.byKey[key]; seen {
		if !sameEntry(f.entries[i].Entry, e) {
			return "", &Error{Op: op, Status: http.StatusConflict, Code: CodeIdempotencyConflict}
		}
		return f.entries[i].ID, nil
	}
	id := ids.New()
	f.byKey[key] = len(f.entries)
	f.entries = append(f.entries, Stored{ID: id, ReversalOf: reversalOf, Entry: e})
	return id, nil
}

func sameEntry(a, b Entry) bool {
	return a.LedgerID == b.LedgerID && a.DocumentID == b.DocumentID && a.PostingDate == b.PostingDate && a.Currency == b.Currency &&
		slices.EqualFunc(a.Lines, b.Lines, func(x, y Line) bool {
			return x.Account == y.Account && x.Credit == y.Credit && x.Amount.Equal(y.Amount)
		})
}

func (f *Fake) Reverse(_ context.Context, r Reversal) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, done := f.reversed[r.EntryID]; done {
		if i, ok := f.byKey[r.LegalEntityID+"|"+r.IdempotencyKey]; ok && f.entries[i].ID == id {
			return id, nil
		}
		return "", &Error{Op: "reverse", Status: http.StatusConflict, Code: CodeAlreadyReversed}
	}
	i := slices.IndexFunc(f.entries, func(s Stored) bool { return s.ID == r.EntryID })
	if i < 0 {
		return "", &Error{Op: "reverse", Status: http.StatusNotFound, Code: CodeEntryNotFound}
	}
	orig := f.entries[i].Entry
	flipped := orig
	flipped.PostClosed = r.PostClosed
	flipped.IdempotencyKey = r.IdempotencyKey
	if r.PostingDate != "" {
		flipped.PostingDate = r.PostingDate
	}
	if f.refusesClosed(flipped.PostingDate, r.PostClosed) {
		return "", &Error{Op: "reverse", Status: http.StatusUnprocessableEntity, Code: CodePeriodClosed}
	}
	flipped.Lines = make([]Line, len(orig.Lines))
	for j, l := range orig.Lines {
		l.Credit = !l.Credit
		flipped.Lines[j] = l
	}
	id, err := f.store("reverse", flipped, r.EntryID)
	if err == nil {
		f.reversed[r.EntryID] = id
	}
	return id, err
}

// AddLedger registers a ledger and its legal entity for LegalEntity.
func (f *Fake) AddLedger(ledgerID, legalEntityID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ledgers[ledgerID] = legalEntityID
}

func (f *Fake) LegalEntity(_ context.Context, _, ledgerID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if le, ok := f.ledgers[ledgerID]; ok {
		return le, nil
	}
	return "", &Error{Op: "ledger", Status: http.StatusNotFound, Code: "LEDGER_NOT_FOUND"}
}

func (f *Fake) Balance(_ context.Context, q BalanceQuery) (Balance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := Balance{}
	for _, s := range f.entries {
		period := s.PostingDate[:min(len(s.PostingDate), len("2006-01"))]
		if s.LedgerID != q.LedgerID || (q.PeriodTo != "" && period > q.PeriodTo) || (q.PeriodFrom != "" && period < q.PeriodFrom) {
			continue
		}
		for _, l := range s.Lines {
			if l.Account != q.Account {
				continue
			}
			out.Currency = s.Currency
			if l.Credit {
				out.Credit = out.Credit.Add(l.Amount)
			} else {
				out.Debit = out.Debit.Add(l.Amount)
			}
		}
	}
	if out.Currency == "" {
		out.Currency = "USD"
		out.Debit, out.Credit = decimal.Zero, decimal.Zero
	}
	return out, nil
}

// SourceBalances sums, per account, the lines of entries with q.SourceType posted from q.DateFrom to q.DateTo.
func (f *Fake) SourceBalances(_ context.Context, q SourceQuery) ([]AccountBalance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sums := map[string]*AccountBalance{}
	for _, s := range f.entries {
		if s.LedgerID != q.LedgerID || s.SourceType != q.SourceType || (q.DateFrom != "" && s.PostingDate < q.DateFrom) || (q.DateTo != "" && s.PostingDate > q.DateTo) {
			continue
		}
		for _, l := range s.Lines {
			if (q.AccountFrom != "" && l.Account < q.AccountFrom) || (q.AccountTo != "" && l.Account > q.AccountTo) {
				continue
			}
			b := sums[l.Account]
			if b == nil {
				b = &AccountBalance{Account: l.Account}
				sums[l.Account] = b
			}
			if l.Credit {
				b.Credit = b.Credit.Add(l.Amount)
			} else {
				b.Debit = b.Debit.Add(l.Amount)
			}
		}
	}
	out := make([]AccountBalance, 0, len(sums))
	for _, b := range sums {
		out = append(out, *b)
	}
	return out, nil
}

// Balances sums the posted lines of each account from q.Account to q.AccountTo (string order, as the ledger compares) per period range.
func (f *Fake) Balances(_ context.Context, q BalanceQuery) (AccountBalances, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	to := q.AccountTo
	if to == "" {
		to = q.Account
	}
	sums := map[string]*AccountBalance{}
	out := AccountBalances{}
	for _, s := range f.entries {
		period := s.PostingDate[:min(len(s.PostingDate), len("2006-01"))]
		if s.LedgerID != q.LedgerID || (q.PeriodTo != "" && period > q.PeriodTo) || (q.PeriodFrom != "" && period < q.PeriodFrom) {
			continue
		}
		out.Currency = s.Currency
		for _, l := range s.Lines {
			if l.Account < q.Account || l.Account > to {
				continue
			}
			b := sums[l.Account]
			if b == nil {
				b = &AccountBalance{Account: l.Account}
				sums[l.Account] = b
			}
			if l.Credit {
				b.Credit = b.Credit.Add(l.Amount)
			} else {
				b.Debit = b.Debit.Add(l.Amount)
			}
		}
	}
	if out.Currency == "" {
		out.Currency = "USD"
	}
	for _, b := range sums {
		out.Rows = append(out.Rows, *b)
	}
	slices.SortFunc(out.Rows, func(a, b AccountBalance) int { return strings.Compare(a.Account, b.Account) })
	return out, nil
}
