package domain

import (
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func ymd(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestPullScheduleFixture(t *testing.T) {
	// AC-33.13: 36-month study from 2026-01-31, pull points every 3 months, 7-day lead.
	months := []int{0, 3, 6, 9, 12, 18, 24, 36}
	pulls, err := PullSchedule(ymd(2026, time.January, 31), months, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Time{
		ymd(2026, time.January, 31),
		ymd(2026, time.April, 30),
		ymd(2026, time.July, 31),
		ymd(2026, time.October, 31),
		ymd(2027, time.January, 31),
		ymd(2027, time.July, 31),
		ymd(2028, time.January, 31),
		ymd(2029, time.January, 31),
	}
	for i, p := range pulls {
		if !p.PullDate.Equal(want[i]) {
			t.Errorf("pull %d months: %s, want %s", p.Months, p.PullDate.Format("2006-01-02"), want[i].Format("2006-01-02"))
		}
		if !p.OrderDate.Equal(want[i].AddDate(0, 0, -7)) {
			t.Errorf("order for pull %d: %s", p.Months, p.OrderDate.Format("2006-01-02"))
		}
	}
}

func TestAddMonthsClamped(t *testing.T) {
	cases := []struct {
		from   time.Time
		months int
		want   time.Time
	}{
		{ymd(2024, time.January, 31), 1, ymd(2024, time.February, 29)}, // leap year
		{ymd(2025, time.January, 31), 1, ymd(2025, time.February, 28)},
		{ymd(2025, time.August, 31), 6, ymd(2026, time.February, 28)},
		{ymd(2025, time.March, 15), 0, ymd(2025, time.March, 15)},
	}
	for _, c := range cases {
		if got := AddMonthsClamped(c.from, c.months); !got.Equal(c.want) {
			t.Errorf("%s + %d months = %s, want %s", c.from.Format("2006-01-02"), c.months, got.Format("2006-01-02"), c.want.Format("2006-01-02"))
		}
	}
}

func TestPullScheduleRejects(t *testing.T) {
	start := ymd(2026, time.January, 1)
	cases := []struct {
		name   string
		months []int
		lead   int
	}{
		{"empty", nil, 7},
		{"not ascending", []int{0, 6, 3}, 7},
		{"negative", []int{-1, 0}, 7},
		{"beyond 120", []int{0, 121}, 7},
		{"lead too long", []int{0}, 31},
	}
	for _, c := range cases {
		if _, err := PullSchedule(start, c.months, c.lead); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestLatePull(t *testing.T) {
	pull := ymd(2026, time.April, 30)
	days, dev, err := LatePull(pull, ymd(2026, time.May, 3), 3)
	if err != nil || days != 3 || dev {
		t.Errorf("3 days late within a 3-day window: %d %v %v", days, dev, err)
	}
	days, dev, _ = LatePull(pull, ymd(2026, time.May, 4), 3)
	if days != 4 || !dev {
		t.Errorf("4 days late must be a deviation: %d %v", days, dev)
	}
	days, dev, _ = LatePull(pull, ymd(2026, time.April, 28), 3)
	if days != -2 || dev {
		t.Errorf("early pull: %d %v", days, dev)
	}
	if _, _, err := LatePull(pull, pull, 15); err == nil {
		t.Error("window above 14 days must fail")
	}
}

func TestStudyTransitions(t *testing.T) {
	cases := []struct {
		from, to string
		ok       bool
	}{
		{StudyPlanned, StudyActive, true},
		{StudyPlanned, StudyTerminated, true},
		{StudyActive, StudyCompleted, true},
		{StudyActive, StudyTerminated, true},
		{StudyPlanned, StudyCompleted, false},
		{StudyCompleted, StudyActive, false},
		{StudyTerminated, StudyActive, false},
	}
	for _, c := range cases {
		if got := CanTransitionStudy(c.from, c.to); got != c.ok {
			t.Errorf("%s -> %s = %v", c.from, c.to, got)
		}
	}
}

func validSpec() StudySpec {
	return StudySpec{ItemID: ids.New(), BatchID: "B-1", Protocol: "ICH-Q1A", Condition: "25C/60RH",
		StartDate: ymd(2026, time.January, 31), TestGroupID: ids.New(), PullMonths: []int{0, 3, 6, 9, 12, 18, 24, 36},
		LeadDays: 7, WindowDays: 3}
}

func TestValidateStudy(t *testing.T) {
	if f := ValidateStudy(validSpec()); len(f) != 0 {
		t.Errorf("valid study: %v", f)
	}
	f := ValidateStudy(StudySpec{ItemID: "item", PullMonths: []int{3, 3}, PullTestGroups: map[int]string{6: ids.New()}, LeadDays: 31, WindowDays: 20})
	for _, k := range []string{"itemId", "batchId", "protocol", "condition", "startDate", "testGroupId", "pullMonths", "pullTestGroups.6", "leadDays", "windowDays"} {
		if f[k] == "" {
			t.Errorf("missing field error %s in %v", k, f)
		}
	}
}

// AC-33.13: a 36-month study starting 2026-01-31 pulls on 2026-01-31, 2026-04-30,
// 2026-07-31 and so on, each order due 7 days before; every pull carries the
// study's test group unless the pull names its own.
func TestNewStudyScheduleAndTestGroups(t *testing.T) {
	spec := validSpec()
	accelerated := ids.New()
	spec.PullTestGroups = map[int]string{36: accelerated}
	st, err := NewStudy(spec)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != StudyPlanned || st.Version != 1 || len(st.Pulls) != 8 {
		t.Fatalf("study: %+v", st)
	}
	want := map[int]string{0: "2026-01-31", 3: "2026-04-30", 6: "2026-07-31"}
	for _, p := range st.Pulls {
		if w, ok := want[p.Months]; ok && p.PullDate.Format("2006-01-02") != w {
			t.Errorf("pull %d on %s, want %s", p.Months, p.PullDate.Format("2006-01-02"), w)
		}
		if p.PullDate.Sub(p.OrderDate) != 7*24*time.Hour || p.Status != PullPlanned {
			t.Errorf("pull %d: order %s status %s", p.Months, p.OrderDate.Format("2006-01-02"), p.Status)
		}
		wantGroup := spec.TestGroupID
		if p.Months == 36 {
			wantGroup = accelerated
		}
		if p.TestGroupID != wantGroup {
			t.Errorf("pull %d tested with %s, want %s", p.Months, p.TestGroupID, wantGroup)
		}
	}
	if got := st.TestGroupIDs(); len(got) != 2 {
		t.Errorf("test groups = %v", got)
	}
}

func TestNewStudyRejects(t *testing.T) {
	for name, edit := range map[string]func(*StudySpec){
		"missing item":   func(s *StudySpec) { s.ItemID = "" },
		"no pulls":       func(s *StudySpec) { s.PullMonths = nil },
		"lead too large": func(s *StudySpec) { s.LeadDays = 40 },
		"window too big": func(s *StudySpec) { s.WindowDays = 15 },
		"no test group":  func(s *StudySpec) { s.TestGroupID = "" },
	} {
		spec := validSpec()
		edit(&spec)
		if _, err := NewStudy(spec); err == nil {
			t.Errorf("%s: want rejection", name)
		}
	}
}

func TestPullDatesProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		start := ymd(2020, time.Month(rapid.IntRange(1, 12).Draw(t, "month")), rapid.IntRange(1, 28).Draw(t, "day"))
		start = start.AddDate(0, 0, rapid.IntRange(0, 3).Draw(t, "extra"))
		m := rapid.IntRange(0, 120).Draw(t, "months")
		p := AddMonthsClamped(start, m)
		monthsApart := (p.Year()-start.Year())*12 + int(p.Month()) - int(start.Month())
		if monthsApart != m {
			t.Fatalf("%s + %d months landed in %s", start.Format("2006-01-02"), m, p.Format("2006-01-02"))
		}
		if p.Day() > start.Day() {
			t.Fatalf("day grew: %s -> %s", start.Format("2006-01-02"), p.Format("2006-01-02"))
		}
	})
}
