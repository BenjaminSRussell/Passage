package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// TestPostgres_ParametersVersioning checks defaults for a legal entity that
// never saved, create-with-zero, a stale PUT (412), a matching PUT, and the
// parameters.changed event in the outbox.
func TestPostgres_ParametersVersioning(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	got, err := s.GetParameters(ctx, le)
	if err != nil || got.Version != 0 || !reflect.DeepEqual(got.Parameters, domain.DefaultParameters()) || got.UpdatedAt != nil {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	in := SalesParameters{LegalEntityID: le, Parameters: domain.Parameters{QuotationExpiryDays: 45, QuotationProbabilityDefault: 40, CreditReleaseValidityDays: 10,
		CreditHoldSLABusinessDays: 3, DefaultOrderType: "SUBSCRIPTION"}}
	saved, err := s.PutParameters(ctx, in, 0, "admin")
	if err != nil || saved.Version != 1 || saved.QuotationExpiryDays != 45 || saved.QuotationProbabilityDefault != 40 ||
		saved.UpdatedBy != "admin" || saved.UpdatedAt == nil {
		t.Fatalf("create: %+v %v", saved, err)
	}
	if _, err := s.PutParameters(ctx, in, 0, "admin"); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("second create must conflict: %v", err)
	}
	in.QuotationExpiryDays = 60
	if _, err := s.PutParameters(ctx, in, 5, "admin"); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale PUT: %v", err)
	}
	updated, err := s.PutParameters(ctx, in, 1, "admin")
	if err != nil || updated.Version != 2 || updated.QuotationExpiryDays != 60 {
		t.Fatalf("matching PUT: %+v %v", updated, err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from outbox where aggregate_type = 'parameters' and aggregate_id = $1`, le).Scan(&n); err != nil || n != 2 {
		t.Fatalf("parameters.changed rows %d err %v", n, err)
	}
	other, err := s.GetParameters(ctx, ids.New())
	if err != nil || other.Version != 0 {
		t.Fatalf("another legal entity keeps the defaults: %+v %v", other, err)
	}
}

// TestPostgres_ParametersDriveFlows checks that the saved values are read:
// quotation validity when none is given, the default order type, the CREDIT
// release validity and the hold queue's SLA due date (business days).
func TestPostgres_ParametersDriveFlows(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	issued := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC) // a Monday

	q, _, err := s.CreateQuotation(ctx, withoutValidity(threeLineQuotation(le, customer, "q-default"), issued))
	if err != nil || q.ValidUntil != "2027-03-31" || q.ProbabilityPct != domain.DefaultProbabilityPct {
		t.Fatalf("default validity and probability: %+v %v", q, err)
	}
	if _, err := s.PutParameters(ctx, SalesParameters{LegalEntityID: le, Parameters: domain.Parameters{QuotationExpiryDays: 10,
		QuotationProbabilityDefault: 25, CreditReleaseValidityDays: 14, CreditHoldSLABusinessDays: 4, DefaultOrderType: "SUBSCRIPTION"}}, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	q, _, err = s.CreateQuotation(ctx, withoutValidity(threeLineQuotation(le, customer, "q-ten"), issued))
	if err != nil || q.ValidUntil != "2027-03-11" || q.ProbabilityPct != 25 {
		t.Fatalf("saved validity and probability: %+v %v", q, err)
	}
	explicit := threeLineQuotation(le, customer, "q-explicit")
	if q, _, err = s.CreateQuotation(ctx, explicit); err != nil || q.ValidUntil != "2027-03-31" || q.ProbabilityPct != 60 {
		t.Fatalf("an explicit validity and probability win: %+v %v", q, err)
	}

	limits := domain.CreditProfile{Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(5),
		ToleranceAmount: d(1000), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits,
		Exposure: domain.Exposure{OpenAR: d(8500)}}, 0); err != nil {
		t.Fatal(err)
	}
	in := oneLineOrder(le, customer, ids.New(), issued)
	in.OrderType = ""
	in.Lines[0].UnitPrice, in.Lines[0].Quantity, in.Lines[0].LineNet = d(3000), d(1), d(3000)
	order, _, err := s.CreateOrder(ctx, in)
	if err != nil || order.OrderType != "SUBSCRIPTION" {
		t.Fatalf("default order type: %q %v", order.OrderType, err)
	}
	// Confirm places the CREDIT hold (exposure 8500 + 3000 is over the limit with tolerance).
	held, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", issued)
	if err != nil || held.Confirmed {
		t.Fatalf("confirm must hold: %+v %v", held.Confirmed, err)
	}
	queue, err := s.ListHolds(ctx, HoldQueueFilter{LegalEntityID: le, Limit: 50})
	if err != nil || len(queue) != 1 {
		t.Fatalf("queue: %v %+v", err, queue)
	}
	if want := domain.AddBusinessDays(queue[0].AppliedAt, 4); !queue[0].SLADueAt.Equal(want) || queue[0].SLADueAt.Sub(queue[0].AppliedAt) < 4*24*time.Hour {
		t.Fatalf("sla due %s applied %s, want %s", queue[0].SLADueAt, queue[0].AppliedAt, want)
	}
	releasedAt := time.Date(2027, 3, 2, 9, 0, 0, 0, time.UTC)
	if _, err := s.ReleaseHold(principalCtx("credit-manager"), order.ID, queue[0].HoldID, queue[0].OrderVersion, "credit-manager", releasedAt); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOrder(ctx, order.ID)
	if err != nil || len(got.Holds) != 1 || got.Holds[0].ReleasedUntil == nil || !got.Holds[0].ReleasedUntil.Equal(releasedAt.AddDate(0, 0, 14)) {
		t.Fatalf("release validity must be 14 days: %+v %v", got.Holds, err)
	}
}

func withoutValidity(in NewQuotation, issued time.Time) NewQuotation {
	in.ValidUntil, in.IssuedAt, in.ProbabilityPct = time.Time{}, issued, nil
	return in
}

// TestPostgres_ParametersScope: a caller scoped to another legal entity or
// tenant sees the defaults rather than the saved values and cannot replace
// them, and a create for a legal entity outside the scope fails (row-level
// security).
func TestPostgres_ParametersScope(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	saved, err := s.PutParameters(ctx, SalesParameters{LegalEntityID: le, Parameters: domain.Parameters{QuotationExpiryDays: 12,
		QuotationProbabilityDefault: 70, CreditReleaseValidityDays: 3, CreditHoldSLABusinessDays: 5, DefaultOrderType: "JOURNAL"}}, 0, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]context.Context{
		"other legal entity": tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "x", Duties: testkit.ScopeDuties("x")}),
		"other tenant":       tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "x", Duties: testkit.ScopeDuties("")}),
	} {
		if got, err := s.GetParameters(c, le); err != nil || got.Version != 0 || !reflect.DeepEqual(got.Parameters, domain.DefaultParameters()) {
			t.Fatalf("%s: get %+v %v", name, got, err)
		}
		if _, err := s.PutParameters(c, saved, saved.Version, "x"); problemCode(t, err) != CodeVersionConflict {
			t.Fatalf("%s: replace %v", name, err)
		}
	}
	otherLE := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "x", Duties: testkit.ScopeDuties("x")})
	if _, err := s.PutParameters(otherLE, SalesParameters{LegalEntityID: ids.New(), Parameters: domain.DefaultParameters()}, 0, "x"); err == nil {
		t.Fatal("a create for a legal entity outside the caller's scope must fail")
	}
	if got, err := s.GetParameters(ctx, le); err != nil || got.Version != 1 || got.QuotationExpiryDays != 12 {
		t.Fatalf("own scope keeps the saved values: %+v %v", got, err)
	}
}
