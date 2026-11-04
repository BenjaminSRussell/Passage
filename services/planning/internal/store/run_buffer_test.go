package store_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// TestRunBuffers covers what a plan run reads for DDMRP (FR-22.16): the
// site-scope buffer of an item whose site-scope coverage is a decoupling point,
// for the caller's tenant only; warehouse buffers and items whose coverage is
// not a decoupling point (priority-based alone is not) are not returned.
func TestRunBuffers(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.setup.maintain", "planning.read", "planning.run")
	profile, _, err := st.CreateBufferProfile(ctx, store.BufferProfileCreate{Spec: br2211Profile("BUF-RUN")})
	if err != nil {
		t.Fatal(err)
	}
	group, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("REQ", domain.CoverageRequirement)})
	if err != nil {
		t.Fatal(err)
	}
	site, wh := ids.New(), ids.New()
	managed, plain := ids.New(), ids.New()
	for _, item := range []string{managed, plain} {
		if _, err := st.UpsertDdmrpBuffer(ctx, br2211Buffer(t, item, site, "", profile.ID, 90)); err != nil {
			t.Fatal(err)
		}
		cov := domain.DefaultItemCoverage(item, site, group.ID)
		cov.DecouplingPoint = item != plain
		cov.PriorityBased = true
		if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: cov}); err != nil {
			t.Fatal(err)
		}
	}
	// A buffer in a warehouse scope is not the site's buffer.
	if _, err := st.UpsertDdmrpBuffer(ctx, br2211Buffer(t, ids.New(), site, wh, profile.ID, 10)); err != nil {
		t.Fatal(err)
	}
	all := []string{managed, plain}
	got, err := st.RunBuffers(ctx, site, "", all)
	if err != nil || len(got) != 1 {
		t.Fatalf("RunBuffers = %+v, %v; want the decoupling point only", got, err)
	}
	var b domain.RunBuffer
	for _, g := range got {
		if g.ItemID == managed {
			b = g
		}
		if g.ItemID == plain {
			t.Fatalf("an item that is not a decoupling point was returned: %+v", g)
		}
	}
	if b.Zone != domain.ZoneYellow || !b.PlannedQty.Equal(decimal.NewFromInt(115)) || !b.Priority.Equal(decimal.RequireFromString("0.5306")) || b.DLTDays.IsZero() {
		t.Fatalf("BR-22.11 buffer as read by the run: %+v", b)
	}
	other := testkit.Ctx(testkit.TenantID(2), "planning.run")
	if got, err := st.RunBuffers(other, site, "", all); err != nil || len(got) != 0 {
		t.Fatalf("other tenant sees %+v, %v", got, err)
	}
	if got, err := st.RunBuffers(ctx, ids.New(), "", all); err != nil || len(got) != 0 {
		t.Fatalf("other site: %+v, %v", got, err)
	}
	if got, err := st.RunBuffers(ctx, site, "", nil); err != nil || got != nil {
		t.Fatalf("no items: %+v, %v", got, err)
	}
}

// TestRunBuffers_LegalEntity: a run of a plan of one legal entity reads only
// that entity's buffers, and an entity-scoped caller never sees another's.
func TestRunBuffers_LegalEntity(t *testing.T) {
	st, _, _ := setup(t)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB, all := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB), scopedCtx(testkit.TenantID(1), "")
	profile, _, err := st.CreateBufferProfile(ctxA, store.BufferProfileCreate{Spec: br2211Profile("BUF-LE")})
	if err != nil {
		t.Fatal(err)
	}
	group, _, err := st.CreateCoverageGroup(ctxA, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("REQ", domain.CoverageRequirement)})
	if err != nil {
		t.Fatal(err)
	}
	item, site := ids.New(), ids.New()
	if _, err := st.UpsertDdmrpBuffer(ctxA, br2211Buffer(t, item, site, "", profile.ID, 90)); err != nil {
		t.Fatal(err)
	}
	cov := domain.DefaultItemCoverage(item, site, group.ID)
	cov.DecouplingPoint = true
	if _, _, err := st.CreateItemCoverage(ctxA, store.ItemCoverageCreate{Spec: cov}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		ctx    context.Context
		entity string
		want   int
	}{
		"entity A's plan":                 {ctxA, entityA, 1},
		"all-entity run of A's plan":      {all, entityA, 1},
		"all-entity run of B's plan":      {all, entityB, 0},
		"entity B caller":                 {ctxB, entityB, 0},
		"entity B caller naming entity A": {ctxB, entityA, 0},
	} {
		if got, err := st.RunBuffers(c.ctx, site, c.entity, []string{item}); err != nil || len(got) != c.want {
			t.Errorf("%s: RunBuffers = %+v, %v; want %d", name, got, err, c.want)
		}
	}
}

