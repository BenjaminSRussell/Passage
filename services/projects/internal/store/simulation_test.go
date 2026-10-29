package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// ac4115WBS is project 1 with tasks 1.1 → 1.2 → 1.3 → 1.4.
func ac4115WBS() ([]domain.WBSNodeInput, []domain.WBSDependencyInput) {
	one := "1"
	nodes := []domain.WBSNodeInput{
		{WBSCode: "1", Name: "Project"},
		{WBSCode: "1.1", ParentWBSCode: &one, Name: "Design", DurationDays: 5},
		{WBSCode: "1.2", ParentWBSCode: &one, Name: "Build", DurationDays: 10},
		{WBSCode: "1.3", ParentWBSCode: &one, Name: "Test", DurationDays: 4},
		{WBSCode: "1.4", ParentWBSCode: &one, Name: "Handover", DurationDays: 1},
	}
	deps := []domain.WBSDependencyInput{
		{PredecessorWBSCode: "1.1", SuccessorWBSCode: "1.2", Type: domain.DepFS},
		{PredecessorWBSCode: "1.2", SuccessorWBSCode: "1.3", Type: domain.DepFS},
		{PredecessorWBSCode: "1.3", SuccessorWBSCode: "1.4", Type: domain.DepFS},
	}
	domain.NormalizeWBS(nodes)
	return nodes, deps
}

func nodeByCode(w WBS, code string) domain.WBSNode {
	for _, n := range w.Nodes {
		if n.WBSCode == code {
			return n
		}
	}
	return domain.WBSNode{}
}

