package consumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/reimbursement"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

const (
	reportID = "00000000-0000-0000-0000-0000000000e1"
	workerID = "00000000-0000-0000-0000-0000000000d1"
	runDay   = "2027-03-31"
)

func approvedEvent(t *testing.T, over func(*reimbursement.ReportApproved)) events.Event {
	t.Helper()
	d := reimbursement.ReportApproved{ReportID: reportID, LegalEntityID: le, WorkerID: workerID, Currency: "USD", Amount: "80.00", ApprovedOn: "2027-03-10"}
	if over != nil {
		over(&d)
	}
	evt, err := events.New(reimbursement.ReportApprovedType, "/passage/expense/expense", reportID, testkit.TenantID(1), d)
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func TestHandleExpenseReportApproved_OneOpenItemPerReportFromTheEventAmount(t *testing.T) {
	s := setup(t)
	for range 2 {
		if err := HandleExpenseReportApproved(context.Background(), s, approvedEvent(t, nil)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le})
	items, err := s.List(ctx, store.Filter{VendorID: workerID}, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v err=%v; want one open item for the employee payee", items, err)
	}
	got := items[0]
	if got.SourceInvoiceID != reportID || got.VendorID != workerID || !got.Amount.Equal(decimal.NewFromInt(80)) || got.Currency != "USD" || got.Status != "OPEN" || got.DueDate != "2027-03-10" {
		t.Fatalf("open item = %+v", got)
	}
	err = HandleExpenseReportApproved(context.Background(), s, approvedEvent(t, func(d *reimbursement.ReportApproved) { d.Amount = "81.00" }))
	if !errors.Is(err, store.ErrConflictingReplay) {
		t.Fatalf("redelivery with another amount = %v; want %v", err, store.ErrConflictingReplay)
	}
}

func TestHandleExpenseReportApproved_RejectsInvalidEvents(t *testing.T) {
	s := setup(t)
	cases := map[string]func(*reimbursement.ReportApproved){
		"zero amount":     func(d *reimbursement.ReportApproved) { d.Amount = "0" },
		"negative amount": func(d *reimbursement.ReportApproved) { d.Amount = "-5" },
		"bad amount":      func(d *reimbursement.ReportApproved) { d.Amount = "abc" },
		"bad date":        func(d *reimbursement.ReportApproved) { d.ApprovedOn = "03/10/2027" },
		"bad worker":      func(d *reimbursement.ReportApproved) { d.WorkerID = "" },
		"bad currency":    func(d *reimbursement.ReportApproved) { d.Currency = "usd" },
	}
	for name, over := range cases {
		if err := HandleExpenseReportApproved(context.Background(), s, approvedEvent(t, over)); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%s: err = %v; want ErrInvalidEvent", name, err)
		}
	}
	wrong, _ := events.New(InvoicePostedType, "x", reportID, testkit.TenantID(1), map[string]string{})
	if err := HandleExpenseReportApproved(context.Background(), s, wrong); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("wrong type: err = %v", err)
	}
}

func settledEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []reimbursement.OpenItemSettled {
	t.Helper()
	var out []reimbursement.OpenItemSettled
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select payload->'data' from outbox where type=$1`, reimbursement.OpenItemSettledType)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var d reimbursement.OpenItemSettled
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestApprovedExpenseReport_BecomesAnOpenItemAndAPaymentRunSettlesIt(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	keys, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	s := store.New(app).WithFieldCrypt(keys)
	if err := HandleExpenseReportApproved(context.Background(), s, approvedEvent(t, nil)); err != nil {
		t.Fatal(err)
	}
	as := func(user string) context.Context {
		return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: user, Duties: testkit.ScopeDuties(le)})
	}
	alice, bob, erin := as("alice"), as("bob"), as("erin")

	candidates, err := s.Candidates(alice, le, runDay)
	if err != nil || len(candidates) != 1 || candidates[0].VendorID != workerID {
		t.Fatalf("candidates = %+v err=%v; want the employee's open item", candidates, err)
	}
	runDate, _ := time.Parse(time.DateOnly, runDay)
	groups, err := domain.BuildProposal(domain.ProposalInput{RunDate: runDate, LegalEntityCurrency: "USD", DefaultMethod: "TRANSFER", Candidates: candidates})
	if err != nil || len(groups) != 1 || !groups[0].Net.Equal(decimal.NewFromInt(80)) {
		t.Fatalf("groups = %+v err=%v; want one payment of 80", groups, err)
	}
	proposal, _, err := s.CreateProposal(alice, store.NewProposal{LegalEntityID: le, RunDate: runDay, RunID: "run-exp", DueBy: runDay, RateDate: runDay,
		LegalEntityCurrency: "USD", PaymentMethod: "TRANSFER", RequestHash: "hash-exp", CreatedBy: "alice", Groups: groups})
	if err != nil {
		t.Fatal(err)
	}
	if proposal, err = s.ApproveProposal(bob, proposal.ID, "bob", runDay); err != nil {
		t.Fatal(err)
	}
	run, _, err := s.StartRun(erin, store.NewRun{LegalEntityID: le, ProposalID: proposal.ID, RequestedBy: "erin", Today: runDay,
		LedgerID: "00000000-0000-0000-0000-0000000000b1", BankAccount: "1010", ControlAccount: "2000", FileConfig: []byte(`{"debtorBic":"CHASUS33"}`)})
	if err != nil {
		t.Fatal(err)
	}
	payments := []domain.RunPayment{{GroupID: groups[0].ID, VendorID: workerID, Currency: "USD", Method: groups[0].Method, EndToEndID: domain.PaymentNumber(groups[0].ID), Amount: groups[0].Net, Valued: groups[0].Valued}}
	if run, err = s.SaveRunFile(erin, run.ID, store.RunFile{Number: "F1", SHA256: "abc", Content: "<xml/>", PaymentCount: 1, Payments: payments}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() (store.PaymentRun, error){
		func() (store.PaymentRun, error) { return s.BeginJournal(erin, run.ID) },
		func() (store.PaymentRun, error) { return s.RecordJournal(erin, run.ID, "entry-1") },
		func() (store.PaymentRun, error) { return s.SettleRun(erin, run.ID) },
		func() (store.PaymentRun, error) { return s.CompleteRun(erin, run.ID) },
	} {
		if run, err = step(); err != nil {
			t.Fatal(err)
		}
	}
	if len(run.Payments) != 1 || run.Payments[0].VendorID != workerID {
		t.Fatalf("run payments = %+v; want the employee's payment", run.Payments)
	}

	got := settledEvents(t, erin, app)
	if len(got) != 1 || got[0].SourceInvoiceID != reportID || got[0].VendorID != workerID || got[0].Status != reimbursement.StatusSettled ||
		got[0].Currency != "USD" || got[0].LegalEntityID != le || got[0].SettledAmount != "80" {
		t.Fatalf("settled events = %+v; want the report's open item settled in full for the employee", got)
	}
}
