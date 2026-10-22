package ledger

import (
	"errors"
	"math"
	"sync"
	"testing"
)

func balanced() []Line {
	return []Line{
		{Account: "Cash", Debit: 100, Credit: 0},
		{Account: "Revenue", Debit: 0, Credit: 100},
	}
}

func TestPostBalancedEntry(t *testing.T) {
	book := NewBook()
	entry, err := book.Post(balanced())
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != 1 {
		t.Fatalf("id = %d", entry.ID)
	}
	balances := book.TrialBalance()
	var total int64
	for _, balance := range balances {
		total += balance.Balance
	}
	if total != 0 {
		t.Fatalf("trial balance = %d", total)
	}
}

func TestRejectUnbalanced(t *testing.T) {
	book := NewBook()
	_, err := book.Post([]Line{
		{Account: "Cash", Debit: 100, Credit: 0},
		{Account: "Revenue", Debit: 0, Credit: 90},
	})
	if !errors.Is(err, ErrUnbalanced) {
		t.Fatalf("err = %v", err)
	}
	if len(book.Entries()) != 0 {
		t.Fatal("rejected entry was stored")
	}
}

func TestRejectInvalidLines(t *testing.T) {
	book := NewBook()
	cases := [][]Line{
		{{Account: "Cash", Debit: 10, Credit: 0}},
		{{Account: " ", Debit: 10, Credit: 0}, {Account: "Revenue", Debit: 0, Credit: 10}},
		{{Account: "Cash", Debit: -1, Credit: 0}, {Account: "Revenue", Debit: 0, Credit: -1}},
		{{Account: "Cash", Debit: 10, Credit: 10}, {Account: "Revenue", Debit: 0, Credit: 10}},
		{{Account: "Cash", Debit: 0, Credit: 0}, {Account: "Revenue", Debit: 10, Credit: 0}},
	}
	for i, lines := range cases {
		if _, err := book.Post(lines); !errors.Is(err, ErrInvalidLine) {
			t.Fatalf("case %d err = %v", i, err)
		}
	}
}

func TestRejectAmountOverflow(t *testing.T) {
	book := NewBook()
	_, err := book.Post([]Line{
		{Account: "Cash", Debit: math.MaxInt64, Credit: 0},
		{Account: "Revenue", Debit: 0, Credit: math.MaxInt64},
		{Account: "Cash", Debit: 1, Credit: 0},
		{Account: "Revenue", Debit: 0, Credit: 1},
	})
	if !errors.Is(err, ErrInvalidLine) {
		t.Fatalf("err = %v", err)
	}
	if len(book.Entries()) != 0 {
		t.Fatal("overflowing entry was stored")
	}
}

func TestRejectWhenFullAndPageWindow(t *testing.T) {
	book := NewBookMax(2)
	if _, err := book.Post(balanced()); err != nil {
		t.Fatal(err)
	}
	if _, err := book.Post(balanced()); err != nil {
		t.Fatal(err)
	}
	if _, err := book.Post(balanced()); !errors.Is(err, ErrCapacity) {
		t.Fatalf("err = %v", err)
	}
	page, total := book.EntriesPage(1, 1)
	if total != 2 || len(page) != 1 || page[0].ID != 2 {
		t.Fatalf("page=%v total=%d", page, total)
	}
	if got := len(book.Entries()); got != 2 {
		t.Fatalf("entries = %d", got)
	}
}

func TestConcurrentPostsStayBalanced(t *testing.T) {
	book := NewBook()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := book.Post(balanced()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var total int64
	for _, balance := range book.TrialBalance() {
		total += balance.Balance
	}
	if total != 0 {
		t.Fatalf("trial balance = %d", total)
	}
	if got := len(book.Entries()); got != 100 {
		t.Fatalf("entries = %d", got)
	}
}
