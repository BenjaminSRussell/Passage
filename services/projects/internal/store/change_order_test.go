package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// userCtx is a principal of tenant n named user, unrestricted by legal entity.
func userCtx(n int, user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: user, Duties: testkit.ScopeDuties("")})
}

// seedBaseline creates a project with contract value 500,000.00, a WORKING plan
// with WBS 1 (summary), 1.1 and 1.3 (1.1 FS 1.3), and a BASELINE copy. It
// returns the working node id of 1.3 and the baseline.
func seedBaseline(t *testing.T, s *Postgres, ctx context.Context, id string) (string, domain.PlanVersion) {
	t.Helper()
	p := sampleProject(id)
	p.ContractValue = "500000"
	if _, _, err := s.CreateProject(ctx, p, ""); err != nil {
		t.Fatal(err)
	}
	working, _, err := s.CreatePlanVersion(ctx, id, NewPlan{Kind: domain.KindWorking}, id+"-w")
	if err != nil {
		t.Fatal(err)
	}
	one := "1"
	nodes := []domain.WBSNodeInput{
		{WBSCode: "1", Name: "Build"},
		{WBSCode: "1.1", ParentWBSCode: &one, Name: "Design", DurationDays: 5, EffortHours: strPtr("80.00")},
		{WBSCode: "1.3", ParentWBSCode: &one, Name: "Install", DurationDays: 4, EffortHours: strPtr("120.00")},
	}
	domain.NormalizeWBS(nodes)
	deps := []domain.WBSDependencyInput{{PredecessorWBSCode: "1.1", SuccessorWBSCode: "1.3", Type: domain.DepFS}}
	w, err := s.ReplaceWBS(ctx, id, working.ID, 1, nodes, deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SchedulePlan(ctx, id, working.ID); err != nil {
		t.Fatal(err)
	}
	baseline, _, err := s.CreatePlanVersion(ctx, id, NewPlan{Kind: domain.KindBaseline, CopyFromVersionID: working.ID}, id+"-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range w.Nodes {
		if n.WBSCode == "1.3" {
			return n.ID, baseline
		}
	}
	t.Fatal("no node 1.3")
	return "", baseline
}

func scope(projectID, nodeID string) domain.ChangeOrder {
	return domain.ChangeOrder{ProjectID: projectID, Type: domain.COScopeChange, Title: "Extra panel", Lines: []domain.ChangeOrderLine{
		{WBSNodeID: &nodeID, CostImpact: "30000", RevenueImpact: "50000", EffortHoursDelta: "40", ScheduleDaysDelta: 2},
	}}
}

// outboxEvent reads the one event of an aggregate through the owner-less app
// pool (RLS applies) and decodes its type and data.
func outboxEvents(t *testing.T, s *Postgres, ctx context.Context, aggregateType, aggregateID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `select payload from outbox where aggregate_type = $1 and aggregate_id = $2 order by created_at, id`, aggregateType, aggregateID)
		if err != nil {
			return err
		}
		payloads, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
		if err != nil {
			return err
		}
		for _, p := range payloads {
			var evt map[string]any
			if err := json.Unmarshal(p, &evt); err != nil {
				return err
			}
			out = append(out, evt)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestChangeOrders_ApplyAC4112 runs PRD AC-41.12 against Postgres: contract
// value +50,000.00, budget delta 30,000.00 on the event, a new BASELINE that
// supersedes the old one and carries the WBS 1.3 deltas (rescheduled), and a
// second apply that returns PROJ-CO-001.
func TestChangeOrders_ApplyAC4112(t *testing.T) {
	s, _ := setup(t)
	pm, ctl := userCtx(17, "pm-1"), userCtx(17, "controller-1")
	node, oldBase := seedBaseline(t, s, pm, "PROJ-CO")

	co, created, err := s.CreateChangeOrder(pm, scope("PROJ-CO", node), "co-1")
	if err != nil || !created || co.Number != "CO-0001" || co.CreatedBy != "pm-1" || co.Lines[0].WBSCode == nil || *co.Lines[0].WBSCode != "1.3" ||
		co.Lines[0].RevenueImpact != "50000.00" || co.RevenueImpact != "50000.00" || co.CostImpact != "30000.00" {
		t.Fatalf("create: %+v created=%v %v", co, created, err)
	}
	if co, err = s.DecideChangeOrder(pm, co.ID, 1, domain.COStatusSubmitted, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideChangeOrder(pm, co.ID, co.Version, domain.COStatusApproved, nil); ruleCode(err) != domain.CodeSelfApproval {
		t.Fatalf("self-approval: %v", err)
	}
	if _, err := s.DecideChangeOrder(ctl, co.ID, 9, domain.COStatusApproved, nil); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale approve: %v", err)
	}
	if co, err = s.DecideChangeOrder(ctl, co.ID, co.Version, domain.COStatusApproved, nil); err != nil || co.DecidedBy == nil || *co.DecidedBy != "controller-1" {
		t.Fatalf("approve: %+v %v", co, err)
	}

	out, err := s.ApplyChangeOrder(ctl, co.ID, co.Version)
	if err != nil {
		t.Fatal(err)
	}
	if out.Effect.ContractValueDelta != "50000.00" || out.Effect.BudgetDelta != "30000.00" || out.Project.ContractValue != "550000.00" || out.Project.Version != 2 {
		t.Fatalf("effect %+v project %+v", out.Effect, out.Project)
	}
	if out.ChangeOrder.Status != domain.COStatusApplied || out.ChangeOrder.AppliedAt == nil || *out.ChangeOrder.AppliedBaselineVersionID != out.Baseline.ID ||
		*out.ChangeOrder.AppliedBy != "controller-1" {
		t.Fatalf("applied change order: %+v", out.ChangeOrder)
	}
	if got, _ := s.GetProject(pm, "PROJ-CO"); got.ContractValue != "550000.00" {
		t.Fatalf("stored contract value: %s", got.ContractValue)
	}

	plans, err := s.ListPlanVersions(pm, "PROJ-CO")
	if err != nil || len(plans) != 3 {
		t.Fatalf("plans: %+v %v", plans, err)
	}
	old := plans[1]
	if old.ID != oldBase.ID || old.SupersededByVersionID == nil || *old.SupersededByVersionID != out.Baseline.ID {
		t.Fatalf("old baseline: %+v", old)
	}
	if nb := plans[2]; nb.ID != out.Baseline.ID || nb.Kind != domain.KindBaseline || !nb.Locked || *nb.CopiedFromVersionID != oldBase.ID || nb.Sequence != 3 {
		t.Fatalf("new baseline: %+v", nb)
	}
	before, _ := s.LoadWBS(pm, "PROJ-CO", oldBase.ID)
	after, _ := s.LoadWBS(pm, "PROJ-CO", out.Baseline.ID)
	if len(after.Nodes) != 3 || len(after.Links) != 1 {
		t.Fatalf("new baseline WBS: %+v", after)
	}
	for i, n := range after.Nodes {
		b := before.Nodes[i]
		if n.ID == b.ID {
			t.Fatal("the new baseline shares node ids with the old one")
		}
		if n.WBSCode == "1.3" {
			// 1.1 runs 2026-10-01..07 (5 working days); 1.3 now takes 6 days from Thursday 10-08.
			if n.DurationDays != 6 || *n.EffortHours != "160.00" || *n.EarlyStart != "2026-10-08" || *n.EarlyFinish != "2026-10-15" {
				t.Fatalf("1.3 in new baseline: %+v start %s finish %s", n, *n.EarlyStart, *n.EarlyFinish)
			}
			if b.DurationDays != 4 || *b.EffortHours != "120.00" {
				t.Fatalf("old baseline changed: %+v", b)
			}
		}
	}

	if _, err := s.ApplyChangeOrder(ctl, co.ID, co.Version); ruleCode(err) != domain.CodeChangeOrderApplied {
		t.Fatalf("second apply: %v", err)
	}
	if _, _, err := s.CreatePlanVersion(pm, "PROJ-CO", NewPlan{Kind: domain.KindBaseline, CopyFromVersionID: plans[0].ID}, "co-b2"); ruleCode(err) != domain.CodePlanKind {
		t.Fatalf("a second current baseline: %v", err)
	}

	events := outboxEvents(t, s, pm, "change_order", co.ID)
	if len(events) != 1 || events[0]["type"] != "dev.passage.projects.changeorder.applied.v1" {
		t.Fatalf("change order events: %+v", events)
	}
	data := events[0]["data"].(map[string]any)
	if data["contractValueDelta"] != "50000.00" || data["budgetDelta"] != "30000.00" || data["newBaselineVersionId"] != out.Baseline.ID ||
		data["projectId"] != "PROJ-CO" || data["currency"] != "USD" || events[0]["actor"] != "controller-1" {
		t.Fatalf("applied event data: %+v", events[0])
	}
	projectEvents := outboxEvents(t, s, pm, "project", "PROJ-CO")
	last := projectEvents[len(projectEvents)-1]
	if last["type"] != "dev.passage.projects.project.updated.v1" || fmt.Sprint(last["data"].(map[string]any)["changedFields"]) != "[contractValue]" {
		t.Fatalf("project updated event: %+v", last)
	}
}

func TestChangeOrders_ApplyRollsBackOnRefusal(t *testing.T) {
	s, _ := setup(t)
	pm, ctl := userCtx(18, "pm-1"), userCtx(18, "controller-1")
	node, _ := seedBaseline(t, s, pm, "PROJ-ROLL")
	in := scope("PROJ-ROLL", node)
	in.Lines[0].EffortHoursDelta = "-500" // more than the 120 hours of 1.3
	co, _, err := s.CreateChangeOrder(pm, in, "roll-1")
	if err != nil {
		t.Fatal(err)
	}
	co, _ = s.DecideChangeOrder(pm, co.ID, 1, domain.COStatusSubmitted, nil)
	co, _ = s.DecideChangeOrder(ctl, co.ID, co.Version, domain.COStatusApproved, nil)
	if _, err := s.ApplyChangeOrder(ctl, co.ID, co.Version); ruleCode(err) != domain.CodeApplyImpact {
		t.Fatalf("apply negative effort: %v", err)
	}
	p, _ := s.GetProject(pm, "PROJ-ROLL")
	plans, _ := s.ListPlanVersions(pm, "PROJ-ROLL")
	again, _ := s.GetChangeOrder(pm, co.ID)
	if p.ContractValue != "500000.00" || p.Version != 1 || len(plans) != 2 || plans[1].SupersededByVersionID != nil || again.Status != domain.COStatusApproved {
		t.Fatalf("refused apply left changes: project %+v plans %d change order %s", p, len(plans), again.Status)
	}

	// Without a baseline there is nothing to copy.
	if _, _, err := s.CreateProject(pm, sampleProject("PROJ-NOB"), ""); err != nil {
		t.Fatal(err)
	}
	nob, _, _ := s.CreateChangeOrder(pm, domain.ChangeOrder{ProjectID: "PROJ-NOB", Type: domain.COInternal, Title: "Rework",
		Lines: []domain.ChangeOrderLine{{CostImpact: "10", RevenueImpact: "0", EffortHoursDelta: "0"}}}, "nob-1")
	nob, _ = s.DecideChangeOrder(pm, nob.ID, 1, domain.COStatusSubmitted, nil)
	nob, _ = s.DecideChangeOrder(ctl, nob.ID, nob.Version, domain.COStatusApproved, nil)
	if _, err := s.ApplyChangeOrder(ctl, nob.ID, nob.Version); ruleCode(err) != domain.CodeNoBaseline {
		t.Fatalf("apply without baseline: %v", err)
	}
}

func TestChangeOrders_CreateKeysNodesAndConcurrency(t *testing.T) {
	s, _ := setup(t)
	pm := userCtx(19, "pm-1")
	node, base := seedBaseline(t, s, pm, "PROJ-KEY")

	first, _, err := s.CreateChangeOrder(pm, scope("PROJ-KEY", node), "k-1")
	if err != nil {
		t.Fatal(err)
	}
	replay, created, err := s.CreateChangeOrder(pm, scope("PROJ-KEY", node), "k-1")
	if err != nil || created || replay.ID != first.ID || len(replay.Lines) != 1 {
		t.Fatalf("replay: %+v created=%v %v", replay, created, err)
	}
	other := scope("PROJ-KEY", node)
	other.Lines[0].CostImpact = "30000.01"
	if _, _, err := s.CreateChangeOrder(pm, other, "k-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("reused key: %v", err)
	}
	if _, _, err := s.CreateChangeOrder(pm, scope("PROJ-KEY", ids.New()), "k-2"); problemCode(err) != CodeValidation {
		t.Fatalf("unknown node: %v", err)
	}
	// A node of the baseline is a node of the project too.
	baseWBS, _ := s.LoadWBS(pm, "PROJ-KEY", base.ID)
	if co, _, err := s.CreateChangeOrder(pm, scope("PROJ-KEY", baseWBS.Nodes[0].ID), "k-3"); err != nil || *co.Lines[0].WBSCode != "1" || co.Number != "CO-0002" {
		t.Fatalf("baseline node: %+v %v", co, err)
	}
	if _, _, err := s.CreateChangeOrder(pm, scope("NOPE", node), "k-4"); problemCode(err) != CodeNotFound {
		t.Fatalf("unknown project: %v", err)
	}

	const n = 6
	var wg sync.WaitGroup
	got := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			co, _, err := s.CreateChangeOrder(pm, scope("PROJ-KEY", node), "race")
			if err != nil {
				t.Errorf("concurrent create: %v", err)
			}
			got <- co.ID
		}()
	}
	wg.Wait()
	close(got)
	seen := map[string]bool{}
	for id := range got {
		seen[id] = true
	}
	list, _ := s.ListChangeOrders(pm, ChangeOrderFilter{ProjectID: "PROJ-KEY"})
	if len(seen) != 1 || len(list) != 3 || list[2].Number != "CO-0003" {
		t.Fatalf("concurrent same-key creates made %d change orders (list %d)", len(seen), len(list))
	}
	if drafts, _ := s.ListChangeOrders(pm, ChangeOrderFilter{ProjectID: "PROJ-KEY", Status: domain.COStatusApproved}); len(drafts) != 0 {
		t.Fatalf("status filter: %d", len(drafts))
	}
}

