package jobs_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/jobs"
)

type fakePurger struct {
	calls   []string
	cutoffs []time.Time
	fail    string
	deleted int64
}

func (f *fakePurger) PurgeJobs(ctx context.Context, cutoff time.Time) (int64, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return 0, errors.New("no tenant in context")
	}
	f.calls = append(f.calls, p.TenantID)
	f.cutoffs = append(f.cutoffs, cutoff)
	if p.TenantID == f.fail {
		return 0, errors.New("db down")
	}
	return f.deleted, nil
}

func TestNextRun_TableDriven(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 26, h, m, 0, 0, time.UTC) }
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"before the hour runs today", at(0, 30), at(1, 0)},
		{"exactly at the hour waits a day", at(1, 0), time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)},
		{"after the hour runs tomorrow", at(23, 59), time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)},
		{"non-UTC input is normalized", time.Date(2026, 10, 26, 3, 30, 0, 0, time.FixedZone("CET", 3600)), time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := jobs.NextRun(c.now, jobs.PurgeHourUTC); !got.Equal(c.want) {
				t.Fatalf("NextRun = %s, want %s", got, c.want)
			}
		})
	}
}

func TestPurgeTenants_RunsForEachTenantWithCutoff(t *testing.T) {
	f := &fakePurger{deleted: 3}
	now := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	total, err := jobs.PurgeTenants(context.Background(), f, []string{"t-1", "t-2"}, now, 30)
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 || len(f.calls) != 2 || f.calls[0] != "t-1" || f.calls[1] != "t-2" {
		t.Fatalf("total %d calls %v", total, f.calls)
	}
	want := domain.RetentionCutoff(now, 30)
	for _, c := range f.cutoffs {
		if !c.Equal(want) {
			t.Fatalf("cutoff %s, want %s", c, want)
		}
	}
}

func TestPurgeTenants_FailingTenantDoesNotBlockTheOthers(t *testing.T) {
	f := &fakePurger{fail: "t-1", deleted: 4}
	total, err := jobs.PurgeTenants(context.Background(), f, []string{"t-1", "t-2"}, time.Now(), 30)
	if err == nil || !strings.Contains(err.Error(), "t-1") {
		t.Fatalf("err = %v, want the t-1 failure", err)
	}
	if total != 4 || len(f.calls) != 2 || f.calls[1] != "t-2" {
		t.Fatalf("total %d calls %v, want t-2 purged after the t-1 failure", total, f.calls)
	}
}

func TestPurgeTenants_NoTenantsIsNoOp(t *testing.T) {
	f := &fakePurger{}
	total, err := jobs.PurgeTenants(context.Background(), f, nil, time.Now(), 30)
	if err != nil || total != 0 || len(f.calls) != 0 {
		t.Fatalf("total %d err %v calls %v", total, err, f.calls)
	}
}

func TestRunDaily_ReturnsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		jobs.RunDaily(ctx, &fakePurger{}, []string{"t-1"}, 30, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunDaily did not return after cancel")
	}
}
