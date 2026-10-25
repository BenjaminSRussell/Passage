package domain_test

import (
	"bytes"
	"encoding/csv"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

func ymd(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

var febSunday = domain.CalendarSpec{StartMonth: time.February, WeekStart: time.Sunday, OpeningClosing: true}

func withPattern(s domain.CalendarSpec, p domain.Pattern) domain.CalendarSpec {
	s.Pattern = p
	return s
}

func renderCSV(t *testing.T, periods []domain.FiscalPeriod) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Write([]string{"fiscal_year", "period_no", "name", "start_date", "end_date", "type", "week53"})
	for _, p := range periods {
		w.Write([]string{strconv.Itoa(p.FiscalYear), strconv.Itoa(p.PeriodNo), p.Name, p.Start.Format("2006-01-02"), p.End.Format("2006-01-02"), string(p.Type), strconv.FormatBool(p.Week53)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func requireCode(t *testing.T, err error, code string) *domain.Error {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	return de
}

func TestGenerateYear_MatchesGoldenFixtures(t *testing.T) {
	cal := domain.CalendarSpec{Pattern: domain.PatternCalendar, StartMonth: time.January, OpeningClosing: true}
	cases := []struct {
		file string
		spec domain.CalendarSpec
		year int
	}{
		{"calendar-2026", cal, 2026},
		{"4-4-5-2026", withPattern(febSunday, domain.Pattern445), 2026},
		{"4-4-5-2028", withPattern(febSunday, domain.Pattern445), 2028},
		{"13-period-2026", withPattern(febSunday, domain.Pattern13), 2026},
		{"4-5-4-2026", withPattern(febSunday, domain.Pattern454), 2026},
		{"5-4-4-2026", withPattern(febSunday, domain.Pattern544), 2026},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			periods, err := domain.GenerateYear(c.spec, c.year)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile("../../testdata/fiscal-patterns/" + c.file + ".csv")
			if err != nil {
				t.Fatal(err)
			}
			got := renderCSV(t, periods)
			if bytes.Equal(got, want) {
				return
			}
			gl, wl := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
			for i := 0; i < len(gl) || i < len(wl); i++ {
				var g, w string
				if i < len(gl) {
					g = gl[i]
				}
				if i < len(wl) {
					w = wl[i]
				}
				if g != w {
					t.Fatalf("line %d: got %q, want %q", i+1, g, w)
				}
			}
		})
	}
}

func TestGenerateYear_NRFFiftyThreeWeekYears(t *testing.T) {
	var got []int
	for y := 2010; y <= 2030; y++ {
		periods, err := domain.GenerateYear(withPattern(febSunday, domain.Pattern445), y)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range periods {
			if p.Week53 {
				got = append(got, y)
			}
		}
	}
	want := []int{2012, 2017, 2023, 2028}
	if len(got) != len(want) {
		t.Fatalf("53-week years = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("53-week years = %v, want %v", got, want)
		}
	}
}

func TestGenerateYear_RejectsInvalidSpec(t *testing.T) {
	_, err := domain.ParsePattern("weekly")
	requireCode(t, err, domain.CodeCalPattern)
	if p, err := domain.ParsePattern("4-5-4"); err != nil || p != domain.Pattern454 {
		t.Fatalf("ParsePattern(4-5-4) = %v, %v", p, err)
	}

	r := domain.DateRange{Start: ymd(2026, 1, 1), End: ymd(2026, 1, 31)}
	tooMany := make([]domain.DateRange, 25)
	for i := range tooMany {
		tooMany[i] = r
	}
	cases := []struct {
		name string
		spec domain.CalendarSpec
		year int
	}{
		{"zero value", domain.CalendarSpec{}, 2026},
		{"start month 0", domain.CalendarSpec{Pattern: domain.PatternCalendar}, 2026},
		{"start month 13", domain.CalendarSpec{Pattern: domain.PatternCalendar, StartMonth: 13}, 2026},
		{"special periods 5", domain.CalendarSpec{Pattern: domain.PatternCalendar, StartMonth: 1, SpecialPeriods: 5}, 2026},
		{"special periods -1", domain.CalendarSpec{Pattern: domain.PatternCalendar, StartMonth: 1, SpecialPeriods: -1}, 2026},
		{"year 1899", domain.CalendarSpec{Pattern: domain.PatternCalendar, StartMonth: 1}, 1899},
		{"year 3000", domain.CalendarSpec{Pattern: domain.PatternCalendar, StartMonth: 1}, 3000},
		{"empty custom", domain.CalendarSpec{Pattern: domain.PatternCustom, StartMonth: 1}, 2026},
		{"25 custom ranges", domain.CalendarSpec{Pattern: domain.PatternCustom, StartMonth: 1, Custom: tooMany}, 2026},
		{"custom end before start", domain.CalendarSpec{Pattern: domain.PatternCustom, StartMonth: 1, Custom: []domain.DateRange{{Start: ymd(2026, 2, 1), End: ymd(2026, 1, 1)}}}, 2026},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			periods, err := domain.GenerateYear(c.spec, c.year)
			requireCode(t, err, domain.CodeCalPattern)
			if periods != nil {
				t.Fatalf("periods = %v, want nil", periods)
			}
		})
	}
}

func TestGenerateYear_Custom(t *testing.T) {
	spec := func(secondStart time.Time) domain.CalendarSpec {
		return domain.CalendarSpec{Pattern: domain.PatternCustom, StartMonth: 1, OpeningClosing: true, Custom: []domain.DateRange{
			{Start: ymd(2026, 1, 1), End: ymd(2026, 4, 30)},
			{Start: secondStart, End: ymd(2026, 8, 31)},
			{Start: ymd(2026, 9, 1), End: ymd(2026, 12, 31)},
		}}
	}

	periods, err := domain.GenerateYear(spec(ymd(2026, 5, 1)), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(periods) != 5 || periods[1].Name != "P01" || periods[3].Name != "P03" || periods[4].PeriodNo != 99 {
		t.Fatalf("periods = %+v", periods)
	}

	err = func() error { _, err := domain.GenerateYear(spec(ymd(2026, 5, 2)), 2026); return err }()
	if de := requireCode(t, err, domain.CodeCalGap); de.Detail != "periods leave a gap on 2026-05-01" {
		t.Fatalf("detail = %q", de.Detail)
	}
	err = func() error { _, err := domain.GenerateYear(spec(ymd(2026, 4, 30)), 2026); return err }()
	if de := requireCode(t, err, domain.CodeCalOverlap); de.Detail != "periods overlap on 2026-04-30" {
		t.Fatalf("detail = %q", de.Detail)
	}
}

func TestGenerateYear_SpecialAndOpeningClosing(t *testing.T) {
	spec := withPattern(febSunday, domain.Pattern445)
	spec.SpecialPeriods = 2
	periods, err := domain.GenerateYear(spec, 2026)
	if err != nil {
		t.Fatal(err)
	}
	last := ymd(2027, 1, 30)
	n := len(periods)
	for i, want := range []struct {
		no   int
		name string
		typ  domain.PeriodType
	}{{13, "A13", domain.PeriodAdjustment}, {14, "A14", domain.PeriodAdjustment}, {99, "Closing", domain.PeriodClosing}} {
		p := periods[n-3+i]
		if p.PeriodNo != want.no || p.Name != want.name || p.Type != want.typ || !p.Start.Equal(last) || !p.End.Equal(last) {
			t.Fatalf("periods[%d] = %+v, want %+v on %s", n-3+i, p, want, last)
		}
	}

	spec = withPattern(febSunday, domain.Pattern13)
	spec.SpecialPeriods = 1
	periods, err = domain.GenerateYear(spec, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if p := periods[len(periods)-2]; p.PeriodNo != 14 || p.Name != "A14" {
		t.Fatalf("adjustment = %+v", p)
	}

	spec.OpeningClosing = false
	spec.SpecialPeriods = 0
	periods, err = domain.GenerateYear(spec, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(periods) != 13 || periods[0].PeriodNo != 1 || periods[12].PeriodNo != 13 {
		t.Fatalf("periods = %+v", periods)
	}
}

func TestProveContiguous(t *testing.T) {
	year := domain.DateRange{Start: ymd(2026, 1, 1), End: ymd(2026, 1, 31)}
	operating := domain.FiscalPeriod{Name: "P01", Start: year.Start, End: year.End, Type: domain.PeriodOperating}
	if err := domain.ProveContiguous([]domain.FiscalPeriod{operating}, year); err != nil {
		t.Fatal(err)
	}

	short := operating
	short.End = ymd(2026, 1, 30)
	err := domain.ProveContiguous([]domain.FiscalPeriod{short}, year)
	if de := requireCode(t, err, domain.CodeCalGap); de.Detail != "periods leave a gap on 2026-01-31" {
		t.Fatalf("detail = %q", de.Detail)
	}

	long := operating
	long.End = ymd(2026, 2, 1)
	err = domain.ProveContiguous([]domain.FiscalPeriod{long}, year)
	if de := requireCode(t, err, domain.CodeCalOverlap); de.Detail != "periods overlap on 2026-02-01" {
		t.Fatalf("detail = %q", de.Detail)
	}

	err = domain.ProveContiguous(nil, year)
	requireCode(t, err, domain.CodeCalGap)

	opening := domain.FiscalPeriod{Name: "Opening", Start: ymd(2026, 1, 2), End: ymd(2026, 1, 2), Type: domain.PeriodOpening}
	err = domain.ProveContiguous([]domain.FiscalPeriod{opening, operating}, year)
	requireCode(t, err, domain.CodeCalPattern)
}

func TestGenerateYear_PropertyTilesEveryYear(t *testing.T) {
	patterns := []domain.Pattern{domain.PatternCalendar, domain.Pattern445, domain.Pattern454, domain.Pattern544, domain.Pattern13}
	rapid.Check(t, func(t *rapid.T) {
		spec := domain.CalendarSpec{
			Pattern:        rapid.SampledFrom(patterns).Draw(t, "pattern"),
			StartMonth:     time.Month(rapid.IntRange(1, 12).Draw(t, "startMonth")),
			WeekStart:      time.Weekday(rapid.IntRange(0, 6).Draw(t, "weekStart")),
			OpeningClosing: rapid.Bool().Draw(t, "openingClosing"),
		}
		year := rapid.IntRange(1950, 2150).Draw(t, "year")

		periods, err := domain.GenerateYear(spec, year)
		if err != nil {
			t.Fatal(err)
		}
		var operating []domain.FiscalPeriod
		opening, closing, week53 := 0, 0, 0
		for _, p := range periods {
			switch p.PeriodNo {
			case 0:
				opening++
			case 99:
				closing++
			default:
				operating = append(operating, p)
			}
			if p.Week53 {
				week53++
			}
		}
		if spec.OpeningClosing != (opening == 1 && closing == 1) || (!spec.OpeningClosing && opening+closing != 0) {
			t.Fatalf("opening=%d closing=%d with OpeningClosing=%v", opening, closing, spec.OpeningClosing)
		}
		for i := 1; i < len(operating); i++ {
			if !operating[i].Start.Equal(operating[i-1].End.AddDate(0, 0, 1)) {
				t.Fatalf("period %d does not follow period %d", operating[i].PeriodNo, operating[i-1].PeriodNo)
			}
		}
		start, end := operating[0].Start, operating[len(operating)-1].End
		days := int(end.Sub(start).Hours()/24) + 1
		if spec.Pattern == domain.PatternCalendar && days != 365 && days != 366 {
			t.Fatalf("calendar year has %d days", days)
		}
		if spec.Pattern != domain.PatternCalendar && days != 364 && days != 371 {
			t.Fatalf("week-based year has %d days", days)
		}
		if week53 > 1 || (week53 == 1 && days != 371) || (week53 == 0 && days == 371) {
			t.Fatalf("week53=%d in a %d-day year", week53, days)
		}
		next, err := domain.GenerateYear(spec, year+1)
		if err != nil {
			t.Fatal(err)
		}
		nextStart := next[0].Start
		if spec.OpeningClosing {
			nextStart = next[1].Start
		}
		if !nextStart.Equal(end.AddDate(0, 0, 1)) {
			t.Fatalf("year %d starts %s, want %s", year+1, nextStart, end.AddDate(0, 0, 1))
		}
	})
}
