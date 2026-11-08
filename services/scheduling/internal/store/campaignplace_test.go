package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// plannedCampaign builds the BR-23.12 campaign on a fresh resource: orders B
// (90 min) and C (120 min) from group A, a cleanout of 240 min after C at
// campaign end, one operation per order on the resource, sequenced.
func plannedCampaign(t *testing.T, ctx context.Context, s *store.Postgres, code string) (store.Campaign, store.Resource, []store.Operation) {
	t.Helper()
	r := mustResource(t, ctx, s, code, true)
	br2312Matrix(t, ctx, s, r.ID, "2027-01-01")
	if _, _, err := s.CreateCleanoutRule(ctx, store.CleanoutRule{SetupGroup: "C", DurationMinutes: 240, AtCampaignEnd: true}, store.Idem{}); err != nil &&
		problemCode(err) != store.CodeCleanoutDuplicate {
		t.Fatal(err)
	}
	ob, oc := campaignOrder("B", "200", 90), campaignOrder("C", "100", 120)
	current := "A"
	c, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: code, PlannedStart: at(0, 8, 0),
		SequenceRule: domain.SequenceNearestNeighbor, CurrentSetupGroup: &current, Orders: []store.CampaignOrder{oc, ob}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	opB := mustOperation(t, ctx, s, ob.OrderID, 10, 45, r.ID)
	opC := mustOperation(t, ctx, s, oc.OrderID, 10, 45, r.ID)
	c, _, err = s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	return c, r, []store.Operation{opB, opC}
}

func TestPlaceCampaign_JobsBackToBackWithCleanoutWindow(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	c, r, ops := plannedCampaign(t, ctx, s, "MIX-1")
	placed, replayed, err := s.PlaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version, HorizonDays: 60}, store.Idem{Key: "place-1", Fingerprint: "fp"})
	if err != nil || replayed {
		t.Fatalf("place: replayed=%v err=%v", replayed, err)
	}
	p := placed.Placement
	if p == nil || placed.PlacedAt == nil || len(p.Jobs) != 2 || len(p.Cleanouts) != 1 || placed.Version != c.Version+1 {
		t.Fatalf("placement = %+v campaign %+v", p, placed)
	}
	// B first: leading setup A->B 30 min, then 90 run minutes; C straight after with B->C 20 min.
	if p.Jobs[0].OperationID != ops[0].ID || !p.Jobs[0].Start.Equal(at(0, 8, 0)) || !p.Jobs[0].End.Equal(at(0, 10, 0)) || p.Jobs[0].ChangeoverMinutes != 30 {
		t.Fatalf("first job = %+v, want B 08:00-10:00 with the 30 min leading setup", p.Jobs[0])
	}
	if p.Jobs[1].OperationID != ops[1].ID || !p.Jobs[1].Start.Equal(p.Jobs[0].End) || !p.Jobs[1].End.Equal(at(0, 12, 20)) || p.Jobs[1].ChangeoverMinutes != 20 {
		t.Fatalf("second job = %+v, want C 10:00-12:20 with the 20 min changeover", p.Jobs[1])
	}
	// 12:20 leaves 220 open minutes; the 240 min cleanout is never split, so it takes the next working day.
	w := p.Cleanouts[0]
	if w.AfterOrderID != placed.Orders[1].OrderID || !w.Start.Equal(at(1, 8, 0)) || !w.End.Equal(at(1, 12, 0)) || w.Minutes != 240 {
		t.Fatalf("cleanout = %+v, want 240 min in one piece on Tuesday 08:00-12:00", w)
	}
	if placed.PlannedEnd == nil || !placed.PlannedEnd.Equal(at(1, 12, 0)) {
		t.Fatalf("plannedEnd = %v", placed.PlannedEnd)
	}
	// The window is closed capacity: the resource offers no open time inside it.
	iv, err := s.ResourceCapacity(ctx, r.ID, at(1, 0, 0), at(2, 0, 0))
	if err != nil || len(iv) != 1 || !iv[0].Start.Equal(at(1, 12, 0)) {
		t.Fatalf("capacity on the cleanout day = %+v err=%v, want only 12:00-16:00", iv, err)
	}
	if outboxCount(t, admin, schedevents.CampaignPlaced) != 1 || outboxCount(t, admin, schedevents.JobScheduled) != 2 {
		t.Fatalf("events: placed=%d job.scheduled=%d", outboxCount(t, admin, schedevents.CampaignPlaced), outboxCount(t, admin, schedevents.JobScheduled))
	}
	bars := barsOf(t, ctx, s)
	if len(bars) != 2 {
		t.Fatalf("gantt bars = %d, want the two campaign jobs", len(bars))
	}
	// A replay answers the stored result and publishes nothing new.
	again, replayed, err := s.PlaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version, HorizonDays: 60}, store.Idem{Key: "place-1", Fingerprint: "fp"})
	if err != nil || !replayed || again.Version != placed.Version || outboxCount(t, admin, schedevents.CampaignPlaced) != 1 {
		t.Fatalf("replay: replayed=%v version=%d err=%v", replayed, again.Version, err)
	}
	// A placed campaign keeps its orders and sequence.
	if _, _, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: placed.Version}, store.Idem{}); problemCode(err) != store.CodeCampaignPlaced {
		t.Fatalf("sequence while placed: %v", err)
	}
	if _, _, err := s.RemoveCampaignOrder(ctx, store.CampaignCommand{CampaignID: c.ID, Version: placed.Version, OrderID: placed.Orders[0].OrderID}, store.Idem{}); problemCode(err) != store.CodeCampaignPlaced {
		t.Fatalf("remove while placed: %v", err)
	}
	if _, err := s.GetCampaign(tenantCtx(2), c.ID); problemCode(err) != store.CodeCampaignNotFound {
		t.Fatalf("tenant 2: %v", err)
	}
}

