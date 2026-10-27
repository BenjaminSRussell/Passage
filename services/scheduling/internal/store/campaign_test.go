package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func intp(v int) *int { return &v }

// br2312Matrix is the BR-23.12 example: A->B 30, A->C 90, B->C 20, C->B 60.
func br2312Matrix(t *testing.T, ctx context.Context, s *store.Postgres, resourceID, activeFrom string) store.SetupMatrix {
	t.Helper()
	m, created, err := s.CreateSetupMatrix(ctx, store.SetupMatrix{ResourceID: resourceID, ActiveFrom: activeFrom, Currency: "USD",
		Entries: []domain.SetupEntry{
			{FromGroup: "A", ToGroup: "B", Minutes: 30, Cost: decimal.RequireFromString("45.50")},
			{FromGroup: "A", ToGroup: "C", Minutes: 90, Cost: decimal.RequireFromString("120")},
			{FromGroup: "B", ToGroup: "C", Minutes: 20, Cost: decimal.RequireFromString("30")},
			{FromGroup: "C", ToGroup: "B", Minutes: 60, Cost: decimal.RequireFromString("80")},
		}}, store.Idem{})
	if err != nil || !created {
		t.Fatalf("create matrix: created=%v err=%v", created, err)
	}
	return m
}

func campaignOrder(group string, qty string, minutes int) store.CampaignOrder {
	return store.CampaignOrder{OrderID: ids.New(), SetupGroup: group, Quantity: decimal.RequireFromString(qty), RunMinutes: minutes, RouteSetupMinutes: 15}
}

func campaignEvents(t *testing.T, admin *pgxpool.Pool) int {
	t.Helper()
	return outboxCount(t, admin, schedevents.CampaignSequenced)
}

