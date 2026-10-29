package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/store"
	"github.com/BenjaminSRussell/passage/services/platform-health/migrations"
)

const (
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func principal(n int, user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: user})
}

func release(env, action, image, previous string) store.Release {
	return store.Release{Service: "hello-ledger", Environment: env, Action: action, ImageDigest: image, PreviousDigest: previous, Signed: true}
}

func TestReleases_RecordListAndEvents(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	ctx := principal(1, "zach")

	promoted, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestB, digestA), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Actor != "zach" || promoted.Action != "promote" || !promoted.Signed {
		t.Fatalf("unexpected release %+v", promoted)
	}
	replay, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestA, digestB), "key-1")
	if err != nil || replay.ID != promoted.ID || replay.ImageDigest != digestB {
		t.Fatalf("idempotent replay must return the original, got %+v, %v", replay, err)
	}
	rolledBack := release("stage", store.ActionRollback, digestA, digestB)
	rolledBack.Reason = "bad deploy"
	if _, err := s.RecordRelease(principal(1, ""), rolledBack, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordRelease(ctx, release("prod", store.ActionPromote, digestB, digestA), ""); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListReleases(ctx, "", "")
	if err != nil || len(all) != 3 {
		t.Fatalf("list all = %d, %v; want 3 (replay must not add a row)", len(all), err)
	}
	if all[0].Environment != "prod" {
		t.Fatalf("newest first, got %+v", all[0])
	}
	if stage, _ := s.ListReleases(ctx, "hello-ledger", "stage"); len(stage) != 2 {
		t.Fatalf("stage filter = %d, want 2", len(stage))
	}
	if other, _ := s.ListReleases(ctx, "other-service", ""); len(other) != 0 {
		t.Fatalf("service filter = %d, want 0", len(other))
	}
	if otherTenant, err := s.ListReleases(principal(2, "x"), "", ""); err != nil || len(otherTenant) != 3 {
		t.Fatalf("releases are platform-level and visible to any caller with the duty, got %d, %v", len(otherTenant), err)
	}

	rows, err := admin.Query(context.Background(), `select type, tenant_id::text, aggregate_id, payload->>'actor', payload->'data'->>'previous_digest' from outbox order by created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var typ, tenantID, aggregateID, actor, previous string
		if err := rows.Scan(&typ, &tenantID, &aggregateID, &actor, &previous); err != nil {
			t.Fatal(err)
		}
		if tenantID != ids.Nil || previous == "" {
			t.Fatalf("outbox row tenant %s previous %q", tenantID, previous)
		}
		types = append(types, typ)
	}
	want := "dev.passage.platform.release.promoted.v1,dev.passage.platform.release.rolledback.v1,dev.passage.platform.release.promoted.v1"
	if strings.Join(types, ",") != want {
		t.Fatalf("outbox types = %v, want one event per recorded release and none for the replay", types)
	}
}

func TestReleases_UnknownEnvironmentWritesNothing(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	_, err := s.RecordRelease(principal(1, "zach"), release("nowhere", store.ActionPromote, digestB, digestA), "")
	if !errors.Is(err, store.ErrUnknownEnvironment) {
		t.Fatalf("err = %v, want ErrUnknownEnvironment", err)
	}
	var events int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox`).Scan(&events); err != nil || events != 0 {
		t.Fatalf("outbox rows = %d, %v; want 0", events, err)
	}
}

func TestReleases_AppRoleIsAppendOnlyAndScoped(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	if _, err := s.RecordRelease(principal(1, "zach"), release("stage", store.ActionPromote, digestB, digestA), ""); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`update platform_release set actor = 'x'`,
		`delete from platform_release`,
		`update outbox set type = 'x'`,
		`delete from outbox`,
		`insert into platform_environment (tenant_id, name, argo_root_app, promotion_policy, soak_minutes, base_url) values ('` + ids.Nil + `', 'x', 'x', 'auto', 0, 'x')`,
	} {
		tx, err := app.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(context.Background(), `select set_config('passage.tenant_id', $1, true)`, ids.Nil); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(context.Background(), sql); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%q as app role: %v, want permission denied", sql, err)
		}
		_ = tx.Rollback(context.Background())
	}
	tx, err := app.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `select set_config('passage.tenant_id', $1, true)`, testkit.TenantID(1)); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := tx.QueryRow(context.Background(), `select count(*) from platform_release`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("a customer tenant scope sees %d platform rows, %v; want 0", visible, err)
	}
}
