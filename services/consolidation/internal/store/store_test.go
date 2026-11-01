package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/store"
	"github.com/BenjaminSRussell/passage/services/consolidation/migrations"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func runInput(group, period string, netIncome string) domain.RunInput {
	return domain.RunInput{GroupID: group, Period: period, Currency: "USD", Units: []domain.RunUnit{{
		ID: "S1",
		Pair: domain.Pair{
			Currency: "USD", OwnershipPct: dec("80"),
			Rates:   domain.Rates{Closing: dec("1.10"), Average: dec("1.08"), Opening: dec("1.05"), Historical: dec("1.05")},
			ICSales: dec("100000"), ICCost: dec("70000"), UnsoldShare: dec("0.4"), ICReceivable: dec("100000"),
			PayableGroup: dec("108500"), ShareCapital: dec("500000"), PreAcqRE: dec("200000"),
			InvestmentCost: dec("620000"), NetIncome: dec(netIncome), SellerIsSubsidiary: true,
		},
		OpeningNetAssets: dec("700000"), ClosingNetAssets: dec("850000"),
	}}}
}

func compute(t *testing.T, in domain.RunInput) domain.RunResult {
	t.Helper()
	res, err := domain.ComputeRun(in)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func start(t *testing.T, s *store.Postgres, ctx context.Context, in domain.RunInput, key string) (store.RunRecord, bool, error) {
	t.Helper()
	return s.CreateRun(ctx, in, compute(t, in), key)
}

func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool, context.Context) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(pool), pool, testkit.Ctx(testkit.TenantID(1))
}

func codeOf(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func outboxTypes(t *testing.T, pool *pgxpool.Pool, ctx context.Context) []map[string]any {
	t.Helper()
	p, _ := tenant.FromContext(ctx)
	rows, err := pool.Query(ctx, `select type, payload->'data' from outbox where tenant_id = $1 order by created_at, id`, p.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var typ string
		var raw []byte
		if err := rows.Scan(&typ, &raw); err != nil {
			t.Fatal(err)
		}
		data := map[string]any{"_type": typ}
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		out = append(out, data)
	}
	return out
}

func TestCreateRun_RoundTripAndEvent(t *testing.T) {
	s, pool, ctx := setup(t)
	in := runInput("G1", "2026-06", "150000")
	rec, replayed, err := start(t, s, ctx, in, "")
	if err != nil || replayed {
		t.Fatalf("create: replayed=%v err=%v", replayed, err)
	}
	if !ids.Valid(rec.ID) || rec.Version != 1 || rec.Status != store.StatusCompleted || rec.CreatedBy != "user-test" ||
		rec.InputsHash == "" || rec.UnitCount != 1 || rec.EliminationCount != 16 || !rec.CTA.Equal(dec("38000")) {
		t.Fatalf("header = %+v", rec)
	}
	got, err := s.GetRun(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Balances) != 6 || len(got.Entries) != 16 || got.GroupID != "G1" || got.Period != "2026-06" || got.Currency != "USD" {
		t.Fatalf("detail = %+v", got.RunRecord)
	}
	if got.Balances[2].Account != domain.AcctTranslationCTA || !got.Balances[2].Amount.Equal(dec("38000")) {
		t.Errorf("CTA balance = %+v", got.Balances[2])
	}
	want := compute(t, in)
	for i, e := range got.Entries {
		w := want.Entries[i]
		if e.UnitID != w.UnitID || e.RuleCode != w.RuleCode || e.Account != w.Account || !e.Debit.Equal(w.Debit) || !e.Credit.Equal(w.Credit) {
			t.Fatalf("entry %d = %+v, want %+v", i, e, want.Entries[i])
		}
	}
	st, err := s.GetStatements(ctx, rec.ID)
	if err != nil || len(st.Lines) != len(want.Statements) {
		t.Fatalf("statements: %d lines, %v", len(st.Lines), err)
	}
	for i, l := range st.Lines {
		if l.Statement != want.Statements[i].Statement || l.Code != want.Statements[i].Code || !l.Amount.Equal(want.Statements[i].Amount) {
			t.Errorf("line %d = %+v, want %+v", i, l, want.Statements[i])
		}
	}
	evts := outboxTypes(t, pool, ctx)
	if len(evts) != 1 || evts[0]["_type"] != "dev.passage.consol.run.completed.v1" || evts[0]["runId"] != rec.ID ||
		evts[0]["groupId"] != "G1" || evts[0]["version"] != float64(1) || evts[0]["ctaAmount"] != "38000" || evts[0]["eliminationCount"] != float64(16) {
		t.Fatalf("outbox = %v", evts)
	}
}

func TestCreateRun_RerunIsANewVersion(t *testing.T) {
	s, _, ctx := setup(t)
	first, _, err := start(t, s, ctx, runInput("G1", "2026-06", "150000"), "")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := start(t, s, ctx, runInput("G1", "2026-06", "160000"), "")
	if err != nil || second.Version != 2 || second.ID == first.ID || second.InputsHash == first.InputsHash {
		t.Fatalf("rerun: %+v err=%v", second, err)
	}
	other, _, err := start(t, s, ctx, runInput("G1", "2026-07", "150000"), "")
	if err != nil || other.Version != 1 {
		t.Fatalf("another period starts at version 1: %+v err=%v", other, err)
	}
	again, err := s.GetRun(ctx, first.ID)
	if err != nil || len(again.Entries) != 16 || !again.CTA.Equal(dec("38000")) {
		t.Fatalf("version 1 changed by the rerun: %+v err=%v", again.RunRecord, err)
	}
	page, err := s.ListRuns(ctx, store.RunFilter{GroupID: "G1", Period: "2026-06"}, "", 50)
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != second.ID {
		t.Fatalf("list = %+v err=%v", page, err)
	}
}

func TestCreateRun_ConcurrentRerunsGetDistinctVersions(t *testing.T) {
	s, _, ctx := setup(t)
	var wg sync.WaitGroup
	versions := make([]int, 6)
	errs := make([]error, 6)
	for i := range versions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec, _, err := s.CreateRun(ctx, runInput("G1", "2026-06", "150000"), compute(t, runInput("G1", "2026-06", "150000")), "")
			versions[i], errs[i] = rec.Version, err
		}(i)
	}
	wg.Wait()
	seen := map[int]bool{}
	for i, v := range versions {
		if errs[i] != nil || seen[v] {
			t.Fatalf("run %d: version %d err=%v (seen %v)", i, v, errs[i], seen)
		}
		seen[v] = true
	}
}

