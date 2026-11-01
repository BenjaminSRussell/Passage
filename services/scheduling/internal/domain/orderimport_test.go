package domain

import (
	"testing"
	"time"
)

func TestPlanImport_BuildsOperationsFromProcessAndSetupJobs(t *testing.T) {
	delivery := time.Date(2031, 3, 14, 0, 0, 0, 0, time.UTC)
	start := time.Date(2031, 3, 3, 6, 0, 30, 0, time.UTC)
	ops, skipped := PlanImport(ProductionOrder{OrderID: "o", Start: start, DeliveryDate: &delivery, Jobs: []ProductionJob{
		{OperNumber: 20, Seq: 1, JobType: "process", ResourceID: "r2", Hours: "0.5"},
		{OperNumber: 10, Seq: 2, JobType: "process", ResourceID: "r1", Hours: "1.51"},
		{OperNumber: 10, Seq: 1, JobType: "setup", ResourceID: "r1", Hours: "0.25"},
		{OperNumber: 10, Seq: 3, JobType: "queue-after", ResourceID: "r1", Hours: "8"},
		{OperNumber: 10, Seq: 4, JobType: "process", ResourceID: "r3", Hours: "0.01"},
	}})
	if len(skipped) != 0 || len(ops) != 2 {
		t.Fatalf("ops %+v skipped %+v", ops, skipped)
	}
	first := ops[0]
	if first.OperNumber != 10 || first.Minutes != 92 || first.SetupMinutes != 15 || len(first.ResourceIDs) != 2 || first.ResourceIDs[0] != "r1" || first.ResourceIDs[1] != "r3" {
		t.Fatalf("first = %+v, want 1.52 h = 92 min rounded up, 15 min setup, resources r1 then r3", first)
	}
	if first.ReadyAt == nil || !first.ReadyAt.Equal(time.Date(2031, 3, 3, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("first ready = %v, want the order start to the minute", first.ReadyAt)
	}
	if ops[1].ReadyAt != nil || ops[1].Minutes != 30 {
		t.Fatalf("second = %+v, want no ready time and 30 minutes", ops[1])
	}
	if first.DueAt != nil || ops[1].DueAt != nil {
		t.Fatalf("due = %v, want it left to the caller, who knows the resource's time zone", first.DueAt)
	}
}

func TestDeliveryDue_IsTheNextLocalMidnight(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		date time.Time
		loc  *time.Location
		want time.Time
	}{
		{"utc", time.Date(2031, 3, 14, 0, 0, 0, 0, time.UTC), time.UTC, time.Date(2031, 3, 15, 0, 0, 0, 0, time.UTC)},
		{"nil zone is utc", time.Date(2031, 3, 14, 0, 0, 0, 0, time.UTC), nil, time.Date(2031, 3, 15, 0, 0, 0, 0, time.UTC)},
		{"berlin winter", time.Date(2031, 1, 10, 0, 0, 0, 0, time.UTC), berlin, time.Date(2031, 1, 10, 23, 0, 0, 0, time.UTC)},
		{"berlin summer", time.Date(2031, 7, 10, 0, 0, 0, 0, time.UTC), berlin, time.Date(2031, 7, 10, 22, 0, 0, 0, time.UTC)},
		{"the spring-forward day ends at the CEST midnight", time.Date(2031, 3, 30, 0, 0, 0, 0, time.UTC), berlin, time.Date(2031, 3, 30, 22, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := DeliveryDue(c.date, c.loc); !got.Equal(c.want) {
			t.Errorf("%s: DeliveryDue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPlanImport_SkipsWhatCannotBeScheduled(t *testing.T) {
	ops, skipped := PlanImport(ProductionOrder{OrderID: "o", Jobs: []ProductionJob{
		{OperNumber: 10, Seq: 1, JobType: "setup", ResourceID: "r1", Hours: "1"},
		{OperNumber: 20, Seq: 1, JobType: "process", ResourceID: "r1", Hours: "x"},
		{OperNumber: 30, Seq: 1, JobType: "process", ResourceID: "r1", Hours: "2000"},
		{OperNumber: 40, Seq: 1, JobType: "process", ResourceID: "r1", Hours: "1"},
		{OperNumber: 40, Seq: 2, JobType: "setup", ResourceID: "r1", Hours: "200"},
		{OperNumber: 50, Seq: 1, JobType: "process", ResourceID: "r1", Hours: "0"},
	}})
	got := map[int]string{}
	for _, s := range skipped {
		got[s.OperNumber] = s.Code
	}
	want := map[int]string{10: SkipNoProcessJob, 20: SkipBadHours, 30: SkipTooLong, 40: SkipSetupTooLong}
	if len(got) != len(want) {
		t.Fatalf("skipped %v, want %v", got, want)
	}
	for n, c := range want {
		if got[n] != c {
			t.Errorf("operation %d skipped as %q, want %q", n, got[n], c)
		}
	}
	if len(ops) != 1 || ops[0].OperNumber != 50 || ops[0].Minutes != 1 || ops[0].ReadyAt != nil {
		t.Fatalf("ops = %+v, want operation 50 with a one-minute floor", ops)
	}
}

func TestOperationNumbers_AreDistinctAndSorted(t *testing.T) {
	got := ProductionOrder{Jobs: []ProductionJob{{OperNumber: 20}, {OperNumber: 10}, {OperNumber: 20}}}.OperationNumbers()
	if len(got) != 2 || got[0] != 10 || got[1] != 20 {
		t.Fatalf("got %v", got)
	}
}
