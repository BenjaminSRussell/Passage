package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func quotaIn(le, product string, customer *string, start, end string, qty int64) NewAllocationQuota {
	key := ids.New()
	return NewAllocationQuota{LegalEntityID: le, ProductID: product, CustomerID: customer, PeriodStart: start, PeriodEnd: end,
		Quantity: d(qty), IdempotencyKey: key, RequestHash: key, Actor: "demand-planner"}
}

func orderOf(le, customer, product, shipDay string, qty int64) NewOrder {
	in := oneLineOrder(le, customer, product, time.Date(2027, 2, 20, 0, 0, 0, 0, time.UTC))
	in.Lines[0].Quantity, in.Lines[0].LineNet, in.Lines[0].RequestedShipDate = d(qty), d(qty*10), shipDay
	in.RequestedShipDate = shipDay
	return in
}

func confirmed(t *testing.T, s *Postgres, ctx context.Context, in NewOrder) Order {
	t.Helper()
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", time.Now())
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !res.Confirmed {
		t.Fatalf("not confirmed: %+v", res.Evaluation)
	}
	return res.Order
}

// TestPostgres_AllocationQuotaLifecycle covers FR-27.16 and BR-27.15: quota is
// consumed at confirmation in the requested period, an exhausted period moves
// the line to the next one with SLS-0013, cancellation returns quota to the
// period it came from, a change below the consumed quantity is 409, and a
// line with no later period left is refused.
func TestPostgres_AllocationQuotaLifecycle(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer, product := ids.New(), ids.New(), ids.New()
	march, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-03-01", "2027-03-31", 100))
	if err != nil {
		t.Fatal(err)
	}
	april, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-04-01", "2027-04-30", 50))
	if err != nil {
		t.Fatal(err)
	}
	if got := outboxTypes(t, pool, march.ID); len(got) != 1 || got[0] != salesevents.AllocationQuotaSet {
		t.Fatalf("quota created event: %v", got)
	}

	// Idempotency and overlap.
	in := quotaIn(le, product, nil, "2027-05-01", "2027-05-31", 10)
	first, created, err := s.CreateAllocationQuota(ctx, in)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	replay, created, err := s.CreateAllocationQuota(ctx, in)
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("replay must return the stored quota: %v %v %+v", created, err, replay)
	}
	diff := in
	diff.RequestHash = "other"
	if _, _, err := s.CreateAllocationQuota(ctx, diff); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
	if _, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-03-20", "2027-04-10", 5)); problemCode(t, err) != CodeQuotaOverlap {
		t.Fatalf("overlapping period: %v", err)
	}
	other := ids.New()
	if _, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, &other, "2027-03-20", "2027-04-10", 5)); err != nil {
		t.Fatalf("another customer scope may overlap: %v", err)
	}

	// 60 consumed from March; the line ships on its requested day.
	first60 := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-10", 60))
	if first60.Lines[0].ConfirmedShipDate == nil || *first60.Lines[0].ConfirmedShipDate != "2027-03-10" || len(first60.Warnings) != 0 {
		t.Fatalf("within quota: %+v warnings %+v", first60.Lines[0], first60.Warnings)
	}
	q, err := s.GetAllocationQuota(ctx, march.ID)
	if err != nil || !q.ConsumedQty.Equal(d(60)) || !q.RemainingQty.Equal(d(40)) || len(q.Consumptions) != 1 || q.Consumptions[0].OrderID != first60.ID {
		t.Fatalf("march after 60: %v %+v", err, q)
	}

	// 50 more do not fit March (40 left): confirmed in April with SLS-0013.
	spill := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-25", 50))
	if got := *spill.Lines[0].ConfirmedShipDate; got != "2027-04-01" {
		t.Fatalf("spilled line confirmed %s, want 2027-04-01", got)
	}
	if len(spill.Warnings) != 1 || spill.Warnings[0].Code != CodeAllocationExhausted || spill.Warnings[0].LineNo != 1 {
		t.Fatalf("warnings %+v", spill.Warnings)
	}
	var data salesevents.AllocationData
	if err := json.Unmarshal(outboxData(t, pool, april.ID, salesevents.AllocationConsumed), &data); err != nil {
		t.Fatal(err)
	}
	if data.QuotaID != april.ID || data.OrderID != spill.ID || data.Source != "ALLOCATION" || data.ConfirmedShipDate != "2027-04-01" || data.Remaining != "0" {
		t.Fatalf("allocation.consumed %+v", data)
	}
	var aggregate string
	if err := pool.QueryRow(ctx, `select aggregate_type from outbox where aggregate_id = $1 and type = $2`, april.ID, salesevents.AllocationConsumed).Scan(&aggregate); err != nil || aggregate != "allocation" {
		t.Fatalf("allocation events route to passage.sales.allocation: %q %v", aggregate, err)
	}
	types := outboxTypes(t, pool, spill.ID)
	hasConfirmed := false
	for _, ty := range types {
		hasConfirmed = hasConfirmed || ty == salesevents.OrderConfirmed
	}
	if !hasConfirmed {
		t.Fatalf("order.confirmed missing: %v", types)
	}

	// 10 more cannot be served by March (40 left is fine) -> uses March.
	small := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-12", 40))
	if *small.Lines[0].ConfirmedShipDate != "2027-03-12" {
		t.Fatalf("40 fit March exactly: %+v", small.Lines[0])
	}
	// Everything is used now except May 10: a 20 request in March has nowhere to go.
	o, _, err := s.CreateOrder(ctx, orderOf(le, customer, product, "2027-03-30", 20))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", time.Now()); problemCode(t, err) != CodeAllocationExhausted {
		t.Fatalf("exhausted everywhere: %v", err)
	}
	if again, _ := s.GetOrder(ctx, o.ID); again.Status == domain.OrderConfirmed {
		t.Fatalf("a refused confirm must leave the order unconfirmed: %s", again.Status)
	}
	q, _ = s.GetAllocationQuota(ctx, march.ID)
	if !q.ConsumedQty.Equal(d(100)) {
		t.Fatalf("refused confirm consumed nothing: %s", q.ConsumedQty)
	}

	// Cancelling returns the quota to the period it came from.
	if _, err := s.CancelOrder(ctx, spill.ID, spill.Version, "customer withdrew", "sales-rep", time.Now()); err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetAllocationQuota(ctx, april.ID)
	if !a.ConsumedQty.Equal(d(0)) || a.Consumptions[0].ReturnedAt == nil {
		t.Fatalf("april after cancel: %+v", a)
	}
	m, _ := s.GetAllocationQuota(ctx, march.ID)
	if !m.ConsumedQty.Equal(d(100)) {
		t.Fatalf("march untouched by the april return: %s", m.ConsumedQty)
	}
	if err := json.Unmarshal(outboxData(t, pool, april.ID, salesevents.AllocationReturned), &data); err != nil || data.Quantity != "50" || data.Consumed != "0" {
		t.Fatalf("allocation.returned %v %+v", err, data)
	}
	// And the order that was refused can now confirm in April.
	res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", time.Now())
	if err != nil || *res.Order.Lines[0].ConfirmedShipDate != "2027-04-01" {
		t.Fatalf("after the return: %v %+v", err, res.Order.Lines)
	}

	// Quota change: below consumed is 409, at consumed is allowed, stale ETag is refused.
	m, _ = s.GetAllocationQuota(ctx, march.ID)
	if _, err := s.ChangeAllocationQuota(ctx, m.ID, m.Version, d(99), "demand-planner", time.Now()); problemCode(t, err) != CodeQuotaBelowConsumed {
		t.Fatalf("below consumed: %v", err)
	}
	if _, err := s.ChangeAllocationQuota(ctx, m.ID, m.Version-1, d(120), "demand-planner", time.Now()); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	raised, err := s.ChangeAllocationQuota(ctx, m.ID, m.Version, d(120), "demand-planner", time.Now())
	if err != nil || !raised.RemainingQty.Equal(d(20)) {
		t.Fatalf("raise: %v %+v", err, raised)
	}
	var moved salesevents.AllocationQuotaData
	if err := json.Unmarshal(outboxData(t, pool, m.ID, salesevents.AllocationQuotaMoved), &moved); err != nil || moved.PreviousQty != "100" || moved.QuotaQty != "120" {
		t.Fatalf("allocationquota.changed %v %+v", err, moved)
	}

	// Inquiry filters.
	list, err := s.ListAllocationQuotas(ctx, AllocationFilter{LegalEntityID: le, ProductID: product, OnDate: "2027-04-05", Limit: 50})
	if err != nil || len(list) != 2 {
		t.Fatalf("onDate 04-05 covers April (all customers) and the other customer's tail: %v %+v", err, list)
	}
	if none, err := s.ListAllocationQuotas(ctx, AllocationFilter{LegalEntityID: ids.New(), Limit: 50}); err != nil || len(none) != 0 {
		t.Fatalf("other legal entity: %v %+v", err, none)
	}
	if _, err := s.GetAllocationQuota(ctx, ids.New()); problemCode(t, err) != CodeQuotaNotFound {
		t.Fatalf("unknown quota: %v", err)
	}
}

