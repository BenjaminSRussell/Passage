package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

func ruleCode(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return problemCode(err)
}

func TestProjectGroups_UpsertVersionsAndTenantScope(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(11)
	g, created, err := s.UpsertProjectGroup(ctx, domain.ProjectGroup{ID: "OPS", Name: "Operations", DefaultProjectType: domain.TypeTimeMaterial}, 0)
	if err != nil || !created || g.Version != 1 || g.DefaultProjectType != domain.TypeTimeMaterial {
		t.Fatalf("create: %+v created=%v err=%v", g, created, err)
	}
	if _, _, err := s.UpsertProjectGroup(ctx, domain.ProjectGroup{ID: "OPS", Name: "Ops"}, 0); problemCode(err) != CodePrecondition {
		t.Fatalf("replace without If-Match: %v", err)
	}
	if _, _, err := s.UpsertProjectGroup(ctx, domain.ProjectGroup{ID: "OPS", Name: "Ops"}, 4); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale If-Match: %v", err)
	}
	g, created, err = s.UpsertProjectGroup(ctx, domain.ProjectGroup{ID: "OPS", Name: "Ops"}, 1)
	if err != nil || created || g.Version != 2 || g.Name != "Ops" || g.DefaultProjectType != "" {
		t.Fatalf("replace: %+v created=%v err=%v", g, created, err)
	}
	list, err := s.ListProjectGroups(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "ENG" || list[1].ID != "OPS" {
		t.Fatalf("list: %+v %v", list, err)
	}
	other, _ := s.ListProjectGroups(tenantCtx(12))
	for _, og := range other {
		if og.ID == "OPS" {
			t.Fatal("tenant 12 sees tenant 11's group")
		}
	}
}

func TestCreateProject_UnknownGroupIsRefused(t *testing.T) {
	s, _ := setup(t)
	p := sampleProject("PROJ-NOGRP")
	p.ProjectGroupID = "NOPE"
	if _, _, err := s.CreateProject(tenantCtx(11), p, ""); problemCode(err) != CodeGroupUnknown {
		t.Fatalf("unknown group: %v", err)
	}
}

func TestPostingProfiles_StoreListAndRejectDeadKeys(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(13)
	mk := func(group, cat, prop, cost string) domain.PostingProfile {
		return domain.PostingProfile{Key: domain.ProfileKey{ProjectGroup: group, Category: cat, LineProperty: prop}, CostAccount: cost,
			WIPAccount: "1400", AccruedRevenueAccount: "1410", InvoicedRevenueAccount: "4000", OnAccountAccount: "2400"}
	}
	for _, p := range []domain.PostingProfile{mk("ENG", "LABOR", "CHARGEABLE", "5100"), mk("ENG", "*", "*", "5200"), mk("*", "*", "*", "5900")} {
		if _, created, err := s.UpsertPostingProfile(ctx, p, 0); err != nil || !created {
			t.Fatalf("create %v: %v", p.Key, err)
		}
	}
	updated, created, err := s.UpsertPostingProfile(ctx, mk("ENG", "*", "*", "5250"), 1)
	if err != nil || created || updated.Version != 2 || updated.CostAccount != "5250" {
		t.Fatalf("replace: %+v %v", updated, err)
	}
	list, err := s.ListPostingProfiles(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %v %v", list, err)
	}
	got, err := domain.Resolve(list, domain.ProfileKey{ProjectGroup: "ENG", Category: "TRAVEL", LineProperty: "CHARGEABLE"})
	if err != nil || got.CostAccount != "5250" {
		t.Fatalf("resolve through stored rows: %+v %v", got, err)
	}
	// The table refuses a key the resolver could never read, even past the API.
	if _, _, err := s.UpsertPostingProfile(ctx, mk("ENG", "*", "CHARGEABLE", "5100"), 0); err == nil {
		t.Fatal("dead key (group, *, property) must be refused by the table")
	}
}

func wbsInput() ([]domain.WBSNodeInput, []domain.WBSDependencyInput) {
	one := "1"
	nodes := []domain.WBSNodeInput{
		{WBSCode: "1", Name: "Design", DurationDays: 0},
		{WBSCode: "1.10", ParentWBSCode: &one, Name: "Sign-off", DurationDays: 1},
		{WBSCode: "1.2", ParentWBSCode: &one, Name: "Draft", DurationDays: 3, EffortHours: strPtr("24.50")},
	}
	deps := []domain.WBSDependencyInput{{PredecessorWBSCode: "1.2", SuccessorWBSCode: "1.10", Type: domain.DepFS, LagDays: 2}}
	domain.NormalizeWBS(nodes)
	return nodes, deps
}

