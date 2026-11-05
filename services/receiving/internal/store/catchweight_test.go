package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	catchLocation  = "00000000-0000-0000-0000-0000000000f1"
	catchSite      = "00000000-0000-0000-0000-0000000000f2"
	catchWarehouse = "00000000-0000-0000-0000-0000000000f3"
)

func catchArrival(p tenant.Principal, slip string) store.Receipt {
	return store.Receipt{
		LegalEntityID: p.LegalEntityID, VendorID: vendorID, PurchaseOrderID: poID, PackingSlip: slip,
		Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Currency: "USD", ExchangeRate: decimal.RequireFromString("1"),
		Lines: []store.Line{{
			ProductID: productID, OrderedRemaining: decimal.NewFromInt(200), UnitPrice: decimal.RequireFromString("2"),
			Unit: "KG", SiteID: catchSite, WarehouseID: catchWarehouse,
		}},
	}
}

func registerCatch(weight string, cwQty int) store.RegisterFunc {
	return func(r store.Receipt, recorded map[domain.SerialKey]struct{}) ([]store.Line, error) {
		l := r.Lines[0]
		l.Qty = decimal.RequireFromString(weight)
		l.CWQty = cwQty
		l.CWNominal, l.CWMin, l.CWMax = decimal.RequireFromString("10"), decimal.RequireFromString("9.8"), decimal.RequireFromString("11")
		l.LocationID = catchLocation
		dr := domain.Receipt{ID: r.ID, LegalEntityID: r.LegalEntityID, Date: r.Date, State: r.State, Lines: []domain.ReceiptLine{{
			LineNo: l.LineNo, ProductID: l.ProductID, Qty: l.Qty, OrderedRemaining: l.OrderedRemaining,
			OverDeliveryPct: l.OverDeliveryPct, CWQty: l.CWQty,
			CatchWeight: domain.CatchWeight{Nominal: l.CWNominal, Min: l.CWMin, Max: l.CWMax},
		}}}
		if _, err := dr.Register(recorded); err != nil {
			return nil, err
		}
		return []store.Line{l}, nil
	}
}

// TestCatchWeightReceiptPostsWeightAndCases is Wave 24 AC4: 10 cases at 102 kg
// and 2.00/kg persist qty 102 / cw_qty 10 and post GR/IR 204; 95 kg is refused.
func TestCatchWeightReceiptPostsWeightAndCases(t *testing.T) {
	e := setup(t)
	st, p := e.st, principal(1)
	if _, err := st.DirectReceipt(ctxOf(p), catchArrival(p, "PS-95"), registerCatch("95", 10)); err == nil {
		t.Fatal("95 kg for 10 cases (min 9.8) should be refused")
	} else if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeCatchWeight {
		t.Fatalf("95 kg: %v, want RCV-CW-001", err)
	}

	r, err := st.DirectReceipt(ctxOf(p), catchArrival(p, "PS-102"), registerCatch("102", 10))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Lines[0].Qty.Equal(decimal.RequireFromString("102")) || r.Lines[0].CWQty != 10 {
		t.Fatalf("registered line qty=%s cwQty=%d, want 102 and 10", r.Lines[0].Qty, r.Lines[0].CWQty)
	}

	posted, err := st.Post(ctxOf(p), r.ID, func(rec store.Receipt) (store.PostOutcome, error) {
		evt, err := events.New(events.Type("receiving", "receipt", "posted", 1), "/passage/receiving/receiving", rec.ID, p.TenantID,
			map[string]string{"receiptId": rec.ID, "ledgerEntryId": "entry-cw"})
		if err != nil {
			return store.PostOutcome{}, err
		}
		amount := rec.Lines[0].UnitPrice.Mul(rec.Lines[0].Qty)
		return store.PostOutcome{
			PostingDate: rec.Date, LedgerEntryID: "entry-cw", Event: evt,
			Amounts: []store.LineAmount{{LineNo: 1, Amount: amount, AmountAcct: amount}},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !posted.Lines[0].Amount.Decimal.Equal(decimal.RequireFromString("204")) {
		t.Fatalf("posted amount %s, want 204", posted.Lines[0].Amount.Decimal)
	}
	// The PO line's destination is stored with the line and read back (#480).
	if posted.Lines[0].SiteID != catchSite || posted.Lines[0].WarehouseID != catchWarehouse {
		t.Fatalf("destination %q / %q, want %s / %s", posted.Lines[0].SiteID, posted.Lines[0].WarehouseID, catchSite, catchWarehouse)
	}
	grirCases := func(want int) {
		t.Helper()
		page, err := st.ListGrir(ctxOf(p), store.GrirFilter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range page.Items {
			if g.ReceiptID == r.ID && g.CWQty != want {
				t.Fatalf("GR/IR item cwQty %d, want %d", g.CWQty, want)
			}
		}
	}
	grirCases(10)
	var qty, amount decimal.Decimal
	err = dbx.WithTenantTx(ctxOf(p), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select received_qty, received_amount from receiving_grir_open_item where receipt_id = $1`, r.ID).Scan(&qty, &amount)
	})
	if err != nil || !qty.Equal(decimal.RequireFromString("102")) || !amount.Equal(decimal.RequireFromString("204")) {
		t.Fatalf("GR/IR qty=%s amount=%s err=%v, want 102 and 204", qty, amount, err)
	}

	// A short-case correction stores the new count with the weight; a change
	// without a count (CWQty 0) keeps the stored one.
	correct := func(weight string, cases int) store.Receipt {
		t.Helper()
		w := decimal.RequireFromString(weight)
		got, err := st.Correct(ctxOf(p), r.ID, func(store.Receipt) (store.Adjustment, error) {
			return store.Adjustment{Lines: []store.LineChange{{LineNo: 1, Qty: w, CWQty: cases, Amount: w.Mul(decimal.NewFromInt(2)), AmountAcct: w.Mul(decimal.NewFromInt(2))}}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := correct("92", 9); !got.Lines[0].Qty.Equal(decimal.NewFromInt(92)) || got.Lines[0].CWQty != 9 {
		t.Fatalf("short-case correction: qty=%s cwQty=%d, want 92 and 9", got.Lines[0].Qty, got.Lines[0].CWQty)
	}
	grirCases(9)
	if got := correct("93", 0); !got.Lines[0].Qty.Equal(decimal.NewFromInt(93)) || got.Lines[0].CWQty != 9 {
		t.Fatalf("weight-only correction: qty=%s cwQty=%d, want 93 and 9", got.Lines[0].Qty, got.Lines[0].CWQty)
	}
	err = dbx.WithTenantTx(ctxOf(p), e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `update receiving_receipt_line set cw_nominal = null where receipt_id = $1`, r.ID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "receiving_receipt_line_catch_weight_check") {
		t.Fatalf("a catch-weight line without its nominal must fail receiving_receipt_line_catch_weight_check: %v", err)
	}
}