func TestChangeOrdersAndRisks_TenantAndLegalEntityScope(t *testing.T) {
	s, _ := setup(t)
	pm := userCtx(20, "pm-1")
	node, _ := seedBaseline(t, s, pm, "PROJ-SCO")
	co, _, err := s.CreateChangeOrder(pm, scope("PROJ-SCO", node), "sc-1")
	if err != nil {
		t.Fatal(err)
	}
	risk, _, err := s.CreateRisk(pm, domain.RiskIssue{ProjectID: "PROJ-SCO", Kind: domain.KindRisk, Title: "Steel", Probability: 2, Impact: 2}, "sr-1")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetProject(pm, "PROJ-SCO")
	for name, ctx := range map[string]context.Context{"other tenant": userCtx(1, "x"), "other legal entity": leCtx(20, "99999999-0000-4000-8000-000000000009")} {
		if _, err := s.GetChangeOrder(ctx, co.ID); problemCode(err) != CodeNotFound {
			t.Errorf("%s reads the change order: %v", name, err)
		}
		if _, err := s.ListChangeOrders(ctx, ChangeOrderFilter{ProjectID: "PROJ-SCO"}); problemCode(err) != CodeNotFound {
			t.Errorf("%s lists change orders: %v", name, err)
		}
		if _, err := s.ListRisks(ctx, RiskFilter{ProjectID: "PROJ-SCO"}); problemCode(err) != CodeNotFound {
			t.Errorf("%s lists risks: %v", name, err)
		}
		risk.Status = domain.RiskClosed
		if _, err := s.UpdateRisk(ctx, 1, risk); problemCode(err) != CodeNotFound {
			t.Errorf("%s updates the risk: %v", name, err)
		}
		var rows int
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `select (select count(*) from project_change_order) + (select count(*) from project_change_order_line)
				+ (select count(*) from project_risk_issue)`).Scan(&rows)
		})
		if err != nil || rows != 0 {
			t.Errorf("%s sees %d rows (%v)", name, rows, err)
		}
	}
	if got, err := s.GetChangeOrder(leCtx(20, p.LegalEntityID), co.ID); err != nil || len(got.Lines) != 1 {
		t.Fatalf("own legal entity: %+v %v", got, err)
	}
}

