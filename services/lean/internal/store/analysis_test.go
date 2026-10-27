package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

// cycleOf runs a planned card with jobs through both of them, completing it.
func cycleOf(t *testing.T, s *store.Postgres, ctx context.Context, kanbanID string, reported, scrap string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		_, jobs := jobsOf(t, s, ctx, kanbanID)
		started, _, err := s.StartJob(ctx, jobs[i].ID, jobs[i].Version, store.Idem{})
		if err != nil {
			t.Fatalf("start job %d: %v", i, err)
		}
		if _, _, err := s.CompleteJob(ctx, started.ID, started.Version, done(reported, scrap, 1), store.Idem{}); err != nil {
			t.Fatalf("complete job %d: %v", i, err)
		}
	}
}

func today() time.Time { return time.Now().UTC().Truncate(24 * time.Hour) }

// shiftHistory moves the recorded cycles and job completions of a card back
// by days, as if they had happened then.
func shiftHistory(t *testing.T, admin *pgxpool.Pool, kanbanID string, days int) {
	t.Helper()
	for _, q := range []string{
		`update kanban_cycle set completed_at = completed_at - make_interval(days => $2), planned_at = planned_at - make_interval(days => $2) where kanban_id = $1`,
		`update kanban_job_completion set completed_at = completed_at - make_interval(days => $2), started_at = started_at - make_interval(days => $2) where kanban_id = $1`,
	} {
		if _, err := admin.Exec(context.Background(), q, kanbanID, days); err != nil {
			t.Fatal(err)
		}
	}
}

// A reused fixed card counts once per completion: the cycle survives the
// reset of its jobs (PRD 4.24.14 cycles), and lead time runs from planning.
func TestAnalysis_CyclesSurviveCardReuse(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1)
	f := cellFlow(t, s, ctx, ids.New(), ids.New(), false)
	rule := fixedRule("ANA-FIX", 2)
	rule.FlowID = &f.ID
	r, _, err := s.CreateRule(ctx, rule, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID, Count: 1}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	k := walk(t, s, ctx, cards[0], domain.Planned)
	cycleOf(t, s, ctx, k.ID, "25", "0")
	k, _ = jobsOf(t, s, ctx, k.ID)
	k = walk(t, s, ctx, k, domain.Emptied, domain.NotPlanned, domain.Planned)
	cycleOf(t, s, ctx, k.ID, "25", "0")

	act, err := s.RuleActivity(ctx, r.ID, today(), today())
	if err != nil {
		t.Fatal(err)
	}
	if act.Rule.ID != r.ID || len(act.Cycles) != 2 || act.OverdueOpen != 0 || act.Rule.ActiveKanbans != 1 {
		t.Fatalf("activity %+v", act)
	}
	for _, c := range act.Cycles {
		if c.PlannedAt == nil || c.Due == nil || c.CompletedAt.Before(*c.PlannedAt) {
			t.Fatalf("cycle %+v", c)
		}
		// Planned now with an 8 h lead time: due 8 h after planning.
		if got := c.Due.Sub(*c.PlannedAt); got < 7*time.Hour+59*time.Minute || got > 8*time.Hour+time.Minute {
			t.Fatalf("due %v after planning", got)
		}
	}
	kpi := domain.AnalyzeCycles(act.Cycles)
	if kpi.Cycles != 2 || kpi.TimedCycles != 2 || kpi.LateCycles != 0 || kpi.LatePct == nil || !kpi.LatePct.IsZero() {
		t.Fatalf("kpi %+v", kpi)
	}

	// The period filter is by completion day (UTC): a cycle moved to 40 days
	// ago leaves today's window and enters that day's.
	shiftHistory(t, admin, k.ID, 40)
	if act, err = s.RuleActivity(ctx, r.ID, today(), today()); err != nil || len(act.Cycles) != 0 {
		t.Fatalf("today after shift %d %v", len(act.Cycles), err)
	}
	if act, err = s.RuleActivity(ctx, r.ID, today().AddDate(0, 0, -40), today().AddDate(0, 0, -40)); err != nil || len(act.Cycles) != 2 {
		t.Fatalf("40 days ago %d %v", len(act.Cycles), err)
	}

	if _, err := s.RuleActivity(ctx, ids.New(), today(), today()); problemCode(err) != store.CodeRuleNotFound {
		t.Fatalf("missing rule: %v", err)
	}
}

