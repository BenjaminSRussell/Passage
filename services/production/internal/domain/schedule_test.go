package domain

import (
	"errors"
	"testing"
	"time"
)

func routeForSchedule() []Operation {
	return []Operation{
		{OperNumber: 10, ResourceID: "r1", SetupH: d("1"), RunHPer: d("0.1"), ProcessQty: d("10"), QueueBeforeH: d("2"), TransitH: d("0.5")},
		{OperNumber: 20, ResourceID: "r2", RunHPer: d("0.25"), ProcessQty: d("10"), QueueAfterH: d("1")},
	}
}

func TestPlanSchedule_ForwardJobs(t *testing.T) {
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	s, err := PlanSchedule(routeForSchedule(), MethodJobs, Forward, at)
	if err != nil {
		t.Fatal(err)
	}
	// op 10: queue 2h, setup 1h, process 1h, transit 0.5h = 4.5h; op 20: process 2.5h, queue 1h = 3.5h.
	if !s.Start.Equal(at) || !s.End.Equal(at.Add(8*time.Hour)) {
		t.Fatalf("window %v..%v", s.Start, s.End)
	}
	want := []struct {
		oper int
		typ  JobType
		min  int
		len  int
	}{{10, JobQueueBefore, 0, 120}, {10, JobSetup, 120, 60}, {10, JobProcess, 180, 60}, {10, JobTransport, 240, 30},
		{20, JobProcess, 270, 150}, {20, JobQueueAfter, 420, 60}}
	if len(s.Jobs) != len(want) {
		t.Fatalf("%d jobs: %+v", len(s.Jobs), s.Jobs)
	}
	for i, w := range want {
		j := s.Jobs[i]
		if j.OperNumber != w.oper || j.Type != w.typ || !j.Start.Equal(at.Add(time.Duration(w.min)*time.Minute)) || j.End.Sub(j.Start) != time.Duration(w.len)*time.Minute {
			t.Errorf("job %d = %+v, want %+v", i, j, w)
		}
	}
	if s.Jobs[3].Seq != 4 || s.Jobs[4].Seq != 1 {
		t.Errorf("seq restarts per operation: %d %d", s.Jobs[3].Seq, s.Jobs[4].Seq)
	}
	if w := s.Windows[1]; !w.Start.Equal(at.Add(270*time.Minute)) || !w.End.Equal(s.End) {
		t.Errorf("window 20 = %+v", w)
	}
	if s.StartDate() != "2026-11-02" || s.EndDate() != "2026-11-02" {
		t.Errorf("dates %s %s", s.StartDate(), s.EndDate())
	}
}

func TestPlanSchedule_OperationsMethodOneProcessJob(t *testing.T) {
	at := time.Date(2026, 11, 2, 22, 0, 0, 0, time.UTC)
	s, err := PlanSchedule(routeForSchedule(), MethodOperations, Forward, at)
	if err != nil {
		t.Fatal(err)
	}
	// op 10: queue 2h, then one process job of setup + run (2h), then transit; no separate setup job.
	if len(s.Jobs) != 5 || s.Jobs[1].Type != JobProcess || s.Jobs[1].Hours.Cmp(d("2")) != 0 {
		t.Fatalf("jobs %+v", s.Jobs)
	}
	if s.EndDate() != "2026-11-03" {
		t.Errorf("end date %s crosses midnight", s.EndDate())
	}
}

func TestPlanSchedule_BackwardEndsAtAnchor(t *testing.T) {
	end := time.Date(2026, 11, 10, 23, 59, 59, 0, time.UTC)
	s, err := PlanSchedule(routeForSchedule(), MethodJobs, Backward, end)
	if err != nil {
		t.Fatal(err)
	}
	if !s.End.Equal(end) || !s.Start.Equal(end.Add(-8*time.Hour)) {
		t.Fatalf("window %v..%v", s.Start, s.End)
	}
	last := s.Jobs[len(s.Jobs)-1]
	if !last.End.Equal(end) {
		t.Errorf("last job ends %v", last.End)
	}
	for i := 1; i < len(s.Jobs); i++ {
		if !s.Jobs[i].Start.Equal(s.Jobs[i-1].End) {
			t.Errorf("job %d does not follow job %d without a gap or overlap", i, i-1)
		}
	}
}

