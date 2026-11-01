package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func fjob(oper, seq int, t JobType, h string, start, end time.Time) PlannedJob {
	return PlannedJob{OperNumber: oper, Seq: seq, Type: t, ResourceID: "r", Hours: decimal.RequireFromString(h), Start: start, End: end}
}

func TestApplyFiniteWindows_ProportionalAndExact(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	ops := []Operation{{OperNumber: 10}, {OperNumber: 20}}
	jobs := []PlannedJob{
		fjob(10, 1, JobSetup, "1", t0, t0.Add(time.Hour)),
		fjob(10, 2, JobProcess, "3", t0.Add(time.Hour), t0.Add(4*time.Hour)),
		fjob(20, 1, JobProcess, "2", t0.Add(4*time.Hour), t0.Add(6*time.Hour)),
	}
	// Operation 10 stretched over a weekend gap to 32 h, ending where it did so operation 20 can keep its dates.
	w := OperationWindow{OperNumber: 10, Start: t0.Add(-28 * time.Hour), End: t0.Add(4 * time.Hour)}
	got, err := ApplyFiniteWindows(ops, jobs, []OperationWindow{w}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Jobs) != 3 || !got.Jobs[0].Start.Equal(w.Start) || !got.Jobs[0].End.Equal(w.Start.Add(8*time.Hour)) {
		t.Fatalf("setup job = %v", got.Jobs[0])
	}
	if !got.Jobs[1].Start.Equal(got.Jobs[0].End) || !got.Jobs[1].End.Equal(w.End) {
		t.Fatalf("process job = %v must run from the setup end to the window end", got.Jobs[1])
	}
	if !got.Jobs[2].Start.Equal(t0.Add(4 * time.Hour)) {
		t.Fatalf("operation 20 must keep its job: %v", got.Jobs[2])
	}
	if !got.Start.Equal(w.Start) || !got.End.Equal(t0.Add(6*time.Hour)) || len(got.Compressed) != 0 {
		t.Fatalf("order span = %v to %v", got.Start, got.End)
	}
}

