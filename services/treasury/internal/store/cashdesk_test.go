package store

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
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
	if _, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", CloseDay{Counted: dec("379")}); problemCode(t, err) != domain.CodeDeskDifference {
		t.Fatalf("close without a reason: %v", err)
	}
	closed, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", CloseDay{Counted: dec("379"), Reason: "a coin missing"})
	if err != nil || closed.Status != "closed" || closed.Difference == nil || !dec(*closed.Difference).Equal(dec("-0.50")) || closed.DifferenceReason != "a coin missing" {
		t.Fatalf("closed = %+v, %v", closed, err)
	}
	if _, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", CloseDay{Counted: dec("379")}); problemCode(t, err) != domain.CodeDeskDayClosed {
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

func TestCashDesk_PostingStateBlocksCloseUntilEveryLineIsInTheLedger(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	desk := newDesk(t, s, ids.New())
	if _, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-02"); err != nil {
		t.Fatal(err)
	}
	bank := ids.New()
	e, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "receipt", Amount: "100", Description: "float", CounterAccount: "8000", PostPending: true}, ids.New())
	if err != nil || e.PostingStatus != "pending" || e.CounterAccount != "8000" {
		t.Fatalf("pending line = %+v, %v", e, err)
	}
	t2, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "payment", Amount: "40", Description: "deposit", BankAccountID: bank, PostPending: true}, ids.New())
	if err != nil || t2.BankAccountID == nil || *t2.BankAccountID != bank {
		t.Fatalf("transfer line = %+v, %v", t2, err)
	}
	if _, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", CloseDay{Counted: dec("60")}); problemCode(t, err) != domain.CodeDeskUnposted {
		t.Fatalf("close with pending lines: %v", err)
	}
	pend, err := s.PendingDeskPostings(ctx, desk.ID, "2026-11-02")
	if err != nil || len(pend.Entries) != 2 || pend.Entries[0].LineNo != 1 {
		t.Fatalf("pending = %+v, %v; want both lines in order", pend, err)
	}
	for _, p := range pend.Entries {
		if err := s.MarkDeskEntryPosted(ctx, desk.ID, p.ID, "led-"+p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkDeskEntryPosted(ctx, desk.ID, e.ID, "other"); err != nil {
		t.Fatalf("marking a posted line again must change nothing: %v", err)
	}
	list, err := s.ListCashDeskEntries(ctx, desk.ID, "2026-11-02")
	if err != nil || list[0].PostingStatus != "posted" || list[0].LedgerEntryID != "led-"+e.ID || list[0].PostedAt == nil {
		t.Fatalf("entries = %+v, %v; want the first ledger id kept", list, err)
	}
	day, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", CloseDay{Counted: dec("59"), Reason: "short", PostDifference: true})
	if err != nil || day.Status != "closed" || day.DifferencePosting != "pending" {
		t.Fatalf("close = %+v, %v; want closed with the difference pending", day, err)
	}
	pend, err = s.PendingDeskPostings(ctx, desk.ID, "2026-11-02")
	if err != nil || pend.Difference == nil || !dec(*pend.Difference).Equal(dec("-1")) {
		t.Fatalf("pending difference = %+v, %v", pend, err)
	}
	if err := s.MarkDeskDifferencePosted(ctx, desk.ID, "2026-11-02", "led-diff"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetCashDeskDay(ctx, desk.ID, "2026-11-02"); err != nil || got.DifferencePosting != "posted" || got.DifferenceLedgerEntryID != "led-diff" || !dec(got.BookBalance).Equal(dec("60")) {
		t.Fatalf("day = %+v, %v", got, err)
	}
}

func TestCashDesk_DenominationsAreKeptAndDualControlNeedsAnotherPerson(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	desk := newDesk(t, s, ids.New())
	version := desk.Version
	if _, err := s.UpdateCashDesk(ctx, desk.ID, version, UpdateCashDesk{DualControl: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-02"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "receipt", Amount: "90", Description: "sale"}, ids.New()); err != nil {
		t.Fatal(err)
	}
	den := []domain.Denomination{{Value: "20", Count: 4}, {Value: "5", Count: 2}}
	day, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", CloseDay{Counted: dec("90"), Denominations: den})
	if err != nil || day.Status != "closing" || day.ClosedAt != nil || len(day.Denominations) != 2 || day.Denominations[0].Value != "20" {
		t.Fatalf("close = %+v, %v; want closing with the count lines and no close time", day, err)
	}
	// A counted day blocks new lines and the next day, but the desk shows it as the open one.
	if _, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "receipt", Amount: "1", Description: "late"}, ids.New()); problemCode(t, err) != domain.CodeDeskDayClosed {
		t.Fatalf("entry on a counted day: %v", err)
	}
	if _, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-03"); problemCode(t, err) != domain.CodeDeskDayOpen {
		t.Fatalf("next day while closing: %v", err)
	}
	if got, err := s.GetCashDesk(ctx, desk.ID); err != nil || got.OpenDate == nil || *got.OpenDate != "2026-11-02" {
		t.Fatalf("desk = %+v, %v", got, err)
	}
	if _, err := s.ApproveCashDeskDay(ctx, desk.ID, "2026-11-02", false); problemCode(t, err) != domain.CodeDeskSameSigner {
		t.Fatalf("approving your own count: %v", err)
	}
	p, _ := tenant.FromContext(ctx)
	p.UserID = "second-signer"
	other := tenant.With(ctx, p)
	approved, err := s.ApproveCashDeskDay(other, desk.ID, "2026-11-02", false)
	if err != nil || approved.Status != "closed" || approved.ApprovedBy != "second-signer" || approved.ClosedAt == nil {
		t.Fatalf("approve = %+v, %v", approved, err)
	}
	if _, err := s.ApproveCashDeskDay(other, desk.ID, "2026-11-02", false); problemCode(t, err) != domain.CodeDeskNotClosing {
		t.Fatalf("approving a closed day: %v", err)
	}
	next, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-03")
	if err != nil || !dec(next.OpeningBalance).Equal(dec("90")) {
		t.Fatalf("next day = %+v, %v", next, err)
	}
}

