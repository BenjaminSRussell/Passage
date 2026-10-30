package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	vendor3 = "00000000-0000-0000-0000-0000000000c3"
	runDay  = "2027-03-31"
)

func ctxAs(n int, le, user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: le, UserID: user, Duties: map[string]struct{}{}})
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func seed(t *testing.T, s *Postgres, ctx context.Context, le, vendor string, n int, due, currency, amount string) OpenItem {
	t.Helper()
	in := invoice(le, vendor, fmt.Sprintf("00000000-0000-0000-0000-%012d", 9000+n), due)
	in.Currency, in.Amount = currency, dec(amount)
	o, _, err := s.CreateFromInvoice(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func candidateIDs(t *testing.T, s *Postgres, ctx context.Context, le, dueBy string) []string {
	t.Helper()
	got, err := s.Candidates(ctx, le, dueBy)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(got))
	for i, c := range got {
		out[i] = c.ID
	}
	return out
}

func proposalFor(t *testing.T, s *Postgres, ctx context.Context, le, runID string) NewProposal {
	t.Helper()
	candidates, err := s.Candidates(ctx, le, runDay)
	if err != nil {
		t.Fatal(err)
	}
	runDate, _ := time.Parse(time.DateOnly, runDay)
	groups, err := domain.BuildProposal(domain.ProposalInput{
		RunDate: runDate, LegalEntityCurrency: "USD", DefaultMethod: "TRANSFER",
		Rates: map[string]decimal.Decimal{"EUR": dec("1.0825")}, Candidates: candidates,
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewProposal{LegalEntityID: le, RunDate: runDay, RunID: runID, DueBy: runDay, RateDate: runDay, LegalEntityCurrency: "USD",
		PaymentMethod: "TRANSFER", RequestHash: "hash-" + runID, CreatedBy: "alice", Groups: groups}
}

func TestCandidates_SelectsDueUnheldUnblockedUnreserved(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	i1 := seed(t, s, ctx, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	i2 := seed(t, s, ctx, leA, vendor1, 2, "2027-03-31", "USD", "100.00")
	seed(t, s, ctx, leA, vendor1, 3, "2027-04-01", "USD", "100.00")
	held := seed(t, s, ctx, leA, vendor2, 4, "2027-03-05", "USD", "100.00")
	seed(t, s, ctx, leA, vendor3, 5, "2027-03-05", "USD", "100.00")
	settled := seed(t, s, ctx, leA, vendor1, 6, "2027-03-05", "USD", "100.00")
	partial := seed(t, s, ctx, leA, vendor1, 7, "2027-03-05", "USD", "100.00")
	seed(t, s, ctxFor(1, leB), leB, vendor1, 8, "2027-03-05", "USD", "100.00")
	if err := s.SetPaymentHold(ctx, held.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.BlockVendor(ctx, vendor3, "sanctions review"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(ctx, settled.ID, settled.Version, dec("100.00"), "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(ctx, partial.ID, partial.Version, dec("40.00"), "s2"); err != nil {
		t.Fatal(err)
	}

	got, err := s.Candidates(ctx, leA, runDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != partial.ID || got[1].ID != i1.ID || got[2].ID != i2.ID {
		t.Fatalf("candidates = %+v, want partial(03-05), i1(03-10), i2(03-31) in due order", got)
	}
	if !got[0].PartiallySettled || !got[0].OpenAmount.Equal(dec("60")) || got[1].PartiallySettled {
		t.Fatalf("partial flag / open amount wrong: %+v", got)
	}

	if err := s.SetPaymentHold(ctx, held.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.UnblockVendor(ctx, vendor3); err != nil {
		t.Fatal(err)
	}
	if n := len(candidateIDs(t, s, ctx, leA, runDay)); n != 5 {
		t.Fatalf("after releasing the hold and the block: %d candidates, want 5", n)
	}
	if err := s.SetPaymentHold(ctx, "00000000-0000-0000-0000-00000000dead", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("hold on a missing item = %v, want ErrNotFound", err)
	}
	if err := s.UnblockVendor(ctx, vendor1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unblock of an unblocked vendor = %v, want ErrNotFound", err)
	}
}

func TestCreateProposal_PersistsGroupsAndIsIdempotent(t *testing.T) {
	s := setup(t)
	ctx := ctxAs(1, leA, "alice")
	first := seed(t, s, ctx, leA, vendor1, 1, "2027-03-10", "USD", "1000.00")
	seed(t, s, ctx, leA, vendor1, 2, "2027-03-20", "USD", "500.00")
	seed(t, s, ctx, leA, vendor2, 3, "2027-03-20", "EUR", "200.00")
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update ap_open_item set discount_date = $2::date, discount_amount = 20.00 where id = $1::uuid`, first.ID, runDay)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	in := proposalFor(t, s, ctx, leA, "run-1")
	created, isNew, err := s.CreateProposal(ctx, in)
	if err != nil || !isNew {
		t.Fatalf("create: new=%v err=%v", isNew, err)
	}
	if created.Status != domain.ProposalDraft || created.CreatedBy != "alice" || created.ApprovedBy != nil || len(created.Groups) != 2 {
		t.Fatalf("created = %+v", created)
	}
	usd, eur := created.Groups[0], created.Groups[1]
	if usd.VendorID != vendor1 || !usd.Gross.Equal(dec("1500")) || !usd.Discount.Equal(dec("20")) || !usd.Net.Equal(dec("1480")) || !usd.Valued.Equal(dec("1480")) || len(usd.Lines) != 2 {
		t.Fatalf("vendor1 USD group = %+v, want gross 1500 discount 20 net 1480", usd)
	}
	if eur.Currency != "EUR" || !eur.Net.Equal(dec("200")) || !eur.Valued.Equal(dec("216.5")) || !eur.Lines[0].Rate.Equal(dec("1.0825")) {
		t.Fatalf("vendor2 EUR group = %+v, want valued 216.50 at 1.0825", eur)
	}
	if !created.TotalValued.Equal(dec("1696.5")) {
		t.Fatalf("total valued = %s, want 1696.5", created.TotalValued)
	}

	again, isNew, err := s.CreateProposal(ctx, proposalFor(t, s, ctx, leA, "run-1"))
	if err != nil || isNew || again.ID != created.ID {
		t.Fatalf("replay: new=%v id=%s want %s err=%v", isNew, again.ID, created.ID, err)
	}
	found, err := s.FindProposal(ctx, leA, runDay, "run-1")
	if err != nil || found.ID != created.ID || found.RequestHash != "hash-run-1" {
		t.Fatalf("find = %+v err=%v", found, err)
	}
	if left := candidateIDs(t, s, ctx, leA, runDay); len(left) != 0 {
		t.Fatalf("items on a proposal must not be selectable again: %v", left)
	}
}

func TestCreateProposal_RejectsItemsChangedAfterSelection(t *testing.T) {
	tests := map[string]func(t *testing.T, s *Postgres, ctx context.Context, item OpenItem){
		"settled": func(t *testing.T, s *Postgres, ctx context.Context, item OpenItem) {
			if _, err := s.Settle(ctx, item.ID, item.Version, dec("1.00"), "s"); err != nil {
				t.Fatal(err)
			}
		},
		"held": func(t *testing.T, s *Postgres, ctx context.Context, item OpenItem) {
			if err := s.SetPaymentHold(ctx, item.ID, true); err != nil {
				t.Fatal(err)
			}
		},
		"vendor blocked": func(t *testing.T, s *Postgres, ctx context.Context, item OpenItem) {
			if err := s.BlockVendor(ctx, item.VendorID, "fraud"); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			s := setup(t)
			ctx := ctxAs(1, leA, "alice")
			item := seed(t, s, ctx, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
			seed(t, s, ctx, leA, vendor2, 2, "2027-03-10", "USD", "100.00")
			in := proposalFor(t, s, ctx, leA, "run-1")
			change(t, s, ctx, item)
			if _, _, err := s.CreateProposal(ctx, in); !errors.Is(err, ErrItemsChanged) {
				t.Fatalf("err = %v, want ErrItemsChanged", err)
			}
			if _, err := s.FindProposal(ctx, leA, runDay, "run-1"); !errors.Is(err, ErrProposalNotFound) {
				t.Fatalf("a failed create must leave nothing behind, find = %v", err)
			}
		})
	}
}

func TestCreateProposal_ItemAlreadyOnAnotherProposal(t *testing.T) {
	s := setup(t)
	ctx := ctxAs(1, leA, "alice")
	seed(t, s, ctx, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	a, b := proposalFor(t, s, ctx, leA, "run-a"), proposalFor(t, s, ctx, leA, "run-b")
	if _, _, err := s.CreateProposal(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateProposal(ctx, b); !errors.Is(err, ErrItemsChanged) {
		t.Fatalf("second proposal over the same item = %v, want ErrItemsChanged", err)
	}
	if _, err := s.FindProposal(ctx, leA, runDay, "run-b"); !errors.Is(err, ErrProposalNotFound) {
		t.Fatalf("losing proposal persisted: %v", err)
	}
}

func TestApproveProposal_SegregationOfDuties(t *testing.T) {
	s := setup(t)
	alice := ctxAs(1, leA, "alice")
	seed(t, s, alice, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	created, _, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-1"))
	if err != nil {
		t.Fatal(err)
	}

	var pgErr *pgconn.PgError
	direct := dbx.WithTenantTx(alice, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(alice, `update ap_payment_proposal set status = 'APPROVED', approved_by = created_by, approved_at = now() where id = $1::uuid`, created.ID)
		return err
	})
	if !errors.As(direct, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("database accepted self-approval: %v", direct)
	}
	tamper := dbx.WithTenantTx(alice, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(alice, `update ap_payment_proposal set created_by = 'bob' where id = $1::uuid`, created.ID)
		return err
	})
	if !errors.As(tamper, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("app role rewrote created_by: %v", tamper)
	}

	if _, err := s.ApproveProposal(alice, created.ID, "alice", runDay); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("creator approval = %v, want ErrSelfApproval", err)
	}
	if still, _ := s.GetProposal(alice, created.ID); still.Status != domain.ProposalDraft {
		t.Fatalf("status after refused approval = %s", still.Status)
	}
	approved, err := s.ApproveProposal(alice, created.ID, "bob", runDay)
	if err != nil || approved.Status != domain.ProposalApproved || approved.ApprovedBy == nil || *approved.ApprovedBy != "bob" || approved.ApprovedAt == nil {
		t.Fatalf("approved = %+v err=%v", approved, err)
	}
	if _, err := s.ApproveProposal(alice, created.ID, "carol", runDay); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("second approval = %v, want ErrNotDraft", err)
	}
	if _, err := s.ApproveProposal(alice, "00000000-0000-0000-0000-00000000dead", "bob", runDay); !errors.Is(err, ErrProposalNotFound) {
		t.Fatalf("unknown proposal = %v, want ErrProposalNotFound", err)
	}
}

func TestProposal_TenantAndLegalEntityIsolation(t *testing.T) {
	s := setup(t)
	alice := ctxAs(1, leA, "alice")
	seed(t, s, alice, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	created, _, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{"other tenant": ctxAs(2, leA, "bob"), "other legal entity": ctxAs(1, leB, "bob")} {
		if _, err := s.GetProposal(ctx, created.ID); !errors.Is(err, ErrProposalNotFound) {
			t.Errorf("%s get = %v, want ErrProposalNotFound", name, err)
		}
		if _, err := s.FindProposal(ctx, leA, runDay, "run-1"); !errors.Is(err, ErrProposalNotFound) {
			t.Errorf("%s find = %v, want ErrProposalNotFound", name, err)
		}
		if _, err := s.ApproveProposal(ctx, created.ID, "bob", runDay); !errors.Is(err, ErrProposalNotFound) {
			t.Errorf("%s approve = %v, want ErrProposalNotFound", name, err)
		}
		if got, _ := s.Candidates(ctx, leA, runDay); len(got) != 0 {
			t.Errorf("%s candidates = %d, want 0", name, len(got))
		}
	}
	if _, _, err := s.CreateProposal(ctxAs(1, leB, "bob"), NewProposal{LegalEntityID: leA, RunDate: runDay, RunID: "x", DueBy: runDay, RateDate: runDay,
		LegalEntityCurrency: "USD", PaymentMethod: "TRANSFER", RequestHash: "h", CreatedBy: "bob"}); err == nil {
		t.Fatal("proposal for a legal entity outside the caller's scope was created")
	}
}

func TestList_FiltersAndKeysetPagination(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, "")
	a1 := seed(t, s, ctx, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	a2 := seed(t, s, ctx, leA, vendor1, 2, "2027-03-10", "EUR", "100.00")
	a3 := seed(t, s, ctx, leA, vendor2, 3, "2027-04-10", "USD", "100.00")
	seed(t, s, ctx, leB, vendor1, 4, "2027-03-10", "USD", "100.00")
	if _, err := s.Settle(ctx, a3.ID, a3.Version, dec("100.00"), "s"); err != nil {
		t.Fatal(err)
	}

	ids := func(f Filter) []string {
		got, err := s.List(ctx, f, 10)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(got))
		for i, o := range got {
			out[i] = o.ID
		}
		return out
	}
	tests := []struct {
		name string
		f    Filter
		want int
	}{
		{"legal entity", Filter{LegalEntityID: leA}, 3},
		{"currency", Filter{LegalEntityID: leA, Currency: "EUR"}, 1},
		{"status settled", Filter{LegalEntityID: leA, Status: "SETTLED"}, 1},
		{"status open", Filter{LegalEntityID: leA, Status: "OPEN"}, 2},
		{"due from", Filter{LegalEntityID: leA, DueFrom: "2027-04-01"}, 1},
		{"due range", Filter{LegalEntityID: leA, DueFrom: "2027-03-01", DueOnOrBy: "2027-03-31"}, 2},
		{"vendor", Filter{LegalEntityID: leA, VendorID: vendor2}, 1},
		{"no match", Filter{LegalEntityID: leA, Currency: "GBP"}, 0},
	}
	for _, tc := range tests {
		if got := ids(tc.f); len(got) != tc.want {
			t.Errorf("%s: %d items, want %d", tc.name, len(got), tc.want)
		}
	}

	first, err := s.List(ctx, Filter{LegalEntityID: leA}, 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("page 1 = %d err=%v", len(first), err)
	}
	last := first[1]
	rest, err := s.List(ctx, Filter{LegalEntityID: leA, AfterDue: last.DueDate, AfterID: last.ID}, 2)
	if err != nil || len(rest) != 1 {
		t.Fatalf("page 2 = %d err=%v, want the single remaining item", len(rest), err)
	}
	seen := map[string]bool{first[0].ID: true, first[1].ID: true, rest[0].ID: true}
	if len(seen) != 3 || !seen[a1.ID] || !seen[a2.ID] || !seen[a3.ID] {
		t.Fatalf("pages overlap or miss items: %v", seen)
	}

	got, err := s.Get(ctx, a1.ID)
	if err != nil || got.ID != a1.ID || got.PaymentHold {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	if _, err := s.Get(ctx, "00000000-0000-0000-0000-00000000dead"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing = %v", err)
	}
	if _, err := s.Get(ctxFor(2, ""), a1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get across tenants = %v", err)
	}
}

func TestApproveProposal_RechecksLinesAtApproval(t *testing.T) {
	tests := map[string]func(t *testing.T, s *Postgres, ctx context.Context, item OpenItem){
		"held": func(t *testing.T, s *Postgres, ctx context.Context, item OpenItem) {
			if err := s.SetPaymentHold(ctx, item.ID, true); err != nil {
				t.Fatal(err)
			}
		},
		"partially paid": func(t *testing.T, s *Postgres, ctx context.Context, item OpenItem) {
			if _, err := s.Settle(ctx, item.ID, item.Version, dec("10.00"), "s"); err != nil {
				t.Fatal(err)
			}
		},
		"fully settled": func(t *testing.T, s *Postgres, ctx context.Context, item OpenItem) {
			if _, err := s.Settle(ctx, item.ID, item.Version, dec("100.00"), "s"); err != nil {
				t.Fatal(err)
			}
		},
		"vendor blocked": func(t *testing.T, s *Postgres, ctx context.Context, item OpenItem) {
			if err := s.BlockVendor(ctx, item.VendorID, "fraud"); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			s := setup(t)
			ctx := ctxAs(1, leA, "alice")
			item := seed(t, s, ctx, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
			seed(t, s, ctx, leA, vendor2, 2, "2027-03-10", "USD", "100.00")
			created, _, err := s.CreateProposal(ctx, proposalFor(t, s, ctx, leA, "run-1"))
			if err != nil {
				t.Fatal(err)
			}
			change(t, s, ctx, item)
			_, err = s.ApproveProposal(ctx, created.ID, "bob", runDay)
			var changed *LinesChangedError
			if !errors.As(err, &changed) || len(changed.OpenItemIDs) != 1 || changed.OpenItemIDs[0] != item.ID {
				t.Fatalf("err = %v, want LinesChangedError naming only %s", err, item.ID)
			}
			if still, _ := s.GetProposal(ctx, created.ID); still.Status != domain.ProposalDraft {
				t.Fatalf("status = %s after refused approval", still.Status)
			}
		})
	}
}

func TestApproveProposal_SucceedsWhenLinesUnchangedOrRestored(t *testing.T) {
	s := setup(t)
	ctx := ctxAs(1, leA, "alice")
	item := seed(t, s, ctx, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	created, _, err := s.CreateProposal(ctx, proposalFor(t, s, ctx, leA, "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BlockVendor(ctx, item.VendorID, "review"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveProposal(ctx, created.ID, "bob", runDay); err == nil {
		t.Fatal("approved with a blocked vendor")
	}
	if err := s.UnblockVendor(ctx, item.VendorID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ApproveProposal(ctx, created.ID, "bob", runDay); err != nil || got.Status != domain.ProposalApproved {
		t.Fatalf("approve after unblock: %+v err=%v", got, err)
	}
}

func TestApproveProposal_RefusesAfterRunDate(t *testing.T) {
	s := setup(t)
	ctx := ctxAs(1, leA, "alice")
	seed(t, s, ctx, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	created, _, err := s.CreateProposal(ctx, proposalFor(t, s, ctx, leA, "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveProposal(ctx, created.ID, "bob", "2027-04-01"); !errors.Is(err, ErrRunDateElapsed) {
		t.Fatalf("approval the day after the run date = %v, want ErrRunDateElapsed", err)
	}
	if still, _ := s.GetProposal(ctx, created.ID); still.Status != domain.ProposalDraft {
		t.Fatalf("status = %s after refused approval", still.Status)
	}
	if got, err := s.ApproveProposal(ctx, created.ID, "bob", runDay); err != nil || got.Status != domain.ProposalApproved {
		t.Fatalf("approval on the run date: %+v err=%v", got, err)
	}
}

func TestCancelProposal_ReleasesItemsForANewProposal(t *testing.T) {
	s := setup(t)
	alice := ctxAs(1, leA, "alice")
	item := seed(t, s, alice, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	first, _, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateIDs(t, s, alice, leA, runDay); len(got) != 0 {
		t.Fatalf("candidates while reserved = %v", got)
	}
	cancelled, err := s.CancelProposal(alice, first.ID, "carol")
	if err != nil || cancelled.Status != domain.ProposalCancelled || cancelled.CancelledBy == nil || *cancelled.CancelledBy != "carol" || cancelled.CancelledAt == nil {
		t.Fatalf("cancelled = %+v err=%v", cancelled, err)
	}
	if len(cancelled.Groups) != 1 || len(cancelled.Groups[0].Lines) != 1 {
		t.Fatalf("a cancelled proposal keeps its lines for audit: %+v", cancelled.Groups)
	}
	if got := candidateIDs(t, s, alice, leA, runDay); len(got) != 1 || got[0] != item.ID {
		t.Fatalf("candidates after cancel = %v, want the released item", got)
	}
	second, isNew, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-2"))
	if err != nil || !isNew || len(second.Groups) != 1 {
		t.Fatalf("new proposal over released item: new=%v err=%v %+v", isNew, err, second)
	}
	if _, _, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-3")); err != nil && !errors.Is(err, ErrItemsChanged) {
		t.Fatal(err)
	}
	if _, err := s.CancelProposal(alice, first.ID, "carol"); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("second cancel = %v, want ErrNotDraft", err)
	}
	if _, err := s.ApproveProposal(alice, first.ID, "bob", runDay); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("approve of a cancelled proposal = %v, want ErrNotDraft", err)
	}
}

func TestCancelProposal_RefusesApprovedAndOutsiders(t *testing.T) {
	s := setup(t)
	alice := ctxAs(1, leA, "alice")
	seed(t, s, alice, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	created, _, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{"other tenant": ctxAs(2, leA, "bob"), "other legal entity": ctxAs(1, leB, "bob")} {
		if _, err := s.CancelProposal(ctx, created.ID, "bob"); !errors.Is(err, ErrProposalNotFound) {
			t.Errorf("%s cancel = %v, want ErrProposalNotFound", name, err)
		}
	}
	if _, err := s.ApproveProposal(alice, created.ID, "bob", runDay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelProposal(alice, created.ID, "alice"); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("cancel of an approved proposal = %v, want ErrNotDraft", err)
	}
	if got, _ := s.GetProposal(alice, created.ID); got.Status != domain.ProposalApproved {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestProposal_AppRoleCannotRewriteTerminalStateOrReleaseLines(t *testing.T) {
	s := setup(t)
	alice := ctxAs(1, leA, "alice")
	seed(t, s, alice, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	seed(t, s, alice, leA, vendor2, 2, "2027-03-10", "USD", "100.00")
	approved, _, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveProposal(alice, approved.ID, "bob", runDay); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) error {
		return dbx.WithTenantTx(alice, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(alice, sql, args...)
			return err
		})
	}
	wantGuard := func(name string, err error) {
		t.Helper()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Errorf("%s: %v, want a 55000 guard error", name, err)
		}
	}
	wantGuard("revert APPROVED to DRAFT", exec(`update ap_payment_proposal set status = 'DRAFT', approved_by = null, approved_at = null where id = $1::uuid`, approved.ID))
	wantGuard("rewrite approved_by", exec(`update ap_payment_proposal set approved_by = 'mallory' where id = $1::uuid`, approved.ID))
	wantGuard("cancel through SQL once approved", exec(`update ap_payment_proposal set status = 'CANCELLED', cancelled_by = 'x', cancelled_at = now() where id = $1::uuid`, approved.ID))
	if got, _ := s.GetProposal(alice, approved.ID); got.Status != domain.ProposalApproved || *got.ApprovedBy != "bob" {
		t.Fatalf("approved proposal changed: %+v", got)
	}

	draftCtx := ctxAs(1, leA, "alice")
	seed(t, s, draftCtx, leA, vendor1, 3, "2027-03-10", "USD", "100.00")
	draft, _, err := s.CreateProposal(draftCtx, proposalFor(t, s, draftCtx, leA, "run-2"))
	if err != nil {
		t.Fatal(err)
	}
	wantGuard("release a draft line without cancelling", exec(`update ap_payment_proposal_line set reserved = false where proposal_id = $1::uuid`, draft.ID))
	if _, err := s.CancelProposal(draftCtx, draft.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	wantGuard("re-reserve a released line", exec(`update ap_payment_proposal_line set reserved = true where proposal_id = $1::uuid`, draft.ID))
	wantGuard("revive a cancelled proposal", exec(`update ap_payment_proposal set status = 'DRAFT', cancelled_by = null, cancelled_at = null where id = $1::uuid`, draft.ID))
}
