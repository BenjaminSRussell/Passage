package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func tolIn(le, product string, customer *string, over, under, partial string) NewDeliveryTolerance {
	key := ids.New()
	return NewDeliveryTolerance{LegalEntityID: le, ProductID: product, CustomerID: customer,
		Tolerance:      domain.DeliveryTolerance{OverdeliveryPct: decimal.RequireFromString(over), UnderdeliveryPct: decimal.RequireFromString(under), PartialDelivery: partial},
		IdempotencyKey: key, RequestHash: key, Actor: "sales-admin"}
}

// TestPostgres_DeliveryToleranceLifecycle covers create with Idempotency-Key,
// the replay, key reuse with another body, one rule per product and customer
// (SLS-0126), the If-Match replace, an unchanged replace that keeps its
// version, the delete, the events and the list filters.
func TestPostgres_DeliveryToleranceLifecycle(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, product, other := ids.New(), ids.New(), ids.New()
	c1 := ids.New()
	in := tolIn(le, product, &c1, "5", "2.5", domain.PartialCompleteOnly)
	first, created, err := s.CreateDeliveryTolerance(ctx, in)
	if err != nil || !created || first.Version != 1 || first.OverdeliveryPct.String() != "5" || first.UnderdeliveryPct.String() != "2.5" || first.PartialDelivery != domain.PartialCompleteOnly {
		t.Fatalf("create: %+v %v %v", first, created, err)
	}
	replay, created, err := s.CreateDeliveryTolerance(ctx, in)
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("replay must return the stored rule: %+v %v %v", replay, created, err)
	}
	reused := in
	reused.RequestHash = "other"
	if _, _, err := s.CreateDeliveryTolerance(ctx, reused); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, &c1, "1", "1", domain.PartialAllowed)); problemCode(t, err) != CodeToleranceExists {
		t.Fatalf("second rule for one product and customer: %v", err)
	}
	all, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, nil, "10", "0", domain.PartialAllowed))
	if err != nil {
		t.Fatalf("an all-customers rule is its own scope: %v", err)
	}
	if _, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, nil, "3", "0", domain.PartialAllowed)); problemCode(t, err) != CodeToleranceExists {
		t.Fatalf("second all-customers rule (NULL customer must still collide): %v", err)
	}
	if _, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, other, &c1, "0", "0", domain.PartialOneShipment)); err != nil {
		t.Fatal(err)
	}
	if got := outboxTypes(t, pool, first.ID); len(got) != 1 || got[0] != salesevents.DeliveryToleranceCreated {
		t.Fatalf("created event: %v", got)
	}

	next := domain.DeliveryTolerance{OverdeliveryPct: decimal.RequireFromString("7.5"), UnderdeliveryPct: decimal.Zero, PartialDelivery: domain.PartialAllowed}
	if _, err := s.ReplaceDeliveryTolerance(ctx, first.ID, first.Version+1, next, "admin", time.Now()); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale replace: %v", err)
	}
	same, err := s.ReplaceDeliveryTolerance(ctx, first.ID, first.Version, first.values(), "admin", time.Now())
	if err != nil || same.Version != first.Version {
		t.Fatalf("unchanged replace must keep the version: %+v %v", same, err)
	}
	changed, err := s.ReplaceDeliveryTolerance(ctx, first.ID, first.Version, next, "admin", time.Now())
	if err != nil || changed.Version != 2 || changed.OverdeliveryPct.String() != "7.5" || changed.UpdatedBy != "admin" || changed.PartialDelivery != domain.PartialAllowed {
		t.Fatalf("replace: %+v %v", changed, err)
	}
	if got := outboxTypes(t, pool, first.ID); len(got) != 2 || got[1] != salesevents.DeliveryToleranceChanged {
		t.Fatalf("changed event: %v", got)
	}

	byProduct, err := s.ListDeliveryTolerances(ctx, DeliveryToleranceFilter{LegalEntityID: le, ProductID: product, Limit: 50})
	if err != nil || len(byProduct) != 2 || byProduct[0].ID != all.ID || byProduct[1].ID != first.ID {
		t.Fatalf("list by product puts the all-customers rule first: %+v %v", byProduct, err)
	}
	byCustomer, err := s.ListDeliveryTolerances(ctx, DeliveryToleranceFilter{CustomerID: c1, Limit: 50})
	if err != nil || len(byCustomer) != 2 {
		t.Fatalf("list by customer: %+v %v", byCustomer, err)
	}
	if got, err := s.GetDeliveryTolerance(ctx, first.ID); err != nil || got.Version != 2 {
		t.Fatalf("get: %+v %v", got, err)
	}

	if err := s.DeleteDeliveryTolerance(ctx, first.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := outboxTypes(t, pool, first.ID); len(got) != 3 || got[2] != salesevents.DeliveryToleranceRemoved {
		t.Fatalf("removed event: %v", got)
	}
	if _, err := s.GetDeliveryTolerance(ctx, first.ID); problemCode(t, err) != CodeToleranceNotFound {
		t.Fatalf("deleted rule: %v", err)
	}
	if err := s.DeleteDeliveryTolerance(ctx, first.ID, "admin"); problemCode(t, err) != CodeToleranceNotFound {
		t.Fatalf("second delete: %v", err)
	}
	if _, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, &c1, "1", "1", domain.PartialAllowed)); err != nil {
		t.Fatalf("a deleted rule frees its scope: %v", err)
	}
}

