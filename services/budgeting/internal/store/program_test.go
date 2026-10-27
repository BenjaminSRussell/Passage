package store

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	budgetevents "github.com/BenjaminSRussell/passage/services/budgeting/internal/events"
	"github.com/BenjaminSRussell/passage/services/budgeting/migrations"
)

type programFixture struct {
	ctx  context.Context
	st   *Postgres
	pool *pgxpool.Pool
	prog ProgramView
}

// newProgram migrates a fresh schema and creates a 2026 program of 5,000,000.00.
func newProgram(t *testing.T) programFixture {
	t.Helper()
	pool := testkit.DB(t)
	ctx := testCtx(testkit.TenantID(7), "budget.program.maintain")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	f := programFixture{ctx: ctx, st: New(pool), pool: pool}
	status, raw, err := f.st.CreateProgram(ctx, "prog", "h", NewProgram{LegalEntityID: ids.New(), Code: "CAPEX-26", Name: "Plant capex",
		ApprovalYear: 2026, Currency: "USD", Budget: d("5000000.00")})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create program: %d %v", status, err)
	}
	f.prog = decodeView(t, raw)
	return f
}

func decodeView(t *testing.T, raw []byte) ProgramView {
	t.Helper()
	var v ProgramView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode view: %v (%s)", err, raw)
	}
	return v
}

func (f programFixture) distribute(t *testing.T, parent string, allocations ...Allocation) ProgramView {
	t.Helper()
	_, raw, err := f.st.DistributePositions(f.ctx, ids.New(), "h", f.prog.ID, DistributeRequest{ParentPositionID: parent, Allocations: allocations})
	if err != nil {
		t.Fatalf("distribute under %q: %v", parent, err)
	}
	return decodeView(t, raw)
}

