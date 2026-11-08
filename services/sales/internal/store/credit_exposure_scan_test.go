package store

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

// TestPostgres_ScanCreditProfilesPagesEveryProfile: the credit exposure report
// reads every profile of the scope in key order, batch by batch, so a profile
// past the first batch is measured; the customer and legal-entity filters and
// the legal-entity row-level security hold on every batch, and an error from
// the callback stops the scan.
func TestPostgres_ScanCreditProfilesPagesEveryProfile(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("credit-analyst")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(appOf(t, pool))
	le, other := ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(5), ToleranceAmount: d(1000),
		OldestItemDaysMax: 30, MaxDunningLevel: 3}
	var customers []string
	for i := 0; i < 5; i++ {
		c := ids.New()
		customers = append(customers, c)
		if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: c, CreditProfile: limits}, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: other, CustomerID: customers[0], CreditProfile: limits}, 0); err != nil {
		t.Fatal(err)
	}
	sort.Strings(customers)

	scan := func(ctx context.Context, le, customer string) ([]string, []int) {
		t.Helper()
		var seen []string
		var sizes []int
		if err := s.ScanCreditProfiles(ctx, le, customer, 2, func(page []CreditProfile) error {
			sizes = append(sizes, len(page))
			for _, c := range page {
				if !c.Exposure.OpenOrders.IsZero() {
					t.Fatalf("no orders, no open orders: %+v", c.Exposure)
				}
				seen = append(seen, c.CustomerID)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return seen, sizes
	}
	seen, sizes := scan(ctx, le, "")
	if len(seen) != 5 || len(sizes) != 3 || sizes[2] != 1 {
		t.Fatalf("five profiles in batches of two: %v %v", seen, sizes)
	}
	for i := range customers {
		if seen[i] != customers[i] {
			t.Fatalf("key order: %v want %v", seen, customers)
		}
	}
	if all, _ := scan(ctx, "", ""); len(all) != 6 {
		t.Fatalf("every legal entity in scope: %d", len(all))
	}
	if one, _ := scan(ctx, le, customers[4]); len(one) != 1 || one[0] != customers[4] {
		t.Fatalf("customer filter: %v", one)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: other, UserID: "x", Duties: testkit.ScopeDuties(other)})
	if mine, _ := scan(scoped, "", ""); len(mine) != 1 {
		t.Fatalf("a caller scoped to one legal entity sees only its profiles: %v", mine)
	}
	stop := errors.New("stop")
	batches := 0
	if err := s.ScanCreditProfiles(ctx, le, "", 2, func([]CreditProfile) error { batches++; return stop }); !errors.Is(err, stop) || batches != 1 {
		t.Fatalf("callback error stops the scan: %v after %d batches", err, batches)
	}
}
