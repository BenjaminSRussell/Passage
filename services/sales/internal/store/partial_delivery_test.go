package store

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

func savePartialDefault(t *testing.T, s *Postgres, ctx context.Context, le, policy string) {
	t.Helper()
	cur, err := s.GetParameters(ctx, le)
	if err != nil {
		t.Fatal(err)
	}
	in := cur
	in.LegalEntityID = le
	if cur.Version == 0 {
		in.Parameters = domain.DefaultParameters()
	}
	in.PartialDeliveryDefault = policy
	if _, err := s.PutParameters(ctx, in, cur.Version, "admin"); err != nil {
		t.Fatalf("save %s: %v", policy, err)
	}
}

// TestPostgres_PartialDeliveryDefaultResolves: the legal entity's
// partial_delivery_default is what a product and customer without a rule get
// from the resolve call; a rule of any scope still replaces it, and another
// legal entity keeps ALLOWED. A PUT without the policy keeps the saved one.
// The policy travels in parameters.changed.
func TestPostgres_PartialDeliveryDefaultResolves(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, other, product, c1, c2 := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	got, err := s.GetParameters(ctx, le)
	if err != nil || got.PartialDeliveryDefault != domain.PartialAllowed {
		t.Fatalf("a legal entity that never saved reads ALLOWED: %+v %v", got, err)
	}
	savePartialDefault(t, s, ctx, le, domain.PartialCompleteOnly)
	if got, err := s.GetParameters(ctx, le); err != nil || got.PartialDeliveryDefault != domain.PartialCompleteOnly || got.Version != 1 {
		t.Fatalf("saved: %+v %v", got, err)
	}
	// A PUT that leaves the policy out (a client that predates it) keeps
	// COMPLETE_ONLY; a create without it saves ALLOWED.
	cur, err := s.GetParameters(ctx, le)
	if err != nil {
		t.Fatal(err)
	}
	cur.PartialDeliveryDefault = ""
	cur.QuotationExpiryDays = 45
	if got, err := s.PutParameters(ctx, cur, cur.Version, "old-client"); err != nil || got.PartialDeliveryDefault != domain.PartialCompleteOnly || got.QuotationExpiryDays != 45 || got.Version != 2 {
		t.Fatalf("absent keeps the saved policy: %+v %v", got, err)
	}
	fresh := SalesParameters{LegalEntityID: other, Parameters: domain.DefaultParameters()}
	fresh.PartialDeliveryDefault = ""
	if got, err := s.PutParameters(ctx, fresh, 0, "old-client"); err != nil || got.PartialDeliveryDefault != domain.PartialAllowed {
		t.Fatalf("absent on create saves ALLOWED: %+v %v", got, err)
	}
	res, err := s.ResolveDeliveryTolerance(ctx, le, product, c1)
	if err != nil || res.Source != domain.ToleranceDefault || res.Tolerance.PartialDelivery != domain.PartialCompleteOnly ||
		!res.Tolerance.OverdeliveryPct.IsZero() || !res.Tolerance.UnderdeliveryPct.IsZero() {
		t.Fatalf("the default follows the legal entity: %+v %v", res, err)
	}
	if res, err := s.ResolveDeliveryTolerance(ctx, other, product, c1); err != nil || res.Tolerance.PartialDelivery != domain.PartialAllowed {
		t.Fatalf("another legal entity: %+v %v", res, err)
	}
	if _, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, nil, "0", "0", domain.PartialAllowed)); err != nil {
		t.Fatal(err)
	}
	if res, err := s.ResolveDeliveryTolerance(ctx, le, product, c2); err != nil || res.Source != domain.ToleranceAllCustomers || res.Tolerance.PartialDelivery != domain.PartialAllowed {
		t.Fatalf("a rule replaces the entity default: %+v %v", res, err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from outbox where aggregate_type = 'parameters' and aggregate_id = $1
		and payload->'data'->>'partial_delivery_default' = 'COMPLETE_ONLY'`, le).Scan(&n); err != nil || n != 2 {
		t.Fatalf("parameters.changed carries the policy: %d %v", n, err)
	}
}

// TestPostgres_PromiseFollowsPartialDeliveryPolicy: a promise that does not say
// completeOnly follows the policy of the line's product and the order's
// customer. 45 against ATP 40 and 5 later splits in two schedule lines under
// ALLOWED and ships whole on the date the cumulative ATP covers it (03-04)
// under ONE_SHIPMENT (a customer rule) or a COMPLETE_ONLY entity default; an explicit completeOnly overrides the policy either way.
func TestPostgres_PromiseFollowsPartialDeliveryPolicy(t *testing.T) {
	s, _, ctx := migrated(t)
	le, product, strict, relaxed := ids.New(), ids.New(), ids.New(), ids.New()
	if _, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, &strict, "0", "0", domain.PartialOneShipment)); err != nil {
		t.Fatal(err)
	}
	promise := func(customer string, completeOnly *bool) PromiseResult {
		t.Helper()
		o := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-01", 45))
		res, err := s.PromiseLine(ctx, o.ID, o.Version, PromiseRequest{LineNo: 1, Periods: atpBuckets(), CompleteOnly: completeOnly}, "planner", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// No rule for the relaxed customer and no saved parameters: ALLOWED splits.
	if res := promise(relaxed, nil); res.CompleteOnly || res.PartialDelivery != domain.PartialAllowed || len(res.Schedule) != 2 {
		t.Fatalf("ALLOWED splits the line: %+v", res)
	}
	// The customer's ONE_SHIPMENT rule asks for the whole quantity on one date: the first date the cumulative ATP covers 45 is 03-04.
	res := promise(strict, nil)
	if !res.CompleteOnly || res.PartialDelivery != domain.PartialOneShipment || !res.Promise.Complete || len(res.Schedule) != 1 ||
		!res.Schedule[0].Quantity.Equal(d(45)) || *res.Schedule[0].DeliveryDate != "2027-03-04" {
		t.Fatalf("ONE_SHIPMENT keeps the line whole: %+v", res)
	}
	// An explicit completeOnly=false overrides the policy; an explicit true overrides ALLOWED.
	no, yes := false, true
	if res := promise(strict, &no); res.CompleteOnly || res.PartialDelivery != "" || len(res.Schedule) != 2 {
		t.Fatalf("the caller's false wins: %+v", res)
	}
	if res := promise(relaxed, &yes); !res.CompleteOnly || res.PartialDelivery != "" || len(res.Schedule) != 1 || !res.Schedule[0].Quantity.Equal(d(45)) {
		t.Fatalf("the caller's true wins: %+v", res)
	}
	// The entity default applies to a customer without a rule, from the next promise on.
	savePartialDefault(t, s, ctx, le, domain.PartialCompleteOnly)
	if res := promise(relaxed, nil); !res.CompleteOnly || res.PartialDelivery != domain.PartialCompleteOnly || len(res.Schedule) != 1 || !res.Schedule[0].Quantity.Equal(d(45)) {
		t.Fatalf("COMPLETE_ONLY entity default: %+v", res)
	}
	// Whole quantity on one date when the supply covers it.
	o := confirmed(t, s, ctx, orderOf(le, relaxed, product, "2027-03-01", 30))
	res, err := s.PromiseLine(ctx, o.ID, o.Version, PromiseRequest{LineNo: 1, Periods: atpBuckets()}, "planner", time.Now())
	if err != nil || !res.Promise.Complete || len(res.Schedule) != 1 || !res.Schedule[0].Quantity.Equal(d(30)) {
		t.Fatalf("a covered line ships whole: %+v %v", res, err)
	}
}
