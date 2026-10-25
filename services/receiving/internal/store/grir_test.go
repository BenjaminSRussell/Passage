package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	otherVendorID = "00000000-0000-0000-0000-0000000000a2"
	otherPOID     = "00000000-0000-0000-0000-0000000000b2"
)

func postedReceipt(t *testing.T, e env, p tenant.Principal, slip, serial string, mutate func(*store.Receipt)) store.Receipt {
	t.Helper()
	in := arrival(p, slip)
	if mutate != nil {
		mutate(&in)
	}
	r, err := e.st.DirectReceipt(ctxOf(p), in, registerWith(serial))
	if err != nil {
		t.Fatal(err)
	}
	if r, err = e.st.Post(ctxOf(p), r.ID, postWith(t, p, "entry-"+slip)); err != nil {
		t.Fatal(err)
	}
	return r
}

func updateGrir(t *testing.T, e env, p tenant.Principal, receiptID, set string) {
	t.Helper()
	err := dbx.WithTenantTx(ctxOf(p), e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `update receiving_grir_open_item set `+set+` where receipt_id = $1`, receiptID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func totalFor(page store.GrirPage, currency string) decimal.Decimal {
	for _, c := range page.Totals.OpenAmount {
		if c.Currency == currency {
			return c.Amount
		}
	}
	return decimal.Zero
}

func TestListGrirFiltersPagesAndTotals(t *testing.T) {
	e := setup(t)
	p := principal(1)
	usd := []store.Receipt{
		postedReceipt(t, e, p, "PS-1", "S1", nil),
		postedReceipt(t, e, p, "PS-2", "S2", nil),
		postedReceipt(t, e, p, "PS-3", "S3", nil),
	}
	eur := func(r *store.Receipt) { r.VendorID, r.PurchaseOrderID, r.Currency = otherVendorID, otherPOID, "EUR" }
	postedReceipt(t, e, p, "PS-4", "S4", eur)
	postedReceipt(t, e, p, "PS-5", "S5", eur)
	updateGrir(t, e, p, usd[0].ID, `created_at = now() - interval '3 days', cleared_amount = 2.5`)
	updateGrir(t, e, p, usd[1].ID, `status = 'CLEARED', cleared_amount = received_amount`)
	ctx := ctxOf(p)

	all, err := e.st.ListGrir(ctx, store.GrirFilter{})
	if err != nil || len(all.Items) != 5 || all.Totals.Count != 5 {
		t.Fatalf("all items=%d totals=%+v err=%v, want 5", len(all.Items), all.Totals, err)
	}
	if got := totalFor(all, "USD"); !got.Equal(decimal.RequireFromString("12.5")) {
		t.Errorf("USD open total=%s, want 12.5 (5.0 + 0 + 7.5)", got)
	}
	if got := totalFor(all, "EUR"); !got.Equal(decimal.RequireFromString("15")) || len(all.Totals.OpenAmount) != 2 {
		t.Errorf("EUR open total=%s currencies=%d, want 15 and two currencies", got, len(all.Totals.OpenAmount))
	}
	sample := all.Items[len(all.Items)-1]
	if sample.ReceiptNumber == "" || sample.LineNo != 1 || !sample.OpenAmount.Equal(sample.ReceivedAmount.Sub(sample.ClearedAmount)) || sample.POLineID != poLineID {
		t.Errorf("row not populated: %+v", sample)
	}

	byVendor, err := e.st.ListGrir(ctx, store.GrirFilter{VendorID: otherVendorID})
	if err != nil || len(byVendor.Items) != 2 || byVendor.Totals.Count != 2 || len(byVendor.Totals.OpenAmount) != 1 {
		t.Errorf("vendor filter items=%d totals=%+v err=%v", len(byVendor.Items), byVendor.Totals, err)
	}
	byPO, err := e.st.ListGrir(ctx, store.GrirFilter{PurchaseOrderID: poID})
	if err != nil || len(byPO.Items) != 3 || byPO.Totals.Count != 3 {
		t.Errorf("PO filter items=%d totals=%+v err=%v", len(byPO.Items), byPO.Totals, err)
	}
	cleared, err := e.st.ListGrir(ctx, store.GrirFilter{Status: "CLEARED"})
	if err != nil || len(cleared.Items) != 1 || !cleared.Items[0].OpenAmount.IsZero() {
		t.Errorf("status filter items=%+v err=%v", cleared.Items, err)
	}
	two := 2
	aged, err := e.st.ListGrir(ctx, store.GrirFilter{AgeGt: &two})
	if err != nil || len(aged.Items) != 1 || aged.Items[0].AgeDays != 3 || aged.Totals.Count != 1 {
		t.Errorf("ageGt filter items=%+v totals=%+v err=%v", aged.Items, aged.Totals, err)
	}
	none, err := e.st.ListGrir(ctx, store.GrirFilter{Status: "WRITTEN_OFF"})
	if err != nil || len(none.Items) != 0 || none.Totals.Count != 0 || len(none.Totals.OpenAmount) != 0 {
		t.Errorf("empty filter items=%d totals=%+v err=%v, want empty", len(none.Items), none.Totals, err)
	}

	page1, err := e.st.ListGrir(ctx, store.GrirFilter{Limit: 2})
	if err != nil || len(page1.Items) != 3 || page1.Totals.Count != 5 || !totalFor(page1, "USD").Equal(totalFor(all, "USD")) {
		t.Fatalf("page1 items=%d totals=%+v err=%v, want limit+1 rows and totals over the full filter", len(page1.Items), page1.Totals, err)
	}
	last := page1.Items[1]
	page2, err := e.st.ListGrir(ctx, store.GrirFilter{Limit: 2, CursorCreatedAt: last.CreatedAt, CursorID: last.ID})
	if err != nil || len(page2.Items) != 3 || page2.Items[0].ID != page1.Items[2].ID {
		t.Fatalf("page2 items=%d err=%v, want three rows starting at %s", len(page2.Items), err, page1.Items[2].ID)
	}
	last = page2.Items[1]
	page3, err := e.st.ListGrir(ctx, store.GrirFilter{Limit: 2, CursorCreatedAt: last.CreatedAt, CursorID: last.ID})
	if err != nil || len(page3.Items) != 1 || page3.Items[0].ID != page2.Items[2].ID {
		t.Fatalf("page3 items=%d err=%v, want the final row", len(page3.Items), err)
	}
}

func TestListGrirTenantIsolationAndPrincipal(t *testing.T) {
	e := setup(t)
	one, two := principal(1), principal(2)
	postedReceipt(t, e, one, "PS-1", "S1", nil)
	page, err := e.st.ListGrir(ctxOf(two), store.GrirFilter{})
	if err != nil || len(page.Items) != 0 || page.Totals.Count != 0 {
		t.Fatalf("other tenant items=%d totals=%+v err=%v, want none", len(page.Items), page.Totals, err)
	}
	if _, err := e.st.ListGrir(context.Background(), store.GrirFilter{}); err == nil {
		t.Fatal("want error without a principal")
	}
}
