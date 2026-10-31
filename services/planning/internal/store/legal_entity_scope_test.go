package store_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func scopedCtx(tenantID, scope string) context.Context {
	duties := map[string]struct{}{}
	if scope == "" {
		duties["org.switcher.all_scope"] = struct{}{}
	}
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: scope, UserID: "u", Duties: duties})
}

func countAs(t *testing.T, app *pgxpool.Pool, ctx context.Context, query string, args ...any) (n int) {
	t.Helper()
	if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func requireRLSViolation(t *testing.T, err error, msg string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Errorf("%s: %v, want insufficient_privilege 42501", msg, err)
	}
}

func requireStatus(t *testing.T, err error, status int, msg string) {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != status {
		t.Errorf("%s: %v, want status %d", msg, err, status)
	}
}

func TestLegalEntityScope_PlansForecastsAndQuotas(t *testing.T) {
	st, admin, app := setup(t)
	testkit.AssertTenantRLS(t, admin)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB)
	all, otherTenant := scopedCtx(testkit.TenantID(1), ""), scopedCtx(testkit.TenantID(2), "")
	site, item := ids.New(), ids.New()

	version, _, err := st.RunPlan(ctxA, runRequest(oneItemPlan(t), "k", "in"))
	if err != nil {
		t.Fatalf("entity A plan run: %v", err)
	}
	if _, err := st.UpsertForecastLines(ctxA, "BASE", "", 0, nil, []store.ForecastCellInput{cellIn(item, site, "2026-11-02", "2026-11-08", "5", "0")}); err != nil {
		t.Fatalf("entity A forecast: %v", err)
	}
	if _, _, err := st.CreateQuotaArrangement(ctxA, store.QuotaCreate{Spec: br2212(item, site, day(2026, 11, 1), day(2026, 12, 31))}); err != nil {
		t.Fatalf("entity A quota: %v", err)
	}
	for _, stmt := range []string{
		`insert into action_message (tenant_id, id, plan_version_id, item_id, action, qty) select tenant_id, gen_random_uuid(), id, gen_random_uuid(), 'delay', 1 from plan_version`,
		`insert into net_requirement (tenant_id, id, plan_version_id, item_id, day, gross_requirements, scheduled_receipts, planned_receipts, pab, net_requirement)
			select tenant_id, gen_random_uuid(), id, gen_random_uuid(), '2026-11-02', 0, 0, 0, 0, 0 from plan_version`,
	} {
		if _, err := admin.Exec(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}

	tables := []string{"plan", "plan_version", "planned_order", "pegging", "action_message", "net_requirement",
		"forecast_model", "forecast_line", "quota_arrangement", "quota_source"}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			q := "select count(*) from " + table
			own := countAs(t, app, ctxA, q)
			if own == 0 {
				t.Fatal("entity A cannot read its own rows")
			}
			if n := countAs(t, app, all, q); n != own {
				t.Errorf("all-entity caller sees %d rows, want %d", n, own)
			}
			if n := countAs(t, app, ctxB, q); n != 0 {
				t.Errorf("entity B reads %d of entity A's rows", n)
			}
			if n := countAs(t, app, otherTenant, q); n != 0 {
				t.Errorf("another tenant reads %d rows", n)
			}
		})
	}

	t.Run("a legacy row without an entity is invisible to scoped callers", func(t *testing.T) {
		if _, err := admin.Exec(context.Background(), `insert into plan (tenant_id, id, code) values ($1, $2, 'LEGACY')`, testkit.TenantID(1), ids.New()); err != nil {
			t.Fatal(err)
		}
		if n := countAs(t, app, ctxA, `select count(*) from plan where code = 'LEGACY'`); n != 0 {
			t.Errorf("entity A reads %d unassigned plans", n)
		}
		if n := countAs(t, app, all, `select count(*) from plan where code = 'LEGACY'`); n != 1 {
			t.Errorf("all-entity caller sees %d unassigned plans, want 1", n)
		}
	})

	t.Run("line inserts follow the parent's entity", func(t *testing.T) {
		inserts := map[string]string{
			"action_message":  `insert into action_message (tenant_id, id, plan_version_id, item_id, action, qty) values ($1, gen_random_uuid(), $2, gen_random_uuid(), 'delay', 1)`,
			"net_requirement": `insert into net_requirement (tenant_id, id, plan_version_id, item_id, day, gross_requirements, scheduled_receipts, planned_receipts, pab, net_requirement) values ($1, gen_random_uuid(), $2, gen_random_uuid(), '2026-11-03', 0, 0, 0, 0, 0)`,
		}
		for table, insert := range inserts {
			err := dbx.WithTenantTx(ctxB, app, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctxB, insert, testkit.TenantID(1), versionID(t, admin))
				return err
			})
			requireRLSViolation(t, err, table+": entity B insert under entity A's version")
			err = dbx.WithTenantTx(ctxA, app, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctxA, insert, testkit.TenantID(1), versionID(t, admin))
				return err
			})
			if err != nil {
				t.Errorf("%s: entity A insert under its own version: %v", table, err)
			}
		}
	})

	t.Run("another entity cannot take over a code or write into the model", func(t *testing.T) {
		_, _, err := st.RunPlan(ctxB, runRequest(oneItemPlan(t), "kb", "in"))
		requireStatus(t, err, http.StatusConflict, "entity B run of entity A's plan code")
		_, err = st.UpsertForecastLines(ctxB, "BASE", "", 1, nil, []store.ForecastCellInput{cellIn(item, site, "2026-11-09", "2026-11-15", "5", "0")})
		requireStatus(t, err, http.StatusConflict, "entity B write to entity A's forecast model")
		req := runRequest(oneItemPlan(t), "kc", "in")
		req.PlanCode, req.LegalEntityID = "OTHER", entityB
		_, _, err = st.RunPlan(ctxA, req)
		requireStatus(t, err, http.StatusForbidden, "entity A run owned by entity B")
	})

	t.Run("each entity owns its own plan and the version stays with it", func(t *testing.T) {
		req := runRequest(oneItemPlan(t), "kb2", "in")
		req.PlanCode = "B-PLAN"
		v, _, err := st.RunPlan(ctxB, req)
		if err != nil {
			t.Fatalf("entity B plan run: %v", err)
		}
		if _, err := st.GetVersion(ctxA, "B-PLAN", v.Number); err == nil {
			t.Error("entity A read entity B's version")
		}
		if _, err := st.GetVersion(all, "B-PLAN", v.Number); err != nil {
			t.Errorf("all-entity caller cannot read entity B's version: %v", err)
		}
		if _, err := st.GetVersion(ctxA, "MAIN", version.Number); err != nil {
			t.Errorf("entity A cannot read its own version: %v", err)
		}
	})
}

