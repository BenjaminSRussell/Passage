package domain

import "time"

// ResourceKPI is the use of one resource by the segments of a schedule run
// inside the run's span (PRD 4.23.14 Resource utilization). Utilization is
// booked minutes over open minutes of the resource's calendar in the span.
type ResourceKPI struct {
	ResourceID     string
	BookedMinutes  int
	OpenMinutes    int
	UtilizationPct float64
	Jobs           int
}

// RunUtilization reports how much of r's open time in [start, end) the run's
// segments on r fill. Segments are the run's own bookings only, so another
// run's jobs on the same resource do not count. The calendar is read from one
// local day before start to one after end, so a zone east or west of UTC
// contributes its straddling days.
func RunUtilization(r Resource, segments []Busy, start, end time.Time) ResourceKPI {
	k := ResourceKPI{ResourceID: r.ID}
	jobs := map[string]bool{}
	for _, s := range segments {
		k.BookedMinutes += ClippedMinutes(s.Start, s.End, start, end)
		jobs[s.JobID] = true
	}
	k.Jobs = len(jobs)
	loc := r.Location
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := start.In(loc).Date()
	days := int(end.Sub(start).Hours()/24) + 4
	for _, i := range r.Open(time.Date(y, m, d-1, 0, 0, 0, 0, loc), days) {
		k.OpenMinutes += ClippedMinutes(i.Start, i.End, start, end)
	}
	if k.OpenMinutes > 0 {
		k.UtilizationPct = float64(k.BookedMinutes) * 100 / float64(k.OpenMinutes)
	}
	return k
}
