package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/testdb"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

const nightly = "0 3 * * *"

// seedCron seeds a binding and returns the cron the store asked next() about.
func seedCron(t *testing.T, s store.Store, ctx context.Context, bindingID string, due time.Time) string {
	t.Helper()
	got := ""
	if err := s.SeedSync(ctx, bindingID, func(c string) (time.Time, bool) { got = c; return due, true }); err != nil {
		t.Fatal(err)
	}
	return got
}

func candidateIDs(t *testing.T, s store.Store) map[string]bool {
	t.Helper()
	cs, err := s.SyncCandidates(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, c := range cs {
		out[c.BindingID] = true
	}
	return out
}

// exerciseScheduleOverride runs against Postgres (under the app role) and Mem: each binding may carry its own cron
// and the project's cron stays the default.
func exerciseScheduleOverride(t *testing.T, s store.Store, ctx context.Context) {
	r := newSyncRig(t, s, ctx, everyQuarter) // customer binding, project every 15 minutes
	vendor, _, err := s.SaveEntityBinding(ctx, custBinding(r.profile.ID, "vendor"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := seedCron(t, s, ctx, vendor.ID, time.Now().Add(time.Hour)); got != everyQuarter {
		t.Fatalf("without an override the project's cron applies, got %q", got)
	}
	v, _ := s.GetSync(ctx, vendor.ID)
	if v.ScheduleCron != everyQuarter || v.ScheduleOverride != "" {
		t.Fatalf("default view = %+v", v)
	}

	// An override wins for its binding only, and moves next_due_at to the next slot of the new cron.
	nightlyCron := nightly
	if _, err := s.UpdateSync(ctx, vendor.ID, v.Version, store.SyncEdit{ScheduleCron: &nightlyCron}); err != nil {
		t.Fatal(err)
	}
	if !candidateIDs(t, s)[vendor.ID] {
		t.Fatal("a changed schedule must make the binding a candidate so it is re-seeded")
	}
	due := time.Now().Add(7 * time.Hour).Truncate(time.Second)
	if got := seedCron(t, s, ctx, vendor.ID, due); got != nightly {
		t.Fatalf("override cron not used: %q", got)
	}
	v, _ = s.GetSync(ctx, vendor.ID)
	if v.ScheduleCron != nightly || v.ScheduleOverride != nightly || v.ProjectScheduleCron != everyQuarter || v.NextDueAt == nil || v.NextDueAt.Sub(due).Abs() > time.Second || v.ProjectID != r.project.ID {
		t.Fatalf("after override = %+v", v)
	}
	if c, _ := s.GetSync(ctx, r.binding.ID); c.ScheduleCron != everyQuarter || c.ScheduleOverride != "" {
		t.Fatalf("the other binding keeps the project's cron: %+v", c)
	}
	// It is not due before its slot, so it is neither a candidate nor claimable.
	if candidateIDs(t, s)[vendor.ID] {
		t.Fatal("a binding on a nightly cron is not due")
	}
	if _, ok, _ := s.ClaimSync(ctx, vendor.ID, ids.New()); ok {
		t.Fatal("claimed before its own slot")
	}

	// Editing the schedule leaves the watermark alone (it is not a binding edit).
	// Clearing the override hands the binding back to the project's cron.
	empty := ""
	if _, err := s.UpdateSync(ctx, vendor.ID, v.Version, store.SyncEdit{ScheduleCron: &empty}); err != nil {
		t.Fatal(err)
	}
	if got := seedCron(t, s, ctx, vendor.ID, time.Now().Add(time.Hour)); got != everyQuarter {
		t.Fatalf("cleared override should fall back to the project, got %q", got)
	}
	if v, _ = s.GetSync(ctx, vendor.ID); v.ScheduleCron != everyQuarter || v.ScheduleOverride != "" {
		t.Fatalf("after clearing = %+v", v)
	}
	// A due claim under the override needs the override to match the stored cron.
	if _, err := s.UpdateSync(ctx, vendor.ID, v.Version, store.SyncEdit{ScheduleCron: &nightlyCron}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ClaimSync(ctx, vendor.ID, ids.New()); ok {
		t.Fatal("an unseeded schedule change must not be claimed")
	}
	seedCron(t, s, ctx, vendor.ID, time.Now().Add(-time.Minute))
	claim := ids.New()
	claimed, ok, err := s.ClaimSync(ctx, vendor.ID, claim)
	if err != nil || !ok || claimed.ScheduleCron != nightly {
		t.Fatalf("claim under the override: %v %v %+v", ok, err, claimed)
	}
	// The schedule changes while the run reads the source and the scheduler re-seeds it: the end of the run keeps the
	// slot of the new cron instead of writing back the next slot of the cron it was claimed under.
	v, _ = s.GetSync(ctx, vendor.ID)
	if _, err := s.UpdateSync(ctx, vendor.ID, v.Version, store.SyncEdit{ScheduleCron: &empty}); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	seedCron(t, s, ctx, vendor.ID, soon)
	if err := s.FinishSync(ctx, store.SyncFinish{BindingID: vendor.ID, ClaimID: claim, RunID: ids.New(), Entity: "vendor", Status: domain.SyncOK,
		BindingVersion: vendor.Version, NextCron: time.Now().Add(20 * time.Hour), Cron: claimed.ScheduleCron}); err != nil {
		t.Fatal(err)
	}
	if v, _ = s.GetSync(ctx, vendor.ID); v.ScheduleCron != everyQuarter || v.NextDueAt == nil || v.NextDueAt.Sub(soon).Abs() > time.Second {
		t.Fatalf("a run that ends after a schedule change keeps the new slot: %+v", v)
	}

	// A project without a schedule still hosts a binding that has its own.
	p2, _, err := s.SaveConnectionProfile(ctx, sqlProfile("Second ERP"), "")
	if err != nil {
		t.Fatal(err)
	}
	b2, _, err := s.SaveEntityBinding(ctx, custBinding(p2.ID, "customer"), "")
	if err != nil {
		t.Fatal(err)
	}
	bare := nightly
	if _, err := s.UpdateSync(ctx, b2.ID, 0, store.SyncEdit{ScheduleCron: &bare}); err == nil {
		t.Fatal("an override needs an active migrate project on the profile")
	} else {
		var pb *httpx.Problem
		if !errors.As(err, &pb) || pb.Code != store.CodeSyncNoProject {
			t.Fatalf("no-project error = %v", err)
		}
	}
	if _, _, err := s.SaveProject(ctx, store.Project{Name: "Unscheduled", Type: domain.ProjectMigrate, SourceSystem: "custom_two", Status: domain.ProjectActive, ConnectionProfileID: p2.ID}, ""); err != nil {
		t.Fatal(err)
	}
	if candidateIDs(t, s)[b2.ID] {
		t.Fatal("a binding with no schedule anywhere is not a candidate")
	}
	if _, err := s.UpdateSync(ctx, b2.ID, 0, store.SyncEdit{ScheduleCron: &bare}); err != nil {
		t.Fatal(err)
	}
	if !candidateIDs(t, s)[b2.ID] {
		t.Fatal("an override schedules the binding under the unscheduled project")
	}
	if got := seedCron(t, s, ctx, b2.ID, time.Now().Add(time.Hour)); got != nightly {
		t.Fatalf("cron under unscheduled project = %q", got)
	}
	v, _ = s.GetSync(ctx, b2.ID)
	if v.ProjectID == "" || v.ScheduleCron != nightly || v.ProjectScheduleCron != "" {
		t.Fatalf("view = %+v", v)
	}
	// Clearing the override where the project has no schedule leaves the binding unscheduled, and the status says so.
	if v, err = s.UpdateSync(ctx, b2.ID, v.Version, store.SyncEdit{ScheduleCron: &empty}); err != nil || v.ScheduleCron != "" || v.NextDueAt != nil {
		t.Fatalf("cleared without a project schedule = %+v %v", v, err)
	}
	if candidateIDs(t, s)[b2.ID] {
		t.Fatal("a binding whose override was cleared under an unscheduled project is not a candidate")
	}
	if _, ok, _ := s.ClaimSync(ctx, b2.ID, ids.New()); ok {
		t.Fatal("claimed with no schedule")
	}
}

func TestMem_ScheduleOverride(t *testing.T) {
	exerciseScheduleOverride(t, store.NewMem(), principalContext(1))
}

func TestPostgres_ScheduleOverrideUnderRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	exerciseScheduleOverride(t, store.New(pool), principalContext(1))
}
