package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// trapMatrix makes nearest-neighbour expensive: A->B 1 then B->C 100, while
// A->C 2 then C->B 1 costs 3.
func trapMatrix(t *testing.T, ctx context.Context, s *store.Postgres, resourceID string) {
	t.Helper()
	if _, _, err := s.CreateSetupMatrix(ctx, store.SetupMatrix{ResourceID: resourceID, ActiveFrom: "2026-01-01", Currency: "USD",
		Entries: []domain.SetupEntry{
			{FromGroup: "A", ToGroup: "B", Minutes: 1, Cost: decimal.Zero},
			{FromGroup: "A", ToGroup: "C", Minutes: 2, Cost: decimal.Zero},
			{FromGroup: "B", ToGroup: "C", Minutes: 100, Cost: decimal.Zero},
			{FromGroup: "C", ToGroup: "B", Minutes: 1, Cost: decimal.Zero},
		}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
}

// A cp-sat campaign keeps a certified, cheaper solver order and says so with
// SCHED-0006; without an answer it keeps nearest-neighbour; the matrix the
// API prices the request with is the one in force on the planned start.
func TestSequenceCampaign_CPSAT(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	trapMatrix(t, ctx, s, r.ID)
	b, c := campaignOrder("B", "1", 60), campaignOrder("C", "1", 60)
	current := "A"
	newCampaign := func(rule string, orders ...store.CampaignOrder) store.Campaign {
		camp, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: "K", PlannedStart: at(0, 6, 0),
			SequenceRule: rule, CurrentSetupGroup: &current, Orders: orders}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		return camp
	}
	camp := newCampaign(domain.SequenceCPSAT, b, c)

	m, err := s.CampaignSetupMatrix(ctx, r.ID, camp.PlannedStart)
	if err != nil || m.Len() != 4 {
		t.Fatalf("matrix in force: %d entries err=%v", m.Len(), err)
	}
	if cm := domain.ChangeoverMatrix(m, current, camp.SetupOrders()); cm[0][2] != 2 || cm[2][1] != 1 {
		t.Fatalf("solver matrix = %v", cm)
	}

	heur, _, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: camp.ID, Version: camp.Version,
		Proposal: &domain.SolverProposal{Status: domain.SolverUnknown}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if heur.ChangeoverMinutes != 101 || heur.Plan.Optimized || heur.Plan.SolverStatus != domain.SolverUnknown ||
		len(heur.Plan.Warnings) != 1 || heur.Plan.Warnings[0].Code != domain.CodeOptimizationStatus {
		t.Fatalf("unknown answer: changeover %d plan %+v", heur.ChangeoverMinutes, heur.Plan)
	}

	opt, _, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: camp.ID, Version: heur.Version,
		Proposal: &domain.SolverProposal{Status: domain.SolverOptimal, OrderIDs: []string{c.OrderID, b.OrderID}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if opt.ChangeoverMinutes != 3 || !opt.Plan.Optimized || opt.Plan.SolverStatus != domain.SolverOptimal || opt.Orders[0].OrderID != c.OrderID ||
		opt.Plan.Warnings[0].Detail != "Optimization ended with status OPTIMAL; optimized schedule kept." {
		t.Fatalf("optimal answer: changeover %d orders %+v plan %+v", opt.ChangeoverMinutes, opt.Orders, opt.Plan)
	}

	// A proposal for a nearest-neighbour campaign is ignored.
	nn := newCampaign(domain.SequenceNearestNeighbor, campaignOrder("B", "1", 60), campaignOrder("C", "1", 60))
	got, _, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: nn.ID, Version: nn.Version,
		Proposal: &domain.SolverProposal{Status: domain.SolverOptimal, OrderIDs: []string{nn.Orders[1].OrderID, nn.Orders[0].OrderID}}}, store.Idem{})
	if err != nil || got.ChangeoverMinutes != 101 || got.Plan.SolverStatus != "" || len(got.Plan.Warnings) != 0 {
		t.Fatalf("nearest-neighbour campaign: %+v err=%v", got.Plan, err)
	}
}