func (f programFixture) position(t *testing.T, code string) Position {
	t.Helper()
	view, err := f.st.GetProgram(f.ctx, f.prog.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range view.Positions {
		if p.Code == code {
			return p
		}
	}
	t.Fatalf("position %s not found", code)
	return Position{}
}

// AC-38.13 through the store: 4,800,000.00 of 5,000,000.00 is distributed, a
// further 300,000.00 is refused with CO-0641 and leaves nothing behind.
func TestProgram_AC38_13_DistributionCappedByParent(t *testing.T) {
	f := newProgram(t)
	view := f.distribute(t, "", Allocation{PositionCode: "PLANT", Budget: d("4800000.00")})
	if !view.Distributed.Equal(d("4800000")) || len(view.Positions) != 1 || view.Positions[0].Level != 1 || view.Positions[0].Category != domain.CategoryCapex {
		t.Fatalf("after first distribution: %+v", view)
	}
	_, _, err := f.st.DistributePositions(f.ctx, ids.New(), "h", f.prog.ID, DistributeRequest{Allocations: []Allocation{{PositionCode: "IT", Budget: d("300000.00")}}})
	if !hasCode(err, domain.CodeDistributionExceeds) {
		t.Fatalf("err = %v, want CO-0641", err)
	}
	after, err := f.st.GetProgram(f.ctx, f.prog.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Positions) != 1 || !after.Distributed.Equal(d("4800000")) || after.Version != view.Version {
		t.Fatalf("a refused distribution changed the program: %+v", after)
	}
	view = f.distribute(t, "", Allocation{PositionCode: "IT", Category: domain.CategoryContingency, Budget: d("200000.00")})
	if !view.Distributed.Equal(d("5000000")) {
		t.Fatalf("distributed %s, want 5000000", view.Distributed)
	}
}

func TestProgram_HierarchyRules(t *testing.T) {
	f := newProgram(t)
	f.distribute(t, "", Allocation{PositionCode: "PLANT", Budget: d("4000000")})
	plant := f.position(t, "PLANT")
	f.distribute(t, plant.ID, Allocation{PositionCode: "LINE1", Budget: d("3000000")}, Allocation{PositionCode: "LINE2", Budget: d("1000000")})
	if p := f.position(t, "PLANT"); !p.Distributed.Equal(d("4000000")) || !p.Undistributed.IsZero() {
		t.Fatalf("plant after distribution: %+v", p)
	}
	if line := f.position(t, "LINE1"); line.Level != 2 || line.ParentID != plant.ID {
		t.Fatalf("line1: %+v", line)
	}

	// BR-38.15: a parent cannot drop below what its children hold.
	_, _, err := f.st.DistributePositions(f.ctx, ids.New(), "h", f.prog.ID, DistributeRequest{Allocations: []Allocation{{PositionCode: "PLANT", Budget: d("3999999.99")}}})
	if !hasCode(err, domain.CodeBelowDistributed) {
		t.Fatalf("reduce parent below children: %v", err)
	}
	// A code already used under another parent is refused rather than moved.
	_, _, err = f.st.DistributePositions(f.ctx, ids.New(), "h", f.prog.ID, DistributeRequest{Allocations: []Allocation{{PositionCode: "LINE1", Budget: d("1")}}})
	if !hasCode(err, CodePositionCodeInUse) {
		t.Fatalf("code under another parent: %v", err)
	}
	// A parent of another program is not found here.
	_, raw, err := f.st.CreateProgram(f.ctx, "other", "h", NewProgram{LegalEntityID: f.prog.LegalEntityID, Code: "OTHER-26", ApprovalYear: 2026, Currency: "USD", Budget: d("10")})
	if err != nil {
		t.Fatal(err)
	}
	other := programFixture{ctx: f.ctx, st: f.st, pool: f.pool, prog: decodeView(t, raw)}
	other.distribute(t, "", Allocation{PositionCode: "X", Budget: d("10")})
	_, _, err = f.st.DistributePositions(f.ctx, ids.New(), "h", f.prog.ID, DistributeRequest{ParentPositionID: other.position(t, "X").ID,
		Allocations: []Allocation{{PositionCode: "Y", Budget: d("1")}}})
	if !hasCode(err, domain.CodePositionNotFound) {
		t.Fatalf("foreign parent: %v", err)
	}

	// Six levels at most.
	parent := f.position(t, "LINE1").ID
	for _, code := range []string{"L3", "L4", "L5", "L6"} {
		f.distribute(t, parent, Allocation{PositionCode: code, Budget: d("100")})
		parent = f.position(t, code).ID
	}
	if f.position(t, "L6").Level != 6 {
		t.Fatalf("L6 level %d", f.position(t, "L6").Level)
	}
	_, _, err = f.st.DistributePositions(f.ctx, ids.New(), "h", f.prog.ID, DistributeRequest{ParentPositionID: parent, Allocations: []Allocation{{PositionCode: "L7", Budget: d("1")}}})
	if !hasCode(err, domain.CodeLevelExceeded) {
		t.Fatalf("level 7: %v", err)
	}
}

// Rollup and carry-forward: actuals and commitments of assigned objects roll up
// to every ancestor, and each position carries its own unused slice exactly once.
func TestProgram_RollupAndCarryForward(t *testing.T) {
	f := newProgram(t)
	f.distribute(t, "", Allocation{PositionCode: "PLANT", Budget: d("4800000")})
	plant := f.position(t, "PLANT")
	f.distribute(t, plant.ID, Allocation{PositionCode: "LINE1", Budget: d("3000000")}, Allocation{PositionCode: "LINE2", Budget: d("1500000")})
	line1, line2 := f.position(t, "LINE1"), f.position(t, "LINE2")

	assign := func(pos, ref, plan, actual, commitment string) ProgramView {
		t.Helper()
		_, raw, err := f.st.AssignObject(f.ctx, ids.New(), "h", f.prog.ID, AssignRequest{PositionID: pos, ObjectType: domain.ObjectCapexRequest, ObjectRef: ref,
			Plan: d(plan), Actual: d(actual), Commitment: d(commitment)})
		if err != nil {
			t.Fatal(err)
		}
		return decodeView(t, raw)
	}
	assign(line1.ID, "CR-1", "2500000", "1000000", "500000")
	view := assign(line2.ID, "CR-2", "1000000", "200000", "0")
	if !view.Actual.Equal(d("1200000")) || !view.Commitment.Equal(d("500000")) || !view.Available.Equal(d("3300000")) {
		t.Fatalf("program totals: actual %s commitment %s available %s", view.Actual, view.Commitment, view.Available)
	}
	if p := f.position(t, "PLANT"); !p.Plan.Equal(d("3500000")) || !p.Actual.Equal(d("1200000")) || !p.Available.Equal(d("3100000")) {
		t.Fatalf("plant rollup: %+v", p)
	}
	// Re-reporting CR-2 on LINE1 moves it: LINE2 is empty again.
	assign(line1.ID, "CR-2", "1000000", "300000", "0")
	if p := f.position(t, "LINE2"); !p.Actual.IsZero() || len(p.Assignments) != 0 {
		t.Fatalf("line2 after move: %+v", p)
	}
	if p := f.position(t, "LINE1"); !p.Actual.Equal(d("1300000")) || len(p.Assignments) != 2 {
		t.Fatalf("line1 after move: %+v", p)
	}

	if _, _, err := f.st.CarryForward(f.ctx, ids.New(), "h", f.prog.ID, 2028); !hasCode(err, CodeCarryForwardYear) {
		t.Fatalf("carry to 2028: %v", err)
	}
	_, raw, err := f.st.CarryForward(f.ctx, "cf-1", "h", f.prog.ID, 2027)
	if err != nil {
		t.Fatal(err)
	}
	var cf CarryForwardResponse
	if err := json.Unmarshal(raw, &cf); err != nil {
		t.Fatal(err)
	}
	// PLANT keeps 4.8M − 4.5M distributed = 300,000; LINE1 3M − 1.3M − 0.5M = 1,200,000; LINE2 1,500,000.
	want := map[string]string{"PLANT": "300000", "LINE1": "1200000", "LINE2": "1500000"}
	if !cf.Amount.Equal(d("3000000")) || len(cf.Positions) != len(want) {
		t.Fatalf("carry-forward: %+v", cf)
	}
	for _, p := range cf.Positions {
		if !p.Amount.Equal(d(want[p.Code])) {
			t.Errorf("%s carried %s, want %s", p.Code, p.Amount, want[p.Code])
		}
	}
	var payload budgetevents.ProgramCarriedForwardData
	var raw2 []byte
	if err := f.pool.QueryRow(f.ctx, `select payload->'data' from outbox where type = $1`, budgetevents.ProgramCarriedForward).Scan(&raw2); err != nil {
		t.Fatalf("carriedforward event: %v", err)
	}
	if err := json.Unmarshal(raw2, &payload); err != nil || payload.Amount != "3000000" || payload.FromYear != 2026 || payload.ToYear != 2027 || len(payload.Positions) != 3 {
		t.Fatalf("event payload %s (%v)", raw2, err)
	}
	// A replay returns the stored answer; a second carry-forward is refused.
	var replay CarryForwardResponse
	if _, again, err := f.st.CarryForward(f.ctx, "cf-1", "h", f.prog.ID, 2027); err != nil || json.Unmarshal(again, &replay) != nil ||
		!replay.Amount.Equal(cf.Amount) || len(replay.Positions) != len(cf.Positions) {
		t.Fatalf("replay: %v %s", err, again)
	}
	if _, _, err := f.st.CarryForward(f.ctx, "cf-2", "h", f.prog.ID, 2027); !hasCode(err, domain.CodeCarryForwardRepeated) {
		t.Fatalf("second carry-forward: %v", err)
	}
	if v, _ := f.st.GetProgram(f.ctx, f.prog.ID); !v.CarriedForward.Equal(d("3000000")) {
		t.Fatalf("program carried %s", v.CarriedForward)
	}
}

// Two concurrent distributions of 3,000,000.00 under 5,000,000.00: the program
// row lock lets exactly one through, so Σ children ≤ parent holds (BR-38.15).
func TestProgram_ConcurrentDistributionKeepsInvariant(t *testing.T) {
	f := newProgram(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, code := range []string{"A", "B"} {
		wg.Add(1)
		go func(i int, code string) {
			defer wg.Done()
			_, _, errs[i] = f.st.DistributePositions(f.ctx, ids.New(), "h", f.prog.ID, DistributeRequest{Allocations: []Allocation{{PositionCode: code, Budget: d("3000000")}}})
		}(i, code)
	}
	wg.Wait()
	refused := 0
	for _, err := range errs {
		switch {
		case err == nil:
		case hasCode(err, domain.CodeDistributionExceeds):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	view, err := f.st.GetProgram(f.ctx, f.prog.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refused != 1 || !view.Distributed.Equal(d("3000000")) {
		t.Fatalf("refused %d, distributed %s", refused, view.Distributed)
	}
}

func TestProgram_DuplicateAndNotFound(t *testing.T) {
	f := newProgram(t)
	_, _, err := f.st.CreateProgram(f.ctx, "prog-2", "h", NewProgram{LegalEntityID: f.prog.LegalEntityID, Code: "CAPEX-26", ApprovalYear: 2026, Currency: "USD", Budget: d("1")})
	if !hasCode(err, CodeProgramDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := f.st.GetProgram(f.ctx, ids.New()); !hasCode(err, CodeProgramNotFound) {
		t.Fatalf("missing program: %v", err)
	}
	if _, _, err := f.st.AssignObject(f.ctx, ids.New(), "h", f.prog.ID, AssignRequest{PositionID: ids.New(), ObjectType: domain.ObjectProject, ObjectRef: "P-1"}); !hasCode(err, domain.CodePositionNotFound) {
		t.Fatalf("assign to missing position: %v", err)
	}
	list, err := f.st.ListPrograms(f.ctx, f.prog.LegalEntityID)
	if err != nil || len(list) != 1 || list[0].Code != "CAPEX-26" {
		t.Fatalf("list: %v %+v", err, list)
	}
}
