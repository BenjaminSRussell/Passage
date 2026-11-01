package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	gitSHA  = "0123456789abcdef0123456789abcdef01234567"
)

func principal(n int, user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: user})
}

func release(env, action, image, previous string) store.Release {
	return store.Release{Service: "hello-ledger", Environment: env, Action: action, ImageDigest: image, PreviousDigest: previous, GitSHA: gitSHA, Signed: true}
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
	replay, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestB, digestA), "key-1")
	if err != nil || replay.ID != promoted.ID {
		t.Fatalf("idempotent replay must return the original, got %+v, %v", replay, err)
	}
	unsignedReplay := release("stage", store.ActionPromote, digestB, digestA)
	unsignedReplay.Signed = false
	if replay, err := s.RecordRelease(ctx, unsignedReplay, "key-1"); err != nil || replay.ID != promoted.ID || !replay.Signed {
		t.Fatalf("a re-run whose signature check flaked must replay the original, got %+v, %v", replay, err)
	}
	if _, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestA, digestB), "key-1"); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("replay with a different payload: err = %v, want ErrIdempotencyConflict", err)
	}
	recordedByOther, err := s.RecordRelease(principal(2, "x"), release("stage", store.ActionPromote, digestA, digestB), "key-1")
	if err != nil || recordedByOther.ID == promoted.ID || recordedByOther.ImageDigest != digestA {
		t.Fatalf("idempotency keys are scoped per caller tenant, got %+v, %v", recordedByOther, err)
	}
	rolledBack := release("stage", store.ActionRollback, digestA, digestB)
	rolledBack.Reason = "bad deploy"
	if _, err := s.RecordRelease(principal(1, ""), rolledBack, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordRelease(ctx, release("prod", store.ActionPromote, digestB, digestA), ""); err != nil {
		t.Fatal(err)
	}

	all, _, err := s.ListReleases(ctx, "", "", "")
	if err != nil || len(all) != 4 {
		t.Fatalf("list all = %d, %v; want 4 (replay must not add a row)", len(all), err)
	}
	if all[0].Environment != "prod" {
		t.Fatalf("newest first, got %+v", all[0])
	}
	if stage, _, _ := s.ListReleases(ctx, "hello-ledger", "stage", ""); len(stage) != 3 {
		t.Fatalf("stage filter = %d, want 3", len(stage))
	}
	if other, _, _ := s.ListReleases(ctx, "other-service", "", ""); len(other) != 0 {
		t.Fatalf("service filter = %d, want 0", len(other))
	}
	if otherTenant, _, err := s.ListReleases(principal(2, "x"), "", "", ""); err != nil || len(otherTenant) != 4 {
		t.Fatalf("releases are platform-level and visible to any caller with the duty, got %d, %v", len(otherTenant), err)
	}

	var audited int
	if err := admin.QueryRow(context.Background(), `select count(*) from platform_release where recorded_by_tenant = $1::uuid and recorded_by_user = 'zach' and git_sha = $2`, testkit.TenantID(1), gitSHA).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("rows recorded by tenant 1 / zach = %d, %v; want 2", audited, err)
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
	want := "dev.passage.platform.release.promoted.v1,dev.passage.platform.release.promoted.v1,dev.passage.platform.release.rolledback.v1,dev.passage.platform.release.promoted.v1"
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
		`update platform_release set recorded_by_user = 'x'`,
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

func TestReleases_RollbackRequiresPriorPromotionOfSameTarget(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	ctx := principal(1, "zach")
	for _, env := range []string{"stage", "prod"} {
		if _, err := s.RecordRelease(ctx, release(env, store.ActionRollback, digestA, digestB), ""); !errors.Is(err, store.ErrRollbackTargetUnseen) {
			t.Fatalf("%s rollback to an unseen digest: err = %v, want ErrRollbackTargetUnseen", env, err)
		}
	}
	if _, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestA, digestB), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordRelease(ctx, release("prod", store.ActionRollback, digestA, digestB), ""); !errors.Is(err, store.ErrRollbackTargetUnseen) {
		t.Fatalf("prod rollback to a digest promoted only to stage: err = %v, want ErrRollbackTargetUnseen", err)
	}
	if _, err := s.RecordRelease(ctx, release("stage", store.ActionRollback, digestA, digestB), ""); err != nil {
		t.Fatalf("stage rollback to a promoted digest: %v", err)
	}
	if _, err := s.RecordRelease(ctx, release("prod", store.ActionPromote, digestA, digestB), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordRelease(ctx, release("prod", store.ActionRollback, digestA, digestB), ""); err != nil {
		t.Fatalf("prod rollback to a promoted digest: %v", err)
	}
	var rows, events int
	if err := admin.QueryRow(context.Background(), `select count(*) from platform_release`).Scan(&rows); err != nil || rows != 4 {
		t.Fatalf("release rows = %d, %v; want 4 (rejected rollbacks write nothing)", rows, err)
	}
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox`).Scan(&events); err != nil || events != 4 {
		t.Fatalf("outbox rows = %d, %v; want 4", events, err)
	}
}

func TestReleases_RollbackToDigestRunningBeforeTheRegistryIsAccepted(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	ctx := principal(1, "zach")
	digestC := "sha256:" + strings.Repeat("c", 64)
	if _, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestB, digestA), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordRelease(ctx, release("prod", store.ActionRollback, digestA, digestB), ""); !errors.Is(err, store.ErrRollbackTargetUnseen) {
		t.Fatalf("prod never ran digest A in the registry: err = %v, want ErrRollbackTargetUnseen", err)
	}
	if _, err := s.RecordRelease(ctx, release("stage", store.ActionRollback, digestC, digestB), ""); !errors.Is(err, store.ErrRollbackTargetUnseen) {
		t.Fatalf("rollback to a never-seen digest: err = %v, want ErrRollbackTargetUnseen", err)
	}
	if _, err := s.RecordRelease(ctx, release("stage", store.ActionRollback, digestA, digestB), ""); err != nil {
		t.Fatalf("go-live A->B->A rollback to the pre-registry digest: %v", err)
	}
}

func TestReleases_ReusedKeyConflictsBeforeTheRollbackTargetCheck(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	ctx := principal(1, "zach")
	if _, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestB, digestA), "key-1"); err != nil {
		t.Fatal(err)
	}
	unseen := "sha256:" + strings.Repeat("c", 64)
	if _, err := s.RecordRelease(ctx, release("stage", store.ActionRollback, unseen, digestB), "key-1"); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("reused key on an unseen rollback: err = %v, want ErrIdempotencyConflict", err)
	}
	if _, err := s.RecordRelease(ctx, release("stage", store.ActionRollback, digestA, digestB), "key-1"); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("reused key on a valid rollback: err = %v, want ErrIdempotencyConflict", err)
	}
}

func TestReleases_RollbackToUnregisteredEnvironmentIsUnknownEnvironment(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	if _, err := s.RecordRelease(principal(1, "zach"), release("nowhere", store.ActionRollback, digestA, digestB), ""); !errors.Is(err, store.ErrUnknownEnvironment) {
		t.Fatalf("err = %v, want ErrUnknownEnvironment", err)
	}
}

func TestReleases_PreviousDigestMustContinueTheRecordedChain(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	ctx := principal(1, "zach")
	digestC := "sha256:" + strings.Repeat("c", 64)
	first, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestB, digestA), "")
	if err != nil || first.ChainBreak {
		t.Fatalf("the first row has nothing to chain to, got %+v, %v", first, err)
	}
	linked, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestC, digestB), "")
	if err != nil || linked.ChainBreak {
		t.Fatalf("previousDigest equal to the newest image is a continuous chain, got %+v, %v", linked, err)
	}
	broken, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestA, digestB), "")
	if err != nil || !broken.ChainBreak {
		t.Fatalf("previousDigest %s differs from the newest image %s: want ChainBreak, got %+v, %v", digestB, digestC, broken, err)
	}
	otherEnv, err := s.RecordRelease(ctx, release("prod", store.ActionPromote, digestB, digestA), "")
	if err != nil || otherEnv.ChainBreak {
		t.Fatalf("chains are per environment, got %+v, %v", otherEnv, err)
	}
	replay, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestA, digestB), "")
	if err != nil || !replay.ChainBreak {
		t.Fatalf("unkeyed repeat is a new row and a new break, got %+v, %v", replay, err)
	}
}

func TestReleases_ChainBreakEventCarriesTheExpectedDigest(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	ctx := principal(1, "zach")
	digestC := "sha256:" + strings.Repeat("c", 64)
	if _, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestB, digestA), ""); err != nil {
		t.Fatal(err)
	}
	broken, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestC, digestA), "")
	if err != nil || !broken.ChainBreak || broken.ExpectedPreviousDigest != digestB {
		t.Fatalf("want a break expecting %s, got %+v, %v", digestB, broken, err)
	}
	var flagged bool
	var expected string
	if err := admin.QueryRow(context.Background(), `select (payload->'data'->>'chain_break')::boolean, payload->'data'->>'expected_previous_digest' from outbox where aggregate_id = $1`, broken.ID).Scan(&flagged, &expected); err != nil || !flagged || expected != digestB {
		t.Fatalf("event chain_break = %v expected = %q, %v", flagged, expected, err)
	}
}

func TestReleases_ConcurrentRecordsChainThroughTheLock(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	ctx := principal(1, "zach")
	if _, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, digestB, digestA), ""); err != nil {
		t.Fatal(err)
	}
	const writers = 8
	var wg sync.WaitGroup
	results := make([]store.Release, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := fmt.Sprintf("sha256:%064x", i+1)
			r, err := s.RecordRelease(ctx, release("stage", store.ActionPromote, next, digestB), "")
			if err != nil {
				t.Error(err)
			}
			results[i] = r
		}()
	}
	wg.Wait()
	breaks := 0
	for _, r := range results {
		if r.ChainBreak {
			breaks++
		}
	}
	if breaks != writers-1 {
		t.Fatalf("all writers claim %s as previous: exactly one chains cleanly, got %d breaks of %d", digestB, breaks, writers)
	}
}

func TestReleases_ListPaginatesWithAKeysetCursor(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	ctx := principal(1, "zach")
	const total = 103
	for i := range total {
		in := release("stage", store.ActionPromote, fmt.Sprintf("sha256:%064x", i+1), fmt.Sprintf("sha256:%064x", i))
		if _, err := s.RecordRelease(ctx, in, ""); err != nil {
			t.Fatal(err)
		}
	}
	first, next, err := s.ListReleases(ctx, "", "", "")
	if err != nil || len(first) != 100 || next != first[99].ID {
		t.Fatalf("first page = %d, next = %q, %v", len(first), next, err)
	}
	second, next, err := s.ListReleases(ctx, "", "", next)
	if err != nil || len(second) != total-100 || next != "" {
		t.Fatalf("second page = %d, next = %q, %v", len(second), next, err)
	}
	if second[0].ID == first[99].ID {
		t.Fatalf("pages overlap")
	}
	if _, _, err := s.ListReleases(ctx, "", "", ids.New()); !errors.Is(err, store.ErrUnknownCursor) {
		t.Fatalf("unknown cursor: err = %v, want ErrUnknownCursor", err)
	}
}
