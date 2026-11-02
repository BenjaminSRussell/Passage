package store

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

func scopedTo(le string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "scoped", Duties: testkit.ScopeDuties(le)})
}

func employedWorker(t *testing.T, s *Postgres, ctx context.Context, name, le, jobCode string) Worker {
	t.Helper()
	w := mustWorker(t, s, ctx, name)
	from := day(time.January, 1)
	e := mustEmployment(t, s, ctx, w.ID, le, "PRIMARY", from)
	pos, _, err := s.CreatePosition(ctx, Position{PositionNumber: "P-" + name, JobCode: jobCode, DepartmentID: ids.New(), LegalEntityID: le, FTE: "1.00", ValidFrom: from}, "")
	if err != nil {
		t.Fatalf("create position for %s: %v", name, err)
	}
	mustAssignment(t, s, ctx, pos.ID, w.ID, e.ID, "1.00", from, nil)
	return w
}

func gapsByWorker(r JobSkillGaps) map[string]JobWorkerGap {
	out := map[string]JobWorkerGap{}
	for _, g := range r.Items {
		out[g.WorkerID] = g
	}
	return out
}

// TestJobSkillProfileAndGaps (FR-42.18): required skills per job against the
// ratings of the workers holding the job, per job and per worker.
func TestJobSkillProfileAndGaps(t *testing.T) {
	s, ctx := appStore(t)
	le := ids.New()
	mustSkill(t, s, ctx, "WELDING", 5)
	mustSkill(t, s, ctx, "PLC", 5)
	job, _, err := s.CreateJob(ctx, Job{Code: "WELDER", Title: "Welder", JobType: "HOURLY"}, "")
	if err != nil {
		t.Fatal(err)
	}
	a := employedWorker(t, s, ctx, "A", le, "WELDER")
	b := employedWorker(t, s, ctx, "B", le, "WELDER")
	c := employedWorker(t, s, ctx, "C", le, "CLERK")
	mustRate(t, s, ctx, a.ID, "WELDING", 4, day(time.February, 1))
	mustRate(t, s, ctx, a.ID, "PLC", 1, day(time.February, 1))
	mustRate(t, s, ctx, b.ID, "WELDING", 2, day(time.February, 1))

	if _, err := s.SetJobRequirement(ctx, job.ID, "WELDING", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetJobRequirement(ctx, job.ID, "PLC", 2); err != nil {
		t.Fatal(err)
	}
	_, err = s.SetJobRequirement(ctx, job.ID, "PLC", 6)
	requireCode(t, err, domain.CodeSkillRating)
	_, err = s.SetJobRequirement(ctx, job.ID, "NOPE", 1)
	requireCode(t, err, CodeSkillNotFound)
	_, err = s.SetJobRequirement(ctx, ids.New(), "PLC", 1)
	requireCode(t, err, CodeJobNotFound)
	if reqs, err := s.ListJobRequirements(ctx, job.ID); err != nil || len(reqs) != 2 || reqs[0].SkillCode != "PLC" || reqs[1].SkillCode != "WELDING" {
		t.Fatalf("requirements = %+v err %v, want PLC then WELDING", reqs, err)
	}

	on := day(time.March, 10)
	report, err := s.JobGaps(ctx, job.ID, on)
	if err != nil || report.JobCode != "WELDER" || len(report.Items) != 2 || report.Truncated {
		t.Fatalf("report = %+v err %v, want the two welders", report, err)
	}
	by := gapsByWorker(report)
	if _, ok := by[c.ID]; ok {
		t.Fatal("a clerk must not appear in the welder gap report")
	}
	if g := by[a.ID]; g.Met || len(g.Gaps) != 1 || g.Gaps[0] != (domain.GapRow{Skill: "PLC", Required: 2, Actual: 1, Delta: -1}) {
		t.Fatalf("worker A gaps = %+v, want only PLC 2/1", g)
	}
	if g := by[b.ID]; g.Met || len(g.Gaps) != 2 || g.Gaps[0].Skill != "PLC" || g.Gaps[0].Actual != 0 || g.Gaps[1] != (domain.GapRow{Skill: "WELDING", Required: 3, Actual: 2, Delta: -1}) {
		t.Fatalf("worker B gaps = %+v, want PLC 2/0 and WELDING 3/2", g)
	}

	gap, err := s.SkillGapForJob(ctx, a.ID, job.ID)
	if err != nil || gap.Met || gap.JobID != job.ID || len(gap.Comparison) != 2 || len(gap.Gaps) != 1 {
		t.Fatalf("worker gap = %+v err %v", gap, err)
	}
	_, err = s.SkillGapForJob(ctx, ids.New(), job.ID)
	requireCode(t, err, CodeWorkerNotFound)

	mustRate(t, s, ctx, a.ID, "PLC", 2, day(time.March, 1))
	report, _ = s.JobGaps(ctx, job.ID, on)
	if g := gapsByWorker(report)[a.ID]; !g.Met || len(g.Gaps) != 0 {
		t.Fatalf("worker A after the PLC rating = %+v, want met", g)
	}
	if before, err := s.JobGaps(ctx, job.ID, day(time.January, 1).AddDate(0, 0, -1)); err != nil || len(before.Items) != 0 {
		t.Fatalf("report before any assignment = %+v err %v, want empty", before, err)
	}
	if other, err := s.JobGaps(scopedTo(ids.New()), job.ID, on); err != nil || len(other.Items) != 0 {
		t.Fatalf("report for a caller scoped to another legal entity = %+v err %v, want empty", other, err)
	}

	if err := s.RemoveJobRequirement(ctx, job.ID, "PLC"); err != nil {
		t.Fatal(err)
	}
	requireCode(t, s.RemoveJobRequirement(ctx, job.ID, "PLC"), CodeRequirementNotFound)
	report, _ = s.JobGaps(ctx, job.ID, on)
	if g := gapsByWorker(report)[b.ID]; len(g.Gaps) != 1 || g.Gaps[0].Skill != "WELDING" {
		t.Fatalf("worker B after dropping PLC = %+v, want only WELDING", g)
	}
}

func TestGoalLifecycleReplayAndScope(t *testing.T) {
	s, ctx := appStore(t)
	le := ids.New()
	at(s, day(time.March, 1))
	w := employedWorker(t, s, ctx, "G", le, "J")
	due := "2026-12-31"
	goal, inserted, err := s.CreateGoal(ctx, Goal{WorkerID: w.ID, Title: "Ship the forklift certification", DueOn: &due}, "goal-key")
	if err != nil || !inserted || goal.Status != domain.GoalOpen || goal.ProgressPct != 0 || goal.LegalEntityID == nil || *goal.LegalEntityID != le {
		t.Fatalf("create = %+v inserted=%v err=%v", goal, inserted, err)
	}
	replay, inserted, err := s.CreateGoal(ctx, Goal{WorkerID: w.ID, Title: "Ship the forklift certification", DueOn: &due}, "goal-key")
	if err != nil || inserted || replay.ID != goal.ID {
		t.Fatalf("replay = %+v inserted=%v err=%v", replay, inserted, err)
	}
	_, _, err = s.CreateGoal(ctx, Goal{WorkerID: w.ID, Title: "Something else"}, "goal-key")
	requireCode(t, err, CodeIdempotencyReuse)
	_, _, err = s.CreateGoal(ctx, Goal{WorkerID: ids.New(), Title: "Nobody"}, "")
	requireCode(t, err, CodeWorkerNotFound)

	pct := 40
	updated, err := s.UpdateGoal(ctx, goal.ID, nil, &pct)
	if err != nil || updated.ProgressPct != 40 || updated.Status != domain.GoalOpen {
		t.Fatalf("progress = %+v err %v", updated, err)
	}
	achieved := domain.GoalAchieved
	done, err := s.UpdateGoal(ctx, goal.ID, &achieved, nil)
	if err != nil || done.Status != domain.GoalAchieved || done.ProgressPct != 100 {
		t.Fatalf("achieved = %+v err %v, want 100 percent", done, err)
	}
	_, err = s.UpdateGoal(ctx, ids.New(), &achieved, nil)
	requireCode(t, err, CodeGoalNotFound)

	if owner, err := s.GoalWorker(ctx, goal.ID); err != nil || owner != w.ID {
		t.Fatalf("goal worker = %q err %v", owner, err)
	}
	if list, err := s.ListWorkerGoals(ctx, w.ID); err != nil || len(list) != 1 || list[0].ID != goal.ID {
		t.Fatalf("goals = %+v err %v", list, err)
	}
	if _, err := s.GoalWorker(scopedTo(ids.New()), goal.ID); err == nil {
		t.Fatal("a caller scoped to another legal entity must not see the goal")
	}
	if list, err := s.ListWorkerGoals(scopedTo(ids.New()), w.ID); err != nil || len(list) != 0 {
		t.Fatalf("scoped list = %+v err %v, want empty", list, err)
	}
}

func reviewStatuses(t *testing.T, s *Postgres, ctx context.Context, id string) []string {
	t.Helper()
	var out []string
	for _, e := range outboxFor(t, s, ctx, id) {
		if e.Type != hrevents.ReviewChanged {
			continue
		}
		for _, k := range []string{"achievements", "development", "comments", "self_achievements", "manager_comments"} {
			if _, leaked := e.Data[k]; leaked {
				t.Fatalf("review event leaks %q: %+v", k, e.Data)
			}
		}
		out = append(out, e.Data["status"].(string))
	}
	return out
}

// TestReviewTwoStepLifecycle: draft -> self_submitted by the reviewed worker
// only -> signed_off by someone else; repeats are no-ops, wrong order and
// changed text are refused, and each step publishes review.changed.
func TestReviewTwoStepLifecycle(t *testing.T) {
	s, ctx := newStore(t)
	le := ids.New()
	at(s, day(time.March, 1))
	w := employedWorker(t, s, ctx, "R", le, "J")
	manager := employedWorker(t, s, ctx, "M", le, "J")
	start, end := day(time.January, 1), day(time.December, 31)

	review, inserted, err := s.OpenReview(ctx, w.ID, start, end, "rev-key")
	if err != nil || !inserted || review.Status != domain.ReviewDraft || review.PeriodStart != "2026-01-01" || review.PeriodEnd != "2026-12-31" {
		t.Fatalf("open = %+v inserted=%v err=%v", review, inserted, err)
	}
	again, inserted, err := s.OpenReview(ctx, w.ID, start, end, "")
	if err != nil || inserted || again.ID != review.ID {
		t.Fatalf("open again for the period = %+v inserted=%v err=%v, want the stored review", again, inserted, err)
	}
	replay, inserted, err := s.OpenReview(ctx, w.ID, start, end, "rev-key")
	if err != nil || inserted || replay.ID != review.ID {
		t.Fatalf("replayed key = %+v inserted=%v err=%v", replay, inserted, err)
	}
	_, _, err = s.OpenReview(ctx, w.ID, start, day(time.June, 30), "rev-key")
	requireCode(t, err, CodeIdempotencyReuse)
	_, _, err = s.OpenReview(ctx, ids.New(), start, end, "")
	requireCode(t, err, CodeWorkerNotFound)

	_, _, err = s.SignOffReview(ctx, review.ID, manager.ID, nil, nil)
	requireCode(t, err, CodeReviewState)
	_, _, err = s.SubmitSelfAssessment(ctx, review.ID, manager.ID, "I did it", nil)
	requireCode(t, err, CodeReviewNotFound)

	dev := "More PLC training"
	submitted, changed, err := s.SubmitSelfAssessment(ctx, review.ID, w.ID, "Hit every target", &dev)
	if err != nil || !changed || submitted.Status != domain.ReviewSelfSubmitted || submitted.SelfSubmittedAt == nil ||
		submitted.SelfAchievements == nil || *submitted.SelfAchievements != "Hit every target" {
		t.Fatalf("self-assessment = %+v changed=%v err=%v", submitted, changed, err)
	}
	if same, changed, err := s.SubmitSelfAssessment(ctx, review.ID, w.ID, "Hit every target", &dev); err != nil || changed || same.Status != domain.ReviewSelfSubmitted {
		t.Fatalf("same self-assessment again = %+v changed=%v err=%v, want a no-op", same, changed, err)
	}
	_, _, err = s.SubmitSelfAssessment(ctx, review.ID, w.ID, "Rewritten after the fact", &dev)
	requireCode(t, err, CodeReviewState)

	_, _, err = s.SignOffReview(ctx, review.ID, w.ID, nil, nil)
	requireCode(t, err, CodeReviewSelf)

	comments, rating := "Strong year", 4
	signed, changed, err := s.SignOffReview(ctx, review.ID, manager.ID, &comments, &rating)
	if err != nil || !changed || signed.Status != domain.ReviewSignedOff || signed.SignedOffAt == nil || signed.SignedOffBy == nil ||
		signed.OverallRating == nil || *signed.OverallRating != 4 {
		t.Fatalf("sign-off = %+v changed=%v err=%v", signed, changed, err)
	}
	if same, changed, err := s.SignOffReview(ctx, review.ID, manager.ID, &comments, &rating); err != nil || changed || same.Status != domain.ReviewSignedOff {
		t.Fatalf("same sign-off again = %+v changed=%v err=%v, want a no-op", same, changed, err)
	}
	other := 2
	_, _, err = s.SignOffReview(ctx, review.ID, manager.ID, &comments, &other)
	requireCode(t, err, CodeReviewState)
	_, _, err = s.SubmitSelfAssessment(ctx, review.ID, w.ID, "Hit every target", &dev)
	requireCode(t, err, CodeReviewState)

	if got := reviewStatuses(t, s, ctx, review.ID); len(got) != 3 || got[0] != "draft" || got[1] != "self_submitted" || got[2] != "signed_off" {
		t.Fatalf("review.changed statuses = %v, want draft, self_submitted, signed_off", got)
	}
	if list, err := s.ListWorkerReviews(ctx, w.ID); err != nil || len(list) != 1 || list[0].ID != review.ID {
		t.Fatalf("reviews = %+v err %v", list, err)
	}
	if owner, err := s.ReviewWorker(ctx, review.ID); err != nil || owner != w.ID {
		t.Fatalf("review worker = %q err %v", owner, err)
	}
	_, err = s.GetReview(ctx, ids.New())
	requireCode(t, err, CodeReviewNotFound)
}

func TestGrants_PerformancePathsWorkAsAppRole(t *testing.T) {
	s, ctx := appStore(t)
	le := ids.New()
	at(s, day(time.March, 1))
	w := employedWorker(t, s, ctx, "P", le, "J")
	manager := employedWorker(t, s, ctx, "Q", le, "J")
	if _, _, err := s.CreateGoal(ctx, Goal{WorkerID: w.ID, Title: "goal"}, ""); err != nil {
		t.Fatalf("goal as the app role: %v", err)
	}
	review, _, err := s.OpenReview(ctx, w.ID, day(time.January, 1), day(time.December, 31), "")
	if err != nil {
		t.Fatalf("open review as the app role: %v", err)
	}
	if _, _, err := s.SubmitSelfAssessment(ctx, review.ID, w.ID, "done", nil); err != nil {
		t.Fatalf("self-assessment as the app role: %v", err)
	}
	if _, _, err := s.SignOffReview(ctx, review.ID, manager.ID, nil, nil); err != nil {
		t.Fatalf("sign-off as the app role: %v", err)
	}
	if _, err := s.GetReview(scopedTo(ids.New()), review.ID); err == nil {
		t.Fatal("a caller scoped to another legal entity must not read the review")
	}
	mustSkill(t, s, ctx, "PLC", 5)
	job, _, err := s.CreateJob(ctx, Job{Code: "ELEC", Title: "Electrician", JobType: "HOURLY"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetJobRequirement(ctx, job.ID, "PLC", 2); err != nil {
		t.Fatalf("job requirement as the app role: %v", err)
	}
	if err := s.RemoveJobRequirement(ctx, job.ID, "PLC"); err != nil {
		t.Fatalf("remove job requirement as the app role: %v", err)
	}
}
