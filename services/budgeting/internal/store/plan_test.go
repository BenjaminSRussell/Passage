package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/migrations"
)

const planTenant = 39

func asUser(user string) context.Context {
	p := tenant.Principal{TenantID: testkit.TenantID(planTenant), UserID: user, Duties: map[string]struct{}{}}
	return tenant.With(context.Background(), p)
}

type planFixture struct {
	st   *Postgres
	rule Rule
	view PlanView
}

func newPlanFixture(t *testing.T) planFixture {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	_, raw, err := st.CreateRule(asUser("setup"), ids.New(), "h", NewRule{LegalEntityID: ids.New(), Code: "PLAN", WarnPct: d("0"), BlockPct: d("0"),
		Currency: "USD", EffectiveFrom: "2027-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	var rule Rule
	if err := json.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	f := planFixture{st: st, rule: rule}
	f.view = decodePlanView(t, mustCommand(t)(st.CreatePlan(asUser("planner"), ids.New(), "h", NewPlan{LegalEntityID: rule.LegalEntityID, RuleVersionID: rule.ID, Name: "FY27 operating", FiscalYear: 2027})))
	return f
}

func mustCommand(t *testing.T) func(int, []byte, error) []byte {
	return func(status int, raw []byte, err error) []byte {
		t.Helper()
		if err != nil || status >= http.StatusBadRequest {
			t.Fatalf("command: status %d err %v body %s", status, err, raw)
		}
		return raw
	}
}

func decodePlanView(t *testing.T, raw []byte) PlanView {
	t.Helper()
	var v PlanView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (f planFixture) scenario(t *testing.T, name string) Scenario {
	t.Helper()
	v, err := f.st.GetPlan(asUser("planner"), f.view.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v.Scenarios {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no scenario %q", name)
	return Scenario{}
}

func (f planFixture) plan(t *testing.T) PlanView {
	t.Helper()
	v, err := f.st.GetPlan(asUser("planner"), f.view.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f planFixture) write(t *testing.T, sc Scenario, cells ...domain.PlanCell) GridWritten {
	t.Helper()
	var w GridWritten
	if err := json.Unmarshal(mustCommand(t)(f.st.UpsertCells(asUser("planner"), ids.New(), "h", f.view.ID, sc.ID, sc.Version, cells)), &w); err != nil {
		t.Fatal(err)
	}
	return w
}

func cell(account, cc, period, amount string) domain.PlanCell {
	return domain.PlanCell{Account: account, CostCenter: cc, Period: period, Amount: d(amount)}
}

func (f planFixture) owners(t *testing.T, owners ...StageOwnerInput) {
	t.Helper()
	mustCommand(t)(f.st.SetStageOwners(asUser("planner"), ids.New(), "h", f.view.ID, owners))
}

func (f planFixture) submit(t *testing.T) PlanView {
	t.Helper()
	return decodePlanView(t, mustCommand(t)(f.st.SubmitPlan(asUser("planner"), ids.New(), "h", f.view.ID)))
}

func (f planFixture) signOff(user string, in SignOffRequest) error {
	in.PlanID = f.view.ID
	_, _, err := f.st.SignOff(asUser(user), ids.New(), "h", in)
	return err
}

// P4 scenarios: three scenarios in one plan, the third copied from the first with an adjustment.
func TestPlan_ThreeScenariosWithCopyFrom(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	f.write(t, base, cell("6000", "CC1", "2027-01", "1000"), cell("6000", "CC1", "2027-02", "2000.5"))
	var opt Scenario
	if err := json.Unmarshal(mustCommand(t)(f.st.CreateScenario(asUser("planner"), ids.New(), "h", f.view.ID,
		NewScenario{Name: "Optimistic", Kind: "Optimistic", CopyFromID: base.ID, AdjustmentPct: d("10")})), &opt); err != nil {
		t.Fatal(err)
	}
	mustCommand(t)(f.st.CreateScenario(asUser("planner"), ids.New(), "h", f.view.ID, NewScenario{Name: "Pessimistic", Kind: "Pessimistic"}))
	if !opt.Total.Equal(d("3300.55")) || opt.CopiedFromID != base.ID {
		t.Fatalf("copied scenario total %s from %s, want 3300.55 from %s", opt.Total, opt.CopiedFromID, base.ID)
	}
	if v := f.plan(t); len(v.Scenarios) != 3 {
		t.Fatalf("plan holds %d scenarios, want 3", len(v.Scenarios))
	}
	_, _, err := f.st.CreateScenario(asUser("planner"), ids.New(), "h", f.view.ID, NewScenario{Name: "Optimistic", Kind: "Custom"})
	if !hasCode(err, CodeScenarioDuplicate) {
		t.Fatalf("duplicate scenario name: %v, want %s", err, CodeScenarioDuplicate)
	}
	_, _, err = f.st.CreateScenario(asUser("planner"), ids.New(), "h", f.view.ID, NewScenario{Name: "Other", Kind: "Custom", CopyFromID: ids.New()})
	if !hasCode(err, CodeScenarioNotFound) {
		t.Fatalf("copy from a foreign scenario: %v, want %s", err, CodeScenarioNotFound)
	}
}

func TestPlan_ScenarioLimit(t *testing.T) {
	f := newPlanFixture(t)
	for i := 1; i < domain.MaxScenarios; i++ {
		mustCommand(t)(f.st.CreateScenario(asUser("planner"), ids.New(), "h", f.view.ID, NewScenario{Name: "S" + string(rune('a'+i)), Kind: "Custom"}))
	}
	_, _, err := f.st.CreateScenario(asUser("planner"), ids.New(), "h", f.view.ID, NewScenario{Name: "One too many", Kind: "Custom"})
	if !hasCode(err, CodeScenarioLimit) {
		t.Fatalf("eleventh scenario: %v, want %s", err, CodeScenarioLimit)
	}
}

// P4 grid: bulk upsert under the grid ETag; a stale ETag writes nothing.
func TestPlan_GridUpsertETagConflict(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	first := f.write(t, base, cell("6000", "CC1", "2027-01", "100"), cell("6100", "CC2", "2027-03", "50"))
	if first.Version != base.Version+1 || first.Cells != 2 || !first.Total.Equal(d("150")) {
		t.Fatalf("first write = %+v from version %d", first, base.Version)
	}
	_, _, err := f.st.UpsertCells(asUser("planner"), ids.New(), "h", f.view.ID, base.ID, base.Version, []domain.PlanCell{cell("6000", "CC1", "2027-01", "999")})
	if !hasCode(err, CodeGridStale) {
		t.Fatalf("stale ETag: %v, want %s", err, CodeGridStale)
	}
	g, err := f.st.GetGrid(asUser("planner"), f.view.ID, base.ID, GridFilter{Account: "6000"})
	if err != nil || len(g.Cells) != 1 || !g.Cells[0].Amount.Equal(d("100")) {
		t.Fatalf("grid after the refused write = %+v (%v), want the 100 untouched", g, err)
	}
	second := f.write(t, Scenario{ID: base.ID, Version: first.Version}, cell("6000", "CC1", "2027-01", "120"))
	if second.Version != first.Version+1 || !second.Total.Equal(d("170")) {
		t.Fatalf("second write = %+v, want version %d total 170", second, first.Version+1)
	}
	_, _, err = f.st.UpsertCells(asUser("planner"), ids.New(), "h", f.view.ID, base.ID, second.Version, []domain.PlanCell{cell("6000", "CC1", "2026-12", "1")})
	if err == nil {
		t.Fatal("a period outside the fiscal year was accepted")
	}
}

func TestPlan_GridWriteReplaysByIdempotencyKey(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	cells := []domain.PlanCell{cell("6000", "CC1", "2027-01", "100")}
	_, first, err := f.st.UpsertCells(asUser("planner"), "grid-key", "h", f.view.ID, base.ID, base.Version, cells)
	if err != nil {
		t.Fatal(err)
	}
	_, again, err := f.st.UpsertCells(asUser("planner"), "grid-key", "h", f.view.ID, base.ID, base.Version, cells)
	var a, b GridWritten
	if err := json.Unmarshal(first, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(again, &b); err != nil || a.Version != b.Version || a.ScenarioID != b.ScenarioID || a.Cells != b.Cells || !a.Total.Equal(b.Total) {
		t.Fatalf("replay = %s (%v), want %s", again, err, first)
	}
}

func TestPlan_StageOwnersRejectOnePersonInBothStages(t *testing.T) {
	f := newPlanFixture(t)
	unit := ids.New()
	_, _, err := f.st.SetStageOwners(asUser("planner"), ids.New(), "h", f.view.ID, []StageOwnerInput{
		{Stage: domain.StageReview, OrgUnitID: unit, Owner: "alex"}, {Stage: domain.StageApprove, OrgUnitID: unit, Owner: "alex"}})
	if !hasCode(err, CodePlanSeparation) {
		t.Fatalf("one person in both stages: %v, want %s", err, CodePlanSeparation)
	}
}

func TestPlan_SubmitNeedsOwnersAndAmount(t *testing.T) {
	f := newPlanFixture(t)
	_, _, err := f.st.SubmitPlan(asUser("planner"), ids.New(), "h", f.view.ID)
	if !hasCode(err, CodePlanIncomplete) {
		t.Fatalf("empty plan: %v, want %s", err, CodePlanIncomplete)
	}
}

// P4 stages and approval: draft -> submitted -> reviewed -> approved, by org unit owners, and the approved scenario
// becomes the budget register amounts that budget control reads.
func TestPlan_ApprovalPostsTheScenarioToBudgetControl(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	f.write(t, base, cell("6000", "CC1", "2027-01", "1000"))
	var opt Scenario
	if err := json.Unmarshal(mustCommand(t)(f.st.CreateScenario(asUser("planner"), ids.New(), "h", f.view.ID,
		NewScenario{Name: "Optimistic", Kind: "Optimistic", CopyFromID: base.ID, AdjustmentPct: d("25")})), &opt); err != nil {
		t.Fatal(err)
	}
	f.write(t, opt, cell("6100", "CC2", "2027-02", "400"), cell("6200", "CC2", "2027-03", "0"))
	unitA, unitB := ids.New(), ids.New()
	f.owners(t,
		StageOwnerInput{Stage: domain.StageReview, OrgUnitID: unitA, Owner: "rev-a"},
		StageOwnerInput{Stage: domain.StageReview, OrgUnitID: unitB, Owner: "rev-b"},
		StageOwnerInput{Stage: domain.StageApprove, OrgUnitID: unitA, Owner: "boss"})
	submitted := f.submit(t)
	if submitted.Status != domain.PlanSubmitted || submitted.Submission != 1 || submitted.SubmittedBy != "planner" {
		t.Fatalf("after submit: %s submission %d by %s", submitted.Status, submitted.Submission, submitted.SubmittedBy)
	}
	if err := f.signOff("boss", SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unitA, ScenarioID: opt.ID, Submission: 1}); !hasCode(err, CodeStageNotOpen) {
		t.Fatalf("approve before review: %v, want %s", err, CodeStageNotOpen)
	}
	if err := f.signOff("rev-b", SignOffRequest{Stage: domain.StageReview, OrgUnitID: unitA, Submission: 1}); !hasCode(err, CodeNotStageOwner) {
		t.Fatalf("a reviewer of another unit: %v, want %s", err, CodeNotStageOwner)
	}
	if err := f.signOff("rev-a", SignOffRequest{Stage: domain.StageReview, OrgUnitID: unitA, Submission: 2}); !hasCode(err, CodePlanStale) {
		t.Fatalf("a sign-off for another submission: %v, want %s", err, CodePlanStale)
	}
	if err := f.signOff("rev-a", SignOffRequest{Stage: domain.StageReview, OrgUnitID: unitA, Submission: 1}); err != nil {
		t.Fatal(err)
	}
	if got := f.plan(t).Status; got != domain.PlanSubmitted {
		t.Fatalf("one of two units signed: status %s, want Submitted", got)
	}
	if err := f.signOff("rev-a", SignOffRequest{Stage: domain.StageReview, OrgUnitID: unitA, Submission: 1}); err != nil {
		t.Fatalf("a repeated sign-off must be a no-op: %v", err)
	}
	if err := f.signOff("rev-b", SignOffRequest{Stage: domain.StageReview, OrgUnitID: unitB, Submission: 1}); err != nil {
		t.Fatal(err)
	}
	if got := f.plan(t).Status; got != domain.PlanReviewed {
		t.Fatalf("all units signed: status %s, want Reviewed", got)
	}
	if err := f.signOff("rev-a", SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unitA, ScenarioID: opt.ID, Submission: 1}); !hasCode(err, CodeNotStageOwner) {
		t.Fatalf("a reviewer approving: %v, want %s", err, CodeNotStageOwner)
	}
	if err := f.signOff("boss", SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unitA, ScenarioID: ids.New(), Submission: 1}); !hasCode(err, CodeScenarioNotFound) {
		t.Fatalf("approving a scenario of another plan: %v, want %s", err, CodeScenarioNotFound)
	}
	if err := f.signOff("boss", SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unitA, ScenarioID: opt.ID, Submission: 1}); err != nil {
		t.Fatal(err)
	}
	approved := f.plan(t)
	if approved.Status != domain.PlanApproved || approved.ApprovedScenarioID != opt.ID || approved.RegisterEntryID == "" {
		t.Fatalf("after approval: %+v", approved.Plan)
	}
	ctx := asUser("planner")
	rows, err := f.st.Available(ctx, f.rule.ID, "6100|CC2", "2027-02")
	if err != nil || len(rows) != 1 || !rows[0].BudgetTotal.Equal(d("400")) || !rows[0].Available.Equal(d("400")) {
		t.Fatalf("budget control balance of the approved cell = %+v (%v), want 400", rows, err)
	}
	if rows, err = f.st.Available(ctx, f.rule.ID, "6000|CC1", "2027-01"); err != nil || len(rows) != 1 || !rows[0].BudgetTotal.Equal(d("1250")) {
		t.Fatalf("the approved scenario is the copy with 25 percent, got %+v (%v), want 1250", rows, err)
	}
	if rows, err = f.st.Available(ctx, f.rule.ID, "6200|CC2", "2027-03"); err != nil || len(rows) != 0 {
		t.Fatalf("a zero cell must not create a balance: %+v (%v)", rows, err)
	}
	entries, err := f.st.ListRegisterEntries(ctx, f.rule.ID)
	if err != nil || len(entries) != 1 || entries[0].ID != approved.RegisterEntryID || entries[0].EntryType != domain.EntryOriginal {
		t.Fatalf("register entries = %+v (%v), want the plan's Original entry", entries, err)
	}
	if _, _, err := f.st.UpsertCells(ctx, ids.New(), "h", f.view.ID, base.ID, base.Version, []domain.PlanCell{cell("6000", "CC1", "2027-01", "1")}); !hasCode(err, CodePlanNotDraft) {
		t.Fatalf("editing an approved plan: %v, want %s", err, CodePlanNotDraft)
	}
}

// P4 least privilege: the person who submitted a plan cannot sign it off, even as a stage owner.
func TestPlan_SubmitterCannotSignOff(t *testing.T) {
	f := newPlanFixture(t)
	f.write(t, f.scenario(t, "Baseline"), cell("6000", "CC1", "2027-01", "10"))
	unit := ids.New()
	f.owners(t,
		StageOwnerInput{Stage: domain.StageReview, OrgUnitID: unit, Owner: "planner"},
		StageOwnerInput{Stage: domain.StageApprove, OrgUnitID: unit, Owner: "boss"})
	f.submit(t)
	if err := f.signOff("planner", SignOffRequest{Stage: domain.StageReview, OrgUnitID: unit, Submission: 1}); !hasCode(err, CodePlanSeparation) {
		t.Fatalf("submitter signing: %v, want %s", err, CodePlanSeparation)
	}
}

// A reject returns the plan to Draft, clears the sign-offs and a stale workflow's signal is refused.
func TestPlan_RejectReturnsToDraftAndResubmits(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	f.write(t, base, cell("6000", "CC1", "2027-01", "10"))
	unit := ids.New()
	f.owners(t,
		StageOwnerInput{Stage: domain.StageReview, OrgUnitID: unit, Owner: "rev"},
		StageOwnerInput{Stage: domain.StageApprove, OrgUnitID: unit, Owner: "boss"})
	f.submit(t)
	if err := f.signOff("rev", SignOffRequest{Stage: domain.StageReview, OrgUnitID: unit, Submission: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.RejectPlan(asUser("planner"), ids.New(), "h", RejectRequest{PlanID: f.view.ID, Reason: "no", Submission: 1}); !hasCode(err, CodeNotStageOwner) {
		t.Fatalf("a non-owner rejecting: %v, want %s", err, CodeNotStageOwner)
	}
	if _, _, err := f.st.RejectPlan(asUser("boss"), ids.New(), "h", RejectRequest{PlanID: f.view.ID, Reason: "too high", Submission: 2}); !hasCode(err, CodePlanStale) {
		t.Fatalf("a stale reject: %v, want %s", err, CodePlanStale)
	}
	mustCommand(t)(f.st.RejectPlan(asUser("boss"), ids.New(), "h", RejectRequest{PlanID: f.view.ID, Reason: "too high", Submission: 1}))
	back := f.plan(t)
	if back.Status != domain.PlanDraft || back.ApprovedScenarioID != "" {
		t.Fatalf("after reject: %s approved scenario %q", back.Status, back.ApprovedScenarioID)
	}
	for _, o := range back.StageOwners {
		if o.SignedBy != "" {
			t.Fatalf("sign-off of %s survived the reject", o.Owner)
		}
	}
	f.write(t, f.scenario(t, "Baseline"), cell("6000", "CC1", "2027-01", "8"))
	if again := f.submit(t); again.Submission != 2 {
		t.Fatalf("resubmitted as submission %d, want 2", again.Submission)
	}
	if err := f.signOff("rev", SignOffRequest{Stage: domain.StageReview, OrgUnitID: unit, Submission: 1}); !hasCode(err, CodePlanStale) {
		t.Fatalf("a signal of the first submission: %v, want %s", err, CodePlanStale)
	}
	var decisions int
	err := dbx.WithTenantTx(asUser("planner"), f.st.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select count(*) from budget_plan_decision where process_id = $1`, f.view.ID).Scan(&decisions)
	})
	if err != nil || decisions != 4 {
		t.Fatalf("decision log has %d rows (%v), want submit, sign-off, reject, submit", decisions, err)
	}
}

func TestPlan_CrossTenantPlanIsInvisible(t *testing.T) {
	f := newPlanFixture(t)
	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(planTenant + 1), UserID: "x", Duties: map[string]struct{}{}})
	if _, err := f.st.GetPlan(other, f.view.ID); !hasCode(err, CodePlanNotFound) {
		t.Fatalf("another tenant reading the plan: %v, want %s", err, CodePlanNotFound)
	}
}

func TestPlan_ApproverMustBeIndependentOfTheAuthors(t *testing.T) {
	cases := []struct {
		name, approver string
		setter, editor string
	}{
		{"the creator", "planner", "setter", "editor"},
		{"whoever set the owners", "setter", "setter", "editor"},
		{"whoever last edited the cells", "editor", "setter", "editor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlanFixture(t)
			unit := ids.New()
			base := f.scenario(t, "Baseline")
			if _, _, err := f.st.UpsertCells(asUser(tc.editor), ids.New(), "h", f.view.ID, base.ID, base.Version, []domain.PlanCell{cell("6000", "CC1", "2027-01", "10")}); err != nil {
				t.Fatal(err)
			}
			mustCommand(t)(f.st.SetStageOwners(asUser(tc.setter), ids.New(), "h", f.view.ID, []StageOwnerInput{
				{Stage: domain.StageReview, OrgUnitID: unit, Owner: "rev"}, {Stage: domain.StageApprove, OrgUnitID: unit, Owner: tc.approver}}))
			mustCommand(t)(f.st.SubmitPlan(asUser("submitter"), ids.New(), "h", f.view.ID))
			if err := f.signOff("rev", SignOffRequest{Stage: domain.StageReview, OrgUnitID: unit, Submission: 1}); err != nil {
				t.Fatal(err)
			}
			err := f.signOff(tc.approver, SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unit, ScenarioID: base.ID, Submission: 1})
			if !hasCode(err, CodePlanSeparation) {
				t.Fatalf("%s approving: %v, want %s", tc.name, err, CodePlanSeparation)
			}
		})
	}
}

func TestPlan_ApprovingAScenarioWithoutAmountsIsRefusedBeforeItLocksIn(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	f.write(t, base, cell("6000", "CC1", "2027-01", "10"))
	var empty Scenario
	if err := json.Unmarshal(mustCommand(t)(f.st.CreateScenario(asUser("planner"), ids.New(), "h", f.view.ID, NewScenario{Name: "Empty", Kind: "Custom"})), &empty); err != nil {
		t.Fatal(err)
	}
	unit := ids.New()
	f.owners(t, StageOwnerInput{Stage: domain.StageReview, OrgUnitID: unit, Owner: "rev"}, StageOwnerInput{Stage: domain.StageApprove, OrgUnitID: unit, Owner: "boss"})
	f.submit(t)
	if err := f.signOff("rev", SignOffRequest{Stage: domain.StageReview, OrgUnitID: unit, Submission: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.signOff("boss", SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unit, ScenarioID: empty.ID, Submission: 1}); !hasCode(err, CodePlanIncomplete) {
		t.Fatalf("approving an empty scenario: %v, want %s", err, CodePlanIncomplete)
	}
	if got := f.plan(t); got.ApprovedScenarioID != "" || got.Status != domain.PlanReviewed {
		t.Fatalf("the refused pick locked in %q with status %s", got.ApprovedScenarioID, got.Status)
	}
	if err := f.signOff("boss", SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unit, ScenarioID: base.ID, Submission: 1}); err != nil {
		t.Fatalf("approving the scenario with amounts afterwards: %v", err)
	}
}

func TestPlan_SecondApprovedPlanOnTheSameRuleAndYearIsRefused(t *testing.T) {
	f := newPlanFixture(t)
	approve := func(planID string) error {
		unit := ids.New()
		v, err := f.st.GetPlan(asUser("planner"), planID)
		if err != nil {
			t.Fatal(err)
		}
		sc := v.Scenarios[0]
		mustCommand(t)(f.st.UpsertCells(asUser("planner"), ids.New(), "h", planID, sc.ID, sc.Version, []domain.PlanCell{cell("6000", "CC1", "2027-01", "10")}))
		mustCommand(t)(f.st.SetStageOwners(asUser("planner"), ids.New(), "h", planID, []StageOwnerInput{
			{Stage: domain.StageReview, OrgUnitID: unit, Owner: "rev"}, {Stage: domain.StageApprove, OrgUnitID: unit, Owner: "boss"}}))
		mustCommand(t)(f.st.SubmitPlan(asUser("planner"), ids.New(), "h", planID))
		if _, _, err := f.st.SignOff(asUser("rev"), ids.New(), "h", SignOffRequest{PlanID: planID, Stage: domain.StageReview, OrgUnitID: unit, Submission: 1}); err != nil {
			t.Fatal(err)
		}
		_, _, err = f.st.SignOff(asUser("boss"), ids.New(), "h", SignOffRequest{PlanID: planID, Stage: domain.StageApprove, OrgUnitID: unit, ScenarioID: sc.ID, Submission: 1})
		return err
	}
	if err := approve(f.view.ID); err != nil {
		t.Fatal(err)
	}
	second := decodePlanView(t, mustCommand(t)(f.st.CreatePlan(asUser("planner"), ids.New(), "h", NewPlan{LegalEntityID: f.rule.LegalEntityID, RuleVersionID: f.rule.ID, Name: "FY27 stretch", FiscalYear: 2027})))
	if err := approve(second.ID); !hasCode(err, CodePlanYearApproved) {
		t.Fatalf("second approval for the rule version and year: %v, want %s", err, CodePlanYearApproved)
	}
	entries, err := f.st.ListRegisterEntries(asUser("planner"), f.rule.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("register entries = %d (%v), want the first plan's only", len(entries), err)
	}
}

func TestPlan_ListPlansMatchesAnUppercaseLegalEntityID(t *testing.T) {
	f := newPlanFixture(t)
	plans, err := f.st.ListPlans(asUser("planner"), strings.ToUpper(f.rule.LegalEntityID))
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans for the upper-cased legal entity = %d (%v), want 1", len(plans), err)
	}
}

func (f planFixture) writeAs(t *testing.T, user string, sc Scenario, cells ...domain.PlanCell) {
	t.Helper()
	cur := f.scenarioByID(t, sc.ID)
	mustCommand(t)(f.st.UpsertCells(asUser(user), ids.New(), "h", f.view.ID, sc.ID, cur.Version, cells))
}

func (f planFixture) scenarioByID(t *testing.T, id string) Scenario {
	t.Helper()
	for _, s := range f.plan(t).Scenarios {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no scenario %s", id)
	return Scenario{}
}

func (f planFixture) reviewedWith(t *testing.T, approver string) string {
	t.Helper()
	unit := ids.New()
	f.owners(t, StageOwnerInput{Stage: domain.StageReview, OrgUnitID: unit, Owner: "rev"}, StageOwnerInput{Stage: domain.StageApprove, OrgUnitID: unit, Owner: approver})
	mustCommand(t)(f.st.SubmitPlan(asUser("submitter"), ids.New(), "h", f.view.ID))
	if err := f.signOff("rev", SignOffRequest{Stage: domain.StageReview, OrgUnitID: unit, Submission: 1}); err != nil {
		t.Fatal(err)
	}
	return unit
}

func TestPlan_AnEarlierEditorCannotApproveAfterSomeoneElseEdits(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	f.writeAs(t, "boss", base, cell("6000", "CC1", "2027-01", "10"))
	f.writeAs(t, "later", base, cell("6000", "CC1", "2027-02", "20"))
	unit := f.reviewedWith(t, "boss")
	err := f.signOff("boss", SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unit, ScenarioID: base.ID, Submission: 1})
	if !hasCode(err, CodePlanSeparation) {
		t.Fatalf("an earlier editor approving: %v, want %s", err, CodePlanSeparation)
	}
}

func TestPlan_ACopyKeepsTheEditorsOfItsSourceAndTheCopier(t *testing.T) {
	for _, approver := range []string{"author", "copier"} {
		t.Run(approver, func(t *testing.T) {
			f := newPlanFixture(t)
			base := f.scenario(t, "Baseline")
			f.writeAs(t, "author", base, cell("6000", "CC1", "2027-01", "10"))
			var copied Scenario
			raw := mustCommand(t)(f.st.CreateScenario(asUser("copier"), ids.New(), "h", f.view.ID, NewScenario{Name: "Copy", Kind: "Custom", CopyFromID: base.ID, AdjustmentPct: d("10")}))
			if err := json.Unmarshal(raw, &copied); err != nil {
				t.Fatal(err)
			}
			unit := f.reviewedWith(t, approver)
			err := f.signOff(approver, SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unit, ScenarioID: copied.ID, Submission: 1})
			if !hasCode(err, CodePlanSeparation) {
				t.Fatalf("%s approving the copy: %v, want %s", approver, err, CodePlanSeparation)
			}
		})
	}
}

func TestPlan_ApproverWhoNeverTouchedTheScenarioMayApproveIt(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	f.writeAs(t, "author", base, cell("6000", "CC1", "2027-01", "10"))
	unit := f.reviewedWith(t, "boss")
	if err := f.signOff("boss", SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unit, ScenarioID: base.ID, Submission: 1}); err != nil {
		t.Fatalf("an uninvolved approver: %v", err)
	}
}

func TestPlan_ConcurrentApprovalsOfTwoPlansOnTheSameRuleAndYearPostOnce(t *testing.T) {
	f := newPlanFixture(t)
	second := decodePlanView(t, mustCommand(t)(f.st.CreatePlan(asUser("planner"), ids.New(), "h", NewPlan{LegalEntityID: f.rule.LegalEntityID, RuleVersionID: f.rule.ID, Name: "FY27 stretch", FiscalYear: 2027})))
	type ready struct{ planID, unit, scenarioID string }
	prepare := func(planID string) ready {
		v, err := f.st.GetPlan(asUser("planner"), planID)
		if err != nil {
			t.Fatal(err)
		}
		sc, unit := v.Scenarios[0], ids.New()
		mustCommand(t)(f.st.UpsertCells(asUser("author"), ids.New(), "h", planID, sc.ID, sc.Version, []domain.PlanCell{cell("6000", "CC1", "2027-01", "10")}))
		mustCommand(t)(f.st.SetStageOwners(asUser("planner"), ids.New(), "h", planID, []StageOwnerInput{
			{Stage: domain.StageReview, OrgUnitID: unit, Owner: "rev"}, {Stage: domain.StageApprove, OrgUnitID: unit, Owner: "boss"}}))
		mustCommand(t)(f.st.SubmitPlan(asUser("submitter"), ids.New(), "h", planID))
		if _, _, err := f.st.SignOff(asUser("rev"), ids.New(), "h", SignOffRequest{PlanID: planID, Stage: domain.StageReview, OrgUnitID: unit, Submission: 1}); err != nil {
			t.Fatal(err)
		}
		return ready{planID, unit, sc.ID}
	}
	plans := []ready{prepare(f.view.ID), prepare(second.ID)}
	errs := make([]error, len(plans))
	var wg sync.WaitGroup
	for i, p := range plans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = f.st.SignOff(asUser("boss"), ids.New(), "h", SignOffRequest{PlanID: p.planID, Stage: domain.StageApprove, OrgUnitID: p.unit, ScenarioID: p.scenarioID, Submission: 1})
		}()
	}
	wg.Wait()
	won, lost := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case hasCode(err, CodePlanYearApproved):
			lost++
		default:
			t.Fatalf("concurrent approval: %v", err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("concurrent approvals: %d won, %d refused with %s, want 1 and 1", won, lost, CodePlanYearApproved)
	}
	entries, err := f.st.ListRegisterEntries(asUser("planner"), f.rule.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("register entries = %d (%v), want 1", len(entries), err)
	}
}

func TestPlan_TheDatabaseRefusesASecondApprovedPlanForTheRuleAndYear(t *testing.T) {
	f := newPlanFixture(t)
	second := decodePlanView(t, mustCommand(t)(f.st.CreatePlan(asUser("planner"), ids.New(), "h", NewPlan{LegalEntityID: f.rule.LegalEntityID, RuleVersionID: f.rule.ID, Name: "FY27 stretch", FiscalYear: 2027})))
	for i, id := range []string{f.view.ID, second.ID} {
		_, err := f.st.pool.Exec(context.Background(), `update budget_plan_process set status = 'Approved' where id = $1`, id)
		var pgErr *pgconn.PgError
		if i == 0 && err != nil {
			t.Fatal(err)
		}
		if i == 1 && (!errors.As(err, &pgErr) || pgErr.ConstraintName != oneApprovedPlanIndex) {
			t.Fatalf("second Approved plan: %v, want a violation of %s", err, oneApprovedPlanIndex)
		}
	}
}

func TestPlan_AnExistingOriginalEntryForTheYearBlocksApproval(t *testing.T) {
	cases := []struct {
		name, span string
		blocked    bool
	}{
		{"fiscal year span", "FY2027", true},
		{"month span in the year", "2027-03", true},
		{"another year", "FY2026", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlanFixture(t)
			mustCommand(t)(f.st.PostRegisterEntry(asUser("setup"), ids.New(), "h", RegisterRequest{RuleVersionID: f.rule.ID, EntryType: domain.EntryOriginal,
				Lines: []domain.RegisterLine{{DimensionKey: "d1", SpanKey: tc.span, Amount: d("500")}}}))
			base := f.scenario(t, "Baseline")
			f.writeAs(t, "author", base, cell("6000", "CC1", "2027-01", "10"))
			unit := f.reviewedWith(t, "boss")
			err := f.signOff("boss", SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unit, ScenarioID: base.ID, Submission: 1})
			if tc.blocked {
				if !hasCode(err, CodeYearHasOriginal) {
					t.Fatalf("approving over an Original entry: %v, want %s", err, CodeYearHasOriginal)
				}
				if got := f.plan(t); got.Status != domain.PlanReviewed || got.ApprovedScenarioID != "" {
					t.Fatalf("the refused approval left status %s and approved scenario %q", got.Status, got.ApprovedScenarioID)
				}
				return
			}
			if err != nil {
				t.Fatalf("approving with an Original entry for another year: %v", err)
			}
		})
	}
}

func TestPlan_GridReadSaysWhenItIsCutOffAndTotalsTheWholeFilter(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	if _, err := f.st.pool.Exec(context.Background(), `insert into budget_plan_cell (tenant_id, legal_entity_id, process_id, scenario_id, account, cost_center, period, amount)
		select $1, $2, $3, $4, 'A' || g, 'CC1', '2027-01', 1 from generate_series(1, $5::int) g`,
		testkit.TenantID(planTenant), f.rule.LegalEntityID, f.view.ID, base.ID, gridLimit+1); err != nil {
		t.Fatal(err)
	}
	g, err := f.st.GetGrid(asUser("planner"), f.view.ID, base.ID, GridFilter{})
	if err != nil || !g.Truncated || len(g.Cells) != gridLimit || !g.Total.Equal(d("10001")) {
		t.Fatalf("grid over the cap: %d cells, truncated %v, total %s, err %v; want %d cells, truncated and the full total 10001", len(g.Cells), g.Truncated, g.Total, err, gridLimit)
	}
	one, err := f.st.GetGrid(asUser("planner"), f.view.ID, base.ID, GridFilter{Account: "A7"})
	if err != nil || one.Truncated || len(one.Cells) != 1 || !one.Total.Equal(d("1")) {
		t.Fatalf("filtered grid: %+v err %v, want one cell, not truncated", one, err)
	}
}

func TestPlan_SpreadAnswerCarriesThePriorYearFallbackFlag(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	var written GridWritten
	raw := mustCommand(t)(f.st.SpreadCells(asUser("planner"), ids.New(), "h", f.view.ID, base.ID, base.Version, []domain.PlanCell{cell("6000", "CC1", "2027-01", "10")}, true))
	if err := json.Unmarshal(raw, &written); err != nil || !written.PriorYearFallback || !strings.Contains(string(raw), `"priorYearFallback":true`) {
		t.Fatalf("spread answer %s (%v), want priorYearFallback true", raw, err)
	}
}

func TestPlan_ALastEditorRecordedOnlyInUpdatedByCannotApprove(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	f.writeAs(t, "author", base, cell("6000", "CC1", "2027-01", "10"))
	if _, err := f.st.pool.Exec(context.Background(), `delete from budget_plan_scenario_editor where scenario_id = $1`, base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.pool.Exec(context.Background(), `update budget_plan_scenario set updated_by = 'legacy' where id = $1`, base.ID); err != nil {
		t.Fatal(err)
	}
	unit := f.reviewedWith(t, "legacy")
	err := f.signOff("legacy", SignOffRequest{Stage: domain.StageApprove, OrgUnitID: unit, ScenarioID: base.ID, Submission: 1})
	if !hasCode(err, CodePlanSeparation) {
		t.Fatalf("a pre-existing scenario's last editor approving: %v, want %s", err, CodePlanSeparation)
	}
}
