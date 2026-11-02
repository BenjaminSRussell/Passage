package store_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/testdb"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

const everyQuarter = "*/15 * * * *"

// syncRig is a connection profile with an active customer binding and a scheduled migrate project over it.
type syncRig struct {
	profile store.ConnectionProfile
	binding store.EntityBinding
	project store.Project
}

func newSyncRig(t *testing.T, s store.Store, ctx context.Context, cron string) syncRig {
	t.Helper()
	profile, _, err := s.SaveConnectionProfile(ctx, sqlProfile("Acme ERP"), "")
	if err != nil {
		t.Fatal(err)
	}
	binding, _, err := s.SaveEntityBinding(ctx, custBinding(profile.ID, "customer"), "")
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := s.SaveProject(ctx, store.Project{Name: "Acme sync", Type: domain.ProjectMigrate, SourceSystem: "custom_acme", Status: domain.ProjectActive,
		ConnectionProfileID: profile.ID, ScheduleCron: cron}, "")
	if err != nil {
		t.Fatal(err)
	}
	return syncRig{profile, binding, project}
}

// seed creates a binding's state through the store's own schedule lookup; a changed schedule moves it to next.
func seed(t *testing.T, s store.Store, ctx context.Context, bindingID string, next time.Time) {
	t.Helper()
	if err := s.SeedSync(ctx, bindingID, func(string) (time.Time, bool) { return next, true }); err != nil {
		t.Fatal(err)
	}
}

func finishOK(r syncRig, claim, wm string, conflicts ...domain.Conflict) store.SyncFinish {
	return store.SyncFinish{BindingID: r.binding.ID, ClaimID: claim, RunID: ids.New(), Entity: "customer", Status: domain.SyncOK, Advance: true, Watermark: wm, BindingVersion: r.binding.Version,
		Extracted: 3, Loaded: 2, Rejected: len(conflicts), NextCron: time.Now().Add(15 * time.Minute), Conflicts: conflicts}
}