// TestPostgres_ResolveDeliveryTolerance covers the precedence: the customer's
// own rule replaces the all-customers rule, which replaces the default, and a
// rule of another legal entity, product or customer never applies.
func TestPostgres_ResolveDeliveryTolerance(t *testing.T) {
	s, _, ctx := migrated(t)
	le, product, c1, c2 := ids.New(), ids.New(), ids.New(), ids.New()
	resolve := func(le, product, customer string) ResolvedTolerance {
		t.Helper()
		got, err := s.ResolveDeliveryTolerance(ctx, le, product, customer)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := resolve(le, product, c1); got.Source != domain.ToleranceDefault || got.ToleranceID != "" || !got.Tolerance.OverdeliveryPct.IsZero() || got.Tolerance.PartialDelivery != domain.PartialAllowed {
		t.Fatalf("no rule is the default: %+v", got)
	}
	all, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, nil, "10", "5", domain.PartialAllowed))
	if err != nil {
		t.Fatal(err)
	}
	own, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, &c1, "1", "0", domain.PartialOneShipment))
	if err != nil {
		t.Fatal(err)
	}
	if got := resolve(le, product, c1); got.Source != domain.ToleranceCustomer || got.ToleranceID != own.ID || got.Tolerance.OverdeliveryPct.String() != "1" || got.Tolerance.PartialDelivery != domain.PartialOneShipment {
		t.Fatalf("own rule: %+v", got)
	}
	if got := resolve(le, product, c2); got.Source != domain.ToleranceAllCustomers || got.ToleranceID != all.ID || got.Tolerance.UnderdeliveryPct.String() != "5" {
		t.Fatalf("all-customers rule: %+v", got)
	}
	if got := resolve(le, ids.New(), c1); got.Source != domain.ToleranceDefault {
		t.Fatalf("another product: %+v", got)
	}
	if got := resolve(ids.New(), product, c1); got.Source != domain.ToleranceDefault {
		t.Fatalf("another legal entity: %+v", got)
	}
	if err := s.DeleteDeliveryTolerance(ctx, own.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if got := resolve(le, product, c1); got.Source != domain.ToleranceAllCustomers {
		t.Fatalf("after removing the own rule the customer falls back: %+v", got)
	}
}

// TestPostgres_DeliveryToleranceStaysInScope: under the application role, a
// caller scoped to one legal entity cannot create, read, list, change, remove
// or resolve a rule of another legal entity; row-level security hides it and
// the resolve falls back to the default rather than leaking the foreign rule.
func TestPostgres_DeliveryToleranceStaysInScope(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s is needed for the row-level security test", testkit.EnvAppURL)
	}
	s, _, ctx := migrated(t)
	own, foreign, product, customer := ids.New(), ids.New(), ids.New(), ids.New()
	theirs, _, err := s.CreateDeliveryTolerance(ctx, tolIn(foreign, product, &customer, "9", "9", domain.PartialOneShipment))
	if err != nil {
		t.Fatal(err)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "rep", LegalEntityID: own})
	if _, _, err := s.CreateDeliveryTolerance(scoped, tolIn(own, product, &customer, "1", "0", domain.PartialAllowed)); err != nil {
		t.Fatalf("own legal entity: %v", err)
	}
	if _, _, err := s.CreateDeliveryTolerance(scoped, tolIn(foreign, product, nil, "1", "0", domain.PartialAllowed)); err == nil {
		t.Fatal("a rule was stored for a legal entity outside the caller's scope")
	}
	if got, err := s.ListDeliveryTolerances(scoped, DeliveryToleranceFilter{Limit: 50}); err != nil || len(got) != 1 || got[0].LegalEntityID != own {
		t.Fatalf("scoped list: %v %+v", err, got)
	}
	if _, err := s.GetDeliveryTolerance(scoped, theirs.ID); problemCode(t, err) != CodeToleranceNotFound {
		t.Fatalf("read across legal entities: %v", err)
	}
	if _, err := s.ReplaceDeliveryTolerance(scoped, theirs.ID, theirs.Version, domain.DefaultTolerance(), "rep", time.Now()); problemCode(t, err) != CodeToleranceNotFound {
		t.Fatalf("change across legal entities: %v", err)
	}
	if err := s.DeleteDeliveryTolerance(scoped, theirs.ID, "rep"); problemCode(t, err) != CodeToleranceNotFound {
		t.Fatalf("remove across legal entities: %v", err)
	}
	if got, err := s.ResolveDeliveryTolerance(scoped, foreign, product, customer); err != nil || got.Source != domain.ToleranceDefault {
		t.Fatalf("resolve across legal entities must not see the rule: %v %+v", err, got)
	}
	if got, err := s.GetDeliveryTolerance(ctx, theirs.ID); err != nil || got.Version != 1 || got.PartialDelivery != domain.PartialOneShipment {
		t.Fatalf("the foreign rule is untouched: %v %+v", err, got)
	}
}
