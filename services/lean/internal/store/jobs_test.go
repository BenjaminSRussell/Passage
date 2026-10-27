package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	leanevents "github.com/BenjaminSRussell/passage/services/lean/internal/events"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

// cellFlow is a two-activity flow: Mix on the mixing cell, then Fill on the
// filling cell, which needs picking when picking is true.
func cellFlow(t *testing.T, s *store.Postgres, ctx context.Context, mixCell, fillCell string, picking bool) store.Flow {
	t.Helper()
	f, _, err := s.CreateFlow(ctx, store.Flow{ValueStreamID: ids.New(), SiteID: ids.New(), Name: "Cell line " + ids.New()[:8], Activities: []store.Activity{
		{Sequence: 1, Name: "Mix", Kind: "process", CycleTimeS: 40, WorkCellResourceID: &mixCell, OutputLocationID: ptr(ids.New()), Predecessors: []int{}},
		{Sequence: 2, Name: "Fill", Kind: "process", CycleTimeS: 30, WorkCellResourceID: &fillCell, OutputLocationID: ptr(ids.New()), Predecessors: []int{1},
			PickingRequired: picking},
	}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func done(reported, scrap string, units int) domain.Completion {
	return domain.Completion{ReportedQty: dec(reported), ScrapQty: dec(scrap), HandlingUnits: units}
}

func jobsOf(t *testing.T, s *store.Postgres, ctx context.Context, kanbanID string) (store.Kanban, []store.Job) {
	t.Helper()
	k, err := s.GetKanban(ctx, kanbanID)
	if err != nil {
		t.Fatal(err)
	}
	return k, k.Jobs
}

// AC-24.6 through the work-cell board: event kanbans of a rule with a flow get
// one job per activity; starting the first job runs the card, completing the
// last completes it with that job's report, and a Planned job dragged to
// Completed is refused (AC-24.7).
func TestJobs_DriveTheCardThroughTheWorkCellBoard(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1)
	mixCell, fillCell := ids.New(), ids.New()
	f := cellFlow(t, s, ctx, mixCell, fillCell, false)
	rule := eventRule("FILL-JOB")
	rule.FlowID = &f.ID
	r, _, err := s.CreateRule(ctx, rule, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID, DemandQty: dec("50"), EventRefType: ptr("sales-line"), EventRefID: ptr("SO-7/10")}, store.Idem{})
	if err != nil || len(cards) != 3 || cards[0].JobCount != 2 {
		t.Fatalf("generate %+v %v", cards, err)
	}
	k, jobs := jobsOf(t, s, ctx, cards[0].ID)
	if len(jobs) != 2 || jobs[0].Sequence != 1 || jobs[0].State != domain.Planned || jobs[1].ActivityName != "Fill" || *jobs[1].WorkCellResourceID != fillCell {
		t.Fatalf("jobs of a new event card %+v", jobs)
	}
	mixBoard, err := s.BoardJobs(ctx, store.BoardFilter{WorkCellID: mixCell})
	if err != nil || len(mixBoard) != 3 || mixBoard[0].ActivityName != "Mix" {
		t.Fatalf("mix cell board %d %v", len(mixBoard), err)
	}

	// A card with jobs does not move to In progress or Completed by hand.
	if _, _, err := s.MoveKanban(ctx, k.ID, domain.Prepared, k.Version, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	k, jobs = jobsOf(t, s, ctx, k.ID)
	if jobs[0].State != domain.Prepared || jobs[1].State != domain.Prepared {
		t.Fatalf("jobs did not follow the card to prepared: %+v", jobs)
	}
	if _, _, err := s.MoveKanban(ctx, k.ID, domain.InProgress, k.Version, store.Idem{}); problemCode(err) != domain.CodeMove {
		t.Fatalf("card with jobs moved to in progress by hand: %v", err)
	}
	// AC-24.7: a job that has not started cannot complete.
	if _, _, err := s.CompleteJob(ctx, jobs[0].ID, jobs[0].Version, done("20", "0", 1), store.Idem{}); problemCode(err) != domain.CodeMove {
		t.Fatalf("prepared job completed: %v", err)
	}
	if _, _, err := s.StartJob(ctx, jobs[0].ID, jobs[0].Version+5, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale start: %v", err)
	}

	mix, fresh, err := s.StartJob(ctx, jobs[0].ID, jobs[0].Version, store.Idem{Key: "start-mix", Fingerprint: "fp"})
	if err != nil || !fresh || mix.State != domain.InProgress || mix.StartedAt == nil || mix.CardState != domain.InProgress {
		t.Fatalf("start mix fresh=%v %+v %v", fresh, mix, err)
	}
	if again, fresh, err := s.StartJob(ctx, jobs[0].ID, jobs[0].Version, store.Idem{Key: "start-mix", Fingerprint: "fp"}); err != nil || fresh || again.Version != mix.Version {
		t.Fatalf("start replay fresh=%v %+v %v", fresh, again, err)
	}
	if _, _, err := s.StartJob(ctx, jobs[0].ID, mix.Version, store.Idem{}); problemCode(err) != domain.CodeMove {
		t.Fatalf("started twice: %v", err)
	}
	mix, _, err = s.CompleteJob(ctx, mix.ID, mix.Version, done("19", "1", 1), store.Idem{})
	if err != nil || mix.State != domain.Completed || !mix.ReportedQty.Equal(dec("19")) || mix.CardState != domain.InProgress {
		t.Fatalf("complete mix %+v %v", mix, err)
	}
	fill, _, err := s.StartJob(ctx, jobs[1].ID, jobs[1].Version, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	fill, _, err = s.CompleteJob(ctx, fill.ID, fill.Version, done("18", "1", 2), store.Idem{Key: "done-fill", Fingerprint: "fp"})
	if err != nil || fill.CardState != domain.Completed || *fill.HandlingUnits != 2 {
		t.Fatalf("complete fill %+v %v", fill, err)
	}
	k, _ = jobsOf(t, s, ctx, k.ID)
	if k.State != domain.Completed || k.HandlingUnits == nil || *k.HandlingUnits != 2 {
		t.Fatalf("card after last job %+v", k)
	}

	// The card's completion event carries the last job and its report; the job
	// events carry the work cell and, on completion, the quantities.
	var completed leanevents.KanbanStateChangedData
	for _, e := range outboxEvents(t, admin, leanevents.KanbanStateChanged) {
		var d leanevents.KanbanStateChangedData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.KanbanID == k.ID && d.To == string(domain.Completed) {
			completed = d
		}
	}
	if completed.JobID == nil || *completed.JobID != fill.ID || *completed.ReportedQty != "18" || *completed.ScrapQty != "1" || *completed.HandlingUnits != 2 {
		t.Fatalf("card completion event %+v", completed)
	}
	jobEvents := outboxEvents(t, admin, leanevents.JobStateChanged)
	if len(jobEvents) != 4 {
		t.Fatalf("job events = %d, want 4 (two starts, two completions)", len(jobEvents))
	}
	var last leanevents.JobStateChangedData
	if err := json.Unmarshal(jobEvents[3].Data, &last); err != nil || last.JobID != fill.ID || last.From != "in-progress" || last.To != "completed" ||
		*last.WorkCellResourceID != fillCell || *last.ReportedQty != "18" {
		t.Fatalf("last job event %+v %v", last, err)
	}

	// The completed card leaves the board once it is emptied.
	if _, _, err := s.MoveKanban(ctx, k.ID, domain.Emptied, k.Version, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if fillBoard, err := s.BoardJobs(ctx, store.BoardFilter{WorkCellID: fillCell}); err != nil || len(fillBoard) != 2 {
		t.Fatalf("fill cell board after emptying %d %v", len(fillBoard), err)
	}
	if n, err := s.BoardJobs(ctx, store.BoardFilter{WorkCellID: fillCell, RuleID: ids.New()}); err != nil || len(n) != 0 {
		t.Fatalf("rule filter %d %v", len(n), err)
	}
}

// BR-24.6: Prepared means picking is complete, so the first job at an activity
// that requires picking waits for the card to be Prepared; without picking, a
// Planned card passes through Prepared on its way to In progress.
func TestJobs_PickingAndReuse(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1)
	f := cellFlow(t, s, ctx, ids.New(), ids.New(), true)
	rule := fixedRule("PICK-1", 1)
	rule.FlowID = &f.ID
	r, _, err := s.CreateRule(ctx, rule, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	k := walk(t, s, ctx, cards[0], domain.Planned)
	_, jobs := jobsOf(t, s, ctx, k.ID)
	if _, _, err := s.StartJob(ctx, jobs[1].ID, jobs[1].Version, store.Idem{}); problemCode(err) != domain.CodeMove {
		t.Fatalf("picking job started on a planned card: %v", err)
	}
	mix, _, err := s.StartJob(ctx, jobs[0].ID, jobs[0].Version, store.Idem{})
	if err != nil || mix.CardState != domain.InProgress {
		t.Fatalf("start without picking %+v %v", mix, err)
	}
	_, jobs = jobsOf(t, s, ctx, k.ID)
	if jobs[1].State != domain.Prepared {
		t.Fatalf("other job did not follow the card through prepared: %s", jobs[1].State)
	}
	if _, _, err := s.CompleteJob(ctx, mix.ID, mix.Version, done("25", "0", 1), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	fill, _, err := s.StartJob(ctx, jobs[1].ID, jobs[1].Version, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CompleteJob(ctx, fill.ID, fill.Version, done("25", "0", 3), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	k, _ = jobsOf(t, s, ctx, k.ID)
	k = walk(t, s, ctx, k, domain.Emptied, domain.NotPlanned)
	// Card reuse clears the jobs and the card's handling units.
	k, jobs = jobsOf(t, s, ctx, k.ID)
	if k.HandlingUnits != nil || jobs[0].State != domain.NotPlanned || jobs[0].ReportedQty != nil || jobs[0].StartedAt != nil || jobs[1].CompletedAt != nil {
		t.Fatalf("reused card %+v jobs %+v", k, jobs)
	}
	if _, _, err := s.StartJob(ctx, jobs[0].ID, jobs[0].Version, store.Idem{}); problemCode(err) != domain.CodeMove {
		t.Fatalf("job started on a not-planned card: %v", err)
	}
}

// Two last jobs of one card completing at the same time: the card lock orders
// them, so exactly one of them completes the card.
func TestJobs_ConcurrentLastCompletionsRollUpOnce(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1)
	cell := ids.New()
	f, _, err := s.CreateFlow(ctx, store.Flow{ValueStreamID: ids.New(), SiteID: ids.New(), Name: "Parallel", Activities: []store.Activity{
		{Sequence: 1, Name: "Left", Kind: "transfer", WorkCellResourceID: &cell, Predecessors: []int{}},
		{Sequence: 2, Name: "Right", Kind: "transfer", WorkCellResourceID: &cell, Predecessors: []int{}},
	}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	rule := eventRule("PAR-1")
	rule.FlowID = &f.ID
	r, _, err := s.CreateRule(ctx, rule, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID, DemandQty: dec("20")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	running := make([]store.Job, 2)
	for i := range running {
		// Starting the first job moves its sibling to Prepared with the card, so
		// each start reads the jobs afresh.
		_, jobs := jobsOf(t, s, ctx, cards[0].ID)
		if running[i], _, err = s.StartJob(ctx, jobs[i].ID, jobs[i].Version, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	errs := make(chan error, 2)
	for _, j := range running {
		go func() {
			_, _, err := s.CompleteJob(ctx, j.ID, j.Version, done("20", "0", 1), store.Idem{})
			errs <- err
		}()
	}
	if a, b := <-errs, <-errs; a != nil || b != nil {
		t.Fatalf("concurrent completions: %v / %v", a, b)
	}
	if k, _ := jobsOf(t, s, ctx, cards[0].ID); k.State != domain.Completed {
		t.Fatalf("card state %s after both jobs completed", k.State)
	}
	n := 0
	for _, e := range outboxEvents(t, admin, leanevents.KanbanStateChanged) {
		var d leanevents.KanbanStateChangedData
		if json.Unmarshal(e.Data, &d) == nil && d.To == string(domain.Completed) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("card completion events = %d, want 1", n)
	}
}

func TestJobs_TenantIsolation(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1)
	cell := ids.New()
	f := cellFlow(t, s, ctx, cell, ids.New(), false)
	rule := eventRule("ISO-1")
	rule.FlowID = &f.ID
	r, _, err := s.CreateRule(ctx, rule, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID, DemandQty: dec("20")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	_, jobs := jobsOf(t, s, ctx, cards[0].ID)
	other := ctxFor(2)
	if _, _, err := s.StartJob(other, jobs[0].ID, jobs[0].Version, store.Idem{}); problemCode(err) != store.CodeJobNotFound {
		t.Fatalf("other tenant started a job: %v", err)
	}
	if board, err := s.BoardJobs(other, store.BoardFilter{WorkCellID: cell}); err != nil || len(board) != 0 {
		t.Fatalf("other tenant's work-cell board %d %v", len(board), err)
	}
	if _, _, err := s.CompleteJob(ctx, ids.New(), 1, done("1", "0", 1), store.Idem{}); problemCode(err) != store.CodeJobNotFound {
		t.Fatalf("unknown job: %v", err)
	}
}
