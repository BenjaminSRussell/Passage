package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

type reportedTax struct {
	Transactions []taxTransaction `json:"transactions"`
}

func storedTaxBody(t *testing.T, admin *pgxpool.Pool, invoiceID string) reportedTax {
	t.Helper()
	var raw []byte
	if err := admin.QueryRow(context.Background(), `select body from tax_transaction_report where invoice_id = $1`, invoiceID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var out reportedTax
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// #29: booking to the GL queues the tax report in the booking transaction: nothing for a posted but unbooked document, one output
// transaction per code at the tax the lines carry, negative on a credit note that links its invoice, and a second booking queues
// no second report.
func TestTaxReport_QueuedOnlyWhenBookedNegativeOnCreditNoteIdempotent(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	count := func() int {
		var n int
		if err := admin.QueryRow(ctx, `select count(*) from tax_transaction_report`).Scan(&n); err != nil {
			t.Helper()
			t.Fatal(err)
		}
		return n
	}

	proposal, err := s.CreateProposal(tctx, newProposal(t, ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostProposal(tctx, proposal.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0)
	if err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Fatal("a posted invoice that is not booked queued a tax report")
	}
	if done, err := s.MarkGLBooked(tctx, posted.ID, ids.New(), nil); err != nil || !done {
		t.Fatalf("MarkGLBooked = %v, %v", done, err)
	}
	if done, err := s.MarkGLBooked(tctx, posted.ID, ids.New(), nil); err != nil || done {
		t.Fatalf("a second MarkGLBooked = %v, %v, want no change", done, err)
	}
	if count() != 1 {
		t.Fatalf("a booked invoice queued %d reports, want 1", count())
	}
	body := storedTaxBody(t, admin, posted.ID)
	if len(body.Transactions) != 1 || body.Transactions[0].Code != "DE19" || body.Transactions[0].Direction != "output" ||
		body.Transactions[0].Amount != "19.00" || body.Transactions[0].TaxDate != "2027-03-12" || !ids.Valid(body.Transactions[0].ID) {
		t.Fatalf("invoice body = %+v", body)
	}

	cn, err := creditNote(t, s, tctx, posted.ID, []domain.CreditQty{{LineNo: 1, Qty: decimal.NewFromInt(1)}})
	if err != nil {
		t.Fatal(err)
	}
	postedCN, err := s.PostCreditNote(tctx, cn.ID, time.Date(2027, 3, 21, 0, 0, 0, 0, time.UTC), 0)
	if err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("a posted credit note that is not booked queued a tax report")
	}
	if done, err := s.MarkGLBooked(tctx, postedCN.ID, ids.New(), nil); err != nil || !done {
		t.Fatalf("MarkGLBooked credit note = %v, %v", done, err)
	}
	cnBody := storedTaxBody(t, admin, postedCN.ID)
	if len(cnBody.Transactions) != 1 || cnBody.Transactions[0].Amount != "-19.00" || cnBody.Transactions[0].Code != "DE19" || cnBody.Transactions[0].Direction != "output" {
		t.Fatalf("credit note body = %+v", cnBody)
	}
	if cnBody.Transactions[0].ID == body.Transactions[0].ID {
		t.Error("the credit note reuses the invoice's transaction id")
	}
	var kind, original string
	if err := admin.QueryRow(ctx, `select kind, original_invoice_id::text from tax_transaction_report where invoice_id = $1`, postedCN.ID).Scan(&kind, &original); err != nil || kind != "CREDIT_NOTE" || original != posted.ID {
		t.Fatalf("credit note report kind %q original %q (%v), want CREDIT_NOTE of %s", kind, original, err, posted.ID)
	}
}

// #29: a document with no tax queues nothing; amounts per code sum to the document's tax (the figure the GL booking credits to the
// tax account), so the tax service's settlement reconciliation ties to the booking.
func TestTaxReport_PerCodeAmountsTieToTheDocumentTax(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	inv := newProposal(t, ids.New())
	second := domain.Line{No: 2, Description: "Reduced", Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(50), RevenueAccount: "4000", TaxCode: "DE7"}
	third := domain.Line{No: 3, Description: "More", Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10), RevenueAccount: "4000", TaxCode: "DE19"}
	inv.Lines = append(inv.Lines, Line{Line: second, Net: decimal.NewFromInt(50), Tax: decimal.RequireFromString("3.50")}, Line{Line: third, Net: decimal.NewFromInt(10), Tax: decimal.RequireFromString("1.90")})
	inv.Totals = domain.Totals{Net: decimal.NewFromInt(160), Charges: decimal.Zero, Tax: decimal.RequireFromString("24.40"), Gross: decimal.RequireFromString("184.40")}
	created, err := s.CreateProposal(tctx, inv)
	if err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostProposal(tctx, created.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkGLBooked(tctx, posted.ID, ids.New(), nil); err != nil {
		t.Fatal(err)
	}
	sum, codes := decimal.Zero, map[string]string{}
	for _, tx := range storedTaxBody(t, admin, posted.ID).Transactions {
		sum = sum.Add(decimal.RequireFromString(tx.Amount))
		codes[tx.Code] = tx.Amount
	}
	if !sum.Equal(posted.Totals.Tax) || codes["DE19"] != "20.90" || codes["DE7"] != "3.50" || len(codes) != 2 {
		t.Fatalf("per-code tax %v sums to %s, want 24.40 (%s)", codes, sum, posted.Totals.Tax)
	}

	untaxed := newProposal(t, ids.New())
	untaxed.Lines[0].Tax = decimal.Zero
	untaxed.Totals = domain.Totals{Net: decimal.NewFromInt(100), Charges: decimal.Zero, Tax: decimal.Zero, Gross: decimal.NewFromInt(100)}
	created, err = s.CreateProposal(tctx, untaxed)
	if err != nil {
		t.Fatal(err)
	}
	postedFree, err := s.PostProposal(tctx, created.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkGLBooked(tctx, postedFree.ID, ids.New(), nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := admin.QueryRow(ctx, `select count(*) from tax_transaction_report where invoice_id = $1`, postedFree.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a document without tax queued %d reports (%v)", n, err)
	}
}

// #29: the reports of one legal entity are invisible to another's scope; an operator lists parked reports and retries one:
// PARKED goes back to PENDING due now with attempts from zero, REPORTED is a 409 and an unknown id a 404.
func TestTaxReport_LegalEntityIsolationListAndRetry(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	le, other := ids.New(), ids.New()
	created, err := s.CreateProposal(tctx, newProposal(t, le))
	if err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostProposal(tctx, created.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkGLBooked(tctx, posted.ID, ids.New(), nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListTaxReports(scopedCtx(other), "", 10); err != nil || len(got) != 0 {
		t.Fatalf("another legal entity lists %+v, %v", got, err)
	}
	if got, err := s.ListTaxReports(scopedCtx(le), "", 10); err != nil || len(got) != 1 || got[0].Kind != "INVOICE" || got[0].InvoiceID != posted.ID {
		t.Fatalf("own legal entity lists %+v, %v", got, err)
	}
	if _, err := s.RetryTaxReport(scopedCtx(other), ids.New()); err == nil {
		t.Fatal("retry of an unknown report succeeded")
	}

	if _, err := admin.Exec(ctx, `update tax_transaction_report set status = 'PARKED', attempts = 20, last_error = 'tax refused the transactions (400)'`); err != nil {
		t.Fatal(err)
	}
	parked, err := s.ListTaxReports(tctx, TaxReportParked, 10)
	if err != nil || len(parked) != 1 || parked[0].LastError == nil {
		t.Fatalf("parked = %+v, %v", parked, err)
	}
	if _, err := s.RetryTaxReport(scopedCtx(other), parked[0].ID); err == nil {
		t.Fatal("another legal entity retried the report")
	}
	retried, err := s.RetryTaxReport(tctx, parked[0].ID)
	if err != nil || retried.Status != TaxReportPending || retried.Attempts != 0 {
		t.Fatalf("retried = %+v, %v", retried, err)
	}
	if due, err := s.TaxReportDueTenants(ctx); err != nil || len(due) != 1 {
		t.Fatalf("due tenants after a retry = %v, %v", due, err)
	}
	if _, err := admin.Exec(ctx, `update tax_transaction_report set status = 'REPORTED', reported_at = now()`); err != nil {
		t.Fatal(err)
	}
	var p *httpx.Problem
	if _, err := s.RetryTaxReport(tctx, parked[0].ID); !errors.As(err, &p) || p.Status != http.StatusConflict || p.Code != CodeTaxReportState {
		t.Errorf("retrying a reported report = %v, want a conflict", err)
	}
}