// TestRunPlan_StoresBufferPriorityAndRedZoneMessage: the order of a red-zone
// buffer is stored with the buffer's priority and zone, and the PLAN-0010
// message is stored and read back by its action (migration 0019).
func TestRunPlan_StoresBufferPriorityAndRedZoneMessage(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	item := ids.New()
	buffers := []domain.RunBuffer{{ItemID: item, DLTDays: decimal.NewFromInt(10), PlannedQty: decimal.NewFromInt(80), Priority: decimal.RequireFromString("0.2449"), Zone: domain.ZoneRed}}
	items := domain.ApplyBuffers([]domain.Item{
		{ID: item, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2},
		{ID: ids.New(), Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2,
			Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 5, Qty: decimal.NewFromInt(3), Priority: domain.PrioritySales}}},
	}, buffers)
	res, err := domain.Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	res = domain.AddBufferResults(res, buffers)
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-red", "in-red")); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 2 {
		t.Fatalf("orders = %+v, %v", orders, err)
	}
	var bufferOrder string
	for _, o := range orders {
		if o.ItemID != item {
			if o.PlanningPriority != nil || o.BufferZone != nil {
				t.Errorf("an order without a buffer has a priority: %+v", o)
			}
			continue
		}
		bufferOrder = o.Number
		if o.PlanningPriority == nil || !decimal.RequireFromString(*o.PlanningPriority).Equal(decimal.RequireFromString("0.2449")) || o.BufferZone == nil || *o.BufferZone != domain.ZoneRed {
			t.Errorf("buffer order = %+v", o)
		}
		got, err := st.GetPlannedOrder(ctx, o.ID)
		if err != nil || got.PlanningPriority == nil || got.BufferZone == nil {
			t.Errorf("get buffer order = %+v, %v", got, err)
		}
	}
	msgs, err := st.ListMessages(ctx, "MAIN", domain.ActionRedZone, "", "", 10)
	if err != nil || len(msgs) != 1 || msgs[0].SupplyRef != bufferOrder || !decimal.RequireFromString(msgs[0].Qty).Equal(decimal.NewFromInt(80)) {
		t.Fatalf("red-zone messages = %+v, %v", msgs, err)
	}
}

