package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func TestNetworkFeasibility_ListsInfeasibleOrdersAndWaitViolations(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	v1, f1, f3 := mustResource(t, ctx, s, "V1", true), mustResource(t, ctx, s, "F1", true), mustResource(t, ctx, s, "F3", true)
	net, _, err := s.CreateNetwork(ctx, store.Network{Code: "MIX-FILL", Name: "Mixing to filling", ActiveFrom: "2027-01-01",
		Edges: []domain.NetworkEdge{{From: v1.ID, To: f1.ID}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.CreateNetwork(ctx, store.Network{Code: "OTHER", Name: "Another", ActiveFrom: "2027-01-01",
		Edges: []domain.NetworkEdge{{From: v1.ID, To: f1.ID}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	order := ids.New()
	mix := mustOperation(t, ctx, s, order, 10, 60, v1.ID)
	fill, _, err := s.CreateOperation(ctx, store.Operation{OrderID: order, OperNumber: 20, Minutes: 60, Priority: 100, ResourceIDs: []string{f3.ID}, NetworkID: &net.ID}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil || len(run.Messages) != 2 {
		t.Fatalf("run = %+v err=%v, want both operations unplaced with SCHED-0009", run, err)
	}
	// A second run's warning: a wait over the maximum behind a frozen job.
	warn, _ := json.Marshal([]store.RunWarning{{Code: domain.CodeNetworkWait, OperationID: fill.ID, Detail: "waits 400 min, maximum 120"},
		{Code: "SCHED-0012", OperationID: mix.ID, Detail: "route setup used"}})
	if _, err := admin.Exec(context.Background(), `insert into schedule_run (tenant_id, id, scope, mode, messages, warnings, created_at)
		values ($1, $2, 'site', 'jobs', '[]', $3, now() + interval '1 hour')`, testkit.TenantID(1), ids.New(), warn); err != nil {
		t.Fatal(err)
	}

	all, err := s.NetworkFeasibility(ctx, store.NetworkFeasibilityFilter{Limit: 10})
	if err != nil || all.Truncated || all.Infeasible != 2 || all.InfeasibleOrders != 1 || all.WaitViolations != 1 || len(all.Items) != 3 {
		t.Fatalf("all = %+v err=%v, want 2 infeasible operations of 1 order, 1 wait violation (SCHED-0012 is not listed)", all, err)
	}
	if w := all.Items[0]; w.Code != domain.CodeNetworkWait || w.Flag != store.FlagWaitExceeded || w.OperationID != fill.ID || w.Detail != "waits 400 min, maximum 120" ||
		w.NetworkCode == nil || *w.NetworkCode != "MIX-FILL" || w.OperNumber == nil || *w.OperNumber != 20 {
		t.Fatalf("newest finding = %+v", w)
	}
	byOp := map[string]store.NetworkFinding{}
	for _, x := range all.Items[1:] {
		if x.Code != domain.CodeNetworkInfeasible || x.Flag != domain.FlagNetworkInfeasible || x.RunID != run.ID || x.OrderID == nil || *x.OrderID != order {
			t.Fatalf("finding = %+v", x)
		}
		byOp[x.OperationID] = x
	}
	if byOp[mix.ID].NetworkID != nil || byOp[fill.ID].NetworkID == nil || *byOp[fill.ID].NetworkID != net.ID ||
		byOp[fill.ID].Detail == "" || byOp[fill.ID].Detail[:10] != "SCHED-0009" {
		t.Fatalf("by operation = %+v", byOp)
	}

	// Filters: one network leaves only its own operation's findings.
	one, err := s.NetworkFeasibility(ctx, store.NetworkFeasibilityFilter{NetworkID: net.ID, Limit: 10})
	if err != nil || len(one.Items) != 2 || one.Infeasible != 1 || one.InfeasibleOrders != 1 || one.WaitViolations != 1 {
		t.Fatalf("network filter = %+v err=%v", one, err)
	}
	if none, err := s.NetworkFeasibility(ctx, store.NetworkFeasibilityFilter{NetworkID: other.ID, Limit: 10}); err != nil || len(none.Items) != 0 || none.Infeasible != 0 {
		t.Fatalf("other network = %+v err=%v", none, err)
	}
	if _, err := s.NetworkFeasibility(ctx, store.NetworkFeasibilityFilter{NetworkID: ids.New()}); problemCode(err) != store.CodeNetworkNotFound {
		t.Fatalf("unknown network: err=%v", err)
	}
	// The range is on the run's time: before this hour only the first run counts.
	cut := run.CreatedAt.Add(30 * 60 * 1e9)
	early, err := s.NetworkFeasibility(ctx, store.NetworkFeasibilityFilter{To: &cut, Limit: 10})
	if err != nil || len(early.Items) != 2 || early.WaitViolations != 0 {
		t.Fatalf("to filter = %+v err=%v", early, err)
	}
	late, err := s.NetworkFeasibility(ctx, store.NetworkFeasibilityFilter{From: &cut, Limit: 10})
	if err != nil || len(late.Items) != 1 || late.Infeasible != 0 {
		t.Fatalf("from filter = %+v err=%v", late, err)
	}
	cutShort, err := s.NetworkFeasibility(ctx, store.NetworkFeasibilityFilter{Limit: 2})
	if err != nil || len(cutShort.Items) != 2 || !cutShort.Truncated || cutShort.Infeasible != 2 {
		t.Fatalf("limit 2 = %+v err=%v, want counts over every match", cutShort, err)
	}
	if foreign, err := s.NetworkFeasibility(tenantCtx(2), store.NetworkFeasibilityFilter{Limit: 10}); err != nil || len(foreign.Items) != 0 || foreign.Infeasible != 0 {
		t.Fatalf("tenant 2 sees %+v err=%v", foreign, err)
	}
}

// A finding whose operation was deleted since still lists, without order or network.
func TestNetworkFeasibility_KeepsFindingsOfDeletedOperations(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	gone := ids.New()
	msgs, _ := json.Marshal([]string{gone + ": SCHED-0009 NETWORK_INFEASIBLE: no edge", "not an operation id: SCHED-0009", ids.New() + ": placed late"})
	if _, err := admin.Exec(context.Background(), `insert into schedule_run (tenant_id, id, scope, mode, messages) values ($1, $2, 'site', 'jobs', $3)`,
		testkit.TenantID(1), ids.New(), msgs); err != nil {
		t.Fatal(err)
	}
	got, err := s.NetworkFeasibility(ctx, store.NetworkFeasibilityFilter{Limit: 10})
	if err != nil || len(got.Items) != 1 || got.Infeasible != 1 || got.InfeasibleOrders != 0 || got.Items[0].OperationID != gone || got.Items[0].OrderID != nil || got.Items[0].NetworkCode != nil ||
		got.Items[0].Detail != "SCHED-0009 NETWORK_INFEASIBLE: no edge" {
		t.Fatalf("got = %+v err=%v", got, err)
	}
}