func TestPlaceCampaign_ReplacesTheEarlierPlacementAndUnplaceFreesEverything(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	c, r, ops := plannedCampaign(t, ctx, s, "MIX-1")
	placed, _, err := s.PlaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version, HorizonDays: 60}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	later := at(2, 8, 0)
	moved, _, err := s.PlaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: placed.Version, Start: &later, HorizonDays: 60}, store.Idem{})
	if err != nil || !moved.Placement.Jobs[0].Start.Equal(later) || len(moved.Placement.Cleanouts) != 1 {
		t.Fatalf("re-place = %+v err=%v", moved.Placement, err)
	}
	if bars := barsOf(t, ctx, s); len(bars) != 2 {
		t.Fatalf("bars after re-place = %d, want 2 (the first placement is gone)", len(bars))
	}
	// A stale version is refused.
	if _, _, err := s.UnplaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: placed.Version}, store.Idem{}); problemCode(err) != store.CodeCampaignVersion {
		t.Fatalf("stale: %v", err)
	}
	free, _, err := s.UnplaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: moved.Version}, store.Idem{})
	if err != nil || free.Placement != nil || free.PlacedAt != nil || free.PlannedEnd != nil {
		t.Fatalf("unplace = %+v err=%v", free, err)
	}
	if len(barsOf(t, ctx, s)) != 0 {
		t.Fatal("jobs remain after unplace")
	}
	iv, err := s.ResourceCapacity(ctx, r.ID, at(3, 0, 0), at(4, 0, 0))
	if err != nil || len(iv) != 1 || !iv[0].Start.Equal(at(3, 8, 0)) {
		t.Fatalf("capacity after unplace = %+v err=%v, want the full shift back", iv, err)
	}
	got, err := s.ListOperations(ctx, store.OperationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range got {
		if op.Status != "open" {
			t.Fatalf("operation %s is %s after unplace, want open", op.ID, op.Status)
		}
	}
	_ = ops
	if outboxCount(t, admin, schedevents.CampaignUnplaced) != 1 || outboxCount(t, admin, schedevents.OrderUnscheduled) != 2 {
		t.Fatal("unplace events missing")
	}
	if _, _, err := s.UnplaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: free.Version}, store.Idem{}); problemCode(err) != store.CodeCampaignNotPlacedYet {
		t.Fatalf("second unplace: %v", err)
	}
	// Unplaced, the campaign can change again and be placed once more.
	if _, _, err := s.PlaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: free.Version, HorizonDays: 60}, store.Idem{}); err != nil {
		t.Fatalf("place again: %v", err)
	}
}

