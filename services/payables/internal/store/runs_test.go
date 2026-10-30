package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const ledger1 = "00000000-0000-0000-0000-0000000000b1"

func runInput(proposalID, le string) NewRun {
	return NewRun{LegalEntityID: le, ProposalID: proposalID, RequestedBy: "erin", Today: runDay, LedgerID: ledger1,
		BankAccount: "1010", ControlAccount: "2000", DiscountAccount: "4900"}
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
	if run, err = s.BeginJournal(ctx, run.ID); err != nil {
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
	if _, err := s.BeginJournal(erin, run.ID); !errors.Is(err, ErrRunOutOfOrder) {
		t.Errorf("journal request before file = %v", err)
	}
	if _, err := s.RecordJournal(erin, run.ID, "e"); !errors.Is(err, ErrRunOutOfOrder) {
		t.Errorf("journal before request = %v", err)
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

func TestPaymentRun_CannotFailOnceTheJournalWasRequested(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	run, _, _ := s.StartRun(erin, runInput(proposal.ID, leA))
	if _, err := s.SaveRunFile(erin, run.ID, RunFile{Number: "F1", SHA256: "abc", Content: "<xml/>"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginJournal(erin, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailRun(erin, run.ID, "ledger answered slowly"); !errors.Is(err, ErrJournalRequested) {
		t.Fatalf("fail while the ledger call may be in flight = %v, want ErrJournalRequested", err)
	}
	if got := candidateIDs(t, s, erin, leA, runDay); len(got) != 0 {
		t.Fatalf("items must stay reserved once the journal was requested: %v", got)
	}
	exec := func(sql string) error {
		return dbx.WithTenantTx(erin, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(erin, sql)
			return err
		})
	}
	wantGuard := func(name string, err error) {
		t.Helper()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "55000" && pgErr.Code != "23514") {
			t.Errorf("%s: %v, want a guard or check error", name, err)
		}
	}
	wantGuard("fail the proposal in SQL", exec(fmt.Sprintf(`update ap_payment_proposal set status = 'FAILED', failed_at = now() where id = '%s'`, proposal.ID)))
	wantGuard("fail the run in SQL", exec(fmt.Sprintf(`update ap_payment_run set status = 'FAILED', failure_reason = 'x' where id = '%s'`, run.ID)))
	wantGuard("clear the request", exec(fmt.Sprintf(`update ap_payment_run set journal_requested_at = null where id = '%s'`, run.ID)))
	if _, err := s.RecordJournal(erin, run.ID, "entry-1"); err != nil {
		t.Fatal(err)
	}
	wantGuard("clear the entry id", exec(fmt.Sprintf(`update ap_payment_run set journal_entry_id = null, journal_posted_at = null where id = '%s'`, run.ID)))
	if _, err := s.RejectJournal(erin, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailRun(erin, run.ID, "late"); !errors.Is(err, ErrJournalRequested) {
		t.Fatalf("a posted journal can never be failed, even if a rejection was recorded: %v", err)
	}
}

func TestPaymentRun_RejectedJournalMayFailAndReleaseLines(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	run, _, _ := s.StartRun(erin, runInput(proposal.ID, leA))
	if _, err := s.RejectJournal(erin, run.ID); !errors.Is(err, ErrRunOutOfOrder) {
		t.Fatalf("reject before a request = %v", err)
	}
	_, _ = s.SaveRunFile(erin, run.ID, RunFile{Number: "F1", SHA256: "abc", Content: "<xml/>"})
	if _, err := s.BeginJournal(erin, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RejectJournal(erin, run.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := s.FailRun(erin, run.ID, "ledger rejected the journal")
	if err != nil || failed.Status != domain.RunFailed {
		t.Fatalf("fail after a definitive ledger rejection: %+v %v", failed, err)
	}
	if got := candidateIDs(t, s, erin, leA, runDay); len(got) != 3 {
		t.Fatalf("candidates = %v, want the released items", got)
	}
}

func TestPaymentRun_VerifyRunLinesCatchesAHoldBeforeTheFileIsBuilt(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	run, _, _ := s.StartRun(erin, runInput(proposal.ID, leA))
	if _, err := s.VerifyRunLines(erin, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaymentHold(erin, proposal.Groups[0].Lines[0].OpenItemID, true); err != nil {
		t.Fatal(err)
	}
	var changed *LinesChangedError
	if _, err := s.VerifyRunLines(erin, run.ID); !errors.As(err, &changed) || len(changed.OpenItemIDs) != 1 {
		t.Fatalf("verify over a held item = %v", err)
	}
}

func TestPaymentRun_ExecutorCannotBeTheCreatorOrTheApprover(t *testing.T) {
	s := setup(t)
	proposal, _ := approvedProposal(t, s, "run-1", threeItems(t, s))
	for _, user := range []string{"alice", "bob"} {
		in := runInput(proposal.ID, leA)
		in.RequestedBy = user
		if _, _, err := s.StartRun(ctxAs(1, leA, user), in); !errors.Is(err, ErrExecutorConflict) {
			t.Errorf("%s: %v, want ErrExecutorConflict", user, err)
		}
	}
	if p, _ := s.GetProposal(ctxAs(1, leA, "x"), proposal.ID); p.Status != domain.ProposalApproved {
		t.Fatalf("a refused executor left the proposal %s", p.Status)
	}
	raw := ctxAs(1, leA, "bob")
	err := dbx.WithTenantTx(raw, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(raw, `insert into ap_payment_run (tenant_id, id, legal_entity_id, proposal_id, status, requested_by, ledger_id, bank_account, control_account)
			values ($1, $2::uuid, $3::uuid, $4::uuid, 'RUNNING', 'bob', $5::uuid, '1010', '2000')`, testkit.TenantID(1), ids.New(), leA, proposal.ID, ledger1)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
		t.Fatalf("the database must refuse an approver as executor: %v", err)
	}
}

func TestPaymentRun_StartAfterTheRunDateIsRefused(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	late := runInput(proposal.ID, leA)
	late.Today = "2027-04-01"
	if _, _, err := s.StartRun(erin, late); !errors.Is(err, ErrRunDateElapsed) {
		t.Fatalf("start after the run date = %v, want ErrRunDateElapsed", err)
	}
	if run, created, err := s.StartRun(erin, runInput(proposal.ID, leA)); err != nil || !created {
		t.Fatalf("start on the run date: %+v %v", run, err)
	}
	if _, created, err := s.StartRun(erin, late); err != nil || created {
		t.Fatalf("replaying an existing run after its day stays allowed: created=%v %v", created, err)
	}
}

func TestProposalPayees_AreEncryptedBoundToTheirTenantAndShownMasked(t *testing.T) {
	s := setup(t)
	keys, _ := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)})
	s.WithFieldCrypt(keys)
	alice := ctxAs(1, leA, "alice")
	seed(t, s, alice, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	in := proposalFor(t, s, alice, leA, "run-1")
	in.Payees = map[string]domain.CreditorDTO{vendor1: {PartyDTO: domain.PartyDTO{Name: "Acme Supply Co"}, AccountDTO: domain.AccountDTO{IBAN: "DE89370400440532013000"}, BIC: "COBADEFF"}}
	created, _, err := s.CreateProposal(alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Payees) != 1 || created.Payees[0].Account != "****3000" || created.Payees[0].Bank != "****DEFF" || created.Payees[0].Name != "Acme Supply Co" {
		t.Fatalf("payees = %+v, want the masked summary the approver sees", created.Payees)
	}
	var sealed []byte
	_ = dbx.WithTenantTx(alice, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(alice, `select sealed from ap_payment_proposal_payee where proposal_id = $1::uuid`, created.ID).Scan(&sealed)
	})
	if len(sealed) == 0 || bytes.Contains(sealed, []byte("DE89")) || bytes.Contains(sealed, []byte("Acme")) {
		t.Fatalf("bank details must be encrypted at rest: %q", sealed)
	}
	full, err := s.ProposalPayees(alice, created.ID)
	if err != nil || full[vendor1].IBAN != "DE89370400440532013000" {
		t.Fatalf("decrypted = %+v err %v", full, err)
	}
	if _, err := s.ProposalPayees(ctxAs(2, leA, "x"), created.ID); err != nil {
		t.Fatalf("another tenant sees no rows: %v", err)
	}
	exec := func(sql string) error {
		return dbx.WithTenantTx(alice, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(alice, sql, created.ID)
			return err
		})
	}
	if err := exec(`update ap_payment_proposal_payee set sealed = '\x00' where proposal_id = $1::uuid`); err == nil {
		t.Fatal("the application role must not be able to rewrite payee bank details")
	}
	if err := exec(`delete from ap_payment_proposal_payee where proposal_id = $1::uuid`); err == nil {
		t.Fatal("the application role must not be able to delete payee bank details")
	}
	bare := setup(t)
	bareCtx := ctxAs(1, leA, "alice")
	seed(t, bare, bareCtx, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	if _, _, err := bare.CreateProposal(bareCtx, withPayees(proposalFor(t, bare, bareCtx, leA, "run-1"), in.Payees)); !errors.Is(err, ErrNoFieldCrypt) {
		t.Fatalf("without keys = %v, want ErrNoFieldCrypt", err)
	}
}

func withPayees(in NewProposal, payees map[string]domain.CreditorDTO) NewProposal {
	in.Payees = payees
	return in
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
	if _, err := s.BeginJournal(erin, run.ID); err != nil {
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
	_, _ = s.BeginJournal(erin, run.ID)
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
