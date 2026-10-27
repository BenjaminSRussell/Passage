package store_test

import (
	"slices"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// FR-23.14 campaign proposals through the database: scheduled orders that
// share a key on one resource are proposed together; orders already in a
// campaign, orders without a setup group, lone orders, other resources'
// filters, other tenants and jobs outside the window are left out.
func TestProposeCampaigns_GroupsScheduledOrdersBySharedKey(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r1, r2 := mustResource(t, ctx, s, "MIX-1", true), mustResource(t, ctx, s, "MIX-2", true)
	a := groupOperation(t, ctx, s, "WHITE", 60, r1.ID, 20)
	b := groupOperation(t, ctx, s, "WHITE", 60, r1.ID, 20)
	inCampaign := groupOperation(t, ctx, s, "WHITE", 60, r1.ID, 20)
	lone := groupOperation(t, ctx, s, "WHITE", 60, r2.ID, 20)
	mustOperation(t, ctx, s, ids.New(), 10, 60, r1.ID) // no setup group
	fam := func(group string) store.Operation {
		op, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 30, Priority: 100, SetupGroup: strp(group),
			SetupMinutes: 5, Attributes: map[string]string{domain.CampaignKeyAttribute: "FAMILY-1"}, ResourceIDs: []string{r1.ID}}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	f1, f2 := fam("BLACK"), fam("GREY")
	if _, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r1.ID, CampaignKey: "WHITE", PlannedStart: at(0, 8, 0), SequenceRule: domain.SequenceManual,
		Orders: []store.CampaignOrder{{OrderID: inCampaign.OrderID, SetupGroup: "WHITE", Quantity: decimal.NewFromInt(5), RunMinutes: 60}}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ProposeCampaigns(ctx, store.ProposalFilter{From: at(0, 0, 0), WindowDays: 14})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]domain.ProposedCampaign{}
	for _, p := range got {
		byKey[p.CampaignKey+"/"+p.ResourceID] = p
	}
	if len(got) != 2 {
		t.Fatalf("proposals = %+v, want WHITE and FAMILY-1 on MIX-1", got)
	}
	white, ok := byKey["WHITE/"+r1.ID]
	if !ok || !sameOrders(white, a.OrderID, b.OrderID) {
		t.Fatalf("WHITE proposal = %+v, want orders a and b only", white)
	}
	if w := white.Orders[0]; w.OperationID != a.ID && w.OperationID != b.ID || w.SetupGroup != "WHITE" || w.RunMinutes != 60 || w.RouteSetupMinutes != 15 || w.DueAt == nil || w.Start.Location().String() != "UTC" {
		t.Fatalf("WHITE first order = %+v", w)
	}
	if !white.PlannedStart.Equal(white.Orders[0].Start) || white.RunMinutes != 120 {
		t.Fatalf("WHITE planned start %v run minutes %d", white.PlannedStart, white.RunMinutes)
	}
	family, ok := byKey["FAMILY-1/"+r1.ID]
	if !ok || !sameOrders(family, f1.OrderID, f2.OrderID) || family.Orders[0].Start.After(family.Orders[1].Start) {
		t.Fatalf("FAMILY-1 proposal = %+v, want the BLACK and GREY orders in start order", family)
	}
	for _, p := range got {
		for _, o := range p.Orders {
			if o.OrderID == inCampaign.OrderID || o.OrderID == lone.OrderID {
				t.Fatalf("order %s should not be proposed", o.OrderID)
			}
		}
	}

	if only, err := s.ProposeCampaigns(ctx, store.ProposalFilter{From: at(0, 0, 0), WindowDays: 14, ResourceID: r2.ID}); err != nil || len(only) != 0 {
		t.Fatalf("MIX-2 proposals = %+v err=%v, want none (one order)", only, err)
	}
	if later, err := s.ProposeCampaigns(ctx, store.ProposalFilter{From: at(1, 0, 0), WindowDays: 14}); err != nil || len(later) != 0 {
		t.Fatalf("proposals from Tuesday = %+v err=%v, want none (every job starts on Monday)", later, err)
	}
	if other, err := s.ProposeCampaigns(tenantCtx(2), store.ProposalFilter{From: at(0, 0, 0), WindowDays: 14}); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 proposals = %+v err=%v, want none", other, err)
	}
}

func sameOrders(p domain.ProposedCampaign, want ...string) bool {
	got := make([]string, 0, len(p.Orders))
	for _, o := range p.Orders {
		got = append(got, o.OrderID)
	}
	slices.Sort(got)
	slices.Sort(want)
	return slices.Equal(got, want)
}
