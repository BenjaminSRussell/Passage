package store

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

func firmedIn(warehouse *string) PlannedIntakeIn {
	return PlannedIntakeIn{
		PlannedOrderID: ids.New(), PlanVersionID: ids.New(), BatchID: ids.New(), TargetID: ids.New(), ProductID: ids.New(), SiteID: ids.New(),
		WarehouseID: warehouse, Quantity: d("40"), OrderDate: "2026-11-02", DeliveryDate: "2026-11-20", EventID: "evt-" + ids.New(),
	}
}

func record(t *testing.T, pool *pgxpool.Pool, s *Postgres, ctx context.Context, in PlannedIntakeIn) bool {
	t.Helper()
	var added bool
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) (e error) {
		added, e = s.RecordPlannedOrder(ctx, tx, in)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return added
}

// FR-22.10: a firmed planned order is stored once (a second firming event of it adds nothing), listed
// by status, and converting it creates the order under the target id with the planned order's header,
// warehouse and origin, and marks the row converted in the same transaction.
func TestIntake_ConvertCreatesTheTargetOrderAndClosesTheRow(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(95))
	warehouse := ids.New()
	in := firmedIn(&warehouse)
	if !record(t, pool, s, ctx, in) {
		t.Fatal("first firming not stored")
	}
	again := in
	again.EventID = "evt-other"
	again.Quantity = d("99")
	if record(t, pool, s, ctx, again) {
		t.Fatal("a second event of the same planned order added a row")
	}
	page, err := s.ListPlannedIntake(ctx, IntakeFilter{Status: IntakeOpen})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("open rows: %+v %v", page.Items, err)
	}
	row := page.Items[0]
	if !row.Quantity.Equal(d("40")) || row.WarehouseID == nil || *row.WarehouseID != warehouse || row.TargetID != in.TargetID || row.Version != 1 {
		t.Fatalf("row: %+v", row)
	}

	order := sampleOrder(t, "intake-conv", ids.New())
	order.ID, order.PlannedIntakeID = row.TargetID, row.ID
	order.ProductID, order.SiteID, order.WarehouseID, order.Quantity = row.ProductID, row.SiteID, warehouse, row.Quantity
	order.OriginType, order.OriginID = "PlannedOrder", &row.PlannedOrderID
	arrangement, source := ids.New(), ids.New()
	order.QuotaArrangementID, order.QuotaSourceID = &arrangement, &source
	created, isNew, err := s.CreateOrder(ctx, order)
	if err != nil || !isNew {
		t.Fatalf("convert: %v new=%v", err, isNew)
	}
	if created.QuotaArrangementID == nil || *created.QuotaArrangementID != arrangement || created.QuotaSourceID == nil || *created.QuotaSourceID != source {
		t.Fatalf("quota not kept on the order: %+v", created)
	}
	if created.ID != in.TargetID || created.WarehouseID != warehouse || created.OriginType != "PlannedOrder" || created.OriginID == nil || *created.OriginID != in.PlannedOrderID {
		t.Fatalf("order: %+v", created)
	}
	done, err := s.GetPlannedIntake(ctx, row.ID)
	if err != nil || done.Status != IntakeConverted || done.OrderID == nil || *done.OrderID != created.ID || done.Version != 2 {
		t.Fatalf("row after convert: %+v %v", done, err)
	}
	if open, _ := s.ListPlannedIntake(ctx, IntakeFilter{Status: IntakeOpen}); len(open.Items) != 0 {
		t.Fatalf("converted row still open: %+v", open.Items)
	}

	// The same key replays the order; another key on a converted row is refused and creates nothing.
	if replay, isNew, err := s.CreateOrder(ctx, order); err != nil || isNew || replay.ID != created.ID {
		t.Fatalf("replay: %+v new=%v %v", replay, isNew, err)
	}
	second := order
	second.IdempotencyKey, second.RequestHash = "intake-conv-2", "hash-2"
	_, _, err = s.CreateOrder(ctx, second)
	expectProblem(t, err, http.StatusConflict, CodeIntakeNotOpen)
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from production_order where tenant_id = $1`, testkit.TenantID(95)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("orders: %d %v", n, err)
	}
	var created1 int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where aggregate_id = $1 and type = $2`, created.ID, prodevents.OrderCreated).Scan(&created1); err != nil || created1 != 1 {
		t.Fatalf("order.created events: %d %v", created1, err)
	}
}