func TestNetwork_CreateReplayAndRules(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	v1, f1, f2 := mustResource(t, ctx, s, "V1", true), mustResource(t, ctx, s, "F1", true), mustResource(t, ctx, s, "F2", true)
	in := store.Network{Code: "MIX-FILL", Name: "Mixing to filling", ActiveFrom: "2027-01-01", Edges: []domain.NetworkEdge{
		{From: v1.ID, To: f2.ID, TransferMinutes: 20, MaxWaitMinutes: intp(120)},
		{From: v1.ID, To: f1.ID, TransferMinutes: 10, MaxWaitMinutes: intp(120)},
	}}
	idem := store.Idem{Key: "net-1", Fingerprint: "fp-1"}
	n, created, err := s.CreateNetwork(ctx, in, idem)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if len(n.Edges) != 2 || n.Edges[0].To != f1.ID || *n.Edges[0].MaxWaitMinutes != 120 || n.ActiveTo != nil || n.Version != 1 {
		t.Fatalf("network = %+v, want two edges with F1 (shorter transfer) first", n)
	}
	again, created, err := s.CreateNetwork(ctx, in, idem)
	if err != nil || created || again.ID != n.ID {
		t.Fatalf("replay: id=%s created=%v err=%v, want the same network", again.ID, created, err)
	}
	if _, _, err := s.CreateNetwork(ctx, in, store.Idem{Key: "net-1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse with another body: err=%v", err)
	}
	if _, _, err := s.CreateNetwork(ctx, in, store.Idem{}); problemCode(err) != store.CodeNetworkDuplicate {
		t.Fatalf("duplicate code: err=%v", err)
	}
	unknown := in
	unknown.Code, unknown.Edges = "OTHER", []domain.NetworkEdge{{From: v1.ID, To: ids.New()}}
	if _, _, err := s.CreateNetwork(ctx, unknown, store.Idem{}); problemCode(err) != store.CodeUnknownResource {
		t.Fatalf("edge to an unknown resource: err=%v", err)
	}
	got, err := s.GetNetwork(ctx, n.ID)
	if err != nil || len(got.Edges) != 2 {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	if list, err := s.ListNetworks(ctx); err != nil || len(list) != 1 || len(list[0].Edges) != 2 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if _, err := s.GetNetwork(tenantCtx(2), n.ID); problemCode(err) != store.CodeNetworkNotFound {
		t.Fatalf("tenant 2 reads tenant 1 network: err=%v", err)
	}
}

func TestSetupMatrix_VersionsAndExactCosts(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	first := br2312Matrix(t, ctx, s, r.ID, "2027-01-01")
	second := br2312Matrix(t, ctx, s, r.ID, "2027-06-01")
	if first.Version != 1 || second.Version != 2 {
		t.Fatalf("versions = %d, %d, want 1 and 2", first.Version, second.Version)
	}
	if len(first.Entries) != 4 || !first.Entries[0].Cost.Equal(decimal.RequireFromString("45.5")) {
		t.Fatalf("entries = %+v, want A->B first at 45.50", first.Entries)
	}
	list, err := s.ListSetupMatrices(ctx, r.ID)
	if err != nil || len(list) != 2 || list[0].ID != second.ID {
		t.Fatalf("list = %+v err=%v, want newest version first", list, err)
	}
	if _, _, err := s.CreateSetupMatrix(ctx, store.SetupMatrix{ResourceID: ids.New(), ActiveFrom: "2027-01-01", Currency: "USD",
		Entries: first.Entries}, store.Idem{}); problemCode(err) != store.CodeUnknownResource {
		t.Fatalf("matrix on an unknown resource: err=%v", err)
	}
	if _, err := s.GetSetupMatrix(ctx, ids.New()); problemCode(err) != store.CodeMatrixNotFound {
		t.Fatalf("unknown matrix: err=%v", err)
	}
}

func TestCleanoutRuleAndCapacityBlock_OnePerKey(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "PAINT-1", true)
	rule, created, err := s.CreateCleanoutRule(ctx, store.CleanoutRule{SetupGroup: "C", DurationMinutes: 240, AfterBatches: intp(5), AtCampaignEnd: true}, store.Idem{})
	if err != nil || !created || rule.AfterHours != nil || *rule.AfterBatches != 5 {
		t.Fatalf("rule = %+v created=%v err=%v", rule, created, err)
	}
	if _, _, err := s.CreateCleanoutRule(ctx, store.CleanoutRule{SetupGroup: "C", DurationMinutes: 60, AtCampaignEnd: true}, store.Idem{}); problemCode(err) != store.CodeCleanoutDuplicate {
		t.Fatalf("second rule for group C: err=%v", err)
	}
	white := []domain.Predicate{{Attribute: "color", Op: "=", Value: "white"}}
	b, created, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: r.ID, Code: "MON-WHITE",
		Windows: []domain.Shift{{Weekday: 1, StartMinute: 360, EndMinute: 840}}, Condition: white, Priority: 10, Active: true}, store.Idem{})
	if err != nil || !created || len(b.Windows) != 1 || b.Condition[0].Value != "white" || b.StartAt != nil {
		t.Fatalf("block = %+v created=%v err=%v", b, created, err)
	}
	start, end := at(2, 6, 0), at(2, 14, 0)
	dated, _, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: r.ID, Code: "WED-ANY", StartAt: &start, EndAt: &end,
		Condition: []domain.Predicate{}, Active: true}, store.Idem{})
	if err != nil || dated.Windows != nil || !dated.StartAt.Equal(start) || len(dated.Condition) != 0 {
		t.Fatalf("dated block = %+v err=%v", dated, err)
	}
	if _, _, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: r.ID, Code: "MON-WHITE", StartAt: &start, EndAt: &end,
		Condition: white, Active: true}, store.Idem{}); problemCode(err) != store.CodeBlockDuplicate {
		t.Fatalf("duplicate block code: err=%v", err)
	}
	blocks, err := s.ListCapacityBlocks(ctx, r.ID)
	if err != nil || len(blocks) != 2 || blocks[0].Code != "MON-WHITE" {
		t.Fatalf("blocks = %+v err=%v, want the priority 10 block first", blocks, err)
	}
	if rules, err := s.ListCleanoutRules(tenantCtx(2)); err != nil || len(rules) != 0 {
		t.Fatalf("tenant 2 sees cleanout rules %+v err=%v", rules, err)
	}
}

