package ledger

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

const MaxEntries = 1000

var (
	ErrUnbalanced  = errors.New("journal entry is not balanced")
	ErrInvalidLine = errors.New("invalid journal line")
	ErrCapacity    = errors.New("journal is full")
)

// Line is one debit or credit in minor currency units.
type Line struct {
	Account string
	Debit   int64
	Credit  int64
}

// Entry is an accepted balanced journal entry.
type Entry struct {
	ID    int64
	Lines []Line
}

// Balance is an account's debit-positive balance.
type Balance struct {
	Account string
	Balance int64
}

// Book stores accepted entries and their account totals.
type Book struct {
	mu         sync.Mutex
	nextID     int64
	entries    []Entry
	totals     map[string]int64
	maxEntries int
}

func NewBook() *Book {
	return NewBookMax(MaxEntries)
}

func NewBookMax(max int) *Book {
	if max < 1 {
		panic("max entries must be positive")
	}
	return &Book{totals: map[string]int64{}, maxEntries: max}
}

// Post accepts a balanced entry. The returned entry is a copy.
func (b *Book) Post(lines []Line) (Entry, error) {
	normalized, err := validate(lines)
	if err != nil {
		return Entry{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.entries) >= b.maxEntries {
		return Entry{}, ErrCapacity
	}
	updated := make(map[string]int64, len(normalized))
	for _, line := range normalized {
		current, seen := updated[line.Account]
		if !seen {
			current = b.totals[line.Account]
		}
		next, err := add(current, line.Debit-line.Credit)
		if err != nil {
			return Entry{}, err
		}
		updated[line.Account] = next
	}
	b.nextID++
	entry := Entry{ID: b.nextID, Lines: normalized}
	b.entries = append(b.entries, cloneLinesEntry(entry))
	for account, balance := range updated {
		b.totals[account] = balance
	}
	return cloneLinesEntry(entry), nil
}

// Entries returns a copy of every accepted entry. The book never stores more than its cap.
func (b *Book) Entries() []Entry {
	limit := b.maxEntries
	entries, _ := b.EntriesPage(0, limit)
	return entries
}

// EntriesPage copies one window of accepted entries. total is the number stored.
func (b *Book) EntriesPage(offset, limit int) ([]Entry, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := len(b.entries)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	if limit < 1 {
		limit = 1
	}
	end := offset + limit
	if end > total {
		end = total
	}
	window := b.entries[offset:end]
	out := make([]Entry, len(window))
	for i, entry := range window {
		out[i] = cloneLinesEntry(entry)
	}
	return out, total
}

// TrialBalance returns account balances sorted by account. The sum of
// balances is zero after every successful Post.
func (b *Book) TrialBalance() []Balance {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Balance, 0, len(b.totals))
	for account, balance := range b.totals {
		out = append(out, Balance{Account: account, Balance: balance})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
	return out
}

func validate(lines []Line) ([]Line, error) {
	if len(lines) < 2 {
		return nil, fmt.Errorf("%w: at least two lines are required", ErrInvalidLine)
	}
	out := make([]Line, len(lines))
	var debit, credit int64
	for i, line := range lines {
		account := strings.TrimSpace(line.Account)
		if account == "" {
			return nil, fmt.Errorf("%w: account is required", ErrInvalidLine)
		}
		if line.Debit < 0 || line.Credit < 0 {
			return nil, fmt.Errorf("%w: amounts must be zero or positive", ErrInvalidLine)
		}
		if line.Debit > 0 && line.Credit > 0 {
			return nil, fmt.Errorf("%w: a line cannot carry both a debit and a credit", ErrInvalidLine)
		}
		if line.Debit == 0 && line.Credit == 0 {
			return nil, fmt.Errorf("%w: line amount is zero", ErrInvalidLine)
		}
		out[i] = Line{Account: account, Debit: line.Debit, Credit: line.Credit}
		var err error
		debit, err = add(debit, line.Debit)
		if err != nil {
			return nil, err
		}
		credit, err = add(credit, line.Credit)
		if err != nil {
			return nil, err
		}
	}
	if debit != credit {
		return nil, ErrUnbalanced
	}
	return out, nil
}

func add(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, fmt.Errorf("%w: amount overflow", ErrInvalidLine)
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, fmt.Errorf("%w: amount overflow", ErrInvalidLine)
	}
	return a + b, nil
}

func cloneLinesEntry(entry Entry) Entry {
	lines := make([]Line, len(entry.Lines))
	copy(lines, entry.Lines)
	entry.Lines = lines
	return entry
}
