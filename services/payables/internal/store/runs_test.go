package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const ledger1 = "00000000-0000-0000-0000-0000000000b1"

func runInput(proposalID, le string) NewRun {
	return NewRun{LegalEntityID: le, ProposalID: proposalID, RequestedBy: "erin", RequestHash: "hash-1", LedgerID: ledger1,
		BankAccount: "1010", ControlAccount: "2000", DiscountAccount: "4900", FileRequest: json.RawMessage(`{}`)}
}

func approvedProposal(t *testing.T, s *Postgres, runID string, seedItems func(ctx context.Context)) (Proposal, context.Context) {
	t.Helper()
	alice := ctxAs(1, leA, "alice")
	seedItems(alice)
	created, _, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, runID))
	if err != nil {
		t.Fatal(err)
	}
	approved, err := s.ApproveProposal(ctxAs(1, leA, "bob"), created.ID, "bob", runDay)
	if err != nil {
		t.Fatal(err)
	}
	return approved, ctxAs(1, leA, "erin")
}

func threeItems(t *testing.T, s *Postgres) func(ctx context.Context) {
	return func(ctx context.Context) {
		discounted := invoice(leA, vendor1, "00000000-0000-0000-0000-000000009001", "2027-03-10")
		discounted.Amount, discounted.DiscountAmount = dec("100.00"), dec("2.00")
		day := "2027-03-31"
		discounted.DiscountDate = &day
		if _, _, err := s.CreateFromInvoice(ctx, discounted); err != nil {
			t.Fatal(err)
		}
		seed(t, s, ctx, leA, vendor1, 2, "2027-03-11", "USD", "50.00")
		seed(t, s, ctx, leA, vendor2, 3, "2027-03-12", "EUR", "200.00")
	}
}

func settledCount(t *testing.T, s *Postgres, ctx context.Context) (settled, events int) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select count(*) from ap_open_item where status = 'SETTLED' and open_amount = 0 and version = 2`).Scan(&settled); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select count(*) from outbox where type = 'dev.passage.payables.openitem.settled.v1'`).Scan(&events)
	})
	if err != nil {
		t.Fatal(err)
	}
	return settled, events
}

func runThrough(t *testing.T, s *Postgres, ctx context.Context, run PaymentRun, proposal Proposal) PaymentRun {
	t.Helper()
	payments := make([]domain.RunPayment, len(proposal.Groups))
	for i, g := range proposal.Groups {
		payments[i] = domain.RunPayment{GroupID: g.ID, VendorID: g.VendorID, Currency: g.Currency, Method: g.Method, EndToEndID: domain.PaymentNumber(g.ID), Amount: g.Net, Valued: g.Valued}
	}
	var err error
	if run, err = s.SaveRunFile(ctx, run.ID, RunFile{Number: "F1", SHA256: "abc", Content: "<xml/>", PaymentCount: len(payments), Payments: payments}); err != nil {
		t.Fatal(err)
	}
	if run, err = s.RecordJournal(ctx, run.ID, "entry-1"); err != nil {
		t.Fatal(err)
	}
	if run, err = s.SettleRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if run, err = s.CompleteRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestPaymentRun_ExecutesApprovedProposalExactlyOnce(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))

	run, created, err := s.StartRun(erin, runInput(proposal.ID, leA))
	if err != nil || !created || run.Status != domain.RunRunning {
		t.Fatalf("start: created=%v run=%+v err=%v", created, run, err)
	}
	if p, _ := s.GetProposal(erin, proposal.ID); p.Status != domain.ProposalExecuting {
		t.Fatalf("proposal = %s, want EXECUTING", p.Status)
	}
	again, created, err := s.StartRun(erin, runInput(proposal.ID, leA))
	if err != nil || created || again.ID != run.ID {
		t.Fatalf("replayed start: created=%v id=%s want %s err=%v", created, again.ID, run.ID, err)
	}
	conflicting := runInput(proposal.ID, leA)
	conflicting.RequestHash = "hash-2"
	if _, _, err := s.StartRun(erin, conflicting); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("different request on the same proposal = %v, want ErrRunConflict", err)
	}

	for pass := 1; pass <= 2; pass++ {
		done := runThrough(t, s, erin, run, proposal)
		if done.Status != domain.RunCompleted || len(done.Payments) != 2 || done.JournalEntryID == nil || *done.JournalEntryID != "entry-1" || done.SettledAt == nil {
			t.Fatalf("pass %d: run = %+v", pass, done)
		}
	}
	if p, _ := s.GetProposal(erin, proposal.ID); p.Status != domain.ProposalExecuted {
		t.Fatalf("proposal = %s, want EXECUTED", p.Status)
	}
	if settled, events := settledCount(t, s, erin); settled != 3 || events != 3 {
		t.Fatalf("settled items = %d, settled events = %d, want 3 and 3 after running every step twice", settled, events)
	}
	var paid int
	_ = dbx.WithTenantTx(erin, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(erin, `select count(*) from ap_payment`).Scan(&paid)
	})
	if paid != 2 {
		t.Fatalf("payments = %d, want one per group (2)", paid)
	}
	if got := candidateIDs(t, s, erin, leA, runDay); len(got) != 0 {
		t.Fatalf("candidates after execution = %v", got)
	}
}

