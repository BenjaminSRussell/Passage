package store

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

func migrated(t *testing.T) (*Postgres, *pgxpool.Pool, context.Context) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(pool), pool, ctx
}

func oneLineOrder(le, customer, product string, orderDay time.Time) NewOrder {
	line := Line{LineNo: 1, ProductID: product, Quantity: d(2), Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: d(10),
		DiscPct: d(0), LineNet: d(20), RequestedShipDate: "2027-03-04"}
	return NewOrder{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "USD",
		RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: ids.New(), OrderDate: orderDay, Actor: "sales-rep", Lines: []Line{line}}
}

// TestPostgres_ProductListingDenial covers AC-27.11 and BR-27.13: customer C9
// with a DENY listing for FG-120 is refused with PRODUCT_NOT_LISTED; a reason
// without the approve duty is still refused; the duty plus a reason accepts
// the line and logs the override on the order. Group entries apply when the
// order names the group, a customer ALLOW beats a group DENY, and validity is
// evaluated on the order date.
func TestPostgres_ProductListingDenial(t *testing.T) {
	s, _, ctx := migrated(t)
	le, c9, other, fg120 := ids.New(), ids.New(), ids.New(), ids.New()
	day := time.Date(2027, 3, 1, 15, 0, 0, 0, time.UTC)
	add := func(scope, key, typ string, from, to *string) ProductListing {
		l, err := s.CreateListing(ctx, ProductListing{LegalEntityID: le, ScopeType: scope, ScopeKey: key, ProductID: fg120, ListingType: typ,
			ValidFrom: from, ValidTo: to, CreatedBy: "listing-admin"})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	deny := add(domain.ScopeCustomer, c9, domain.ListingDeny, nil, nil)
	if deny.ID == "" || deny.CreatedBy != "listing-admin" || deny.CreatedAt.IsZero() {
		t.Fatalf("stored listing %+v", deny)
	}

	if _, _, err := s.CreateOrder(ctx, oneLineOrder(le, c9, fg120, day)); problemCode(t, err) != CodeProductNotListed {
		t.Fatalf("denied line: %v", err)
	}
	in := oneLineOrder(le, c9, fg120, day)
	in.ListingOverrideReason = "replacement for recalled batch"
	if _, _, err := s.CreateOrder(ctx, in); problemCode(t, err) != CodeProductNotListed {
		t.Fatalf("reason without sales.order.approve must still be refused: %v", err)
	}
	in.CanOverrideListing = true
	in.ListingOverrideReason = "  "
	if _, _, err := s.CreateOrder(ctx, in); problemCode(t, err) != CodeProductNotListed {
		t.Fatalf("the duty without a reason must still be refused: %v", err)
	}
	in.ListingOverrideReason = "replacement for recalled batch"
	order, created, err := s.CreateOrder(ctx, in)
	if err != nil || !created {
		t.Fatalf("override: created=%v err=%v", created, err)
	}
	if len(order.ListingOverrides) != 1 || order.ListingOverrides[0].Decision != domain.ReasonCustomerDeny ||
		order.ListingOverrides[0].Reason != "replacement for recalled batch" || order.ListingOverrides[0].Actor != "sales-rep" {
		t.Fatalf("override log %+v", order.ListingOverrides)
	}
	reread, err := s.GetOrder(ctx, order.ID)
	if err != nil || len(reread.ListingOverrides) != 1 {
		t.Fatalf("override not persisted: %+v %v", reread.ListingOverrides, err)
	}

	if _, _, err := s.CreateOrder(ctx, oneLineOrder(le, other, fg120, day)); err != nil {
		t.Fatalf("a DENY for C9 must not restrict another customer: %v", err)
	}
	add(domain.ScopeGroup, "EU-RETAIL", domain.ListingDeny, nil, nil)
	grouped := oneLineOrder(le, other, fg120, day)
	group := "EU-RETAIL"
	grouped.CustomerGroup = &group
	if _, _, err := s.CreateOrder(ctx, grouped); problemCode(t, err) != CodeProductNotListed {
		t.Fatalf("group DENY: %v", err)
	}
	add(domain.ScopeCustomer, other, domain.ListingAllow, nil, nil)
	grouped.RequestHash = ids.New()
	if o, _, err := s.CreateOrder(ctx, grouped); err != nil || o.CustomerGroup == nil || *o.CustomerGroup != group {
		t.Fatalf("customer ALLOW beats group DENY: %v", err)
	}

	if err := s.DeleteListing(ctx, deny.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteListing(ctx, deny.ID); problemCode(t, err) != CodeListingNotFound {
		t.Fatalf("second delete: %v", err)
	}
	if _, _, err := s.CreateOrder(ctx, oneLineOrder(le, c9, fg120, day)); problemCode(t, err) != CodeProductNotListed {
		t.Fatalf("once FG-120 has an ALLOW list, a customer not on it is NOT_LISTED: %v", err)
	}

	fg100 := ids.New()
	from, to := "2027-04-01", "2027-04-30"
	if _, err := s.CreateListing(ctx, ProductListing{LegalEntityID: le, ScopeType: domain.ScopeCustomer, ScopeKey: c9, ProductID: fg100,
		ListingType: domain.ListingDeny, ValidFrom: &from, ValidTo: &to, CreatedBy: "listing-admin"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateOrder(ctx, oneLineOrder(le, c9, fg100, day)); err != nil {
		t.Fatalf("a DENY that starts after the order date does not apply: %v", err)
	}
	if _, _, err := s.CreateOrder(ctx, oneLineOrder(le, c9, fg100, day.AddDate(0, 1, 0))); problemCode(t, err) != CodeProductNotListed {
		t.Fatalf("a DENY valid on the order date applies: %v", err)
	}

	items, err := s.ListListings(ctx, ListingFilter{ProductID: fg100, ScopeKey: c9, Limit: 50})
	if err != nil || len(items) != 1 || items[0].ValidFrom == nil || *items[0].ValidFrom != from {
		t.Fatalf("listing inquiry by customer: %+v %v", items, err)
	}
	if items, err := s.ListListings(ctx, ListingFilter{LegalEntityID: ids.New(), Limit: 50}); err != nil || len(items) != 0 {
		t.Fatalf("another legal entity sees %d entries: %v", len(items), err)
	}
}

func agreementFixture(t *testing.T, s *Postgres, ctx context.Context, le string, commitment int64, from, to *string) Agreement {
	t.Helper()
	customer := ids.New()
	a, created, err := s.CreateAgreement(ctx, NewAgreement{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, ProductID: ids.New(),
		Unit: "EA", UnitPrice: d(12), Currency: "USD", Commitment: d(commitment), ValidFrom: from, ValidTo: to,
		IdempotencyKey: ids.New(), RequestHash: "agreement", Actor: "key-account"})
	if err != nil || !created {
		t.Fatalf("create agreement: created=%v err=%v", created, err)
	}
	return a
}

func release(qty int64, key string) AgreementRelease {
	return AgreementRelease{Quantity: d(qty), SiteID: ids.New(), WarehouseID: ids.New(), RequestedShipDate: "2027-04-15",
		IdempotencyKey: key, RequestHash: key, Actor: "key-account"}
}

// TestPostgres_AgreementReleases covers FR-27.10, BR-27.9 and AC-27.9: each
// release creates a DRAFT order at the agreement price and decrements the
// commitment in the same transaction; a fully released 1,000-unit agreement is
// EXHAUSTED and the next release is refused with AGREEMENT_EXHAUSTED and
// changes nothing; a replay returns the stored result without drawing again.
func TestPostgres_AgreementReleases(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := agreementFixture(t, s, ctx, le, 1000, nil, nil)
	if a.AgreementNumber != "SA-0000001" || a.Status != domain.AgreementActive || !a.Remaining.Equal(d(1000)) || a.Version != 1 {
		t.Fatalf("new agreement %+v", a)
	}
	replay, created, err := s.CreateAgreement(ctx, NewAgreement{LegalEntityID: le, IdempotencyKey: "dup", RequestHash: "x", CustomerID: a.CustomerID,
		InvoiceAccountID: a.CustomerID, ProductID: a.ProductID, Unit: "EA", UnitPrice: d(1), Currency: "USD", Commitment: d(1), Actor: "k"})
	if err != nil || !created || replay.AgreementNumber != "SA-0000002" {
		t.Fatalf("second agreement: %+v %v", replay, err)
	}
	if again, created, err := s.CreateAgreement(ctx, NewAgreement{LegalEntityID: le, IdempotencyKey: "dup", RequestHash: "x"}); err != nil || created || again.ID != replay.ID {
		t.Fatalf("create replay: created=%v err=%v", created, err)
	}
	if _, _, err := s.CreateAgreement(ctx, NewAgreement{LegalEntityID: le, IdempotencyKey: "dup", RequestHash: "y"}); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("create key reuse: %v", err)
	}

	first, created, err := s.ReleaseAgreement(ctx, a.ID, release(600, "r1"), now)
	if err != nil || !created {
		t.Fatalf("release 600: created=%v err=%v", created, err)
	}
	if !first.Agreement.Released.Equal(d(600)) || !first.Agreement.Remaining.Equal(d(400)) || first.Agreement.Version != 2 {
		t.Fatalf("after 600: %+v", first.Agreement)
	}
	o := first.Order
	if o.Status != domain.OrderDraft || o.Channel != agreementChannel || o.CustomerID != a.CustomerID || len(o.Lines) != 1 ||
		!o.Lines[0].UnitPrice.Equal(d(12)) || !o.Lines[0].Quantity.Equal(d(600)) || !o.NetTotal.Equal(d(7200)) || o.Lines[0].ProductID != a.ProductID {
		t.Fatalf("released order %+v", o)
	}

	again, created, err := s.ReleaseAgreement(ctx, a.ID, release(600, "r1"), now)
	if err != nil || created || again.Order.ID != o.ID || !again.Agreement.Released.Equal(d(600)) {
		t.Fatalf("release replay must not draw again: created=%v released=%s err=%v", created, again.Agreement.Released, err)
	}
	reused := release(5, "r1")
	reused.RequestHash = "another body"
	if _, _, err := s.ReleaseAgreement(ctx, a.ID, reused, now); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("release key reuse with another body: %v", err)
	}

	last, _, err := s.ReleaseAgreement(ctx, a.ID, release(400, "r2"), now)
	if err != nil || last.Agreement.Status != domain.AgreementExhausted || !last.Agreement.Remaining.IsZero() {
		t.Fatalf("exact draw-down: %+v %v", last.Agreement, err)
	}
	if _, _, err := s.ReleaseAgreement(ctx, a.ID, release(1, "r3"), now); problemCode(t, err) != CodeAgreementExhausted {
		t.Fatalf("AC-27.9 release after exhaustion: %v", err)
	}
	if n := countWhere(t, pool, `select count(*) from sales_order where channel = 'AGREEMENT'`); n != 2 {
		t.Fatalf("a refused release must not leave an order: %d agreement orders", n)
	}

	got, err := s.GetAgreement(ctx, a.ID)
	if err != nil || len(got.Releases) != 2 || got.Releases[0].OrderNumber != last.Order.OrderNumber || !got.Releases[1].Quantity.Equal(d(600)) {
		t.Fatalf("release history %+v %v", got.Releases, err)
	}
	if types := outboxTypes(t, pool, a.ID); len(types) != 2 || types[0] != salesevents.AgreementReleased {
		t.Fatalf("outbox %v", types)
	}
	var raw []byte
	if err := pool.QueryRow(context.Background(), `select payload->'data' from outbox where aggregate_id = $1 and type = $2
		order by created_at desc, id desc limit 1`, a.ID, salesevents.AgreementReleased).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var evt salesevents.AgreementReleasedData
	if err := json.Unmarshal(raw, &evt); err != nil || evt.SalesOrderID != last.Order.ID || evt.Quantity != "400" ||
		evt.Released != "1000" || evt.Remaining != "0" || evt.Status != domain.AgreementExhausted || evt.Version != 3 || evt.AgreementNumber != "SA-0000001" {
		t.Fatalf("agreement.released %+v %v", evt, err)
	}

	list, err := s.ListAgreements(ctx, AgreementFilter{LegalEntityID: le, Status: domain.AgreementExhausted, Limit: 50})
	if err != nil || len(list) != 1 || list[0].ID != a.ID || list[0].Releases != nil {
		t.Fatalf("list exhausted: %+v %v", list, err)
	}
}

// TestPostgres_AgreementValidityAndConcurrency: a release outside the validity
// period is refused, and concurrent releases never draw more than the
// commitment (the row lock serializes them).
func TestPostgres_AgreementValidityAndConcurrency(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	from, to := "2027-01-01", "2027-06-30"
	a := agreementFixture(t, s, ctx, le, 100, &from, &to)
	if _, _, err := s.ReleaseAgreement(ctx, a.ID, release(1, "early"), time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC)); problemCode(t, err) != CodeAgreementNotValid {
		t.Fatalf("before validFrom: %v", err)
	}
	if _, _, err := s.ReleaseAgreement(ctx, a.ID, release(1, "late"), time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC)); problemCode(t, err) != CodeAgreementNotValid {
		t.Fatalf("after validTo: %v", err)
	}
	if _, _, err := s.ReleaseAgreement(ctx, ids.New(), release(1, "missing"), time.Now()); problemCode(t, err) != CodeAgreementNotFound {
		t.Fatalf("unknown agreement: %v", err)
	}

	now := time.Date(2027, 6, 30, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.ReleaseAgreement(ctx, a.ID, release(30, ids.New()), now)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok, exhausted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case problemCode(t, err) == CodeAgreementExhausted:
			exhausted++
		}
	}
	if ok != 3 || exhausted != 5 {
		t.Fatalf("100 units in releases of 30: %d applied, %d exhausted", ok, exhausted)
	}
	got, err := s.GetAgreement(ctx, a.ID)
	if err != nil || !got.Released.Equal(d(90)) || len(got.Releases) != 3 {
		t.Fatalf("after concurrent releases: released %s, %d releases, %v", got.Released, len(got.Releases), err)
	}
	if n := countWhere(t, pool, `select count(*) from sales_order where channel = 'AGREEMENT'`); n != 3 {
		t.Fatalf("%d agreement orders", n)
	}
}

