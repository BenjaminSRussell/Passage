package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/migrations"
)

func newStore(t *testing.T) *Postgres {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	seed := []domain.Region{{Name: "us-east", Role: domain.Primary}, {Name: "eu-west", Role: domain.Passive}}
	if err := s.Seed(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	return s
}

func roles(t *testing.T, s *Postgres) map[string]domain.Role {
	t.Helper()
	regions, err := s.Regions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]domain.Role{}
	for _, r := range regions {
		out[r.Name] = r.Role
	}
	return out
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestSeedOnlyFillsAnEmptyTable(t *testing.T) {
	s := newStore(t)
	if err := s.Seed(context.Background(), []domain.Region{{Name: "ap-south", Role: domain.Primary}}); err != nil {
		t.Fatal(err)
	}
	if r := roles(t, s); len(r) != 2 || r["ap-south"] != "" {
		t.Fatalf("seed overwrote the topology: %v", r)
	}
	if err := s.Seed(context.Background(), []domain.Region{{Name: "a", Role: domain.Primary}, {Name: "b", Role: domain.Primary}}); err == nil {
		t.Fatal("two PRIMARY seeded")
	}
}

func TestFailoverSwapsRolesAndRecordsIt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	rec, replay, err := s.Failover(ctx, "evt-1", domain.FailoverRequest{To: "eu-west", Caller: "alice", MaxLagSeconds: 300})
	if err != nil || replay {
		t.Fatalf("err = %v, replay = %v", err, replay)
	}
	if rec.Plan.From != "us-east" || rec.Plan.To != "eu-west" || len(rec.Plan.Steps) != 6 || rec.RequestedBy != "alice" {
		t.Fatalf("rec = %+v", rec)
	}
	if r := roles(t, s); r["eu-west"] != domain.Primary || r["us-east"] != domain.Fenced {
		t.Fatalf("roles = %v", r)
	}
	// The same event id returns the first outcome and changes nothing.
	again, replay, err := s.Failover(ctx, "evt-1", domain.FailoverRequest{To: "us-east", Caller: "bob", MaxLagSeconds: 300})
	if err != nil || !replay || again.Plan.To != "eu-west" || again.RequestedBy != "alice" {
		t.Fatalf("replay = %v, again = %+v, err = %v", replay, again, err)
	}
	if r := roles(t, s); r["eu-west"] != domain.Primary {
		t.Fatalf("roles after replay = %v", r)
	}
}

func TestFailoverRefusalsLeaveTheTopologyAlone(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.ReportLag(ctx, "eu-west", 900); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Failover(ctx, "evt-lag", domain.FailoverRequest{To: "eu-west", Caller: "alice", MaxLagSeconds: 300})
	if problemCode(err) != domain.CodeLagExceeded {
		t.Fatalf("err = %v", err)
	}
	_, _, err = s.Failover(ctx, "evt-same", domain.FailoverRequest{To: "eu-west", Caller: "alice", Approver: "alice", AcceptDataLoss: true, MaxLagSeconds: 300})
	if problemCode(err) != domain.CodeApprovalRequired {
		t.Fatalf("err = %v", err)
	}
	if r := roles(t, s); r["us-east"] != domain.Primary || r["eu-west"] != domain.Passive {
		t.Fatalf("roles = %v", r)
	}
	rec, _, err := s.Failover(ctx, "evt-ok", domain.FailoverRequest{To: "eu-west", Caller: "alice", Approver: "bob", AcceptDataLoss: true, MaxLagSeconds: 300})
	if err != nil || !rec.Plan.AcceptDataLoss || rec.ApprovedBy != "bob" {
		t.Fatalf("rec = %+v, err = %v", rec, err)
	}
}

func TestConcurrentFailoversNeverLeaveTwoPrimaries(t *testing.T) {
	s := newStore(t)
	var wg sync.WaitGroup
	results := make([]error, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, results[i] = s.Failover(context.Background(), "evt-"+string(rune('a'+i)), domain.FailoverRequest{To: "eu-west", Caller: "alice", MaxLagSeconds: 300})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d failovers succeeded, want exactly 1: %v", ok, results)
	}
	primaries := 0
	for _, role := range roles(t, s) {
		if role == domain.Primary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("%d PRIMARY regions", primaries)
	}
}

func TestOnePrimaryIndexBacksTheStateMachine(t *testing.T) {
	s := newStore(t)
	if _, err := s.pool.Exec(context.Background(), `update ops_region set role = 'PRIMARY' where name = 'eu-west'`); err == nil {
		t.Fatal("database accepted two PRIMARY regions")
	}
}

func TestRejoinNeedsAVerifiedResync(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Rejoin(ctx, "us-east", true); problemCode(err) != domain.CodeTransitionInvalid {
		t.Fatalf("primary rejoin: %v", err)
	}
	if _, _, err := s.Failover(ctx, "evt-1", domain.FailoverRequest{To: "eu-west", Caller: "alice", MaxLagSeconds: 300}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rejoin(ctx, "us-east", false); problemCode(err) != domain.CodeResyncUnverified {
		t.Fatalf("unverified: %v", err)
	}
	if _, err := s.Rejoin(ctx, "nowhere", true); problemCode(err) != domain.CodeRegionUnknown {
		t.Fatalf("unknown: %v", err)
	}
	r, err := s.Rejoin(ctx, "us-east", true)
	if err != nil || r.Role != domain.Passive {
		t.Fatalf("r = %+v, err = %v", r, err)
	}
	if got := roles(t, s); got["us-east"] != domain.Passive || got["eu-west"] != domain.Primary {
		t.Fatalf("roles = %v", got)
	}
}

func TestReportLagUnknownRegion(t *testing.T) {
	s := newStore(t)
	if _, err := s.ReportLag(context.Background(), "nowhere", 5); problemCode(err) != domain.CodeRegionUnknown {
		t.Fatalf("err = %v", err)
	}
}
