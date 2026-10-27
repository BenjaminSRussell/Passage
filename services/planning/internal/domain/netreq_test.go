package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func rowsOf(rows []NetRow, itemID string) []NetRow {
	var out []NetRow
	for _, r := range rows {
		if r.ItemID == itemID {
			out = append(out, r)
		}
	}
	return out
}

// TestRun_AC2210NetRequirementsOfRM300 plans the BR-22.2 example and reads the
// view of RM-300: on-hand 200, the open PO of 300 in week 2, dependent demand
// 600 (week 2), 600 + 110 (week 3) and 80 (week 4), and the planned purchases
// of 500 due in weeks 2 and 3. The PAB column reads 400, 190, 110 (AC-22.10).
func TestRun_AC2210NetRequirementsOfRM300(t *testing.T) {
	r, err := Run(week(1), br222())
	if err != nil {
		t.Fatal(err)
	}
	rows := rowsOf(r.Net, "RM-300")
	want := []struct {
		day                     int
		gross, sr, pr, pab, net string
	}{
		{week(2), "600", "300", "500", "400", "100"},
		{week(3), "710", "0", "500", "190", "310"},
		{week(4), "80", "0", "0", "110", "0"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v; want %d", rows, len(want))
	}
	for i, w := range want {
		got := rows[i]
		if got.Day != w.day || !got.Gross.Equal(d(w.gross)) || !got.ScheduledReceipts.Equal(d(w.sr)) ||
			!got.PlannedReceipts.Equal(d(w.pr)) || !got.PAB.Equal(d(w.pab)) || !got.Net.Equal(d(w.net)) {
			t.Errorf("row %d = day %d GR %s SR %s PR %s PAB %s net %s; want %+v", i, got.Day, got.Gross, got.ScheduledReceipts,
				got.PlannedReceipts, got.PAB, got.Net, w)
		}
	}
}

// TestRun_NetRequirementsOpenWithSafetyStock: safety stock is a gross
// requirement at today, so FG-100 of BR-22.2 opens at PAB 20 − 10 = 10
// (BR-22.1 step 4, policy today) and ends at zero once PP-1 and PP-2 land.
func TestRun_NetRequirementsOpenWithSafetyStock(t *testing.T) {
	r, err := Run(week(1), br222())
	if err != nil {
		t.Fatal(err)
	}
	rows := rowsOf(r.Net, "FG-100")
	if len(rows) != 3 {
		t.Fatalf("FG-100 rows %+v; want today, week 4 and week 5", rows)
	}
	if rows[0].Day != week(1) || !rows[0].Gross.Equal(d("10")) || !rows[0].PAB.Equal(d("10")) {
		t.Fatalf("opening row %+v; want day 7, GR 10, PAB 10", rows[0])
	}
	if last := rows[2]; last.Day != week(5) || !last.PAB.IsZero() || !last.Net.Equal(d("80")) {
		t.Fatalf("closing row %+v; want week 5, PAB 0, net 80", last)
	}
}

// TestRun_NetRequirementsAreOrderedByItemThenDay keeps the view stable for
// the store and the screen.
func TestRun_NetRequirementsAreOrderedByItemThenDay(t *testing.T) {
	r, err := Run(week(1), br222())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(r.Net); i++ {
		a, b := r.Net[i-1], r.Net[i]
		if a.ItemID > b.ItemID || (a.ItemID == b.ItemID && a.Day >= b.Day) {
			t.Fatalf("row %d (%s day %d) is not after row %d (%s day %d)", i, b.ItemID, b.Day, i-1, a.ItemID, a.Day)
		}
	}
}

// TestNetRows_PastDueCountsAtToday: demand and receipts dated before today
// are netted at today, so the view starts there.
func TestNetRows_PastDueCountsAtToday(t *testing.T) {
	r, err := Run(10, []Item{{
		ID: "A", Coverage: CoverageRequirement, OrderType: OrderPurchase, OnHand: d("5"),
		Demand:   []Requirement{{Ref: "SO-OLD", RefType: RefSales, Date: 3, Qty: d("20"), Priority: PrioritySales}},
		Receipts: []Requirement{{Ref: "PO-OLD", RefType: "purchase-line", Date: 4, Qty: d("8")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rows := rowsOf(r.Net, "A")
	if len(rows) != 1 || rows[0].Day != 10 || !rows[0].PlannedReceipts.Equal(d("7")) || !rows[0].PAB.IsZero() || !rows[0].Net.Equal(d("7")) {
		t.Fatalf("rows %+v; want one row at today with PR 7, PAB 0, net 7", rows)
	}
}

// TestRun_PropertyNetRequirementsReconcile (PRD 4.22.9 reconciliation): for
// every item, PAB(t) = PAB(t−1) + SR + PR − GR row by row, days strictly
// increase, the rows hold every demand, receipt and planned order of the item,
// and the closing PAB is on-hand + Σ supply − Σ demand (invariant of 4.22.4).
func TestRun_PropertyNetRequirementsReconcile(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := genItems(t)
		r, err := Run(0, items)
		if err != nil {
			t.Skip(err)
		}
		for _, it := range items {
			rows := rowsOf(r.Net, it.ID)
			pab := it.OnHand
			gross, sr, pr := decimal.Zero, decimal.Zero, decimal.Zero
			prev := -1
			for _, row := range rows {
				if row.Day <= prev {
					t.Fatalf("%s: day %d not after %d", it.ID, row.Day, prev)
				}
				prev = row.Day
				pab = pab.Add(row.ScheduledReceipts).Add(row.PlannedReceipts).Sub(row.Gross)
				if !pab.Equal(row.PAB) {
					t.Fatalf("%s day %d: PAB %s want %s", it.ID, row.Day, row.PAB, pab)
				}
				if row.Net.IsNegative() {
					t.Fatalf("%s day %d: negative net requirement %s", it.ID, row.Day, row.Net)
				}
				gross, sr, pr = gross.Add(row.Gross), sr.Add(row.ScheduledReceipts), pr.Add(row.PlannedReceipts)
			}
			wantGross := it.SafetyStock
			for _, dm := range it.Demand {
				wantGross = wantGross.Add(dm.Qty)
			}
			// genItems has no BOM, so there is no dependent demand to add.
			wantSR := decimal.Zero
			for _, rc := range it.Receipts {
				wantSR = wantSR.Add(rc.Qty)
			}
			wantPR := decimal.Zero
			for _, o := range r.Orders {
				if o.ItemID == it.ID {
					wantPR = wantPR.Add(o.Qty)
				}
			}
			if !gross.Equal(wantGross) || !sr.Equal(wantSR) || !pr.Equal(wantPR) {
				t.Fatalf("%s totals GR %s/%s SR %s/%s PR %s/%s", it.ID, gross, wantGross, sr, wantSR, pr, wantPR)
			}
			if !pab.Equal(it.OnHand.Add(sr).Add(pr).Sub(gross)) {
				t.Fatalf("%s closing PAB %s does not reconcile", it.ID, pab)
			}
		}
	})
}

// TestRun_PropertyPABNeverNegativeWhenCovered: an item planned by
// requirement, period or min/max coverage without safety stock and without
// negative days is never projected below zero on any day: every shortage is
// met by a planned receipt on or before its day. This ties the view to the
// netting rather than to its own arithmetic.
func TestRun_PropertyPABNeverNegativeWhenCovered(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := genItems(t)
		for i := range items {
			items[i].SafetyStock = decimal.Zero
			items[i].NegativeDays = 0
		}
		r, err := Run(0, items)
		if err != nil {
			t.Skip(err)
		}
		for _, row := range r.Net {
			if row.PAB.IsNegative() {
				t.Fatalf("%s day %d: PAB %s below zero; rows %+v", row.ItemID, row.Day, row.PAB, rowsOf(r.Net, row.ItemID))
			}
		}
	})
}
