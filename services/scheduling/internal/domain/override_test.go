package domain

import "testing"

func TestApplyOverrides_DowntimeSplitsAndExtraOpens(t *testing.T) {
	shifts := weekdayShift(8*60, 16*60)
	cases := []struct {
		name string
		ovs  []CapacityOverride
		want int // open minutes on Monday and Tuesday
	}{
		{"none", nil, 960},
		{"downtime inside a shift", []CapacityOverride{{Kind: OverrideDowntime, Start: at(0, 10, 0), End: at(0, 12, 0)}}, 840},
		{"downtime covering a whole day", []CapacityOverride{{Kind: OverrideDowntime, Start: at(0, 0, 0), End: at(1, 0, 0)}}, 480},
		{"extra shift on a closed evening", []CapacityOverride{{Kind: OverrideExtra, Start: at(0, 18, 0), End: at(0, 20, 0)}}, 1080},
		{"extra overlapping a shift is not counted twice", []CapacityOverride{{Kind: OverrideExtra, Start: at(0, 14, 0), End: at(0, 18, 0)}}, 1080},
		{"downtime wins over an overlapping extra", []CapacityOverride{
			{Kind: OverrideExtra, Start: at(0, 18, 0), End: at(0, 20, 0)},
			{Kind: OverrideDowntime, Start: at(0, 17, 0), End: at(0, 22, 0)}}, 960},
		{"extra beyond the window adds nothing", []CapacityOverride{{Kind: OverrideExtra, Start: at(30, 8, 0), End: at(30, 12, 0)}}, 960},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := 0
			for _, i := range OpenWithOverrides(nil, shifts, c.ovs, monday, 2) {
				got += i.Minutes()
			}
			if got != c.want {
				t.Fatalf("open minutes = %d, want %d", got, c.want)
			}
		})
	}
}

func TestSchedule_SkipsDowntimeAndUsesExtraShift(t *testing.T) {
	shifts := weekdayShift(8*60, 12*60)
	r := res("r1", "A", true, shifts)
	r.Overrides = []CapacityOverride{{Kind: OverrideDowntime, Start: at(0, 8, 0), End: at(0, 11, 0)}}
	p := Problem{Resources: []Resource{r}, Start: monday, HorizonDays: 7,
		Operations: []Operation{{ID: "op", OrderID: "o", OperNumber: 10, Minutes: 120, Candidates: []string{"r1"}}}}
	got := Schedule(p).Placements
	if len(got) != 1 || len(got[0].Segments) != 2 {
		t.Fatalf("placements = %+v, want two segments", got)
	}
	if s := got[0].Segments[0]; !s.Start.Equal(at(0, 11, 0)) || !s.End.Equal(at(0, 12, 0)) {
		t.Fatalf("first segment = %v-%v, want 11:00-12:00 after the downtime", s.Start, s.End)
	}
	r.Overrides = []CapacityOverride{{Kind: OverrideExtra, Start: at(0, 12, 0), End: at(0, 14, 0)}}
	p.Resources = []Resource{r}
	p.Operations[0].Minutes = 360
	got = Schedule(p).Placements
	if len(got) != 1 || !got[0].End.Equal(at(0, 14, 0)) {
		t.Fatalf("placements = %+v, want the job to end 14:00 inside the extra shift", got)
	}
}

func TestOutsideOpen(t *testing.T) {
	iv := []Interval{{at(0, 8, 0), at(0, 12, 0)}, {at(0, 12, 0), at(0, 16, 0)}, {at(1, 8, 0), at(1, 16, 0)}}
	cases := []struct {
		name string
		job  Interval
		want bool
	}{
		{"inside one interval", Interval{at(0, 9, 0), at(0, 10, 0)}, false},
		{"across adjacent intervals", Interval{at(0, 11, 0), at(0, 13, 0)}, false},
		{"exactly an interval", Interval{at(0, 8, 0), at(0, 12, 0)}, false},
		{"starts before the shift", Interval{at(0, 7, 0), at(0, 9, 0)}, true},
		{"runs past the shift", Interval{at(0, 15, 0), at(0, 17, 0)}, true},
		{"spans the closed night", Interval{at(0, 15, 0), at(1, 9, 0)}, true},
		{"after every interval", Interval{at(2, 8, 0), at(2, 9, 0)}, true},
	}
	for _, c := range cases {
		if got := OutsideOpen(iv, c.job.Start, c.job.End); got != c.want {
			t.Errorf("%s: OutsideOpen = %v, want %v", c.name, got, c.want)
		}
	}
	if !OutsideOpen(nil, at(0, 9, 0), at(0, 10, 0)) {
		t.Error("no open capacity: want outside")
	}
}
