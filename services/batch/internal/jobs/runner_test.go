package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestRunner_OneMillionRowsResumeAfterKillWithoutDuplicates(t *testing.T) {
	const rows = 1_000_000
	const tenantID = "00000000-0000-4000-8000-000000000001"
	store := NewMemoryStore()
	runner := Runner{Store: store, Partitions: 16, MaxAttempts: 1}
	seen := make([]uint32, rows)
	killed := false
	podKilled := errors.New("pod killed")
	process := func(_ context.Context, _ pgx.Tx, row int64) error {
		if row == 400_000 && !killed {
			killed = true
			return podKilled
		}
		if atomic.AddUint32(&seen[row], 1) != 1 {
			t.Fatalf("row %d processed more than once", row)
		}
		return nil
	}

	if _, err := runner.Run(context.Background(), Job{ID: "million", TenantID: tenantID, Rows: rows}, process); !errors.Is(err, podKilled) {
		t.Fatalf("first run error = %v, want pod kill", err)
	}
	result, err := runner.Run(context.Background(), Job{ID: "million", TenantID: tenantID, Rows: rows}, process)
	if err != nil {
		t.Fatal(err)
	}
	if result.Processed != rows-400_000 {
		t.Fatalf("resume processed %d rows, want %d", result.Processed, rows-400_000)
	}
	for row, count := range seen {
		if count != 1 {
			t.Fatalf("row %d processed %d times", row, count)
		}
	}
	if completed := store.Completed(tenantID, "million"); completed != rows {
		t.Fatalf("checkpointed rows = %d, want %d", completed, rows)
	}
}

func TestRunner_RetriesTransientFailure(t *testing.T) {
	const tenantID = "00000000-0000-4000-8000-000000000001"
	store := NewMemoryStore()
	runner := Runner{Store: store, Partitions: 2, MaxAttempts: 3}
	attempts := 0
	result, err := runner.Run(context.Background(), Job{ID: "retry", TenantID: tenantID, Rows: 1}, func(context.Context, pgx.Tx, int64) error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || result.Retries != 2 || result.Processed != 1 {
		t.Fatalf("attempts=%d result=%+v", attempts, result)
	}
}

func TestRunner_ValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		runner Runner
		job    Job
	}{
		{name: "store required", runner: Runner{Partitions: 1, MaxAttempts: 1}, job: Job{ID: "j", TenantID: "00000000-0000-4000-8000-000000000001", Rows: 1}},
		{name: "partitions positive", runner: Runner{Store: NewMemoryStore(), MaxAttempts: 1}, job: Job{ID: "j", TenantID: "00000000-0000-4000-8000-000000000001", Rows: 1}},
		{name: "attempts positive", runner: Runner{Store: NewMemoryStore(), Partitions: 1}, job: Job{ID: "j", TenantID: "00000000-0000-4000-8000-000000000001", Rows: 1}},
		{name: "job id required", runner: Runner{Store: NewMemoryStore(), Partitions: 1, MaxAttempts: 1}, job: Job{TenantID: "00000000-0000-4000-8000-000000000001", Rows: 1}},
		{name: "tenant id required", runner: Runner{Store: NewMemoryStore(), Partitions: 1, MaxAttempts: 1}, job: Job{ID: "j", Rows: 1}},
		{name: "tenant id must be uuid", runner: Runner{Store: NewMemoryStore(), Partitions: 1, MaxAttempts: 1}, job: Job{ID: "j", TenantID: "not-a-uuid", Rows: 1}},
		{name: "rows nonnegative", runner: Runner{Store: NewMemoryStore(), Partitions: 1, MaxAttempts: 1}, job: Job{ID: "j", TenantID: "00000000-0000-4000-8000-000000000001", Rows: -1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.runner.Run(context.Background(), test.job, func(context.Context, pgx.Tx, int64) error { return nil })
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}
