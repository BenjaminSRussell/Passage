package store_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func execTenant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// What "open" takes in and leaves out: only OPEN lines of CONFIRMED orders with quantity left to
// receive (not a fully received line, a cancelled line, a DRAFT or an APPROVED order), one row per
// currency, the open value rounded half to even to the currency's minor units.
func TestInquiries_OpenRulesCurrenciesAndRounding(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(64)
	vendor := ids.New()
	asOf := day(t, "2027-02-01").Time

	// USD: line 1 is 3 EA for 100.00 with 1 received (open 66.666… -> 66.67), line 2 is cancelled,
	// line 3 is received in full.
	usd := lifecyclePO(t, vendor, "3", "2027-01-05")
	usd.Lines[0].GrossAmount, usd.Lines[0].LineAmount = dec("100"), dec("100")
	for _, n := range []int{2, 3} {
		l := lifecyclePO(t, vendor, "1", "2027-01-05").Lines[0]
		l.LineNo = n
		usd.Lines = append(usd.Lines, l)
	}
	usd.Subtotal, usd.Total = dec("120"), dec("120")
	a := confirmedPO(t, ctx, s, usd)
	execTenant(t, ctx, pool, `update purchase_order_line set received_qty = 1 where purchase_order_id = $1 and line_no in (1, 3)`, a.ID)
	execTenant(t, ctx, pool, `update purchase_order_line set status = 'CANCELLED' where purchase_order_id = $1 and line_no = 2`, a.ID)
	// EUR: the same vendor, its own row.
	eur := lifecyclePO(t, vendor, "5", "2027-03-01")
	eur.Currency = "EUR"
	confirmedPO(t, ctx, s, eur)
	// Neither a DRAFT nor an APPROVED (not yet confirmed) order is an open order.
	if _, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, vendor, "7", "2027-01-01"), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	approved, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, vendor, "9", "2027-01-01"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	walk(t, ctx, s, approved, domain.POActionSubmit, domain.POActionApprove)

	rows, err := s.ListOpenOrdersByVendor(ctx, store.InquiryFilter{AsOf: asOf}, "", 0)
	if err != nil || len(rows.Items) != 2 {
		t.Fatalf("by vendor = %+v, %v", rows.Items, err)
	}
	got := map[string]store.VendorOpenOrders{}
	for _, r := range rows.Items {
		got[r.Currency] = r
	}
	if r := got["USD"]; r.LineCount != 1 || r.OrderCount != 1 || r.OpenValue.String() != "66.67" || r.OverdueLines != 1 || r.OverdueValue.String() != "66.67" {
		t.Fatalf("USD row = %+v", r)
	}
	if r := got["EUR"]; r.LineCount != 1 || r.OpenValue.String() != "50" || r.OverdueLines != 0 || r.EarliestDue.String() != "2027-03-01" {
		t.Fatalf("EUR row = %+v", r)
	}
	overdue, err := s.ListOverdueLines(ctx, store.InquiryFilter{AsOf: asOf}, "", 0)
	if err != nil || len(overdue.Items) != 1 || overdue.Items[0].LineNo != 1 || overdue.Items[0].OpenValue.String() != "66.67" {
		t.Fatalf("overdue = %+v, %v", overdue.Items, err)
	}
}

