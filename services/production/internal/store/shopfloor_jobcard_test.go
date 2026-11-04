package store

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

func sfLaborActual(st *stepper) decimal.Decimal {
	c := st.costs()
	return groupSum(c.Actual, domain.GroupLabor)
}

func ptrDec(s string) *decimal.Decimal {
	v := d(s)
	return &v
}

func shopfloorCard(reg, orderID string, extra ShopfloorJobCard) ShopfloorJobCard {
	in := ShopfloorJobCard{
		RegistrationID:   reg,
		Kind:             "job-stop",
		OrderRef:         orderID,
		OperationRef:     "10",
		Hours:            ptrDec("2.00"),
		CostRate:         ptrDec("26"),
		CostRateCurrency: "USD",
		Actor:            "shopfloor",
	}
	if extra.Kind != "" {
		in.Kind = extra.Kind
	}
	if extra.OrderRef != "" {
		in.OrderRef = extra.OrderRef
	}
	if extra.OperationRef != "" {
		in.OperationRef = extra.OperationRef
	}
	if extra.Hours != nil {
		in.Hours = extra.Hours
	}
	if extra.CostRate != nil {
		in.CostRate = extra.CostRate
	}
	if extra.CostRateCurrency != "" {
		in.CostRateCurrency = extra.CostRateCurrency
	}
	if extra.RegistrationID != "" {
		in.RegistrationID = extra.RegistrationID
	}
	return in
}

// FR-25.4 / BR-25.9: a closed shop-floor job-stop posts a job-card on the production
// order at the worker's cost rate when that rate is set, not the resource labor rate.
func TestPostShopfloorJobCard_WorkerRateBeatsResourceRate(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9401))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-sf-jobcard", ids.New(), 10))
	reg := ids.New()

	j, posted, err := s.PostShopfloorJobCard(ctx, shopfloorCard(reg, st.orderID, ShopfloorJobCard{}))
	if err != nil || !posted || j.Type != domain.JournalJobCard || !j.Posted {
		t.Fatalf("post: posted=%v journal=%+v err=%v", posted, j, err)
	}
	if len(j.Lines) != 1 || !j.Lines[0].Qty.Equal(d("2")) || !j.Lines[0].UnitCost.Equal(d("26")) || !j.Lines[0].Amount.Equal(d("52")) {
		t.Fatalf("hours line: %+v, want 2 h at 26.00 = 52.00 (not 30.00 × 2 = 60.00)", j.Lines)
	}
	if c := st.costs(); !groupSum(c.Actual, domain.GroupLabor).Equal(d("52")) {
		t.Fatalf("actual labor %s, want 52 (worker rate), not 60 (resource rate)", groupSum(c.Actual, domain.GroupLabor))
	}

	again, posted, err := s.PostShopfloorJobCard(ctx, shopfloorCard(reg, st.orderID, ShopfloorJobCard{}))
	if err != nil || posted || again.ID != j.ID {
		t.Fatalf("replay: posted=%v id=%s want %s err=%v", posted, again.ID, j.ID, err)
	}
	if c := st.costs(); !groupSum(c.Actual, domain.GroupLabor).Equal(d("52")) {
		t.Fatalf("labor after replay %s, want 52", groupSum(c.Actual, domain.GroupLabor))
	}
}

func TestPostShopfloorJobCard_ResourceRateWhenWorkerRateUnset(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9402))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-sf-jobcard-res", ids.New(), 10))
	zero := decimal.Zero
	j, posted, err := s.PostShopfloorJobCard(ctx, shopfloorCard(ids.New(), st.orderID, ShopfloorJobCard{CostRate: &zero}))
	if err != nil || !posted {
		t.Fatalf("post: posted=%v err=%v", posted, err)
	}
	if !j.Lines[0].Amount.Equal(d("60")) {
		t.Fatalf("hours amount %s, want 60.00 at the resource rate 30", j.Lines[0].Amount)
	}
}

