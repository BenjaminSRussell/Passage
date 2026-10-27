package domain

import (
	"errors"
	"reflect"
	"testing"
)

// testCalendar: FY2025 with 12 regular periods and adjustment period 13, FY2026
// with 12 regular periods. FY2024 is absent so PY from 2025 has no target.
func testCalendar(t *testing.T) Calendar {
	t.Helper()
	var ps []Period
	for no := 1; no <= 12; no++ {
		ps = append(ps, Period{Year: 2025, No: no, Quarter: (no-1)/3 + 1})
	}
	ps = append(ps, Period{Year: 2025, No: 13, Quarter: 4, Adjustment: true})
	for no := 1; no <= 12; no++ {
		ps = append(ps, Period{Year: 2026, No: no, Quarter: (no-1)/3 + 1})
	}
	cal, err := NewCalendar(ps)
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func labelsAt(t *testing.T, cal Calendar, idx []int) []string {
	t.Helper()
	out := []string{}
	for _, i := range idx {
		out = append(out, cal.Periods()[i].Label())
	}
	return out
}

func TestSelect_PeriodExpressions(t *testing.T) {
	cal := testCalendar(t)
	tests := []struct {
		name    string
		expr    string
		at      string
		adj     bool
		want    []string
		found   bool
		wantErr string
	}{
		{"current period", "P", "2026-03", false, []string{"2026-03"}, true, ""},
		{"one period back", "P-1", "2026-03", false, []string{"2026-02"}, true, ""},
		{"back across the year boundary", "P-2", "2026-01", false, []string{"2025-11"}, true, ""},
		{"twelve back lands on the first period", "P-12", "2026-01", false, []string{"2025-01"}, true, ""},
		{"thirteen back has no target", "P-13", "2026-01", false, nil, false, ""},
		{"back from the first period is missing", "P-1", "2025-01", false, nil, false, ""},
		{"adjustment skipped when not included", "P-1", "2026-01", false, []string{"2025-12"}, true, ""},
		{"adjustment counted when included", "P-1", "2026-01", true, []string{"2025-13"}, true, ""},
		{"zero back is the period itself", "P-0", "2026-07", false, []string{"2026-07"}, true, ""},
		{"year to date", "YTD", "2026-03", false, []string{"2026-01", "2026-02", "2026-03"}, true, ""},
		{"year to date excludes later periods", "YTD", "2026-01", false, []string{"2026-01"}, true, ""},
		{"year to date skips adjustment", "YTD", "2025-13", false, []string{"2025-01", "2025-02", "2025-03", "2025-04", "2025-05", "2025-06", "2025-07", "2025-08", "2025-09", "2025-10", "2025-11", "2025-12"}, true, ""},
		{"year to date includes adjustment when flagged", "YTD", "2025-13", true, []string{"2025-01", "2025-02", "2025-03", "2025-04", "2025-05", "2025-06", "2025-07", "2025-08", "2025-09", "2025-10", "2025-11", "2025-12", "2025-13"}, true, ""},
		{"full fiscal year", "FY", "2026-03", false, []string{"2026-01", "2026-02", "2026-03", "2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09", "2026-10", "2026-11", "2026-12"}, true, ""},
		{"full fiscal year without adjustment", "FY", "2025-06", false, []string{"2025-01", "2025-02", "2025-03", "2025-04", "2025-05", "2025-06", "2025-07", "2025-08", "2025-09", "2025-10", "2025-11", "2025-12"}, true, ""},
		{"quarter containing the period", "Q", "2026-05", false, []string{"2026-04", "2026-05", "2026-06"}, true, ""},
		{"quarter of an adjustment run excludes adjustment", "Q", "2025-13", false, []string{"2025-10", "2025-11", "2025-12"}, true, ""},
		{"prior year same period", "PY", "2026-03", false, []string{"2025-03"}, true, ""},
		{"prior year missing after calendar change", "PY", "2025-03", false, nil, false, ""},
		{"prior year to date", "PYTD", "2026-03", false, []string{"2025-01", "2025-02", "2025-03"}, true, ""},
		{"prior year to date stops at the prior period", "PYTD", "2026-12", true, []string{"2025-01", "2025-02", "2025-03", "2025-04", "2025-05", "2025-06", "2025-07", "2025-08", "2025-09", "2025-10", "2025-11", "2025-12"}, true, ""},
		{"prior year to date with missing prior year", "PYTD", "2025-02", false, nil, false, ""},
		{"calc selects nothing", "CALC", "2026-03", false, nil, true, ""},
		{"unknown expression", "YTD+1", "2026-03", false, nil, false, CodeExpression},
		{"malformed offset", "P-x", "2026-03", false, nil, false, CodeExpression},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			at, ok := cal.Index(atoiYear(tc.at), atoiNo(tc.at))
			if !ok {
				t.Fatalf("test period %s not in calendar", tc.at)
			}
			sel, found, err := Select(tc.expr, at, cal, tc.adj)
			if tc.wantErr != "" {
				var de *Error
				if !errors.As(err, &de) || de.Code != tc.wantErr {
					t.Fatalf("err = %v, want code %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if found != tc.found {
				t.Fatalf("found = %v, want %v", found, tc.found)
			}
			if got := labelsAt(t, cal, sel); !reflect.DeepEqual(got, append([]string{}, tc.want...)) && !(len(got) == 0 && len(tc.want) == 0) {
				t.Fatalf("selection = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSelect_RunPeriodOutOfRange(t *testing.T) {
	cal := testCalendar(t)
	if _, _, err := Select("P", cal.Len(), cal, false); err == nil {
		t.Fatal("index past the calendar must fail")
	}
}

func TestNewCalendar_RejectsDuplicatePeriods(t *testing.T) {
	_, err := NewCalendar([]Period{{Year: 2026, No: 1}, {Year: 2026, No: 1}})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodePeriodNotFound {
		t.Fatalf("duplicate periods: err = %v", err)
	}
}

func atoiYear(label string) int {
	y := 0
	for _, c := range label[:4] {
		y = y*10 + int(c-'0')
	}
	return y
}

func atoiNo(label string) int {
	n := 0
	for _, c := range label[5:] {
		n = n*10 + int(c-'0')
	}
	return n
}