func TestIntake_DismissNeedsTheVersionAndAnOpenRow(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(96))
	record(t, pool, s, ctx, firmedIn(nil))
	page, _ := s.ListPlannedIntake(ctx, IntakeFilter{})
	row := page.Items[0]
	if row.WarehouseID != nil {
		t.Fatalf("site scope row has a warehouse: %v", *row.WarehouseID)
	}
	_, err := s.DismissPlannedIntake(ctx, IntakeDismiss{ID: row.ID, IfMatch: 7, Reason: "x", Actor: "planner-1"})
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)
	got, err := s.DismissPlannedIntake(ctx, IntakeDismiss{ID: row.ID, IfMatch: 1, Reason: "Demand withdrawn", Actor: "planner-1"})
	if err != nil || got.Status != IntakeDismissed || got.Version != 2 || got.DismissReason == nil || *got.DismissReason != "Demand withdrawn" {
		t.Fatalf("dismiss: %+v %v", got, err)
	}
	_, err = s.DismissPlannedIntake(ctx, IntakeDismiss{ID: row.ID, IfMatch: 2, Reason: "again", Actor: "planner-1"})
	expectProblem(t, err, http.StatusConflict, CodeIntakeNotOpen)

	// A dismissed row cannot be converted, and the failed create leaves no order.
	order := sampleOrder(t, "intake-dismissed", ids.New())
	order.ID, order.PlannedIntakeID = row.TargetID, row.ID
	_, _, err = s.CreateOrder(ctx, order)
	expectProblem(t, err, http.StatusConflict, CodeIntakeNotOpen)
	if page, err := s.ListOrders(ctx, ListFilter{}); err != nil || len(page.Items) != 0 {
		t.Fatalf("orders after a refused conversion: %+v %v", page.Items, err)
	}
	_, err = s.GetPlannedIntake(ctx, ids.New())
	expectProblem(t, err, http.StatusNotFound, CodeIntakeNotFound)
}