// TestPostgres_AllocationCustomerQuotaAndChange checks that a customer's own
// quota replaces the all-customer quota, that a product without a quota for the
// requested date is unconstrained, and that changing a confirmed line's
// quantity gives back and draws again.
func TestPostgres_AllocationCustomerQuotaAndChange(t *testing.T) {
	s, _, ctx := migrated(t)
	le, vip, other, product := ids.New(), ids.New(), ids.New(), ids.New()
	all, _, _ := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-03-01", "2027-03-31", 10))
	own, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, &vip, "2027-03-01", "2027-03-31", 500))
	if err != nil {
		t.Fatal(err)
	}
	vipOrder := confirmed(t, s, ctx, orderOf(le, vip, product, "2027-03-05", 300))
	if vipOrder.Lines[0].ConfirmedShipDate == nil || *vipOrder.Lines[0].ConfirmedShipDate != "2027-03-05" {
		t.Fatalf("vip line: %+v", vipOrder.Lines[0])
	}
	if q, _ := s.GetAllocationQuota(ctx, own.ID); !q.ConsumedQty.Equal(d(300)) {
		t.Fatalf("own quota consumed %s", q.ConsumedQty)
	}
	if q, _ := s.GetAllocationQuota(ctx, all.ID); !q.ConsumedQty.IsZero() {
		t.Fatalf("all-customer quota must be untouched: %s", q.ConsumedQty)
	}
	// Another customer is held to the 10 of the all-customer quota.
	o, _, _ := s.CreateOrder(ctx, orderOf(le, other, product, "2027-03-05", 11))
	if _, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", time.Now()); problemCode(t, err) != CodeAllocationExhausted {
		t.Fatalf("other customer over 10: %v", err)
	}
	// A date no quota covers is unconstrained.
	free := confirmed(t, s, ctx, orderOf(le, other, product, "2027-06-01", 9999))
	if *free.Lines[0].ConfirmedShipDate != "2027-06-01" {
		t.Fatalf("unconstrained: %+v", free.Lines[0])
	}

	// Changing the confirmed line from 300 to 450 redraws; 600 is refused and keeps the 300.
	qty450, qty600 := decimal.NewFromInt(450), decimal.NewFromInt(600)
	changed, err := s.ChangeOrder(ctx, vipOrder.ID, vipOrder.Version, ChangeRequest{Reason: "more", Lines: []LineChange{{LineNo: 1, Quantity: &qty450}}}, "sales-rep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if q, _ := s.GetAllocationQuota(ctx, own.ID); !q.ConsumedQty.Equal(d(450)) {
		t.Fatalf("after change to 450: %s", q.ConsumedQty)
	}
	if _, err := s.ChangeOrder(ctx, vipOrder.ID, changed.Version, ChangeRequest{Reason: "too many", Lines: []LineChange{{LineNo: 1, Quantity: &qty600}}}, "sales-rep", time.Now()); problemCode(t, err) != CodeAllocationExhausted {
		t.Fatalf("600 exceeds 500 with no later period: %v", err)
	}
	if q, _ := s.GetAllocationQuota(ctx, own.ID); !q.ConsumedQty.Equal(d(450)) {
		t.Fatalf("a refused change keeps the draw: %s", q.ConsumedQty)
	}
	if _, err := s.CancelOrder(ctx, vipOrder.ID, changed.Version, "gone", "sales-rep", time.Now()); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.GetAllocationQuota(ctx, own.ID); !q.ConsumedQty.IsZero() {
		t.Fatalf("cancel returns the redrawn quantity: %s", q.ConsumedQty)
	}
}