// AC-23.11 through the store: orders C and B on the BR-23.12 matrix from
// current group A are sequenced B then C with 50 changeover minutes, and the
// cleanout rule of C puts a cleanout after C at campaign end.
func TestSequenceCampaign_AC23_11(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	m := br2312Matrix(t, ctx, s, r.ID, "2027-01-01")
	if _, _, err := s.CreateCleanoutRule(ctx, store.CleanoutRule{SetupGroup: "C", DurationMinutes: 240, AtCampaignEnd: true}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	orderC, orderB := campaignOrder("C", "100", 120), campaignOrder("B", "200", 90)
	current := "A"
	c, created, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: "FAMILY-1", PlannedStart: at(0, 6, 0),
		SequenceRule: domain.SequenceNearestNeighbor, CurrentSetupGroup: &current, Orders: []store.CampaignOrder{orderC, orderB}},
		store.Idem{Key: "camp-1", Fingerprint: "fp"})
	if err != nil || !created || c.Status != domain.CampaignProposed || c.OrderCount != 2 || c.Version != 1 || c.Plan != nil {
		t.Fatalf("create = %+v created=%v err=%v", c, created, err)
	}

	seq, replayed, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: 1}, store.Idem{Key: "seq-1", Fingerprint: "fp"})
	if err != nil || replayed {
		t.Fatalf("sequence: replayed=%v err=%v", replayed, err)
	}
	if seq.Status != domain.CampaignPlanned || seq.Version != 2 || seq.ChangeoverMinutes != 50 || seq.RunMinutes != 210 || seq.CleanoutMinutes != 240 {
		t.Fatalf("sequenced = %+v, want planned v2 with 50 changeover, 210 run and 240 cleanout minutes", seq)
	}
	if seq.Orders[0].OrderID != orderB.OrderID || *seq.Orders[0].Sequence != 1 || seq.Orders[1].OrderID != orderC.OrderID || *seq.Orders[1].Sequence != 2 {
		t.Fatalf("orders = %+v, want B then C", seq.Orders)
	}
	p := seq.Plan
	if p == nil || p.MatrixID != m.ID || len(p.Changeovers) != 2 || p.Changeovers[0].FromGroup != "A" || p.Changeovers[0].BeforeOrderID != "" ||
		!p.Changeovers[0].Cost.Equal(decimal.RequireFromString("45.5")) || len(p.Warnings) != 0 {
		t.Fatalf("plan = %+v, want the leading A->B setup and B->C with no warnings", p)
	}
	if len(p.Cleanouts) != 1 || p.Cleanouts[0].AfterOrderID != orderC.OrderID || p.Cleanouts[0].Reason != domain.CleanoutCampaignEnd {
		t.Fatalf("cleanouts = %+v, want one after C at campaign end", p.Cleanouts)
	}
	if n := campaignEvents(t, admin); n != 1 {
		t.Fatalf("campaign.sequenced events = %d, want 1", n)
	}

	// A replay returns the stored answer and publishes nothing new.
	again, replayed, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: 1}, store.Idem{Key: "seq-1", Fingerprint: "fp"})
	if err != nil || !replayed || again.Version != 2 || campaignEvents(t, admin) != 1 {
		t.Fatalf("replay: version=%d replayed=%v err=%v", again.Version, replayed, err)
	}
	// A stale version is refused.
	if _, _, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: 1}, store.Idem{}); problemCode(err) != store.CodeCampaignVersion {
		t.Fatalf("stale version: err=%v", err)
	}
	if _, err := s.GetCampaign(tenantCtx(2), c.ID); problemCode(err) != store.CodeCampaignNotFound {
		t.Fatalf("tenant 2 reads tenant 1 campaign: err=%v", err)
	}
}

func TestSequenceCampaign_MissingEntryWarnsAndUsesRouteSetup(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	a, z := campaignOrder("A", "1", 60), campaignOrder("Z", "1", 60)
	c, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: "K", PlannedStart: at(0, 6, 0),
		SequenceRule: domain.SequenceManual, Orders: []store.CampaignOrder{a, z}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	seq, _, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if seq.ChangeoverMinutes != 15 || len(seq.Plan.Warnings) != 1 || seq.Plan.Warnings[0].Code != domain.CodeMissingSetupEntry || seq.Plan.MatrixID != "" {
		t.Fatalf("plan = %+v changeover=%d, want the route setup (15) and one SCHED-0012 warning", seq.Plan, seq.ChangeoverMinutes)
	}
	if seq.Orders[0].OrderID != a.OrderID {
		t.Fatalf("manual rule reordered the campaign: %+v", seq.Orders)
	}
}

