package store

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
	rev "github.com/BenjaminSRussell/passage/services/revrec/internal/events"
	"github.com/BenjaminSRussell/passage/services/revrec/migrations"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// tenantCtx is a principal of tenant n; scope is its legal entity ("" = all).
func tenantCtx(n int, scope string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: scope})
}

func testStore(t *testing.T) (*Postgres, *pgxpool.Pool) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(pool), pool
}

// bundleContract builds the BR-29.1 bundle the way the API does: allocate, then
// plan each obligation's schedule and set the initial statuses.
func bundleContract(t *testing.T, legalEntityID string) Contract {
	t.Helper()
	obs := []domain.Obligation{
		{ID: "hardware", SSP: decimal.RequireFromString("8000"), SSPSource: "EXPLICIT", Method: domain.MethodPointInTime},
		{ID: "installation", SSP: decimal.RequireFromString("1500"), SSPSource: "EXPLICIT", Method: domain.MethodMilestone},
		{ID: "support", SSP: decimal.RequireFromString("2500"), SSPSource: "EXPLICIT", Method: domain.MethodRatable},
	}
	allocated, err := domain.Allocate(money.Must("10800.00", "USD"), obs, domain.ResidualLargest)
	if err != nil {
		t.Fatal(err)
	}
	c := Contract{
		ID: ids.New(), LegalEntityID: legalEntityID, OrderID: ids.New(), CustomerID: ids.New(), Currency: "USD",
		TransactionPrice: decimal.RequireFromString("10800.00"), ResidualRule: domain.ResidualLargest,
		ContractDate: day(2027, time.March, 10), IdempotencyKey: ids.New(), RequestHash: "hash",
	}
	shipped := day(2027, time.March, 10)
	satisfied := []*time.Time{&shipped, nil, nil}
	start, end := day(2027, time.March, 10), day(2028, time.March, 9)
	for i, o := range allocated {
		ob := Obligation{ID: ids.New(), ContractID: c.ID, LineNo: i + 1, ProductID: ids.New(), SSP: o.SSP, SSPSource: o.SSPSource,
			Allocated: o.Allocated, Method: o.Method, SatisfiedOn: satisfied[i]}
		if o.Method == domain.MethodRatable {
			ob.TermStart, ob.TermEnd = &start, &end
		}
		lines, err := domain.ScheduleLines(o, "USD", ob.TermStart, ob.TermEnd, ob.SatisfiedOn)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			sc := Schedule{ID: ids.New(), Seq: l.Seq, Date: l.Date, Amount: l.Amount, Status: domain.InitialStatus(o.Method, l.Date)}
			if sc.Status == domain.ScheduleRecognized {
				sc.RecognizedOn = l.Date
			}
			ob.Schedule = append(ob.Schedule, sc)
		}
		c.Obligations = append(c.Obligations, ob)
	}
	return c
}

