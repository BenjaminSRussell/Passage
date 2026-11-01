package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func campaignOrders() []CampaignOrder {
	due := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	return []CampaignOrder{
		{SetupOrder: SetupOrder{OrderID: "oc", SetupGroup: "C", Due: due, Minutes: 120}, Qty: w(60)},
		{SetupOrder: SetupOrder{OrderID: "ob", SetupGroup: "B", Due: due, Minutes: 100}, Qty: w(40)},
	}
}

func TestCanJoin(t *testing.T) {
	cases := map[string]bool{
		CampaignProposed:  true,
		CampaignPlanned:   true,
		CampaignStarted:   false,
		CampaignCompleted: false,
		CampaignCancelled: false,
		"bogus":           false,
	}
	for status, open := range cases {
		c := CanJoin(status)
		if open != (c == nil) {
			t.Fatalf("CanJoin(%s) = %v, want open=%v", status, c, open)
		}
		if c != nil && c.Code != CodeCampaignStarted {
			t.Fatalf("CanJoin(%s) code = %s, want %s", status, c.Code, CodeCampaignStarted)
		}
	}
}

// TestPlanCampaign_AC23_11 covers AC-23.11: with the BR-23.12 matrix, orders B
// and C from state A are sequenced B then C with 50 changeover minutes and a
// cleanout job after C.
func TestPlanCampaign_AC23_11(t *testing.T) {
	plan, err := PlanCampaign(CampaignInput{
		Status:       CampaignPlanned,
		Matrix:       brMatrix(t),
		Cleanouts:    []CleanoutRule{{SetupGroup: "C", DurationMinutes: 240, AtCampaignEnd: true, CleaningSetID: "WET-CLEAN"}},
		CurrentGroup: "A",
		Orders:       campaignOrders(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{plan.Sequence.Orders[0].OrderID, plan.Sequence.Orders[1].OrderID}; got[0] != "ob" || got[1] != "oc" {
		t.Fatalf("sequence = %v, want [ob oc]", got)
	}
	if plan.ChangeoverMinutes != 50 {
		t.Fatalf("changeover = %d, want 50", plan.ChangeoverMinutes)
	}
	want := PlannedCleanout{AfterOrderID: "oc", SetupGroup: "C", Minutes: 240, Reason: CleanoutCampaignEnd, CleaningSetID: "WET-CLEAN"}
	if len(plan.Cleanouts) != 1 || plan.Cleanouts[0] != want {
		t.Fatalf("cleanouts = %+v, want [%+v]", plan.Cleanouts, want)
	}
	if plan.RunMinutes != 220 || !plan.TotalQty.Equal(w(100)) || plan.CleanoutMinutes != 240 || plan.DurationMinutes() != 510 {
		t.Fatalf("run %d qty %s cleanout %d duration %d", plan.RunMinutes, plan.TotalQty, plan.CleanoutMinutes, plan.DurationMinutes())
	}
}

func TestPlanCampaign_ManualKeepsTheGivenOrder(t *testing.T) {
	plan, err := PlanCampaign(CampaignInput{Status: CampaignProposed, Rule: SequenceManual, Matrix: brMatrix(t), CurrentGroup: "A", Orders: campaignOrders()})
	if err != nil {
		t.Fatal(err)
	}
	// A->C 90, then C->B 60: the planner's choice costs 150 minutes (BR-23.12 example).
	if plan.Sequence.Orders[0].OrderID != "oc" || plan.ChangeoverMinutes != 150 || len(plan.Cleanouts) != 0 {
		t.Fatalf("manual plan = %+v", plan)
	}
}

func TestPlanCampaign_UnknownRuleRefused(t *testing.T) {
	if _, err := PlanCampaign(CampaignInput{Status: CampaignProposed, Rule: "genetic", Matrix: brMatrix(t), Orders: campaignOrders()}); err == nil {
		t.Fatal("an unknown sequence rule must be refused")
	}
}

// TestPlanCampaign_BatchCounterInsertsCleanouts covers BR-23.13: with a rule of
// "after 2 batches", five orders of one group get a cleanout after the 2nd and
// 4th, and the campaign-end cleanout after the 5th. The counter carried in from
// before the campaign counts too.
func TestPlanCampaign_BatchCounterInsertsCleanouts(t *testing.T) {
	var orders []CampaignOrder
	for _, id := range []string{"o1", "o2", "o3", "o4", "o5"} {
		orders = append(orders, CampaignOrder{SetupOrder: SetupOrder{OrderID: id, SetupGroup: "P", Minutes: 60}, Qty: w(10)})
	}
	rule := CleanoutRule{SetupGroup: "P", DurationMinutes: 30, AfterBatches: 2, AtCampaignEnd: true}
	plan, err := PlanCampaign(CampaignInput{Status: CampaignProposed, Matrix: SetupMatrix{}, Cleanouts: []CleanoutRule{rule}, CurrentGroup: "P", Orders: orders})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range plan.Cleanouts {
		got[c.AfterOrderID] = c.Reason
	}
	if len(got) != 3 || got["o2"] != CleanoutBatches || got["o4"] != CleanoutBatches || got["o5"] != CleanoutCampaignEnd {
		t.Fatalf("cleanouts = %+v", plan.Cleanouts)
	}
	if plan.CleanoutMinutes != 90 {
		t.Fatalf("cleanout minutes = %d, want 90", plan.CleanoutMinutes)
	}

	batchesOnly := CleanoutRule{SetupGroup: "P", DurationMinutes: 30, AfterBatches: 2}
	carried, err := PlanCampaign(CampaignInput{Status: CampaignProposed, Matrix: SetupMatrix{}, Cleanouts: []CleanoutRule{batchesOnly}, CurrentGroup: "P",
		Carry: CleanoutCounter{Batches: 1}, Orders: orders[:1]})
	if err != nil {
		t.Fatal(err)
	}
	if len(carried.Cleanouts) != 1 || carried.Cleanouts[0].Reason != CleanoutBatches {
		t.Fatalf("carried counter: %+v", carried.Cleanouts)
	}
}

// TestPlanCampaign_RunTimeCounter covers the "after M hours" half of BR-23.13.
func TestPlanCampaign_RunTimeCounter(t *testing.T) {
	orders := []CampaignOrder{
		{SetupOrder: SetupOrder{OrderID: "o1", SetupGroup: "P", Minutes: 300}, Qty: w(1)},
		{SetupOrder: SetupOrder{OrderID: "o2", SetupGroup: "P", Minutes: 300}, Qty: w(1)},
		{SetupOrder: SetupOrder{OrderID: "o3", SetupGroup: "P", Minutes: 100}, Qty: w(1)},
	}
	plan, err := PlanCampaign(CampaignInput{Status: CampaignProposed, Rule: SequenceManual, Cleanouts: []CleanoutRule{{SetupGroup: "P", DurationMinutes: 45, AfterMinutes: 600}},
		CurrentGroup: "P", Orders: orders})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Cleanouts) != 1 || plan.Cleanouts[0].AfterOrderID != "o2" || plan.Cleanouts[0].Reason != CleanoutRunTime {
		t.Fatalf("cleanouts = %+v, want one run-time cleanout after o2", plan.Cleanouts)
	}
}

// A changeover that requires a cleaning set cleans the resource and restarts
// the batch counter.
func TestPlanCampaign_CleaningChangeoverResetsCounter(t *testing.T) {
	m, err := NewSetupMatrix([]SetupEntry{{FromGroup: "PEANUT", ToGroup: "FREE", Minutes: 240, CleaningSetID: "ALLERGEN"}})
	if err != nil {
		t.Fatal(err)
	}
	orders := []CampaignOrder{
		{SetupOrder: SetupOrder{OrderID: "o1", SetupGroup: "PEANUT", Minutes: 60}, Qty: w(1)},
		{SetupOrder: SetupOrder{OrderID: "o2", SetupGroup: "FREE", Minutes: 60}, Qty: w(1)},
	}
	plan, err := PlanCampaign(CampaignInput{Status: CampaignProposed, Rule: SequenceManual, Matrix: m, CurrentGroup: "PEANUT",
		Carry: CleanoutCounter{Batches: 5}, Cleanouts: []CleanoutRule{{SetupGroup: "FREE", DurationMinutes: 30, AfterBatches: 2}}, Orders: orders})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Cleanouts) != 0 {
		t.Fatalf("the allergen changeover already cleaned the line, got %+v", plan.Cleanouts)
	}
	if plan.Sequence.Changeovers[0].CleaningSetID != "ALLERGEN" || plan.ChangeoverMinutes != 240 {
		t.Fatalf("changeover = %+v", plan.Sequence.Changeovers)
	}
}

func TestPlanCampaign_RefusesFrozenAndInvalid(t *testing.T) {
	m := brMatrix(t)
	t.Run("started campaign is frozen", func(t *testing.T) {
		_, err := PlanCampaign(CampaignInput{Status: CampaignStarted, Matrix: m, CurrentGroup: "A", Orders: campaignOrders()})
		var c *Conflict
		if !errors.As(err, &c) || c.Code != CodeCampaignStarted {
			t.Fatalf("err = %v, want SCHED-0011 conflict", err)
		}
	})
	t.Run("no orders", func(t *testing.T) {
		if _, err := PlanCampaign(CampaignInput{Status: CampaignPlanned, Matrix: m}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("duplicate order", func(t *testing.T) {
		o := campaignOrders()
		o = append(o, o[0])
		if _, err := PlanCampaign(CampaignInput{Status: CampaignPlanned, Matrix: m, Orders: o}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("zero quantity", func(t *testing.T) {
		o := campaignOrders()
		o[0].Qty = decimal.Zero
		if _, err := PlanCampaign(CampaignInput{Status: CampaignPlanned, Matrix: m, Orders: o}); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("limits", func(t *testing.T) {
		// From state A the plan runs 220 minutes and changes over for 50.
		cases := []CampaignLimits{{MaxOrders: 1}, {MaxQty: decimal.RequireFromString("99.5")}, {MaxMinutes: 269}}
		for _, lim := range cases {
			if _, err := PlanCampaign(CampaignInput{Status: CampaignPlanned, Matrix: m, CurrentGroup: "A", Limits: lim, Orders: campaignOrders()}); err == nil {
				t.Fatalf("limits %+v accepted an over-full campaign", lim)
			}
		}
		exact := CampaignLimits{MaxOrders: 2, MaxQty: w(100), MaxMinutes: 270}
		if _, err := PlanCampaign(CampaignInput{Status: CampaignPlanned, Matrix: m, CurrentGroup: "A", Limits: exact, Orders: campaignOrders()}); err != nil {
			t.Fatalf("exact limits rejected: %v", err)
		}
	})
}

// TestPlanCampaign_Property checks on random campaigns that every order appears
// exactly once, run minutes are the sum of order minutes, and the duration is
// run + changeover + cleanout minutes.
func TestPlanCampaign_Property(t *testing.T) {
	groups := []string{"A", "B", "C"}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 15).Draw(t, "n")
		orders := make([]CampaignOrder, n)
		want := 0
		for i := range orders {
			m := rapid.IntRange(0, 500).Draw(t, "minutes")
			want += m
			orders[i] = CampaignOrder{SetupOrder: SetupOrder{OrderID: string(rune('a' + i)),
				SetupGroup: rapid.SampledFrom(groups).Draw(t, "group"), Minutes: m,
				RouteSetupMinutes: rapid.IntRange(0, 60).Draw(t, "route setup")}, Qty: w(int64(i + 1))}
		}
		rules := []CleanoutRule{{SetupGroup: rapid.SampledFrom(groups).Draw(t, "rule group"), DurationMinutes: 30,
			AfterBatches: rapid.IntRange(0, 4).Draw(t, "after batches"), AtCampaignEnd: rapid.Bool().Draw(t, "at end")}}
		plan, err := PlanCampaign(CampaignInput{Status: CampaignPlanned, Cleanouts: rules, CurrentGroup: "A", Orders: orders})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, o := range plan.Sequence.Orders {
			seen[o.OrderID] = true
		}
		if plan.RunMinutes != want || len(plan.Sequence.Orders) != n || len(seen) != n {
			t.Fatalf("run %d want %d, sequenced %d of %d", plan.RunMinutes, want, len(plan.Sequence.Orders), n)
		}
		if plan.CleanoutMinutes != 30*len(plan.Cleanouts) || plan.DurationMinutes() != plan.RunMinutes+plan.ChangeoverMinutes+plan.CleanoutMinutes {
			t.Fatalf("duration bookkeeping: %+v", plan)
		}
	})
}

func TestCanCompleteAndCancel(t *testing.T) {
	for status, want := range map[string]struct{ complete, cancel string }{
		CampaignProposed:  {CodeCampaignNotStarted, ""},
		CampaignPlanned:   {CodeCampaignNotStarted, ""},
		CampaignStarted:   {"", ""},
		CampaignCompleted: {CodeCampaignStarted, CodeCampaignStarted},
		CampaignCancelled: {CodeCampaignStarted, CodeCampaignStarted},
	} {
		code := func(c *Conflict) string {
			if c == nil {
				return ""
			}
			return c.Code
		}
		if got := code(CanComplete(status)); got != want.complete {
			t.Errorf("CanComplete(%s) = %q, want %q", status, got, want.complete)
		}
		if got := code(CanCancel(status)); got != want.cancel {
			t.Errorf("CanCancel(%s) = %q, want %q", status, got, want.cancel)
		}
	}
}
