package store

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

// #29: a credit note of an invoice that was reported to sales queues nothing (sales has no reversal of an invoice), and the
// invoice's report is queued in the same transaction as its post: a refused post leaves none.
func TestSalesReport_OnlyAPostedInvoiceQueuesAndACreditNoteDoesNot(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	le := ids.New()
	count := func() int {
		var n int
		if err := admin.QueryRow(ctx, `select count(*) from sales_invoice_report`).Scan(&n); err != nil {
			t.Helper()
			t.Fatal(err)
		}
		return n
	}

	proposal, err := s.CreateProposal(tctx, shipmentInvoice(t, le, ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Fatal("a proposal queued a report")
	}
	if _, err := s.PostProposal(tctx, proposal.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), proposal.Version+5); err == nil {
		t.Fatal("a stale version posted")
	}
	if count() != 0 {
		t.Fatal("a refused post queued a report")
	}
	posted, err := s.PostProposal(tctx, proposal.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0)
	if err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatalf("a posted invoice queued %d reports, want 1", count())
	}

	cn, err := creditNote(t, s, tctx, posted.ID, []domain.CreditQty{{LineNo: 1, Qty: decimal.NewFromInt(1)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostCreditNote(tctx, cn.ID, time.Date(2027, 3, 21, 0, 0, 0, 0, time.UTC), 0); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatalf("a posted credit note left %d reports, want the invoice's one", count())
	}
}
