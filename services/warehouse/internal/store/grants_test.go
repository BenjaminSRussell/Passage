package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

var adminPools sync.Map

func adminOf(st *Store) *pgxpool.Pool {
	pool, _ := adminPools.Load(st)
	return pool.(*pgxpool.Pool)
}

func appOf(t *testing.T, admin *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(testkit.EnvAppURL)
	if url == "" {
		return admin
	}
	var schema string
	if err := admin.QueryRow(context.Background(), `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	app, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app
}

func TestGrants_AppRoleCannotRewriteAppendOnlyTables(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s is needed for the grants test", testkit.EnvAppURL)
	}
	st, ctx, _ := setup(t)
	for _, stmt := range []string{
		`update dock_appointment set reference = reference`,
		`delete from dock_appointment`,
		`update location_directive set sequence = sequence`,
		`delete from work`,
		`delete from receipt`,
		`select count(*) from outbox`,
		`update outbox set type = type`,
	} {
		err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}

func TestGrants_LockedSkipLockedAndStockPathsWorkAsAppRole(t *testing.T) {
	st, ctx, wh := setup(t)
	mustLocations(t, st, ctx, wh, loc("A-01", "A", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "A"))
	mustDirective(t, st, ctx, directive(wh, "SALES_PICK", domain.DirectionPick, domain.StrategyFIFO, "A"))
	receive(t, st, ctx, wh, "BOLT", "5", "5")
	pick := plan(t, st, ctx, wh, "SALES_PICK", domain.NewLine{ItemID: "BOLT", Direction: domain.DirectionPick, Qty: dq("5"), Weight: dq("5")})
	assign(t, st, ctx, wh, "picker", time.Now())
	if done := confirm(t, st, ctx, pick, "picker"); done.Status != domain.WorkCompleted {
		t.Fatalf("pick status %s", done.Status)
	}
	if q := stockAt(t, st, ctx, wh, "A-01", "BOLT"); !q.IsZero() {
		t.Fatalf("A-01 holds %s BOLT after the full pick, want 0", q)
	}
}
