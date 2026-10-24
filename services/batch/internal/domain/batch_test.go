package domain

import "testing"

func TestCanStartRun(t *testing.T) {
	if err := CanStartRun(Job{Status: JobHold}, false); err == nil || err.(*Error).Code != CodeJobHold {
		t.Fatalf("hold: %v", err)
	}
	if err := CanStartRun(Job{Exclusive: true, Status: JobActive}, true); err == nil || err.(*Error).Code != CodeRunOverlap {
		t.Fatalf("overlap: %v", err)
	}
	if err := CanStartRun(Job{Exclusive: true, Status: JobActive}, false); err != nil {
		t.Fatal(err)
	}
}

func TestValidateJob(t *testing.T) {
	if fields := ValidateJob(Job{}); len(fields) == 0 {
		t.Fatal("expected errors")
	}
	if fields := ValidateJob(Job{Code: "sweep", Handler: "outbox.sweep", PartitionCount: 1, Concurrency: 1, PartitionStrategy: PartitionHash, Priority: PriorityB}); len(fields) != 0 {
		t.Fatalf("%v", fields)
	}
}

func TestRangeBounds(t *testing.T) {
	start, end := RangeBounds(1_000_000, 16, 0)
	if start != 0 || end != 62_500 {
		t.Fatalf("%d %d", start, end)
	}
	start, end = RangeBounds(1_000_000, 16, 15)
	if start != 937_500 || end != 1_000_000 {
		t.Fatalf("%d %d", start, end)
	}
}