// A card without jobs completes by hand and is recorded too; an open card past
// its due time is overdue now; a card planned again starts a new lead time.
func TestAnalysis_JoblessCardsAndOverdue(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1)
	r, _, err := s.CreateRule(ctx, eventRule("ANA-EVT"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID, DemandQty: dec("40"), Due: &past}, store.Idem{})
	if err != nil || len(cards) != 2 || cards[0].JobCount != 0 {
		t.Fatalf("generate %+v %v", cards, err)
	}
	// Event cards start Planned: planning time is their creation.
	walk(t, s, ctx, cards[0], domain.Prepared, domain.InProgress, domain.Completed)

	act, err := s.RuleActivity(ctx, r.ID, today(), today())
	if err != nil || len(act.Cycles) != 1 || act.OverdueOpen != 1 {
		t.Fatalf("activity %+v %v", act, err)
	}
	c := act.Cycles[0]
	if c.PlannedAt == nil || c.Due == nil || !c.Due.Equal(past) || !c.CompletedAt.After(*c.Due) {
		t.Fatalf("late cycle %+v", c)
	}
	if kpi := domain.AnalyzeCycles(act.Cycles); kpi.LateCycles != 1 || kpi.LatePct.String() != "100" {
		t.Fatalf("kpi %+v", kpi)
	}
	var planned *time.Time
	if err := admin.QueryRow(context.Background(), `select planned_at from kanban where id = $1`, cards[1].ID).Scan(&planned); err != nil || planned == nil {
		t.Fatalf("event card planning time %v %v", planned, err)
	}
}

// FR-24.12 backflush input: the completions in the period per activity of the
// flow, including those of a card reused since, with reported plus scrap.
func TestBackflush_PeriodWorkKeepsReusedCycles(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1)
	f := cellFlow(t, s, ctx, ids.New(), ids.New(), false)
	rule := fixedRule("BF-FIX", 1)
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
	cycleOf(t, s, ctx, k.ID, "24", "1")
	shiftHistory(t, admin, k.ID, 10) // the first cycle ran 10 days ago
	k, _ = jobsOf(t, s, ctx, k.ID)
	k = walk(t, s, ctx, k, domain.Emptied, domain.NotPlanned, domain.Planned)
	cycleOf(t, s, ctx, k.ID, "20", "0.5")
	// Another flow's work is not this flow's.
	other := cellFlow(t, s, ctx, ids.New(), ids.New(), false)
	otherRule := fixedRule("BF-OTHER", 1)
	otherRule.FlowID = &other.ID
	or, _, err := s.CreateRule(ctx, otherRule, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	oc, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: or.ID}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cycleOf(t, s, ctx, walk(t, s, ctx, oc[0], domain.Planned).ID, "99", "0")

	p := openPeriod(t, s, ctx, f.ID, -15, 0)
	work, err := s.PeriodWork(ctx, f.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if work.Period.ID != p.ID || len(work.Activities) != 2 {
		t.Fatalf("work %+v", work)
	}
	mix, fill := work.Activities[0], work.Activities[1]
	// Both cycles count, although the card's jobs now hold only the second.
	if mix.Name != "Mix" || mix.CycleTimeS != 40 || mix.Jobs != 2 || !mix.Qty.Equal(dec("45.5")) || fill.Name != "Fill" || fill.Jobs != 2 {
		t.Fatalf("mix %+v fill %+v", mix, fill)
	}
	// A period before both cycles sees none of them.
	early := openPeriod(t, s, ctx, f.ID, -40, -16)
	if w, err := s.PeriodWork(ctx, f.ID, early.ID); err != nil || w.Activities[0].Jobs != 0 || !w.Activities[0].Qty.IsZero() {
		t.Fatalf("earlier period %+v %v", w, err)
	}
	bf, err := domain.BackflushConversion(p.Currency, work.Activities, map[string]decimal.Decimal{mix.ActivityID: dec("90"), fill.ActivityID: dec("60")})
	if err != nil {
		t.Fatal(err)
	}
	// Mix: 40 s × 45.5 = 0.50556 h × 90 = 45.50; Fill: 30 s × 45.5 = 0.37917 h × 60 = 22.75.
	if bf.Conversion.Value.String() != "68.25" {
		t.Fatalf("conversion %s", bf.Conversion)
	}
	if _, err := s.PeriodWork(ctx, other.ID, p.ID); problemCode(err) != store.CodePeriodNotFound {
		t.Fatalf("period under another flow: %v", err)
	}
}

// Row-level security: another tenant sees neither the rule nor its history.
func TestAnalysis_TenantIsolation(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1)
	f := cellFlow(t, s, ctx, ids.New(), ids.New(), false)
	rule := fixedRule("ISO-FIX", 1)
	rule.FlowID = &f.ID
	r, _, err := s.CreateRule(ctx, rule, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: r.ID}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cycleOf(t, s, ctx, walk(t, s, ctx, cards[0], domain.Planned).ID, "25", "0")
	p := openPeriod(t, s, ctx, f.ID, -1, 0)

	other := ctxFor(2)
	if _, err := s.RuleActivity(other, r.ID, today(), today()); problemCode(err) != store.CodeRuleNotFound {
		t.Fatalf("tenant 2 read tenant 1's rule: %v", err)
	}
	if _, err := s.PeriodWork(other, f.ID, p.ID); problemCode(err) != store.CodePeriodNotFound {
		t.Fatalf("tenant 2 read tenant 1's period: %v", err)
	}
	var cycles, completions int
	if err := admin.QueryRow(context.Background(), `select (select count(*) from kanban_cycle where tenant_id = $1),
		(select count(*) from kanban_job_completion where tenant_id = $1)`, testkit.TenantID(1)).Scan(&cycles, &completions); err != nil {
		t.Fatal(err)
	}
	if cycles != 1 || completions != 2 {
		t.Fatalf("history rows %d %d", cycles, completions)
	}
}
