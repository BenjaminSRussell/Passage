package store

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

func jobOf(t *testing.T, s *Postgres, ctx context.Context, orderID string, oper int, typ domain.JobType) Job {
	t.Helper()
	for _, j := range jobsOf(t, s, ctx, orderID) {
		if j.OperNumber == oper && j.JobType == typ {
			return j
		}
	}
	t.Fatalf("order %s has no %s job on operation %d", orderID, typ, oper)
	return Job{}
}

func problemCode(err error) int {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Status
	}
	return 0
}

// FR-21.6: a job card reports hours and quantities per job; the first card starts the job, a card
// that finishes it closes it, and the operation closes once its work jobs are finished.
func TestJobCard_FeedbackStartsAndFinishesJobs(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9301))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-jobcard", ids.New(), 10, 20))
	job10 := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess)

	first := st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job10.ID, Hours: d("4"), QtyGood: d("40")}}}).ID)
	if first.Type != domain.JournalJobCard || len(first.Lines) != 1 || first.Lines[0].JobType == nil || *first.Lines[0].JobType != "process" ||
		first.Lines[0].OperNumber == nil || *first.Lines[0].OperNumber != 10 || !first.Lines[0].Amount.Equal(d("120")) {
		t.Fatalf("posted job card: %+v", first.Lines)
	}
	j := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess)
	if j.Status != "started" || !j.HoursReported.Equal(d("4")) || !j.QtyGood.Equal(d("40")) {
		t.Fatalf("job after the first card: %+v", j)
	}
	if op := st.refresh().Operations[0]; op.Status != domain.OperStarted || !op.HoursReported.Equal(d("4")) || !op.QtyGood.Equal(d("40")) {
		t.Fatalf("operation after the first card: %+v", op)
	}

	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job10.ID, Hours: d("6"), QtyGood: d("60"), Finished: true}}}).ID)
	j = jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess)
	op := st.refresh().Operations[0]
	if j.Status != "finished" || !j.HoursReported.Equal(d("10")) || op.Status != domain.OperFinished || !op.HoursReported.Equal(d("10")) {
		t.Fatalf("after the finishing card: job %+v, operation %+v", j, op)
	}
	if other := jobOf(t, s, ctx, st.orderID, 20, domain.JobProcess); other.Status != "planned" || !other.HoursReported.IsZero() {
		t.Fatalf("the other operation's job moved: %+v", other)
	}

	// A finished job takes no more time, on a draft or at posting.
	if _, err := st.journal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job10.ID, Hours: d("1")}}}); problemCode(err) != http.StatusConflict {
		t.Fatalf("a card on a finished operation: %v", err)
	}
	if page, err := s.ListJobs(ctx, JobFilter{OrderID: st.orderID, Limit: 10}); err != nil || !page.Items[0].HoursReported.Equal(d("10")) {
		t.Fatalf("job list: %+v %v", page, err)
	}
}

// With no lines the card proposes the time still open on each setup and process job (FR-21.6),
// and a reset to Estimated reverses the card and plans the job again.
func TestJobCard_ProposalAndReversal(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9302))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-jobcard-prop", ids.New(), 10, 20))
	job10 := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job10.ID, Hours: d("3")}}}).ID)

	prop := st.mustJournal(NewJournal{Type: domain.JournalJobCard, Proposal: domain.ProposeRemaining})
	if len(prop.Lines) != 2 || !prop.Lines[0].Qty.Equal(d("7")) || !prop.Lines[1].Qty.Equal(d("10")) {
		t.Fatalf("proposal: %+v", prop.Lines)
	}
	st.mustPost(prop.ID)
	// 3 + 7 + 10 hours at 30.00 labor.
	if c := st.costs(); !c.WIPBalance.Equal(d("600")) {
		t.Fatalf("WIP after the cards: %s", c.WIPBalance)
	}

	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Released })
	for _, j := range jobsOf(t, s, ctx, st.orderID) {
		if j.Status != "planned" || !j.HoursReported.IsZero() || !j.QtyGood.IsZero() {
			t.Errorf("job after the reversal: %+v", j)
		}
	}
	if c := st.costs(); !c.WIPBalance.IsZero() {
		t.Fatalf("WIP after the reversal: %s", c.WIPBalance)
	}
}

