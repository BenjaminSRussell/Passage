package store

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
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

// #553: the list pages newest first with a cursor, so a backlog beyond one page can be reached.
func TestSalesReport_ListPagesWithACursor(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	for range 5 {
		inv, err := s.CreateProposal(tctx, shipmentInvoice(t, ids.New(), ids.New()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PostProposal(tctx, inv.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after, pages := "", 0
	for {
		items, next, err := s.ListSalesReportsPage(tctx, "", 2, after)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if seen[it.ID] {
				t.Fatalf("report %s on two pages", it.ID)
			}
			seen[it.ID] = true
		}
		pages++
		if next == "" {
			break
		}
		if len(items) != 2 {
			t.Fatalf("a page with a next cursor holds %d items", len(items))
		}
		after = next
	}
	if len(seen) != 5 || pages != 3 {
		t.Fatalf("saw %d reports on %d pages, want 5 on 3", len(seen), pages)
	}
	if _, _, err := s.ListSalesReportsPage(tctx, "", 2, "garbage"); !errors.Is(err, ErrBadReportCursor) {
		t.Fatalf("bad cursor: %v", err)
	}
}

// #29: an operator lists parked reports and retries one: PARKED goes back to PENDING due now with attempts from zero, a
// REPORTED report is a 409 and an unknown one a 404; the list filters by status.
func TestSalesReport_ListAndRetryParked(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	inv, err := s.CreateProposal(tctx, shipmentInvoice(t, ids.New(), ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostProposal(tctx, inv.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `update sales_invoice_report set status = 'PARKED', attempts = 20, last_error = 'sales refused the invoice report (422)'`); err != nil {
		t.Fatal(err)
	}
	parked, err := s.ListSalesReports(tctx, SalesReportParked, 10)
	if err != nil || len(parked) != 1 || parked[0].LastError == nil || parked[0].InvoiceID != inv.ID {
		t.Fatalf("parked = %+v, %v", parked, err)
	}
	if pending, err := s.ListSalesReports(tctx, SalesReportPending, 10); err != nil || len(pending) != 0 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	retried, err := s.RetrySalesReport(tctx, parked[0].ID)
	if err != nil || retried.Status != SalesReportPending || retried.Attempts != 0 || retried.NextAttemptAt == nil {
		t.Fatalf("retried = %+v, %v", retried, err)
	}
	due, err := s.SalesReportDueTenants(ctx)
	if err != nil || len(due) != 1 {
		t.Fatalf("due tenants after a retry = %v, %v", due, err)
	}
	if again, err := s.RetrySalesReport(tctx, parked[0].ID); err != nil || again.Status != SalesReportPending {
		t.Fatalf("retrying a pending report = %+v, %v", again, err)
	}
	if _, err := admin.Exec(ctx, `update sales_invoice_report set status = 'REPORTED', reported_at = now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetrySalesReport(tctx, parked[0].ID); !isSalesReportConflict(err) {
		t.Errorf("retrying a reported report = %v, want a conflict", err)
	}
	if _, err := s.RetrySalesReport(tctx, ids.New()); err == nil {
		t.Error("retrying an unknown report succeeded")
	}
}

func isSalesReportConflict(err error) bool {
	var p *httpx.Problem
	return errors.As(err, &p) && p.Status == http.StatusConflict && p.Code == CodeSalesReportState
}
