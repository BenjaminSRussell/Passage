package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

func d(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

func principalCtx(user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: user, Duties: map[string]struct{}{}})
}

func problemCode(t *testing.T, err error) string {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) {
		t.Fatalf("want a problem, got %v", err)
	}
	return p.Code
}

// TestPostgres_CreditHoldAndRelease covers AC-27.3 and AC-27.2 end to end:
// a 3000 order over the 10,000 limit with 8,500 open AR places a CREDIT hold,
// a second confirm is refused, and a release by another user lets it confirm.
func TestPostgres_CreditHoldAndRelease(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	le, customer, product, site := ids.New(), ids.New(), ids.New(), ids.New()
	const day = "2027-03-04"

	limits := domain.CreditProfile{Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(5), ToleranceAmount: d(1000), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits, Exposure: domain.Exposure{OpenAR: d(8500)}}, 0); err != nil {
		t.Fatal(err)
	}

	key := "intake-1"
	line := Line{LineNo: 1, ProductID: product, Quantity: d(1), Unit: "EA", SiteID: site, WarehouseID: ids.New(), UnitPrice: d(3000), DiscPct: d(0), LineNet: d(3000), RequestedShipDate: day}
	in := NewOrder{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "USD", RequestedShipDate: day, Channel: "UI", IdempotencyKey: &key, RequestHash: "h1", Lines: []Line{line}}
	order, created, err := s.CreateOrder(ctx, in)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	replay, created, err := s.CreateOrder(ctx, in)
	if err != nil || created || replay.ID != order.ID {
		t.Fatalf("replay must return the original order: created=%v id=%s err=%v", created, replay.ID, err)
	}
	in.RequestHash = "h2"
	if _, _, err := s.CreateOrder(ctx, in); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("reused key with a new body: %v", err)
	}

	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	held, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || held.Confirmed {
		t.Fatalf("confirm over limit must place a hold: confirmed=%v err=%v", held.Confirmed, err)
	}
	if len(held.Order.Holds) != 1 || held.Order.Holds[0].HoldType != domain.HoldCredit || held.Order.Holds[0].Status != domain.HoldActive {
		t.Fatalf("want one ACTIVE CREDIT hold, got %+v", held.Order.Holds)
	}
	if _, err := s.ConfirmOrder(ctx, order.ID, held.Order.Version, "sales-rep", now); problemCode(t, err) != CodeCreditHold {
		t.Fatalf("confirm while held: %v", err)
	}

	releaser := principalCtx("credit-manager")
	if _, err := s.ReleaseHold(ctx, order.ID, held.Order.Holds[0].ID, held.Order.Version, "sales-rep", now); problemCode(t, err) != "AUTH-0003" {
		t.Fatalf("the user who applied the hold must not release it: %v", err)
	}
	released, err := s.ReleaseHold(releaser, order.ID, held.Order.Holds[0].ID, held.Order.Version, "credit-manager", now)
	if err != nil || released.Order.Holds[0].Status != domain.HoldReleased {
		t.Fatalf("release: %v %+v", err, released.Order.Holds)
	}
	// AC-27.3: the release by a CreditRelease user confirms the order.
	if !released.Confirmed || released.Order.Status != domain.OrderConfirmed {
		t.Fatalf("release must confirm the held order: confirmed=%v status=%s", released.Confirmed, released.Order.Status)
	}
	if released.Order.Lines[0].ConfirmedShipDate == nil || *released.Order.Lines[0].ConfirmedShipDate != day {
		t.Fatalf("confirmed ship date %v", released.Order.Lines[0].ConfirmedShipDate)
	}
	if _, err := s.ReleaseHold(releaser, order.ID, held.Order.Holds[0].ID, released.Order.Version, "credit-manager", now); problemCode(t, err) != CodeHoldNotActive {
		t.Fatalf("second release: %v", err)
	}
	if _, err := s.ConfirmOrder(ctx, order.ID, released.Order.Version, "sales-rep", now); problemCode(t, err) != CodeNotConfirmable {
		t.Fatalf("a confirmed order cannot be confirmed again: %v", err)
	}

	// Every state change wrote exactly one event in its transaction.
	types := outboxTypes(t, pool, order.ID)
	want := []string{salesevents.CreditCheckEvaluated, salesevents.OrderConfirmed, salesevents.OrderHoldApplied, salesevents.OrderHoldReleased}
	sort.Strings(types)
	sort.Strings(want)
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("outbox %v want %v", types, want)
	}
	var data salesevents.OrderConfirmedData
	payload := outboxData(t, pool, order.ID, salesevents.OrderConfirmed)
	if err := json.Unmarshal(payload, &data); err != nil {
		t.Fatal(err)
	}
	if data.OrderID != order.ID || data.Version != released.Order.Version || len(data.Lines) != 1 || data.Lines[0].ConfirmedShipDate != day || data.NetTotal != "3000" {
		t.Fatalf("order.confirmed payload %+v", data)
	}
}

