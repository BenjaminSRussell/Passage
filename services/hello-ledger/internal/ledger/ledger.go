// Package ledger is the wave-1 placeholder journal: in-memory, balanced
// double-entry postings. Later waves replace this with the universal journal.
package ledger

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

var (
	ErrUnbalanced  = errors.New("journal entry is not balanced")
	ErrInvalidLine = errors.New("invalid journal line")
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
	mu      sync.Mutex
	nextID  int64
	entries []Entry
	totals  map[string]int64
}

func NewBook() *Book {
	return &Book{totals: map[string]int64{}}
}

// Post accepts a balanced entry. The returned entry is a copy.
func (b *Book) Post(lines []Line) (Entry, error) {
	normalized, err := validate(lines)
	if err != nil {
		return Entry{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	entry := Entry{ID: b.nextID, Lines: normalized}
	b.entries = append(b.entries, cloneLinesEntry(entry))
	for _, line := range normalized {
		b.totals[line.Account] += line.Debit - line.Credit
	}
	return cloneLinesEntry(entry), nil
}

// Entries returns a copy of accepted entries.
func (b *Book) Entries() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Entry, len(b.entries))
	for i, entry := range b.entries {
		out[i] = cloneLinesEntry(entry)
	}
	return out
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
		debit += line.Debit
		credit += line.Credit
	}
	if debit != credit {
		return nil, ErrUnbalanced
	}
	return out, nil
}

func cloneLinesEntry(entry Entry) Entry {
	lines := make([]Line, len(entry.Lines))
	copy(lines, entry.Lines)
	entry.Lines = lines
	return entry
}
