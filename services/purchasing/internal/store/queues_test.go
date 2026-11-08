package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// Near-limit queue: lines of EFFECTIVE agreements at or above the threshold of their commitment on
// their own measure (quantity for QUANTITY lines, amount otherwise), most released first, overruns
// flagged, ON_HOLD and DRAFT agreements and lines below the threshold left out, paged by
// (percent, line id).
func TestQueues_NearLimitAgreementLines(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(201)
	vendor, product := ids.New(), ids.New()

	mk := func(v, status string) store.AgreementDetail {
		a := blanket(t, v, product)
		a.Status = status
		got, err := s.CreateAgreement(ctx, a, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// Line 1 is a HARD QUANTITY line of 1,000 EA, line 2 a SOFT TOTAL_VALUE line of 500.00.
	near := mk(vendor, domain.AgreementEffective)
	execTenant(t, ctx, pool, `update purchase_agreement_line set released_qty = 900, released_amount = 520 where agreement_id = $1 and line_no = 1`, near.Agreement.ID)
	execTenant(t, ctx, pool, `update purchase_agreement_line set released_amount = 520, released_qty = 10 where agreement_id = $1 and line_no = 2`, near.Agreement.ID)
	// 899.9 / 1000 is 89.99 %: below the threshold. 449.995 / 500 is 89.999 -> 90.00 %: on it.
	below := mk(vendor, domain.AgreementEffective)
	execTenant(t, ctx, pool, `update purchase_agreement_line set released_qty = 899.9 where agreement_id = $1 and line_no = 1`, below.Agreement.ID)
	execTenant(t, ctx, pool, `update purchase_agreement_line set released_amount = 449.995 where agreement_id = $1 and line_no = 2`, below.Agreement.ID)
	hold := mk(vendor, domain.AgreementOnHold)
	execTenant(t, ctx, pool, `update purchase_agreement_line set released_qty = 1000 where agreement_id = $1 and line_no = 1`, hold.Agreement.ID)
	draft := mk(vendor, domain.AgreementDraft)
	execTenant(t, ctx, pool, `update purchase_agreement_line set released_qty = 1000 where agreement_id = $1 and line_no = 1`, draft.Agreement.ID)
	other := mk(ids.New(), domain.AgreementEffective)
	execTenant(t, ctx, pool, `update purchase_agreement_line set released_qty = 950 where agreement_id = $1 and line_no = 1`, other.Agreement.ID)

	page, err := s.ListNearLimitAgreementLines(ctx, store.InquiryFilter{VendorID: vendor}, "", 0)
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("near limit = %+v, %v", page.Items, err)
	}
	// Two lines sit at 90.00 %; the later line (higher id) comes first.
	over, at, qty := page.Items[0], page.Items[1], page.Items[2]
	if over.AgreementID != near.Agreement.ID || over.LineNo != 2 || over.CommitmentType != domain.CommitTotalValue || over.PctReleased.String() != "104" || !over.Overrun ||
		over.Committed.String() != "500" || over.Released.String() != "520" || over.Remaining.String() != "-20" || over.MaxEnforced != domain.EnforceSoft || over.Unit != "" {
		t.Fatalf("overrun line = %+v", over)
	}
	if qty.AgreementID != near.Agreement.ID || qty.LineNo != 1 || qty.PctReleased.String() != "90" || qty.Overrun || qty.Committed.String() != "1000" || qty.Released.String() != "900" ||
		qty.Remaining.String() != "100" || qty.Unit != "EA" || qty.ProductID != product || qty.ValidTo == nil || qty.ValidTo.String() != "2027-12-31" {
		t.Fatalf("quantity line = %+v (released amount 520 must not count against a quantity line)", qty)
	}
	if at.AgreementID != below.Agreement.ID || at.LineNo != 2 || at.PctReleased.String() != "90" || at.Remaining.String() != "50" {
		t.Fatalf("rounded-onto-the-threshold line = %+v", at)
	}

	// The threshold moves the cut; a high one keeps the overrun only.
	ninetyEight := 98
	if hi, err := s.ListNearLimitAgreementLines(ctx, store.InquiryFilter{VendorID: vendor, ThresholdPct: &ninetyEight}, "", 0); err != nil || len(hi.Items) != 1 || hi.Items[0].LineNo != 2 {
		t.Fatalf("98 %% = %+v, %v", hi.Items, err)
	}
	// Paging by (percent, line id), no row twice or missed.
	one, err := s.ListNearLimitAgreementLines(ctx, store.InquiryFilter{VendorID: vendor}, "", 1)
	if err != nil || len(one.Items) != 1 || one.Next == "" || one.Items[0].LineID != over.LineID {
		t.Fatalf("page 1 = %+v, %v", one, err)
	}
	rest, err := s.ListNearLimitAgreementLines(ctx, store.InquiryFilter{VendorID: vendor}, one.Next, 0)
	if err != nil || len(rest.Items) != 2 || rest.Items[0].LineID != at.LineID || rest.Items[1].LineID != qty.LineID {
		t.Fatalf("rest = %+v, %v", rest.Items, err)
	}
	if all, err := s.ListNearLimitAgreementLines(ctx, store.InquiryFilter{}, "", 0); err != nil || len(all.Items) != 4 {
		t.Fatalf("all vendors = %d, %v", len(all.Items), err)
	}
	if _, err := s.ListNearLimitAgreementLines(ctx, store.InquiryFilter{}, "junk", 1); err == nil {
		t.Fatal("a cursor this list did not return was accepted")
	}
	for _, bad := range []int{49, 101} {
		if _, err := s.ListNearLimitAgreementLines(ctx, store.InquiryFilter{ThresholdPct: &bad}, "", 0); err == nil {
			t.Fatalf("threshold %d was accepted", bad)
		}
	}
	if o, err := s.ListNearLimitAgreementLines(principalContext(202), store.InquiryFilter{}, "", 0); err != nil || len(o.Items) != 0 {
		t.Fatalf("other tenant = %+v, %v", o.Items, err)
	}
	otherLE := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(201), LegalEntityID: ids.New(), UserID: ids.New()})
	if o, err := s.ListNearLimitAgreementLines(otherLE, store.InquiryFilter{}, "", 0); err != nil || len(o.Items) != 0 {
		t.Fatalf("other legal entity = %+v, %v", o.Items, err)
	}

	// Rounding is domain.Pct's, not the database's: 899.85 / 1000 is 89.985 %, a half-way tie that
	// rounds to even (89.98, where half-up would give 89.99), and 899.95 / 1000 is 89.995 %, which
	// rounds to even upwards onto 90.00. A SOFT line 0.004 past its commitment shows 100.00 % and
	// is still an overrun, decided on the stored figures rather than the rounded percentage.
	tieVendor := ids.New()
	tie := mk(tieVendor, domain.AgreementEffective)
	execTenant(t, ctx, pool, `update purchase_agreement_line set released_qty = 899.85 where agreement_id = $1 and line_no = 1`, tie.Agreement.ID)
	execTenant(t, ctx, pool, `update purchase_agreement_line set released_amount = 500.004 where agreement_id = $1 and line_no = 2`, tie.Agreement.ID)
	up := mk(tieVendor, domain.AgreementEffective)
	execTenant(t, ctx, pool, `update purchase_agreement_line set released_qty = 899.95 where agreement_id = $1 and line_no = 1`, up.Agreement.ID)
	eightyNine := 89
	got, err := s.ListNearLimitAgreementLines(ctx, store.InquiryFilter{VendorID: tieVendor, ThresholdPct: &eightyNine}, "", 0)
	if err != nil || len(got.Items) != 3 {
		t.Fatalf("rounding = %+v, %v", got.Items, err)
	}
	edge, even, down := got.Items[0], got.Items[1], got.Items[2]
	if edge.AgreementID != tie.Agreement.ID || edge.LineNo != 2 || edge.PctReleased.StringFixed(2) != "100.00" || !edge.Overrun || edge.Released.String() != "500" {
		t.Fatalf("line just over its commitment = %+v", edge)
	}
	thousand := decimal.NewFromInt(1000)
	if want := domain.Pct(decimal.RequireFromString("899.95"), thousand); even.AgreementID != up.Agreement.ID || even.PctReleased.StringFixed(2) != "90.00" || !even.PctReleased.Equal(want) || even.Overrun {
		t.Fatalf("89.995 %% line = %+v, domain.Pct = %s", even, want)
	}
	if want := domain.Pct(decimal.RequireFromString("899.85"), thousand); down.AgreementID != tie.Agreement.ID || down.LineNo != 1 || down.PctReleased.StringFixed(2) != "89.98" || !down.PctReleased.Equal(want) || down.Overrun {
		t.Fatalf("89.985 %% line = %+v, domain.Pct = %s", down, want)
	}
	if at90, err := s.ListNearLimitAgreementLines(ctx, store.InquiryFilter{VendorID: tieVendor}, "", 0); err != nil || len(at90.Items) != 2 || at90.Items[1].AgreementID != up.Agreement.ID {
		t.Fatalf("90 %% with ties = %+v, %v", at90.Items, err)
	}
}