// A re-date gives jobs new ids but keeps what the cards reported on the operation's job.
func TestJobCard_RedateKeepsFeedback(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9303))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-jobcard-redate", ids.New(), 10, 20))
	job10 := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job10.ID, Hours: d("2"), QtyGood: d("5"), Finished: true}}}).ID)

	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error { return redateOrder(ctx, tx, testkit.TenantID(9303), st.orderID, "planner-1") })
	if err != nil {
		t.Fatal(err)
	}
	j := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess)
	if j.ID == job10.ID || j.Status != "finished" || !j.HoursReported.Equal(d("2")) || !j.QtyGood.Equal(d("5")) {
		t.Fatalf("job after the re-date: %+v (was %s)", j, job10.ID)
	}
}

// planJobCard rules that need no database: the order must be scheduled, only setup and process
// jobs take time, quantities belong to the process job.
func TestPlanJobCard_Rules(t *testing.T) {
	ops := []domain.Operation{{OperNumber: 10, Status: domain.OperOpen}}
	jobs := []workJob{{id: "setup", oper: 10, typ: domain.JobSetup, plannedHours: d("1"), status: "planned"},
		{id: "proc", oper: 10, typ: domain.JobProcess, plannedHours: d("9"), status: "planned"}}
	_, err := planJobCard(NewJournal{Hours: []HoursInput{{JobID: "proc", Hours: d("1")}}}, ops, nil, nil, d("0"), d("100"))
	if problemCode(err) != http.StatusConflict {
		t.Errorf("unscheduled order: %v", err)
	}
	for name, in := range map[string]NewJournal{
		"unknown job":        {Hours: []HoursInput{{JobID: "queue", Hours: d("1")}}},
		"listed twice":       {Hours: []HoursInput{{JobID: "proc", Hours: d("1")}, {JobID: "proc", Hours: d("1")}}},
		"quantity on setup":  {Hours: []HoursInput{{JobID: "setup", Hours: d("1"), QtyGood: d("1")}}},
		"hours not 2 places": {Hours: []HoursInput{{JobID: "proc", Hours: d("1.005")}}},
		"issues":             {Issues: []IssueInput{{LineNo: "10", Qty: d("1")}}},
	} {
		if _, err := planJobCard(in, ops, jobs, nil, d("0"), d("100")); problemCode(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v", name, err)
		}
	}
	lines, err := planJobCard(NewJournal{Proposal: domain.ProposeRemaining}, ops, jobs, nil, d("0"), d("100"))
	if err != nil || len(lines) != 2 || !lines[0].Qty.Equal(d("1")) || *lines[1].JobType != "process" {
		t.Errorf("proposal: %+v %v", lines, err)
	}
}