func TestIntake_ListPagesNewestFirst(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(97))
	site := ids.New()
	for i := 0; i < 3; i++ {
		in := firmedIn(nil)
		if i < 2 {
			in.SiteID = site
		}
		record(t, pool, s, ctx, in)
	}
	first, err := s.ListPlannedIntake(ctx, IntakeFilter{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	next, err := s.ListPlannedIntake(ctx, IntakeFilter{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(next.Items) != 1 || next.NextCursor != "" {
		t.Fatalf("second page: %+v %v", next, err)
	}
	if bySite, err := s.ListPlannedIntake(ctx, IntakeFilter{SiteID: site}); err != nil || len(bySite.Items) != 2 {
		t.Fatalf("site filter: %+v %v", bySite.Items, err)
	}
}

// The legal entity a firmed event carries is stored on the intake row (and none stays none).
func TestIntake_StoresTheLegalEntityOfTheFirmedEvent(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(93))
	le := ids.New()
	with, without := firmedIn(nil), firmedIn(nil)
	with.LegalEntityID = &le
	if !record(t, pool, s, ctx, with) || !record(t, pool, s, ctx, without) {
		t.Fatal("rows not stored")
	}
	page, err := s.ListPlannedIntake(ctx, IntakeFilter{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("rows: %+v %v", page.Items, err)
	}
	for _, row := range page.Items {
		switch row.PlannedOrderID {
		case with.PlannedOrderID:
			if row.LegalEntityID == nil || *row.LegalEntityID != le {
				t.Fatalf("legal entity not stored: %+v", row)
			}
		case without.PlannedOrderID:
			if row.LegalEntityID != nil {
				t.Fatalf("unexpected legal entity: %+v", row)
			}
		}
	}
}

// Under the NOSUPERUSER NOBYPASSRLS application role the intake table is granted (select, insert,
// update) and scoped by tenant.
func TestIntake_RowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	ctxA, ctxB := tenantCtx(testkit.TenantID(98)), tenantCtx(testkit.TenantID(99))
	record(t, app, s, ctxA, firmedIn(nil))
	page, err := s.ListPlannedIntake(ctxA, IntakeFilter{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("own tenant: %+v %v", page.Items, err)
	}
	if foreign, err := s.ListPlannedIntake(ctxB, IntakeFilter{}); err != nil || len(foreign.Items) != 0 {
		t.Fatalf("another tenant sees %d rows (%v)", len(foreign.Items), err)
	}
	_, err = s.GetPlannedIntake(ctxB, page.Items[0].ID)
	expectProblem(t, err, http.StatusNotFound, CodeIntakeNotFound)
	if _, err := s.DismissPlannedIntake(ctxA, IntakeDismiss{ID: page.Items[0].ID, IfMatch: 1, Reason: "rls", Actor: "x"}); err != nil {
		t.Fatalf("dismiss as the application role: %v", err)
	}

	// Legal-entity scope: the consumer's principal (no legal entity, no all-scope duty) stores a row of
	// entity 1 and one planning sent without an entity; a caller of entity 1 sees and converts only its
	// row, a caller of entity 2 sees none, the row without an entity is visible to all-entity callers only.
	tid := testkit.TenantID(94)
	le1, le2 := ids.New(), ids.New()
	consumer := tenant.With(context.Background(), tenant.Principal{TenantID: tid, UserID: "service:production-planning",
		Duties: map[string]struct{}{"production.planned.intake": {}}})
	scoped := firmedIn(nil)
	scoped.LegalEntityID = &le1
	unscoped := firmedIn(nil)
	if !record(t, app, s, consumer, scoped) || !record(t, app, s, consumer, unscoped) {
		t.Fatal("the consumer did not store its rows")
	}
	if record(t, app, s, consumer, scoped) {
		t.Fatal("a redelivery of a scoped planned order added a row")
	}
	in1, err := s.ListPlannedIntake(scopedCtx(tid, le1), IntakeFilter{})
	if err != nil || len(in1.Items) != 1 || in1.Items[0].LegalEntityID == nil || *in1.Items[0].LegalEntityID != le1 {
		t.Fatalf("entity 1 sees %+v (%v)", in1.Items, err)
	}
	if in2, err := s.ListPlannedIntake(scopedCtx(tid, le2), IntakeFilter{}); err != nil || len(in2.Items) != 0 {
		t.Fatalf("entity 2 sees %d rows (%v)", len(in2.Items), err)
	}
	_, err = s.GetPlannedIntake(scopedCtx(tid, le2), in1.Items[0].ID)
	expectProblem(t, err, http.StatusNotFound, CodeIntakeNotFound)
	if all, err := s.ListPlannedIntake(tenantCtx(tid), IntakeFilter{}); err != nil || len(all.Items) != 2 {
		t.Fatalf("all entities see %d rows (%v)", len(all.Items), err)
	}
	row := in1.Items[0]
	foreign := sampleOrder(t, "intake-le2", le2)
	foreign.ID, foreign.PlannedIntakeID = row.TargetID, row.ID
	_, _, err = s.CreateOrder(scopedCtx(tid, le2), foreign)
	expectProblem(t, err, http.StatusNotFound, CodeIntakeNotFound)
	own := sampleOrder(t, "intake-le1", le1)
	own.ID, own.PlannedIntakeID = row.TargetID, row.ID
	if order, _, err := s.CreateOrder(scopedCtx(tid, le1), own); err != nil || order.ID != row.TargetID {
		t.Fatalf("convert in the planned order's entity: %+v %v", order, err)
	}
}

// Slice 26: an order made for the planned order outside the intake (a plain create before slice 26)
// takes the planned order, so converting its open row is the intake's 409, not a 500, and leaves the
// row open with no second order.
func TestIntake_ConvertOfAPlannedOrderThatAlreadyHasAnOrder(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(93))
	le := ids.New()
	in := firmedIn(nil)
	record(t, pool, s, ctx, in)
	page, _ := s.ListPlannedIntake(ctx, IntakeFilter{})
	row := page.Items[0]

	legacy := sampleOrder(t, "legacy-planned", le)
	legacy.OriginType, legacy.OriginID = "PlannedOrder", &row.PlannedOrderID
	if _, _, err := s.CreateOrder(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	convert := sampleOrder(t, "intake-taken", le)
	convert.ID, convert.PlannedIntakeID = row.TargetID, row.ID
	convert.OriginType, convert.OriginID = "PlannedOrder", &row.PlannedOrderID
	_, _, err := s.CreateOrder(ctx, convert)
	expectProblem(t, err, http.StatusConflict, CodeIntakeNotOpen)

	if got, err := s.GetPlannedIntake(ctx, row.ID); err != nil || got.Status != IntakeOpen || got.Version != 1 {
		t.Fatalf("row after the refused conversion: %+v %v", got, err)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from production_order where tenant_id = $1 and origin_id = $2`,
		testkit.TenantID(93), row.PlannedOrderID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("orders of the planned order: %d %v", n, err)
	}
}

// Migration 0026 refuses a database that already holds two orders of one planned order (made by the
// plain create before slice 26) with a message naming them, and builds the index once they are resolved.
func TestMigration0026_RefusesExistingDuplicatePlannedOrigins(t *testing.T) {
	pool := testkit.DB(t)
	upTo0025 := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= "0026" {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		upTo0025[e.Name()] = &fstest.MapFile{Data: body}
	}
	if err := dbx.Migrate(context.Background(), pool, upTo0025); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	ctx := tenantCtx(testkit.TenantID(92))
	planned := ids.New()
	var second string
	for _, key := range []string{"dup-1", "dup-2"} {
		o := sampleOrder(t, key, ids.New())
		o.OriginType, o.OriginID = "PlannedOrder", &planned
		created, _, err := s.CreateOrder(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		second = created.ID
	}

	err = dbx.Migrate(context.Background(), pool, migrations.FS)
	if err == nil || !strings.Contains(err.Error(), "1 planned order(s) have more than one production order") || !strings.Contains(err.Error(), planned) {
		t.Fatalf("migration over duplicates: %v", err)
	}
	var applied bool
	if err := pool.QueryRow(context.Background(), `select exists(select 1 from schema_migrations where name = '0026_order_planned_origin.sql')`).Scan(&applied); err != nil || applied {
		t.Fatalf("0026 recorded as applied: %v %v", applied, err)
	}

	if _, err := pool.Exec(context.Background(), `update production_order set origin_type = 'Manual' where id = $1`, second); err != nil {
		t.Fatal(err)
	}
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatalf("migration after the duplicate was resolved: %v", err)
	}
	dup := sampleOrder(t, "dup-3", ids.New())
	dup.OriginType, dup.OriginID = "PlannedOrder", &planned
	if _, _, err := s.CreateOrder(ctx, dup); err == nil {
		t.Fatal("a second order of the planned order was stored after 0026")
	}
}