// Change orders: only a confirmed PO whose next version is stored unconfirmed and IN_REVIEW is
// listed; a first submission, a rejected change (until it is submitted again), a change confirmed
// at once and a PO whose change was approved and confirmed are not. Longest waiting first, paged, with both totals.
func TestQueues_ChangeOrders(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(203)
	vendor := ids.New()

	first := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "100", "2027-01-05"))
	waiting1, err := s.EditPurchaseOrder(ctx, first.ID, first.RowVersion, edited(first, lifecyclePO(t, "", "110", "2027-01-05")))
	if err != nil || waiting1.ApprovalStatus != domain.POStatusInReview {
		t.Fatalf("change 1 = %s, %v", waiting1.ApprovalStatus, err)
	}
	second := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "50", "2027-01-05"))
	if _, err := s.EditPurchaseOrder(ctx, second.ID, second.RowVersion, edited(second, lifecyclePO(t, "", "80", "2027-01-05"))); err != nil {
		t.Fatal(err)
	}
	// Not change orders: a first submission, a rejected change, a date move confirmed at once, an approved and confirmed change.
	draft, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, vendor, "10", "2027-01-05"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	walk(t, ctx, s, draft, domain.POActionSubmit)
	rejected := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "20", "2027-01-05"))
	pending, err := s.EditPurchaseOrder(ctx, rejected.ID, rejected.RowVersion, edited(rejected, lifecyclePO(t, "", "40", "2027-01-05")))
	if err != nil {
		t.Fatal(err)
	}
	rejectedNow := walk(t, ctx, s, pending, domain.POActionReject)
	moved := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "30", "2027-01-05"))
	if got, err := s.EditPurchaseOrder(ctx, moved.ID, moved.RowVersion, edited(moved, lifecyclePO(t, "", "30", "2027-01-06"))); err != nil || got.ApprovalStatus != domain.POStatusConfirmed {
		t.Fatalf("date move = %s, %v", got.ApprovalStatus, err)
	}
	done := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "60", "2027-01-05"))
	changed, err := s.EditPurchaseOrder(ctx, done.ID, done.RowVersion, edited(done, lifecyclePO(t, "", "90", "2027-01-05")))
	if err != nil {
		t.Fatal(err)
	}
	walk(t, ctx, s, changed, domain.POActionApprove, domain.POActionConfirm)
	// A PO of another vendor.
	elsewhere := confirmedPO(t, ctx, s, lifecyclePO(t, ids.New(), "10", "2027-01-05"))
	if _, err := s.EditPurchaseOrder(ctx, elsewhere.ID, elsewhere.RowVersion, edited(elsewhere, lifecyclePO(t, "", "20", "2027-01-05"))); err != nil {
		t.Fatal(err)
	}

	page, err := s.ListChangeOrders(ctx, store.InquiryFilter{VendorID: vendor}, "", 0)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("change orders = %+v, %v", page.Items, err)
	}
	a, b := page.Items[0], page.Items[1]
	if a.PurchaseOrderID != first.ID || a.Number != first.Number || a.ConfirmedVersion != 1 || a.PendingVersion != 2 || a.ChangeCount < 1 || a.Currency != "USD" ||
		a.ConfirmedTotal.String() != "1000" || a.ProposedTotal.String() != "1100" || a.ChangedAt.IsZero() {
		t.Fatalf("first = %+v", a)
	}
	if b.PurchaseOrderID != second.ID || b.ConfirmedTotal.String() != "500" || b.ProposedTotal.String() != "800" || b.ChangedAt.Before(a.ChangedAt) {
		t.Fatalf("second = %+v", b)
	}
	if all, err := s.ListChangeOrders(ctx, store.InquiryFilter{}, "", 0); err != nil || len(all.Items) != 3 {
		t.Fatalf("all vendors = %d, %v", len(all.Items), err)
	}
	one, err := s.ListChangeOrders(ctx, store.InquiryFilter{VendorID: vendor}, "", 1)
	if err != nil || len(one.Items) != 1 || one.Next == "" || one.Items[0].PurchaseOrderID != first.ID {
		t.Fatalf("page 1 = %+v, %v", one, err)
	}
	rest, err := s.ListChangeOrders(ctx, store.InquiryFilter{VendorID: vendor}, one.Next, 0)
	if err != nil || len(rest.Items) != 1 || rest.Items[0].PurchaseOrderID != second.ID {
		t.Fatalf("rest = %+v, %v", rest.Items, err)
	}
	// Editing the change back to the confirmed content drops it from the queue; approving and confirming it also does.
	back, err := s.EditPurchaseOrder(ctx, first.ID, waiting1.RowVersion, edited(first, lifecyclePO(t, "", "100", "2027-01-05")))
	if err != nil || back.ApprovalStatus != domain.POStatusConfirmed {
		t.Fatalf("edit back = %s, %v", back.ApprovalStatus, err)
	}
	if after, err := s.ListChangeOrders(ctx, store.InquiryFilter{VendorID: vendor}, "", 0); err != nil || len(after.Items) != 1 || after.Items[0].PurchaseOrderID != second.ID {
		t.Fatalf("after edit back = %+v, %v", after.Items, err)
	}
	// A rejected change sent for approval again is waiting again, with the same pending version.
	if again := walk(t, ctx, s, rejectedNow, domain.POActionSubmit); again.ApprovalStatus != domain.POStatusInReview {
		t.Fatalf("resubmit = %s", again.ApprovalStatus)
	}
	resubmitted, err := s.ListChangeOrders(ctx, store.InquiryFilter{VendorID: vendor}, "", 0)
	if err != nil || len(resubmitted.Items) != 2 || resubmitted.Items[0].PurchaseOrderID != second.ID {
		t.Fatalf("after resubmit = %+v, %v", resubmitted.Items, err)
	}
	if r := resubmitted.Items[1]; r.PurchaseOrderID != rejected.ID || r.ConfirmedVersion != 1 || r.PendingVersion != 2 || r.ConfirmedTotal.String() != "200" || r.ProposedTotal.String() != "400" {
		t.Fatalf("resubmitted change = %+v", r)
	}
	if _, err := s.ListChangeOrders(ctx, store.InquiryFilter{}, "junk", 1); err == nil {
		t.Fatal("a cursor this list did not return was accepted")
	}
	if o, err := s.ListChangeOrders(principalContext(204), store.InquiryFilter{}, "", 0); err != nil || len(o.Items) != 0 {
		t.Fatalf("other tenant = %+v, %v", o.Items, err)
	}
	otherLE := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(203), LegalEntityID: ids.New(), UserID: ids.New()})
	if o, err := s.ListChangeOrders(otherLE, store.InquiryFilter{}, "", 0); err != nil || len(o.Items) != 0 {
		t.Fatalf("other legal entity = %+v, %v", o.Items, err)
	}
}
