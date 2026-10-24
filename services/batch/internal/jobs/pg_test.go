package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/batch/internal/jobs"
	"github.com/BenjaminSRussell/passage/services/batch/migrations"
)

func TestPgStore_ResumesAndScopesRowsByTenant(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	store := jobs.NewPgStore(pool)
	runner := jobs.Runner{Store: store, Partitions: 4, MaxAttempts: 1}
	tenantA, tenantB := testkit.TenantID(1), testkit.TenantID(2)
	jobID := fmt.Sprintf("shared-%d", time.Now().UnixNano())
	killed := false
	podKilled := errors.New("pod killed")
	process := func(_ context.Context, tx pgx.Tx, row int64) error {
		if tx == nil {
			t.Fatal("Postgres store must provide the transaction to the effect")
		}
		if row == 37 && !killed {
			killed = true
			return podKilled
		}
		return nil
	}

	if _, err := runner.Run(ctx, jobs.Job{ID: jobID, TenantID: tenantA, Rows: 100}, process); !errors.Is(err, podKilled) {
		t.Fatalf("first run error = %v, want pod kill", err)
	}
	if result, err := runner.Run(ctx, jobs.Job{ID: jobID, TenantID: tenantA, Rows: 100}, process); err != nil || result.Processed != 63 {
		t.Fatalf("resume = %+v, %v; want 63 rows", result, err)
	}
	if result, err := runner.Run(ctx, jobs.Job{ID: jobID, TenantID: tenantB, Rows: 2}, process); err != nil || result.Processed != 2 {
		t.Fatalf("second tenant = %+v, %v", result, err)
	}

	if count := processedRows(t, pool, tenantA, jobID); count != 100 {
		t.Fatalf("tenant A processed rows = %d, want 100", count)
	}
	if count := processedRows(t, pool, tenantB, jobID); count != 2 {
		t.Fatalf("tenant B processed rows = %d, want 2", count)
	}
}

func processedRows(t *testing.T, pool *pgxpool.Pool, tenantID, jobID string) int {
	t.Helper()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
	var count int
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			select count(*)
			from batch_processed_rows
			where tenant_id = $1 and job_id = $2`,
			tenantID, jobID,
		).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	return count
}
