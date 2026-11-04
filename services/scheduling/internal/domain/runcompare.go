package domain

import (
	"sort"
	"time"
)

// SnapJob is one job as a schedule run placed it: the whole job, not a
// segment. The resource is that of the first segment, Start the first start
// and End the last end.
type SnapJob struct {
	JobID             string
	OrderID           string
	OperNumber        int
	ResourceID        string
	Start             time.Time
	End               time.Time
	LateMinutes       int
	ChangeoverMinutes int
}

// JobChange is a job that both runs placed, differently.
type JobChange struct {
	Base  SnapJob
	Other SnapJob
	// ResourceChanged is true when the job moved to another resource.
	ResourceChanged bool
	// StartShiftMinutes and EndShiftMinutes are Other minus Base, negative
	// when the other run places the job earlier.
	StartShiftMinutes int
	EndShiftMinutes   int
	// LateDeltaMinutes is Other's lateness minus Base's.
	LateDeltaMinutes int
}

// RunDiff is the difference between two runs' snapshots. Removed holds jobs
// only the base run placed, Added jobs only the other run placed.
type RunDiff struct {
	Added     []SnapJob
	Removed   []SnapJob
	Changed   []JobChange
	Unchanged int
}

// CompareRuns diffs two snapshots by job. A job is unchanged when resource,
// start and end are all equal; the changeover and lateness follow from them.
// Every list is ordered by order id, operation number and job id so the
// answer is deterministic.
func CompareRuns(base, other []SnapJob) RunDiff {
	d := RunDiff{Added: []SnapJob{}, Removed: []SnapJob{}, Changed: []JobChange{}}
	b := make(map[string]SnapJob, len(base))
	for _, j := range base {
		b[j.JobID] = j
	}
	seen := make(map[string]bool, len(other))
	for _, o := range other {
		seen[o.JobID] = true
		p, ok := b[o.JobID]
		if !ok {
			d.Added = append(d.Added, o)
			continue
		}
		if p.ResourceID == o.ResourceID && p.Start.Equal(o.Start) && p.End.Equal(o.End) {
			d.Unchanged++
			continue
		}
		d.Changed = append(d.Changed, JobChange{Base: p, Other: o, ResourceChanged: p.ResourceID != o.ResourceID,
			StartShiftMinutes: int(o.Start.Sub(p.Start) / time.Minute), EndShiftMinutes: int(o.End.Sub(p.End) / time.Minute),
			LateDeltaMinutes: o.LateMinutes - p.LateMinutes})
	}
	for _, j := range base {
		if !seen[j.JobID] {
			d.Removed = append(d.Removed, j)
		}
	}
	less := func(a, b SnapJob) bool {
		if a.OrderID != b.OrderID {
			return a.OrderID < b.OrderID
		}
		if a.OperNumber != b.OperNumber {
			return a.OperNumber < b.OperNumber
		}
		return a.JobID < b.JobID
	}
	sort.Slice(d.Added, func(i, j int) bool { return less(d.Added[i], d.Added[j]) })
	sort.Slice(d.Removed, func(i, j int) bool { return less(d.Removed[i], d.Removed[j]) })
	sort.Slice(d.Changed, func(i, j int) bool { return less(d.Changed[i].Base, d.Changed[j].Base) })
	return d
}

// SnapMeasures are the figures of one snapshot shown side by side in a run
// comparison.
type SnapMeasures struct {
	Jobs              int
	Orders            int
	LateJobs          int
	TotalLateMinutes  int
	MaxLateMinutes    int
	ChangeoverMinutes int
	MakespanMinutes   int
}

// MeasureSnapshot computes the comparison figures of a snapshot.
func MeasureSnapshot(jobs []SnapJob) SnapMeasures {
	m := SnapMeasures{Jobs: len(jobs)}
	orders := map[string]bool{}
	var first, last time.Time
	for i, j := range jobs {
		orders[j.OrderID] = true
		if j.LateMinutes > 0 {
			m.LateJobs++
			m.TotalLateMinutes += j.LateMinutes
			m.MaxLateMinutes = max(m.MaxLateMinutes, j.LateMinutes)
		}
		m.ChangeoverMinutes += j.ChangeoverMinutes
		if i == 0 || j.Start.Before(first) {
			first = j.Start
		}
		if i == 0 || j.End.After(last) {
			last = j.End
		}
	}
	m.Orders = len(orders)
	if len(jobs) > 0 {
		m.MakespanMinutes = int(last.Sub(first) / time.Minute)
	}
	return m
}