func counterRow(t *testing.T, admin *pgxpool.Pool, resourceID string) (batches, minutes int, tenantID string) {
	t.Helper()
	if err := admin.QueryRow(context.Background(), `select batches_since, minutes_since, tenant_id::text from scheduling_cleanout_counter
		where resource_id = $1`, resourceID).Scan(&batches, &minutes, &tenantID); err != nil {
		t.Fatal(err)
	}
	return batches, minutes, tenantID
}

// BR-23.13 across campaigns: the counter a started campaign leaves is where
// the next one begins, and a campaign sequenced from a counter that has
// since moved must be sequenced again before it starts (SCHED-0123).
func TestStartCampaign_CarriesTheCleanoutCounter(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "FILL-1", true)
	if _, _, err := s.CreateCleanoutRule(ctx, store.CleanoutRule{SetupGroup: "P", DurationMinutes: 45, AfterBatches: intp(3)}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	plan := func(orders ...store.CampaignOrder) store.Campaign {
		c, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: "P", PlannedStart: at(0, 6, 0),
			SequenceRule: domain.SequenceManual, Orders: orders}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		c, _, err = s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first := plan(campaignOrder("P", "1", 60), campaignOrder("P", "1", 70))
	second := plan(campaignOrder("P", "1", 80), campaignOrder("P", "1", 90))
	if first.Plan.CarryIn != (domain.CleanoutCounter{}) || first.Plan.CarryOut != (domain.CleanoutCounter{Batches: 2, Minutes: 130}) || len(first.Plan.Cleanouts) != 0 {
		t.Fatalf("first plan = %+v", first.Plan)
	}

	first, _, err := s.StartCampaign(ctx, store.CampaignCommand{CampaignID: first.ID, Version: first.Version}, store.Idem{})
	if err != nil || first.Status != domain.CampaignStarted {
		t.Fatalf("start first: %+v err=%v", first, err)
	}
	if b, m, tid := counterRow(t, admin, r.ID); b != 2 || m != 130 || tid != testkit.TenantID(1) {
		t.Fatalf("counter after first = %d batches %d minutes tenant %s", b, m, tid)
	}

	// The second campaign was planned from a zero counter; starting it now
	// would skip the cleanout its third batch needs.
	if _, _, err := s.StartCampaign(ctx, store.CampaignCommand{CampaignID: second.ID, Version: second.Version}, store.Idem{}); problemCode(err) != store.CodeCleanoutCounterMoved {
		t.Fatalf("stale counter start: err=%v", err)
	}
	second, _, err = s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: second.ID, Version: second.Version}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Plan.CarryIn != (domain.CleanoutCounter{Batches: 2, Minutes: 130}) || len(second.Plan.Cleanouts) != 1 ||
		second.Plan.Cleanouts[0].AfterOrderID != second.Orders[0].OrderID || second.Plan.CarryOut != (domain.CleanoutCounter{Batches: 1, Minutes: 90}) {
		t.Fatalf("resequenced plan = %+v", second.Plan)
	}
	if _, _, err := s.StartCampaign(ctx, store.CampaignCommand{CampaignID: second.ID, Version: second.Version}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if b, m, _ := counterRow(t, admin, r.ID); b != 1 || m != 90 {
		t.Fatalf("counter after second = %d batches %d minutes, want 1 and 90", b, m)
	}
	// Another tenant's resource of the same id space starts from zero.
	other := tenantCtx(2)
	r2 := mustResource(t, other, s, "FILL-1", true)
	c2, _, err := s.CreateCampaign(other, store.Campaign{ResourceID: r2.ID, CampaignKey: "P", PlannedStart: at(0, 6, 0),
		SequenceRule: domain.SequenceManual, Orders: []store.CampaignOrder{campaignOrder("P", "1", 10)}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if c2, _, err = s.SequenceCampaign(other, store.CampaignCommand{CampaignID: c2.ID, Version: c2.Version}, store.Idem{}); err != nil || c2.Plan.CarryIn != (domain.CleanoutCounter{}) {
		t.Fatalf("tenant 2 plan %+v err=%v", c2.Plan, err)
	}
}