func TestPostShopfloorJobCard_SkipsNonJobStops(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9403))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-sf-jobcard-skip", ids.New(), 10))
	zero := decimal.Zero
	for name, in := range map[string]ShopfloorJobCard{
		"clock-in":   shopfloorCard(ids.New(), st.orderID, ShopfloorJobCard{Kind: "clock-in"}),
		"job-start":  shopfloorCard(ids.New(), st.orderID, ShopfloorJobCard{Kind: "job-start"}),
		"zero hours": shopfloorCard(ids.New(), st.orderID, ShopfloorJobCard{Hours: &zero}),
		"not a uuid": shopfloorCard(ids.New(), st.orderID, ShopfloorJobCard{OrderRef: "MO-1"}),
		"bad oper":   shopfloorCard(ids.New(), st.orderID, ShopfloorJobCard{OperationRef: "OP-10"}),
	} {
		j, posted, err := s.PostShopfloorJobCard(ctx, in)
		if err != nil || posted || j.ID != "" {
			t.Errorf("%s: posted=%v journal=%+v err=%v", name, posted, j, err)
		}
	}
	if c := st.costs(); !groupSum(c.Actual, domain.GroupLabor).IsZero() {
		t.Fatalf("skipped cards still posted labor %s", groupSum(c.Actual, domain.GroupLabor))
	}
}

func TestPostShopfloorJobCard_ForeignCurrencyWorkerRateFallsBackToResourceRate(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9404))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-sf-jobcard-eur", ids.New(), 10))
	j, posted, err := s.PostShopfloorJobCard(ctx, shopfloorCard(ids.New(), st.orderID, ShopfloorJobCard{CostRateCurrency: "EUR"}))
	if err != nil || !posted {
		t.Fatalf("post: posted=%v err=%v", posted, err)
	}
	if !j.Lines[0].UnitCost.Equal(d("30")) || !j.Lines[0].Amount.Equal(d("60")) {
		t.Fatalf("hours line %+v, want the resource rate 30 (EUR 26 must not post as USD 26)", j.Lines[0])
	}
}

func TestPostShopfloorJobCard_UnknownOperationIsPermanentRefusal(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9405))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-sf-jobcard-op", ids.New(), 10))
	_, posted, err := s.PostShopfloorJobCard(ctx, shopfloorCard(ids.New(), st.orderID, ShopfloorJobCard{OperationRef: "99"}))
	if posted || !errors.Is(err, ErrJobCardRefused) {
		t.Fatalf("posted=%v err=%v, want ErrJobCardRefused", posted, err)
	}
	if !sfLaborActual(st).IsZero() {
		t.Fatal("unknown operation posted labor")
	}
}

func TestPostShopfloorJobCard_UnknownOrderIsPermanentRefusal(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9406))
	_, posted, err := s.PostShopfloorJobCard(ctx, shopfloorCard(ids.New(), ids.New(), ShopfloorJobCard{}))
	if posted || !errors.Is(err, ErrJobCardRefused) {
		t.Fatalf("posted=%v err=%v, want ErrJobCardRefused", posted, err)
	}
}

func TestPostShopfloorJobCard_PostFailureAfterCreateKeepsWorkerRate(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9408))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-sf-jobcard-crash", ids.New(), 10))
	reg := ids.New()
	jobs, err := s.ListJobs(ctx, JobFilter{OrderID: st.orderID, JobType: domain.JobProcess})
	if err != nil || len(jobs.Items) == 0 {
		t.Fatalf("jobs: %v %v", jobs, err)
	}
	key := shopfloorJobCardKey + reg
	rate := d("26")
	draft, _, err := s.CreateJournal(ctx, NewJournal{OrderID: st.orderID, Type: domain.JournalJobCard, Proposal: domain.ProposeNone, AutoStart: true,
		Hours: []HoursInput{{JobID: jobs.Items[0].ID, Hours: d("2.00"), LaborRate: &rate}}, Actor: "shopfloor", IdempotencyKey: key, RequestHash: key})
	if err != nil || draft.Posted {
		t.Fatalf("draft: %+v err=%v", draft, err)
	}

	manual, _, err := s.PostJournal(ctx, PostRequest{JournalID: draft.ID, AutoStart: true, Actor: "planner", IdempotencyKey: "manual-post-1"})
	if err != nil || !manual.Lines[0].Amount.Equal(d("52")) {
		t.Fatalf("manual post of the draft: %+v err=%v, want 52 at the stored worker rate", manual.Lines, err)
	}
	again, posted, err := s.PostShopfloorJobCard(ctx, shopfloorCard(reg, st.orderID, ShopfloorJobCard{}))
	if err != nil || posted || again.ID != draft.ID {
		t.Fatalf("replay after manual post: posted=%v id=%s want %s err=%v", posted, again.ID, draft.ID, err)
	}
	if got := sfLaborActual(st); !got.Equal(d("52")) {
		t.Fatalf("labor %s, want 52 booked once", got)
	}
}
