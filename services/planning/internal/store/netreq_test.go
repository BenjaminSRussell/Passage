package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
)

// latePlan is a purchased item with on-hand 5, an open PO of 10 on day 3 and a
// sales line of 30 on day 1 that a 5-day lead time cannot meet: the planned
// order of 25 is expected on day 5, so the run raises delay messages.
func latePlan(t *testing.T) (domain.Result, string) {
	t.Helper()
	item := ids.New()
	res, err := domain.Run(0, []domain.Item{{
		ID: item, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 5,
		OnHand:   decimal.NewFromInt(5),
		Receipts: []domain.Requirement{{Ref: "PO-OPEN", RefType: "purchase-line", Date: 3, Qty: decimal.NewFromInt(10)}},
		Demand:   []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 1, Qty: decimal.NewFromInt(30), Priority: domain.PrioritySales}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) == 0 || len(res.Net) == 0 {
		t.Fatalf("fixture needs messages and net rows: %+v", res)
	}
	return res, item
}

// TestRunPlan_NetRequirementsAndMessageEvents: the run stores the net
// requirements view of its version (FR-22.12) that GET /net-requirements
// reads back for the current version only, and writes one message.raised.v1
// per stored message in the same transaction (FR-22.7).
func TestRunPlan_NetRequirementsAndMessageEvents(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	res, item := latePlan(t)
	v, _, err := st.RunPlan(ctx, runRequest(res, "n-1", "in-1"))
	if err != nil {
		t.Fatal(err)
	}

	rows, err := st.ListNetRequirements(ctx, "MAIN", item, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(res.Net) {
		t.Fatalf("rows %+v; want %d", rows, len(res.Net))
	}
	for i, n := range res.Net {
		got := rows[i]
		if got.ItemID != item || got.Date != dayOf(n.Day) || !dec(t, got.GrossRequirements).Equal(n.Gross) ||
			!dec(t, got.ScheduledReceipts).Equal(n.ScheduledReceipts) || !dec(t, got.PlannedReceipts).Equal(n.PlannedReceipts) ||
			!dec(t, got.PAB).Equal(n.PAB) || !dec(t, got.NetRequirement).Equal(n.Net) {
			t.Fatalf("row %d = %+v; engine %+v", i, got, n)
		}
	}
	if limited, err := st.ListNetRequirements(ctx, "MAIN", item, 1); err != nil || len(limited) != 1 || limited[0].Date != rows[0].Date {
		t.Fatalf("limit 1: %+v, %v", limited, err)
	}
	other := testkit.Ctx(testkit.TenantID(2), "planning.read")
	if leaked, err := st.ListNetRequirements(other, "MAIN", item, 100); err != nil || len(leaked) != 0 {
		t.Fatalf("tenant 2 sees %+v, %v", leaked, err)
	}

	// A new version replaces the view: the item of the first run is not in it.
	res2 := oneItemPlan(t)
	if _, _, err := st.RunPlan(ctx, runRequest(res2, "n-2", "in-2")); err != nil {
		t.Fatal(err)
	}
	if stale, err := st.ListNetRequirements(ctx, "MAIN", item, 100); err != nil || len(stale) != 0 {
		t.Fatalf("superseded version still read: %+v, %v", stale, err)
	}

	var stored int
	if err := admin.QueryRow(context.Background(), `select count(*) from action_message where plan_version_id = $1`, v.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	out, err := admin.Query(context.Background(), `select payload from outbox where type = $1 order by id`, planevents.MessageRaised)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var raised []planevents.MessageRaisedData
	for out.Next() {
		var raw []byte
		if err := out.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Data planevents.MessageRaisedData `json:"data"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		raised = append(raised, envelope.Data)
	}
	if len(raised) != stored || stored != len(res.Messages) {
		t.Fatalf("message.raised events %d, stored messages %d, engine messages %d", len(raised), stored, len(res.Messages))
	}
	for _, m := range raised {
		var action string
		if err := admin.QueryRow(context.Background(), `select action from action_message where id = $1 and plan_version_id = $2`, m.MessageID, v.ID).Scan(&action); err != nil {
			t.Fatalf("event %+v names no stored message: %v", m, err)
		}
		if action != m.Action || m.ItemID != item || m.VersionID != v.ID || m.PlanCode != "MAIN" || m.SuggestedDate == "" {
			t.Fatalf("event %+v does not match stored action %s", m, action)
		}
	}
}

func dayOf(day int) string {
	return runRequest(domain.Result{}, "", "").AsOf.AddDate(0, 0, day).Format("2006-01-02")
}

func dec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
