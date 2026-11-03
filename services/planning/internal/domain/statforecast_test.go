package domain

import (
	"math"
	"testing"
	"time"
)

func TestMonthPeriodsCrossYearAndLeapFebruary(t *testing.T) {
	got := MonthPeriods(time.Date(2027, 12, 1, 0, 0, 0, 0, time.UTC), 4)
	want := [][2]string{{"2027-12-01", "2027-12-31"}, {"2028-01-01", "2028-01-31"}, {"2028-02-01", "2028-02-29"}, {"2028-03-01", "2028-03-31"}}
	for i, p := range got {
		if p.Start.Format("2006-01-02") != want[i][0] || p.End.Format("2006-01-02") != want[i][1] {
			t.Fatalf("period %d = %v..%v, want %v", i, p.Start, p.End, want[i])
		}
	}
}

func TestQtyFromFloat(t *testing.T) {
	cases := []struct {
		in   float64
		want string
		ok   bool
	}{
		{12.3456789, "12.345679", true}, {0.1 + 0.2, "0.3", true}, {2.0000005, "2.000001", true}, {1e-7, "0", true},
		{-3, "0", true}, {0, "0", true}, {math.NaN(), "0", true}, {math.Inf(-1), "0", true}, {100, "100", true},
		{999999999999.9999, "999999999999.9999", true}, {1e12, "", false}, {math.Inf(1), "", false},
	}
	for _, c := range cases {
		got, ok := QtyFromFloat(c.in)
		if ok != c.ok || (ok && got.String() != c.want) {
			t.Errorf("QtyFromFloat(%v) = %s, %v; want %s, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
