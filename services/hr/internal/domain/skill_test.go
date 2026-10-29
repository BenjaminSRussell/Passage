package domain

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestGap_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		required []Requirement
		actual   map[string]int
		want     []GapRow
	}{
		{
			name:     "AC-42.16 only the shortfall is listed",
			required: []Requirement{{"WELDING", 3}, {"PLC", 2}},
			actual:   map[string]int{"WELDING": 4, "PLC": 1},
			want:     []GapRow{{Skill: "PLC", Required: 2, Actual: 1, Delta: -1}},
		},
		{
			name:     "missing skill counts as zero",
			required: []Requirement{{"FORKLIFT", 3}},
			actual:   map[string]int{},
			want:     []GapRow{{Skill: "FORKLIFT", Required: 3, Actual: 0, Delta: -3}},
		},
		{
			name:     "exact match is not a gap",
			required: []Requirement{{"PLC", 2}},
			actual:   map[string]int{"PLC": 2},
			want:     []GapRow{},
		},
		{
			name:     "duplicate requirements keep the highest minimum",
			required: []Requirement{{"PLC", 1}, {"PLC", 3}},
			actual:   map[string]int{"PLC": 2},
			want:     []GapRow{{Skill: "PLC", Required: 3, Actual: 2, Delta: -1}},
		},
		{
			name:     "rows are sorted by skill",
			required: []Requirement{{"ZINC", 2}, {"ALU", 2}},
			actual:   map[string]int{},
			want: []GapRow{
				{Skill: "ALU", Required: 2, Actual: 0, Delta: -2},
				{Skill: "ZINC", Required: 2, Actual: 0, Delta: -2},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Gap(tc.required, tc.actual); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Gap = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestGap_Property(t *testing.T) {
	skills := []string{"A", "B", "C", "D"}
	rapid.Check(t, func(t *rapid.T) {
		reqs := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) Requirement {
			return Requirement{
				Skill:     rapid.SampledFrom(skills).Draw(t, "skill"),
				MinRating: rapid.IntRange(1, 5).Draw(t, "min"),
			}
		}), 0, 8).Draw(t, "reqs")
		actual := map[string]int{}
		for _, s := range skills {
			if rapid.Bool().Draw(t, "has-"+s) {
				actual[s] = rapid.IntRange(0, 5).Draw(t, "rating-"+s)
			}
		}
		rows := Gap(reqs, actual)
		seen := map[string]bool{}
		for i, row := range rows {
			if row.Delta != row.Actual-row.Required || row.Delta >= 0 {
				t.Fatalf("row %+v: delta must be actual-required and negative", row)
			}
			if seen[row.Skill] {
				t.Fatalf("duplicate skill %s in gap", row.Skill)
			}
			seen[row.Skill] = true
			if i > 0 && rows[i-1].Skill >= row.Skill {
				t.Fatalf("gap rows not sorted: %+v", rows)
			}
		}
		// Every normalized requirement is either met or listed.
		for _, r := range Normalize(reqs) {
			met := actual[r.Skill] >= r.MinRating
			if met == seen[r.Skill] {
				t.Fatalf("requirement %+v met=%v but listed=%v", r, met, seen[r.Skill])
			}
		}
	})
}

func TestValidateRating_Scale(t *testing.T) {
	tests := []struct {
		rating, scale int
		wantErr       bool
	}{
		{1, 5, false},
		{5, 5, false},
		{0, 5, true},
		{6, 5, true},
		{3, 0, true},
		{3, MaxSkillScale + 1, true},
	}
	for _, tc := range tests {
		err := ValidateRating(tc.rating, tc.scale)
		if (err != nil) != tc.wantErr {
			t.Fatalf("ValidateRating(%d, %d) err = %v, wantErr %v", tc.rating, tc.scale, err, tc.wantErr)
		}
		if err != nil {
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeSkillRating {
				t.Fatalf("want %s, got %v", CodeSkillRating, err)
			}
		}
	}
}

func TestCompareListsMetAndUnmetRequirements(t *testing.T) {
	got := Compare([]Requirement{{"WELDING", 3}, {"PLC", 2}}, map[string]int{"WELDING": 4, "PLC": 1})
	want := []GapRow{{Skill: "PLC", Required: 2, Actual: 1, Delta: -1}, {Skill: "WELDING", Required: 3, Actual: 4, Delta: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Compare = %+v, want %+v", got, want)
	}
}

func TestCertificationExpiry(t *testing.T) {
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }
	tests := []struct {
		name      string
		completed time.Time
		months    int
		want      *time.Time
	}{
		{"AC-42.9 24 months", d(2026, 5, 10), 24, ptr(d(2028, 5, 10))},
		{"31 January plus one month is the end of February", d(2026, 1, 31), 1, ptr(d(2026, 2, 28))},
		{"leap day plus 12 months is 28 February", d(2028, 2, 29), 12, ptr(d(2029, 2, 28))},
		{"31 August plus 6 months crosses the year", d(2026, 8, 31), 6, ptr(d(2027, 2, 28))},
		{"a time of day is dropped", time.Date(2026, 3, 15, 22, 30, 0, 0, time.UTC), 3, ptr(d(2026, 6, 15))},
		{"zero validity does not expire", d(2026, 5, 10), 0, nil},
		{"negative validity does not expire", d(2026, 5, 10), -1, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CertificationExpiry(tc.completed, tc.months)
			if (got == nil) != (tc.want == nil) || (got != nil && !got.Equal(*tc.want)) {
				t.Fatalf("CertificationExpiry = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCertificationExpiryProperty: the expiry is always validityMonths
// calendar months later, on the same day or the target month's last day.
func TestCertificationExpiryProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		completed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, rapid.IntRange(0, 4000).Draw(t, "day"))
		months := rapid.IntRange(1, 240).Draw(t, "months")
		exp := CertificationExpiry(completed, months)
		if exp == nil {
			t.Fatal("positive validity must expire")
		}
		gotMonths := (exp.Year()-completed.Year())*12 + int(exp.Month()) - int(completed.Month())
		if gotMonths != months {
			t.Fatalf("%s + %d months = %s (%d months)", completed.Format("2006-01-02"), months, exp.Format("2006-01-02"), gotMonths)
		}
		lastDay := time.Date(exp.Year(), exp.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		if exp.Day() != completed.Day() && exp.Day() != lastDay {
			t.Fatalf("%s + %d months = %s: neither the same day nor the month end", completed.Format("2006-01-02"), months, exp.Format("2006-01-02"))
		}
	})
}

func TestCertificationValidWindow(t *testing.T) {
	issued := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	exp := time.Date(2028, 5, 10, 0, 0, 0, 0, time.UTC)
	if !CertificationValid(issued, &exp, time.Date(2028, 5, 9, 23, 0, 0, 0, time.UTC)) {
		t.Fatal("valid the day before expiry")
	}
	if CertificationValid(issued, &exp, exp) {
		t.Fatal("the expiry day itself is no longer valid")
	}
	if !CertificationValid(issued, &exp, issued) {
		t.Fatal("valid on the issue day")
	}
	if CertificationValid(issued, &exp, issued.AddDate(0, 0, -1)) {
		t.Fatal("not valid before it was issued")
	}
	if !CertificationValid(issued, nil, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("no expiry is valid for ever after issue")
	}
}