// TestTransfer_AC4115 runs AC-41.15 end to end: a SIMULATION copied from
// WORKING; 1.2 is then moved in WORKING (and changed in the simulation) and
// 1.4 changed only in the simulation. The transfer reports 1.2 as a conflict
// and changes nothing until it is resolved; then 1.4 is applied, 1.2 keeps
// WORKING's move, and the simulation is locked.
func TestTransfer_AC4115(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(15)
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-SIM"), ""); err != nil {
		t.Fatal(err)
	}
	working, _, err := s.CreatePlanVersion(ctx, "PROJ-SIM", NewPlan{Kind: domain.KindWorking}, "sim-w")
	if err != nil {
		t.Fatal(err)
	}
	nodes, deps := ac4115WBS()
	if _, err := s.ReplaceWBS(ctx, "PROJ-SIM", working.ID, 1, nodes, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SchedulePlan(ctx, "PROJ-SIM", working.ID); err != nil {
		t.Fatal(err)
	}
	sim, _, err := s.CreatePlanVersion(ctx, "PROJ-SIM", NewPlan{Kind: domain.KindSimulation, CopyFromVersionID: working.ID}, "sim-s")
	if err != nil {
		t.Fatal(err)
	}

	// An untouched simulation transfers nothing.
	pre, err := s.PreviewTransfer(ctx, "PROJ-SIM", sim.ID)
	if err != nil || len(pre.Plan.Applied) != 0 || len(pre.Plan.Conflicts) != 0 || pre.Working.ID != working.ID {
		t.Fatalf("untouched preview: %+v %v", pre, err)
	}

	wk := append([]domain.WBSNodeInput(nil), nodes...)
	wk[2].ConstraintType, wk[2].ConstraintDate = domain.ConstraintSNET, strPtr("2026-11-09")
	w2, err := s.ReplaceWBS(ctx, "PROJ-SIM", working.ID, 2, wk, deps)
	if err != nil {
		t.Fatal(err)
	}
	sm := append([]domain.WBSNodeInput(nil), nodes...)
	sm[2].DurationDays, sm[4].DurationDays = 14, 3
	if _, err := s.ReplaceWBS(ctx, "PROJ-SIM", sim.ID, 1, sm, deps); err != nil {
		t.Fatal(err)
	}

	pre, err = s.PreviewTransfer(ctx, "PROJ-SIM", sim.ID)
	if err != nil || len(pre.Plan.Applied) != 1 || pre.Plan.Applied[0].WBSCode != "1.4" ||
		len(pre.Plan.Conflicts) != 1 || pre.Plan.Conflicts[0].WBSCode != "1.2" || pre.Simulation.Version != 2 {
		t.Fatalf("preview: %+v %v", pre, err)
	}
	if _, err := s.TransferSimulation(ctx, "PROJ-SIM", sim.ID, 2, w2.Plan.Version, nil); ruleCode(err) != domain.CodeSimConflict {
		t.Fatalf("unresolved transfer: %v", err)
	}
	if got, _ := s.LoadWBS(ctx, "PROJ-SIM", working.ID); got.Plan.Version != w2.Plan.Version || nodeByCode(got, "1.4").DurationDays != 1 {
		t.Fatalf("a refused transfer changed WORKING: %+v", got.Plan)
	}
	if _, err := s.TransferSimulation(ctx, "PROJ-SIM", sim.ID, 1, 0, nil); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale simulation version: %v", err)
	}
	if _, err := s.TransferSimulation(ctx, "PROJ-SIM", sim.ID, 2, w2.Plan.Version-1, map[string]string{"1.2": domain.ResolveKeepWorking}); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale working version: %v", err)
	}

	out, err := s.TransferSimulation(ctx, "PROJ-SIM", sim.ID, 2, w2.Plan.Version, map[string]string{"1.2": domain.ResolveKeepWorking})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Simulation.Locked || out.Simulation.TransferredAt == nil || out.Simulation.Version != 3 || out.Working.Version != w2.Plan.Version+1 {
		t.Fatalf("transfer result: %+v", out)
	}
	got, err := s.LoadWBS(ctx, "PROJ-SIM", working.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := nodeByCode(got, "1.4"); n.DurationDays != 3 || n.EarlyStart != nil {
		t.Fatalf("1.4 after transfer: %+v (applied, dates cleared)", n)
	}
	if n := nodeByCode(got, "1.2"); n.DurationDays != 10 || n.ConstraintType != domain.ConstraintSNET {
		t.Fatalf("1.2 after transfer: %+v (WORKING kept)", n)
	}
	if len(got.Nodes) != 5 || len(got.Links) != 3 {
		t.Fatalf("WORKING after transfer: %d nodes, %d links", len(got.Nodes), len(got.Links))
	}

	// Locked: no second transfer, no edits.
	if _, err := s.TransferSimulation(ctx, "PROJ-SIM", sim.ID, 3, 0, nil); ruleCode(err) != domain.CodePlanLocked {
		t.Fatalf("second transfer: %v", err)
	}
	if _, err := s.PreviewTransfer(ctx, "PROJ-SIM", sim.ID); ruleCode(err) != domain.CodePlanLocked {
		t.Fatalf("preview of a transferred simulation: %v", err)
	}
	if _, err := s.ReplaceWBS(ctx, "PROJ-SIM", sim.ID, 3, sm, deps); ruleCode(err) != domain.CodePlanLocked {
		t.Fatalf("edit after transfer: %v", err)
	}
	// Only a SIMULATION transfers; other tenants and legal entities see nothing.
	if _, err := s.PreviewTransfer(ctx, "PROJ-SIM", working.ID); ruleCode(err) != domain.CodePlanKind {
		t.Fatalf("transfer of WORKING: %v", err)
	}
	if _, err := s.PreviewTransfer(tenantCtx(16), "PROJ-SIM", sim.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if _, err := s.PreviewTransfer(leCtx(15, ids.New()), "PROJ-SIM", sim.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("other legal entity: %v", err)
	}
}

// A simulation copied from the BASELINE diffs against it: WORKING's changes
// since the baseline are kept unless the simulation changed the same node.
func TestTransfer_FromBaselineAndWithoutAncestor(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(17)
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-SIMB"), ""); err != nil {
		t.Fatal(err)
	}
	working, _, _ := s.CreatePlanVersion(ctx, "PROJ-SIMB", NewPlan{Kind: domain.KindWorking}, "simb-w")
	nodes, deps := ac4115WBS()
	if _, err := s.ReplaceWBS(ctx, "PROJ-SIMB", working.ID, 1, nodes, deps); err != nil {
		t.Fatal(err)
	}
	baseline, _, err := s.CreatePlanVersion(ctx, "PROJ-SIMB", NewPlan{Kind: domain.KindBaseline, CopyFromVersionID: working.ID}, "simb-b")
	if err != nil {
		t.Fatal(err)
	}
	sim, _, err := s.CreatePlanVersion(ctx, "PROJ-SIMB", NewPlan{Kind: domain.KindSimulation, CopyFromVersionID: baseline.ID}, "simb-s")
	if err != nil || sim.Locked {
		t.Fatalf("simulation from baseline: %+v %v", sim, err)
	}
	wk := append([]domain.WBSNodeInput(nil), nodes...)
	wk[1].Name = "Design (working)"
	if _, err := s.ReplaceWBS(ctx, "PROJ-SIMB", working.ID, 2, wk, deps); err != nil {
		t.Fatal(err)
	}
	sm := append([]domain.WBSNodeInput(nil), nodes[:4]...) // remove 1.4
	if _, err := s.ReplaceWBS(ctx, "PROJ-SIMB", sim.ID, 1, sm, deps[:2]); err != nil {
		t.Fatal(err)
	}
	out, err := s.TransferSimulation(ctx, "PROJ-SIMB", sim.ID, 2, 0, nil)
	if err != nil || len(out.Plan.Applied) != 1 || out.Plan.Applied[0].Change != domain.ChangeRemoved {
		t.Fatalf("transfer: %+v %v", out.Plan, err)
	}
	got, _ := s.LoadWBS(ctx, "PROJ-SIMB", working.ID)
	if len(got.Nodes) != 4 || len(got.Links) != 2 || nodeByCode(got, "1.1").Name != "Design (working)" {
		t.Fatalf("WORKING after transfer: %+v", got.Nodes)
	}

	// A simulation copied before ancestors were recorded cannot be transferred.
	old, _, err := s.CreatePlanVersion(ctx, "PROJ-SIMB", NewPlan{Kind: domain.KindSimulation, CopyFromVersionID: working.ID}, "simb-old")
	if err != nil {
		t.Fatal(err)
	}
	// Clearing the ancestor stands for a simulation copied before migration 0008.
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update plan_version set base_fingerprints = null where id = $1::uuid`, old.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewTransfer(ctx, "PROJ-SIMB", old.ID); ruleCode(err) != domain.CodePlanKind {
		t.Fatalf("simulation without ancestor: %v", err)
	}
}

func TestPlans_SimulationCap(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(18)
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-CAP"), ""); err != nil {
		t.Fatal(err)
	}
	working, _, _ := s.CreatePlanVersion(ctx, "PROJ-CAP", NewPlan{Kind: domain.KindWorking}, "cap-w")
	for i := 0; i < domain.MaxSimulations; i++ {
		if _, _, err := s.CreatePlanVersion(ctx, "PROJ-CAP", NewPlan{Kind: domain.KindSimulation, CopyFromVersionID: working.ID}, ids.New()); err != nil {
			t.Fatalf("simulation %d: %v", i+1, err)
		}
	}
	if _, _, err := s.CreatePlanVersion(ctx, "PROJ-CAP", NewPlan{Kind: domain.KindSimulation, CopyFromVersionID: working.ID}, ids.New()); ruleCode(err) != domain.CodePlanKind {
		t.Fatalf("simulation %d: %v", domain.MaxSimulations+1, err)
	}
}

// The ancestor and transfer columns are checked by the table: only a
// SIMULATION has an ancestor, and only a locked one is transferred.
func TestPlans_TransferColumnsAreChecked(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(19)
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-CHK"), ""); err != nil {
		t.Fatal(err)
	}
	working, _, _ := s.CreatePlanVersion(ctx, "PROJ-CHK", NewPlan{Kind: domain.KindWorking}, "chk-w")
	for _, stmt := range []string{
		`update plan_version set base_fingerprints = '{}' where id = $1::uuid`,
		`update plan_version set transferred_at = now() where id = $1::uuid`,
	} {
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), stmt, working.ID)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("%q = %v; want check_violation", stmt, err)
		}
	}
}
