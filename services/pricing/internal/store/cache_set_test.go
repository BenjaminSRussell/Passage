package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

// gatedStore holds every load until release is closed and counts loads atomically.
type gatedStore struct {
	Store
	loads   atomic.Int32
	release chan struct{}
}

func (s *gatedStore) ActiveRecords(context.Context, ActiveQuery) ([]domain.Record, error) {
	s.loads.Add(1)
	<-s.release
	return []domain.Record{{ID: "r1", Status: domain.StatusActive}}, nil
}

// FR-26.14: concurrent misses of one snapshot wait for a single load instead of each reading
// the whole price list, and all of them get the same shared RecordSet.
func TestCache_ConcurrentMissesShareOneLoad(t *testing.T) {
	inner := &gatedStore{release: make(chan struct{})}
	c := NewCache(inner, time.Minute)
	ctx := cacheCtx("t1", "")
	const callers = 20
	sets := make([]*domain.RecordSet, callers)
	var wg sync.WaitGroup
	for i := range sets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			set, err := c.ActiveRecordSet(ctx, query("le1", ""))
			if err != nil {
				t.Error(err)
			}
			sets[i] = set
		}()
	}
	for inner.loads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the other callers reach the in-flight load
	close(inner.release)
	wg.Wait()
	if n := inner.loads.Load(); n != 1 {
		t.Fatalf("%d concurrent misses made %d loads; want 1", callers, n)
	}
	for i, s := range sets {
		if s == nil || s != sets[0] {
			t.Fatalf("caller %d got set %p; want the shared %p", i, s, sets[0])
		}
	}
}

// A hit returns the shared set itself; ActiveRecords still hands out a private copy; a flush
// makes the next call load a new set.
func TestCache_RecordSetIsSharedUntilFlush(t *testing.T) {
	inner := &countingStore{records: []domain.Record{{ID: "r1"}}}
	c := NewCache(inner, time.Minute)
	ctx := cacheCtx("t1", "")
	first, _ := c.ActiveRecordSet(ctx, query("le1", ""))
	again, _ := c.ActiveRecordSet(ctx, query("le1", ""))
	if first != again || inner.loads != 1 {
		t.Fatalf("second call = %p after %d loads; want the same set %p from one load", again, inner.loads, first)
	}
	recs, _ := c.ActiveRecords(ctx, query("le1", ""))
	recs[0].ID = "changed by a caller"
	if got := first.Records(); got[0].ID != "r1" || inner.loads != 1 {
		t.Fatalf("set records = %v after %d loads; a caller's copy must not reach the set", got, inner.loads)
	}
	c.Flush()
	if after, _ := c.ActiveRecordSet(ctx, query("le1", "")); after == first || inner.loads != 2 {
		t.Fatalf("after a flush got %p after %d loads; want a new set from a reload", after, inner.loads)
	}
}