func versionID(t *testing.T, admin *pgxpool.Pool) (id string) {
	t.Helper()
	if err := admin.QueryRow(context.Background(), `select id::text from plan_version limit 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLegalEntityScope_OwnersAreDerivedOrNamed(t *testing.T) {
	st, admin, app := setup(t)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB)
	ctxAB := scopedCtx(testkit.TenantID(1), entityA+","+entityB)
	all := scopedCtx(testkit.TenantID(1), "")
	site, item := ids.New(), ids.New()
	cell := []store.ForecastCellInput{cellIn(item, site, "2026-11-02", "2026-11-08", "5", "0")}

	if _, err := st.UpsertForecastLines(ctxA, "A-MODEL", "", 0, nil, cell); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertForecastLines(ctxB, "B-MODEL", "", 0, nil, cell); err != nil {
		t.Fatal(err)
	}
	planA := runRequest(oneItemPlan(t), "ka", "in")
	planA.PlanCode = "A-PLAN"
	if _, _, err := st.RunPlan(ctxA, planA); err != nil {
		t.Fatal(err)
	}

	t.Run("a plan cannot net another entity's forecast", func(t *testing.T) {
		req := runRequest(oneItemPlan(t), "k-mismatch", "in")
		req.PlanCode, req.ForecastModel = "A-PLAN", "B-MODEL"
		_, _, err := st.RunPlan(ctxAB, req)
		var p *httpx.Problem
		if !errors.As(err, &p) || p.Code != store.CodeForecastEntity {
			t.Fatalf("run with a foreign model = %v, want %s", err, store.CodeForecastEntity)
		}
		req.IdempotencyKey, req.ForecastModel = "k-match", "A-MODEL"
		if _, _, err := st.RunPlan(ctxAB, req); err != nil {
			t.Fatalf("run with the plan's own model: %v", err)
		}
	})

	t.Run("a requested entity that is not the plan owner is refused", func(t *testing.T) {
		req := runRequest(oneItemPlan(t), "k-assert", "in")
		req.PlanCode, req.LegalEntityID = "A-PLAN", entityB
		_, _, err := st.RunPlan(ctxAB, req)
		requireStatus(t, err, http.StatusUnprocessableEntity, "run asserting the wrong owner")
	})

	t.Run("an all-entity caller inherits an existing owner and names a new one", func(t *testing.T) {
		req := runRequest(oneItemPlan(t), "k-all", "in")
		req.PlanCode = "A-PLAN"
		if _, _, err := st.RunPlan(all, req); err != nil {
			t.Fatalf("all-entity run of an existing plan: %v", err)
		}
		fresh := runRequest(oneItemPlan(t), "k-new", "in")
		fresh.PlanCode = "NEW-PLAN"
		_, _, err := st.RunPlan(all, fresh)
		requireStatus(t, err, http.StatusUnprocessableEntity, "all-entity run creating a plan without an entity")
		fresh.LegalEntityID = entityB
		if _, _, err := st.RunPlan(all, fresh); err != nil {
			t.Fatalf("all-entity run naming the owner: %v", err)
		}
		if n := countAs(t, app, ctxB, `select count(*) from plan where code = 'NEW-PLAN'`); n != 1 {
			t.Errorf("entity B sees %d of the plans created for it, want 1", n)
		}
		_, err = st.UpsertForecastLines(all, "ALL-MODEL", "", 0, nil, cell)
		requireStatus(t, err, http.StatusUnprocessableEntity, "all-entity forecast model without an entity")
		if _, err := st.UpsertForecastLines(all, "ALL-MODEL", entityA, 0, nil, cell); err != nil {
			t.Fatalf("all-entity forecast model naming the owner: %v", err)
		}
		_, _, err = st.CreateQuotaArrangement(all, store.QuotaCreate{Spec: br2212(item, site, day(2026, 11, 1), day(2026, 12, 31))})
		requireStatus(t, err, http.StatusUnprocessableEntity, "all-entity quota without an entity")
		if _, _, err := st.CreateQuotaArrangement(all, store.QuotaCreate{LegalEntityID: entityA, Spec: br2212(item, site, day(2026, 11, 1), day(2026, 12, 31))}); err != nil {
			t.Fatalf("all-entity quota naming the owner: %v", err)
		}
	})

	t.Run("a code held by an unassigned plan says so", func(t *testing.T) {
		if _, err := admin.Exec(context.Background(), `insert into plan (tenant_id, id, code) values ($1, $2, 'UNASSIGNED')`, testkit.TenantID(1), ids.New()); err != nil {
			t.Fatal(err)
		}
		req := runRequest(oneItemPlan(t), "k-legacy", "in")
		req.PlanCode = "UNASSIGNED"
		_, _, err := st.RunPlan(ctxA, req)
		var p *httpx.Problem
		if !errors.As(err, &p) || p.Code != store.CodeCodeTaken || !strings.Contains(p.Detail, "no legal entity") {
			t.Fatalf("run of an unassigned plan code = %v", err)
		}
	})
}
