package store

import (
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

func journalsBySource(t *testing.T, s *Postgres, status string) map[string][]GLJournalView {
	t.Helper()
	list, err := s.ListGLJournals(tenantCtx(1), GLJournalFilter{Status: status})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]GLJournalView{}
	for _, j := range list {
		out[j.SourceType] = append(out[j.SourceType], j)
	}
	return out
}

func TestGLJournal_PaymentReceiptAndApplicationPostAndReconcileTo1200(t *testing.T) {
	s, gl, ledgerID := rmaSetup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	// Billing booked the invoice itself: Dr 1200 / Cr 4000.
	if _, err := gl.Post(ctx, ledgerclient.Entry{LegalEntityID: legalEntity, LedgerID: ledgerID, DocumentID: "INV-30001", PostingDate: "2027-03-01",
		Currency: "USD", IdempotencyKey: "billing-invoice-1", SourceType: "billing", Lines: []ledgerclient.Line{
			{Account: "1200", Amount: amt("1000.00")}, {Account: "4000", Credit: true, Amount: amt("1000.00")}}}); err != nil {
		t.Fatal(err)
	}
	cust := item.CustomerID
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "ACH", Source: "MANUAL",
		Amount: amt("980.00"), UnappliedAmount: amt("980.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, "gl-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := journalsBySource(t, s, "PENDING"); len(got[JournalPaymentReceipt]) != 1 || len(got) != 1 {
		t.Fatalf("after receipt: %+v", got)
	}
	rec, err := s.GLReconcile(ctx, legalEntity, "USD", "2027-12-31")
	if err != nil || rec.Reconciled || rec.PendingJournals != 1 || !rec.UnappliedPayments.Equal(amt("980.00")) || !rec.UnappliedDifference.Equal(amt("980.00")) {
		t.Fatalf("reconcile before posting: %+v %v", rec, err)
	}
	plan := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("980.00"), CashDiscount: amt("20.00")}}, Residual: decimal.Zero}
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, plan, ""); err != nil {
		t.Fatal(err)
	}
	res, err := s.PostDueJournals(ctx, time.Now().UTC(), 50)
	if err != nil || res.Posted != 2 || res.Failed != 0 || res.Parked != 0 {
		t.Fatalf("sweep: %+v %v", res, err)
	}
	var apply *ledgerclient.Stored
	for _, e := range gl.Entries() {
		if e.SourceType == "receivables-"+JournalPaymentApply {
			e := e
			apply = &e
		}
	}
	if apply == nil || len(apply.Lines) != 3 || apply.Lines[0].Account != "1205" || apply.Lines[1].Account != "4100" || !apply.Lines[1].Amount.Equal(amt("20.00")) ||
		apply.Lines[2].Account != "1200" || !apply.Lines[2].Credit || !apply.Lines[2].Amount.Equal(amt("1000.00")) {
		t.Fatalf("apply journal: %+v", apply)
	}
	if again, err := s.PostDueJournals(ctx, time.Now().UTC(), 50); err != nil || again.Posted != 0 || len(gl.Entries()) != 3 {
		t.Fatalf("second sweep posted %+v entries %d %v", again, len(gl.Entries()), err)
	}
	rec, err = s.GLReconcile(ctx, legalEntity, "USD", "2027-12-31")
	if err != nil || !rec.Reconciled || !rec.OpenItemsNet.IsZero() || !rec.LedgerReceivable.IsZero() || !rec.LedgerUnapplied.IsZero() {
		t.Fatalf("reconcile after posting: %+v %v", rec, err)
	}
}

func TestGLJournal_ResidualStaysIn1205AndReconciles(t *testing.T) {
	s, gl, ledgerID := rmaSetup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	_, _ = gl.Post(ctx, ledgerclient.Entry{LegalEntityID: legalEntity, LedgerID: ledgerID, DocumentID: "INV-30001", PostingDate: "2027-03-01",
		Currency: "USD", IdempotencyKey: "billing-invoice-1", SourceType: "billing", Lines: []ledgerclient.Line{
			{Account: "1200", Amount: amt("1000.00")}, {Account: "4000", Credit: true, Amount: amt("1000.00")}}})
	cust := item.CustomerID
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "WIRE", Source: "MANUAL",
		Amount: amt("1500.00"), UnappliedAmount: amt("1500.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, "gl-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{
		Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("1000.00")}}, Residual: amt("500.00")}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostDueJournals(ctx, time.Now().UTC(), 50); err != nil {
		t.Fatal(err)
	}
	rec, err := s.GLReconcile(ctx, legalEntity, "USD", "2027-12-31")
	if err != nil || !rec.Reconciled || !rec.UnappliedPayments.Equal(amt("500.00")) || !rec.LedgerUnapplied.Equal(amt("500.00")) {
		t.Fatalf("reconcile: %+v %v", rec, err)
	}
}