// TestListPlannedOrdersAndMessages_SortByPriority: sort=priority lists the
// orders and messages of buffers by planning priority, the lowest (the most
// urgent) first, and the orders without a buffer after them in number order.
// Without sort, only the buffer orders of an item whose site-scope item
// coverage is priorityBased lead (FR-22.16: a priority-based item is ordered
// by priority instead of date); every other order keeps number order.
func TestListPlannedOrdersAndMessages_SortByPriority(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.setup.maintain")
	plain, yellow, red1, red2 := ids.New(), ids.New(), ids.New(), ids.New()
	site := ids.New()
	group, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("REQ", domain.CoverageRequirement)})
	if err != nil {
		t.Fatal(err)
	}
	// red1 is priority-based at this site; red2 is priority-based at another
	// site only, so its orders here keep number order by default.
	for item, at := range map[string]string{red1: site, red2: ids.New(), yellow: site} {
		cov := domain.DefaultItemCoverage(item, at, group.ID)
		cov.DecouplingPoint = true
		cov.PriorityBased = item != yellow
		if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: cov}); err != nil {
			t.Fatal(err)
		}
	}
	buffers := []domain.RunBuffer{
		{ItemID: yellow, DLTDays: decimal.NewFromInt(10), PlannedQty: decimal.NewFromInt(115), Priority: decimal.RequireFromString("0.5306"), Zone: domain.ZoneYellow},
		{ItemID: red1, DLTDays: decimal.NewFromInt(10), PlannedQty: decimal.NewFromInt(80), Priority: decimal.RequireFromString("0.2449"), Zone: domain.ZoneRed},
		{ItemID: red2, DLTDays: decimal.NewFromInt(10), PlannedQty: decimal.NewFromInt(60), Priority: decimal.RequireFromString("0.1000"), Zone: domain.ZoneRed},
	}
	raw := []domain.Item{{ID: plain, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2,
		Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 5, Qty: decimal.NewFromInt(3), Priority: domain.PrioritySales}}}}
	for _, b := range buffers {
		raw = append(raw, domain.Item{ID: b.ItemID, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2})
	}
	res, err := domain.Run(0, domain.ApplyBuffers(raw, buffers))
	if err != nil {
		t.Fatal(err)
	}
	res = domain.AddBufferResults(res, buffers)
	req := runRequest(res, "run-sort", "in-sort")
	req.SiteID = site
	if _, _, err := st.RunPlan(ctx, req); err != nil {
		t.Fatal(err)
	}
	itemsOf := func(sort string) []string {
		t.Helper()
		orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{Sort: sort}, 10)
		if err != nil || len(orders) != 4 {
			t.Fatalf("orders (sort %q) = %+v, %v", sort, orders, err)
		}
		out := []string{}
		for _, o := range orders {
			out = append(out, o.ItemID)
		}
		return out
	}
	if got, want := itemsOf(store.SortPriority), []string{red2, red1, yellow, plain}; !slices.Equal(got, want) {
		t.Errorf("sort=priority items = %v, want %v", got, want)
	}
	byDefault, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(byDefault) != 4 {
		t.Fatalf("orders by default = %+v, %v", byDefault, err)
	}
	if byDefault[0].ItemID != red1 {
		t.Errorf("default order leads with %s, want the priority-based buffer %s", byDefault[0].ItemID, red1)
	}
	for i := 2; i < len(byDefault); i++ {
		if byDefault[i-1].Number > byDefault[i].Number {
			t.Errorf("default order after the priority-based buffer is not by number: %s before %s", byDefault[i-1].Number, byDefault[i].Number)
		}
	}
	msgs, err := st.ListMessages(ctx, "MAIN", domain.ActionRedZone, "", store.SortPriority, 10)
	if err != nil || len(msgs) != 2 || msgs[0].ItemID != red2 || msgs[1].ItemID != red1 {
		t.Fatalf("red-zone messages by priority = %+v, %v", msgs, err)
	}
	if msgs[0].PlanningPriority == nil || !decimal.RequireFromString(*msgs[0].PlanningPriority).Equal(decimal.RequireFromString("0.1")) || msgs[0].BufferZone == nil || *msgs[0].BufferZone != domain.ZoneRed {
		t.Errorf("message does not carry the buffer's priority and zone: %+v", msgs[0])
	}
	// By default the priority-based red1 leads although red2 is more urgent.
	msgs, err = st.ListMessages(ctx, "MAIN", domain.ActionRedZone, "", "", 10)
	if err != nil || len(msgs) != 2 || msgs[0].ItemID != red1 || msgs[1].ItemID != red2 {
		t.Fatalf("red-zone messages by default = %+v, %v", msgs, err)
	}
	// A message about supply that is not a planned order of the version carries no priority.
	all, err := st.ListMessages(ctx, "MAIN", "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if (m.PlanningPriority != nil) != (m.ItemID == red1 || m.ItemID == red2 || m.ItemID == yellow) {
			t.Errorf("message %+v: priority set only on messages about buffer orders", m)
		}
	}
}