func strPtr(s string) *string { return &s }

func TestPlans_ReplaceScheduleCopyAndLock(t *testing.T) {
	s, outboxCount := setup(t)
	ctx := tenantCtx(14)
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-PLN"), ""); err != nil {
		t.Fatal(err)
	}
	working, created, err := s.CreatePlanVersion(ctx, "PROJ-PLN", NewPlan{Kind: domain.KindWorking}, "plan-1")
	if err != nil || !created || working.Sequence != 1 || working.Locked {
		t.Fatalf("working: %+v %v", working, err)
	}
	if again, created, err := s.CreatePlanVersion(ctx, "PROJ-PLN", NewPlan{Kind: domain.KindWorking}, "plan-1"); err != nil || created || again.ID != working.ID {
		t.Fatalf("replay: %+v created=%v %v", again, created, err)
	}
	if _, _, err := s.CreatePlanVersion(ctx, "PROJ-PLN", NewPlan{Kind: domain.KindWorking}, "plan-2"); ruleCode(err) != domain.CodePlanKind {
		t.Fatalf("second working: %v", err)
	}

	nodes, deps := wbsInput()
	if _, err := s.ReplaceWBS(ctx, "PROJ-PLN", working.ID, 3, nodes, deps); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale replace: %v", err)
	}
	w, err := s.ReplaceWBS(ctx, "PROJ-PLN", working.ID, 1, nodes, deps)
	if err != nil || w.Plan.Version != 2 || len(w.Nodes) != 3 || len(w.Links) != 1 {
		t.Fatalf("replace: %+v %v", w, err)
	}
	// Codes sort numerically: 1, 1.2, 1.10.
	if fmt.Sprintf("%s %s %s", w.Nodes[0].WBSCode, w.Nodes[1].WBSCode, w.Nodes[2].WBSCode) != "1 1.2 1.10" || *w.Nodes[1].EffortHours != "24.50" {
		t.Fatalf("node order or effort: %+v", w.Nodes)
	}
	if w.Nodes[1].ParentID == nil || *w.Nodes[1].ParentID != w.Nodes[0].ID || w.Nodes[1].EarlyStart != nil {
		t.Fatalf("parent link or unscheduled dates: %+v", w.Nodes[1])
	}

	res, err := s.SchedulePlan(ctx, "PROJ-PLN", working.ID)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	// Draft Thu 1 to Mon 5 Oct; FS lag 2 puts Sign-off on Thu 8.
	draft, signoff := res.Nodes[1], res.Nodes[2]
	if res.FinishDate != "2026-10-08" || *draft.EarlyFinish != "2026-10-05" || *signoff.EarlyStart != "2026-10-08" ||
		fmt.Sprint(res.CriticalPath) != fmt.Sprint([]string{draft.ID, signoff.ID}) {
		t.Fatalf("schedule: finish %s draft %s signoff %s critical %v", res.FinishDate, *draft.EarlyFinish, *signoff.EarlyStart, res.CriticalPath)
	}
	stored, err := s.LoadWBS(ctx, "PROJ-PLN", working.ID)
	if err != nil || stored.Plan.Version != 2 || stored.Nodes[0].EarlyStart == nil || *stored.Nodes[0].EarlyFinish != "2026-10-08" || !*stored.Nodes[2].Critical {
		t.Fatalf("stored schedule: %+v %v", stored.Nodes, err)
	}
	if n := outboxCount(14, working.ID); n != 1 {
		t.Fatalf("wbs.scheduled outbox rows = %d, want 1", n)
	}
	assertScheduledEvent(t, s, ctx, working.ID, res)

	baseline, _, err := s.CreatePlanVersion(ctx, "PROJ-PLN", NewPlan{Kind: domain.KindBaseline, CopyFromVersionID: working.ID}, "plan-3")
	if err != nil || !baseline.Locked || baseline.Sequence != 2 || baseline.CopiedFromVersionID == nil || *baseline.CopiedFromVersionID != working.ID {
		t.Fatalf("baseline: %+v %v", baseline, err)
	}
	copied, err := s.LoadWBS(ctx, "PROJ-PLN", baseline.ID)
	if err != nil || len(copied.Nodes) != 3 || len(copied.Links) != 1 {
		t.Fatalf("baseline copy: %+v %v", copied, err)
	}
	if copied.Nodes[0].ID == stored.Nodes[0].ID || *copied.Nodes[1].ParentID != copied.Nodes[0].ID ||
		copied.Links[0].PredecessorID != copied.Nodes[1].ID || *copied.Nodes[2].EarlyStart != "2026-10-08" {
		t.Fatalf("copy must remap ids and keep dates: %+v %+v", copied.Nodes, copied.Links)
	}
	if _, err := s.ReplaceWBS(ctx, "PROJ-PLN", baseline.ID, 1, nodes, deps); ruleCode(err) != domain.CodePlanLocked {
		t.Fatalf("replace on baseline: %v", err)
	}
	if _, err := s.SchedulePlan(ctx, "PROJ-PLN", baseline.ID); ruleCode(err) != domain.CodePlanLocked {
		t.Fatalf("schedule on baseline: %v", err)
	}
	if _, _, err := s.CreatePlanVersion(ctx, "PROJ-PLN", NewPlan{Kind: domain.KindSimulation, CopyFromVersionID: baseline.ID}, "plan-4"); err != nil {
		t.Fatalf("simulation from baseline: %v", err)
	}
	plans, err := s.ListPlanVersions(ctx, "PROJ-PLN")
	if err != nil || len(plans) != 3 || plans[2].Kind != domain.KindSimulation {
		t.Fatalf("list: %+v %v", plans, err)
	}
	if _, err := s.ListPlanVersions(ctx, "PROJ-NONE"); problemCode(err) != CodeNotFound {
		t.Fatalf("plans of a missing project: %v", err)
	}
	// A plan id of another project is not found under this one.
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-PLN2"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadWBS(ctx, "PROJ-PLN2", working.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("plan under the wrong project: %v", err)
	}
}