// exerciseSync runs against Postgres (under the app role) and Mem so the fake keeps matching the contract.
func exerciseSync(t *testing.T, s store.Store, ctx context.Context, tenantID string, age func(bindingID string)) {
	r := newSyncRig(t, s, ctx, everyQuarter)

	cands, err := s.SyncCandidates(context.Background(), 10)
	if err != nil || len(cands) != 1 || cands[0].BindingID != r.binding.ID || cands[0].TenantID != tenantID {
		t.Fatalf("candidates = %+v %v", cands, err)
	}
	// Nothing is claimable before the state exists, and a never-scheduled view shows version 0.
	if _, ok, err := s.ClaimSync(ctx, r.binding.ID, ids.New()); err != nil || ok {
		t.Fatalf("claim without state: %v %v", ok, err)
	}
	if v, err := s.GetSync(ctx, r.binding.ID); err != nil || v.Version != 0 || v.ScheduleCron != everyQuarter || v.Watermark != "" || v.SourceOfTruth != "legacy" {
		t.Fatalf("unseeded view = %+v %v", v, err)
	}
	seed(t, s, ctx, r.binding.ID, time.Now().Add(time.Hour))
	if v, err := s.GetSync(ctx, r.binding.ID); err != nil || v.ProjectID != r.project.ID || v.WatermarkBindingVersion != nil {
		t.Fatalf("seeded view = %+v %v", v, err)
	}
	// The first run is due at once (it is the full read); two claims race and exactly one wins.
	claimA, claimB := ids.New(), ids.New()
	_, okA, errA := s.ClaimSync(ctx, r.binding.ID, claimA)
	_, okB, errB := s.ClaimSync(ctx, r.binding.ID, claimB)
	if errA != nil || errB != nil || !okA || okB {
		t.Fatalf("claims: A=%v,%v B=%v,%v", okA, errA, okB, errB)
	}
	if v, _ := s.GetSync(ctx, r.binding.ID); !v.Running {
		t.Fatal("a claimed binding shows as running")
	}
	conflict := domain.Conflict{SourceKey: "C1", TargetID: ids.New(), ContentHash: "h1", Fields: map[string]any{"name": "Legacy name"}}
	if err := s.FinishSync(ctx, finishOK(r, claimB, "x")); !store.IsSyncClaimLost(err) {
		t.Fatalf("a finish with a claim it does not hold must be refused: %v", err)
	}
	if err := s.FinishSync(ctx, finishOK(r, claimA, "2026-11-01T10:00:00.000000Z", conflict)); err != nil {
		t.Fatal(err)
	}
	v, _ := s.GetSync(ctx, r.binding.ID)
	if v.Watermark != "2026-11-01T10:00:00.000000Z" || v.WatermarkBindingVersion == nil || *v.WatermarkBindingVersion != r.binding.Version || v.Running || v.LastStatus != "succeeded" || v.LastSuccessAt == nil || v.RowsLoaded != 2 || v.RowsRejected != 1 || v.OpenConflicts != 1 || v.ConsecutiveFailures != 0 || v.NextDueAt == nil || !v.NextDueAt.After(time.Now()) {
		t.Fatalf("after success = %+v", v)
	}
	if _, ok, _ := s.ClaimSync(ctx, r.binding.ID, ids.New()); ok {
		t.Fatal("a binding that is not due must not be claimed")
	}

	// A failed run leaves the watermark alone, counts the failure and keeps the claim machinery usable.
	runNow := func() string {
		t.Helper()
		cur, _ := s.GetSync(ctx, r.binding.ID)
		if _, err := s.UpdateSync(ctx, r.binding.ID, cur.Version, store.SyncEdit{RunNow: true}); err != nil {
			t.Fatal(err)
		}
		claim := ids.New()
		if _, ok, err := s.ClaimSync(ctx, r.binding.ID, claim); err != nil || !ok {
			t.Fatalf("claim after run now: %v %v", ok, err)
		}
		return claim
	}
	fail := func(claim string, failures int) {
		t.Helper()
		f := finishOK(r, claim, "2099-01-01T00:00:00.000000Z")
		f.Status, f.Advance, f.Error, f.Failures, f.BackoffSecs = domain.SyncFailed, false, "boom", failures, domain.SyncBackoff(failures).Seconds()
		if err := s.FinishSync(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	fail(runNow(), 1)
	v, _ = s.GetSync(ctx, r.binding.ID)
	if v.Watermark != "2026-11-01T10:00:00.000000Z" || v.ConsecutiveFailures != 1 || v.LastStatus != "failed" || v.LastError != "boom" || v.LastSuccessAt == nil {
		t.Fatalf("after a failed run the watermark must not move: %+v", v)
	}
	fail(runNow(), 3)
	v, _ = s.GetSync(ctx, r.binding.ID)
	if v.ConsecutiveFailures != 3 || v.NextDueAt == nil || v.NextDueAt.Before(time.Now().Add(14*time.Minute)) {
		t.Fatalf("three failures back the binding off: %+v", v)
	}
	if err := s.FinishSync(ctx, finishOK(r, runNow(), "2026-11-02T00:00:00.000000Z")); err != nil {
		t.Fatal(err)
	}
	if v, _ = s.GetSync(ctx, r.binding.ID); v.Watermark != "2026-11-02T00:00:00.000000Z" || v.ConsecutiveFailures != 0 {
		t.Fatalf("a success resets the failures and moves the watermark: %+v", v)
	}

	// Pause stops claims; resume restores them. Edits are versioned.
	cur, _ := s.GetSync(ctx, r.binding.ID)
	paused := true
	if _, err := s.UpdateSync(ctx, r.binding.ID, cur.Version-1, store.SyncEdit{Paused: &paused}); err == nil {
		t.Fatal("a stale version must be refused")
	} else {
		problemStatus(t, err, 409, store.CodeVersionConflict)
	}
	if v, err = s.UpdateSync(ctx, r.binding.ID, cur.Version, store.SyncEdit{Paused: &paused, RunNow: true}); err != nil || !v.Paused || v.Version != cur.Version+1 {
		t.Fatalf("pause = %+v %v", v, err)
	}
	if _, ok, _ := s.ClaimSync(ctx, r.binding.ID, ids.New()); ok {
		t.Fatal("a paused binding must not be claimed")
	}
	resumed, passage := false, domain.SourcePassage
	if v, err = s.UpdateSync(ctx, r.binding.ID, v.Version, store.SyncEdit{Paused: &resumed, SourceOfTruth: &passage}); err != nil || v.Paused || v.SourceOfTruth != "passage" {
		t.Fatalf("resume = %+v %v", v, err)
	}
	if _, err := s.UpdateSync(ctx, ids.New(), 0, store.SyncEdit{Paused: &paused}); err == nil {
		t.Fatal("an unknown binding is not found")
	}
	if list, err := s.ListSync(ctx, store.SyncFilter{Limit: 10}); err != nil || len(list) != 1 || list[0].BindingID != r.binding.ID || list[0].ProfileName != "Acme ERP" {
		t.Fatalf("list = %+v %v", list, err)
	}
	if list, _ := s.ListSync(ctx, store.SyncFilter{Failing: true, Limit: 10}); len(list) != 0 {
		t.Fatalf("failing filter = %+v", list)
	}

	// An abandoned claim is taken over after the window and counts as a failure.
	if age != nil {
		stale := runNow()
		age(r.binding.ID)
		taken := ids.New()
		st, ok, err := s.ClaimSync(ctx, r.binding.ID, taken)
		if err != nil || !ok || st.ConsecutiveFailures != 1 || st.LastStatus != "abandoned" {
			t.Fatalf("takeover = %+v %v %v", st, ok, err)
		}
		if err := s.FinishSync(ctx, finishOK(r, stale, "2099-01-01T00:00:00.000000Z")); !store.IsSyncClaimLost(err) {
			t.Fatalf("the abandoned run's late finish must be discarded: %v", err)
		}
		if v, _ := s.GetSync(ctx, r.binding.ID); v.Watermark != "2026-11-02T00:00:00.000000Z" {
			t.Fatalf("a discarded finish moved the watermark: %+v", v)
		}
		if err := s.ReleaseSync(ctx, r.binding.ID, taken); err != nil {
			t.Fatal(err)
		}
	}

	// Conflicts: list, keep Passage (remembered by hash), resolve twice is refused, a changed legacy row reopens it.
	open, err := s.ListConflicts(ctx, store.ConflictFilter{Status: "open", Limit: 10})
	if err != nil || len(open) != 1 || open[0].SourceKey != "C1" || open[0].Entity != "customer" || open[0].LegacyFields["name"] != "Legacy name" {
		t.Fatalf("open conflicts = %+v %v", open, err)
	}
	// A run that reads the same legacy row again leaves the version alone; one that reads a changed row bumps it, and
	// a resolution decided on the older version is refused.
	if open[0].Version != 1 {
		t.Fatalf("a new conflict starts at version 1: %+v", open[0])
	}
	if err := s.FinishSync(ctx, finishOK(r, runNow(), "2026-11-02T00:00:00.000000Z", conflict)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetConflict(ctx, open[0].ID); got.Version != 1 || got.Status != "open" {
		t.Fatalf("the same legacy row read again must not bump the version: %+v", got)
	}
	changed := conflict
	changed.ContentHash, changed.Fields = "h1b", map[string]any{"name": "Newer legacy name"}
	if err := s.FinishSync(ctx, finishOK(r, runNow(), "2026-11-02T00:00:00.000000Z", changed)); err != nil {
		t.Fatal(err)
	}
	_, err = s.ResolveConflict(ctx, open[0].ID, 1, domain.ConflictKept, "user:1")
	problemStatus(t, err, 409, store.CodeVersionConflict)
	if err := s.FinishSync(ctx, finishOK(r, runNow(), "2026-11-02T00:00:00.000000Z", conflict)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetConflict(ctx, open[0].ID)
	if got.Version != 3 || got.LegacyFields["name"] != "Legacy name" {
		t.Fatalf("reverted legacy row = %+v", got)
	}
	resolved, err := s.ResolveConflict(ctx, open[0].ID, got.Version, domain.ConflictKept, "user:1")
	if err != nil || resolved.Status != "kept" || resolved.Version != got.Version+1 || resolved.ResolvedBy != "user:1" {
		t.Fatalf("keep = %+v %v", resolved, err)
	}
	_, err = s.ResolveConflict(ctx, open[0].ID, resolved.Version, domain.ConflictAccepted, "user:1")
	problemStatus(t, err, 409, store.CodeConflictClosed)
	_, err = s.ResolveConflict(ctx, ids.New(), 1, domain.ConflictKept, "user:1")
	problemStatus(t, err, 404, "AUTH-0404")
	if kept, _ := s.KeptConflicts(ctx, r.binding.ID); kept["C1"] != "h1" {
		t.Fatalf("kept = %v", kept)
	}
	conflict.ContentHash = "h2"
	if err := s.FinishSync(ctx, finishOK(r, runNow(), "2026-11-03T00:00:00.000000Z", conflict)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetConflict(ctx, open[0].ID); got.Status != "open" || got.ResolvedAt != nil || got.Version != resolved.Version+1 {
		t.Fatalf("a changed legacy row must reopen the conflict: %+v", got)
	}
	if kept, _ := s.KeptConflicts(ctx, r.binding.ID); len(kept) != 0 {
		t.Fatalf("kept after reopen = %v", kept)
	}
	if _, err := s.GetConflict(ctx, ids.New()); err == nil {
		t.Fatal("unknown conflict")
	}
}

func TestMem_SyncContract(t *testing.T) {
	m := store.NewMem()
	exerciseSync(t, m, principalContext(1), store.MemTenant, func(id string) { m.AgeSyncClaim(id, time.Hour) })
}

func TestPostgres_SyncUnderRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t) // the app role proves the 0011 grants and the definer function are enough
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	one := principalContext(1)
	exerciseSync(t, s, one, testkit.TenantID(1), func(id string) {
		if _, err := admin.Exec(context.Background(), `update migration.sync_state set claimed_at = now() - interval '1 hour' where binding_id = $1`, id); err != nil {
			t.Fatal(err)
		}
	})
	var events int
	if err := admin.QueryRow(context.Background(), `select count(*) from migration.outbox where type like 'dev.passage.migration.syncrun.%.v1' and aggregate_type = 'syncrun'`).Scan(&events); err != nil || events < 5 {
		t.Fatalf("sync-run events in the outbox = %d %v", events, err)
	}
	// The failed run's event carries the watermark the binding kept, written in the transaction that ended the claim.
	var failedPayload string
	if err := admin.QueryRow(context.Background(), `select payload::text from migration.outbox where type = 'dev.passage.migration.syncrun.failed.v1' order by created_at, id limit 1`).Scan(&failedPayload); err != nil ||
		!strings.Contains(failedPayload, `"watermark": "2026-11-01T10:00:00.000000Z"`) {
		t.Fatalf("failed event = %s %v", failedPayload, err)
	}
	if ok, err := s.SyncCanScan(context.Background()); err != nil || !ok {
		t.Fatalf("the definer function owner must bypass RLS: %v %v", ok, err)
	}

	// Tenant two sees none of tenant one's sync rows, conflicts or claims, and cannot claim or edit them.
	two := principalContext(2)
	var bindingID string
	if err := admin.QueryRow(context.Background(), `select binding_id::text from migration.sync_state limit 1`).Scan(&bindingID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListSync(two, store.SyncFilter{Limit: 10}); len(list) != 0 {
		t.Fatalf("tenant two lists %d sync rows", len(list))
	}
	if cs, _ := s.ListConflicts(two, store.ConflictFilter{Limit: 10}); len(cs) != 0 {
		t.Fatalf("tenant two lists %d conflicts", len(cs))
	}
	if _, ok, _ := s.ClaimSync(two, bindingID, ids.New()); ok {
		t.Fatal("tenant two claimed tenant one's binding")
	}
	paused := true
	if _, err := s.UpdateSync(two, bindingID, 0, store.SyncEdit{Paused: &paused}); err == nil {
		t.Fatal("tenant two edited tenant one's sync")
	}
	if _, err := s.GetSync(two, bindingID); err == nil {
		t.Fatal("tenant two read tenant one's sync")
	}
	// A legal-entity scoped caller is shut out like on every migration table.
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: leA, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	if list, _ := s.ListSync(scoped, store.SyncFilter{Limit: 10}); len(list) != 0 {
		t.Fatalf("a legal-entity scoped caller lists %d rows", len(list))
	}
}

// Two schedulers racing for one due binding: the conditional update lets exactly one through.
func TestPostgres_ConcurrentClaimsAdmitOne(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	ctx := principalContext(1)
	r := newSyncRig(t, s, ctx, everyQuarter)
	seed(t, s, ctx, r.binding.ID, time.Now())
	var won atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, ok, err := s.ClaimSync(ctx, r.binding.ID, ids.New()); err != nil {
				t.Error(err)
			} else if ok {
				won.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d claims won, want 1", won.Load())
	}
}

// The definer function returns identifiers only, with a pinned search path, and the scheduling project of a binding
// is the lowest-id one: a second scheduled project on the same profile must not make the binding flip between them.
func TestPostgres_SyncCandidatesIdentifiersOnlyStableAndFair(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var secdef bool
	var config []string
	var outCols int
	if err := admin.QueryRow(context.Background(), `select prosecdef, proconfig, cardinality(proallargtypes) - 1 from pg_proc p join pg_namespace n on n.oid = p.pronamespace
		where n.nspname = 'migration' and p.proname = 'sync_candidates'`).Scan(&secdef, &config, &outCols); err != nil {
		t.Fatal(err)
	}
	if !secdef || len(config) != 1 || config[0] != "search_path=pg_catalog, pg_temp" || outCols != 2 {
		t.Fatalf("sync_candidates: definer=%v config=%v output columns=%d", secdef, config, outCols)
	}
	var publicExec bool
	if err := admin.QueryRow(context.Background(), `select has_function_privilege('public', 'migration.sync_candidates(int)', 'execute')`).Scan(&publicExec); err != nil || publicExec {
		t.Fatalf("PUBLIC may execute sync_candidates: %v %v", publicExec, err)
	}

	s := store.New(pool)
	one := principalContext(1)
	r := newSyncRig(t, s, one, everyQuarter)
	second, _, err := s.SaveProject(one, store.Project{Name: "Acme hourly", Type: domain.ProjectMigrate, SourceSystem: "custom_acme", Status: domain.ProjectActive,
		ConnectionProfileID: r.profile.ID, ScheduleCron: "0 * * * *"}, "")
	if err != nil {
		t.Fatal(err)
	}
	first := min(r.project.ID, second.ID)
	seed(t, s, one, r.binding.ID, time.Now().Add(time.Hour))
	if _, ok, err := s.ClaimSync(one, r.binding.ID, ids.New()); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	for range 3 {
		cands, err := s.SyncCandidates(context.Background(), 10)
		if err != nil || len(cands) != 0 {
			t.Fatalf("a claimed binding with two scheduled projects is a candidate again: %+v %v", cands, err)
		}
	}
	if v, _ := s.GetSync(one, r.binding.ID); v.ProjectID != first {
		t.Fatalf("scheduling project = %s, want the lowest id %s", v.ProjectID, first)
	}

	// Tenant one has three never-run bindings, tenant two one: a scan of two returns one of each.
	for _, e := range []string{"vendor", "item"} {
		if _, _, err := s.SaveEntityBinding(one, custBinding(r.profile.ID, e), ""); err != nil {
			t.Fatal(err)
		}
	}
	two := principalContext(2)
	r2 := newSyncRig(t, s, two, everyQuarter)
	cands, err := s.SyncCandidates(context.Background(), 2)
	if err != nil || len(cands) != 2 || cands[0].TenantID == cands[1].TenantID {
		t.Fatalf("candidates are not shared across tenants: %+v %v", cands, err)
	}
	found := false
	for _, c := range cands {
		found = found || c.BindingID == r2.binding.ID
	}
	if !found {
		t.Fatalf("tenant two's binding is missing: %+v", cands)
	}
}

// loadXref records customer keys of the rig's source system as loaded, each into legalEntity ("" untagged).
func loadXref(t *testing.T, s store.Store, ctx context.Context, legalEntity string, keys ...string) {
	t.Helper()
	items := make([]domain.XrefItem, len(keys))
	for i, k := range keys {
		items[i] = domain.XrefItem{SourceKey: k, Entry: domain.XrefEntry{TargetID: ids.New(), ContentHash: "h", LegalEntityID: legalEntity}}
	}
	if err := s.Xref().PutMany(ctx, "custom_acme", "customer", items); err != nil {
		t.Fatal(err)
	}
}

// absentRig claims, finishes and makes due again one binding; list returns its absent keys.
type absentRig struct {
	t   *testing.T
	s   store.Store
	ctx context.Context
	r   syncRig
}

func (a absentRig) finish(mutate func(f *store.SyncFinish)) {
	a.t.Helper()
	claim := ids.New()
	if _, ok, err := a.s.ClaimSync(a.ctx, a.r.binding.ID, claim); err != nil || !ok {
		a.t.Fatalf("claim: %v %v", ok, err)
	}
	f := finishOK(a.r, claim, "w")
	mutate(&f)
	if err := a.s.FinishSync(a.ctx, f); err != nil {
		a.t.Fatal(err)
	}
	cur, _ := a.s.GetSync(a.ctx, a.r.binding.ID)
	if _, err := a.s.UpdateSync(a.ctx, a.r.binding.ID, cur.Version, store.SyncEdit{RunNow: true}); err != nil {
		a.t.Fatal(err)
	}
}

func (a absentRig) list() []string {
	a.t.Helper()
	rows, err := a.s.ListAbsent(a.ctx, store.AbsentFilter{BindingID: a.r.binding.ID, Limit: 10})
	if err != nil {
		a.t.Fatal(err)
	}
	var out []string
	for _, x := range rows {
		if x.TargetID == "" {
			a.t.Fatalf("absent key %s has no target id", x.SourceKey)
		}
		out = append(out, x.SourceKey)
	}
	return out
}

// exerciseAbsent: a full read replaces the binding's absent list, announces only newly absent keys and counts as the
// key check; a run that is not a full read, or failed, leaves the list alone; a failed full read still defers the
// next check by a period so the runs in between stay deltas.
func exerciseAbsent(t *testing.T, s store.Store, ctx context.Context, outboxCount func() int) {
	r := newSyncRig(t, s, ctx, everyQuarter)
	seed(t, s, ctx, r.binding.ID, time.Now().Add(-time.Minute))
	loadXref(t, s, ctx, "", "A", "B", "C", "Z")
	a := absentRig{t, s, ctx, r}
	finish, list := a.finish, a.list
	if v, _ := s.GetSync(ctx, r.binding.ID); v.KeyCheckHours != domain.DefaultKeyCheckHours || !v.KeyCheckDue || v.LastKeyCheckAt != nil || v.AbsentCount != 0 {
		t.Fatalf("before any full read: %+v", v)
	}
	base := outboxCount()

	finish(func(f *store.SyncFinish) { f.FullRead, f.Absent = true, []string{"B", "A"} })
	if got := list(); len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("absent = %v", got)
	}
	if v, _ := s.GetSync(ctx, r.binding.ID); v.AbsentCount != 2 || v.LastKeyCheckAt == nil || v.KeyCheckDue {
		t.Fatalf("after the first full read: %+v", v)
	}
	if n := outboxCount() - base; n != 2 { // the run event and one absent event
		t.Fatalf("events = %d, want run + absent", n)
	}

	// A delta (not a full read) and a failed full read leave the list and the last completed check alone.
	checked := func() time.Time { v, _ := s.GetSync(ctx, r.binding.ID); return *v.LastKeyCheckAt }
	at := checked()
	finish(func(f *store.SyncFinish) { f.Absent = []string{"Z"} })
	asked, _ := s.GetSync(ctx, r.binding.ID)
	if v, err := s.UpdateSync(ctx, r.binding.ID, asked.Version, store.SyncEdit{KeyCheckNow: true}); err != nil || !v.KeyCheckDue {
		t.Fatalf("keyCheckNow: %+v %v", v, err)
	}
	finish(func(f *store.SyncFinish) {
		f.KeyCheckAttempted, f.Absent, f.Status, f.Error, f.Advance = true, []string{"Z"}, domain.SyncFailed, "orders has more than 2000000 rows", false
	})
	if got := list(); len(got) != 2 {
		t.Fatalf("absent after a delta and a failed read = %v", got)
	}
	if v, _ := s.GetSync(ctx, r.binding.ID); v.KeyCheckDue || v.KeyCheckError == "" || !v.LastKeyCheckAt.Equal(at) || v.AbsentCount != 2 {
		t.Fatalf("a failed full read must defer the next check, keep the list and say why: %+v", v)
	}
	// Asking for a check makes it due again; the next completed read clears the error.
	cur, _ := s.GetSync(ctx, r.binding.ID)
	if v, err := s.UpdateSync(ctx, r.binding.ID, cur.Version, store.SyncEdit{KeyCheckNow: true}); err != nil || !v.KeyCheckDue || v.LastKeyCheckAt == nil {
		t.Fatalf("keyCheckNow: %+v %v", v, err)
	}

	// B stays missing (no new event), A is back, C is new: the list is replaced and only C is announced.
	before := outboxCount()
	finish(func(f *store.SyncFinish) { f.FullRead, f.Absent = true, []string{"B", "C"} })
	if got := list(); len(got) != 2 || got[0] != "B" || got[1] != "C" {
		t.Fatalf("absent = %v, want [B C]", got)
	}
	if n := outboxCount() - before; n != 2 {
		t.Fatalf("events = %d, want run + absent for C", n)
	}
	before = outboxCount()
	finish(func(f *store.SyncFinish) { f.FullRead, f.Absent = true, []string{"B", "C"} })
	if n := outboxCount() - before; n != 1 {
		t.Fatalf("an unchanged absent list published %d events, want only the run's", n)
	}
	// Paging by key, and an empty full read clears the list.
	rows, _ := s.ListAbsent(ctx, store.AbsentFilter{BindingID: r.binding.ID, After: "B", Limit: 10})
	if len(rows) != 1 || rows[0].SourceKey != "C" {
		t.Fatalf("page after B = %+v", rows)
	}
	if v, _ := s.GetSync(ctx, r.binding.ID); v.KeyCheckError != "" {
		t.Fatalf("a completed read keeps the error: %q", v.KeyCheckError)
	}
	// A key the binding never loaded (no xref row) is not listed.
	finish(func(f *store.SyncFinish) { f.FullRead, f.Absent = true, []string{"B", "C", "NEVER"} })
	if got := list(); len(got) != 2 {
		t.Fatalf("absent = %v, want [B C]", got)
	}
	// A read that found no rows at all is taken for a broken source: the list stays and the reason is shown.
	finish(func(f *store.SyncFinish) { f.FullRead, f.Absent, f.Extracted = true, []string{"A", "B", "C", "Z"}, 0 })
	if v, _ := s.GetSync(ctx, r.binding.ID); len(list()) != 2 || v.AbsentCount != 2 || v.KeyCheckError == "" || v.KeyCheckDue {
		t.Fatalf("an empty read replaced the list: %v %+v", list(), v)
	}
	finish(func(f *store.SyncFinish) { f.FullRead = true })
	if got := list(); len(got) != 0 {
		t.Fatalf("absent after a full read that found every key = %v", got)
	}

	// The period and an explicit check request drive KeyCheckDue.
	zero := 0
	cur, _ = s.GetSync(ctx, r.binding.ID)
	v, err := s.UpdateSync(ctx, r.binding.ID, cur.Version, store.SyncEdit{KeyCheckHours: &zero, KeyCheckNow: true})
	if err != nil || v.KeyCheckHours != 0 || v.KeyCheckDue || v.LastKeyCheckAt == nil {
		t.Fatalf("check off: %+v %v", v, err)
	}
	day := 24
	v, err = s.UpdateSync(ctx, r.binding.ID, v.Version, store.SyncEdit{KeyCheckHours: &day})
	if err != nil || v.KeyCheckHours != 24 || !v.KeyCheckDue {
		t.Fatalf("check on, asked for: %+v %v", v, err)
	}
	if _, err := s.ListAbsent(ctx, store.AbsentFilter{BindingID: ids.New(), Limit: 10}); err == nil {
		t.Fatal("unknown binding")
	}
}

func TestMem_AbsentContract(t *testing.T) {
	m := store.NewMem()
	exerciseAbsent(t, m, principalContext(1), func() int { return len(m.SyncEvents()) })
}

func TestPostgres_AbsentUnderRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	exerciseAbsent(t, s, principalContext(1), func() int {
		var n int
		if err := admin.QueryRow(context.Background(), `select count(*) from migration.outbox`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	})
	// Leave one absent key behind for the tenant isolation checks.
	ctx := principalContext(3)
	r := newSyncRig(t, s, ctx, everyQuarter)
	seed(t, s, ctx, r.binding.ID, time.Now().Add(-time.Minute))
	claim := ids.New()
	if _, ok, _ := s.ClaimSync(ctx, r.binding.ID, claim); !ok {
		t.Fatal("claim")
	}
	loadXref(t, s, ctx, "", "K1")
	f := finishOK(r, claim, "w")
	f.FullRead, f.Absent = true, []string{"K1"}
	if err := s.FinishSync(ctx, f); err != nil {
		t.Fatal(err)
	}
	two := principalContext(2)
	if _, err := s.ListAbsent(two, store.AbsentFilter{BindingID: r.binding.ID, Limit: 10}); err == nil {
		t.Fatal("tenant two listed tenant one's binding")
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from migration.sync_absent a join migration.xref x on x.tenant_id = a.tenant_id
		and x.source_key = a.source_key and x.target_id::text = a.target_id where a.source_key = 'K1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("absent row carries the xref target id: %d %v", n, err)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(3), LegalEntityID: leA, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	if rows, _ := s.ListAbsent(scoped, store.AbsentFilter{BindingID: r.binding.ID, Limit: 10}); len(rows) != 0 {
		t.Fatalf("a legal-entity scoped caller lists %d absent keys", len(rows))
	}
}

// exerciseAbsentScope: a key check lists only keys of its own legal entity, and nothing when another binding shares
// the key space.
func exerciseAbsentScope(t *testing.T, s store.Store, ctx context.Context) {
	profile, _, err := s.SaveConnectionProfile(ctx, sqlProfile("Acme DE"), "")
	if err != nil {
		t.Fatal(err)
	}
	binding, _, err := s.SaveEntityBinding(ctx, custBinding(profile.ID, "customer"), "")
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := s.SaveProject(ctx, store.Project{Name: "Acme DE sync", Type: domain.ProjectMigrate, SourceSystem: "custom_acme", TargetLegalEntityID: leA,
		Status: domain.ProjectActive, ConnectionProfileID: profile.ID, ScheduleCron: everyQuarter}, "")
	if err != nil {
		t.Fatal(err)
	}
	r := syncRig{profile, binding, project}
	seed(t, s, ctx, binding.ID, time.Now().Add(-time.Minute))
	loadXref(t, s, ctx, leA, "A1")
	loadXref(t, s, ctx, leB, "B1")
	loadXref(t, s, ctx, "", "N1")
	a := absentRig{t, s, ctx, r}
	all := []string{"A1", "B1", "N1"}

	// Alone in the source system: its own legal entity and untagged rows, never another legal entity's.
	a.finish(func(f *store.SyncFinish) { f.FullRead, f.Absent = true, all })
	if got := a.list(); len(got) != 2 || got[0] != "A1" || got[1] != "N1" {
		t.Fatalf("absent = %v, want [A1 N1]", got)
	}
	// Another binding loads customers from the same source system into leB: untagged rows may be its own.
	other := func(name, le string) {
		p, _, err := s.SaveConnectionProfile(ctx, sqlProfile(name), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.SaveEntityBinding(ctx, custBinding(p.ID, "customer"), ""); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.SaveProject(ctx, store.Project{Name: name + " sync", Type: domain.ProjectMigrate, SourceSystem: "custom_acme", TargetLegalEntityID: le,
			Status: domain.ProjectActive, ConnectionProfileID: p.ID}, ""); err != nil {
			t.Fatal(err)
		}
	}
	other("Acme US", leB)
	a.finish(func(f *store.SyncFinish) { f.FullRead, f.Absent = true, all })
	if got := a.list(); len(got) != 1 || got[0] != "A1" {
		t.Fatalf("absent = %v, want [A1]", got)
	}
	// A third binding loads customers into any legal entity: the key space is shared, nothing is recorded.
	other("Acme any", "")
	a.finish(func(f *store.SyncFinish) { f.FullRead, f.Absent = true, []string{"A1", "B1", "N1"} })
	v, _ := s.GetSync(ctx, binding.ID)
	if got := a.list(); len(got) != 1 || v.KeyCheckError == "" || v.AbsentCount != 1 || v.KeyCheckDue {
		t.Fatalf("a shared key space changed the list: %v %+v", got, v)
	}
}

func TestMem_AbsentScope(t *testing.T) {
	exerciseAbsentScope(t, store.NewMem(), principalContext(1))
}

func TestPostgres_AbsentScopeUnderRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	exerciseAbsentScope(t, store.New(pool), principalContext(1))
}
