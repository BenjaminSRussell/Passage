package store

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
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

// 30-2: a legal entity's bank account mapping picks the ledger account of the cash: the account the money came to, else the
// entity's default for the currency, else 1000; a mapping is per currency and per legal entity.
func TestBankAccountMapping_PicksTheLedgerAccountOfTheCash(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	pay := func(key, bank, currency string) Payment {
		in := Payment{LegalEntityID: legalEntity, PaymentMode: "WIRE", Source: "MANUAL", Amount: amt("100.00"), UnappliedAmount: amt("100.00"),
			Currency: currency, ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}
		if bank != "" {
			in.BankAccountRef = &bank
		}
		p, err := s.CreatePayment(ctx, in, key)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	debitAccount := func(p Payment) string {
		var accounts []string
		if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			var raw []byte
			if err := tx.QueryRow(ctx, `select lines from gl_journal where source_type = 'payment_receipt' and source_id = $1`, p.ID).Scan(&raw); err != nil {
				return err
			}
			var lines []journalLine
			if err := json.Unmarshal(raw, &lines); err != nil {
				return err
			}
			for _, l := range lines {
				if !l.Credit {
					accounts = append(accounts, l.Account)
				}
			}
			return nil
		}); err != nil || len(accounts) != 1 {
			t.Fatalf("receipt journal debit accounts = %v (%v)", accounts, err)
		}
		return accounts[0]
	}
	if got := debitAccount(pay("bank-1", "", "USD")); got != "1000" {
		t.Errorf("no mapping books %s, want 1000", got)
	}
	for _, b := range []BankAccount{
		{LegalEntityID: legalEntity, Currency: "USD", GLAccount: "1010", Name: "Operating USD"},
		{LegalEntityID: legalEntity, Currency: "USD", AccountRef: "us 0000 1234", GLAccount: "1020", Name: "Lockbox USD"},
		{LegalEntityID: legalEntity, Currency: "EUR", GLAccount: "1030"},
	} {
		if err := s.PutBankAccount(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if got := debitAccount(pay("bank-2", "US00001234", "USD")); got != "1020" {
		t.Errorf("the lockbox account books %s, want 1020", got)
	}
	if got := debitAccount(pay("bank-3", "ELSEWHERE", "USD")); got != "1010" {
		t.Errorf("an unmapped account books %s, want the USD default 1010", got)
	}
	if got := debitAccount(pay("bank-4", "", "EUR")); got != "1030" {
		t.Errorf("the EUR default books %s, want 1030", got)
	}
	if got := debitAccount(pay("bank-5", "", "GBP")); got != "1000" {
		t.Errorf("a currency with no mapping books %s, want 1000", got)
	}
	list, err := s.ListBankAccounts(ctx, legalEntity)
	if err != nil || len(list) != 3 || list[0].Currency != "EUR" || list[2].AccountRef != "US00001234" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := s.PutBankAccount(ctx, BankAccount{LegalEntityID: legalEntity, Currency: "usd", GLAccount: "1010"}); err == nil {
		t.Error("a lower case currency was accepted")
	}
	if err := s.PutBankAccount(ctx, BankAccount{LegalEntityID: legalEntity, Currency: "USD", GLAccount: "10 10"}); err == nil {
		t.Error("an account with a space was accepted")
	}
	if err := s.DeleteBankAccount(ctx, legalEntity, "USD", "US 0000 1234"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBankAccount(ctx, legalEntity, "USD", "US 0000 1234"); err == nil {
		t.Error("deleting twice succeeded")
	}
	if got := debitAccount(pay("bank-6", "US00001234", "USD")); got != "1010" {
		t.Errorf("after the mapping is gone: %s, want the default 1010", got)
	}
}

// 30-2: a camt.053 statement's own account id reaches the payment, so its cash books to the mapped account.
func TestBankAccountMapping_CamtStatementAccountBooksToItsLedgerAccount(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	if err := s.PutBankAccount(ctx, BankAccount{LegalEntityID: legalEntity, Currency: "EUR", AccountRef: "DE89370400440532013000", GLAccount: "1015"}); err != nil {
		t.Fatal(err)
	}
	batch, err := s.ImportBankStatement(ctx, BankStatementImport{LegalEntityID: legalEntity, FileSHA256: strings.Repeat("a", 64), StatementCount: 1,
		Credits: []domain.CAMTCredit{{StatementID: "S1", Reference: "E1", Currency: "EUR", Amount: amt("50.00"), ValueDate: "2027-03-20", BankAccount: "DE89370400440532013000"}}})
	if err != nil || len(batch.Payments) != 1 || batch.Payments[0].BankAccountRef == nil || *batch.Payments[0].BankAccountRef != "DE89370400440532013000" {
		t.Fatalf("import: %+v %v", batch, err)
	}
	var raw []byte
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select lines from gl_journal where source_id = $1`, batch.Payments[0].ID).Scan(&raw)
	}); err != nil || !strings.Contains(string(raw), `"account": "1015"`) || strings.Contains(string(raw), `"account": "1000"`) {
		t.Fatalf("journal lines = %s (%v)", raw, err)
	}
}