// FR-21.6: an operation takes its feedback from route cards or from job cards, not both, so the
// same time is not counted twice; the order's other operations stay free, and the reset that
// reverses the cards frees the operation again.
func TestJobCard_OneFeedbackModePerOperation(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9304))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-jobcard-mode", ids.New(), 10, 20))
	job10 := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess)
	job20 := jobOf(t, s, ctx, st.orderID, 20, domain.JobProcess)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job10.ID, Hours: d("3")}}}).ID)
	st.routeCard(HoursInput{OperNumber: 20, Hours: d("2")})

	if _, err := st.journal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("1")}}}); codeOf(err) != CodeFeedbackMode {
		t.Fatalf("route card on a job-card operation: %v", err)
	}
	if _, err := st.journal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job20.ID, Hours: d("1")}}}); codeOf(err) != CodeFeedbackMode {
		t.Fatalf("job card on a route-card operation: %v", err)
	}
	// The proposals leave the other mode's operation out: 10 - 3 on job 10, 10 - 2 on operation 20.
	if prop := st.mustJournal(NewJournal{Type: domain.JournalJobCard, Proposal: domain.ProposeRemaining}); len(prop.Lines) != 1 ||
		*prop.Lines[0].OperNumber != 10 || !prop.Lines[0].Qty.Equal(d("7")) {
		t.Fatalf("job card proposal: %+v", prop.Lines)
	}
	if prop := st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Proposal: domain.ProposeRemaining}); len(prop.Lines) != 1 ||
		*prop.Lines[0].OperNumber != 20 || !prop.Lines[0].Qty.Equal(d("8")) {
		t.Fatalf("route card proposal: %+v", prop.Lines)
	}

	// The reset reverses both cards; then either mode may report on either operation.
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Released })
	st.must(domain.CmdStart, nil)
	st.routeCard(HoursInput{OperNumber: 10, Hours: d("1")})
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: jobOf(t, s, ctx, st.orderID, 20, domain.JobProcess).ID, Hours: d("1")}}}).ID)

	// A draft made before the other mode was posted is refused at posting, and books nothing.
	st2 := startedOrder(t, s, ctx, routedOrder(t, "key-jobcard-mode-2", ids.New(), 10))
	early := st2.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("1")}}})
	st2.mustPost(st2.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: jobOf(t, s, ctx, st2.orderID, 10, domain.JobProcess).ID, Hours: d("1")}}}).ID)
	if _, err := st2.post(early.ID, nil); codeOf(err) != CodeFeedbackMode {
		t.Fatalf("posting a route card after a job card on the operation: %v", err)
	}
	if op := st2.refresh().Operations[0]; !op.HoursReported.Equal(d("1")) {
		t.Fatalf("the refused card was booked: %+v", op)
	}
}

// :finish and :reopen on an operation finish and reopen its jobs, so a reopened operation takes
// job cards again (FR-21.6 "unless reopened").
func TestJobCard_ReopenOpensTheJobs(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9305))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-jobcard-reopen", ids.New(), 10, 20))
	job10 := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job10.ID, Hours: d("2"), Finished: true}}}).ID)
	if op := st.refresh().Operations[0]; op.Status != domain.OperFinished {
		t.Fatalf("operation after the finishing card: %+v", op)
	}
	if _, _, err := st.operate(10, domain.OperReopen, "reopen-10"); err != nil {
		t.Fatal(err)
	}
	if j := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess); j.Status != "started" {
		t.Fatalf("job after :reopen: %+v", j)
	}
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job10.ID, Hours: d("1")}}}).ID)

	st.refresh()
	if _, _, err := st.operate(20, domain.OperFinish, "finish-20"); err != nil {
		t.Fatal(err)
	}
	if j := jobOf(t, s, ctx, st.orderID, 20, domain.JobProcess); j.Status != "finished" {
		t.Fatalf("job after :finish: %+v", j)
	}
	if _, _, err := st.operate(20, domain.OperReopen, "reopen-20"); err != nil {
		t.Fatal(err)
	}
	if j := jobOf(t, s, ctx, st.orderID, 20, domain.JobProcess); j.Status != "planned" {
		t.Fatalf("job without feedback after :reopen: %+v", j)
	}
}

func codeOf(err error) string {
	code, _ := problemOf(err)
	return code
}

// As passage_app, the grants of 0018 cover what a job card writes on its job: posting, :reopen,
// a re-date carrying the feedback over, and the reset that reverses the card.
func TestJobCard_AsAppRole(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	tid := testkit.TenantID(9306)
	ctx := tenantCtx(tid)
	st := startedOrder(t, s, ctx, routedOrder(t, "key-jobcard-app", ids.New(), 10))
	job := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job.ID, Hours: d("2"), QtyGood: d("4"), Finished: true}}}).ID)
	st.refresh()
	if _, _, err := st.operate(10, domain.OperReopen, "app-reopen-10"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error { return redateOrder(ctx, tx, tid, st.orderID, "planner-1") }); err != nil {
		t.Fatal(err)
	}
	if j := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess); j.Status != "started" || !j.QtyGood.Equal(d("4")) {
		t.Fatalf("job after reopen and re-date: %+v", j)
	}
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Released })
	if j := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess); j.Status != "planned" || !j.HoursReported.IsZero() {
		t.Fatalf("job after the reset: %+v", j)
	}
}
