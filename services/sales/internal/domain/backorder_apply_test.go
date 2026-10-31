package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dq(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

func entry(date string, q int64, status string) ScheduleEntry {
	return ScheduleEntry{Date: date, Quantity: dq(q), Status: status, Source: SchedSourceLine}
}

func total(es []ScheduleEntry) decimal.Decimal {
	s := decimal.Zero
	for _, e := range es {
		s = s.Add(e.Quantity)
	}
	return s
}

func TestRescheduleLine(t *testing.T) {
	cur := []ScheduleEntry{entry("2027-03-01", 40, SchedConfirmed), entry("2027-03-10", 5, SchedBackorder), {Quantity: dq(5), Status: SchedUnpromised, Source: SchedSourceATP}}

	t.Run("unchanged keeps the entries", func(t *testing.T) {
		got, err := RescheduleLine(cur, "2027-03-01", dq(45), "2027-03-20", "2027-03-20")
		if err != nil || len(got) != 3 || !got[0].Quantity.Equal(dq(40)) || got[2].Status != SchedUnpromised {
			t.Fatalf("%v %+v", err, got)
		}
	})
	t.Run("a gain promises the open quantity on the gain date", func(t *testing.T) {
		got, err := RescheduleLine(cur, "2027-03-01", dq(48), "2027-03-20", "2027-03-18")
		if err != nil || len(got) != 4 || got[2].Date != "2027-03-20" || !got[2].Quantity.Equal(dq(3)) ||
			got[2].Status != SchedBackorder || got[2].BucketDate != "2027-03-18" || !got[3].Quantity.Equal(dq(2)) || !total(got).Equal(dq(50)) {
			t.Fatalf("%v %+v", err, got)
		}
	})
	t.Run("a loss comes off the latest dates and becomes unpromised", func(t *testing.T) {
		got, err := RescheduleLine(cur, "2027-03-01", dq(38), "", "")
		if err != nil || len(got) != 2 || !got[0].Quantity.Equal(dq(38)) || got[1].Status != SchedUnpromised || !got[1].Quantity.Equal(dq(12)) || !total(got).Equal(dq(50)) {
			t.Fatalf("%v %+v", err, got)
		}
	})
	t.Run("a full loss leaves one undated line", func(t *testing.T) {
		got, err := RescheduleLine(cur, "2027-03-01", dq(0), "", "")
		if err != nil || len(got) != 1 || got[0].Date != "" || !got[0].Quantity.Equal(dq(50)) {
			t.Fatalf("%v %+v", err, got)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		if _, err := RescheduleLine(cur, "2027-03-01", dq(51), "2027-03-20", ""); err == nil {
			t.Fatal("more than the line holds")
		}
		if _, err := RescheduleLine(cur, "2027-03-01", dq(-1), "", ""); err == nil {
			t.Fatal("negative")
		}
		if _, err := RescheduleLine(cur, "2027-03-01", dq(46), "", ""); err == nil {
			t.Fatal("gain without a date")
		}
	})
}