func TestPlaceCampaign_NeverInterleavesWithOtherWork(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	c, r, _ := plannedCampaign(t, ctx, s, "MIX-1")
	// Another order holds 09:00-10:00 on the same resource.
	other := mustOperation(t, ctx, s, ids.New(), 10, 60, r.ID)
	if _, _, err := s.RunSchedule(ctx, store.RunRequest{Scope: "selection", Mode: "jobs", Finite: true, Start: at(0, 9, 0), HorizonDays: 60, OperationIDs: []string{other.ID}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	placed, _, err := s.PlaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version, HorizonDays: 60}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range placed.Placement.Jobs {
		if j.Start.Before(at(0, 10, 0)) {
			t.Fatalf("job %+v starts before the other order ends: the campaign was interleaved", j)
		}
	}
	if !placed.Placement.Jobs[0].Start.Equal(at(0, 10, 0)) || !placed.Placement.Jobs[1].Start.Equal(placed.Placement.Jobs[0].End) {
		t.Fatalf("jobs = %+v, want the campaign to start behind the other order and run back to back", placed.Placement.Jobs)
	}
}

func TestPlaceCampaign_Refusals(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	br2312Matrix(t, ctx, s, r.ID, "2027-01-01")
	ob, oc := campaignOrder("B", "1", 60), campaignOrder("C", "1", 60)
	c, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: "K", PlannedStart: at(0, 8, 0), SequenceRule: domain.SequenceNearestNeighbor,
		Orders: []store.CampaignOrder{ob, oc}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cmd := store.CampaignCommand{CampaignID: c.ID, Version: c.Version, HorizonDays: 60}
	if _, _, err := s.PlaceCampaign(ctx, cmd, store.Idem{}); problemCode(err) != store.CodeCampaignNotPlanned {
		t.Fatalf("not sequenced: %v", err)
	}
	if _, _, err := s.UnplaceCampaign(ctx, cmd, store.Idem{}); problemCode(err) != store.CodeCampaignNotPlacedYet {
		t.Fatalf("never placed: %v", err)
	}
	c, _, err = s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Version = c.Version
	if _, _, err := s.PlaceCampaign(ctx, cmd, store.Idem{}); problemCode(err) != store.CodeCampaignOperation {
		t.Fatalf("orders without operations: %v", err)
	}
	// Two operations of one order can run here: it must be named.
	first := mustOperation(t, ctx, s, ob.OrderID, 10, 30, r.ID)
	second := mustOperation(t, ctx, s, ob.OrderID, 20, 30, r.ID)
	mustOperation(t, ctx, s, oc.OrderID, 10, 30, r.ID)
	if _, _, err := s.PlaceCampaign(ctx, cmd, store.Idem{}); problemCode(err) != store.CodeCampaignOperation {
		t.Fatalf("ambiguous operation: %v", err)
	}
	cmd.OperationIDs = map[string]string{ob.OrderID: second.ID}
	// Operation 10 comes first in order B and is not scheduled: the campaign
	// job could run ahead of it.
	if _, _, err := s.PlaceCampaign(ctx, cmd, store.Idem{}); problemCode(err) != domain.CodeCampaignPredecessor {
		t.Fatalf("unscheduled predecessor: %v", err)
	}
	if _, _, err := s.RunSchedule(ctx, store.RunRequest{Scope: "selection", Mode: "jobs", Finite: true, Start: at(0, 8, 0), HorizonDays: 60,
		OperationIDs: []string{first.ID}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	placed, _, err := s.PlaceCampaign(ctx, cmd, store.Idem{})
	if err != nil {
		t.Fatalf("named operation: %v", err)
	}
	var b store.CampaignJob
	for _, j := range placed.Placement.Jobs {
		if j.OrderID == ob.OrderID {
			b = j
		}
	}
	if b.OperationID != second.ID || b.Start.Before(at(0, 8, 30)) {
		t.Fatalf("order B job = %+v, want operation 20 after operation 10 ends at 08:30", b)
	}
	// A firmed job refuses unplacing and re-placing.
	if _, _, err := s.FirmJob(ctx, placed.Placement.Jobs[0].OperationID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UnplaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: placed.Version}, store.Idem{}); problemCode(err) != store.CodeOperationFirmed {
		t.Fatalf("firmed: %v", err)
	}
}

func TestCancelCampaign_DropsTheCleanoutWindows(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	c, r, _ := plannedCampaign(t, ctx, s, "MIX-1")
	placed, _, err := s.PlaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version, HorizonDays: 60}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, _, err := s.CancelCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: placed.Version}, store.Idem{})
	if err != nil || cancelled.PlacedAt != nil || cancelled.Placement != nil {
		t.Fatalf("cancel = %+v err=%v", cancelled, err)
	}
	iv, err := s.ResourceCapacity(ctx, r.ID, at(1, 0, 0), at(2, 0, 0))
	if err != nil || len(iv) != 1 || !iv[0].Start.Equal(at(1, 8, 0)) {
		t.Fatalf("capacity after cancel = %+v err=%v, want the window gone", iv, err)
	}
}