func TestPaymentRun_StepsRunInOrder(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	run, _, err := s.StartRun(erin, runInput(proposal.ID, leA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordJournal(erin, run.ID, "e"); !errors.Is(err, ErrRunOutOfOrder) {
		t.Errorf("journal before file = %v", err)
	}
	if _, err := s.SettleRun(erin, run.ID); !errors.Is(err, ErrRunOutOfOrder) {
		t.Errorf("settle before journal = %v", err)
	}
	if _, err := s.CompleteRun(erin, run.ID); !errors.Is(err, ErrRunOutOfOrder) {
		t.Errorf("complete before settle = %v", err)
	}
	if _, err := s.GetRun(erin, "00000000-0000-0000-0000-00000000dead"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("unknown run = %v", err)
	}
}

func TestPaymentRun_FailBeforeJournalReleasesItemsForANewProposal(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	run, _, err := s.StartRun(erin, runInput(proposal.ID, leA))
	if err != nil {
		t.Fatal(err)
	}
	failed, err := s.FailRun(erin, run.ID, "bank file rejected")
	if err != nil || failed.Status != domain.RunFailed || failed.FailureReason == nil || *failed.FailureReason != "bank file rejected" {
		t.Fatalf("fail: %+v err=%v", failed, err)
	}
	if again, err := s.FailRun(erin, run.ID, "other"); err != nil || *again.FailureReason != "bank file rejected" {
		t.Fatalf("second fail = %+v %v", again, err)
	}
	if p, _ := s.GetProposal(erin, proposal.ID); p.Status != domain.ProposalFailed {
		t.Fatalf("proposal = %s, want FAILED", p.Status)
	}
	if got := candidateIDs(t, s, erin, leA, runDay); len(got) != 3 {
		t.Fatalf("candidates after failure = %v, want the 3 released items", got)
	}
	if _, _, err := s.CreateProposal(erin, proposalFor(t, s, erin, leA, "run-2")); err != nil {
		t.Fatalf("re-proposal over released items: %v", err)
	}
	if _, err := s.SaveRunFile(erin, run.ID, RunFile{}); !errors.Is(err, ErrRunState) {
		t.Fatalf("file step on a failed run = %v, want ErrRunState", err)
	}
	if _, err := s.CompleteRun(erin, run.ID); !errors.Is(err, ErrRunState) {
		t.Fatalf("complete on a failed run = %v, want ErrRunState", err)
	}
}

func TestPaymentRun_CannotFailOnceJournalIsPosted(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	run, _, _ := s.StartRun(erin, runInput(proposal.ID, leA))
	if _, err := s.SaveRunFile(erin, run.ID, RunFile{Number: "F1", SHA256: "abc", Content: "<xml/>"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordJournal(erin, run.ID, "entry-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailRun(erin, run.ID, "late"); !errors.Is(err, ErrJournalPosted) {
		t.Fatalf("fail after journal = %v, want ErrJournalPosted", err)
	}
	if got := candidateIDs(t, s, erin, leA, runDay); len(got) != 0 {
		t.Fatalf("items must stay reserved after the journal posted: %v", got)
	}
}

func TestPaymentRun_HoldPlacedAfterApprovalFailsTheRunAndReleasesLines(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	if err := s.SetPaymentHold(erin, proposal.Groups[0].Lines[0].OpenItemID, true); err != nil {
		t.Fatal(err)
	}
	run, created, err := s.StartRun(erin, runInput(proposal.ID, leA))
	if err != nil || !created || run.Status != domain.RunFailed || run.FailureReason == nil {
		t.Fatalf("start over a held item: %+v created=%v err=%v", run, created, err)
	}
	if p, _ := s.GetProposal(erin, proposal.ID); p.Status != domain.ProposalFailed {
		t.Fatalf("proposal = %s, want FAILED", p.Status)
	}
	if got := candidateIDs(t, s, erin, leA, runDay); len(got) != 2 {
		t.Fatalf("candidates = %v, want the two unheld items released", got)
	}
}

func TestPaymentRun_RefusesWhatItCannotRun(t *testing.T) {
	s := setup(t)
	alice := ctxAs(1, leA, "alice")
	threeItems(t, s)(alice)
	draft, _, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartRun(alice, runInput(draft.ID, leA)); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("draft = %v, want ErrNotApproved", err)
	}
	approved, err := s.ApproveProposal(ctxAs(1, leA, "bob"), draft.ID, "bob", runDay)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		ctx context.Context
		in  NewRun
	}{
		"other legal entity": {ctxAs(1, leB, "erin"), runInput(approved.ID, leB)},
		"other tenant":       {ctxAs(2, leA, "erin"), runInput(approved.ID, leA)},
		"scope mismatch":     {ctxAs(1, leB, "erin"), runInput(approved.ID, leA)},
	} {
		if _, _, err := s.StartRun(tc.ctx, tc.in); !errors.Is(err, ErrProposalNotFound) {
			t.Errorf("%s = %v, want ErrProposalNotFound", name, err)
		}
	}
	noDiscountAccount := runInput(approved.ID, leA)
	noDiscountAccount.DiscountAccount = ""
	if _, _, err := s.StartRun(alice, noDiscountAccount); !errors.Is(err, ErrDiscountAccount) {
		t.Fatalf("discounted proposal without a discount account = %v", err)
	}
	if p, _ := s.GetProposal(alice, approved.ID); p.Status != domain.ProposalApproved {
		t.Fatalf("a refused start must leave the proposal APPROVED, got %s", p.Status)
	}
}

func TestPaymentRun_RefusesNonPain001Methods(t *testing.T) {
	s := setup(t)
	alice := ctxAs(1, leA, "alice")
	seed(t, s, alice, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	in := proposalFor(t, s, alice, leA, "run-1")
	in.Groups[0].Method = "ACH"
	created, _, err := s.CreateProposal(alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveProposal(ctxAs(1, leA, "bob"), created.ID, "bob", runDay); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartRun(alice, runInput(created.ID, leA)); !errors.Is(err, ErrUnsupportedMethod) {
		t.Fatalf("ACH group = %v, want ErrUnsupportedMethod", err)
	}
}

func TestPaymentRun_ConcurrentStartsCreateOneRun(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	var wg sync.WaitGroup
	results := make([]bool, 6)
	ids := make([]string, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, created, err := s.StartRun(erin, runInput(proposal.ID, leA))
			if err != nil {
				t.Error(err)
			}
			results[i], ids[i] = created, run.ID
		}()
	}
	wg.Wait()
	created := 0
	for i, c := range results {
		if c {
			created++
		}
		if ids[i] != ids[0] {
			t.Fatalf("runs differ: %v", ids)
		}
	}
	if created != 1 {
		t.Fatalf("created = %d, want exactly 1 of 6 concurrent starts", created)
	}
}

func TestPaymentRun_ConcurrentStepsSettleOnce(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	run, _, _ := s.StartRun(erin, runInput(proposal.ID, leA))
	if _, err := s.SaveRunFile(erin, run.ID, RunFile{Number: "F1", SHA256: "abc", Content: "<xml/>"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordJournal(erin, run.ID, "entry-1"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.SettleRun(erin, run.ID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if settled, events := settledCount(t, s, erin); settled != 3 || events != 3 {
		t.Fatalf("settled = %d, events = %d, want 3 and 3 after 5 concurrent settle steps", settled, events)
	}
}

func TestPaymentRun_ChangedItemFailsSettlementWithoutPartialEffects(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	run, _, _ := s.StartRun(erin, runInput(proposal.ID, leA))
	_, _ = s.SaveRunFile(erin, run.ID, RunFile{Number: "F1", SHA256: "abc", Content: "<xml/>"})
	_, _ = s.RecordJournal(erin, run.ID, "entry-1")
	last := proposal.Groups[len(proposal.Groups)-1].Lines[0]
	if _, err := s.Settle(erin, last.OpenItemID, 1, dec("1.00"), "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleRun(erin, run.ID); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("settle over a changed item = %v, want ErrVersionConflict", err)
	}
	if settled, events := settledCount(t, s, erin); settled != 0 || events != 1 {
		t.Fatalf("settled = %d, events = %d: the failed settle step must roll back whole (1 event from the other settlement)", settled, events)
	}
}

func TestPaymentProposalGuard_AllowsOnlyTheExecutionTransitions(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	exec := func(sql string) error {
		return dbx.WithTenantTx(erin, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(erin, fmt.Sprintf(sql, proposal.ID))
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
	wantGuard("APPROVED to EXECUTED", exec(`update ap_payment_proposal set status = 'EXECUTED', executed_at = now() where id = '%s'`))
	wantGuard("APPROVED to FAILED", exec(`update ap_payment_proposal set status = 'FAILED', failed_at = now() where id = '%s'`))
	if _, _, err := s.StartRun(erin, runInput(proposal.ID, leA)); err != nil {
		t.Fatal(err)
	}
	wantGuard("EXECUTING to APPROVED", exec(`update ap_payment_proposal set status = 'APPROVED' where id = '%s'`))
	wantGuard("EXECUTING to CANCELLED", exec(`update ap_payment_proposal set status = 'CANCELLED', cancelled_by = 'x', cancelled_at = now() where id = '%s'`))
	wantGuard("release lines while EXECUTING", exec(`update ap_payment_proposal_line set reserved = false where proposal_id = '%s'`))
	wantGuard("rewrite approver while EXECUTING", exec(`update ap_payment_proposal set approved_by = 'mallory' where id = '%s'`))
	if err := exec(`update ap_payment_proposal set status = 'EXECUTED', executed_at = now() where id = '%s'`); err != nil {
		t.Fatalf("EXECUTING to EXECUTED: %v", err)
	}
	wantGuard("EXECUTED to FAILED", exec(`update ap_payment_proposal set status = 'FAILED', failed_at = now() where id = '%s'`))
	wantGuard("EXECUTED to APPROVED", exec(`update ap_payment_proposal set status = 'APPROVED' where id = '%s'`))
}

func TestPaymentRun_IsolatedByTenantAndLegalEntity(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	run, _, _ := s.StartRun(erin, runInput(proposal.ID, leA))
	for name, ctx := range map[string]context.Context{"other tenant": ctxAs(2, leA, "x"), "other legal entity": ctxAs(1, leB, "x")} {
		if _, err := s.GetRun(ctx, run.ID); !errors.Is(err, ErrRunNotFound) {
			t.Errorf("%s get = %v, want ErrRunNotFound", name, err)
		}
		if _, err := s.FailRun(ctx, run.ID, "x"); !errors.Is(err, ErrRunNotFound) {
			t.Errorf("%s fail = %v, want ErrRunNotFound", name, err)
		}
		if _, err := s.RunFileContent(ctx, run.ID); !errors.Is(err, ErrRunNotFound) {
			t.Errorf("%s file = %v, want ErrRunNotFound", name, err)
		}
	}
}

func TestSetPaymentHold_DoesNotBumpVersionLikeAVendorBlock(t *testing.T) {
	s := setup(t)
	alice := ctxAs(1, leA, "alice")
	item := seed(t, s, alice, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	created, _, err := s.CreateProposal(alice, proposalFor(t, s, alice, leA, "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaymentHold(alice, item.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveProposal(ctxAs(1, leA, "bob"), created.ID, "bob", runDay); err == nil {
		t.Fatal("approval over a held item must fail")
	}
	if err := s.SetPaymentHold(alice, item.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(alice, item.ID)
	if got.Version != item.Version {
		t.Fatalf("version = %d after hold and release, want %d unchanged", got.Version, item.Version)
	}
	if _, err := s.ApproveProposal(ctxAs(1, leA, "bob"), created.ID, "bob", runDay); err != nil {
		t.Fatalf("approval after the hold was released: %v", err)
	}
}
