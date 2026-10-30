package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func monthly(t *testing.T, per string) store.InvoicingPlanInput {
	return store.InvoicingPlanInput{Kind: domain.PlanPeriodic, SettleWithoutReceipt: true, Periodic: domain.PeriodicSpec{
		Start: day(t, "2027-01-01"), Frequency: domain.FreqMonthly, Periods: 12, BillingDay: 1, AmountPerPeriod: dec(per)}}
}

func setPlanRowStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, poID string, seq int, status string) {
	t.Helper()
	admin, _ := adminPools.Load(pool)
	_, err := admin.(*pgxpool.Pool).Exec(ctx, `update purchase_invoicing_plan_row set status = $3 where purchase_order_id = $1 and seq = $2`, poID, seq, status)
	if err != nil {
		t.Fatal(err)
	}
}

// AC-17.15 against Postgres: a 12,000.00 line with a monthly plan of 1,000.00
// from 2027-01-01 has 12 rows on the 1st; editing the line to 13,200.00
// before any invoice reproportions all 12 rows to 1,100.00 in the same
// transaction. Invoiced rows keep their amount; a plan with an invoiced row
// cannot be replaced, removed or lose its line.
func TestInvoicingPlan_AC17_15(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(60)
	po, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, ids.New(), "1200", "2027-01-05"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.PutInvoicingPlan(ctx, po.ID, 1, po.RowVersion, monthly(t, "1100")); problemCode(err) != domain.CodeInvoicingPlan {
		t.Fatalf("12 × 1100 on a 12000 line = %v, want %s", err, domain.CodeInvoicingPlan)
	}
	if _, err := s.PutInvoicingPlan(ctx, po.ID, 1, po.RowVersion+1, monthly(t, "1000")); problemCode(err) != store.CodeStaleVersion {
		t.Fatalf("stale If-Match = %v", err)
	}
	if _, err := s.PutInvoicingPlan(ctx, po.ID, 2, po.RowVersion, monthly(t, "1000")); problemCode(err) != store.CodePOLineNotFound {
		t.Fatalf("missing line = %v", err)
	}
	plan, err := s.PutInvoicingPlan(ctx, po.ID, 1, po.RowVersion, monthly(t, "1000"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.PORowVersion != po.RowVersion+1 || len(plan.Rows) != 12 || !plan.LineAmount.Equal(dec("12000")) || plan.Currency != "USD" ||
		plan.Frequency != domain.FreqMonthly || plan.BillingDay != 1 || !plan.SettleWithoutReceipt {
		t.Fatalf("plan = %+v", plan)
	}
	for i, r := range plan.Rows {
		if r.Seq != i+1 || r.DueDate.Day() != 1 || int(r.DueDate.Month()) != i+1 || !r.Amount.Equal(dec("1000")) || r.Status != domain.PlanRowOpen {
			t.Fatalf("row %d = %+v", i+1, r)
		}
	}
	if total := domain.PlanTotal(plan.Rows); !total.Equal(dec("12000")) {
		t.Fatalf("Σ = %s", total)
	}

	cur, err := s.GetPurchaseOrder(ctx, po.ID)
	if err != nil || cur.RowVersion != plan.PORowVersion {
		t.Fatalf("PO row version %d, plan says %d (%v)", cur.RowVersion, plan.PORowVersion, err)
	}
	edited13200, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "1320", "2027-01-05")))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInvoicingPlan(ctx, po.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 12 || !got.LineAmount.Equal(dec("13200")) || got.PORowVersion != edited13200.RowVersion {
		t.Fatalf("after the edit: %d rows, line %s, row version %d (PO %d)", len(got.Rows), got.LineAmount, got.PORowVersion, edited13200.RowVersion)
	}
	for i, r := range got.Rows {
		if !r.Amount.Equal(dec("1100")) {
			t.Fatalf("row %d = %s, want 1100.00", i+1, r.Amount)
		}
	}

	// Row 1 is invoiced (wave 18): it keeps 1,100.00 when the line drops back
	// to 12,000.00, and the other 11 rows share 10,900.00 to the cent.
	setPlanRowStatus(t, ctx, pool, po.ID, 1, domain.PlanRowInvoiced)
	back, err := s.EditPurchaseOrder(ctx, po.ID, edited13200.RowVersion, edited(cur, lifecyclePO(t, "", "1200", "2027-01-05")))
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetInvoicingPlan(ctx, po.ID, 1)
	if !got.Rows[0].Amount.Equal(dec("1100")) || got.Rows[0].Status != domain.PlanRowInvoiced || !domain.PlanTotal(got.Rows).Equal(dec("12000")) {
		t.Fatalf("after the edit back: %+v", got.Rows)
	}
	if _, err := s.EditPurchaseOrder(ctx, po.ID, back.RowVersion, edited(cur, lifecyclePO(t, "", "100", "2027-01-05"))); problemCode(err) != domain.CodeInvoicingPlan {
		t.Fatalf("line below the invoiced 1100 = %v, want %s", err, domain.CodeInvoicingPlan)
	}
	if _, err := s.PutInvoicingPlan(ctx, po.ID, 1, back.RowVersion, monthly(t, "1000")); problemCode(err) != domain.CodePlanInvoiced {
		t.Fatalf("replace an invoiced plan = %v", err)
	}
	if _, err := s.DeleteInvoicingPlan(ctx, po.ID, 1, back.RowVersion); problemCode(err) != domain.CodePlanInvoiced {
		t.Fatalf("delete an invoiced plan = %v", err)
	}
	if _, err := s.ActOnPurchaseOrder(ctx, po.ID, back.RowVersion, domain.POActionCancel, store.Idem{}); problemCode(err) != domain.CodePlanInvoiced {
		t.Fatalf("cancel with an invoiced plan row = %v", err)
	}

	// Without the invoice the order cancels and the plan goes with its line.
	setPlanRowStatus(t, ctx, pool, po.ID, 1, domain.PlanRowOpen)
	cancelled, err := s.ActOnPurchaseOrder(ctx, po.ID, back.RowVersion, domain.POActionCancel, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInvoicingPlan(ctx, po.ID, 1); problemCode(err) != store.CodeInvoicingPlanNotFound {
		t.Fatalf("plan of a cancelled line = %v", err)
	}
	if _, err := s.PutInvoicingPlan(ctx, po.ID, 1, cancelled.RowVersion, monthly(t, "1000")); problemCode(err) != domain.CodePlanTarget {
		t.Fatalf("plan on a cancelled PO = %v", err)
	}
}

// A PARTIAL plan by percent, the per-PO list, delete with If-Match, a line
// removed by an edit taking its plan along, and tenant isolation.
func TestInvoicingPlan_PartialListDeleteAndIsolation(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(61)
	in := lifecyclePO(t, ids.New(), "10", "2027-01-05")
	second := in.Lines[0]
	second.LineNo, second.Description = 2, "nut"
	in.Lines = append(in.Lines, second)
	in.Subtotal, in.Total = dec("200"), dec("200")
	po, err := s.CreatePurchaseOrder(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	pct := func(v string) *decimal.Decimal { x := dec(v); return &x }
	partial := store.InvoicingPlanInput{Kind: domain.PlanPartial, Milestones: []domain.Milestone{
		{Date: day(t, "2027-02-01"), Percent: pct("33.333333")},
		{Date: day(t, "2027-03-01"), Percent: pct("33.333333")},
		{Date: day(t, "2027-04-01"), Percent: pct("33.333334")},
	}}
	plan, err := s.PutInvoicingPlan(ctx, po.ID, 2, po.RowVersion, partial)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SettleWithoutReceipt || len(plan.Rows) != 3 || plan.Rows[2].Percent == nil || !plan.Rows[2].Percent.Equal(dec("33.333334")) ||
		plan.Rows[0].Amount.StringFixed(2) != "33.33" || plan.Rows[2].Amount.StringFixed(2) != "33.34" || plan.Frequency != "" {
		t.Fatalf("partial plan = %+v", plan)
	}
	plan, err = s.PutInvoicingPlan(ctx, po.ID, 1, plan.PORowVersion, monthly(t, "50"))
	if problemCode(err) != domain.CodeInvoicingPlan {
		t.Fatalf("12 × 50 on a 100 line = %v", err)
	}
	all, err := s.ListInvoicingPlans(ctx, po.ID)
	if err != nil || len(all) != 1 || all[0].LineNo != 2 || len(all[0].Rows) != 3 {
		t.Fatalf("list = %+v, %v", all, err)
	}
	for name, other := range map[string]context.Context{"other tenant": principalContext(62)} {
		if _, err := s.GetInvoicingPlan(other, po.ID, 2); problemCode(err) != store.CodePurchaseOrderNotFound {
			t.Fatalf("%s read = %v", name, err)
		}
		if _, err := s.ListInvoicingPlans(other, po.ID); problemCode(err) != store.CodePurchaseOrderNotFound {
			t.Fatalf("%s list = %v", name, err)
		}
		if _, err := s.DeleteInvoicingPlan(other, po.ID, 2, 99); problemCode(err) != store.CodePurchaseOrderNotFound {
			t.Fatalf("%s delete = %v", name, err)
		}
	}

	cur, _ := s.GetPurchaseOrder(ctx, po.ID)
	// The edit keeps line 1 only: line 2 is cancelled and its plan removed.
	only := lifecyclePO(t, "", "10", "2027-01-05")
	if _, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, only)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInvoicingPlan(ctx, po.ID, 2); problemCode(err) != store.CodeInvoicingPlanNotFound {
		t.Fatalf("plan of a removed line = %v", err)
	}

	cur, _ = s.GetPurchaseOrder(ctx, po.ID)
	put, err := s.PutInvoicingPlan(ctx, po.ID, 1, cur.RowVersion, store.InvoicingPlanInput{Kind: domain.PlanPartial,
		Milestones: []domain.Milestone{{Date: day(t, "2027-02-01"), Amount: pct("40")}, {Date: day(t, "2027-03-01"), Amount: pct("60")}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteInvoicingPlan(ctx, po.ID, 1, cur.RowVersion); problemCode(err) != store.CodeStaleVersion {
		t.Fatalf("delete with a stale If-Match = %v", err)
	}
	v, err := s.DeleteInvoicingPlan(ctx, po.ID, 1, put.PORowVersion)
	if err != nil || v != put.PORowVersion+1 {
		t.Fatalf("delete = %d, %v", v, err)
	}
	if _, err := s.DeleteInvoicingPlan(ctx, po.ID, 1, v); problemCode(err) != store.CodeInvoicingPlanNotFound {
		t.Fatalf("second delete = %v", err)
	}
}

// #289: receiving reports a settled row; it turns INVOICED once, a repeat is a
// no-op, a different settlement or amount is refused, and a later line amount
// change leaves the row alone and re-splits only the open ones.
func TestMarkPlanRowInvoiced_FlipsOnceAndRepriceSkipsIt(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(62)
	po, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, ids.New(), "1200", "2027-01-05"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PutInvoicingPlan(ctx, po.ID, 1, po.RowVersion, monthly(t, "1000"))
	if err != nil {
		t.Fatal(err)
	}

	first, err := s.MarkPlanRowInvoiced(ctx, po.ID, 1, 1, "settle-1", dec("1000"))
	if err != nil || first.Replayed || first.Status != domain.PlanRowInvoiced {
		t.Fatalf("first mark = %+v, %v", first, err)
	}
	got, _ := s.GetInvoicingPlan(ctx, po.ID, 1)
	if got.Rows[0].Status != domain.PlanRowInvoiced || got.Rows[1].Status != domain.PlanRowOpen || got.PORowVersion != plan.PORowVersion+1 {
		t.Fatalf("after the mark: %+v at version %d (was %d)", got.Rows[:2], got.PORowVersion, plan.PORowVersion)
	}

	again, err := s.MarkPlanRowInvoiced(ctx, po.ID, 1, 1, "settle-1", dec("1000"))
	if err != nil || !again.Replayed {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
	if after, _ := s.GetInvoicingPlan(ctx, po.ID, 1); after.PORowVersion != got.PORowVersion {
		t.Fatalf("a replay bumped the row version to %d", after.PORowVersion)
	}
	if _, err := s.MarkPlanRowInvoiced(ctx, po.ID, 1, 1, "settle-2", dec("1000")); problemCode(err) != domain.CodePlanRowSettlement {
		t.Fatalf("another reference = %v", err)
	}
	if _, err := s.MarkPlanRowInvoiced(ctx, po.ID, 1, 1, "settle-1", dec("999")); problemCode(err) != domain.CodePlanRowSettlement {
		t.Fatalf("another amount on a settled row = %v", err)
	}
	if _, err := s.MarkPlanRowInvoiced(ctx, po.ID, 1, 2, "settle-3", dec("999")); problemCode(err) != domain.CodePlanRowSettlement {
		t.Fatalf("amount that differs from the open row = %v", err)
	}
	if _, err := s.MarkPlanRowInvoiced(ctx, po.ID, 1, 99, "settle-4", dec("1000")); problemCode(err) != store.CodeInvoicingPlanNotFound {
		t.Fatalf("missing row = %v", err)
	}
	if _, err := s.MarkPlanRowInvoiced(ctx, ids.New(), 1, 1, "settle-5", dec("1000")); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("missing PO = %v", err)
	}
	var settled int
	admin, _ := adminPools.Load(pool)
	if err := admin.(*pgxpool.Pool).QueryRow(ctx, `select count(*) from purchase_invoicing_plan_row_settlement where purchase_order_id = $1`, po.ID).Scan(&settled); err != nil || settled != 1 {
		t.Fatalf("settlement records = %d, %v", settled, err)
	}

	cur, _ := s.GetPurchaseOrder(ctx, po.ID)
	if _, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "1320", "2027-01-05"))); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetInvoicingPlan(ctx, po.ID, 1)
	if !got.Rows[0].Amount.Equal(dec("1000")) || got.Rows[0].Status != domain.PlanRowInvoiced || !domain.PlanTotal(got.Rows).Equal(dec("13200")) {
		t.Fatalf("after the reprice the settled row moved: %+v", got.Rows[0])
	}
	if replay, err := s.MarkPlanRowInvoiced(ctx, po.ID, 1, 1, "settle-1", dec("1000")); err != nil || !replay.Replayed {
		t.Fatalf("a repeat after the reprice = %+v, %v", replay, err)
	}
	cur, _ = s.GetPurchaseOrder(ctx, po.ID)
	if _, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "90", "2027-01-05"))); problemCode(err) != domain.CodeInvoicingPlan {
		t.Fatalf("a reprice below the settled 1000 = %v, want %s", err, domain.CodeInvoicingPlan)
	}
}
