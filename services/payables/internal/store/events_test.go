package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

func outboxPayloads(t *testing.T, s *Postgres, ctx context.Context, eventType string) []map[string]any {
	t.Helper()
	var out []map[string]any
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select payload->'data' from outbox where type = $1 order by id`, eventType)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			var data map[string]any
			if err := json.Unmarshal(raw, &data); err != nil {
				return err
			}
			out = append(out, data)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBackfill_ConcurrentRedeliveryNeitherFailsNorDuplicatesTheEvent(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	if _, _, err := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src1, "2027-03-31")); err != nil {
		t.Fatal(err)
	}
	groups := []string{groupA, groupB}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	got := make([]OpenItem, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := invoice(leA, vendor1, src1, "2027-03-31")
			in.VendorGroupID = groups[i%2]
			got[i], _, errs[i] = s.CreateFromInvoice(ctx, in)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("redelivery %d failed: %v", i, err)
		}
	}
	events := outboxPayloads(t, s, ctx, OpenItemGroupedType)
	if len(events) != 1 {
		t.Fatalf("grouped events = %d, want exactly one for the single assignment", len(events))
	}
	winner := events[0]["vendor_group_id"]
	for i, o := range got {
		if o.VendorGroupID != winner {
			t.Errorf("redelivery %d returned group %q, want the winner %v", i, o.VendorGroupID, winner)
		}
	}
}

func TestBackfill_EmitsGroupedEventOnlyWhenAssigning(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	in := invoice(leA, vendor1, src1, "2027-03-31")
	if _, _, err := s.CreateFromInvoice(ctx, in); err != nil {
		t.Fatal(err)
	}
	if n := len(outboxPayloads(t, s, ctx, OpenItemGroupedType)); n != 0 {
		t.Fatalf("grouped events before a group is known = %d", n)
	}
	in.VendorGroupID = groupA
	for range 2 {
		if _, _, err := s.CreateFromInvoice(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	events := outboxPayloads(t, s, ctx, OpenItemGroupedType)
	if len(events) != 1 || events[0]["vendor_group_id"] != groupA || events[0]["vendor_id"] != vendor1 {
		t.Fatalf("grouped events = %v", events)
	}
}

func TestHoldAndVendorBlock_EmitAuditedEventsOnlyOnChange(t *testing.T) {
	s := setup(t)
	ctx := ctxAs(1, leA, "hana")
	item := seed(t, s, ctx, leA, vendor1, 1, "2027-03-01", "USD", "100.00")

	for _, held := range []bool{true, true, false, false} {
		if err := s.SetPaymentHold(ctx, item.ID, held); err != nil {
			t.Fatal(err)
		}
	}
	held, released := outboxPayloads(t, s, ctx, OpenItemHeldType), outboxPayloads(t, s, ctx, OpenItemReleasedType)
	if len(held) != 1 || len(released) != 1 || held[0]["actor"] != "hana" || held[0]["payment_hold"] != true || released[0]["payment_hold"] != false || held[0]["vendor_id"] != vendor1 {
		t.Fatalf("held=%v released=%v", held, released)
	}

	if err := s.BlockVendor(ctx, vendor1, "sanctions review"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnblockVendor(ctx, vendor1); err != nil {
		t.Fatal(err)
	}
	blocked, unblocked := outboxPayloads(t, s, ctx, VendorBlockedType), outboxPayloads(t, s, ctx, VendorUnblockedType)
	if len(blocked) != 1 || blocked[0]["reason"] != "sanctions review" || blocked[0]["actor"] != "hana" || len(unblocked) != 1 || unblocked[0]["actor"] != "hana" {
		t.Fatalf("blocked=%v unblocked=%v", blocked, unblocked)
	}
	if err := s.UnblockVendor(ctx, vendor1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second unblock = %v, want ErrNotFound", err)
	}
	if n := len(outboxPayloads(t, s, ctx, VendorUnblockedType)); n != 1 {
		t.Fatalf("a refused unblock emitted an event: %d", n)
	}
}

func TestProposalLifecycle_EmitsCreatedApprovedCancelled(t *testing.T) {
	s := setup(t)
	alice, bob := ctxAs(1, leA, "alice"), ctxAs(1, leA, "bob")
	seed(t, s, alice, leA, vendor1, 1, "2027-03-01", "USD", "100.00")
	first, created, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-1"))
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if _, replayed, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-1")); err != nil || replayed {
		t.Fatalf("replay: created=%v err=%v", replayed, err)
	}
	if _, err := s.ApproveProposal(bob, first.ID, "bob", "2027-03-01"); err != nil {
		t.Fatal(err)
	}

	seed(t, s, alice, leA, vendor2, 2, "2027-03-01", "USD", "50.00")
	second, _, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelProposal(bob, second.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelProposal(bob, second.ID, "bob"); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("second cancel = %v", err)
	}

	createdEvents := outboxPayloads(t, s, alice, ProposalCreatedType)
	approved, cancelled := outboxPayloads(t, s, alice, ProposalApprovedType), outboxPayloads(t, s, alice, ProposalCancelledType)
	if len(createdEvents) != 2 || createdEvents[0]["proposal_id"] != first.ID || createdEvents[0]["actor"] != "alice" || createdEvents[0]["status"] != "DRAFT" ||
		createdEvents[0]["total_valued"] != "100" || createdEvents[0]["currency"] != "USD" || createdEvents[0]["run_id"] != "run-1" || createdEvents[0]["legal_entity_id"] != leA {
		t.Fatalf("created = %v", createdEvents)
	}
	if len(approved) != 1 || approved[0]["proposal_id"] != first.ID || approved[0]["actor"] != "bob" || approved[0]["status"] != "APPROVED" || approved[0]["run_date"] != runDay {
		t.Fatalf("approved = %v", approved)
	}
	if len(cancelled) != 1 || cancelled[0]["proposal_id"] != second.ID || cancelled[0]["actor"] != "bob" || cancelled[0]["status"] != "CANCELLED" {
		t.Fatalf("cancelled = %v", cancelled)
	}
}

func TestListProposals_FiltersOrdersAndPages(t *testing.T) {
	s := setup(t)
	alice, bob := ctxAs(1, leA, "alice"), ctxAs(1, leA, "bob")
	var made []Proposal
	for i, day := range []string{"2027-03-29", "2027-03-30", "2027-03-31"} {
		seed(t, s, alice, leA, vendor1, i+1, "2027-03-01", "USD", fmt.Sprintf("%d.00", 100*(i+1)))
		in := proposalFor(t, s, alice, leA, fmt.Sprintf("run-%d", i))
		in.RunDate = day
		p, _, err := s.CreateProposal(alice, in)
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, p)
	}
	if _, err := s.ApproveProposal(bob, made[1].ID, "bob", "2027-03-01"); err != nil {
		t.Fatal(err)
	}

	page, err := s.ListProposals(alice, ProposalFilter{LegalEntityID: leA}, 2)
	if err != nil || len(page) != 2 || page[0].ID != made[2].ID || page[1].ID != made[1].ID || page[0].TotalValued.String() != "300" {
		t.Fatalf("first page = %+v err=%v", page, err)
	}
	next, err := s.ListProposals(alice, ProposalFilter{LegalEntityID: leA, AfterRunDate: page[1].RunDate, AfterID: page[1].ID}, 2)
	if err != nil || len(next) != 1 || next[0].ID != made[0].ID {
		t.Fatalf("second page = %+v err=%v", next, err)
	}
	approved, err := s.ListProposals(alice, ProposalFilter{LegalEntityID: leA, Status: domain.ProposalApproved}, 10)
	if err != nil || len(approved) != 1 || approved[0].ID != made[1].ID || approved[0].ApprovedBy == nil || *approved[0].ApprovedBy != "bob" {
		t.Fatalf("approved = %+v err=%v", approved, err)
	}
	ranged, err := s.ListProposals(alice, ProposalFilter{LegalEntityID: leA, RunFrom: "2027-03-30", RunTo: "2027-03-30"}, 10)
	if err != nil || len(ranged) != 1 || ranged[0].ID != made[1].ID {
		t.Fatalf("ranged = %+v err=%v", ranged, err)
	}
	if other, err := s.ListProposals(ctxAs(2, leA, "eve"), ProposalFilter{LegalEntityID: leA}, 10); err != nil || len(other) != 0 {
		t.Fatalf("another tenant sees %+v err=%v", other, err)
	}
	if other, err := s.ListProposals(ctxAs(1, leB, "eve"), ProposalFilter{LegalEntityID: leA}, 10); err != nil || len(other) != 0 {
		t.Fatalf("another legal entity sees %+v err=%v", other, err)
	}
}

func TestMigration0009_DropsTheUnusedVendorGroupIndex(t *testing.T) {
	s := setup(t)
	var n int
	err := s.pool.QueryRow(context.Background(), `select count(*) from pg_indexes where indexname = 'ap_open_item_entity_vendor_group'`).Scan(&n)
	if err != nil || n != 0 {
		t.Fatalf("index count = %d err=%v", n, err)
	}
}
