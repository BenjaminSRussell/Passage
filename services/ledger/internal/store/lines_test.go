package store

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// postLinesFixture posts three vendor invoices on different dates and
// departments, returning their vouchers in posting order.
func postLinesFixture(t *testing.T, e env) []string {
	t.Helper()
	var vouchers []string
	for _, f := range []struct {
		key, amount, dept string
		day               int
	}{
		{"LQ-1", "100.00", "D100", 1},
		{"LQ-2", "200.00", "D200", 10},
		{"LQ-3", "300.00", "D100", 12},
	} {
		in := vendorInvoice(e.ledger.ID, f.key, f.amount)
		in.PostingDate = time.Date(2026, 3, f.day, 0, 0, 0, 0, time.UTC)
		in.Lines[0].Dimensions = map[string]string{"DEPT": f.dept, "CC": "C1"}
		res, err := e.store.PostEntry(e.ctx, in)
		if err != nil {
			t.Fatalf("post %s: %v", f.key, err)
		}
		vouchers = append(vouchers, res.Entries[0].Voucher)
	}
	return vouchers
}

// TestPostgres_ListLines_KeysetPagingWalksPostingOrder pages two lines at a
// time through six lines and checks every line is returned exactly once, in
// (posting sequence, line number) order, with its entry context.
func TestPostgres_ListLines_KeysetPagingWalksPostingOrder(t *testing.T) {
	e, _ := setup(t)
	vouchers := postLinesFixture(t, e)

	var seen []JournalLine
	var after *LineCursor
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("paging did not terminate")
		}
		page, err := e.store.ListLines(e.ctx, LineFilter{LedgerID: e.ledger.ID, Limit: 2, After: after})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		seen = append(seen, page.Items...)
		if page.Next == nil {
			if pages != 2 {
				t.Fatalf("pages = %d, want 3 pages of 2 lines", pages+1)
			}
			break
		}
		after = page.Next
	}
	if len(seen) != 6 {
		t.Fatalf("lines = %d, want 6", len(seen))
	}
	for i, l := range seen {
		wantVoucher, wantLine := vouchers[i/2], i%2+1
		if l.Voucher != wantVoucher || l.LineNumber != wantLine || l.PostingSequence != int64(i/2+1) {
			t.Fatalf("line %d = %s/%d seq %d, want %s/%d seq %d", i, l.Voucher, l.LineNumber, l.PostingSequence, wantVoucher, wantLine, i/2+1)
		}
	}
	first := seen[0]
	if first.Account != "6010" || first.DebitCredit != "D" || first.Amount != "100.00" || first.AmountAcct != "100.00" || first.Currency != "USD" ||
		first.PostingDate != "2026-03-01" || first.Dimensions["DEPT"] != "D100" || first.TraceID != testTrace ||
		first.SourceSystem != "ap" || first.SourceType != "vendor_invoice" || first.SourceID != "LQ-1" ||
		first.SourceURI != "passage://ap/invoices/LQ-1" || first.NegativePosting {
		t.Fatalf("first line = %+v", first)
	}
	if seen[1].Account != "2100" || seen[1].DebitCredit != "C" || len(seen[1].Dimensions) != 0 {
		t.Fatalf("credit line = %+v", seen[1])
	}
}

func TestPostgres_ListLines_Filters(t *testing.T) {
	e, _ := setup(t)
	vouchers := postLinesFixture(t, e)

	tests := []struct {
		name string
		f    LineFilter
		want []string // voucher/account of each expected line, in order
	}{
		{"account range", LineFilter{AccountFrom: "2000", AccountTo: "2999"},
			[]string{vouchers[0] + "/2100", vouchers[1] + "/2100", vouchers[2] + "/2100"}},
		{"date range is inclusive", LineFilter{DateFrom: "2026-03-10", DateTo: "2026-03-12", AccountFrom: "6010", AccountTo: "6010"},
			[]string{vouchers[1] + "/6010", vouchers[2] + "/6010"}},
		{"one dimension", LineFilter{Dimensions: map[string]string{"DEPT": "D100"}},
			[]string{vouchers[0] + "/6010", vouchers[2] + "/6010"}},
		{"two dimensions must both match", LineFilter{Dimensions: map[string]string{"DEPT": "D200", "CC": "C1"}},
			[]string{vouchers[1] + "/6010"}},
		{"dimension value absent", LineFilter{Dimensions: map[string]string{"DEPT": "NOPE"}}, nil},
		{"an empty value keeps lines without the attribute", LineFilter{Dimensions: map[string]string{"DEPT": ""}},
			[]string{vouchers[0] + "/2100", vouchers[1] + "/2100", vouchers[2] + "/2100"}},
		{"an empty value excludes lines that set it", LineFilter{Dimensions: map[string]string{"DEPT": "D100", "CC": ""}}, nil},
		{"voucher", LineFilter{Voucher: vouchers[1]}, []string{vouchers[1] + "/6010", vouchers[1] + "/2100"}},
		{"source system", LineFilter{SourceSystem: "ar"}, nil},
		{"source type", LineFilter{SourceType: "vendor_invoice", AccountFrom: "6010", AccountTo: "6010"},
			[]string{vouchers[0] + "/6010", vouchers[1] + "/6010", vouchers[2] + "/6010"}},
		{"another source type", LineFilter{SourceType: "year_end_closing"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.f.LedgerID, tt.f.Limit = e.ledger.ID, 100
			page, err := e.store.ListLines(e.ctx, tt.f)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, l := range page.Items {
				got = append(got, l.Voucher+"/"+l.Account)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
			if page.Next != nil {
				t.Fatalf("a short page must not carry a cursor")
			}
		})
	}
}

func TestPostgres_ListLines_ReversalAndIsolation(t *testing.T) {
	e, _ := setup(t)
	posted, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "LQ-R", "50.00"))
	if err != nil {
		t.Fatal(err)
	}
	orig := posted.Entries[0].ID
	rev, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: orig, Key: "LQ-R-REV", Hash: make([]byte, 32), TraceID: testTrace, Today: testToday})
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	page, err := e.store.ListLines(e.ctx, LineFilter{LedgerID: e.ledger.ID, Voucher: rev.Entries[0].Voucher, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ReversalOfEntry != orig || page.Items[0].DebitCredit != "C" || page.Items[0].Account != "6010" {
		t.Fatalf("reversing lines = %+v", page.Items)
	}

	if _, err := e.store.ListLines(principalCtx(testkit.TenantID(2), ""), LineFilter{LedgerID: e.ledger.ID, Limit: 10}); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("another tenant must not read the ledger's lines, got %v", err)
	}
	if _, err := e.store.ListLines(principalCtx(e.tenant, ids.New()), LineFilter{LedgerID: e.ledger.ID, Limit: 10}); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("a caller scoped to another legal entity must not read the ledger's lines, got %v", err)
	}
	if _, err := e.store.ListLines(e.ctx, LineFilter{LedgerID: ids.New(), Limit: 10}); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("unknown ledger: %v", err)
	}
}