// TestRunPlan_StoresSalesOnTopMessage: the PLAN-0015 message of an item whose
// sale could not consume forecast is stored and read back by its action
// (migration 0021 admits the action).
func TestRunPlan_StoresSalesOnTopMessage(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	item := ids.New()
	res, err := domain.Run(0, []domain.Item{{ID: item, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2,
		SalesOnTop: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 4, Qty: decimal.NewFromInt(30)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-top", "in-top")); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.ListMessages(ctx, "MAIN", domain.ActionSalesOnTop, "", "", 10)
	if err != nil || len(msgs) != 1 || msgs[0].ItemID != item || msgs[0].DemandRef != "SO-1" || !decimal.RequireFromString(msgs[0].Qty).Equal(decimal.NewFromInt(30)) {
		t.Fatalf("sales-on-top messages = %+v, %v", msgs, err)
	}
}

// TestRunPlan_StoresNoVendorMessageWithCatalogText: the PLAN-0008 message of a
// purchase without a vendor is stored (migration 0022 admits the action) and
// read back with its catalog code and rendered text; a vendor given by the run
// is stored on the order.
func TestRunPlan_StoresNoVendorMessageWithCatalogText(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	bare, withVendor, vendor := ids.New(), ids.New(), ids.New()
	items := []domain.Item{
		{ID: bare, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2,
			Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 9, Qty: decimal.NewFromInt(5)}}},
		{ID: withVendor, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2,
			Demand: []domain.Requirement{{Ref: "SO-2", RefType: domain.RefSales, Date: 9, Qty: decimal.NewFromInt(6)}}},
	}
	res, err := domain.Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	res = domain.ApplyDefaultVendors(res, map[string]string{withVendor: vendor})
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-vendor", "in-vendor")); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.ListMessages(ctx, "MAIN", domain.ActionNoVendor, "", "", 10)
	if err != nil || len(msgs) != 1 || msgs[0].ItemID != bare || msgs[0].Code != "PLAN-0008" ||
		msgs[0].Text != "No vendor for planned purchase of "+bare+"; using none." || !decimal.RequireFromString(msgs[0].Qty).Equal(decimal.NewFromInt(5)) {
		t.Fatalf("no-vendor messages = %+v, %v", msgs, err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{ItemID: withVendor}, 10)
	if err != nil || len(orders) != 1 || orders[0].VendorID == nil || *orders[0].VendorID != vendor {
		t.Fatalf("orders = %+v, %v", orders, err)
	}
}

// TestRunPlan_StoresLotSplitMessageWithCatalogText: the PLAN-0005 warning of a
// requirement split over the item's maximum order quantity is stored (migration
// 0028 admits the action) and read back with its code and rendered text. It can
// only be dismissed (applying it is 422), and a rerun that raises it again
// unchanged carries the dismissal (slice 30 identity) without a new message.raised.v1.
func TestRunPlan_StoresLotSplitMessageWithCatalogText(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.order.maintain")
	item := ids.New()
	res, err := domain.Run(0, []domain.Item{{ID: item, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2,
		MaxOrderQty: decimal.NewFromInt(100), Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 9, Qty: decimal.NewFromInt(250)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-split", "in-split")); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.ListMessages(ctx, "MAIN", domain.ActionLotSplit, "", "", 10)
	if err != nil || len(msgs) != 1 || msgs[0].ItemID != item || msgs[0].Code != "PLAN-0005" || msgs[0].DelayDays != 3 ||
		msgs[0].Text != "Requirement for "+item+" splits into 3 orders (max 100)." {
		t.Fatalf("lot-split messages = %+v, %v", msgs, err)
	}
	split := msgs[0]
	if _, _, err := st.DecideMessage(ctx, split.ID, split.Version, store.MessageDecision{Apply: true}); problemCode(err) != store.CodeMessageNotActionable {
		t.Fatalf("apply a lot-split warning: %v; want %s", err, store.CodeMessageNotActionable)
	}
	if _, _, err := st.DecideMessage(ctx, split.ID, split.Version, store.MessageDecision{Note: "max is the truck size"}); err != nil {
		t.Fatal(err)
	}
	raised := outboxCount(t, admin, planevents.MessageRaised)
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-split-2", "in-split-2")); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListMessages(ctx, "MAIN", domain.ActionLotSplit, "all", "", 10)
	if err != nil || len(all) != 1 || all[0].ID == split.ID || all[0].Status != "dismissed" || all[0].CarriedFrom == nil || *all[0].CarriedFrom != split.ID {
		t.Fatalf("lot-split after an unchanged rerun = %+v, %v; want the dismissal carried", all, err)
	}
	if got := outboxCount(t, admin, planevents.MessageRaised); got != raised {
		t.Fatalf("message.raised.v1 = %d after the rerun; want %d (a carried warning is not raised again)", got, raised)
	}
}

// TestRunPlan_StoresBOMCycleMessageWithCatalogText: a cyclic BOM no longer refuses
// the run (PLAN-0004); migration 0029 admits the bom-cycle action and the warning is
// read back with its code and text. It can only be dismissed, and a rerun of the
// same plan date that raises it again carries the dismissal without a new
// message.raised.v1, like the other warnings.
func TestRunPlan_StoresBOMCycleMessageWithCatalogText(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.order.maintain")
	a, b := ids.New(), ids.New()
	res, err := domain.Run(0, []domain.Item{
		{ID: a, Coverage: domain.CoverageRequirement, OrderType: domain.OrderProduction, LeadTimeDays: 1,
			Demand:     []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 9, Qty: decimal.NewFromInt(5)}},
			Components: []domain.BOMLine{{ItemID: b, Qty: decimal.NewFromInt(2)}}},
		{ID: b, Coverage: domain.CoverageRequirement, OrderType: domain.OrderProduction, LeadTimeDays: 1,
			Components: []domain.BOMLine{{ItemID: a, Qty: decimal.NewFromInt(1)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-cycle", "in-cycle")); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.ListMessages(ctx, "MAIN", domain.ActionBOMCycle, "", "", 10)
	if err != nil || len(msgs) != 1 || msgs[0].ItemID != b || msgs[0].Code != "PLAN-0004" ||
		msgs[0].Text != "BOM cycle detected for "+b+"; low-level codes not updated." {
		t.Fatalf("bom-cycle messages = %+v, %v", msgs, err)
	}
	if _, _, err := st.DecideMessage(ctx, msgs[0].ID, msgs[0].Version, store.MessageDecision{Apply: true}); problemCode(err) != store.CodeMessageNotActionable {
		t.Fatalf("apply a bom-cycle warning: %v; want %s", err, store.CodeMessageNotActionable)
	}
	cycle := msgs[0]
	if _, _, err := st.DecideMessage(ctx, cycle.ID, cycle.Version, store.MessageDecision{Note: "BOM fix requested"}); err != nil {
		t.Fatal(err)
	}
	raised := outboxCount(t, admin, planevents.MessageRaised)
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-cycle-2", "in-cycle-2")); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListMessages(ctx, "MAIN", domain.ActionBOMCycle, "all", "", 10)
	if err != nil || len(all) != 1 || all[0].ID == cycle.ID || all[0].Status != "dismissed" || all[0].CarriedFrom == nil || *all[0].CarriedFrom != cycle.ID {
		t.Fatalf("bom-cycle after an unchanged rerun = %+v, %v; want the dismissal carried", all, err)
	}
	if got := outboxCount(t, admin, planevents.MessageRaised); got != raised {
		t.Fatalf("message.raised.v1 = %d after the rerun; want %d", got, raised)
	}
}

// TestRunPlan_StoresNoQuotaSourceMessageWithCatalogText: an order no source of its
// quota arrangement can take is stored as a PLAN-0009 message (migration 0029 admits
// the action) read back with its code and text, and it can only be dismissed.
func TestRunPlan_StoresNoQuotaSourceMessageWithCatalogText(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.order.maintain")
	item, arrangement := ids.New(), ids.New()
	res, err := domain.Run(0, []domain.Item{{ID: item, Coverage: domain.CoverageRequirement, OrderType: domain.OrderProduction, LeadTimeDays: 1,
		Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 9, Qty: decimal.NewFromInt(5)}}}})
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	res, out, err := domain.ApplyQuota(asOf, res, []domain.RunQuota{{ArrangementID: arrangement, Spec: domain.QuotaArrangementSpec{
		ItemID: item, ValidFrom: asOf, ValidTo: asOf.AddDate(0, 0, 90),
		Sources: []domain.QuotaSourceSpec{{ID: "src", Type: domain.SourceVendor, VendorID: ids.New(), Quota: decimal.NewFromInt(1)}}}}})
	if err != nil || out.Unassigned != 1 {
		t.Fatalf("outcome %+v err %v; want the production order unassigned", out, err)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-quota-none", "in-quota-none")); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.ListMessages(ctx, "MAIN", domain.ActionNoQuotaSource, "", "", 10)
	if err != nil || len(msgs) != 1 || msgs[0].ItemID != item || msgs[0].DemandRef != arrangement || msgs[0].Code != "PLAN-0009" ||
		msgs[0].Text != "Quota arrangement "+arrangement+" has no usable source in 2026-11-11." {
		t.Fatalf("no-quota-source messages = %+v, %v", msgs, err)
	}
	if _, _, err := st.DecideMessage(ctx, msgs[0].ID, msgs[0].Version, store.MessageDecision{Apply: true}); problemCode(err) != store.CodeMessageNotActionable {
		t.Fatalf("apply a no-quota-source message: %v; want %s", err, store.CodeMessageNotActionable)
	}
}
