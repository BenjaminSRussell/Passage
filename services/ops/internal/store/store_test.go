package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/migrations"
)

func newStore(t *testing.T) *Postgres {
	t.Helper()
	// Migrations run as the admin role; the store runs as passage_app, so a missing grant fails here, not in production.
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
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
	out, err := s.Failover(ctx, "evt-1", domain.FailoverRequest{To: "eu-west", Caller: "alice", MaxLagSeconds: 300})
	if err != nil || out.Replay || out.Failover == nil {
		t.Fatalf("err = %v, out = %+v", err, out)
	}
	rec := *out.Failover
	if rec.Plan.From != "us-east" || rec.Plan.To != "eu-west" || len(rec.Plan.Steps) != 6 || rec.RequestedBy != "alice" {
		t.Fatalf("rec = %+v", rec)
	}
	if r := roles(t, s); r["eu-west"] != domain.Primary || r["us-east"] != domain.Fenced {
		t.Fatalf("roles = %v", r)
	}
	// The same event id returns the first outcome and changes nothing.
	out, err = s.Failover(ctx, "evt-1", domain.FailoverRequest{To: "us-east", Caller: "bob", MaxLagSeconds: 300})
	if err != nil || !out.Replay || out.Failover == nil || out.Failover.Plan.To != "eu-west" || out.Failover.RequestedBy != "alice" {
		t.Fatalf("out = %+v, err = %v", out, err)
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
	_, err := s.Failover(ctx, "evt-lag", domain.FailoverRequest{To: "eu-west", Caller: "alice", MaxLagSeconds: 300})
	if problemCode(err) != domain.CodeLagExceeded {
		t.Fatalf("err = %v", err)
	}
	// An approver passed to Failover is ignored: accepting data loss always becomes a request, never an applied failover.
	out, err := s.Failover(ctx, "evt-req", domain.FailoverRequest{To: "eu-west", Caller: "alice", Approver: "bob", AcceptDataLoss: true, MaxLagSeconds: 300})
	if err != nil || out.Request == nil || out.Failover != nil || out.Request.Status != "PENDING" || out.Request.RequestedBy != "alice" || out.Request.LagSeconds != 900 {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if r := roles(t, s); r["us-east"] != domain.Primary || r["eu-west"] != domain.Passive {
		t.Fatalf("roles = %v", r)
	}
	again, err := s.Failover(ctx, "evt-req", domain.FailoverRequest{To: "eu-west", Caller: "alice", AcceptDataLoss: true, MaxLagSeconds: 300})
	if err != nil || !again.Replay || again.Request == nil || again.Request.ID != out.Request.ID {
		t.Fatalf("replay = %+v, err = %v", again, err)
	}
}

func TestApproveNeedsASecondOperator(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.ReportLag(ctx, "eu-west", 900); err != nil {
		t.Fatal(err)
	}
	out, err := s.Failover(ctx, "evt-1", domain.FailoverRequest{To: "eu-west", Caller: "alice", AcceptDataLoss: true, MaxLagSeconds: 300})
	if err != nil || out.Request == nil {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	id := out.Request.ID
	if _, _, err := s.Approve(ctx, id, "alice", 300); problemCode(err) != domain.CodeApprovalRequired {
		t.Fatalf("self-approval: %v", err)
	}
	if r := roles(t, s); r["us-east"] != domain.Primary {
		t.Fatalf("self-approval changed roles: %v", r)
	}
	if _, _, err := s.Approve(ctx, ids.New(), "bob", 300); problemCode(err) != domain.CodeRequestUnknown {
		t.Fatalf("unknown request: %v", err)
	}
	rec, replay, err := s.Approve(ctx, id, "bob", 300)
	if err != nil || replay || rec.RequestedBy != "alice" || rec.ApprovedBy != "bob" || !rec.Plan.AcceptDataLoss || rec.EventID != "evt-1" {
		t.Fatalf("rec = %+v, replay = %v, err = %v", rec, replay, err)
	}
	if r := roles(t, s); r["eu-west"] != domain.Primary || r["us-east"] != domain.Fenced {
		t.Fatalf("roles = %v", r)
	}
	if _, replay, err := s.Approve(ctx, id, "carol", 300); err != nil || !replay {
		t.Fatalf("second approval: replay = %v, err = %v", replay, err)
	}
	got, err := s.Request(ctx, id)
	if err != nil || got.Status != "APPROVED" || got.ApprovedBy != "bob" || got.ApprovedAt == nil {
		t.Fatalf("request = %+v, err = %v", got, err)
	}
	// The original event id now replays the applied failover.
	if out, err := s.Failover(ctx, "evt-1", domain.FailoverRequest{To: "eu-west", Caller: "alice", AcceptDataLoss: true, MaxLagSeconds: 300}); err != nil || !out.Replay || out.Failover == nil || out.Failover.ApprovedBy != "bob" {
		t.Fatalf("event replay = %+v, err = %v", out, err)
	}
	// The database refuses a request approved by its own requester even if a bug skipped the check.
	if _, err := s.pool.Exec(ctx, `update ops_failover_request set approved_by = requested_by where id = $1`, id); err == nil {
		t.Fatal("database accepted a self-approved request")
	}
}

func TestApproveRefusesAnExpiredRequest(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.ReportLag(ctx, "eu-west", 900); err != nil {
		t.Fatal(err)
	}
	out, err := s.Failover(ctx, "evt-1", domain.FailoverRequest{To: "eu-west", Caller: "alice", AcceptDataLoss: true, MaxLagSeconds: 300})
	if err != nil || out.Request == nil {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if _, err := s.pool.Exec(ctx, `update ops_failover_request set expires_at = now() - interval '1 second' where id = $1`, out.Request.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Approve(ctx, out.Request.ID, "bob", 300); problemCode(err) != domain.CodeRequestExpired {
		t.Fatalf("expired: %v", err)
	}
	if r := roles(t, s); r["us-east"] != domain.Primary {
		t.Fatalf("roles = %v", r)
	}
}

func TestConcurrentApprovalsApplyOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.ReportLag(ctx, "eu-west", 900); err != nil {
		t.Fatal(err)
	}
	out, err := s.Failover(ctx, "evt-1", domain.FailoverRequest{To: "eu-west", Caller: "alice", AcceptDataLoss: true, MaxLagSeconds: 300})
	if err != nil || out.Request == nil {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	var wg sync.WaitGroup
	fresh := make([]bool, 4)
	errs := make([]error, 4)
	for i := range fresh {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var replay bool
			_, replay, errs[i] = s.Approve(ctx, out.Request.ID, "approver-"+string(rune('a'+i)), 300)
			fresh[i] = errs[i] == nil && !replay
		}()
	}
	wg.Wait()
	applied := 0
	for i := range fresh {
		if errs[i] != nil {
			t.Fatalf("approval %d: %v", i, errs[i])
		}
		if fresh[i] {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("%d approvals applied the failover, want 1", applied)
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
			_, results[i] = s.Failover(context.Background(), "evt-"+string(rune('a'+i)), domain.FailoverRequest{To: "eu-west", Caller: "alice", MaxLagSeconds: 300})
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
	if _, err := s.Failover(ctx, "evt-1", domain.FailoverRequest{To: "eu-west", Caller: "alice", MaxLagSeconds: 300}); err != nil {
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
