package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func strp(s string) *string { return &s }

// groupOperation registers a one-operation order in a setup group.
func groupOperation(t *testing.T, ctx context.Context, s *store.Postgres, group string, minutes int, rid string, due int) store.Operation {
	t.Helper()
	dueAt := at(0, due, 0)
	op, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: minutes, Priority: 100,
		DueAt: &dueAt, SetupGroup: strp(group), SetupMinutes: 15, ResourceIDs: []string{rid}}, store.Idem{})
	if err != nil {
		t.Fatalf("create operation in %s: %v", group, err)
	}
	return op
}

func lastJobEvent(t *testing.T, admin *pgxpool.Pool, jobID string) schedevents.JobScheduledData {
	t.Helper()
	var raw []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where type = $1 and aggregate_id = $2 order by created_at desc limit 1`,
		schedevents.JobScheduled, jobID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data schedevents.JobScheduledData `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}
	return evt.Data
}

func TestCreateOperation_StoresSetupAttributesAndNetwork(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	v1, f1 := mustResource(t, ctx, s, "V1", true), mustResource(t, ctx, s, "F1", true)
	n, _, err := s.CreateNetwork(ctx, store.Network{Code: "MIX-FILL", Name: "Mixing to filling", ActiveFrom: "2027-01-01",
		Edges: []domain.NetworkEdge{{From: v1.ID, To: f1.ID}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	in := store.Operation{OrderID: ids.New(), OperNumber: 20, Minutes: 60, Priority: 100, ResourceIDs: []string{f1.ID},
		SetupGroup: strp("WHITE"), SetupMinutes: 25, Attributes: map[string]string{"color": "white", "grade": "2"}, NetworkID: &n.ID}
	op, created, err := s.CreateOperation(ctx, in, store.Idem{Key: "op-1", Fingerprint: "fp"})
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if *op.SetupGroup != "WHITE" || op.SetupMinutes != 25 || op.Attributes["grade"] != "2" || *op.NetworkID != n.ID {
		t.Fatalf("operation = %+v", op)
	}
	if again, created, err := s.CreateOperation(ctx, in, store.Idem{Key: "op-1", Fingerprint: "fp"}); err != nil || created || again.Attributes["color"] != "white" {
		t.Fatalf("replay = %+v created=%v err=%v", again, created, err)
	}
	plain := mustOperation(t, ctx, s, ids.New(), 10, 30, f1.ID)
	if plain.SetupGroup != nil || plain.NetworkID != nil || plain.Attributes == nil || len(plain.Attributes) != 0 {
		t.Fatalf("plain operation = %+v, want no group, no network and empty attributes", plain)
	}
	in.OrderID, in.NetworkID = ids.New(), strp(ids.New())
	if _, _, err := s.CreateOperation(ctx, in, store.Idem{}); problemCode(err) != store.CodeNetworkNotFound {
		t.Fatalf("unknown network: err=%v", err)
	}
	in.NetworkID = &n.ID
	if _, _, err := s.CreateOperation(tenantCtx(2), in, store.Idem{}); problemCode(err) != store.CodeUnknownResource {
		t.Fatalf("tenant 2 names tenant 1 resources: err=%v", err)
	}
}

// AC-23.10 through the database: fillers F1 and F2 are busy until 12:00, F3
// has no edge from V1. The run delays mixing so filling starts within the
// 120 min wait, and the order's own jobs never land on F3.
func TestRunSchedule_EnforcesTheResourceNetwork(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	v1, f1, f2, f3 := mustResource(t, ctx, s, "V1", true), mustResource(t, ctx, s, "F1", true), mustResource(t, ctx, s, "F2", true), mustResource(t, ctx, s, "F3", true)
	n, _, err := s.CreateNetwork(ctx, store.Network{Code: "MIX-FILL", Name: "Mixing to filling", ActiveFrom: "2027-01-01",
		Edges: []domain.NetworkEdge{{From: v1.ID, To: f1.ID, MaxWaitMinutes: intp(120)}, {From: v1.ID, To: f2.ID, MaxWaitMinutes: intp(120)}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	early := at(0, 7, 0)
	for _, rid := range []string{f1.ID, f2.ID} {
		if _, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 240, Priority: 100,
			DueAt: &early, ResourceIDs: []string{rid}}, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	order := ids.New()
	mix := mustOperation(t, ctx, s, order, 10, 60, v1.ID)
	fill, _, err := s.CreateOperation(ctx, store.Operation{OrderID: order, OperNumber: 20, Minutes: 60, Priority: 100,
		ResourceIDs: []string{f3.ID, f1.ID, f2.ID}, NetworkID: &n.ID}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil || len(run.Messages) != 0 || len(run.Warnings) != 0 {
		t.Fatalf("run = %+v err=%v", run, err)
	}
	bars := barsOf(t, ctx, s)
	if b := bars[fill.ID]; b.ResourceID == f3.ID || !b.Start.Equal(at(0, 12, 0)) {
		t.Fatalf("fill = %+v, want F1 or F2 at 12:00, never F3", b)
	}
	if b := bars[mix.ID]; !b.Start.Equal(at(0, 9, 0)) {
		t.Fatalf("mix starts %v, want 09:00 so the wait to 12:00 is 120 min", b.Start)
	}
}

// A network with no edge to any candidate unplaces the order with SCHED-0009.
func TestRunSchedule_FlagsNetworkInfeasibleOrders(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	v1, f1, f3 := mustResource(t, ctx, s, "V1", true), mustResource(t, ctx, s, "F1", true), mustResource(t, ctx, s, "F3", true)
	n, _, err := s.CreateNetwork(ctx, store.Network{Code: "MIX-FILL", Name: "Mixing to filling", ActiveFrom: "2027-01-01",
		Edges: []domain.NetworkEdge{{From: v1.ID, To: f1.ID}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	order := ids.New()
	mix := mustOperation(t, ctx, s, order, 10, 60, v1.ID)
	if _, _, err := s.CreateOperation(ctx, store.Operation{OrderID: order, OperNumber: 20, Minutes: 60, Priority: 100,
		ResourceIDs: []string{f3.ID}, NetworkID: &n.ID}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	run, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil || len(run.Messages) != 2 || !strings.Contains(run.Messages[0], domain.CodeNetworkInfeasible) {
		t.Fatalf("run = %+v err=%v, want both operations unplaced with SCHED-0009", run, err)
	}
	if ops, _ := s.ListOperations(ctx, store.OperationFilter{Status: "unplaced", Limit: 10}); len(ops) != 2 {
		t.Fatalf("unplaced operations = %+v", ops)
	}
	if _, ok := barsOf(t, ctx, s)[mix.ID]; ok {
		t.Fatal("mixing kept a job although its order is network infeasible")
	}
}

// AC-23.11 heuristic half through the database: on a resource in group A,
// B then C take 30 and 20 min changeovers. The changeover is stored on the
// job, shown on the bar and announced on job.scheduled; a later run starts
// from the group the resource was left in.
func TestRunSchedule_LaysChangeoversFromTheSetupMatrix(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	br2312Matrix(t, ctx, s, r.ID, "2027-01-01")
	a := groupOperation(t, ctx, s, "A", 60, r.ID, 9)
	b := groupOperation(t, ctx, s, "B", 60, r.ID, 10)
	c := groupOperation(t, ctx, s, "C", 60, r.ID, 11)
	run, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil || len(run.Warnings) != 0 {
		t.Fatalf("run = %+v err=%v", run, err)
	}
	bars := barsOf(t, ctx, s)
	want := map[string][3]int{a.ID: {8 * 60, 9 * 60, 0}, b.ID: {9 * 60, 10*60 + 30, 30}, c.ID: {10*60 + 30, 11*60 + 50, 20}}
	for id, w := range want {
		bar := bars[id]
		if !bar.Start.Equal(at(0, 0, w[0])) || !bar.End.Equal(at(0, 0, w[1])) || bar.ChangeoverMinutes != w[2] {
			t.Fatalf("bar %s = %v-%v changeover %d, want %d-%d min with %d", id, bar.Start, bar.End, bar.ChangeoverMinutes, w[0], w[1], w[2])
		}
	}
	if *bars[c.ID].SetupGroup != "C" {
		t.Fatalf("bar group = %v, want C", bars[c.ID].SetupGroup)
	}
	if evt := lastJobEvent(t, admin, b.ID); evt.ChangeoverMinutes != 30 {
		t.Fatalf("job.scheduled = %+v, want changeoverMinutes 30", evt)
	}

	// The next day the resource is still in group C: a B job pays C -> B 60.
	next := groupOperation(t, ctx, s, "B", 60, r.ID, 30)
	if _, _, err := s.RunSchedule(ctx, store.RunRequest{Scope: "selection", Mode: "jobs", Finite: true, OperationIDs: []string{next.ID},
		Start: at(1, 6, 0), HorizonDays: 60}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if bar := barsOf(t, ctx, s)[next.ID]; bar.ChangeoverMinutes != 60 || !bar.Start.Equal(at(1, 8, 0)) {
		t.Fatalf("next-day B = %+v, want a 60 min C -> B changeover from 08:00", bar)
	}
}

// A pair without a matrix entry uses the route setup and the run keeps the
// SCHED-0012 warning, readable from the run afterwards.
func TestRunSchedule_RecordsMissingMatrixEntryWarnings(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	br2312Matrix(t, ctx, s, r.ID, "2027-01-01")
	groupOperation(t, ctx, s, "B", 60, r.ID, 9)
	d := groupOperation(t, ctx, s, "D", 60, r.ID, 10)
	run, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetRun(ctx, run.ID)
	if err != nil || len(stored.Warnings) != 1 || stored.Warnings[0].Code != domain.CodeMissingSetupEntry || stored.Warnings[0].OperationID != d.ID {
		t.Fatalf("run warnings = %+v err=%v, want one SCHED-0012 for the D job", stored.Warnings, err)
	}
	if bar := barsOf(t, ctx, s)[d.ID]; bar.ChangeoverMinutes != 15 {
		t.Fatalf("D changeover = %d, want the 15 min route setup", bar.ChangeoverMinutes)
	}
}

// AC-23.13 through the database: a weekly Monday 08:00-12:00 block for
// color = white. The white job is placed in it, the red one after it with a
// SCHED-0014 warning.
func TestRunSchedule_PlacesJobsByCapacityBlock(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "PAINT-1", true)
	if _, _, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: r.ID, Code: "MON-WHITE",
		Windows: []domain.Shift{{Weekday: 1, StartMinute: 8 * 60, EndMinute: 12 * 60}}, Priority: 10, Active: true,
		Condition: []domain.Predicate{{Attribute: "color", Op: "=", Value: "white"}}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	paint := func(color string, due int) store.Operation {
		dueAt := at(0, due, 0)
		op, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 120, Priority: 100, DueAt: &dueAt,
			Attributes: map[string]string{"color": color}, ResourceIDs: []string{r.ID}}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	red, white := paint("red", 9), paint("white", 10)
	run, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil || len(run.Warnings) != 1 || run.Warnings[0].Code != domain.CodeNoMatchingBlock || run.Warnings[0].OperationID != red.ID {
		t.Fatalf("run = %+v err=%v, want one SCHED-0014 for the red job", run, err)
	}
	bars := barsOf(t, ctx, s)
	if !bars[white.ID].Start.Equal(at(0, 8, 0)) || !bars[red.ID].Start.Equal(at(0, 12, 0)) {
		t.Fatalf("white %v, red %v; want white at 08:00 in the block and red at 12:00 after it", bars[white.ID].Start, bars[red.ID].Start)
	}
}

// A move keeps changeovers right: moving B behind C is refused because C
// would be left with too short a changeover after A, and moving the C job
// later reprices it from the group before its new slot.
func TestMoveJob_KeepsChangeoversConsistent(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	br2312Matrix(t, ctx, s, r.ID, "2027-01-01")
	groupOperation(t, ctx, s, "A", 60, r.ID, 9)
	b := groupOperation(t, ctx, s, "B", 60, r.ID, 10)
	c := groupOperation(t, ctx, s, "C", 60, r.ID, 11)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	out, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: b.ID, Start: at(0, 14, 0), HorizonDays: 60, DryRun: true}, store.Idem{})
	if err != nil || out.Conflict == nil || out.Conflict.Code != domain.CodeOverlap || out.Conflict.JobID != c.ID {
		t.Fatalf("move B behind C = %+v err=%v, want SCHED-0001 naming C", out.Conflict, err)
	}
	out, _, err = s.MoveJob(ctx, store.MoveRequest{JobID: c.ID, Start: at(0, 14, 0), HorizonDays: 60}, store.Idem{Key: "mv-c", Fingerprint: "fp"})
	if err != nil || out.Conflict != nil || len(out.Changes) != 1 {
		t.Fatalf("move C = %+v err=%v", out, err)
	}
	if bar := barsOf(t, ctx, s)[c.ID]; !bar.Start.Equal(at(0, 14, 0)) || bar.ChangeoverMinutes != 20 || !bar.End.Equal(at(0, 15, 20)) {
		t.Fatalf("moved C = %+v, want 14:00-15:20 with the B -> C changeover of 20 min", bar)
	}
}
