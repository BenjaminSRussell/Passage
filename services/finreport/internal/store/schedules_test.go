package store

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

func nightly() domain.Schedule {
	return domain.Schedule{Code: "NIGHTLY", Name: "Nightly pack", Definition: "SCHED-DEF", Cron: "0 23 * * *", Timezone: "UTC",
		LedgerIDs: []string{ids.New()}, CurrencyType: "acct", Formats: []string{"pdf"}, Recipients: []string{"a@example.com"},
		AttachmentLimitMB: 20, Enabled: true, UpdatedBy: "u1"}
}

// FR-15.8: a schedule needs a published definition; creating needs no
// If-Match, replacing needs the current revision (428 missing, 412 stale).
func TestSchedule_SaveReplaceAndRevisions(t *testing.T) {
	st, ctx := migrated(t)
	sc := nightly()
	if _, _, err := st.SaveSchedule(ctx, sc, nil); problemCode(err) != CodeNotPublished {
		t.Fatalf("schedule over an unpublished definition = %v, want %s", err, CodeNotPublished)
	}
	if _, _, err := st.SaveDraft(ctx, storeFixtureDefinition("SCHED-DEF"), nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveSchedule(ctx, sc, nil); problemCode(err) != CodeNotPublished {
		t.Fatalf("a draft is not enough: %v", err)
	}
	if _, err := st.Publish(ctx, "SCHED-DEF", "a"); err != nil {
		t.Fatal(err)
	}
	if _, rev, err := st.SaveSchedule(ctx, sc, nil); err != nil || rev != 1 {
		t.Fatalf("create: rev %d, %v", rev, err)
	}
	sc.Enabled = false
	if _, _, err := st.SaveSchedule(ctx, sc, nil); problemCode(err) != "precondition_required" {
		t.Fatalf("replace without If-Match = %v, want 428", err)
	}
	stale := 5
	if _, _, err := st.SaveSchedule(ctx, sc, &stale); problemCode(err) != CodeRevisionConflict {
		t.Fatalf("replace with a stale If-Match = %v, want %s", err, CodeRevisionConflict)
	}
	one := 1
	if _, rev, err := st.SaveSchedule(ctx, sc, &one); err != nil || rev != 2 {
		t.Fatalf("replace: rev %d, %v", rev, err)
	}
	got, rev, err := st.GetSchedule(ctx, "NIGHTLY")
	if err != nil || rev != 2 || got.Enabled || got.LedgerIDs[0] != sc.LedgerIDs[0] || got.UpdatedBy != "u1" {
		t.Fatalf("get: %+v rev %d, %v", got, rev, err)
	}
	second := nightly()
	second.Code = "AAA-FIRST"
	if _, _, err := st.SaveSchedule(ctx, second, nil); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListSchedules(ctx)
	if err != nil || len(list) != 2 || list[0].Code != "AAA-FIRST" || list[1].Code != "NIGHTLY" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, _, err := st.GetSchedule(ctx, "MISSING"); problemCode(err) != CodeScheduleNotFound {
		t.Fatalf("missing schedule = %v, want %s", err, CodeScheduleNotFound)
	}
	if _, err := st.RetireDefinition(ctx, "SCHED-DEF"); err != nil {
		t.Fatal(err)
	}
	two := 2
	if _, _, err := st.SaveSchedule(ctx, sc, &two); problemCode(err) != CodeNotPublished {
		t.Fatalf("schedule over a retired definition = %v, want %s", err, CodeNotPublished)
	}
}

// RunOutput returns the run with the exact definition version and the cells it
// was evaluated with, even after a newer version is published.
func TestRunOutput_UsesTheRunsDefinitionVersion(t *testing.T) {
	st, ctx := migrated(t)
	if _, _, err := st.SaveDraft(ctx, storeFixtureDefinition("OUT-DEF"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "OUT-DEF", "a"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	seedBalance(t, ctx, st.pool, ledger, 2026, 3, "1000", "acct", "10.5", 7)
	run, err := st.CreateRun(ctx, RunSpec{Code: "OUT-DEF", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		Calendar: storeFixtureCalendar(), IdempotencyKey: "out", RequestHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	v2 := storeFixtureDefinition("OUT-DEF")
	v2.Name = "Renamed in version 2"
	if _, _, err := st.SaveDraft(ctx, v2, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "OUT-DEF", "a"); err != nil {
		t.Fatal(err)
	}
	got, def, cells, err := st.RunOutput(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != run.ID || def.Version != 1 || def.Name != "Store fixture" || len(cells) != 2 || cells[0].Value.StringFixed(2) != "10.50" {
		t.Fatalf("output = run %s, def v%d %q, cells %+v", got.ID, def.Version, def.Name, cells)
	}
	if _, _, _, err := st.RunOutput(ctx, ids.New()); problemCode(err) != CodeRunNotFound {
		t.Fatalf("unknown run = %v, want %s", err, CodeRunNotFound)
	}
}