func TestCreateRun_IdempotentReplayAndKeyReuse(t *testing.T) {
	s, pool, ctx := setup(t)
	in := runInput("G1", "2026-06", "150000")
	first, replayed, err := start(t, s, ctx, in, "key-1")
	if err != nil || replayed {
		t.Fatalf("create: replayed=%v err=%v", replayed, err)
	}
	again, replayed, err := start(t, s, ctx, in, "key-1")
	if err != nil || !replayed || again.ID != first.ID || !again.CTA.Equal(first.CTA) || again.Version != 1 {
		t.Fatalf("replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	if _, _, err := start(t, s, ctx, runInput("G1", "2026-06", "160000"), "key-1"); codeOf(err) != domain.CodeIdempotencyReuse {
		t.Fatalf("key reuse with other inputs: got %v, want %s", err, domain.CodeIdempotencyReuse)
	}
	if n := len(outboxTypes(t, pool, ctx)); n != 1 {
		t.Fatalf("a replay published again: %d events", n)
	}
	if _, replayed, err := start(t, s, testkit.Ctx(testkit.TenantID(2)), in, "key-1"); err != nil || replayed {
		t.Fatalf("tenant 2 with the same key: replayed=%v err=%v", replayed, err)
	}
}

func TestGetRun_Missing(t *testing.T) {
	s, _, ctx := setup(t)
	if _, err := s.GetRun(ctx, ids.New()); codeOf(err) != domain.CodeRunNotFound {
		t.Fatalf("missing: got %v", err)
	}
	if _, err := s.GetStatements(ctx, ids.New()); codeOf(err) != domain.CodeRunNotFound {
		t.Fatalf("missing statements: got %v", err)
	}
}

func TestListRuns_FilterAndPages(t *testing.T) {
	s, _, ctx := setup(t)
	for _, p := range [][2]string{{"G1", "2026-05"}, {"G1", "2026-06"}, {"G2", "2026-06"}, {"G1", "2026-06"}} {
		if _, _, err := start(t, s, ctx, runInput(p[0], p[1], "150000"), ""); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListRuns(ctx, store.RunFilter{}, "", 3)
	if err != nil || len(first.Items) != 3 || first.Next == "" {
		t.Fatalf("page 1: %d next=%q err=%v", len(first.Items), first.Next, err)
	}
	second, err := s.ListRuns(ctx, store.RunFilter{}, first.Next, 3)
	if err != nil || len(second.Items) != 1 || second.Next != "" || second.Items[0].Period != "2026-05" {
		t.Fatalf("page 2: %+v err=%v", second, err)
	}
	g1, err := s.ListRuns(ctx, store.RunFilter{GroupID: "G1"}, "", 50)
	if err != nil || len(g1.Items) != 3 {
		t.Fatalf("group G1: %d, %v", len(g1.Items), err)
	}
	june, err := s.ListRuns(ctx, store.RunFilter{GroupID: "G1", Period: "2026-06"}, "", 50)
	if err != nil || len(june.Items) != 2 || june.Items[0].Version != 2 {
		t.Fatalf("G1 June: %+v, %v", june, err)
	}
	if other, err := s.ListRuns(testkit.Ctx(testkit.TenantID(2)), store.RunFilter{}, "", 50); err != nil || len(other.Items) != 0 {
		t.Fatalf("tenant 2 sees %d runs, err=%v", len(other.Items), err)
	}
}

func TestRunData_IsImmutable(t *testing.T) {
	s, pool, ctx := setup(t)
	rec, _, err := start(t, s, ctx, runInput("G1", "2026-06", "150000"), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`update consolidation_run set cta_amount = 0`,
		`delete from consolidation_run`,
		`update consolidation_balance set amount = 0`,
		`delete from consolidation_balance`,
		`update consolidation_entry set debit = 0`,
		`delete from consolidation_entry`,
		`update consolidation_statement_line set amount = 0`,
		`delete from consolidation_statement_line`,
	} {
		err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Errorf("%q = %v; want the immutability trigger (55000)", stmt, err)
		}
	}
	if got, err := s.GetRun(ctx, rec.ID); err != nil || len(got.Entries) != 16 {
		t.Fatalf("run changed: %v", err)
	}
}

func appSetup(t *testing.T) (*store.Postgres, *pgxpool.Pool, context.Context) {
	t.Helper()
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(app), app, testkit.Ctx(testkit.TenantID(1))
}

func TestGrants_AppRoleCannotChangeRunsOrRewriteOutbox(t *testing.T) {
	s, app, ctx := appSetup(t)
	if _, _, err := start(t, s, ctx, runInput("G1", "2026-06", "150000"), "grants"); err != nil {
		t.Fatalf("create as the app role: %v", err)
	}
	for _, stmt := range []string{
		`select count(*) from outbox`,
		`update outbox set type = type`,
		`update consolidation_run set cta_amount = 0`,
		`delete from consolidation_run`,
		`delete from consolidation_entry`,
		`delete from idempotency_keys`,
	} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}

func TestStore_RowLevelSecurity(t *testing.T) {
	s, app, one := appSetup(t)
	two := testkit.Ctx(testkit.TenantID(2))
	for _, table := range []string{"consolidation_run", "consolidation_balance", "consolidation_entry", "consolidation_statement_line", "outbox", "idempotency_keys"} {
		var enabled, forced bool
		if err := app.QueryRow(context.Background(), `select relrowsecurity, relforcerowsecurity from pg_class where relname = $1 and relnamespace = current_schema()::regnamespace`, table).Scan(&enabled, &forced); err != nil {
			t.Fatal(table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v", table, enabled, forced)
		}
	}
	rec, _, err := start(t, s, one, runInput("G1", "2026-06", "150000"), "rls")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRun(two, rec.ID); codeOf(err) != domain.CodeRunNotFound {
		t.Fatalf("tenant 2 reads tenant 1's run: %v", err)
	}
	if _, err := s.GetStatements(two, rec.ID); codeOf(err) != domain.CodeRunNotFound {
		t.Fatalf("tenant 2 reads tenant 1's statements: %v", err)
	}
	if page, err := s.ListRuns(two, store.RunFilter{}, "", 50); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 lists %d runs, err=%v", len(page.Items), err)
	}
	again, _, err := start(t, s, two, runInput("G1", "2026-06", "150000"), "rls")
	if err != nil || again.Version != 1 || again.ID == rec.ID {
		t.Fatalf("tenant 2 with tenant 1's key and group: %+v, %v", again, err)
	}
}
