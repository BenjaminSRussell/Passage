package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	analyticsevents "github.com/BenjaminSRussell/passage/services/analytics/internal/events"
	"github.com/BenjaminSRussell/passage/services/analytics/migrations"
)

const (
	leUSMF = "11111111-0000-4000-8000-000000000001"
	leDEMF = "22222222-0000-4000-8000-000000000002"
)

// ctxFor is a principal for tenant n. An empty legal entity is the whole tenant
// (the scheduled reconciliation job); a set one narrows RLS to that entity.
func ctxFor(n int, legalEntity string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: legalEntity,
		UserID: "user-test", Duties: testkit.ScopeDuties(legalEntity)})
}

func migrated(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return pool
}

func recon(le, metric, date, semantic, source string) domain.Reconciliation {
	return domain.Reconciliation{MetricID: metric, LegalEntityID: le, Date: date, Currency: "USD",
		SemanticValue: decimal.RequireFromString(semantic), SourceValue: decimal.RequireFromString(source),
		Tolerance: domain.DefaultTolerance()}
}

func outboxCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecordReconciliation_EventOnlyOnChangeAndTrustCounter(t *testing.T) {
	pool := migrated(t)
	st := New(pool)
	ctx := ctxFor(1, "")

	first, changed, err := st.RecordReconciliation(ctx, recon(leUSMF, "revenue", "2026-10-01", "1250.00", "1250.00"))
	if err != nil || !changed {
		t.Fatalf("first record: changed %v err %v", changed, err)
	}
	if first.Status != domain.ReconOK || !first.Delta.IsZero() || first.ConsecutiveOKDays != 1 || first.Trusted {
		t.Fatalf("first = %+v", first)
	}
	if n := outboxCount(t, pool); n != 1 {
		t.Fatalf("outbox rows %d, want 1", n)
	}

	// Re-running the same day with the same values is a no-op: no second event.
	again, changed, err := st.RecordReconciliation(ctx, recon(leUSMF, "revenue", "2026-10-01", "1250.00", "1250.00"))
	if err != nil || changed || again.ConsecutiveOKDays != 1 {
		t.Fatalf("replay: changed %v err %v row %+v", changed, err, again)
	}
	if n := outboxCount(t, pool); n != 1 {
		t.Fatalf("replay wrote an event: outbox rows %d, want 1", n)
	}

	second, _, err := st.RecordReconciliation(ctx, recon(leUSMF, "revenue", "2026-10-02", "10.00", "10.004"))
	if err != nil || second.Status != domain.ReconOK || second.ConsecutiveOKDays != 2 {
		t.Fatalf("day 2 inside tolerance: %+v %v", second, err)
	}
	// The caller cannot claim OK: the store derives FAILED from the delta.
	in := recon(leUSMF, "revenue", "2026-10-03", "10.00", "10.01")
	in.Status = domain.ReconOK
	third, _, err := st.RecordReconciliation(ctx, in)
	if err != nil || third.Status != domain.ReconFailed || !third.Delta.Equal(decimal.RequireFromString("-0.01")) || third.ConsecutiveOKDays != 0 {
		t.Fatalf("day 3 over tolerance: %+v %v", third, err)
	}

	var raw []byte
	if err := pool.QueryRow(ctx, `select payload from outbox order by created_at desc, id desc limit 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Type     string                               `json:"type"`
		TenantID string                               `json:"tenantid"`
		Data     analyticsevents.MetricReconciledData `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Type != analyticsevents.MetricReconciled || evt.TenantID != testkit.TenantID(1) || evt.Data.TenantID != testkit.TenantID(1) ||
		evt.Data.Status != domain.ReconFailed || evt.Data.Delta != "-0.01" || evt.Data.Currency != "USD" || evt.Data.Date != "2026-10-03" {
		t.Fatalf("event = %+v", evt)
	}
}

func TestRecordReconciliation_ConcurrentReplicasWriteOneEvent(t *testing.T) {
	pool := migrated(t)
	st := New(pool)
	ctx := ctxFor(1, "")
	const replicas = 8
	changes := make(chan bool, replicas)
	errs := make(chan error, replicas)
	for i := 0; i < replicas; i++ {
		go func() {
			_, changed, err := st.RecordReconciliation(ctx, recon(leUSMF, "revenue", "2026-10-05", "42.00", "42.00"))
			changes <- changed
			errs <- err
		}()
	}
	changed := 0
	for i := 0; i < replicas; i++ {
		if <-changes {
			changed++
		}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if changed != 1 {
		t.Fatalf("%d replicas reported a change, want exactly 1", changed)
	}
	if n := outboxCount(t, pool); n != 1 {
		t.Fatalf("outbox rows %d, want 1", n)
	}
}

func TestRecordReconciliation_TrustedAfterThirtyDaysAndValidation(t *testing.T) {
	pool := migrated(t)
	st := New(pool)
	ctx := ctxFor(1, "")
	var last domain.Reconciliation
	for day := 1; day <= domain.TrustThresholdDays; day++ {
		r, _, err := st.RecordReconciliation(ctx, recon(leUSMF, "cogs", fmt.Sprintf("2026-09-%02d", day), "5", "5"))
		if err != nil {
			t.Fatal(err)
		}
		last = r
	}
	if !last.Trusted || last.ConsecutiveOKDays != domain.TrustThresholdDays {
		t.Fatalf("after 30 OK days: %+v", last)
	}
	if _, _, err := st.RecordReconciliation(ctx, recon("USMF", "cogs", "2026-10-01", "5", "5")); err == nil {
		t.Fatal("a non-UUID legal entity must be rejected before the database")
	}
}

func TestListReconciliations_FiltersByDayAndMetric(t *testing.T) {
	pool := migrated(t)
	st := New(pool)
	ctx := ctxFor(1, "")
	for _, r := range []domain.Reconciliation{
		recon(leUSMF, "revenue", "2026-10-01", "1", "1"),
		recon(leDEMF, "revenue", "2026-10-01", "1", "2"),
		recon(leUSMF, "cogs", "2026-10-01", "1", "1"),
		recon(leUSMF, "revenue", "2026-10-02", "1", "1"),
	} {
		if _, _, err := st.RecordReconciliation(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	all, err := st.ListReconciliations(ctx, "2026-10-01", "")
	if err != nil || len(all) != 3 {
		t.Fatalf("day list = %+v, %v", all, err)
	}
	if all[0].MetricID != "cogs" || all[1].LegalEntityID != leUSMF || all[2].LegalEntityID != leDEMF {
		t.Fatalf("order must be metric then legal entity: %+v", all)
	}
	if !all[2].Delta.Equal(decimal.NewFromInt(-1)) || all[2].Status != domain.ReconFailed {
		t.Fatalf("DEMF row = %+v", all[2])
	}
	only, err := st.ListReconciliations(ctx, "2026-10-01", "revenue")
	if err != nil || len(only) != 2 {
		t.Fatalf("revenue only = %+v, %v", only, err)
	}
	none, err := st.ListReconciliations(ctx, "2026-11-01", "")
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("empty day must be an empty list, got %#v, %v", none, err)
	}
}

// Row-level security through a non-owner application role: another tenant sees
// nothing, a legal-entity scoped caller sees only its entity (AC3) and cannot
// write another entity's result.
func TestTenantAndLegalEntityIsolation(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s not set; skipping RLS test", testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	bg := context.Background()
	if err := dbx.Migrate(bg, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app)
	for _, le := range []string{leUSMF, leDEMF} {
		if _, _, err := st.RecordReconciliation(ctxFor(1, ""), recon(le, "revenue", "2026-10-01", "1", "1")); err != nil {
			t.Fatal(err)
		}
	}
	if rows, err := st.ListReconciliations(ctxFor(2, ""), "2026-10-01", ""); err != nil || len(rows) != 0 {
		t.Fatalf("tenant 2 sees %+v, %v", rows, err)
	}
	rows, err := st.ListReconciliations(ctxFor(1, leDEMF), "2026-10-01", "")
	if err != nil || len(rows) != 1 || rows[0].LegalEntityID != leDEMF {
		t.Fatalf("DEMF-scoped caller sees %+v, %v", rows, err)
	}
	if _, _, err := st.RecordReconciliation(ctxFor(1, leDEMF), recon(leUSMF, "revenue", "2026-10-02", "1", "1")); err == nil {
		t.Fatal("a DEMF-scoped caller must not write a USMF result")
	}
	var outboxRows int
	if err := admin.QueryRow(bg, `select count(*) from outbox where tenant_id = $1`, testkit.TenantID(2)).Scan(&outboxRows); err != nil || outboxRows != 0 {
		t.Fatalf("tenant 2 owns %d outbox rows, %v", outboxRows, err)
	}
}