// A line is due on the vendor's confirmed date when there is one, else the requested date; with a
// delivery schedule the received quantity covers the rows in order and the line waits on the
// first row not covered.
func TestInquiries_DueDateFollowsConfirmationAndSchedule(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(66)
	po, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, ids.New(), "100", "2027-01-05"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDeliverySchedule(ctx, po.ID, 1, po.RowVersion, sched(t, "60", "2027-01-05", "40", "2027-02-10")); err != nil {
		t.Fatal(err)
	}
	if po, err = s.GetPurchaseOrder(ctx, po.ID); err != nil {
		t.Fatal(err)
	}
	po = walk(t, ctx, s, po, domain.POActionSubmit, domain.POActionApprove, domain.POActionConfirm)
	overdueOn := func(asOf string) []store.OverdueLine {
		t.Helper()
		page, err := s.ListOverdueLines(ctx, store.InquiryFilter{AsOf: day(t, asOf).Time}, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		return page.Items
	}
	// Unconfirmed: the first row's requested date counts.
	if got := overdueOn("2027-01-15"); len(got) != 1 || got[0].DueDate.String() != "2027-01-05" || got[0].DaysLate != 10 || got[0].ConfirmedDate != nil {
		t.Fatalf("unconfirmed = %+v", got)
	}
	// The vendor confirms the first row for 2027-01-20: not overdue on the 15th, five days late on the 25th.
	if _, err := s.ConfirmDeliveryDate(ctx, po.ID, 1, 1, po.RowVersion, day(t, "2027-01-20")); err != nil {
		t.Fatal(err)
	}
	if got := overdueOn("2027-01-15"); len(got) != 0 {
		t.Fatalf("a line confirmed for later is overdue: %+v", got)
	}
	got := overdueOn("2027-01-25")
	if len(got) != 1 || got[0].DueDate.String() != "2027-01-20" || got[0].DeliveryDate.String() != "2027-01-05" ||
		got[0].ConfirmedDate == nil || got[0].ConfirmedDate.String() != "2027-01-20" || got[0].DaysLate != 5 {
		t.Fatalf("confirmed = %+v", got)
	}
	vendors, err := s.ListOpenOrdersByVendor(ctx, store.InquiryFilter{AsOf: day(t, "2027-01-15").Time}, "", 0)
	if err != nil || len(vendors.Items) != 1 || vendors.Items[0].OverdueLines != 0 || vendors.Items[0].EarliestDue.String() != "2027-01-20" {
		t.Fatalf("vendor row before the confirmed date = %+v, %v", vendors.Items, err)
	}
	backorderedOn := func(asOf string) []store.BackorderedLine {
		t.Helper()
		page, err := s.ListBackorderedLines(ctx, store.InquiryFilter{AsOf: day(t, asOf).Time}, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		return page.Items
	}
	// Overdue with nothing received: not backordered.
	if got := backorderedOn("2027-01-25"); len(got) != 0 {
		t.Fatalf("a line with nothing received is backordered: %+v", got)
	}
	// The first row is received: the line waits on the second row (2027-02-10, unconfirmed).
	setLineQuantities(t, ctx, pool, po.ID, "60", "0")
	if got := backorderedOn("2027-02-15"); len(got) != 1 || got[0].DueDate.String() != "2027-02-10" || got[0].ConfirmedDate != nil ||
		got[0].DaysLate != 5 || !got[0].ReceivedQty.Equal(dec("60")) || !got[0].OpenQty.Equal(dec("40")) {
		t.Fatalf("backordered on the second row = %+v", got)
	}
	if got := overdueOn("2027-02-01"); len(got) != 0 {
		t.Fatalf("received first row still overdue: %+v", got)
	}
	if got := overdueOn("2027-02-15"); len(got) != 1 || got[0].DueDate.String() != "2027-02-10" || got[0].ConfirmedDate != nil || !got[0].OpenQty.Equal(dec("40")) {
		t.Fatalf("second row = %+v", got)
	}
}

// Overdue lines due on the same day page by (due date, PO id, line no) without repeats or gaps,
// and a caller scoped to another legal entity of the tenant sees none of the three inquiries.
func TestInquiries_TiesAndLegalEntityScope(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(67)
	product := ids.New()
	want := map[string]bool{}
	for i := 0; i < 3; i++ {
		in := lifecyclePO(t, ids.New(), "10", "2027-01-05")
		in.Lines[0].ProductID = product
		second := in.Lines[0]
		second.LineNo = 2
		in.Lines = append(in.Lines, second)
		in.Subtotal, in.Total = dec("200"), dec("200")
		po := confirmedPO(t, ctx, s, in)
		want[po.ID+"/1"], want[po.ID+"/2"] = true, true
	}
	asOf := day(t, "2027-02-01").Time
	seen := map[string]bool{}
	after := ""
	for pages := 0; ; pages++ {
		if pages > len(want) {
			t.Fatal("paging does not end")
		}
		page, err := s.ListOverdueLines(ctx, store.InquiryFilter{AsOf: asOf}, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range page.Items {
			key := l.PurchaseOrderID + "/" + strconv.Itoa(l.LineNo)
			if seen[key] || !want[key] {
				t.Fatalf("page %d returned %s again or unexpectedly", pages, key)
			}
			seen[key] = true
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if len(seen) != len(want) {
		t.Fatalf("paged %d lines, want %d", len(seen), len(want))
	}
	history, err := s.ListPriceHistory(ctx, store.InquiryFilter{ProductID: product}, "", 0)
	if err != nil || len(history.Items) != len(want) {
		t.Fatalf("history = %d, %v", len(history.Items), err)
	}

	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(67), LegalEntityID: ids.New(), UserID: ids.New()})
	for _, f := range []store.InquiryFilter{{AsOf: asOf, ProductID: product}, {AsOf: asOf, ProductID: product, LegalEntityID: legalEntity}} {
		v, err1 := s.ListOpenOrdersByVendor(other, f, "", 0)
		o, err2 := s.ListOverdueLines(other, f, "", 0)
		h, err3 := s.ListPriceHistory(other, f, "", 0)
		if err1 != nil || err2 != nil || err3 != nil || len(v.Items)+len(o.Items)+len(h.Items) != 0 {
			t.Fatalf("other legal entity sees %d/%d/%d rows (%v %v %v)", len(v.Items), len(o.Items), len(h.Items), err1, err2, err3)
		}
	}
}

// PRD 3.17.14 against Postgres: open orders by vendor and currency, overdue lines and the order
// price history, over CONFIRMED purchase orders with a partly received line.
func TestInquiries_VendorOverdueAndPriceHistory(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(62)
	vendorA, vendorB := ids.New(), ids.New()

	// Vendor A: 100 EA at 10 due 2027-01-05 (40 received), vendor B: 50 EA due 2027-03-01.
	a := confirmedPO(t, ctx, s, lifecyclePO(t, vendorA, "100", "2027-01-05"))
	b := confirmedPO(t, ctx, s, lifecyclePO(t, vendorB, "50", "2027-03-01"))
	setLineQuantities(t, ctx, pool, a.ID, "40", "0")

	asOf := day(t, "2027-02-01").Time
	byVendor, err := s.ListOpenOrdersByVendor(ctx, store.InquiryFilter{AsOf: asOf}, "", 0)
	if err != nil || len(byVendor.Items) != 2 {
		t.Fatalf("by vendor = %+v, %v", byVendor.Items, err)
	}
	for _, v := range byVendor.Items {
		switch v.VendorID {
		case vendorA:
			if !v.OpenValue.Equal(dec("600")) || v.OverdueLines != 1 || !v.OverdueValue.Equal(dec("600")) || v.OrderCount != 1 || v.Currency != "USD" {
				t.Fatalf("vendor A row = %+v", v)
			}
		case vendorB:
			if !v.OpenValue.Equal(dec("500")) || v.OverdueLines != 0 || !v.OverdueValue.IsZero() || v.EarliestDue.String() != "2027-03-01" {
				t.Fatalf("vendor B row = %+v", v)
			}
		default:
			t.Fatalf("unexpected vendor %s", v.VendorID)
		}
	}
	// Paging: one row per page, the cursor carries on.
	first, err := s.ListOpenOrdersByVendor(ctx, store.InquiryFilter{AsOf: asOf}, "", 1)
	if err != nil || len(first.Items) != 1 || first.Next == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := s.ListOpenOrdersByVendor(ctx, store.InquiryFilter{AsOf: asOf}, first.Next, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].VendorID == first.Items[0].VendorID || second.Next != "" {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	if _, err := s.ListOpenOrdersByVendor(ctx, store.InquiryFilter{AsOf: asOf}, "junk", 1); err == nil {
		t.Fatal("a bad cursor was accepted")
	}

	overdue, err := s.ListOverdueLines(ctx, store.InquiryFilter{AsOf: asOf}, "", 0)
	if err != nil || len(overdue.Items) != 1 || overdue.Items[0].PurchaseOrderID != a.ID || overdue.Items[0].DaysLate != 27 ||
		!overdue.Items[0].OpenQty.Equal(dec("60")) || !overdue.Items[0].OpenValue.Equal(dec("600")) {
		t.Fatalf("overdue = %+v, %v", overdue.Items, err)
	}
	if none, _ := s.ListOverdueLines(ctx, store.InquiryFilter{AsOf: day(t, "2027-01-05").Time}, "", 0); len(none.Items) != 0 {
		t.Fatalf("a line due on asOf is not overdue, got %d", len(none.Items))
	}
	if onlyB, _ := s.ListOverdueLines(ctx, store.InquiryFilter{AsOf: day(t, "2027-04-01").Time, VendorID: vendorB}, "", 0); len(onlyB.Items) != 1 || onlyB.Items[0].PurchaseOrderID != b.ID {
		t.Fatalf("vendor B overdue = %+v", onlyB.Items)
	}

	// A product no order carries has no history.
	if h, err := s.ListPriceHistory(ctx, store.InquiryFilter{ProductID: ids.New()}, "", 0); err != nil || len(h.Items) != 0 {
		t.Fatalf("unknown product history = %+v, %v", h.Items, err)
	}
}

func TestInquiries_PriceHistoryByProduct(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(63)
	vendor, product := ids.New(), ids.New()

	old := lifecyclePO(t, vendor, "10", "2027-01-05")
	old.Lines[0].ProductID = product
	newer := lifecyclePO(t, vendor, "20", "2027-01-06")
	newer.Lines[0].ProductID, newer.Lines[0].UnitPrice = product, dec("12")
	newer.Lines[0].GrossAmount, newer.Lines[0].LineAmount = dec("240"), dec("240")
	newer.Subtotal, newer.Total = dec("240"), dec("240")
	confirmedPO(t, ctx, s, old)
	confirmedPO(t, ctx, s, newer)
	// A draft order is not a price anyone agreed to.
	draft := lifecyclePO(t, vendor, "5", "2027-01-07")
	draft.Lines[0].ProductID = product
	if _, err := s.CreatePurchaseOrder(ctx, draft, store.Idem{}); err != nil {
		t.Fatal(err)
	}

	h, err := s.ListPriceHistory(ctx, store.InquiryFilter{ProductID: product}, "", 0)
	if err != nil || len(h.Items) != 2 {
		t.Fatalf("history = %+v, %v", h.Items, err)
	}
	if !h.Items[0].UnitPrice.Equal(dec("12")) || !h.Items[1].UnitPrice.Equal(dec("10")) || h.Items[0].PriceSource != "MANUAL" {
		t.Fatalf("newest first expected: %+v", h.Items)
	}
	page, err := s.ListPriceHistory(ctx, store.InquiryFilter{ProductID: product}, "", 1)
	if err != nil || len(page.Items) != 1 || page.Next == "" {
		t.Fatalf("page 1 = %+v, %v", page, err)
	}
	rest, err := s.ListPriceHistory(ctx, store.InquiryFilter{ProductID: product}, page.Next, 1)
	if err != nil || len(rest.Items) != 1 || !rest.Items[0].UnitPrice.Equal(dec("10")) {
		t.Fatalf("page 2 = %+v, %v", rest, err)
	}
	from := day(t, "2999-01-01")
	if none, _ := s.ListPriceHistory(ctx, store.InquiryFilter{ProductID: product, From: &from}, "", 0); len(none.Items) != 0 {
		t.Fatalf("future from still lists %d", len(none.Items))
	}
	if other, _ := s.ListPriceHistory(ctx, store.InquiryFilter{ProductID: product, VendorID: ids.New()}, "", 0); len(other.Items) != 0 {
		t.Fatalf("other vendor lists %d", len(other.Items))
	}
}

// Backordered lines are OPEN lines of CONFIRMED orders received in part: a line with nothing
// received, one received in full, a cancelled line and a DRAFT order are left out; the open value
// is decimal (pro rata, half to even); due dates tie and page by (due, PO id, line no) with no
// repeats; a caller of another legal entity sees none.
func TestInquiries_BackorderedLines(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(68)
	vendor, product := ids.New(), ids.New()
	asOf := day(t, "2027-02-01").Time

	// Three orders of one line each: 3 EA with 1 received -> 100.00 * 2/3 open = 66.67.
	want := map[string]bool{}
	for i := 0; i < 3; i++ {
		in := lifecyclePO(t, vendor, "3", "2027-01-05")
		in.Lines[0].ProductID = product
		in.Lines[0].GrossAmount, in.Lines[0].LineAmount = dec("100"), dec("100")
		in.Subtotal, in.Total = dec("100"), dec("100")
		po := confirmedPO(t, ctx, s, in)
		execTenant(t, ctx, pool, `update purchase_order_line set received_qty = 1 where purchase_order_id = $1`, po.ID)
		want[po.ID+"/1"] = true
	}
	// Left out: nothing received, received in full, cancelled, and a DRAFT with a receipt.
	confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "3", "2027-01-05"))
	full := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "3", "2027-01-05"))
	execTenant(t, ctx, pool, `update purchase_order_line set received_qty = qty where purchase_order_id = $1`, full.ID)
	cancelled := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "3", "2027-01-05"))
	execTenant(t, ctx, pool, `update purchase_order_line set received_qty = 1, status = 'CANCELLED' where purchase_order_id = $1`, cancelled.ID)
	draft, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, vendor, "3", "2027-01-05"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	execTenant(t, ctx, pool, `update purchase_order_line set received_qty = 1 where purchase_order_id = $1`, draft.ID)

	seen := map[string]bool{}
	after := ""
	for pages := 0; ; pages++ {
		if pages > len(want) {
			t.Fatal("paging does not end")
		}
		page, err := s.ListBackorderedLines(ctx, store.InquiryFilter{AsOf: asOf}, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range page.Items {
			key := l.PurchaseOrderID + "/" + strconv.Itoa(l.LineNo)
			if seen[key] || !want[key] {
				t.Fatalf("page %d returned %s again or unexpectedly", pages, key)
			}
			seen[key] = true
			if l.OpenValue.String() != "66.67" || !l.OpenQty.Equal(dec("2")) || !l.ReceivedQty.Equal(dec("1")) || l.DaysLate != 27 || l.Currency != "USD" {
				t.Fatalf("row = %+v", l)
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if len(seen) != len(want) {
		t.Fatalf("paged %d lines, want %d", len(seen), len(want))
	}
	// Not yet due: listed, with zero days late.
	early, err := s.ListBackorderedLines(ctx, store.InquiryFilter{AsOf: day(t, "2027-01-01").Time, ProductID: product}, "", 0)
	if err != nil || len(early.Items) != 3 || early.Items[0].DaysLate != 0 {
		t.Fatalf("before the due date = %+v, %v", early.Items, err)
	}
	if other, err := s.ListBackorderedLines(ctx, store.InquiryFilter{AsOf: asOf, VendorID: ids.New()}, "", 0); err != nil || len(other.Items) != 0 {
		t.Fatalf("other vendor = %+v, %v", other.Items, err)
	}
	if _, err := s.ListBackorderedLines(ctx, store.InquiryFilter{AsOf: asOf}, "junk", 1); err == nil {
		t.Fatal("a bad cursor was accepted")
	}
	// asOf changes days late, not the sort key: a cursor taken at one asOf pages on at another.
	first, err := s.ListBackorderedLines(ctx, store.InquiryFilter{AsOf: asOf}, "", 1)
	if err != nil || first.Next == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	rest, err := s.ListBackorderedLines(ctx, store.InquiryFilter{AsOf: day(t, "2027-01-10").Time}, first.Next, 0)
	if err != nil || len(rest.Items) != len(want)-1 || rest.Items[0].DaysLate != 5 || rest.Items[0].PurchaseOrderID == first.Items[0].PurchaseOrderID {
		t.Fatalf("rest at another asOf = %+v, %v", rest.Items, err)
	}
	// A receipt reversed back to zero (a return) takes the line off the list.
	reversed := first.Items[0].PurchaseOrderID
	execTenant(t, ctx, pool, `update purchase_order_line set received_qty = 0 where purchase_order_id = $1`, reversed)
	left, err := s.ListBackorderedLines(ctx, store.InquiryFilter{AsOf: asOf}, "", 0)
	if err != nil || len(left.Items) != len(want)-1 {
		t.Fatalf("after the reversal = %+v, %v", left.Items, err)
	}
	for _, l := range left.Items {
		if l.PurchaseOrderID == reversed {
			t.Fatalf("a line with its receipt reversed is still backordered: %+v", l)
		}
	}
	otherLE := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(68), LegalEntityID: ids.New(), UserID: ids.New()})
	if got, err := s.ListBackorderedLines(otherLE, store.InquiryFilter{AsOf: asOf}, "", 0); err != nil || len(got.Items) != 0 {
		t.Fatalf("other legal entity sees %d rows (%v)", len(got.Items), err)
	}
}
