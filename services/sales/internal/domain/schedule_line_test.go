package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestScheduleStatus(t *testing.T) {
	for _, tc := range []struct{ date, req, want string }{
		{"2027-03-01", "2027-03-01", SchedConfirmed},
		{"2027-02-27", "2027-03-01", SchedConfirmed},
		{"2027-03-04", "2027-03-01", SchedBackorder},
		{"", "2027-03-01", SchedUnpromised},
	} {
		if got := ScheduleStatus(tc.date, tc.req); got != tc.want {
			t.Errorf("ScheduleStatus(%q,%q) = %s, want %s", tc.date, tc.req, got, tc.want)
		}
	}
}

// AC-27.8: ATP 40 on 03-01 and 5 more on 03-04 for an order of 45 gives two
// schedule lines and the 5 units show as backorder on 03-04.
func TestPlanSchedule_PartialDelivery(t *testing.T) {
	day := func(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }
	periods := []Period{
		{Date: day("2027-03-01"), Supply: decimal.NewFromInt(40)},
		{Date: day("2027-03-04"), Supply: decimal.NewFromInt(5)},
	}
	p, err := PromiseDate(periods, day("2027-03-01"), decimal.NewFromInt(45), false)
	if err != nil {
		t.Fatal(err)
	}
	got := PlanSchedule(p, periods, day("2027-03-01"))
	if len(got) != 2 || got[0].Status != SchedConfirmed || !got[0].Quantity.Equal(decimal.NewFromInt(40)) || got[0].BucketDate != "2027-03-01" ||
		got[1].Status != SchedBackorder || got[1].Date != "2027-03-04" || !got[1].Quantity.Equal(decimal.NewFromInt(5)) ||
		got[1].Source != SchedSourceATP || got[1].BucketDate != "2027-03-04" {
		t.Fatalf("schedule %+v", got)
	}
	// Supply for 30 only: the other 15 are unpromised, undated and name no bucket.
	short := []Period{{Date: day("2027-03-01"), Supply: decimal.NewFromInt(30)}}
	p, err = PromiseDate(short, day("2027-03-01"), decimal.NewFromInt(45), false)
	if err != nil {
		t.Fatal(err)
	}
	got = PlanSchedule(p, short, day("2027-03-01"))
	if len(got) != 2 || !got[0].Quantity.Equal(decimal.NewFromInt(30)) || got[1].Status != SchedUnpromised || got[1].Date != "" ||
		got[1].BucketDate != "" || !got[1].Quantity.Equal(decimal.NewFromInt(15)) {
		t.Fatalf("schedule %+v", got)
	}
	// A request after the bucket date ships on the requested date but still
	// names the bucket it drew on.
	p, _ = PromiseDate(short, day("2027-03-03"), decimal.NewFromInt(10), true)
	got = PlanSchedule(p, short, day("2027-03-03"))
	if len(got) != 1 || got[0].Date != "2027-03-03" || got[0].BucketDate != "2027-03-01" || got[0].Status != SchedConfirmed {
		t.Fatalf("schedule %+v", got)
	}
}