func TestRisks_RegisterLifecycle(t *testing.T) {
	s, _ := setup(t)
	ctx := userCtx(13, "pm-1")
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-RSK"), ""); err != nil {
		t.Fatal(err)
	}
	due := "2026-12-01"
	low := domain.RiskIssue{ProjectID: "PROJ-RSK", Kind: domain.KindRisk, Title: "Steel", Probability: 2, Impact: 3, DueDate: &due, Mitigation: "Second supplier"}
	r, created, err := s.CreateRisk(ctx, low, "r-1")
	if err != nil || !created || r.Status != domain.RiskOpen || r.Score != 6 || *r.DueDate != due {
		t.Fatalf("create: %+v %v", r, err)
	}
	if again, created, err := s.CreateRisk(ctx, low, "r-1"); err != nil || created || again.ID != r.ID {
		t.Fatalf("replay: %v %v", created, err)
	}
	other := low
	other.Impact = 4
	if _, _, err := s.CreateRisk(ctx, other, "r-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("reused key: %v", err)
	}
	high, _, _ := s.CreateRisk(ctx, domain.RiskIssue{ProjectID: "PROJ-RSK", Kind: domain.KindIssue, Title: "Crane", Probability: 5, Impact: 5}, "r-2")
	list, err := s.ListRisks(ctx, RiskFilter{ProjectID: "PROJ-RSK"})
	if err != nil || len(list) != 2 || list[0].ID != high.ID {
		t.Fatalf("list order: %+v %v", list, err)
	}
	if issues, _ := s.ListRisks(ctx, RiskFilter{ProjectID: "PROJ-RSK", Kind: domain.KindIssue}); len(issues) != 1 {
		t.Fatalf("kind filter: %d", len(issues))
	}

	upd := r
	upd.Status, upd.Probability, upd.DueDate = domain.RiskMitigated, 1, nil
	if _, err := s.UpdateRisk(ctx, 5, upd); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale update: %v", err)
	}
	got, err := s.UpdateRisk(ctx, 1, upd)
	if err != nil || got.Version != 2 || got.Score != 3 || got.DueDate != nil || got.Status != domain.RiskMitigated {
		t.Fatalf("update: %+v %v", got, err)
	}
	upd.Status = domain.RiskClosed
	if got, err = s.UpdateRisk(ctx, 2, upd); err != nil || got.Status != domain.RiskClosed {
		t.Fatalf("close: %+v %v", got, err)
	}
	upd.Status = domain.RiskOpen
	if _, err := s.UpdateRisk(ctx, 3, upd); ruleCode(err) != domain.CodeRiskStatus {
		t.Fatalf("reopen closed: %v", err)
	}
	upd.ProjectID = "PROJ-OTHER"
	if _, err := s.UpdateRisk(ctx, 3, upd); problemCode(err) != CodeNotFound {
		t.Fatalf("risk under another project: %v", err)
	}
}
