package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/reimbursement"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
	"github.com/BenjaminSRussell/passage/services/expense/internal/store"
)

func asUser(ctx context.Context, user string) context.Context {
	p, _ := tenant.FromContext(ctx)
	p.UserID = user
	return tenant.With(ctx, p)
}

func submittedReport(t *testing.T, s *store.Store, ctx context.Context, le string, l domain.Line) store.Report {
	t.Helper()
	rep := newReport(t, s, ctx, le, "create")
	rep, _, err := s.AddLine(ctx, rep.ID, "line-1", "h1", l, commitDay)
	if err != nil {
		t.Fatal(err)
	}
	rep, _, err = s.SubmitReport(ctx, rep.ID, "submit-1", "hs", 0, commitDay)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func approvedEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []reimbursement.ReportApproved {
	t.Helper()
	out := []reimbursement.ReportApproved{}
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select payload->'data' from outbox where type=$1`, reimbursement.ReportApprovedType)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var d reimbursement.ReportApproved
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

func TestDecideReport_ApprovalPublishesTheStoredReimbursableAmountOnce(t *testing.T) {
	s, ctx, le, pool := setup(t, 21)
	sam, dana := asUser(ctx, "sam"), asUser(ctx, "dana")
	l := line("TRAVEL", "Hotel", 80)
	l.HasReceipt = true
	rep := submittedReport(t, s, sam, le, l)

	_, _, err := s.DecideReport(sam, rep.ID, true, commitDay, store.Command{Key: "own", Hash: "h"})
	if p := problemOf(t, err); p.Code != store.CodeOwnDecision || p.Status != http.StatusForbidden {
		t.Fatalf("submitter deciding = %s/%d; want %s/403", p.Code, p.Status, store.CodeOwnDecision)
	}
	if _, _, err := s.DecideReport(dana, rep.ID, true, commitDay, store.Command{Key: "stale", Hash: "h", ExpectedVersion: rep.Version + 1}); problemOf(t, err).Code != store.CodeVersionConflict {
		t.Fatalf("stale If-Match must be %s", store.CodeVersionConflict)
	}
	if got := approvedEvents(t, ctx, pool); len(got) != 0 {
		t.Fatalf("refused decisions published %v", got)
	}

	c := store.Command{Key: "approve-1", Hash: "ha", ExpectedVersion: rep.Version}
	done, replayed, err := s.DecideReport(dana, rep.ID, true, commitDay, c)
	if err != nil || replayed || done.Status != "APPROVED" || done.Version != rep.Version+1 {
		t.Fatalf("approve = %+v replayed=%v err=%v; want APPROVED at the next version", done, replayed, err)
	}
	again, replayed, err := s.DecideReport(dana, rep.ID, true, commitDay, c)
	if err != nil || !replayed || again.Status != "APPROVED" {
		t.Fatalf("replay = %+v replayed=%v err=%v; want the stored outcome", again, replayed, err)
	}
	got := approvedEvents(t, ctx, pool)
	want := reimbursement.ReportApproved{ReportID: rep.ID, LegalEntityID: le, WorkerID: rep.WorkerID, Currency: "USD", Amount: "80.00", ApprovedOn: "2025-03-04"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("published = %+v; want exactly %+v", got, want)
	}
	if _, _, err := s.DecideReport(dana, rep.ID, false, commitDay, store.Command{Key: "reject-late", Hash: "hr"}); problemOf(t, err).Code != store.CodeReportState {
		t.Fatalf("deciding an APPROVED report must be %s", store.CodeReportState)
	}
}

func TestDecideReport_RejectionAndNothingToReimburse(t *testing.T) {
	s, ctx, le, pool := setup(t, 22)
	sam, dana := asUser(ctx, "sam"), asUser(ctx, "dana")
	l := line("TRAVEL", "Hotel", 80)
	l.HasReceipt = true
	rejected := submittedReport(t, s, sam, le, l)
	if done, _, err := s.DecideReport(dana, rejected.ID, false, commitDay, store.Command{Key: "reject", Hash: "h"}); err != nil || done.Status != "REJECTED" {
		t.Fatalf("reject = %+v, %v; want REJECTED", done, err)
	}

	cab := line("TRAVEL", "Cab", 12)
	cab.PaidBy = domain.PaidByCompanyCard
	companyPaid := newReport(t, s, sam, le, "company")
	companyPaid, _, err := s.AddLine(sam, companyPaid.ID, "line-c", "hc", cab, commitDay)
	if err != nil {
		t.Fatal(err)
	}
	if companyPaid, _, err = s.SubmitReport(sam, companyPaid.ID, "submit-c", "hsc", 0, commitDay); err != nil {
		t.Fatal(err)
	}
	done, _, err := s.DecideReport(dana, companyPaid.ID, true, commitDay, store.Command{Key: "approve-c", Hash: "hac"})
	if err != nil || done.Status != "PAID" || done.ReimbursedAt == nil {
		t.Fatalf("approve company-paid = %+v, %v; want PAID with nothing sent to AP", done, err)
	}
	if got := approvedEvents(t, ctx, pool); len(got) != 0 {
		t.Fatalf("published %v for a report with nothing to reimburse", got)
	}
}

func TestMarkReimbursed_PaysOnlyWhatTheApprovedReportAsksFor(t *testing.T) {
	s, ctx, le, _ := setup(t, 23)
	sam, dana := asUser(ctx, "sam"), asUser(ctx, "dana")
	l := line("TRAVEL", "Hotel", 80)
	l.HasReceipt = true
	rep := submittedReport(t, s, sam, le, l)
	settled := reimbursement.OpenItemSettled{OpenItemID: ids.New(), LegalEntityID: le, VendorID: rep.WorkerID, SourceInvoiceID: rep.ID,
		Currency: "USD", OpenAmount: "0", Status: reimbursement.StatusSettled, SettledAmount: "80", SettlementID: "run-1"}

	if marked, err := s.MarkReimbursed(ctx, settled); err == nil || marked || !errors.Is(err, store.ErrSettlementMismatch) {
		t.Fatalf("settled before approval = %v, %v; want %v", marked, err, store.ErrSettlementMismatch)
	}
	if _, _, err := s.DecideReport(dana, rep.ID, true, commitDay, store.Command{Key: "approve", Hash: "h"}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*reimbursement.OpenItemSettled){
		"other payee":             func(d *reimbursement.OpenItemSettled) { d.VendorID = ids.New() },
		"overpaid":                func(d *reimbursement.OpenItemSettled) { d.OpenAmount = "-0.01" },
		"more open than approved": func(d *reimbursement.OpenItemSettled) { d.OpenAmount = "80.01" },
		"other money":             func(d *reimbursement.OpenItemSettled) { d.Currency = "EUR" },
	} {
		bad := settled
		mutate(&bad)
		if marked, err := s.MarkReimbursed(ctx, bad); marked || !errors.Is(err, store.ErrSettlementMismatch) {
			t.Fatalf("%s = %v, %v; want %v", name, marked, err, store.ErrSettlementMismatch)
		}
	}
	other := settled
	other.SourceInvoiceID = ids.New()
	if marked, err := s.MarkReimbursed(ctx, other); marked || err != nil {
		t.Fatalf("an open item of another document = %v, %v; want ignored", marked, err)
	}
	if got, _ := s.GetReport(ctx, rep.ID); got.Status != "APPROVED" {
		t.Fatalf("status after refused events = %s; want APPROVED", got.Status)
	}

	partial := settled
	partial.Status, partial.OpenAmount, partial.SettledAmount = "PARTIALLY_SETTLED", "30", "50"
	if marked, err := s.MarkReimbursed(ctx, partial); marked || err != nil {
		t.Fatalf("partial settlement = %v, %v; want no change", marked, err)
	}
	for pass := 1; pass <= 2; pass++ {
		marked, err := s.MarkReimbursed(ctx, settled)
		if err != nil || marked != (pass == 1) {
			t.Fatalf("pass %d: marked=%v err=%v", pass, marked, err)
		}
	}
	paid, _ := s.GetReport(ctx, rep.ID)
	if paid.Status != "PAID" || paid.APOpenItemID != settled.OpenItemID || paid.ReimbursedAt == nil || !paid.Reimbursable.Equal(decimal.NewFromInt(80)) {
		t.Fatalf("report = %+v; want PAID against open item %s", paid, settled.OpenItemID)
	}
}

func TestMarkReimbursed_IsIsolatedByTenant(t *testing.T) {
	s, ctx, le, pool := setup(t, 24)
	l := line("TRAVEL", "Hotel", 80)
	l.HasReceipt = true
	rep := submittedReport(t, s, asUser(ctx, "sam"), le, l)
	if _, _, err := s.DecideReport(asUser(ctx, "dana"), rep.ID, true, commitDay, store.Command{Key: "approve", Hash: "h"}); err != nil {
		t.Fatal(err)
	}
	foreign := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(25), LegalEntityID: le})
	settled := reimbursement.OpenItemSettled{OpenItemID: ids.New(), LegalEntityID: le, VendorID: rep.WorkerID, SourceInvoiceID: rep.ID,
		Currency: "USD", Status: reimbursement.StatusSettled, SettledAmount: "80"}
	if marked, err := store.New(pool).MarkReimbursed(foreign, settled); marked || err != nil {
		t.Fatalf("another tenant's event = %v, %v; want ignored", marked, err)
	}
	if got, _ := s.GetReport(ctx, rep.ID); got.Status != "APPROVED" {
		t.Fatalf("status = %s; want APPROVED", got.Status)
	}
}

func TestDecideReport_RefusesTheCreatorAndAnyReportWithoutARecordedSubmitter(t *testing.T) {
	s, ctx, le, _ := setup(t, 26)
	cora, sam, dana := asUser(ctx, "cora"), asUser(ctx, "sam"), asUser(ctx, "dana")
	l := line("TRAVEL", "Hotel", 80)
	l.HasReceipt = true

	rep := newReport(t, s, cora, le, "by-cora")
	rep, _, err := s.AddLine(cora, rep.ID, "line-1", "h1", l, commitDay)
	if err != nil {
		t.Fatal(err)
	}
	if rep, _, err = s.SubmitReport(sam, rep.ID, "submit-1", "hs", 0, commitDay); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DecideReport(cora, rep.ID, true, commitDay, store.Command{Key: "creator", Hash: "h"}); problemOf(t, err).Code != store.CodeOwnDecision {
		t.Fatalf("creator deciding must be %s, got %v", store.CodeOwnDecision, err)
	}

	anonymous := newReport(t, s, ctx, le, "anonymous")
	if anonymous, _, err = s.AddLine(ctx, anonymous.ID, "line-2", "h2", domain.Line{ID: ids.New(), Category: "TRAVEL", TxnDate: businessDay, Amount: decimal.NewFromInt(80), Merchant: "Hotel", HasReceipt: true}, commitDay); err != nil {
		t.Fatal(err)
	}
	if anonymous, _, err = s.SubmitReport(ctx, anonymous.ID, "submit-2", "hs2", 0, commitDay); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DecideReport(dana, anonymous.ID, true, commitDay, store.Command{Key: "anon", Hash: "h"}); problemOf(t, err).Code != store.CodeOwnDecision {
		t.Fatalf("a report with no recorded creator or submitter must be refused with %s, got %v", store.CodeOwnDecision, err)
	}
	if got, _ := s.GetReport(dana, anonymous.ID); got.Status != "SUBMITTED" {
		t.Fatalf("status = %s; want SUBMITTED", got.Status)
	}
}
