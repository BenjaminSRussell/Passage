// Package jobs provides partitioned, checkpointed batch execution.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

var ErrInvalidArgument = errors.New("invalid argument")

type Job struct {
	ID       string
	TenantID string
	Rows     int64
}

type Result struct {
	Processed int64
	Retries   int64
}

// Store atomically applies a row effect, records its idempotency key, and
// advances the partition checkpoint. Implementations must roll all three
// operations back together when effect returns an error.
type Store interface {
	Checkpoint(ctx context.Context, tenantID, jobID string, partition int) (int64, error)
	Execute(
		ctx context.Context,
		tenantID string,
		jobID string,
		partition int,
		row int64,
		effect func(pgx.Tx) error,
	) (bool, error)
}

type Runner struct {
	Store       Store
	Partitions  int
	MaxAttempts int
}

func (r Runner) Run(
	ctx context.Context,
	job Job,
	process func(context.Context, pgx.Tx, int64) error,
) (Result, error) {
	if r.Store == nil || r.Partitions <= 0 || r.MaxAttempts <= 0 ||
		job.ID == "" || !ids.Valid(job.TenantID) || job.Rows < 0 || process == nil {
		return Result{}, ErrInvalidArgument
	}

	var result Result
	for partition := range r.Partitions {
		start := job.Rows * int64(partition) / int64(r.Partitions)
		end := job.Rows * int64(partition+1) / int64(r.Partitions)
		next, err := r.Store.Checkpoint(ctx, job.TenantID, job.ID, partition)
		if err != nil {
			return result, fmt.Errorf("load partition %d checkpoint: %w", partition, err)
		}
		if next < start {
			next = start
		}
		for row := next; row < end; row++ {
			var applied bool
			for attempt := 1; attempt <= r.MaxAttempts; attempt++ {
				applied, err = r.Store.Execute(ctx, job.TenantID, job.ID, partition, row, func(tx pgx.Tx) error {
					return process(ctx, tx, row)
				})
				if err == nil {
					break
				}
				if attempt == r.MaxAttempts {
					return result, fmt.Errorf("partition %d row %d: %w", partition, row, err)
				}
				result.Retries++
			}
			if applied {
				result.Processed++
			}
		}
	}
	return result, nil
}

type memoryCheckpoint struct {
	next      int64
	processed map[int64]struct{}
}

// MemoryStore is a deterministic Store for tests and local dry runs.
type MemoryStore struct {
	mu         sync.Mutex
	partitions map[string]map[int]*memoryCheckpoint
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{partitions: make(map[string]map[int]*memoryCheckpoint)}
}

func (s *MemoryStore) Checkpoint(_ context.Context, tenantID, jobID string, partition int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.partition(tenantID, jobID, partition).next, nil
}

func (s *MemoryStore) Execute(
	_ context.Context,
	tenantID string,
	jobID string,
	partition int,
	row int64,
	effect func(pgx.Tx) error,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	checkpoint := s.partition(tenantID, jobID, partition)
	if _, exists := checkpoint.processed[row]; exists {
		return false, nil
	}
	if err := effect(nil); err != nil {
		return false, err
	}
	checkpoint.processed[row] = struct{}{}
	if row >= checkpoint.next {
		checkpoint.next = row + 1
	}
	return true, nil
}

func (s *MemoryStore) Completed(tenantID, jobID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var completed int64
	for _, checkpoint := range s.partitions[storeKey(tenantID, jobID)] {
		completed += int64(len(checkpoint.processed))
	}
	return completed
}

func (s *MemoryStore) partition(tenantID, jobID string, partition int) *memoryCheckpoint {
	key := storeKey(tenantID, jobID)
	byPartition := s.partitions[key]
	if byPartition == nil {
		byPartition = make(map[int]*memoryCheckpoint)
		s.partitions[key] = byPartition
	}
	checkpoint := byPartition[partition]
	if checkpoint == nil {
		checkpoint = &memoryCheckpoint{processed: make(map[int64]struct{})}
		byPartition[partition] = checkpoint
	}
	return checkpoint
}

func storeKey(tenantID, jobID string) string {
	return tenantID + "/" + jobID
}