func TestCashDesk_RejectSendsACountedDayBackToOpen(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	desk := newDesk(t, s, ids.New())
	if _, err := s.UpdateCashDesk(ctx, desk.ID, desk.Version, UpdateCashDesk{DualControl: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-02"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RejectCashDeskDay(ctx, desk.ID, "2026-11-02"); problemCode(t, err) != domain.CodeDeskNotClosing {
		t.Fatalf("rejecting an open day: %v", err)
	}
	if _, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", CloseDay{Counted: dec("5"), Reason: "found cash"}); err != nil {
		t.Fatal(err)
	}
	day, err := s.RejectCashDeskDay(ctx, desk.ID, "2026-11-02")
	if err != nil || day.Status != "open" || day.CountedBalance != nil || day.Difference != nil || len(day.Denominations) != 0 {
		t.Fatalf("reject = %+v, %v; want an open day with the count cleared", day, err)
	}
	if _, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", CloseDay{Counted: dec("0")}); err != nil {
		t.Fatalf("recount after a reject: %v", err)
	}
}

func TestCashDesk_EditAndDeactivate(t *testing.T) {
	s, _ := newStore(t)
	ctx := ctxFor(1, "")
	desk := newDesk(t, s, ids.New())
	if _, err := s.UpdateCashDesk(ctx, desk.ID, desk.Version+5, UpdateCashDesk{Name: ptr("x")}); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	acct := ids.New()
	got, err := s.UpdateCashDesk(ctx, desk.ID, desk.Version, UpdateCashDesk{Name: ptr("Back desk"), MaxBalance: ptr("2500"), MainAccountID: &acct})
	if err != nil || got.Name != "Back desk" || got.MainAccountID != acct || !dec(got.MaxBalance).Equal(dec("2500")) || got.Version != desk.Version+1 || got.Code != "FRONT-1" {
		t.Fatalf("update = %+v, %v", got, err)
	}
	if _, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-02"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "receipt", Amount: "800", Description: "cash"}, ids.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateCashDesk(ctx, desk.ID, got.Version, UpdateCashDesk{MaxBalance: ptr("500")}); problemCode(t, err) != domain.CodeDeskOverLimit {
		t.Fatalf("maximum below the open day's cash: %v", err)
	}
	if _, err := s.SetCashDeskStatus(ctx, desk.ID, got.Version, "inactive"); problemCode(t, err) != domain.CodeDeskHasOpenDay {
		t.Fatalf("deactivating with an open day: %v", err)
	}
	if _, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", CloseDay{Counted: dec("800")}); err != nil {
		t.Fatal(err)
	}
	off, err := s.SetCashDeskStatus(ctx, desk.ID, got.Version, "inactive")
	if err != nil || off.Status != "inactive" {
		t.Fatalf("deactivate = %+v, %v", off, err)
	}
	if _, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-03"); problemCode(t, err) != domain.CodeDeskInactive {
		t.Fatalf("opening a day on an inactive desk: %v", err)
	}
	if _, err := s.AddCashDeskEntry(ctx, NewCashDeskEntry{DeskID: desk.ID, Kind: "receipt", Amount: "1", Description: "x"}, ids.New()); err == nil {
		t.Fatal("an inactive desk took an entry")
	}
	if _, err := s.SetCashDeskStatus(ctx, desk.ID, off.Version, "inactive"); err == nil {
		t.Fatal("deactivating twice should be refused")
	}
	on, err := s.SetCashDeskStatus(ctx, desk.ID, off.Version, "active")
	if err != nil || on.Status != "active" {
		t.Fatalf("activate = %+v, %v", on, err)
	}
}

func TestCashDesk_PublishesEventsForTheDeskLifecycle(t *testing.T) {
	s, pool := newStore(t)
	_ = pool
	ctx := ctxFor(1, "")
	desk := newDesk(t, s, ids.New())
	if _, err := s.UpdateCashDesk(ctx, desk.ID, desk.Version, UpdateCashDesk{Name: ptr("Renamed")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenCashDeskDay(ctx, desk.ID, "2026-11-02"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseCashDeskDay(ctx, desk.ID, "2026-11-02", CloseDay{Counted: dec("0")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCashDeskStatus(ctx, desk.ID, desk.Version+1, "inactive"); err != nil {
		t.Fatal(err)
	}
	got := outboxTypes(t, pool, desk.ID)
	want := []string{"cashdesk.created", "cashdesk.updated", "cashdeskday.closed", "cashdesk.deactivated"}
	if len(got) != len(want) {
		t.Fatalf("events = %v; want %v", got, want)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("event %d = %s; want %s", i, got[i], w)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func outboxTypes(t *testing.T, pool *pgxpool.Pool, deskID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select type from outbox where aggregate_id = $1 order by id`, deskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		out = append(out, typ)
	}
	return out
}
