package store

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

func endedByRAF(t *testing.T, s *Postgres, orderID string) (jobs, ops int) {
	t.Helper()
	pool := s.pool
	if err := pool.QueryRow(t.Context(), `select count(*) from production_job where order_id = $1 and ended_by_raf`, orderID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `select count(*) from production_operation where order_id = $1 and ended_by_raf`, orderID).Scan(&ops); err != nil {
		t.Fatal(err)
	}
	return jobs, ops
}

// FR-21.7 "End jobs on last RAF": only the report as finished that completes the quantity closes
// the jobs; one finished earlier by a job card stays as that card left it, and a reversal below
// the quantity reopens just the jobs the RAF closed.
func TestEndJobs_LastRAFClosesOpenJobsAndReversalReopensThem(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9501))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-endjobs", ids.New(), 10, 20))
	job10 := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job10.ID, Hours: d("4"), Finished: true}}}).ID)

	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("40") })
	if j := jobOf(t, s, ctx, st.orderID, 20, domain.JobProcess); j.Status != "planned" {
		t.Fatalf("a partial RAF must not close the jobs: %+v", j)
	}
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("60") })

	for _, j := range jobsOf(t, s, ctx, st.orderID) {
		if j.JobType == domain.JobProcess && j.Status != "finished" {
			t.Errorf("job after the last RAF: %+v", j)
		}
	}
	o := st.refresh()
	if o.Operations[0].Status != domain.OperFinished || o.Operations[1].Status != domain.OperFinished {
		t.Fatalf("operations after the last RAF: %+v", o.Operations)
	}
	// Operation 10 was finished by its job card, so only operation 20 carries the RAF flag.
	if jobs, ops := endedByRAF(t, s, st.orderID); jobs != 1 || ops != 1 {
		t.Fatalf("ended by RAF: %d jobs, %d operations", jobs, ops)
	}

	// Reset to Started reverses the RAFs: the job the RAF closed is planned again, the card's job stays finished.
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Started })
	if j := jobOf(t, s, ctx, st.orderID, 20, domain.JobProcess); j.Status != "planned" {
		t.Errorf("job of operation 20 after the reversal: %+v", j)
	}
	if j := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess); j.Status != "finished" {
		t.Errorf("job of operation 10 after the reversal: %+v", j)
	}
	if op := st.refresh().Operations[1]; op.Status != domain.OperOpen {
		t.Errorf("operation 20 after the reversal: %+v", op)
	}
	if jobs, ops := endedByRAF(t, s, st.orderID); jobs != 0 || ops != 0 {
		t.Fatalf("flags after the reversal: %d jobs, %d operations", jobs, ops)
	}
}

// The site can switch the behaviour off, and the switch is read from the site's parameters.
func TestEndJobs_ParameterOffLeavesJobsAlone(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9502))
	in := routedOrder(t, "key-endjobs-off", ids.New(), 10)
	saveParams(t, s, ctx, in.LegalEntityID, func(p *domain.Parameters) { p.EndJobs = false }, domain.AllKey, 0)
	st := startedOrder(t, s, ctx, in)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("100") })
	if j := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess); j.Status != "planned" {
		t.Fatalf("job with EndJobs off: %+v", j)
	}
	// Turning it on afterwards closes nothing retroactively; the stored value round-trips.
	list, err := s.ListParameters(ctx, in.LegalEntityID)
	if err != nil || len(list) != 1 || list[0].EndJobs {
		t.Fatalf("stored parameters: %+v %v", list, err)
	}
}

// With no stored parameters the PRD default (on) applies, and a replace that leaves endJobs out keeps the stored value.
func TestEndJobs_DefaultOnAndKeptOnReplace(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9503))
	in := routedOrder(t, "key-endjobs-default", ids.New(), 10)
	st := startedOrder(t, s, ctx, in)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("100") })
	if j := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess); j.Status != "finished" {
		t.Fatalf("job with the default: %+v", j)
	}

	le := ids.New()
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.EndJobs = false }, domain.AllKey, 0)
	prm := plantParams(domain.AllKey)
	out, _, err := s.SaveParameters(ctx, ParametersSave{LegalEntityID: le, Parameters: prm, IfMatch: 1, Actor: "admin", Keep: KeepParameters{EndJobs: true}})
	if err != nil || out.EndJobs {
		t.Fatalf("replace keeping endJobs: %+v %v", out, err)
	}
	prm.EndJobs = true
	if out, _, err = s.SaveParameters(ctx, ParametersSave{LegalEntityID: le, Parameters: prm, IfMatch: 2, Actor: "admin"}); err != nil || !out.EndJobs {
		t.Fatalf("replace setting endJobs: %+v %v", out, err)
	}
}

// A command after the last RAF owns what it touches: an operation reopened with :reopen and then
// closed by a job card stays closed when the RAF is reversed, while the operation only the RAF
// closed opens again. Each RAF-driven change emits operation.statuschanged.
func TestEndJobs_CommandAfterTheRAFKeepsItsOperation(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9504))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-endjobs-cross", ids.New(), 10, 20))
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("100") })
	finished := operationEvents(t, pool, st.orderID)
	if len(finished) != 2 || finished[0].OperNumber != 10 || finished[1].OperNumber != 20 {
		t.Fatalf("events of the last RAF: %+v", finished)
	}
	for _, e := range finished {
		if e.Action != string(domain.OperFinish) || e.From != "open" || e.To != "finished" || e.Actor == "" || e.OrderVersion != st.version {
			t.Fatalf("event of the last RAF: %+v (order version %d)", e, st.version)
		}
	}

	if _, _, err := st.operate(20, domain.OperReopen, "cross-reopen-20"); err != nil {
		t.Fatal(err)
	}
	job20 := jobOf(t, s, ctx, st.orderID, 20, domain.JobProcess)
	if job20.Status != "planned" {
		t.Fatalf("job of the reopened operation: %+v", job20)
	}
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job20.ID, Hours: d("2"), Finished: true}}}).ID)
	if jobs, ops := endedByRAF(t, s, st.orderID); jobs != 1 || ops != 1 {
		t.Fatalf("only operation 10 is still the RAF's: %d jobs, %d operations", jobs, ops)
	}

	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Started })
	o := st.refresh()
	if o.Operations[0].Status != domain.OperOpen || o.Operations[1].Status != domain.OperFinished {
		t.Fatalf("operations after the reversal: %+v", o.Operations)
	}
	if j := jobOf(t, s, ctx, st.orderID, 20, domain.JobProcess); j.Status != "finished" {
		t.Fatalf("the job card's job after the reversal: %+v", j)
	}
	all := operationEvents(t, pool, st.orderID)
	last := all[len(all)-1]
	if last.OperNumber != 10 || last.Action != string(domain.OperReopen) || last.From != "finished" || last.To != "open" {
		t.Fatalf("event of the reversal: %+v", all)
	}
	for _, e := range all[2:] {
		if e.OperNumber == 20 && e.Action == string(domain.OperFinish) {
			t.Fatalf("the job card's close is reported by journal.posted, not as a RAF finish: %+v", all)
		}
	}
}
