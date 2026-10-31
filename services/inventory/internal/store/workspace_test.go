package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// The workspace tiles count each kind of open work of the caller's legal
// entity: a posted journal is not open, a negative row only once stock goes
// below zero, a reconciliation difference only while the latest run of that
// account and currency is a difference, and transfers per status.
func TestWorkspaceSummaryCountsOpenWorkPerLegalEntity(t *testing.T) {
	s, _, _ := setup(t)
	le := ids.New()
	ctx := entityCtx(1, le)
	other := entityCtx(1, ids.New())
	product, short := ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "100", "2", dims()), line(short, "5", "2", dims()))

	// One DRAFT and one SUBMITTED journal.
	if _, err := newJournal(ctx, s, domain.JournalMovement, line(product, "1", "2", dims())); err != nil {
		t.Fatal(err)
	}
	sub, err := newJournal(ctx, s, domain.JournalMovement, line(product, "2", "2", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionJournal(ctx, sub.ID, sub.Version, domain.JournalSubmitted); err != nil {
		t.Fatal(err)
	}
	// A negative on-hand row (issue of 8 against 5 with negative stock allowed).
	neg, err := newJournal(ctx, s, domain.JournalAdjustment, line(short, "-8", "2", dims()))
	if err != nil {
		t.Fatal(err)
	}
	allow := postParams()
	allow.Negative = domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}
	if _, err := s.PostJournal(ctx, neg.ID, neg.Version, allow); err != nil {
		t.Fatal(err)
	}
	// One open count sheet and one active reservation.
	plan := countPlan(t, s, ctx, false)
	if _, _, err := s.GenerateCountSheet(ctx, plan.ID, ids.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := reserve(ctx, s, product, "3", domain.PolicyManual, dims(), "ws-r1"); err != nil {
		t.Fatal(err)
	}
	// Two transfer orders: one CREATED, one SHIPPED.
	if _, _, err := s.CreateTransfer(ctx, "ws-t1", transferOrder(ctx, product, "4", false)); err != nil {
		t.Fatal(err)
	}
	shipped, _, err := s.CreateTransfer(ctx, "ws-t2", transferOrder(ctx, product, "5", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ShipTransfer(ctx, shipped.ID, shipped.Version, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	// Reconciliation: USD was a difference yesterday and matches today (resolved);
	// EUR is a difference on its latest run.
	before := time.Now()
	ledger := dec("1")
	yesterday := reconcileIn(ctx, "USD")
	yesterday.RunDate, yesterday.LedgerBalance = reconDay.AddDate(0, 0, -1), &ledger
	if run, err := s.Reconcile(ctx, yesterday); err != nil || run.Status != domain.ReconciliationDifference {
		t.Fatalf("yesterday %+v %v", run, err)
	}
	if run, err := s.Reconcile(ctx, reconcileIn(ctx, "USD")); err != nil || run.Status == domain.ReconciliationDifference {
		t.Fatalf("today %+v %v", run, err)
	}
	eur := reconcileIn(ctx, "EUR")
	eur.LedgerBalance = &ledger
	if run, err := s.Reconcile(ctx, eur); err != nil || run.Status != domain.ReconciliationDifference {
		t.Fatalf("eur %+v %v", run, err)
	}

	got, err := s.WorkspaceSummary(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	want := store.WorkspaceSummary{JournalsDraft: 1, JournalsAwaitingApproval: 1, NegativeOnHandRows: 1, OpenCountSheets: 1,
		ReconciliationDifferences: 1, ActiveReservations: 1}
	if got.JournalsDraft != want.JournalsDraft || got.JournalsAwaitingApproval != want.JournalsAwaitingApproval ||
		got.NegativeOnHandRows != want.NegativeOnHandRows || got.OpenCountSheets != want.OpenCountSheets || got.CountSheetsCounted != 0 ||
		got.ReconciliationDifferences != want.ReconciliationDifferences || got.ActiveReservations != want.ActiveReservations {
		t.Fatalf("own entity %+v", got)
	}
	if got.TransferOrdersByStatus["CREATED"] != 1 || got.TransferOrdersByStatus["SHIPPED"] != 1 || len(got.TransferOrdersByStatus) != 2 {
		t.Fatalf("transfers by status %+v", got.TransferOrdersByStatus)
	}
	if got.LastReconciliationAt == nil || got.LastReconciliationAt.Before(before.Add(-time.Minute)) {
		t.Fatalf("last reconciliation %v", got.LastReconciliationAt)
	}

	// The explicit legal entity (in any letter case) gives the same counts.
	if mine, err := s.WorkspaceSummary(ctx, strings.ToUpper(le)); err != nil || mine.JournalsDraft != 1 || mine.NegativeOnHandRows != 1 ||
		mine.ReconciliationDifferences != 1 || mine.TransferOrdersByStatus["SHIPPED"] != 1 {
		t.Fatalf("explicit own entity %v %+v", err, mine)
	}
	// Another legal entity of the tenant sees none of it (row-level security).
	theirs, err := s.WorkspaceSummary(other, "")
	if err != nil || theirs.JournalsDraft != 0 || theirs.JournalsAwaitingApproval != 0 || theirs.NegativeOnHandRows != 0 ||
		theirs.OpenCountSheets != 0 || theirs.ReconciliationDifferences != 0 || theirs.ActiveReservations != 0 ||
		len(theirs.TransferOrdersByStatus) != 0 || theirs.LastReconciliationAt != nil {
		t.Fatalf("other entity %v %+v", err, theirs)
	}
	// Naming a legal entity outside the caller's scope does not widen it.
	if none, err := s.WorkspaceSummary(other, le); err != nil || none.JournalsDraft != 0 || none.ActiveReservations != 0 {
		t.Fatalf("foreign filter %v %+v", err, none)
	}
	// Another tenant sees nothing either.
	if far, err := s.WorkspaceSummary(tenantWideCtx(2), ""); err != nil || far.JournalsDraft != 0 || far.NegativeOnHandRows != 0 {
		t.Fatalf("other tenant %v %+v", err, far)
	}
}

// A tenant-wide caller sees every legal entity and can narrow to one.
func TestWorkspaceSummaryTenantWideAndNarrowed(t *testing.T) {
	s, _, _ := setup(t)
	a, b := ids.New(), ids.New()
	for _, le := range []string{a, b} {
		if _, err := newJournal(entityCtx(1, le), s, domain.JournalMovement, line(ids.New(), "1", "2", dims())); err != nil {
			t.Fatal(err)
		}
	}
	wide := tenantWideCtx(1)
	if all, err := s.WorkspaceSummary(wide, ""); err != nil || all.JournalsDraft != 2 {
		t.Fatalf("tenant-wide %v %+v", err, all)
	}
	if one, err := s.WorkspaceSummary(wide, b); err != nil || one.JournalsDraft != 1 {
		t.Fatalf("narrowed %v %+v", err, one)
	}
}
