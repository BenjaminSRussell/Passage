package store

import (
	"sync"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

func newDesk(t *testing.T, s *Postgres, le string) CashDesk {
	t.Helper()
	d, err := s.CreateCashDesk(ctxFor(1, ""), NewCashDesk{LegalEntityID: le, Code: "FRONT-1", Name: "Front desk", Currency: "USD", MainAccountID: ids.New(), MaxBalance: "1000"}, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCashDesk_DayLifecycleCarriesTheCountForward(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	desk := newDesk(t, s, ids.New())

	if _, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "receipt", Amount: "10", Description: "x"}, ids.New()); problemCode(t, err) != domain.CodeDeskDayClosed {
		t.Fatalf("entry without an open day: %v", err)
	}
	day, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-02")
	if err != nil || day.OpeningBalance != "0.000000" && day.OpeningBalance != "0" {
		t.Fatalf("open = %+v, %v", day, err)
	}
	if _, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-03"); problemCode(t, err) != domain.CodeDeskDayOpen {
		t.Fatalf("second open day: %v", err)
	}
	if _, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "payment", Amount: "1", Description: "overdraw"}, ids.New()); problemCode(t, err) != domain.CodeDeskOverdrawn {
		t.Fatalf("payment from an empty desk: %v", err)
	}
	if _, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "receipt", Amount: "1000.01", Description: "too much"}, ids.New()); problemCode(t, err) != domain.CodeDeskOverLimit {
		t.Fatalf("receipt over the maximum: %v", err)
	}
	key := ids.New()
	first, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "receipt", Amount: "500", Description: "cash sale", Reference: "INV-1"}, key)
	if err != nil || first.LineNo != 1 || !dec(first.BalanceAfter).Equal(decimal.NewFromInt(500)) {
		t.Fatalf("first entry = %+v, %v", first, err)
	}
	replay, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "receipt", Amount: "500", Description: "cash sale", Reference: "INV-1"}, key)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the stored entry", replay, err)
	}
	if _, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "payment", Amount: "120.50", Description: "taxi"}, ids.New()); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCashDesk(ctx, desk.ID)
	if err != nil || got.Balance == nil || !dec(*got.Balance).Equal(dec("379.50")) || got.OpenDate == nil || *got.OpenDate != "2026-11-02" {
		t.Fatalf("desk = %+v, %v; want balance 379.50 on 2026-11-02", got, err)
	}
	entries, err := s.ListCashDeskEntries(ctx, desk.ID, "2026-11-02")
	if err != nil || len(entries) != 2 || !dec(entries[1].BalanceAfter).Equal(dec("379.50")) {
		t.Fatalf("entries = %+v, %v", entries, err)
	}

	// A count that differs needs a reason and is then kept.
	if _, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", dec("379"), ""); problemCode(t, err) != domain.CodeDeskDifference {
		t.Fatalf("close without a reason: %v", err)
	}
	closed, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", dec("379"), "a coin missing")
	if err != nil || closed.Status != "closed" || closed.Difference == nil || !dec(*closed.Difference).Equal(dec("-0.50")) || closed.DifferenceReason != "a coin missing" {
		t.Fatalf("closed = %+v, %v", closed, err)
	}
	if _, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", dec("379"), ""); problemCode(t, err) != domain.CodeDeskDayClosed {
		t.Fatalf("closing twice: %v", err)
	}
	// The count, not the book, opens the next day, and a day on or before the last closed one is refused.
	if _, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-02"); problemCode(t, err) != domain.CodeDeskDateOrder {
		t.Fatalf("reopening the closed date: %v", err)
	}
	next, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-03")
	if err != nil || !dec(next.OpeningBalance).Equal(dec("379")) {
		t.Fatalf("next day = %+v, %v; want opening 379", next, err)
	}
	days, err := s.ListCashDeskDays(ctx, desk.ID)
	if err != nil || len(days) != 2 || days[0].BusinessDate != "2026-11-03" || !dec(days[1].BookBalance).Equal(dec("379.50")) {
		t.Fatalf("days = %+v, %v", days, err)
	}
}

// Two payments that each fit the cash but not together: exactly one passes (the desk row is locked).
func TestCashDesk_ConcurrentPaymentsNeverOverdraw(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	desk := newDesk(t, s, ids.New())
	if _, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-02"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "receipt", Amount: "100", Description: "float"}, ids.New()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "payment", Amount: "70", Description: "p"}, ids.New())
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if problemCode(t, err) != domain.CodeDeskOverdrawn {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d payments passed, want exactly 1", ok)
	}
}

func TestCashDesk_ScopeAndDuplicates(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	desk := newDesk(t, s, le)
	if _, err := s.CreateCashDesk(ctxFor(1, ""), NewCashDesk{LegalEntityID: le, Code: "FRONT-1", Name: "Again", Currency: "USD", MainAccountID: ids.New(), MaxBalance: "10"}, ids.New()); problemCode(t, err) != "TRS-0550" {
		t.Fatalf("duplicate code: %v", err)
	}
	if _, err := s.GetCashDesk(ctxFor(2, ""), desk.ID); problemCode(t, err) != domain.CodeDeskNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if got, err := s.ListCashDesks(ctxFor(1, ""), le); err != nil || len(got) != 1 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if got, err := s.ListCashDesks(ctxFor(1, ""), ids.New()); err != nil || len(got) != 0 {
		t.Fatalf("other legal entity = %+v, %v", got, err)
	}
}