// TestPostgres_CreditReleaseIsPerOrder checks that a CREDIT release confirms
// only its own order: the customer's next order over the limit is held again.
func TestPostgres_CreditReleaseIsPerOrder(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	le, customer := ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(1000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(0), ToleranceAmount: d(0), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits}, 0); err != nil {
		t.Fatal(err)
	}
	newOrder := func(hash string) Order {
		line := Line{LineNo: 1, ProductID: ids.New(), Quantity: d(1), Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: d(2000), DiscPct: d(0), LineNet: d(2000), RequestedShipDate: "2027-03-04"}
		o, _, err := s.CreateOrder(ctx, NewOrder{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "USD", RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: hash, Lines: []Line{line}})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	o := newOrder("a")
	held, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", now)
	if err != nil || held.Confirmed {
		t.Fatalf("over the limit must hold: %v", err)
	}
	if rel, err := s.ReleaseHold(principalCtx("credit-manager"), o.ID, held.Order.Holds[0].ID, held.Order.Version, "credit-manager", now); err != nil || !rel.Confirmed {
		t.Fatalf("release must confirm: %v", err)
	}
	if got := countWhere(t, pool, `select count(*) from order_hold where order_id = $1 and status = 'RELEASED'`, o.ID); got != 1 {
		t.Fatalf("released holds %d", got)
	}
	// A different order is not covered by another order's release.
	other := newOrder("b")
	res, err := s.ConfirmOrder(ctx, other.ID, other.Version, "sales-rep", now.Add(time.Hour))
	if err != nil || res.Confirmed {
		t.Fatalf("another order must be held on its own: confirmed=%v err=%v", res.Confirmed, err)
	}
}

