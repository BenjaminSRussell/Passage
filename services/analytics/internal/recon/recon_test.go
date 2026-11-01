package recon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

const (
	entityA = "11111111-1111-4111-8111-111111111111"
	entityB = "22222222-2222-4222-8222-222222222222"
)

type fakeSemantic struct {
	rows map[string]*decimal.Decimal
	err  error
}

func (f fakeSemantic) Revenue(context.Context, string) (map[string]*decimal.Decimal, error) {
	return f.rows, f.err
}

type fakeGL struct {
	rows []GLRevenue
	err  error
}

func (f fakeGL) Revenue(context.Context, string) ([]GLRevenue, error) { return f.rows, f.err }

type fakeRecorder struct {
	got     []domain.Reconciliation
	tenants []string
	failFor string
	ran     chan struct{}
}

func (r *fakeRecorder) RecordReconciliation(ctx context.Context, in domain.Reconciliation) (domain.Reconciliation, bool, error) {
	if in.LegalEntityID == r.failFor {
		return domain.Reconciliation{}, false, errors.New("store down")
	}
	p, _ := tenant.FromContext(ctx)
	r.tenants = append(r.tenants, p.TenantID)
	out, verr := domain.FinalizeReconciliation(in)
	if verr != nil {
		return domain.Reconciliation{}, false, verr
	}
	r.got = append(r.got, out)
	if r.ran != nil {
		select {
		case r.ran <- struct{}{}:
		default:
		}
	}
	return out, true, nil
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func ptr(s string) *decimal.Decimal { d := dec(s); return &d }

func newJob(sem fakeSemantic, gl fakeGL, rec *fakeRecorder) Job {
	return Job{Semantic: sem, GL: gl, Recorder: rec, Principal: tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", UserID: "job"}, Location: time.UTC, Now: time.Now}
}

func TestRunDay_NullSemanticRevenueIsZero(t *testing.T) {
	rec := &fakeRecorder{}
	job := newJob(
		fakeSemantic{rows: map[string]*decimal.Decimal{entityA: nil}},
		fakeGL{rows: []GLRevenue{{entityA, "USD", dec("0.00")}, {entityB, "USD", dec("10.00")}}},
		rec)
	res, err := job.RunDay(context.Background(), "2026-03-10")
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 2 || res.Failed != 1 {
		t.Fatalf("result %+v", res)
	}
	byEntity := map[string]domain.Reconciliation{}
	for _, r := range rec.got {
		byEntity[r.LegalEntityID] = r
	}
	if a := byEntity[entityA]; a.Status != domain.ReconOK || !a.SemanticValue.IsZero() || !a.Delta.IsZero() {
		t.Fatalf("NULL against a zero GL day must reconcile: %+v", a)
	}
	if b := byEntity[entityB]; b.Status != domain.ReconFailed || !b.SemanticValue.IsZero() || !b.Delta.Equal(dec("-10.00")) {
		t.Fatalf("an entity missing from the semantic layer reads as zero and fails against 10.00: %+v", b)
	}
	if rec.got[0].MetricID != MetricRevenue || rec.got[0].Date != "2026-03-10" || rec.got[0].Currency != "USD" {
		t.Fatalf("unexpected record %+v", rec.got[0])
	}
	for _, id := range rec.tenants {
		if id != job.Principal.TenantID {
			t.Fatalf("recorded under tenant %s", id)
		}
	}
}

func TestRunDay_ToleranceBoundary(t *testing.T) {
	cases := map[string]string{"0.005": domain.ReconOK, "0.006": domain.ReconFailed, "-0.006": domain.ReconFailed}
	for delta, want := range cases {
		t.Run(delta, func(t *testing.T) {
			rec := &fakeRecorder{}
			job := newJob(fakeSemantic{rows: map[string]*decimal.Decimal{entityA: ptr("100.00")}},
				fakeGL{rows: []GLRevenue{{entityA, "USD", dec("100.00").Sub(dec(delta))}}}, rec)
			if _, err := job.RunDay(context.Background(), "2026-03-10"); err != nil {
				t.Fatal(err)
			}
			if rec.got[0].Status != want {
				t.Fatalf("delta %s: status %s, want %s", delta, rec.got[0].Status, want)
			}
		})
	}
}

func TestRunDay_SourceFailureRecordsNothing(t *testing.T) {
	for name, job := range map[string]Job{
		"semantic": newJob(fakeSemantic{err: errors.New("cube down")}, fakeGL{rows: []GLRevenue{{entityA, "USD", dec("1")}}}, &fakeRecorder{}),
		"ledger":   newJob(fakeSemantic{}, fakeGL{err: errors.New("ledger down")}, &fakeRecorder{}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := job.RunDay(context.Background(), "2026-03-10"); err == nil {
				t.Fatal("want an error")
			}
			if n := len(job.Recorder.(*fakeRecorder).got); n != 0 {
				t.Fatalf("recorded %d results from a failed read", n)
			}
		})
	}
}

func TestRunDay_OneEntityFailingDoesNotStopTheRest(t *testing.T) {
	rec := &fakeRecorder{failFor: entityA}
	job := newJob(fakeSemantic{rows: map[string]*decimal.Decimal{entityB: ptr("5.00")}},
		fakeGL{rows: []GLRevenue{{entityA, "USD", dec("1")}, {entityB, "USD", dec("5.00")}}}, rec)
	res, err := job.RunDay(context.Background(), "2026-03-10")
	if err == nil || res.Checked != 1 || len(rec.got) != 1 || rec.got[0].LegalEntityID != entityB {
		t.Fatalf("res %+v err %v got %+v", res, err, rec.got)
	}
}

func TestNextRun(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database")
	}
	cases := []struct {
		name string
		now  time.Time
		loc  *time.Location
		want time.Time
	}{
		{"before 02:00", time.Date(2026, 3, 10, 1, 59, 0, 0, time.UTC), time.UTC, time.Date(2026, 3, 10, 2, 0, 0, 0, time.UTC)},
		{"at 02:00 waits a day", time.Date(2026, 3, 10, 2, 0, 0, 0, time.UTC), time.UTC, time.Date(2026, 3, 11, 2, 0, 0, 0, time.UTC)},
		{"after 02:00", time.Date(2026, 3, 10, 23, 0, 0, 0, time.UTC), time.UTC, time.Date(2026, 3, 11, 2, 0, 0, 0, time.UTC)},
		{"tenant local time", time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC), ny, time.Date(2026, 1, 10, 2, 0, 0, 0, ny)},
		{"tenant local time after", time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC), ny, time.Date(2026, 1, 11, 2, 0, 0, 0, ny)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NextRun(c.now, c.loc); !got.Equal(c.want) {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestLoop_RunsAtTwoAndReconcilesYesterday(t *testing.T) {
	start := time.Now()
	clock := time.Date(2026, 3, 10, 1, 59, 59, int(980*time.Millisecond), time.UTC)
	rec := &fakeRecorder{ran: make(chan struct{}, 1)}
	job := newJob(fakeSemantic{rows: map[string]*decimal.Decimal{entityA: ptr("3.00")}}, fakeGL{rows: []GLRevenue{{entityA, "USD", dec("3.00")}}}, rec)
	job.Now = func() time.Time { return clock.Add(time.Since(start)) }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { job.Loop(ctx); close(done) }()
	select {
	case <-rec.ran:
	case <-time.After(2 * time.Second):
		t.Fatal("the job never ran at 02:00")
	}
	cancel()
	<-done
	if rec.got[0].Date != "2026-03-09" {
		t.Fatalf("reconciled %s, want the previous day 2026-03-09", rec.got[0].Date)
	}
}
