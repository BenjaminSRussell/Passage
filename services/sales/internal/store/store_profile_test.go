package store

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

// TestPostgres_CreditProfileOptimisticConcurrency checks create-with-zero,
// read, a stale PUT (412 SLS-0006) and a matching PUT that bumps the version.
func TestPostgres_CreditProfileOptimisticConcurrency(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("credit-analyst")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	le, customer := ids.New(), ids.New()
	profile := CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
		Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(5), ToleranceAmount: d(1000),
		OldestItemDaysMax: 30, MaxDunningLevel: 3}}

	created, err := s.PutCreditProfile(ctx, profile, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCreditProfile(ctx, profile, 0); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("second create must conflict: %v", err)
	}
	read, err := s.GetCreditProfile(ctx, le, customer)
	if err != nil || read.Version != created.Version {
		t.Fatalf("read: version %d want %d err %v", read.Version, created.Version, err)
	}
	profile.Limit = d(12000)
	if _, err := s.PutCreditProfile(ctx, profile, created.Version+7); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale PUT: %v", err)
	}
	updated, err := s.PutCreditProfile(ctx, profile, created.Version)
	if err != nil || updated.Version != created.Version+1 || !updated.Limit.Equal(d(12000)) {
		t.Fatalf("matching PUT: version %d err %v limit %s", updated.Version, err, updated.Limit)
	}
	if _, err := s.GetCreditProfile(ctx, le, ids.New()); problemCode(t, err) != CodeProfileNotFound {
		t.Fatalf("unknown customer: %v", err)
	}
}

// TestPostgres_ChannelDuplicateRules checks BR-27.10: the same (channel,
// externalRef) with the same body replays the original order, and with a
// different body it is refused with SLS-0007.
func TestPostgres_ChannelDuplicateRules(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("integration")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	ref := "PO-7781"
	in := NewOrder{LegalEntityID: ids.New(), CustomerID: ids.New(), InvoiceAccountID: ids.New(), OrderType: "ORDER",
		Currency: "USD", RequestedShipDate: "2027-03-04", Channel: "EDI", ExternalRef: &ref, RequestHash: "body-a",
		Lines: []Line{{LineNo: 1, ProductID: ids.New(), Quantity: d(2), Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(),
			UnitPrice: d(5), DiscPct: d(0), LineNet: d(10), RequestedShipDate: "2027-03-04"}}}

	first, created, err := s.CreateOrder(ctx, in)
	if err != nil || !created {
		t.Fatalf("first intake: created=%v err=%v", created, err)
	}
	replay, created, err := s.CreateOrder(ctx, in)
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("same payload must replay: created=%v id=%s err=%v", created, replay.ID, err)
	}
	in.RequestHash = "body-b"
	if _, _, err := s.CreateOrder(ctx, in); problemCode(t, err) != CodeDuplicateIntake {
		t.Fatalf("different payload must be SLS-0007: %v", err)
	}
}