func outboxCount(t *testing.T, pool *pgxpool.Pool, typ string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// TestBundleRollForward_AC29_4 creates the BR-29.1 contract, satisfies the
// installation on 2027-03-25, runs March and checks the roll-forward: additions
// 3,600.00, recognized 1,483.06 (1,350.00 + 133.06), closing 2,116.94 (AC-29.4).
func TestBundleRollForward_AC29_4(t *testing.T) {
	s, pool := testStore(t)
	ctx := tenantCtx(1, "")
	le := ids.New()
	created, err := s.CreateContract(ctx, bundleContract(t, le))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Status != domain.ContractOpen || created.Recognized.StringFixed(2) != "7200.00" {
		t.Errorf("created status %s recognized %s, want OPEN with the hardware 7200.00", created.Status, created.Recognized)
	}
	// The hardware revenue is on the invoice (Cr 4000): contract.allocated only, no schedule.recognized.
	if n, m := outboxCount(t, pool, rev.ContractAllocated), outboxCount(t, pool, rev.ScheduleRecognized); n != 1 || m != 0 {
		t.Errorf("outbox after create: %d allocated, %d recognized; want 1 and 0", n, m)
	}

	got, found, err := s.FindContractByKey(ctx, created.IdempotencyKey)
	if err != nil || !found || got.ID != created.ID {
		t.Fatalf("find by key: found=%v err=%v", found, err)
	}
	if _, found, err := s.FindContractByKey(ctx, "never-used"); err != nil || found {
		t.Errorf("unknown key: found=%v err=%v", found, err)
	}
	round, err := s.GetContract(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if n := len(round.Obligations[2].Schedule); n != 13 || round.Obligations[2].Schedule[0].Amount.StringFixed(2) != "133.06" {
		t.Errorf("support has %d schedule lines starting %s, want 13 starting 133.06", n, round.Obligations[2].Schedule[0].Amount)
	}

	install := created.Obligations[1].ID
	if _, err := s.SatisfyMilestone(ctx, install, day(2027, time.March, 25), 9); problemCode(err) != CodeVersion {
		t.Errorf("stale If-Match: %v, want %s", err, CodeVersion)
	}
	sat, err := s.SatisfyMilestone(ctx, install, day(2027, time.March, 25), 1)
	if err != nil {
		t.Fatalf("satisfy: %v", err)
	}
	if sat.Amount.StringFixed(2) != "1350.00" || sat.Replayed || sat.ContractVersion != 2 {
		t.Errorf("satisfaction %+v, want 1350.00, not replayed, contract version 2", sat)
	}
	var amount, period, entity, aggregate string
	if err := pool.QueryRow(context.Background(), `select payload->'data'->>'amount', payload->'data'->>'period_id',
		payload->'data'->>'legal_entity_id', aggregate_id from outbox where type = $1`, rev.ScheduleRecognized).
		Scan(&amount, &period, &entity, &aggregate); err != nil {
		t.Fatal(err)
	}
	if amount != "1350.00" || period != "2027-03" || entity != le || aggregate != created.ID {
		t.Errorf("schedule.recognized carries %s %s %s keyed %s, want 1350.00 2027-03 %s keyed by the contract", amount, period, entity, aggregate, le)
	}
	replay, err := s.SatisfyMilestone(ctx, install, day(2027, time.March, 25), 1)
	if err != nil || !replay.Replayed || replay.ScheduleID != sat.ScheduleID {
		t.Errorf("retry on the same date: %+v err %v, want a replay", replay, err)
	}
	if _, err := s.SatisfyMilestone(ctx, install, day(2027, time.April, 1), 0); problemCode(err) != "REV-0004" {
		t.Errorf("second satisfaction on another date: %v, want REV-0004", err)
	}
	if _, err := s.SatisfyMilestone(ctx, created.Obligations[2].ID, day(2027, time.March, 25), 0); problemCode(err) != "REV-0005" {
		t.Errorf("satisfying the ratable support: %v, want REV-0005", err)
	}
	if _, err := s.SatisfyMilestone(ctx, ids.New(), day(2027, time.March, 25), 0); problemCode(err) != CodeNotFound {
		t.Errorf("unknown obligation: %v, want %s", err, CodeNotFound)
	}

	run, createdRun, err := s.RunRecognition(ctx, le, day(2027, time.March, 31))
	if err != nil || !createdRun || run.LineCount != 1 {
		t.Fatalf("first run %+v created=%v err=%v, want one line", run, createdRun, err)
	}
	again, createdAgain, err := s.RunRecognition(ctx, le, day(2027, time.March, 31))
	if err != nil || createdAgain || again.ID != run.ID || again.LineCount != 1 {
		t.Errorf("rerun %+v created=%v err=%v, want the same run", again, createdAgain, err)
	}
	if n := outboxCount(t, pool, rev.ScheduleRecognized); n != 2 {
		t.Errorf("schedule.recognized events %d, want 2 (installation and March support)", n)
	}

	rf, err := s.RollForward(ctx, le, "USD", day(2027, time.March, 1), day(2027, time.March, 31))
	if err != nil {
		t.Fatalf("roll-forward: %v", err)
	}
	checks := map[string][2]string{
		"opening":    {rf.Opening.StringFixed(2), "0.00"},
		"additions":  {rf.Additions.StringFixed(2), "3600.00"},
		"recognized": {rf.Recognized.StringFixed(2), "1483.06"},
		"closing":    {rf.Closing.StringFixed(2), "2116.94"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %s, want %s", name, c[0], c[1])
		}
	}
	// April opens at March's closing; the April run recognizes one 187.50 line.
	if _, _, err := s.RunRecognition(ctx, le, day(2027, time.April, 30)); err != nil {
		t.Fatal(err)
	}
	april, err := s.RollForward(ctx, le, "USD", day(2027, time.April, 1), day(2027, time.April, 30))
	if err != nil || april.Opening.StringFixed(2) != "2116.94" || april.Recognized.StringFixed(2) != "187.50" || april.Closing.StringFixed(2) != "1929.44" {
		t.Errorf("April roll-forward %+v err %v, want 2116.94 - 187.50 = 1929.44", april, err)
	}

	after, err := s.GetContract(ctx, created.ID)
	if err != nil || after.Version != 4 || after.Status != domain.ContractOpen {
		t.Errorf("contract after satisfy and two runs: version %d status %s err %v, want 4 and OPEN", after.Version, after.Status, err)
	}
	runs, err := s.ListRuns(ctx, le, 10)
	if err != nil || len(runs) != 2 || !runs[0].PeriodEnd.Equal(day(2027, time.April, 30)) {
		t.Errorf("runs %+v err %v, want April then March", runs, err)
	}
}

// TestContracts_CompleteListAndIsolation checks that a contract is COMPLETE once
// every line is recognized, that the list carries recognized amounts and
// filters, and that another tenant or legal-entity scope sees nothing.
func TestContracts_CompleteListAndIsolation(t *testing.T) {
	s, _ := testStore(t)
	ctx := tenantCtx(1, "")
	le := ids.New()
	bundle, err := s.CreateContract(ctx, bundleContract(t, le))
	if err != nil {
		t.Fatal(err)
	}
	hardwareOnly := bundleContract(t, le)
	hardwareOnly.Obligations = hardwareOnly.Obligations[:1]
	hardwareOnly.Obligations[0].Allocated = hardwareOnly.TransactionPrice
	hardwareOnly.Obligations[0].Schedule[0].Amount = hardwareOnly.TransactionPrice
	done, err := s.CreateContract(ctx, hardwareOnly)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != domain.ContractComplete {
		t.Errorf("point-in-time-only contract status %s, want COMPLETE", done.Status)
	}

	if _, err := s.SatisfyMilestone(ctx, bundle.Obligations[1].ID, day(2027, time.March, 25), 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunRecognition(ctx, le, day(2028, time.March, 31)); err != nil {
		t.Fatal(err)
	}
	closed, err := s.GetContract(ctx, bundle.ID)
	if err != nil || closed.Status != domain.ContractComplete || closed.Recognized.StringFixed(2) != "10800.00" {
		t.Errorf("bundle after the last run: status %s recognized %s err %v, want COMPLETE and 10800.00", closed.Status, closed.Recognized, err)
	}

	all, err := s.ListContracts(ctx, ContractFilter{Limit: 10})
	if err != nil || len(all) != 2 || all[0].ID != done.ID {
		t.Fatalf("list %+v err %v, want both contracts newest first", all, err)
	}
	if !all[1].Recognized.Equal(decimal.RequireFromString("10800")) || len(all[1].Obligations) != 0 {
		t.Errorf("list row %+v, want recognized 10800 and no obligations loaded", all[1])
	}
	byCustomer, err := s.ListContracts(ctx, ContractFilter{CustomerID: bundle.CustomerID, Limit: 10})
	if err != nil || len(byCustomer) != 1 || byCustomer[0].ID != bundle.ID {
		t.Errorf("customer filter %+v err %v", byCustomer, err)
	}
	open, err := s.ListContracts(ctx, ContractFilter{Status: domain.ContractOpen, Limit: 10})
	if err != nil || len(open) != 0 {
		t.Errorf("open filter %+v err %v, want none", open, err)
	}

	if _, err := s.GetContract(tenantCtx(2, ""), bundle.ID); problemCode(err) != CodeNotFound {
		t.Errorf("tenant 2 read tenant 1's contract: %v", err)
	}
	other := tenantCtx(1, ids.New())
	if list, err := s.ListContracts(other, ContractFilter{Limit: 10}); err != nil || len(list) != 0 {
		t.Errorf("another legal-entity scope listed %d contracts (err %v)", len(list), err)
	}
	if _, err := s.SatisfyMilestone(other, bundle.Obligations[1].ID, day(2027, time.March, 25), 0); problemCode(err) != CodeNotFound {
		t.Errorf("another legal-entity scope reached the obligation: %v", err)
	}
	if rf, err := s.RollForward(tenantCtx(2, ""), le, "USD", day(2027, time.March, 1), day(2027, time.March, 31)); err != nil || !rf.Additions.IsZero() {
		t.Errorf("tenant 2 roll-forward %+v err %v, want zero", rf, err)
	}
}

func TestCreateContract_DuplicateKeyIs409(t *testing.T) {
	s, _ := testStore(t)
	ctx := tenantCtx(1, "")
	first := bundleContract(t, ids.New())
	if _, err := s.CreateContract(ctx, first); err != nil {
		t.Fatal(err)
	}
	dup := bundleContract(t, first.LegalEntityID)
	dup.IdempotencyKey = first.IdempotencyKey
	_, err := s.CreateContract(ctx, dup)
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != http.StatusConflict || p.Code != CodeKeyInFlight {
		t.Errorf("duplicate key: %v, want 409 %s", err, CodeKeyInFlight)
	}
}