// TestPostgres_ApplyHold covers FR-27.8 and BR-27.4 for holds applied by hand:
// a FRAUD hold bumps the version, is written with orderhold.applied in the same
// transaction and blocks confirmation; a second FRAUD hold is refused; another
// user releases it with sales.order.hold and the order confirms; a cancelled
// order takes no hold.
func TestPostgres_ApplyHold(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	order, _, err := s.CreateOrder(ctx, oneLineOrder(le, ids.New(), ids.New(), time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyHold(ctx, order.ID, order.Version, HoldRequest{HoldType: domain.HoldCredit, ReasonCode: "X"}, "fraud-analyst"); problemCode(t, err) != CodeHoldNotApplicable {
		t.Fatalf("CREDIT by hand: %v", err)
	}
	if _, err := s.ApplyHold(ctx, order.ID, order.Version+1, HoldRequest{HoldType: domain.HoldFraud, ReasonCode: "X"}, "fraud-analyst"); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	held, err := s.ApplyHold(ctx, order.ID, order.Version, HoldRequest{HoldType: domain.HoldFraud, ReasonCode: "CARD-MISMATCH"}, "fraud-analyst")
	if err != nil {
		t.Fatal(err)
	}
	if held.Version != order.Version+1 || len(held.Holds) != 1 || held.Holds[0].Status != domain.HoldActive || held.Holds[0].AppliedBy != "fraud-analyst" ||
		held.Holds[0].ReasonCode != "CARD-MISMATCH" || !held.Holds[0].OrderAmount.Equal(d(20)) {
		t.Fatalf("held order %+v", held)
	}
	var evt salesevents.OrderHoldData
	if err := json.Unmarshal(outboxData(t, pool, order.ID, salesevents.OrderHoldApplied), &evt); err != nil ||
		evt.HoldType != domain.HoldFraud || evt.Version != held.Version || evt.Actor != "fraud-analyst" {
		t.Fatalf("orderhold.applied %+v %v", evt, err)
	}
	if _, err := s.ApplyHold(ctx, order.ID, held.Version, HoldRequest{HoldType: domain.HoldFraud, ReasonCode: "AGAIN"}, "fraud-analyst"); problemCode(t, err) != CodeHoldNotApplicable {
		t.Fatalf("second FRAUD hold: %v", err)
	}
	if _, err := s.ConfirmOrder(ctx, order.ID, held.Version, "sales-rep", time.Now()); problemCode(t, err) != CodeHoldBlocks {
		t.Fatalf("FRAUD must block confirmation: %v", err)
	}
	if _, err := s.ReleaseHold(ctx, order.ID, held.Holds[0].ID, held.Version, "fraud-analyst", time.Now()); problemCode(t, err) != "AUTH-0003" {
		t.Fatalf("the applier may not release: %v", err)
	}
	released, err := s.ReleaseHold(ctx, order.ID, held.Holds[0].ID, held.Version, "sales-manager", time.Now())
	if err != nil || released.Confirmed {
		t.Fatalf("releasing a FRAUD hold does not confirm by itself: %+v %v", released, err)
	}
	confirmed, err := s.ConfirmOrder(ctx, order.ID, released.Order.Version, "sales-rep", time.Now())
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirm after release: %v", err)
	}
	manual, err := s.ApplyHold(ctx, order.ID, confirmed.Order.Version, HoldRequest{HoldType: domain.HoldManual, ReasonCode: "CUSTOMER-CALL"}, "sales-manager")
	if err != nil || manual.Status != domain.OrderConfirmed {
		t.Fatalf("MANUAL hold on a confirmed order: %v", err)
	}
	cancelled, err := s.CancelOrder(ctx, order.ID, manual.Version, "customer cancelled", "sales-rep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyHold(ctx, order.ID, cancelled.Version, HoldRequest{HoldType: domain.HoldManual, ReasonCode: "X"}, "sales-manager"); problemCode(t, err) != CodeHoldNotApplicable {
		t.Fatalf("hold on a cancelled order: %v", err)
	}
}
