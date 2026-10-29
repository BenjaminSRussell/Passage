package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/migrations"
)

func validatedRun(t *testing.T, tenantID string, records []domain.Record) OpenItemsRun {
	t.Helper()
	run := domain.PackRun{RunID: ids.New(), TenantID: tenantID, Pack: domain.PackOpenItems, SourceSystem: domain.SourceSAPECC,
		SourceCompany: "1000", CutoffTS: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), Status: domain.StatusExtracted, RecordsIn: len(records)}
	if err := run.Advance(domain.StatusValidated); err != nil {
		t.Fatal(err)
	}
	return OpenItemsRun{Run: run, Records: records, Accepted: records}
}

// Two runs of one extract racing each other must load every record exactly once (FR-49.10).
func TestRunOpenItems_ConcurrentRunsLoadEachRecordOnce(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	tenantID := testkit.TenantID(7)
	ctx := testkit.Ctx(tenantID, "MigrationRun")
	var records []domain.Record
	for i := 0; i < 50; i++ {
		records = append(records, domain.Record{LegacyID: fmt.Sprintf("BSIK-%03d", i), Party: "V1", Account: "160000", Currency: "EUR", Amount: decimal.RequireFromString("99.99")})
	}
	var wg sync.WaitGroup
	results := make([]domain.PackRun, 4)
	errs := make([]error, 4)
	inputs := make([]OpenItemsRun, len(results))
	for i := range inputs {
		inputs[i] = validatedRun(t, tenantID, records)
	}
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _, errs[i] = st.RunOpenItems(ctx, Idem{}, inputs[i])
		}(i)
	}
	wg.Wait()
	loaded := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		if r.Status != domain.StatusReconciled {
			t.Fatalf("run %d status %s: every run sees all 50 records in legacy_xref", i, r.Status)
		}
		loaded += r.RecordsLoaded
	}
	if loaded != 50 {
		t.Fatalf("records loaded across runs = %d, want 50", loaded)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from legacy_xref where tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Fatalf("legacy_xref rows = %d", n)
	}
}

func TestGetRun_RoundTripsTotalsAndQuarantine(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	tenantID := testkit.TenantID(8)
	ctx := testkit.Ctx(tenantID)
	good := domain.Record{LegacyID: "A", Party: "V1", Account: "160000", Currency: "EUR", Amount: decimal.RequireFromString("12.345678")}
	bad := domain.Record{LegacyID: "B", Party: "V9", Account: "160000", Currency: "EUR", Amount: decimal.RequireFromString("1")}
	in := validatedRun(t, tenantID, []domain.Record{good, bad})
	in.Accepted = []domain.Record{good}
	in.Run.RecordsQuarantined = 1
	in.Run.Quarantined = []domain.Quarantined{{Record: bad, Rule: domain.RuleReferential, Reason: "party V9 is not in master data"}}
	saved, _, err := st.RunOpenItems(ctx, Idem{}, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRun(ctx, saved.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusLoaded || len(got.Totals) != len(saved.Totals) || len(got.Quarantined) != 1 {
		t.Fatalf("stored run %+v", got)
	}
	for i := range got.Totals {
		if !got.Totals[i].Source.Equal(saved.Totals[i].Source) || !got.Totals[i].Target.Equal(saved.Totals[i].Target) || got.Totals[i].Status != saved.Totals[i].Status {
			t.Fatalf("total %d: stored %+v, computed %+v", i, got.Totals[i], saved.Totals[i])
		}
	}
	if q := got.Quarantined[0]; q.Record.LegacyID != "B" || q.Rule != domain.RuleReferential || !q.Record.Amount.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("quarantine %+v", q)
	}
	if _, err := st.GetRun(testkit.Ctx(testkit.TenantID(9)), saved.RunID); err != ErrNotFound {
		t.Fatalf("other tenant: %v, want ErrNotFound", err)
	}
}

// Concurrent releases of one quarantined record load it once: the run's row lock serializes
// them, the first loads and reconciles, the others replay (idempotent by record).
func TestReleaseQuarantined_ConcurrentReleasesLoadOnce(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	tenantID := testkit.TenantID(10)
	ctx := testkit.Ctx(tenantID)
	good := domain.Record{LegacyID: "A", Party: "V1", Account: "160000", Currency: "EUR", Amount: decimal.RequireFromString("10")}
	bad := domain.Record{LegacyID: "B", Party: "V9", Account: "160000", Currency: "EUR", Amount: decimal.RequireFromString("2.5")}
	in := validatedRun(t, tenantID, []domain.Record{good, bad})
	in.Accepted = []domain.Record{good}
	in.Run.RecordsQuarantined = 1
	in.Run.Parties = []string{"V1"}
	in.Run.Quarantined = []domain.Quarantined{{Record: bad, Rule: domain.RuleReferential, Reason: "party V9 is not in master data"}}
	saved, _, err := st.RunOpenItems(ctx, Idem{}, in)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	replays := make([]bool, 4)
	errs := make([]error, 4)
	for i := range replays {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, replays[i], errs[i] = st.ReleaseQuarantined(ctx, Idem{}, Release{RunID: saved.RunID, Seq: 1, Fix: domain.Correction{Party: "V1"}, By: "ops"})
		}(i)
	}
	wg.Wait()
	fresh := 0
	for i := range replays {
		if errs[i] != nil {
			t.Fatalf("release %d: %v", i, errs[i])
		}
		if !replays[i] {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("releases that loaded = %d, want 1", fresh)
	}
	got, err := st.GetRun(ctx, saved.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusReconciled || got.RecordsLoaded != 2 || got.RecordsQuarantined != 0 || got.Version != 2 || got.HasMismatch() {
		t.Fatalf("run after releases %+v", got)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from legacy_xref where tenant_id = $1 and source_id = 'B'`, tenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("legacy_xref rows for B = %d", n)
	}
}
