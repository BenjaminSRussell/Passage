package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/migrations"
)

// newStore3 is newStore with the operator tenant set and the owner pool returned, to read the outbox past its tenant RLS.
func newStore3(t *testing.T) (*Postgres, *pgxpool.Pool) {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool).WithOperator(ids.New())
	if err := s.Seed(context.Background(), []domain.Region{{Name: "us-east", Role: domain.Primary}, {Name: "eu-west", Role: domain.Passive}}); err != nil {
		t.Fatal(err)
	}
	return s, admin
}

func countEvents(t *testing.T, admin *pgxpool.Pool, typ string) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// concurrently runs f from n goroutines released together and fails on any error.
func concurrently(t *testing.T, n int, f func() error) {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- f()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestLateFailedReportForADoneStepKeepsTheProvisionRunning(t *testing.T) {
	s, _ := newStore3(t)
	ctx := context.Background()
	tid := ids.New()
	req := domain.ProvisionRequest{TenantID: tid, Name: "Cedar Ridge Pumps", Residency: "EU", Region: "eu-west", Tier: "PROD"}
	if _, _, err := s.CreateProvision(ctx, "k1", req, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordProvisionStep(ctx, tid, "realm", "DONE", "", "runner"); err != nil {
		t.Fatal(err)
	}
	// The runner's retry of an earlier attempt reports FAILED after the step is already DONE: the step stays DONE, and so
	// nothing has failed.
	p, err := s.RecordProvisionStep(ctx, tid, "realm", "FAILED", "timeout", "runner")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.ProvisionRunning || len(p.Steps) != 1 || p.Steps[0].Status != "DONE" {
		t.Fatalf("provision after a late FAILED = %+v", p)
	}
	p, err = s.RecordProvisionStep(ctx, tid, "databases", "FAILED", "disk full", "runner")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.ProvisionFailed {
		t.Fatalf("a real failure fails the provision: %+v", p)
	}
}

func TestConcurrentFirstProvisionRequestsReplay(t *testing.T) {
	s, _ := newStore3(t)
	tid := ids.New()
	req := domain.ProvisionRequest{TenantID: tid, Name: "Cedar Ridge Pumps", Residency: "EU", Region: "eu-west", Tier: "PROD"}
	var mu sync.Mutex
	created := 0
	concurrently(t, 8, func() error {
		p, replay, err := s.CreateProvision(context.Background(), "same-key", req, "alice")
		if err != nil {
			return err
		}
		if p.TenantID != tid {
			t.Errorf("provision = %+v", p)
		}
		if !replay {
			mu.Lock()
			created++
			mu.Unlock()
		}
		return nil
	})
	if created != 1 {
		t.Fatalf("created %d provisions, want 1 and the rest replays", created)
	}
}

func TestConcurrentSLOChecksReportABreachOnce(t *testing.T) {
	s, admin := newStore3(t)
	avail := 0.9
	concurrently(t, 8, func() error {
		rep := &domain.SLOReport{Window: "1h", GeneratedAt: time.Now().UTC(), Services: []domain.SLOLine{
			{Service: "ledger", Availability: &avail, AvailabilityTarget: 0.999, AvailabilityBreach: true, Breached: true},
			{Service: "sales", AvailabilityTarget: 0.999, NoTrafficInWindow: true},
		}}
		_, err := s.RecordSLO(context.Background(), rep)
		return err
	})
	if n := countEvents(t, admin, EventSLOBreached); n != 1 {
		t.Fatalf("slo.breached written %d times by concurrent checks, want 1", n)
	}
}

func TestConcurrentLicenseChecksReportAChangeOnce(t *testing.T) {
	s, admin := newStore3(t)
	ctx := context.Background()
	tid := ids.New()
	l := domain.License{TenantID: tid, IssuedAt: "2026-01-01", ExpiresAt: "2030-12-31", FullUsers: 9, OperationalUsers: 5, SelfServiceUsers: 50}
	if _, err := s.InstallLicense(ctx, l, []byte("{}"), []byte("sig"), "alice", 0); err != nil {
		t.Fatal(err)
	}
	// A licence installed before migration 0006 has no check row: its first check must still report once across replicas.
	if _, err := admin.Exec(ctx, `delete from ops_license_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `delete from outbox`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
	concurrently(t, 8, func() error {
		_, err := s.CheckLicenses(ctx, now)
		return err
	})
	if n := countEvents(t, admin, EventLicenseChanged); n != 1 {
		t.Fatalf("license.changed written %d times by concurrent checks, want 1", n)
	}
	if n, err := s.CheckLicenses(ctx, now); err != nil || n != 0 {
		t.Fatalf("an unchanged state is not reported again: %d %v", n, err)
	}
}