func TestGLJournal_FeesInterestDownPaymentsAndDeductionsAreOwed(t *testing.T) {
	s, _, _ := rmaSetup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	dp, err := s.CreateDownPayment(ctx, DownPayment{LegalEntityID: legalEntity, CustomerID: item.CustomerID, Kind: domain.KindDownPayment,
		Currency: "USD", Amount: amt("2000.00")}, "dp-gl")
	if err != nil {
		t.Fatal(err)
	}
	dp, err = s.ReceiveDownPayment(ctx, dp.ID, dp.Version, "2027-03-02")
	if err != nil {
		t.Fatal(err)
	}
	if dp, err = s.ClearDownPayment(ctx, dp.ID, dp.Version, item.ID, nil); err != nil {
		t.Fatal(err)
	}
	got := journalsBySource(t, s, "PENDING")[JournalDownPayment]
	if len(got) != 2 {
		t.Fatalf("down payment journals: %+v", got)
	}
	for _, j := range got {
		if !j.Debit.Equal(amt("1000.00")) && !j.Debit.Equal(amt("2000.00")) {
			t.Fatalf("down payment journal %+v", j)
		}
	}
}

func TestGLJournal_InterestOwesDr1200Cr4310(t *testing.T) {
	s, gl, _ := rmaSetup(t)
	ctx := tenantCtx(1)
	seedItem(t, s, ctx, "INV-I1", custA, "2027-01-31", "10000.00", "USD")
	out, err := s.PostInterest(ctx, InterestRequest{LegalEntityID: legalEntity, CalculationDate: "2027-03-31", AnnualRate: amt("0.12"), Basis: domain.BasisAct365, GraceDays: 5}, "int-gl")
	if err != nil || len(out.Posted) != 1 {
		t.Fatalf("interest: %+v %v", out, err)
	}
	if res, err := s.PostDueJournals(ctx, time.Now().UTC(), 50); err != nil || res.Posted != 1 {
		t.Fatalf("sweep: %+v %v", res, err)
	}
	e := gl.Entries()[0]
	if len(e.Lines) != 2 || e.Lines[0].Account != "1200" || e.Lines[0].Credit || e.Lines[1].Account != "4310" || !e.Lines[1].Credit ||
		!e.Lines[0].Amount.Equal(out.Posted[0].Amount) {
		t.Fatalf("interest journal %+v", e)
	}
}

func TestGLJournal_RefusalParksThenRetryPostsWithNewDate(t *testing.T) {
	s, gl, _ := rmaSetup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	cust := item.CustomerID
	if _, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "ACH", Source: "MANUAL",
		Amount: amt("100.00"), UnappliedAmount: amt("100.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, "gl-3"); err != nil {
		t.Fatal(err)
	}
	gl.err = &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "LED-0004"}
	res, err := s.PostDueJournals(ctx, time.Now().UTC(), 50)
	if err != nil || res.Parked != 1 || res.Posted != 0 {
		t.Fatalf("refused sweep: %+v %v", res, err)
	}
	parked, err := s.ListGLJournals(ctx, GLJournalFilter{ParkedOnly: true})
	if err != nil || len(parked) != 1 || parked[0].Attempts != 1 || parked[0].LastError == nil {
		t.Fatalf("parked: %+v %v", parked, err)
	}
	if again, _ := s.PostDueJournals(ctx, time.Now().UTC().Add(48*time.Hour), 50); again.Posted+again.Failed+again.Parked != 0 {
		t.Fatalf("a parked journal must not be retried by the sweep: %+v", again)
	}
	gl.err = nil
	out, err := s.RetryGLJournal(ctx, parked[0].ID, "2027-04-01")
	if err != nil || out.Status != "POSTED" || out.PostingDate != "2027-04-01" || out.JournalPostingID == nil {
		t.Fatalf("retry: %+v %v", out, err)
	}
	if _, err := s.RetryGLJournal(ctx, parked[0].ID, ""); problemCode(err) != CodeJournalState {
		t.Fatalf("retry of a posted journal: %v", err)
	}
}

func TestGLJournal_OutageBacksOffWithoutParking(t *testing.T) {
	s, gl, _ := rmaSetup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	cust := item.CustomerID
	if _, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "ACH", Source: "MANUAL",
		Amount: amt("100.00"), UnappliedAmount: amt("100.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, "gl-4"); err != nil {
		t.Fatal(err)
	}
	gl.err = &ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable}
	now := time.Now().UTC()
	if res, _ := s.PostDueJournals(ctx, now, 50); res.Failed != 1 {
		t.Fatalf("outage: %+v", res)
	}
	if res, _ := s.PostDueJournals(ctx, now, 50); res.Failed+res.Posted != 0 {
		t.Fatalf("backoff should hold the journal back: %+v", res)
	}
	gl.err = nil
	if res, _ := s.PostDueJournals(ctx, now.Add(2*time.Hour), 50); res.Posted != 1 {
		t.Fatalf("after backoff: %+v", res)
	}
}