// TestPostgres_AllocationMovedLineChange checks the version diff and the
// confirmed date of a line allocation moved to a later period: a change that
// still does not fit the requested period records the moved date, and one
// that now fits brings the line back to its requested date.
func TestPostgres_AllocationMovedLineChange(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer, product := ids.New(), ids.New(), ids.New()
	if _, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-03-01", "2027-03-31", 10)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-04-01", "2027-04-30", 100)); err != nil {
		t.Fatal(err)
	}
	o := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-10", 20))
	if got := *o.Lines[0].ConfirmedShipDate; got != "2027-04-01" {
		t.Fatalf("20 over a March quota of 10 moves to April: %s", got)
	}

	qty30, qty5 := decimal.NewFromInt(30), decimal.NewFromInt(5)
	still, err := s.ChangeOrder(ctx, o.ID, o.Version, ChangeRequest{Reason: "more", Lines: []LineChange{{LineNo: 1, Quantity: &qty30}}}, "sales-rep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := *still.Lines[0].ConfirmedShipDate; got != "2027-04-01" || len(still.Warnings) != 1 || still.Warnings[0].Code != CodeAllocationExhausted {
		t.Fatalf("30 still moves to April: %s warnings %+v", got, still.Warnings)
	}
	back, err := s.ChangeOrder(ctx, o.ID, still.Version, ChangeRequest{Reason: "fewer", Lines: []LineChange{{LineNo: 1, Quantity: &qty5}}}, "sales-rep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := *back.Lines[0].ConfirmedShipDate; got != "2027-03-10" || len(back.Warnings) != 0 {
		t.Fatalf("5 fits March again: %s warnings %+v", got, back.Warnings)
	}
	versions, err := s.ListOrderVersions(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	byVersion := map[int][]string{}
	for _, v := range versions {
		for _, c := range v.Diff {
			if c.Field == "confirmedShipDate" {
				byVersion[v.Version] = append(byVersion[v.Version], c.Old+">"+c.New)
			}
		}
	}
	if got := byVersion[still.Version]; len(got) != 0 {
		t.Fatalf("the date did not change when 30 moved to April again; diff %v", got)
	}
	if got, want := byVersion[back.Version], []string{"2027-04-01>2027-03-10"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("diff of the change back to March %v, want %v", got, want)
	}
}