// TestPostgres_ListOrdersAndNumbering checks the list filters (the credit
// release queue is hold=CREDIT), keyset paging, and that SO- numbers run per
// legal entity.
func TestPostgres_ListOrdersAndNumbering(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	leA, leB, customer := ids.New(), ids.New(), ids.New()
	create := func(le, hash string, price int64) Order {
		line := Line{LineNo: 1, ProductID: ids.New(), Quantity: d(1), Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: d(price), DiscPct: d(0), LineNet: d(price), RequestedShipDate: "2027-03-04"}
		o, _, err := s.CreateOrder(ctx, NewOrder{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "USD", RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: hash, Lines: []Line{line}})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	a1, b1 := create(leA, "a1", 10), create(leB, "b1", 10)
	if a1.OrderNumber != "SO-0000001" || b1.OrderNumber != "SO-0000001" {
		t.Fatalf("each legal entity numbers from 1: %s %s", a1.OrderNumber, b1.OrderNumber)
	}
	a2 := create(leA, "a2", 5000)
	limits := domain.CreditProfile{Limit: d(100), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(0), ToleranceAmount: d(0), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: leA, CustomerID: customer, CreditProfile: limits}, 0); err != nil {
		t.Fatal(err)
	}
	if res, err := s.ConfirmOrder(ctx, a2.ID, a2.Version, "sales-rep", time.Now()); err != nil || res.Confirmed {
		t.Fatalf("a2 must be held: %v", err)
	}
	if res, err := s.ConfirmOrder(ctx, a1.ID, a1.Version, "sales-rep", time.Now()); err != nil || !res.Confirmed {
		t.Fatalf("a1 must confirm: %v", err)
	}

	queue, err := s.ListOrders(ctx, OrderFilter{HoldType: domain.HoldCredit, Limit: 50})
	if err != nil || len(queue.Items) != 1 || queue.Items[0].ID != a2.ID || strings.Join(queue.Items[0].ActiveHolds, ",") != "CREDIT" {
		t.Fatalf("credit queue %+v err %v", queue.Items, err)
	}
	confirmed, err := s.ListOrders(ctx, OrderFilter{Status: domain.OrderConfirmed, Limit: 50})
	if err != nil || len(confirmed.Items) != 1 || confirmed.Items[0].ID != a1.ID || confirmed.Items[0].LineCount != 1 {
		t.Fatalf("confirmed filter %+v err %v", confirmed.Items, err)
	}
	first, err := s.ListOrders(ctx, OrderFilter{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" || first.Items[0].ID != a2.ID {
		t.Fatalf("first page %+v cursor %q err %v", first.Items, first.NextCursor, err)
	}
	second, err := s.ListOrders(ctx, OrderFilter{Limit: 2, Before: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != a1.ID || second.NextCursor != "" {
		t.Fatalf("second page %+v cursor %q err %v", second.Items, second.NextCursor, err)
	}
	profiles, err := s.ListCreditProfiles(ctx, leA, 50)
	if err != nil || len(profiles) != 1 || profiles[0].CustomerID != customer {
		t.Fatalf("profiles %+v err %v", profiles, err)
	}
	if none, err := s.ListCreditProfiles(ctx, leB, 50); err != nil || len(none) != 0 {
		t.Fatalf("leB profiles %+v err %v", none, err)
	}
}

func countWhere(t *testing.T, pool *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func outboxTypes(t *testing.T, pool *pgxpool.Pool, aggregateID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select type from outbox where aggregate_id = $1 order by created_at, id`, aggregateID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		out = append(out, typ)
	}
	return out
}

func outboxData(t *testing.T, pool *pgxpool.Pool, aggregateID, typ string) []byte {
	t.Helper()
	var payload []byte
	if err := pool.QueryRow(context.Background(), `select payload->'data' from outbox where aggregate_id = $1 and type = $2`, aggregateID, typ).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestPostgres_StaleVersionIsRefused(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	le, customer := ids.New(), ids.New()
	line := Line{LineNo: 1, ProductID: ids.New(), Quantity: d(2), Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: d(5), DiscPct: d(0), LineNet: d(10), RequestedShipDate: "2027-03-04"}
	order, _, err := s.CreateOrder(ctx, NewOrder{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "EUR", RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: "x", Lines: []Line{line}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmOrder(ctx, order.ID, order.Version+5, "sales-rep", time.Now()); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale If-Match must be 412 SLS-0006, got %v", err)
	}
	var p *httpx.Problem
	_, err = s.ConfirmOrder(ctx, order.ID, order.Version+5, "sales-rep", time.Now())
	if errors.As(err, &p) && p.Status != http.StatusPreconditionFailed {
		t.Fatalf("status %d", p.Status)
	}
	confirmed, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", time.Now())
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirm without credit profile: %v", err)
	}
}

// TestPostgres_RowLevelSecurity checks that every table forces RLS and that a
// non-owner role sees only its tenant's rows and, on tables that carry a legal
// entity, only the legal entities in passage.legal_entity_scopes.
func TestPostgres_RowLevelSecurity(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	bg := context.Background()
	rows, err := pool.Query(bg, `select c.relname, c.relrowsecurity, c.relforcerowsecurity,
			exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.policyname like '%tenant_access'),
			exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'legal_entity_id' and not a.attisdropped),
			exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.permissive = 'RESTRICTIVE')
		from pg_class c join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = current_schema() and c.relkind = 'r' and c.relname <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	tables := 0
	for rows.Next() {
		var name string
		var enabled, forced, tenantPolicy, hasLE, restrictive bool
		if err := rows.Scan(&name, &enabled, &forced, &tenantPolicy, &hasLE, &restrictive); err != nil {
			t.Fatal(err)
		}
		tables++
		if !enabled || !forced || !tenantPolicy {
			t.Errorf("%s: rls enabled=%v forced=%v tenant policy=%v", name, enabled, forced, tenantPolicy)
		}
		if hasLE && name != "sales_number_sequence" && !restrictive {
			t.Errorf("%s carries legal_entity_id without a legal-entity scope policy", name)
		}
	}
	rows.Close()
	if tables < 6 {
		t.Fatalf("only %d tables checked", tables)
	}

	s := New(pool)
	leA, leB := ids.New(), ids.New()
	other := tenant.With(bg, tenant.Principal{TenantID: testkit.TenantID(2), UserID: "x", Duties: map[string]struct{}{}})
	mk := func(c context.Context, le, hash string) {
		line := Line{LineNo: 1, ProductID: ids.New(), Quantity: d(1), Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: d(1), DiscPct: d(0), LineNet: d(1), RequestedShipDate: "2027-03-04"}
		c2 := ids.New()
		if _, _, err := s.CreateOrder(c, NewOrder{LegalEntityID: le, CustomerID: c2, InvoiceAccountID: c2, OrderType: "ORDER", Currency: "USD", RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: hash, Lines: []Line{line}}); err != nil {
			t.Fatal(err)
		}
	}
	mk(ctx, leA, "a")
	mk(ctx, leB, "b")
	mk(other, leA, "c")

	if _, err := pool.Exec(bg, `do $$ begin create role sales_rls_probe nologin; exception when duplicate_object then null; end $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `grant usage on schema `+currentSchema(t, pool)+` to sales_rls_probe; grant select on all tables in schema `+currentSchema(t, pool)+` to sales_rls_probe`); err != nil {
		t.Fatal(err)
	}
	visible := func(tenantID, scopes, table string) int {
		tx, err := pool.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		if _, err := tx.Exec(bg, `set local role sales_rls_probe`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(bg, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', $2, true)`, tenantID, scopes); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow(bg, `select count(*) from `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := visible(testkit.TenantID(1), "*", "sales_order"); n != 2 {
		t.Fatalf("tenant 1, all entities: %d orders", n)
	}
	if n := visible(testkit.TenantID(1), leB, "sales_order"); n != 1 {
		t.Fatalf("tenant 1 scoped to one entity: %d orders", n)
	}
	if n := visible(testkit.TenantID(2), "*", "sales_order"); n != 1 {
		t.Fatalf("tenant 2: %d orders", n)
	}
	if n := visible(testkit.TenantID(2), "*", "sales_order_line"); n != 1 {
		t.Fatalf("tenant 2: %d lines", n)
	}
}

func currentSchema(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var schema string
	if err := pool.QueryRow(context.Background(), `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	return schema
}