func assertScheduledEvent(t *testing.T, s *Postgres, ctx context.Context, planID string, res ScheduleResult) {
	t.Helper()
	var evt struct {
		Type string `json:"type"`
		Data struct {
			ProjectID     string   `json:"projectId"`
			PlanVersionID string   `json:"planVersionId"`
			CriticalPath  []string `json:"criticalPath"`
			FinishDate    string   `json:"finishDate"`
		} `json:"data"`
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var payload []byte
		if err := tx.QueryRow(context.Background(), `select payload from outbox where aggregate_type = 'plan_version' and aggregate_id = $1`, planID).Scan(&payload); err != nil {
			return err
		}
		return json.Unmarshal(payload, &evt)
	})
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != "dev.passage.projects.wbs.scheduled.v1" || evt.Data.ProjectID != "PROJ-PLN" || evt.Data.PlanVersionID != planID ||
		evt.Data.FinishDate != res.FinishDate || fmt.Sprint(evt.Data.CriticalPath) != fmt.Sprint(res.CriticalPath) {
		t.Fatalf("scheduled event = %+v", evt)
	}
}

func TestPlans_ConcurrentWorkingCreatesOne(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(15)
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-RACEP"), ""); err != nil {
		t.Fatal(err)
	}
	const n = 6
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := s.CreatePlanVersion(ctx, "PROJ-RACEP", NewPlan{Kind: domain.KindWorking}, fmt.Sprintf("race-%d", i))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	ok, refused := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case ruleCode(err) == domain.CodePlanKind:
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || refused != n-1 {
		t.Fatalf("%d created and %d refused, want 1 and %d", ok, refused, n-1)
	}
}

func TestPlans_LegalEntityScopeHidesPlansAndNodes(t *testing.T) {
	s, _ := setup(t)
	all := tenantCtx(16)
	p := sampleProject("PROJ-LEP")
	if _, _, err := s.CreateProject(all, p, ""); err != nil {
		t.Fatal(err)
	}
	plan, _, err := s.CreatePlanVersion(all, p.ID, NewPlan{Kind: domain.KindWorking}, "le-1")
	if err != nil {
		t.Fatal(err)
	}
	nodes, deps := wbsInput()
	if _, err := s.ReplaceWBS(all, p.ID, plan.ID, 1, nodes, deps); err != nil {
		t.Fatal(err)
	}
	elsewhere := leCtx(16, "99999999-0000-4000-8000-000000000009")
	if _, err := s.ListPlanVersions(elsewhere, p.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("plans across legal entities: %v", err)
	}
	if _, err := s.LoadWBS(elsewhere, p.ID, plan.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("wbs across legal entities: %v", err)
	}
	var visible int
	err = dbx.WithTenantTx(elsewhere, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select (select count(*) from wbs_node) + (select count(*) from wbs_dependency)`).Scan(&visible)
	})
	if err != nil || visible != 0 {
		t.Fatalf("nodes and dependencies visible across legal entities: %d %v", visible, err)
	}
	if mine, err := s.LoadWBS(leCtx(16, p.LegalEntityID), p.ID, plan.ID); err != nil || len(mine.Nodes) != 3 {
		t.Fatalf("own legal entity: %+v %v", mine, err)
	}
}
