package store_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func TestDailyLoadAndDrillDown_ReconcileWithTheWeek(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m, tool := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "TOOL", true)
	a := secOperation(t, ctx, s, 10, 120, m.ID, tool.ID)
	b := secOperation(t, ctx, s, 20, 600, m.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}

	days, err := s.DailyCapacityLoad(ctx, store.DailyLoadFilter{ResourceIDs: []string{m.ID}, From: at(0, 13, 0), Days: 7})
	if err != nil || len(days) != 7 {
		t.Fatalf("days = %+v err=%v", days, err)
	}
	if !days[0].Day.Equal(monday) || days[0].OpenMinutes != 480 {
		t.Fatalf("Monday = %+v, want the UTC day with 480 open minutes", days[0])
	}
	week, err := s.CapacityLoad(ctx, store.LoadFilter{ResourceIDs: []string{m.ID}, From: monday, Weeks: 1, ThresholdPct: 90, Consecutive: 1})
	if err != nil || len(week) != 1 {
		t.Fatalf("week = %+v err=%v", week, err)
	}
	sum, open := 0, 0
	for _, d := range days {
		sum += d.BookedMinutes
		open += d.OpenMinutes
	}
	if sum != 720 || sum != week[0].BookedMinutes || open != week[0].OpenMinutes {
		t.Fatalf("days book %d of %d, week books %d of %d; want 720 booked in both", sum, open, week[0].BookedMinutes, week[0].OpenMinutes)
	}
	if days[0].BookedMinutes != 480 || days[1].BookedMinutes != 240 {
		t.Fatalf("Monday/Tuesday booked = %d/%d, want the 720 minutes to fill Monday then spill 240 to Tuesday", days[0].BookedMinutes, days[1].BookedMinutes)
	}

	jobs, truncated, err := s.LoadJobs(ctx, m.ID, monday, at(7, 0, 0))
	if err != nil || truncated {
		t.Fatalf("jobs err=%v truncated=%v", err, truncated)
	}
	total, ids := 0, map[string]bool{}
	for _, j := range jobs {
		total += j.MinutesInRange
		ids[j.JobID] = true
		if j.Role != "primary" || !j.Finite {
			t.Errorf("job = %+v, want a finite primary booking", j)
		}
	}
	if total != 720 || !ids[a.ID] || !ids[b.ID] {
		t.Fatalf("jobs = %+v, want both operations and 720 minutes", jobs)
	}

	// Tuesday only: the part of the long job after Monday's close.
	tue, _, err := s.LoadJobs(ctx, m.ID, at(1, 0, 0), at(2, 0, 0))
	if err != nil || len(tue) == 0 {
		t.Fatalf("tuesday jobs = %+v err=%v", tue, err)
	}
	sum = 0
	for _, j := range tue {
		sum += j.MinutesInRange
	}
	if sum != days[1].BookedMinutes {
		t.Fatalf("Tuesday drill-down %d minutes, daily cell %d", sum, days[1].BookedMinutes)
	}

	if _, _, err := s.FirmJob(ctx, a.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	sec, _, err := s.LoadJobs(ctx, tool.ID, monday, at(7, 0, 0))
	if err != nil || len(sec) != 1 || sec[0].Role != "secondary" || sec[0].JobID != a.ID || sec[0].OperNumber != 10 || sec[0].MinutesInRange != 120 {
		t.Fatalf("tool jobs = %+v err=%v, want the secondary booking of operation 10", sec, err)
	}
	if !sec[0].Firmed {
		t.Fatalf("secondary booking = %+v, want the firmed status of its primary segment", sec[0])
	}
}

func TestDailyLoad_DaysAreLocalToTheResourceCalendar(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	// 06:00-14:00 Tokyo is 21:00-05:00 UTC, so a UTC day would hold the end of
	// one shift and the start of the next.
	r, _, err := s.CreateResource(ctx, store.Resource{Code: "PRESS-T", Name: "Tokyo press", Finite: true, TimeZone: "Asia/Tokyo", Shifts: weekdays(6*60, 14*60)}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	op := secOperation(t, ctx, s, 10, 600, r.ID)
	// Monday 00:00 UTC is 09:00 in Tokyo: 300 minutes fit on Monday, 300 spill to Tuesday.
	if _, _, err := s.RunSchedule(ctx, siteRun(monday), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	days, err := s.DailyCapacityLoad(ctx, store.DailyLoadFilter{ResourceIDs: []string{r.ID}, From: monday, Days: 2})
	if err != nil || len(days) != 2 {
		t.Fatalf("days = %+v err=%v", days, err)
	}
	for i, d := range days {
		if d.OpenMinutes != 480 || d.BookedMinutes != 300 {
			t.Fatalf("day %d = %+v, want one whole 480-minute shift with 300 booked", i, d)
		}
	}
	if days[1].Day.Format(time.DateOnly) != "2027-01-05" || !days[1].Start.Equal(at(0, 15, 0)) || !days[1].End.Equal(at(1, 15, 0)) {
		t.Fatalf("Tuesday = %v [%v, %v), want Tokyo midnight to midnight", days[1].Day, days[1].Start, days[1].End)
	}
	jobs, _, err := s.LoadJobs(ctx, r.ID, days[1].Start, days[1].End)
	if err != nil || len(jobs) != 1 || jobs[0].JobID != op.ID || jobs[0].MinutesInRange != 300 {
		t.Fatalf("Tuesday jobs = %+v err=%v, want the 300 minutes of the cell", jobs, err)
	}
}

func TestLoadJobs_UnknownResourceAndOtherTenant(t *testing.T) {
	s, _, _ := setup(t)
	one, two := tenantCtx(1), tenantCtx(2)
	r := mustResource(t, one, s, "M1", true)
	if _, _, err := s.LoadJobs(two, r.ID, monday, at(7, 0, 0)); problemCode(err) != store.CodeResourceNotFound {
		t.Fatalf("tenant 2 reads tenant 1 resource: err=%v", err)
	}
	if jobs, _, err := s.LoadJobs(one, r.ID, monday, at(7, 0, 0)); err != nil || len(jobs) != 0 {
		t.Fatalf("empty resource: jobs=%+v err=%v", jobs, err)
	}
	if days, err := s.DailyCapacityLoad(two, store.DailyLoadFilter{From: monday, Days: 3}); err != nil || len(days) != 0 {
		t.Fatalf("tenant 2 sees days %+v err=%v", days, err)
	}
}
