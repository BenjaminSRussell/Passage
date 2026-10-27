package store

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/migrations"
)

func setup(t *testing.T) (*Postgres, *pgxpool.Pool, context.Context) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(pool), pool, testkit.Ctx(testkit.TenantID(1), "QM_ORDER_MAINTAIN")
}

func problemStatus(err error) int {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Status
	}
	return 0
}

// TestPostgres_OrderLifecycleWritesOutbox runs one order from creation to a
// usage decision and checks each step wrote its event in the same transaction.
func TestPostgres_OrderLifecycleWritesOutbox(t *testing.T) {
	s, pool, ctx := setup(t)
	outcome := ids.New()
	group, inserted, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "GRP-1", Tests: []domain.TestLine{
		{TestID: ids.New(), Name: "appearance", Kind: domain.KindAttribute, Required: true, PassingOutcomes: []string{outcome}},
	}}, "g-1")
	if err != nil || !inserted || group.Version != 1 {
		t.Fatalf("create group: %+v %v", group, err)
	}
	if _, again, _ := s.CreateTestGroup(ctx, domain.TestGroup{Code: "GRP-1"}, "g-1"); again {
		t.Fatal("a replayed key must not insert")
	}
	order := domain.Order{ID: ids.New(), Trigger: domain.TriggerManual, SourceRef: domain.SourceRef{Type: "po_line", ID: ids.New()},
		ItemID: ids.New(), SiteID: ids.New(), WarehouseID: ids.New(), Dims: map[string]string{}, LotQty: decimal.NewFromInt(51),
		SampleQty: decimal.NewFromInt(20), UOM: "EA", TestGroupID: group.ID, TestGroupVersion: 1, SamplingMethod: domain.MethodAQL,
		Severity: domain.SeverityNormal, CodeLetter: "E", AcceptNo: 1, RejectNo: 2}
	created, _, err := s.CreateOrder(ctx, order, "o-1")
	if err != nil || created.Status != domain.StatusCreated || created.Version != 1 {
		t.Fatalf("create order: %+v %v", created, err)
	}
	batch := []domain.Result{{TestID: group.Tests[0].TestID, SampleNo: 1, OutcomeID: outcome, Passed: true, RecordedBy: "u1"}}
	recorded, applied, err := s.RecordResults(ctx, created.ID, 1, domain.StatusPending, batch, "r-1")
	if err != nil || !applied || recorded.Version != 2 {
		t.Fatalf("record results: %+v %v", recorded, err)
	}
	if _, _, err := s.RecordResults(ctx, created.ID, 1, domain.StatusPending, batch, ""); problemStatus(err) != http.StatusPreconditionFailed {
		t.Fatalf("stale version must be 412, got %v", err)
	}
	if replayed, err := s.HasResultBatch(ctx, created.ID, "r-1"); err != nil || !replayed {
		t.Fatalf("batch r-1 is recorded on the order: %v %v", replayed, err)
	}
	if replayed, _ := s.HasResultBatch(ctx, ids.New(), "r-1"); replayed {
		t.Fatal("a batch key belongs to its own order only")
	}
	if again, applied, err := s.RecordResults(ctx, created.ID, 2, domain.StatusPending, batch, "r-1"); err != nil || applied || again.Version != 2 {
		t.Fatalf("replayed batch must not write: %+v %v %v", again, applied, err)
	}
	dupe := []domain.Result{{TestID: group.Tests[0].TestID, SampleNo: 1, OutcomeID: outcome, Passed: true, RecordedBy: "u1"}}
	if _, _, err := s.RecordResults(ctx, created.ID, 2, domain.StatusPending, dupe, "r-2"); problemStatus(err) != http.StatusConflict {
		t.Fatalf("same test, sample and retest twice must be 409 QM-0111, got %v", err)
	}
	pending, err := s.ListOrders(ctx, OrderFilter{Status: domain.StatusPending, Limit: 10})
	if err != nil || len(pending) != 1 || pending[0].ID != created.ID {
		t.Fatalf("pending worklist: %+v %v", pending, err)
	}
	if none, _ := s.ListOrders(ctx, OrderFilter{SiteID: ids.New(), Limit: 10}); len(none) != 0 {
		t.Fatalf("another site's worklist: %+v", none)
	}
	lines := []domain.DecisionLine{{Decision: domain.DecisionAccept, Quantity: decimal.NewFromInt(51)}}
	split, err := domain.SplitDecision(lines, order.LotQty)
	if err != nil {
		t.Fatal(err)
	}
	closed, decision, err := s.PostUsageDecision(ctx, created.ID, 2, domain.Decision{Lines: lines, Split: split, DecidedBy: "u1"}, "d-1")
	if err != nil || closed.Status != domain.StatusClosed || decision.ID == "" {
		t.Fatalf("decision: %+v %v", closed, err)
	}
	if _, _, err := s.PostUsageDecision(ctx, created.ID, 3, domain.Decision{Lines: lines, Split: split}, ""); problemStatus(err) != http.StatusConflict {
		t.Fatalf("second decision must be refused, got %v", err)
	}
	stored, found, err := s.GetDecision(ctx, created.ID)
	if err != nil || !found || stored.ID != decision.ID || !stored.Split.Accept.Equal(decimal.NewFromInt(51)) {
		t.Fatalf("stored decision: %+v %v %v", stored, found, err)
	}
	if byKey, found, err := s.FindDecisionByKey(ctx, "d-1"); err != nil || !found || byKey.OrderID != created.ID {
		t.Fatalf("decision by key: %+v %v %v", byKey, found, err)
	}

	rows, err := pool.Query(ctx, `select type from outbox order by created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		types = append(types, typ)
	}
	want := []string{"dev.passage.quality.order.created.v1", "dev.passage.quality.result.recorded.v1", "dev.passage.quality.usagedecision.posted.v1"}
	if len(types) != len(want) {
		t.Fatalf("outbox types %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("outbox[%d] = %s, want %s", i, types[i], want[i])
		}
	}
}

// TestPostgres_InstrumentAndAssociationRoundTrip covers the setup tables.
func TestPostgres_InstrumentAndAssociationRoundTrip(t *testing.T) {
	s, _, ctx := setup(t)
	group, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "GRP-2", Tests: []domain.TestLine{
		{TestID: ids.New(), Kind: domain.KindAttribute, PassingOutcomes: []string{ids.New()}},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	assoc, _, err := s.CreateAssociation(ctx, domain.Association{Trigger: domain.TriggerPurchaseReceipt, ItemScope: domain.ScopeAll,
		PartyScope: domain.ScopeAll, SiteScope: domain.ScopeAll, Sequence: 1, TestGroupID: group.ID,
		Plan: domain.SamplingPlan{Method: domain.MethodPercent, Percent: decimal.NewFromInt(10)}.Normalized()}, "a-1")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := domain.NewInstrument("PH-01", "pH meter", 30, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	created, inserted, err := s.CreateInstrument(ctx, inst, "i-1")
	if err != nil || !inserted || !created.DueAt.Equal(inst.DueAt) {
		t.Fatalf("create instrument: %+v %v", created, err)
	}
	if _, _, err := s.CreateInstrument(ctx, inst, "i-2"); problemStatus(err) != http.StatusConflict {
		t.Fatalf("duplicate instrument code: %v", err)
	}
	found, err := s.GetInstruments(ctx, []string{created.ID, ids.New()})
	if err != nil || len(found) != 1 || found[created.ID].Code != "PH-01" {
		t.Fatalf("get instruments: %+v %v", found, err)
	}
	if all, err := s.ListInstruments(ctx, 10); err != nil || len(all) != 1 {
		t.Fatalf("list instruments: %+v %v", all, err)
	}
	recal := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	moved, err := s.CalibrateInstrument(ctx, created.ID, func(cur domain.Instrument) (domain.Instrument, error) {
		return cur.Recalibrate(recal, recal)
	})
	if err != nil || !moved.LastCalibratedAt.Equal(recal) || !moved.DueAt.Equal(recal.AddDate(0, 0, 30)) {
		t.Fatalf("calibrate: %+v %v", moved, err)
	}
	if _, err := s.CalibrateInstrument(ctx, ids.New(), func(cur domain.Instrument) (domain.Instrument, error) { return cur, nil }); problemStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown instrument: %v", err)
	}
	if _, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "GRP-2", Tests: group.Tests}, ""); err != nil {
		t.Fatal(err)
	}
	groups, err := s.ListTestGroups(ctx, 10)
	if err != nil || len(groups) != 2 || groups[0].Version != 2 || groups[1].Version != 1 {
		t.Fatalf("test group versions, newest first: %+v %v", groups, err)
	}
	list, err := s.ListAssociations(ctx)
	if err != nil || len(list) != 1 || list[0].ID != assoc.ID || !list[0].Plan.Percent.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("list associations: %+v %v", list, err)
	}
	if _, _, err := s.CreateAssociation(ctx, domain.Association{Trigger: domain.TriggerSalesPick, ItemScope: domain.ScopeAll,
		PartyScope: domain.ScopeAll, SiteScope: domain.ScopeAll, TestGroupID: ids.New(), Plan: domain.SamplingPlan{Method: domain.MethodFixed, FixedQty: decimal.NewFromInt(1)}.Normalized()}, ""); problemStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown test group: %v", err)
	}
}

// TestPostgres_RowLevelSecurity checks that every table forces RLS with a tenant
// policy, and that a non-owner role sees only its own tenant's rows.
func TestPostgres_RowLevelSecurity(t *testing.T) {
	s, pool, ctx := setup(t)
	bg := context.Background()
	rows, err := pool.Query(bg, `select c.relname, c.relrowsecurity, c.relforcerowsecurity,
			exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.policyname like '%tenant_access'),
			exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'tenant_id' and not a.attisdropped)
		from pg_class c join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = current_schema() and c.relkind = 'r' and c.relname <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	tables := 0
	for rows.Next() {
		var name string
		var enabled, forced, policy, hasTenant bool
		if err := rows.Scan(&name, &enabled, &forced, &policy, &hasTenant); err != nil {
			t.Fatal(err)
		}
		tables++
		if !enabled || !forced || !policy || !hasTenant {
			t.Errorf("%s: rls enabled=%v forced=%v tenant policy=%v tenant_id=%v", name, enabled, forced, policy, hasTenant)
		}
	}
	rows.Close()
	if tables < 7 {
		t.Fatalf("only %d tables checked", tables)
	}

	other := testkit.Ctx(testkit.TenantID(2), "QM_ORDER_MAINTAIN")
	for i, c := range []context.Context{ctx, ctx, other} {
		if _, _, err := s.CreateTestGroup(c, domain.TestGroup{Code: "GRP-" + string(rune('A'+i)), Tests: []domain.TestLine{
			{TestID: ids.New(), Kind: domain.KindAttribute, PassingOutcomes: []string{ids.New()}},
		}}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if theirs, err := s.ListTestGroups(other, 10); err != nil || len(theirs) != 1 {
		t.Fatalf("tenant 2 lists its own group only: %d %v", len(theirs), err)
	}
	schema := ""
	if err := pool.QueryRow(bg, `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `do $$ begin create role quality_rls_probe nologin; exception when duplicate_object then null; end $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `grant usage on schema `+schema+` to quality_rls_probe; grant select on all tables in schema `+schema+` to quality_rls_probe`); err != nil {
		t.Fatal(err)
	}
	visible := func(tenantID string) int {
		tx, err := pool.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		if _, err := tx.Exec(bg, `set local role quality_rls_probe`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(bg, `select set_config('passage.tenant_id', $1, true)`, tenantID); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow(bg, `select count(*) from quality_test_group`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := visible(testkit.TenantID(1)); n != 2 {
		t.Fatalf("tenant 1 sees %d groups, want 2", n)
	}
	if n := visible(testkit.TenantID(2)); n != 1 {
		t.Fatalf("tenant 2 sees %d groups, want 1", n)
	}
}

// TestPostgres_OneOrderPerSourceLine is BR-33.3: a trigger event creates one
// order per source line; another line, or a manual order, creates its own.
func TestPostgres_OneOrderPerSourceLine(t *testing.T) {
	s, _, ctx := setup(t)
	group, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "GRP-3", Tests: []domain.TestLine{
		{TestID: ids.New(), Kind: domain.KindAttribute, PassingOutcomes: []string{ids.New()}},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	receipt := ids.New()
	mk := func(trigger, line string) error {
		_, _, err := s.CreateOrder(ctx, domain.Order{ID: ids.New(), Trigger: trigger, SourceRef: domain.SourceRef{Type: "receipt", ID: receipt, Line: line},
			ItemID: ids.New(), SiteID: ids.New(), WarehouseID: ids.New(), Dims: map[string]string{}, LotQty: decimal.NewFromInt(10),
			SampleQty: decimal.NewFromInt(1), UOM: "EA", TestGroupID: group.ID, TestGroupVersion: 1, SamplingMethod: domain.MethodFixed,
			Severity: domain.SeverityNormal, RejectNo: 1}, "")
		return err
	}
	if err := mk(domain.TriggerPurchaseReceipt, "1"); err != nil {
		t.Fatal(err)
	}
	if err := mk(domain.TriggerPurchaseReceipt, "1"); problemStatus(err) != http.StatusConflict {
		t.Fatalf("same line and trigger twice must be 409 QM-0116, got %v", err)
	}
	if err := mk(domain.TriggerPurchaseReceipt, "2"); err != nil {
		t.Fatalf("another receipt line: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := mk(domain.TriggerManual, "1"); err != nil {
			t.Fatalf("manual orders are not limited per source line: %v", err)
		}
	}
}