func TestPostgres_ListLines_DocumentIDFindsOnlyThatDocumentsEntry(t *testing.T) {
	e, _ := setup(t)
	postLinesFixture(t, e)
	in := vendorInvoice(e.ledger.ID, "LQ-DOC", "50.00")
	res, err := e.store.PostEntry(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	page, err := e.store.ListLines(e.ctx, LineFilter{LedgerID: e.ledger.ID, DocumentID: in.DocumentID, Limit: 10})
	if err != nil || len(page.Items) != 2 || page.Items[0].EntryID != res.Entries[0].ID || page.Items[1].EntryID != res.Entries[0].ID {
		t.Fatalf("by document: %+v, %v", page.Items, err)
	}
	page, err = e.store.ListLines(e.ctx, LineFilter{LedgerID: e.ledger.ID, DocumentID: ids.New(), Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("unknown document: %+v, %v", page.Items, err)
	}
}

// TestPostgres_PlanLineExport_CutsAtTheNthLineUnderTheSameFilterAndScope finds where a capped export
// ends: the n-th matching line when another follows, nil when the result fits, with the ledger's
// dimension attributes and the same tenant and legal-entity refusal as ListLines.
func TestPostgres_PlanLineExport_CutsAtTheNthLineUnderTheSameFilterAndScope(t *testing.T) {
	e, _ := setup(t)
	postLinesFixture(t, e)

	plan, err := e.store.PlanLineExport(e.ctx, LineFilter{LedgerID: e.ledger.ID}, 4)
	if err != nil || plan.Boundary == nil || *plan.Boundary != (LineCursor{PostingSequence: 2, LineNumber: 2}) {
		t.Fatalf("6 lines capped at 4: %+v, %v", plan.Boundary, err)
	}
	if len(plan.DimensionAttributes) != len(e.ledger.DimensionAttributes) {
		t.Fatalf("attributes %v, want the ledger's %v", plan.DimensionAttributes, e.ledger.DimensionAttributes)
	}
	if plan, err := e.store.PlanLineExport(e.ctx, LineFilter{LedgerID: e.ledger.ID}, 6); err != nil || plan.Boundary != nil {
		t.Fatalf("6 lines capped at 6 fit: %+v, %v", plan.Boundary, err)
	}
	// The filter and the cursor apply: two D100 lines, the cut after the first of them.
	f := LineFilter{LedgerID: e.ledger.ID, Dimensions: map[string]string{"DEPT": "D100"}}
	if plan, err := e.store.PlanLineExport(e.ctx, f, 1); err != nil || plan.Boundary == nil || *plan.Boundary != (LineCursor{PostingSequence: 1, LineNumber: 1}) {
		t.Fatalf("filtered: %+v, %v", plan.Boundary, err)
	}
	f.After = &LineCursor{PostingSequence: 1, LineNumber: 1}
	if plan, err := e.store.PlanLineExport(e.ctx, f, 1); err != nil || plan.Boundary != nil {
		t.Fatalf("filtered after the first: %+v, %v", plan.Boundary, err)
	}
	if _, err := e.store.PlanLineExport(principalCtx(testkit.TenantID(2), ""), LineFilter{LedgerID: e.ledger.ID}, 4); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("another tenant: %v", err)
	}
	if _, err := e.store.PlanLineExport(principalCtx(e.tenant, ids.New()), LineFilter{LedgerID: e.ledger.ID}, 4); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("another legal entity: %v", err)
	}
}