func TestPlanSchedule_ZeroHourOperationKeepsItsProcessJob(t *testing.T) {
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	s, err := PlanSchedule([]Operation{{OperNumber: 10, ResourceID: "r1", ProcessQty: d("5")}}, MethodJobs, Forward, at)
	if err != nil || len(s.Jobs) != 1 || s.Jobs[0].Type != JobProcess || !s.End.Equal(at) {
		t.Fatalf("%+v %v", s, err)
	}
}

// An order without a route (a sales line order before its configuration resolves one) can still
// be scheduled: it takes no time and has no jobs.
func TestPlanSchedule_NoOperationsTakesNoTime(t *testing.T) {
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	for _, dir := range []Direction{Forward, Backward} {
		s, err := PlanSchedule(nil, MethodJobs, dir, at)
		if err != nil || len(s.Jobs) != 0 || len(s.Windows) != 0 || !s.Start.Equal(at) || !s.End.Equal(at) {
			t.Fatalf("%s: %+v %v", dir, s, err)
		}
	}
}

// Hours keep 6 decimals (the job column), and the instants follow the rounded hours.
func TestPlanSchedule_RoundsHours(t *testing.T) {
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	ops := []Operation{{OperNumber: 10, ResourceID: "r1", RunHPer: d("0.3333"), ProcessQty: d("1.000001")}}
	s, err := PlanSchedule(ops, MethodJobs, Forward, at)
	// 0.3333 h × 1.000001 = 0.3333003333 h: 0.333300 h kept, 1199.88 s rounded to 1200 s.
	if err != nil || s.Jobs[0].Hours.String() != "0.3333" || !s.End.Equal(at.Add(1200*time.Second)) {
		t.Fatalf("%+v %v", s.Jobs, err)
	}
}

func TestPlanSchedule_Refusals(t *testing.T) {
	at := time.Now()
	huge := []Operation{{OperNumber: 10, ResourceID: "r1", RunHPer: d("1000"), ProcessQty: d("101")}}
	for name, run := range map[string]func() error{
		"bad method":        func() error { _, e := PlanSchedule(routeForSchedule(), "x", Forward, at); return e },
		"bad direction":     func() error { _, e := PlanSchedule(routeForSchedule(), MethodJobs, "x", at); return e },
		"over 100000 hours": func() error { _, e := PlanSchedule(huge, MethodJobs, Forward, at); return e },
	} {
		var de *Error
		if err := run(); !errors.As(err, &de) || de.Code != CodeScheduleInput {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestResolveAnchor(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 30, 15, 500, time.UTC)
	day := "2026-11-10"
	explicit := time.Date(2026, 11, 5, 6, 0, 0, 0, time.FixedZone("x", 3600))
	cases := []struct {
		name string
		dir  Direction
		at   *time.Time
		del  *string
		want time.Time
		fail bool
	}{
		{"forward defaults to now", Forward, nil, nil, time.Date(2026, 11, 2, 9, 30, 15, 0, time.UTC), false},
		{"backward defaults to the end of the delivery date", Backward, nil, &day, time.Date(2026, 11, 10, 23, 59, 59, 0, time.UTC), false},
		{"explicit instant wins, in UTC", Backward, &explicit, nil, explicit.UTC(), false},
		{"backward needs a date", Backward, nil, nil, time.Time{}, true},
	}
	for _, c := range cases {
		got, err := ResolveAnchor(c.dir, c.at, c.del, now)
		if (err != nil) != c.fail || (!c.fail && !got.Equal(c.want)) {
			t.Errorf("%s: %v %v", c.name, got, err)
		}
	}
}