// The campaign job keeps the order's route (FR-23.3): it starts no earlier
// than its scheduled predecessor plus the queue between them, and the order's
// later operation moves forward behind it; a firmed later operation that
// would have to move refuses the placement.
func TestPlaceCampaign_KeepsTheOrdersRoute(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	c, _, ops := plannedCampaign(t, ctx, s, "MIX-1")
	orderB := ops[0].OrderID
	prep, pack := mustResource(t, ctx, s, "PREP", true), mustResource(t, ctx, s, "PACK", true)
	pred, _, err := s.CreateOperation(ctx, store.Operation{OrderID: orderB, OperNumber: 5, Minutes: 60, Priority: 100, ResourceIDs: []string{prep.ID},
		QueueAfterMinutes: 30}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	succ, _, err := s.CreateOperation(ctx, store.Operation{OrderID: orderB, OperNumber: 20, Minutes: 60, Priority: 100, ResourceIDs: []string{pack.ID},
		QueueBeforeMinutes: 15}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cmd := store.CampaignCommand{CampaignID: c.ID, Version: c.Version, HorizonDays: 60}
	if _, _, err := s.PlaceCampaign(ctx, cmd, store.Idem{}); problemCode(err) != domain.CodeCampaignPredecessor {
		t.Fatalf("predecessor not scheduled: %v", err)
	}
	// The order runs first: 5 on PREP 08:00-09:00, 10 on MIX-1 from 09:30, 20 on PACK behind it.
	if _, _, err := s.RunSchedule(ctx, store.RunRequest{Scope: "order", OrderID: orderB, Mode: "jobs", Finite: true, Start: at(0, 8, 0), HorizonDays: 60}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	before := barsOf(t, ctx, s)
	if !before[pred.ID].End.Equal(at(0, 9, 0)) {
		t.Fatalf("predecessor = %+v", before[pred.ID])
	}
	placed, _, err := s.PlaceCampaign(ctx, cmd, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	// B waits for operation 5 plus its 30 min queue: 09:30, 30 min leading setup and 90 min run; C follows at 11:30.
	jb, jc := placed.Placement.Jobs[0], placed.Placement.Jobs[1]
	if jb.OrderID != orderB || !jb.Start.Equal(at(0, 9, 30)) || !jb.End.Equal(at(0, 11, 30)) || !jc.Start.Equal(jb.End) {
		t.Fatalf("jobs = %+v, want B 09:30-11:30 behind its predecessor and C straight after", placed.Placement.Jobs)
	}
	// Operation 20 moved forward to the campaign job's end plus its 15 min queue.
	after := barsOf(t, ctx, s)
	if got := after[succ.ID]; !got.Start.Equal(at(0, 11, 45)) || !got.End.Equal(at(0, 12, 45)) {
		t.Fatalf("successor = %+v, want PACK 11:45-12:45", got)
	}
	if got := after[pred.ID]; !got.Start.Equal(before[pred.ID].Start) {
		t.Fatalf("predecessor moved: %+v", got)
	}
	if n := outboxCount(t, admin, schedevents.JobScheduled); n != 3+3 {
		t.Fatalf("job.scheduled = %d, want the run's three and the placement's two jobs plus the pushed successor", n)
	}
	// A firmed later operation that would have to move refuses the placement.
	if _, _, err := s.FirmJob(ctx, succ.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	later := at(2, 8, 0)
	if _, _, err := s.PlaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: placed.Version, Start: &later, HorizonDays: 60}, store.Idem{}); problemCode(err) != domain.CodeFirmed {
		t.Fatalf("firmed successor: %v", err)
	}
	// Nothing changed: the refused placement rolled back whole.
	if got := barsOf(t, ctx, s); !got[ops[0].ID].Start.Equal(jb.Start) || !got[succ.ID].Start.Equal(at(0, 11, 45)) {
		t.Fatalf("bars after the refusal = %+v", got)
	}
}

// FR-23.14, BR-23.13: the cleanout window is a read-only bar on the resource's
// Gantt row, filtered by resource like any bar, and never flagged as outside
// capacity although it is closed time.
func TestPlaceCampaign_CleanoutWindowIsAGanttBar(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	c, r, _ := plannedCampaign(t, ctx, s, "MIX-GANTT")
	placed, _, err := s.PlaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version, HorizonDays: 60}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	w := placed.Placement.Cleanouts[0]
	bars, err := s.Gantt(ctx, monday.AddDate(0, 0, -1), monday.AddDate(0, 0, 30), []string{r.ID})
	if err != nil {
		t.Fatal(err)
	}
	var cleanouts []store.Bar
	for _, b := range bars {
		if b.Cleanout {
			cleanouts = append(cleanouts, b)
		}
	}
	if len(bars) != 3 || len(cleanouts) != 1 {
		t.Fatalf("bars = %d cleanouts = %d, want two jobs and one cleanout", len(bars), len(cleanouts))
	}
	b := cleanouts[0]
	if b.ResourceID != r.ID || !b.Start.Equal(w.Start) || !b.End.Equal(w.End) || b.OrderID != w.AfterOrderID ||
		b.CampaignID == nil || *b.CampaignID != c.ID || b.JobID != c.ID || b.OutsideCapacity || b.Secondary || b.SetupGroup == nil || *b.SetupGroup != "C" {
		t.Fatalf("cleanout bar = %+v window %+v", b, w)
	}
	// Another resource's filter leaves it out; a window before it too.
	other := mustResource(t, ctx, s, "OTHER-GANTT", true)
	if got, err := s.Gantt(ctx, monday.AddDate(0, 0, -1), monday.AddDate(0, 0, 30), []string{other.ID}); err != nil || len(got) != 0 {
		t.Fatalf("bars on another resource = %d, %v", len(got), err)
	}
	// The window is half-open: one ending where the cleanout starts and one
	// starting where it ends both leave it out, one overlapping by a minute
	// at either edge includes it.
	countCleanouts := func(from, to time.Time) int {
		t.Helper()
		got, err := s.Gantt(ctx, from, to, []string{r.ID})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, b := range got {
			if b.Cleanout {
				n++
			}
		}
		return n
	}
	if n := countCleanouts(w.End, w.End.Add(time.Hour)); n != 0 {
		t.Fatalf("cleanouts in a window starting at its end = %d", n)
	}
	if n := countCleanouts(w.Start.Add(-time.Hour), w.Start); n != 0 {
		t.Fatalf("cleanouts in a window ending at its start = %d", n)
	}
	if n := countCleanouts(w.End.Add(-time.Minute), w.End.Add(time.Hour)); n != 1 {
		t.Fatalf("cleanouts in a window overlapping its end = %d", n)
	}
	if n := countCleanouts(w.Start.Add(-time.Hour), w.Start.Add(time.Minute)); n != 1 {
		t.Fatalf("cleanouts in a window overlapping its start = %d", n)
	}
	// Another tenant does not see it.
	if got, err := s.Gantt(tenantCtx(2), monday.AddDate(0, 0, -1), monday.AddDate(0, 0, 30), nil); err != nil || len(got) != 0 {
		t.Fatalf("bars for another tenant = %d, %v", len(got), err)
	}
	// Unplacing takes the bar off.
	if _, _, err := s.UnplaceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: placed.Version}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Gantt(ctx, monday.AddDate(0, 0, -1), monday.AddDate(0, 0, 30), []string{r.ID}); err != nil || len(got) != 0 {
		t.Fatalf("bars after unplace = %d, %v", len(got), err)
	}
}
