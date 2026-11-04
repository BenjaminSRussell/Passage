package domain

import (
	"testing"
	"time"
)

func snap(job, order string, oper int, res string, startH, endH int, late int) SnapJob {
	t0 := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	return SnapJob{JobID: job, OrderID: order, OperNumber: oper, ResourceID: res,
		Start: t0.Add(time.Duration(startH) * time.Hour), End: t0.Add(time.Duration(endH) * time.Hour), LateMinutes: late}
}

func TestCompareRunsClassifiesJobs(t *testing.T) {
	base := []SnapJob{
		snap("j1", "o1", 10, "r1", 8, 10, 0),
		snap("j2", "o1", 20, "r1", 10, 12, 0),
		snap("j3", "o2", 10, "r2", 8, 9, 30),
		snap("j4", "o3", 10, "r2", 9, 10, 0),
	}
	other := []SnapJob{
		snap("j1", "o1", 10, "r1", 8, 10, 0),  // unchanged
		snap("j2", "o1", 20, "r1", 11, 13, 0), // shifted an hour
		snap("j3", "o2", 10, "r3", 8, 9, 0),   // other resource, no longer late
		snap("j5", "o4", 10, "r1", 13, 14, 0), // only in other
	}
	d := CompareRuns(base, other)
	if d.Unchanged != 1 || len(d.Changed) != 2 || len(d.Added) != 1 || len(d.Removed) != 1 {
		t.Fatalf("diff = %+v", d)
	}
	if d.Added[0].JobID != "j5" || d.Removed[0].JobID != "j4" {
		t.Fatalf("added %v removed %v", d.Added, d.Removed)
	}
	c := d.Changed[0]
	if c.Base.JobID != "j2" || c.ResourceChanged || c.StartShiftMinutes != 60 || c.EndShiftMinutes != 60 || c.LateDeltaMinutes != 0 {
		t.Fatalf("j2 change = %+v", c)
	}
	c = d.Changed[1]
	if c.Base.JobID != "j3" || !c.ResourceChanged || c.StartShiftMinutes != 0 || c.LateDeltaMinutes != -30 {
		t.Fatalf("j3 change = %+v", c)
	}
}

func TestCompareRunsIdenticalAndEmpty(t *testing.T) {
	a := []SnapJob{snap("j1", "o1", 10, "r1", 8, 10, 0)}
	d := CompareRuns(a, a)
	if d.Unchanged != 1 || len(d.Changed)+len(d.Added)+len(d.Removed) != 0 {
		t.Fatalf("identical diff = %+v", d)
	}
	d = CompareRuns(nil, nil)
	if d.Added == nil || d.Removed == nil || d.Changed == nil {
		t.Fatalf("empty lists must be non-nil for JSON: %+v", d)
	}
}

func TestMeasureSnapshot(t *testing.T) {
	jobs := []SnapJob{snap("j1", "o1", 10, "r1", 8, 10, 0), snap("j2", "o1", 20, "r1", 10, 14, 45), snap("j3", "o2", 10, "r2", 9, 10, 15)}
	jobs[1].ChangeoverMinutes = 20
	m := MeasureSnapshot(jobs)
	if m.Jobs != 3 || m.Orders != 2 || m.LateJobs != 2 || m.TotalLateMinutes != 60 || m.MaxLateMinutes != 45 || m.ChangeoverMinutes != 20 || m.MakespanMinutes != 360 {
		t.Fatalf("measures = %+v", m)
	}
	if z := MeasureSnapshot(nil); z != (SnapMeasures{}) {
		t.Fatalf("empty = %+v", z)
	}
}