func TestCampaignOrders_JoinLeaveAndFreeze(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	br2312Matrix(t, ctx, s, r.ID, "2027-01-01")
	maxOrders := 2
	first := campaignOrder("B", "10", 30)
	c, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: "K", PlannedStart: at(0, 6, 0), MaxOrders: &maxOrders,
		SequenceRule: domain.SequenceNearestNeighbor, Orders: []store.CampaignOrder{first}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err = s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{})
	if err != nil || c.Status != domain.CampaignPlanned {
		t.Fatalf("sequence: %+v err=%v", c, err)
	}

	// Adding an order sends the campaign back to proposed and clears the plan.
	second := campaignOrder("C", "10", 30)
	c, _, err = s.AddCampaignOrder(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version, Order: second}, store.Idem{Key: "add-1", Fingerprint: "fp"})
	if err != nil || c.Status != domain.CampaignProposed || c.Plan != nil || c.OrderCount != 2 || c.Orders[1].Position != 2 || c.Orders[1].Sequence != nil {
		t.Fatalf("add: %+v err=%v", c, err)
	}
	// The order limit holds.
	if _, _, err := s.AddCampaignOrder(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version, Order: campaignOrder("B", "1", 1)}, store.Idem{}); problemCode(err) != store.CodeCampaignInvalid {
		t.Fatalf("third order over max 2: err=%v", err)
	}
	// An order belongs to one campaign only.
	if _, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: "K2", PlannedStart: at(0, 6, 0),
		SequenceRule: domain.SequenceManual, Orders: []store.CampaignOrder{second}}, store.Idem{}); problemCode(err) != store.CodeOrderInCampaign {
		t.Fatalf("order in two campaigns: err=%v", err)
	}
	// Removing an unknown order is a 404; removing a member works.
	if _, _, err := s.RemoveCampaignOrder(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version, OrderID: ids.New()}, store.Idem{}); problemCode(err) != store.CodeCampaignOrderNotFound {
		t.Fatalf("remove unknown order: err=%v", err)
	}
	c, _, err = s.RemoveCampaignOrder(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version, OrderID: second.OrderID}, store.Idem{})
	if err != nil || c.OrderCount != 1 {
		t.Fatalf("remove: %+v err=%v", c, err)
	}

	// Start needs a sequenced campaign, then freezes it (SCHED-0011).
	if _, _, err := s.StartCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{}); problemCode(err) != store.CodeCampaignNotPlanned {
		t.Fatalf("start a proposed campaign: err=%v", err)
	}
	c, _, err = s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err = s.StartCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{})
	if err != nil || c.Status != domain.CampaignStarted || c.StartedAt == nil {
		t.Fatalf("start: %+v err=%v", c, err)
	}
	if n := outboxCount(t, admin, schedevents.CampaignStarted); n != 1 {
		t.Fatalf("campaign.started events = %d, want 1", n)
	}
	late := campaignOrder("B", "1", 1)
	if _, _, err := s.AddCampaignOrder(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version, Order: late}, store.Idem{}); problemCode(err) != domain.CodeCampaignStarted {
		t.Fatalf("join a started campaign: err=%v", err)
	}
	if _, _, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{}); problemCode(err) != domain.CodeCampaignStarted {
		t.Fatalf("resequence a started campaign: err=%v", err)
	}

	list, err := s.ListCampaigns(ctx, store.CampaignFilter{Status: domain.CampaignStarted, Limit: 10})
	if err != nil || len(list) != 1 || list[0].ID != c.ID || list[0].Orders != nil {
		t.Fatalf("list started = %+v err=%v", list, err)
	}
	if list, err := s.ListCampaigns(ctx, store.CampaignFilter{Status: domain.CampaignProposed, Limit: 10}); err != nil || len(list) != 0 {
		t.Fatalf("list proposed = %+v err=%v", list, err)
	}
}