func TestApplyFiniteWindows_ZeroHoursSplitEqually(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	jobs := []PlannedJob{fjob(10, 1, JobQueueBefore, "0", t0, t0), fjob(10, 2, JobProcess, "0", t0, t0)}
	got, err := ApplyFiniteWindows([]Operation{{OperNumber: 10}}, jobs, []OperationWindow{{OperNumber: 10, Start: t0, End: t0.Add(10 * time.Minute)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Jobs[0].End.Equal(t0.Add(5*time.Minute)) || !got.Jobs[1].End.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("jobs = %v", got.Jobs)
	}
}

func TestApplyFiniteWindows_Refusals(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	ops := []Operation{{OperNumber: 10}}
	jobs := []PlannedJob{fjob(10, 1, JobProcess, "1", t0, t0.Add(time.Hour))}
	cases := map[string]struct {
		w      []OperationWindow
		locked map[int]bool
		code   string
	}{
		"empty":     {nil, nil, CodeFiniteInput},
		"unknown":   {[]OperationWindow{{OperNumber: 99, Start: t0, End: t0}}, nil, CodeFiniteInput},
		"backwards": {[]OperationWindow{{OperNumber: 10, Start: t0, End: t0.Add(-time.Hour)}}, nil, CodeFiniteInput},
		"twice":     {[]OperationWindow{{OperNumber: 10, Start: t0, End: t0}, {OperNumber: 10, Start: t0, End: t0}}, nil, CodeFiniteInput},
		"locked":    {[]OperationWindow{{OperNumber: 10, Start: t0, End: t0.Add(time.Hour)}}, map[int]bool{10: true}, CodeFiniteLocked},
	}
	for name, c := range cases {
		_, err := ApplyFiniteWindows(ops, jobs, c.w, c.locked)
		var de *Error
		if !errors.As(err, &de) || de.Code != c.code {
			t.Errorf("%s: err = %v, want %s", name, err, c.code)
		}
	}
}

func TestApplyFiniteWindows_JobsTileTheWindow(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	for _, secs := range []int{1, 7, 3599, 100001} {
		jobs := []PlannedJob{fjob(10, 1, JobQueueBefore, "0.3333", t0, t0), fjob(10, 2, JobSetup, "0.1", t0, t0), fjob(10, 3, JobProcess, "7.7", t0, t0)}
		w := OperationWindow{OperNumber: 10, Start: t0, End: t0.Add(time.Duration(secs) * time.Second)}
		got, err := ApplyFiniteWindows([]Operation{{OperNumber: 10}}, jobs, []OperationWindow{w}, nil)
		if err != nil {
			t.Fatal(err)
		}
		cursor := t0
		for _, j := range got.Jobs {
			if !j.Start.Equal(cursor) || j.End.Before(j.Start) {
				t.Fatalf("secs=%d: gap or overlap at %v", secs, j)
			}
			cursor = j.End
		}
		if !cursor.Equal(w.End) {
			t.Fatalf("secs=%d: jobs end at %v, window at %v", secs, cursor, w.End)
		}
	}
}

func TestCheckFiniteStatus(t *testing.T) {
	for _, s := range []Status{Scheduled, Released, Started} {
		if err := CheckFiniteStatus(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []Status{Created, Estimated, ReportedAsFinished, Ended, Cancelled} {
		if CheckFiniteStatus(s) == nil {
			t.Errorf("%s must be refused", s)
		}
	}
}

// Windows must follow the route: an operation may overlap the one before it (BR-23.5) but may not
// start or end before it, whether the earlier one moved or kept its dates.
func TestApplyFiniteWindows_RouteOrder(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	ops := []Operation{{OperNumber: 10}, {OperNumber: 20}}
	jobs := []PlannedJob{fjob(10, 1, JobProcess, "2", t0, t0.Add(2*time.Hour)), fjob(20, 1, JobProcess, "2", t0.Add(2*time.Hour), t0.Add(4*time.Hour))}
	win := func(oper int, from, to time.Duration) OperationWindow {
		return OperationWindow{OperNumber: oper, Start: t0.Add(from), End: t0.Add(to)}
	}
	for name, w := range map[string][]OperationWindow{
		"20 before 10":       {win(10, 24*time.Hour, 26*time.Hour), win(20, 0, 2*time.Hour)},
		"10 moved past 20":   {win(10, 3*time.Hour, 5*time.Hour)},
		"20 ends inside 10":  {win(10, 0, 10*time.Hour), win(20, time.Hour, 9*time.Hour)},
		"20 moved before 10": {win(20, -2*time.Hour, 3*time.Hour)},
	} {
		_, err := ApplyFiniteWindows(ops, jobs, w, nil)
		var de *Error
		if !errors.As(err, &de) || de.Code != CodeFiniteInput {
			t.Errorf("%s: err = %v, want %s", name, err, CodeFiniteInput)
		}
	}
	// Overlap: 20 starts while 10 runs and ends after it.
	if _, err := ApplyFiniteWindows(ops, jobs, []OperationWindow{win(10, 0, 4*time.Hour), win(20, time.Hour, 5*time.Hour)}, nil); err != nil {
		t.Fatalf("overlapping windows in route order: %v", err)
	}
}

// A window shorter than the jobs' planned hours is kept (resource efficiency above 100% explains
// it) but named, so the order shows it; a window that fits the hours is not.
func TestApplyFiniteWindows_FlagsCompressedOperations(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	ops := []Operation{{OperNumber: 10}, {OperNumber: 20}}
	jobs := []PlannedJob{
		fjob(10, 1, JobSetup, "1", t0, t0.Add(time.Hour)), fjob(10, 2, JobProcess, "3", t0.Add(time.Hour), t0.Add(4*time.Hour)),
		fjob(20, 1, JobProcess, "2", t0.Add(4*time.Hour), t0.Add(6*time.Hour)),
	}
	got, err := ApplyFiniteWindows(ops, jobs, []OperationWindow{
		{OperNumber: 10, Start: t0, End: t0.Add(4*time.Hour - time.Second)},
		{OperNumber: 20, Start: t0.Add(4 * time.Hour), End: t0.Add(6 * time.Hour)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Compressed) != 1 || got.Compressed[0] != 10 {
		t.Fatalf("compressed = %v, want [10]", got.Compressed)
	}
	if !got.Jobs[1].End.Equal(t0.Add(4*time.Hour-time.Second)) || !got.Jobs[1].Hours.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("the compressed operation's jobs keep their hours and fill the window: %v", got.Jobs[1])
	}
}
