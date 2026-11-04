package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: scope, Duties: testkit.ScopeDuties(scope)})
}

func migratedPools(t *testing.T) (admin, app *pgxpool.Pool) {
	t.Helper()
	admin, app = testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return admin, app
}

func testStore(t *testing.T) (*Postgres, *pgxpool.Pool) {
	t.Helper()
	admin, app := migratedPools(t)
	return New(app), admin
}

func requireRLSViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("err = %v, want a pgconn error", err)
	}
	if pgErr.Code != "42501" || !strings.Contains(pgErr.Message, "row-level security") {
		t.Fatalf("pg error = %s %q, want 42501 row-level security", pgErr.Code, pgErr.Message)
	}
}

func TestChildInsertUnderOtherEntityParentIsRefused(t *testing.T) {
	_, app := migratedPools(t)
	entityA, entityB := ids.New(), ids.New()
	created, err := New(app).CreateContract(tenantCtx(1, entityA), bundleContract(t, entityA))
	if err != nil {
		t.Fatalf("create contract: %v", err)
	}
	err = dbx.WithTenantTx(tenantCtx(1, entityB), app, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO performance_obligation
			(tenant_id, id, contract_id, line_no, product_id, ssp_amount, ssp_source, allocated_amount, method)
			VALUES ($1, $2, $3, 99, $4, 1, 'EXPLICIT', 1, 'POINT_IN_TIME')`,
			testkit.TenantID(1), ids.New(), created.ID, ids.New())
		return err
	})
	requireRLSViolation(t, err)
}

func TestScheduleInsertUnderOtherEntityObligationIsRefused(t *testing.T) {
	_, app := migratedPools(t)
	entityA, entityB := ids.New(), ids.New()
	created, err := New(app).CreateContract(tenantCtx(1, entityA), bundleContract(t, entityA))
	if err != nil {
		t.Fatalf("create contract: %v", err)
	}
	obligationID := created.Obligations[0].ID
	insert := func(ctx context.Context, scheduleID string) error {
		return dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `INSERT INTO revenue_schedule
				(tenant_id, id, obligation_id, seq, scheduled_date, amount, status)
				VALUES ($1, $2, $3, 99, '2027-04-01', 1, 'SCHEDULED')`,
				testkit.TenantID(1), scheduleID, obligationID)
			return err
		})
	}
	requireRLSViolation(t, insert(tenantCtx(1, entityB), ids.New()))

	ownID := ids.New()
	if err := insert(tenantCtx(1, entityA), ownID); err != nil {
		t.Fatalf("owning entity insert: %v", err)
	}
	var count int
	err = dbx.WithTenantTx(tenantCtx(1, entityA), app, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), "SELECT count(*) FROM revenue_schedule WHERE tenant_id = $1 AND id = $2", testkit.TenantID(1), ownID).Scan(&count)
	})
	if err != nil || count != 1 {
		t.Fatalf("owning entity read = %d, %v; want 1, nil", count, err)
	}
}

func TestLegalEntityScopeRestrictsRevenueChildren(t *testing.T) {
	_, app := migratedPools(t)
	s := New(app)
	entityA, entityB := ids.New(), ids.New()
	created, err := s.CreateContract(tenantCtx(1, entityA), bundleContract(t, entityA))
	if err != nil {
		t.Fatalf("create contract: %v", err)
	}
	if len(created.Obligations) == 0 || len(created.Obligations[len(created.Obligations)-1].Schedule) == 0 {
		t.Fatal("fixture must contain obligations and schedule rows")
	}
	rows := []struct {
		table string
		id    string
	}{
		{table: "performance_obligation", id: created.Obligations[0].ID},
		{table: "revenue_schedule", id: created.Obligations[len(created.Obligations)-1].Schedule[0].ID},
	}
	for _, row := range rows {
		t.Run(row.table, func(t *testing.T) {
			var count int
			err := dbx.WithTenantTx(tenantCtx(1, entityB), app, func(tx pgx.Tx) error {
				return tx.QueryRow(context.Background(), "SELECT count(*) FROM "+row.table+" WHERE tenant_id = $1 AND id = $2", testkit.TenantID(1), row.id).Scan(&count)
			})
			if err != nil {
				t.Fatalf("query child row: %v", err)
			}
			if count != 0 {
				t.Errorf("entity %s can see %s row %s owned by entity %s", entityB, row.table, row.id, entityA)
			}
		})
	}
}

func TestOutboxRejectsTenantMismatch(t *testing.T) {
	_, app := migratedPools(t)
	err := dbx.WithTenantTx(tenantCtx(1, ""), app, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload)
			VALUES ($1, $2, 'customer', $3, 'test', '{}'::jsonb)`, ids.New(), testkit.TenantID(2), ids.New())
		return err
	})
	requireRLSViolation(t, err)
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
		lines, err := domain.ScheduleLines(o, "USD", domain.BasisDaily, ob.TermStart, ob.TermEnd, ob.SatisfiedOn)
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

	page, err := s.ListContracts(ctx, ContractFilter{Limit: 10})
	all := page.Items
	if err != nil || len(all) != 2 || all[0].ID != done.ID || page.NextAfter != "" {
		t.Fatalf("list %+v err %v, want both contracts newest first on one page", page, err)
	}
	if !all[1].Recognized.Equal(decimal.RequireFromString("10800")) || len(all[1].Obligations) != 0 || all[1].RatableBasis != domain.BasisDaily {
		t.Errorf("list row %+v, want recognized 10800, the DAILY basis and no obligations loaded", all[1])
	}
	// Keyset pages of one: the first ends at the newest contract, the second
	// holds the older one and is the last.
	first, err := s.ListContracts(ctx, ContractFilter{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != done.ID || first.NextAfter != done.ID {
		t.Fatalf("first page %+v err %v, want the newest contract and a cursor", first, err)
	}
	second, err := s.ListContracts(ctx, ContractFilter{Limit: 1, After: first.NextAfter})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != bundle.ID || second.NextAfter != "" {
		t.Errorf("second page %+v err %v, want the bundle and no cursor", second, err)
	}
	ratable, err := s.ListContracts(ctx, ContractFilter{Method: domain.MethodRatable, Limit: 10})
	if err != nil || len(ratable.Items) != 1 || ratable.Items[0].ID != bundle.ID {
		t.Errorf("RATABLE method filter %+v err %v, want the bundle only", ratable, err)
	}
	byCustomer, err := s.ListContracts(ctx, ContractFilter{CustomerID: bundle.CustomerID, Limit: 10})
	if err != nil || len(byCustomer.Items) != 1 || byCustomer.Items[0].ID != bundle.ID {
		t.Errorf("customer filter %+v err %v", byCustomer, err)
	}
	byOrder, err := s.ListContracts(ctx, ContractFilter{OrderID: bundle.OrderID, Limit: 10})
	if err != nil || len(byOrder.Items) != 1 || byOrder.Items[0].ID != bundle.ID {
		t.Errorf("order filter %+v err %v, want the bundle only", byOrder, err)
	}
	if upper, err := s.ListContracts(ctx, ContractFilter{OrderID: strings.ToUpper(bundle.OrderID), Limit: 10}); err != nil || len(upper.Items) != 1 {
		t.Errorf("uppercase order filter %+v err %v, want the bundle", upper, err)
	}
	if none, err := s.ListContracts(ctx, ContractFilter{OrderID: ids.New(), Limit: 10}); err != nil || len(none.Items) != 0 {
		t.Errorf("another order's filter %+v err %v, want none", none, err)
	}
	open, err := s.ListContracts(ctx, ContractFilter{Status: domain.ContractOpen, Limit: 10})
	if err != nil || len(open.Items) != 0 {
		t.Errorf("open filter %+v err %v, want none", open, err)
	}

	if _, err := s.GetContract(tenantCtx(2, ""), bundle.ID); problemCode(err) != CodeNotFound {
		t.Errorf("tenant 2 read tenant 1's contract: %v", err)
	}
	other := tenantCtx(1, ids.New())
	if list, err := s.ListContracts(other, ContractFilter{Limit: 10}); err != nil || len(list.Items) != 0 {
		t.Errorf("another legal-entity scope listed %d contracts (err %v)", len(list.Items), err)
	}
	if _, err := s.ContractRollForward(other, bundle.ID, day(2027, time.March, 1), day(2027, time.March, 31)); problemCode(err) != CodeNotFound {
		t.Errorf("another legal-entity scope drilled into the contract: %v", err)
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

// TestContractRollForward_DrillDownSumsToEntity runs the AC-29.4 month for two
// contracts of one entity: each contract's roll-forward is its own movement in
// its currency, and the two add up to the entity roll-forward (FR-29.9).
func TestContractRollForward_DrillDownSumsToEntity(t *testing.T) {
	s, _ := testStore(t)
	ctx := tenantCtx(1, "")
	le := ids.New()
	a, err := s.CreateContract(ctx, bundleContract(t, le))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateContract(ctx, bundleContract(t, le))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SatisfyMilestone(ctx, a.Obligations[1].ID, day(2027, time.March, 25), 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunRecognition(ctx, le, day(2027, time.March, 31)); err != nil {
		t.Fatal(err)
	}
	from, to := day(2027, time.March, 1), day(2027, time.March, 31)
	ra, err := s.ContractRollForward(ctx, a.ID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if ra.Currency != "USD" || ra.LegalEntityID != le || ra.Additions.StringFixed(2) != "3600.00" || ra.Recognized.StringFixed(2) != "1483.06" || ra.Closing.StringFixed(2) != "2116.94" {
		t.Errorf("contract A %+v, want USD additions 3600.00 recognized 1483.06 closing 2116.94 (AC-29.4)", ra)
	}
	rb, err := s.ContractRollForward(ctx, b.ID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if rb.Recognized.StringFixed(2) != "133.06" || rb.Closing.StringFixed(2) != "3466.94" {
		t.Errorf("contract B %+v, want recognized 133.06 (support only) and closing 3466.94", rb)
	}
	entity, err := s.RollForward(ctx, le, "USD", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if !entity.Closing.Equal(ra.Closing.Add(rb.Closing)) || !entity.Recognized.Equal(ra.Recognized.Add(rb.Recognized)) {
		t.Errorf("entity %+v is not the sum of contracts %+v and %+v", entity, ra.RollForward, rb.RollForward)
	}
	april, err := s.ContractRollForward(ctx, a.ID, day(2027, time.April, 1), day(2027, time.April, 30))
	if err != nil || !april.Opening.Equal(ra.Closing) || !april.Additions.IsZero() {
		t.Errorf("April opening %+v err %v, want March closing %s and no additions", april, err, ra.Closing)
	}
	if _, err := s.ContractRollForward(ctx, ids.New(), from, to); problemCode(err) != CodeNotFound {
		t.Errorf("unknown contract: %v, want %s", err, CodeNotFound)
	}
}

// TestCreateContract_StoresMonthlyBasis keeps the basis that planned the lines.
func TestCreateContract_StoresMonthlyBasis(t *testing.T) {
	s, _ := testStore(t)
	ctx := tenantCtx(1, "")
	c := bundleContract(t, ids.New())
	c.RatableBasis = domain.BasisMonthly
	if _, err := s.CreateContract(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetContract(ctx, c.ID)
	if err != nil || got.RatableBasis != domain.BasisMonthly || got.CreatedAt.IsZero() {
		t.Errorf("stored basis %q created %v err %v, want MONTHLY and a creation time", got.RatableBasis, got.CreatedAt, err)
	}
}
