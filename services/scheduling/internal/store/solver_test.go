package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
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

// Cancelling a campaign releases its orders; a started one gives back the
// cleanout counter only while nothing has moved it since. Completing needs a
// started campaign.
func TestCompleteAndCancelCampaign(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "FILL-2", true)
	order := campaignOrder("P", "1", 60)
	mk := func(orders ...store.CampaignOrder) store.Campaign {
		c, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: "P", PlannedStart: at(0, 6, 0),
			SequenceRule: domain.SequenceManual, Orders: orders}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	seq := func(c store.Campaign) store.Campaign {
		c, _, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cmd := func(c store.Campaign) store.CampaignCommand {
		return store.CampaignCommand{CampaignID: c.ID, Version: c.Version}
	}

	// A planned campaign cannot be completed, but can be cancelled; its order is free again.
	c := seq(mk(order))
	if _, _, err := s.CompleteCampaign(ctx, cmd(c), store.Idem{}); problemCode(err) != store.CodeCampaignNotStarted {
		t.Fatalf("complete a planned campaign: err=%v", err)
	}
	stale := cmd(c)
	stale.Version--
	if _, _, err := s.CancelCampaign(ctx, stale, store.Idem{}); problemCode(err) != store.CodeCampaignVersion {
		t.Fatalf("stale version: err=%v", err)
	}
	cc := cmd(c)
	cc.Reason = "demand dropped"
	idem := store.Idem{Key: "cancel-1", Fingerprint: "f"}
	c, replayed, err := s.CancelCampaign(ctx, cc, idem)
	if err != nil || replayed || c.Status != domain.CampaignCancelled || c.CancelledAt == nil || c.CancelReason == nil || *c.CancelReason != "demand dropped" || len(c.Orders) != 0 {
		t.Fatalf("cancel planned: %+v err=%v", c, err)
	}
	if again, replayed, err := s.CancelCampaign(ctx, cc, idem); err != nil || !replayed || again.Version != c.Version {
		t.Fatalf("cancel replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	if n := outboxCount(t, admin, schedevents.CampaignCancelled); n != 1 {
		t.Fatalf("campaign.cancelled events = %d, want 1 (replay must not add one)", n)
	}
	if _, _, err := s.CancelCampaign(ctx, cmd(c), store.Idem{}); problemCode(err) != domain.CodeCampaignStarted {
		t.Fatalf("cancel twice: err=%v", err)
	}
	// The released order joins a new campaign.
	c = seq(mk(order))
	// Start, then cancel: the counter returns to what it was.
	c, _, err = s.StartCampaign(ctx, cmd(c), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if b, m, _ := counterRow(t, admin, r.ID); b != 1 || m != 60 {
		t.Fatalf("counter after start = %d/%d", b, m)
	}
	if c, _, err = s.CancelCampaign(ctx, cmd(c), store.Idem{}); err != nil || c.Status != domain.CampaignCancelled {
		t.Fatalf("cancel started: %+v err=%v", c, err)
	}
	if b, m, _ := counterRow(t, admin, r.ID); b != 0 || m != 0 {
		t.Fatalf("counter after cancel = %d/%d, want it restored to 0/0", b, m)
	}
	var cancelled schedevents.CampaignCancelledData
	if err := admin.QueryRow(context.Background(), `select payload->'data' from outbox where type = $1 and aggregate_id = $2`,
		schedevents.CampaignCancelled, c.ID).Scan(&cancelled); err != nil {
		t.Fatal(err)
	}
	if len(cancelled.OrderIDs) != 1 || cancelled.OrderIDs[0] != order.OrderID || !cancelled.CounterRestored || cancelled.Version != c.Version {
		t.Fatalf("campaign.cancelled data = %+v", cancelled)
	}

	// Complete a started campaign; its orders stay, and it cannot be cancelled afterwards.
	c = seq(mk(order))
	if c, _, err = s.StartCampaign(ctx, cmd(c), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	c, _, err = s.CompleteCampaign(ctx, cmd(c), store.Idem{})
	if err != nil || c.Status != domain.CampaignCompleted || c.CompletedAt == nil || len(c.Orders) != 1 {
		t.Fatalf("complete: %+v err=%v", c, err)
	}
	if n := outboxCount(t, admin, schedevents.CampaignCompleted); n != 1 {
		t.Fatalf("campaign.completed events = %d, want 1", n)
	}
	if _, _, err := s.CancelCampaign(ctx, cmd(c), store.Idem{}); problemCode(err) != domain.CodeCampaignStarted {
		t.Fatalf("cancel a completed campaign: err=%v", err)
	}

	// A later campaign that moved the counter keeps it: cancelling the earlier
	// started one does not roll the counter back.
	a := seq(mk(campaignOrder("P", "1", 10)))
	if a, _, err = s.StartCampaign(ctx, cmd(a), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	b := seq(mk(campaignOrder("P", "1", 20)))
	if b, _, err = s.StartCampaign(ctx, cmd(b), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	before, bm, _ := counterRow(t, admin, r.ID)
	if _, _, err = s.CancelCampaign(ctx, cmd(a), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if after, am, _ := counterRow(t, admin, r.ID); after != before || am != bm {
		t.Fatalf("counter moved from %d/%d to %d/%d by cancelling a superseded campaign", before, bm, after, am)
	}

	// Another tenant cannot see the campaign, so it can neither complete nor cancel it.
	other := tenantCtx(2)
	if _, _, err := s.CompleteCampaign(other, cmd(b), store.Idem{}); problemCode(err) != store.CodeCampaignNotFound {
		t.Fatalf("complete from another tenant: err=%v", err)
	}
	if _, _, err := s.CancelCampaign(other, cmd(b), store.Idem{}); problemCode(err) != store.CodeCampaignNotFound {
		t.Fatalf("cancel from another tenant: err=%v", err)
	}
	if got, err := s.GetCampaign(ctx, b.ID); err != nil || got.Status != domain.CampaignStarted || got.Version != b.Version {
		t.Fatalf("campaign after the other tenant's attempts: %+v err=%v", got, err)
	}
}
