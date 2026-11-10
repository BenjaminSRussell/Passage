package jobs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/print/internal/jobs"
)

type fakeSweeper struct {
	calls []string
	fail  string
}

func (f *fakeSweeper) SweepClaimed(ctx context.Context) (int, error) {
	p, _ := tenant.FromContext(ctx)
	f.calls = append(f.calls, p.TenantID)
	if p.TenantID == f.fail {
		return 0, errors.New("db down")
	}
	return 2, nil
}

func TestSweepTenants_ContinuesPastAFailingTenant(t *testing.T) {
	f := &fakeSweeper{fail: "t2"}
	n, err := jobs.SweepTenants(context.Background(), f, []string{"t1", "t2", "t3"})
	if n != 4 || err == nil || len(f.calls) != 3 {
		t.Fatalf("claimed %d, err %v, calls %v; want 4, an error and all three tenants", n, err, f.calls)
	}
}

func TestRunSweep_RunsOnTickAndStopsOnCancel(t *testing.T) {
	f := &fakeSweeper{}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan int, 4)
	done := make(chan struct{})
	go func() {
		jobs.RunSweep(ctx, f, []string{"t1"}, 5*time.Millisecond, func(claimed int, _ error) { got <- claimed })
		close(done)
	}()
	if n := <-got; n != 2 {
		t.Fatalf("claimed %d, want 2", n)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunSweep did not stop on cancel")
	}
}