// TestPostgres_AllocationCreditRelease checks that releasing a CREDIT hold
// confirms the order and draws quota, and that a release whose confirmation
// allocation cannot cover still releases the hold, leaves the order
// unconfirmed with an SLS-0013 warning and consumes nothing.
func TestPostgres_AllocationCreditRelease(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer, product := ids.New(), ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(100), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(0), ToleranceAmount: d(0), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits}, 0); err != nil {
		t.Fatal(err)
	}
	quota, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-03-01", "2027-03-31", 30))
	if err != nil {
		t.Fatal(err)
	}
	held := func(qty int64) ConfirmResult {
		t.Helper()
		o, _, err := s.CreateOrder(ctx, orderOf(le, customer, product, "2027-03-10", qty)) // 10 per unit, over the 100 limit
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", time.Now())
		if err != nil || res.Confirmed || len(res.Order.Holds) != 1 {
			t.Fatalf("want a CREDIT hold: %v %+v", err, res)
		}
		return res
	}
	releaser := principalCtx("credit-manager")

	first := held(20)
	ok, err := s.ReleaseHold(releaser, first.Order.ID, first.Order.Holds[0].ID, first.Order.Version, "credit-manager", time.Now())
	if err != nil || !ok.Confirmed {
		t.Fatalf("release confirms: %v %+v", err, ok)
	}
	if q, _ := s.GetAllocationQuota(ctx, quota.ID); !q.ConsumedQty.Equal(d(20)) {
		t.Fatalf("the release confirmation draws quota: %s", q.ConsumedQty)
	}

	second := held(20) // 10 left, no later period
	res, err := s.ReleaseHold(releaser, second.Order.ID, second.Order.Holds[0].ID, second.Order.Version, "credit-manager", time.Now())
	if err != nil {
		t.Fatalf("an allocation shortfall must not undo the credit release: %v", err)
	}
	if res.Confirmed || res.Order.Status == domain.OrderConfirmed || res.Order.Holds[0].Status != domain.HoldReleased {
		t.Fatalf("hold released, order unconfirmed: confirmed=%v status=%s holds=%+v", res.Confirmed, res.Order.Status, res.Order.Holds)
	}
	if len(res.Order.Warnings) != 1 || res.Order.Warnings[0].Code != CodeAllocationExhausted {
		t.Fatalf("SLS-0013 warning: %+v", res.Order.Warnings)
	}
	if res.Order.Lines[0].ConfirmedShipDate != nil {
		t.Fatalf("the rolled-back confirmation leaves no confirmed date: %v", *res.Order.Lines[0].ConfirmedShipDate)
	}
	if q, _ := s.GetAllocationQuota(ctx, quota.ID); !q.ConsumedQty.Equal(d(20)) || len(q.Consumptions) != 1 {
		t.Fatalf("nothing drawn by the refused confirmation: %s %d", q.ConsumedQty, len(q.Consumptions))
	}
	stored, err := s.GetOrder(ctx, second.Order.ID)
	if err != nil || stored.Version != res.Order.Version {
		t.Fatalf("the returned version is the stored one: %v %d vs %d", err, stored.Version, res.Order.Version)
	}
	// Raising the quota lets the order confirm; the still-valid release covers credit.
	q, _ := s.GetAllocationQuota(ctx, quota.ID)
	if _, err := s.ChangeAllocationQuota(ctx, q.ID, q.Version, d(40), "demand-planner", time.Now()); err != nil {
		t.Fatal(err)
	}
	again, err := s.ConfirmOrder(ctx, second.Order.ID, stored.Version, "sales-rep", time.Now())
	if err != nil || !again.Confirmed {
		t.Fatalf("confirm after the quota change: %v %+v", err, again.Evaluation)
	}
}

// TestPostgres_AllocationLegalEntityScope checks that a quota is invisible to
// a caller scoped to another legal entity and to another tenant.
func TestPostgres_AllocationLegalEntityScope(t *testing.T) {
	s, _, ctx := migrated(t)
	le, product := ids.New(), ids.New()
	quota, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-03-01", "2027-03-31", 30))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]context.Context{
		"other legal entity": tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "x", Duties: testkit.ScopeDuties("x")}),
		"other tenant":       tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "x", Duties: testkit.ScopeDuties("")}),
	} {
		if _, err := s.GetAllocationQuota(c, quota.ID); problemCode(t, err) != CodeQuotaNotFound {
			t.Fatalf("%s: get %v", name, err)
		}
		if list, err := s.ListAllocationQuotas(c, AllocationFilter{Limit: 50}); err != nil || len(list) != 0 {
			t.Fatalf("%s: list %v %+v", name, err, list)
		}
		if _, err := s.ChangeAllocationQuota(c, quota.ID, quota.Version, d(50), "x", time.Now()); problemCode(t, err) != CodeQuotaNotFound {
			t.Fatalf("%s: change %v", name, err)
		}
	}
	if own, err := s.ListAllocationQuotas(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "x", Duties: testkit.ScopeDuties(le)}),
		AllocationFilter{Limit: 50}); err != nil || len(own) != 1 {
		t.Fatalf("a caller scoped to the legal entity sees its quota: %v %+v", err, own)
	}
}
