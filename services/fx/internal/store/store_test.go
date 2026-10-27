package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fx/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
	"github.com/BenjaminSRussell/passage/services/fx/migrations"
)

var valid = time.Now().UTC().AddDate(0, 0, -3)

func in(source, rate string) store.RateInput {
	return store.RateInput{RateType: "CLOSING", From: "EUR", To: "USD", ValidFrom: valid, Rate: decimal.RequireFromString(rate), Source: source}
}

func ctxFor(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: "user-test"})
}

func migrated(t *testing.T) (*store.Postgres, *pgxpool.Pool, context.Context) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := ctxFor(1)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	if _, err := s.PutRateType(ctx, store.RateType{Code: "CLOSING", Name: "Closing", AllowInverse: true}); err != nil {
		t.Fatal(err)
	}
	return s, pool, ctx
}

func TestPriorityOrder(t *testing.T) {
	order := []string{"ecb", "openexchangerates", "csv", "manual"}
	last := 0
	for _, src := range order {
		p, ok := store.Priority(src)
		if !ok || p <= last {
			t.Fatalf("%s priority %d must exceed %d", src, p, last)
		}
		last = p
	}
	if m, _ := store.Priority("migration"); m != last {
		t.Fatalf("migration priority = %d, want %d", m, last)
	}
	if _, ok := store.Priority("unknown"); ok {
		t.Fatal("unknown source must have no priority")
	}
}

func TestPostRatesRejectsInvalidInputBeforeAnyQuery(t *testing.T) {
	s := store.New(nil)
	future := in("manual", "1.1")
	future.ValidFrom = time.Now().AddDate(0, 0, 5)
	hash := in("manual", "1.1")
	hash.PayloadHash = []byte{1, 2}
	cases := map[string]struct {
		batch []store.RateInput
		want  error
	}{
		"empty batch":    {nil, domain.ErrInvalidRate},
		"zero rate":      {[]store.RateInput{in("manual", "0")}, domain.ErrInvalidRate},
		"negative rate":  {[]store.RateInput{in("manual", "-1")}, domain.ErrInvalidRate},
		"eleven decimal": {[]store.RateInput{in("manual", "1.12345678901")}, domain.ErrInvalidRate},
		"unknown source": {[]store.RateInput{in("bogus", "1.1")}, domain.ErrInvalidRate},
		"same currency":  {[]store.RateInput{{RateType: "CLOSING", From: "EUR", To: "EUR", ValidFrom: valid, Rate: decimal.NewFromInt(1), Source: "manual"}}, domain.ErrInvalidRate},
		"lowercase":      {[]store.RateInput{{RateType: "CLOSING", From: "eur", To: "USD", ValidFrom: valid, Rate: decimal.NewFromInt(1), Source: "manual"}}, domain.ErrInvalidRate},
		"zero date":      {[]store.RateInput{{RateType: "CLOSING", From: "EUR", To: "USD", Rate: decimal.NewFromInt(1), Source: "manual"}}, domain.ErrInvalidRate},
		"future date":    {[]store.RateInput{future}, domain.ErrFutureRate},
		"bad hash":       {[]store.RateInput{hash}, domain.ErrInvalidRate},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.PostRates(ctxFor(1), c.batch); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func count(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostRatesCorrectionAndPrecedence(t *testing.T) {
	s, _, ctx := migrated(t)
	first, err := s.PostRates(ctx, []store.RateInput{in("ecb", "1.1000000000")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PostRates(ctx, []store.RateInput{in("ecb", "1.1100000000")})
	if err != nil {
		t.Fatal(err)
	}
	hist, err := s.History(ctx, "CLOSING", "EUR", "USD")
	if err != nil || len(hist) != 2 {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	if hist[0].ID != first[0].ID || hist[0].SupersededBy != second[0].ID || hist[1].SupersededBy != "" || hist[1].Version != 2 {
		t.Fatalf("correction chain wrong: %+v", hist)
	}
	manual, err := s.PostRates(ctx, []store.RateInput{in("manual", "1.2000000000")})
	if err != nil {
		t.Fatal(err)
	}
	lower, err := s.PostRates(ctx, []store.RateInput{in("csv", "1.3000000000")})
	if err != nil || !lower[0].Superseded {
		t.Fatalf("lower priority must arrive superseded: %+v, %v", lower, err)
	}
	hist, _ = s.History(ctx, "CLOSING", "EUR", "USD")
	if len(hist) != 4 || hist[2].ID != manual[0].ID || hist[2].SupersededBy != "" || hist[3].SupersededBy != manual[0].ID || hist[1].SupersededBy != manual[0].ID {
		t.Fatalf("precedence chain wrong: %+v", hist)
	}
	book, err := s.CurrentBook(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	res, err := book.Resolve("CLOSING", "EUR", "USD", time.Now(), "")
	if err != nil || !res.Factors[0].Equal(decimal.RequireFromString("1.2")) {
		t.Fatalf("resolve = %+v, %v", res, err)
	}
	prev, ok, err := s.Previous(ctx, "CLOSING", "EUR", "USD", time.Now())
	if err != nil || !ok || !prev.Value.Equal(decimal.RequireFromString("1.2")) {
		t.Fatalf("previous = %+v ok=%v err=%v", prev, ok, err)
	}
	if _, ok, err := s.Previous(ctx, "CLOSING", "EUR", "CHF", time.Now()); ok || err != nil {
		t.Fatalf("previous for unknown pair must be absent: ok=%v err=%v", ok, err)
	}
}

func TestPostRatesBatchAtomicityAndOutboxReconciliation(t *testing.T) {
	s, pool, ctx := migrated(t)
	bad := in("manual", "1.5")
	bad.RateType = "NOPE"
	if _, err := s.PostRates(ctx, []store.RateInput{in("manual", "1.4"), bad}); !errors.Is(err, domain.ErrInvalidRate) {
		t.Fatalf("err = %v", err)
	}
	if n := count(t, ctx, pool, `select count(*) from exchange_rate`) + count(t, ctx, pool, `select count(*) from outbox`); n != 0 {
		t.Fatalf("failed batch left %d rows", n)
	}
	batch := []store.RateInput{in("ecb", "1.1"), in("manual", "1.2"), in("csv", "1.3")}
	batch[1].To = "CHF"
	batch[2].To = "GBP"
	batch[2].DeviationPct = "12.00"
	if _, err := s.PostRates(ctx, batch); err != nil {
		t.Fatal(err)
	}
	rates := count(t, ctx, pool, `select count(*) from exchange_rate`)
	published := count(t, ctx, pool, `select count(*) from outbox where type = 'dev.passage.fx.rate.published.v1'`)
	if rates != 3 || published != rates {
		t.Fatalf("rates=%d events=%d", rates, published)
	}
	if n := count(t, ctx, pool, `select count(*) from outbox where payload->'data'->>'deviation_pct' = '12.00'`); n != 1 {
		t.Fatalf("deviation_pct events = %d, want 1", n)
	}
}

func TestCurrentBookSkipsStoredRateTheBookRejects(t *testing.T) {
	s, pool, ctx := migrated(t)
	if _, err := s.PostRates(ctx, []store.RateInput{in("manual", "1.1")}); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `insert into exchange_rate (tenant_id, id, rate_type_id, from_currency, to_currency, valid_from, rate, source)
		select tenant_id, $1::uuid, id, 'GBP', 'USD', current_date - 3, 5000000000, 'manual' from exchange_rate_type where code = 'CLOSING'`, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	book, err := s.CurrentBook(ctx, time.Now())
	if err != nil {
		t.Fatalf("one oversized stored rate must not fail the book: %v", err)
	}
	if _, err := book.Resolve("CLOSING", "EUR", "USD", time.Now(), ""); err != nil {
		t.Fatalf("valid rate lost: %v", err)
	}
	if _, err := book.Resolve("CLOSING", "GBP", "USD", time.Now(), ""); !errors.Is(err, domain.ErrRateMissing) {
		t.Fatalf("oversized rate must be skipped: %v", err)
	}
}

func TestPutRateTypeIsIdempotent(t *testing.T) {
	s, _, ctx := migrated(t)
	again, err := s.PutRateType(ctx, store.RateType{Code: "CLOSING", Name: "Closing rate", AllowInverse: false})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListRateTypes(ctx)
	if err != nil || len(list) != 1 || list[0].ID != again.ID || list[0].Name != "Closing rate" || list[0].AllowInverse {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := s.GetRateType(ctx, "MISSING"); err == nil {
		t.Fatal("unknown rate type must error")
	}
	if _, err := s.PutRateType(ctx, store.RateType{Code: "", Name: "x"}); !errors.Is(err, domain.ErrInvalidRate) {
		t.Fatalf("empty code err = %v", err)
	}
}

func TestRLSHidesOtherTenantRates(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := ctxFor(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "grant usage on schema "+schema+" to passage_app; grant select, insert, update, delete on all tables in schema "+schema+" to passage_app"); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	if _, err := s.PutRateType(ctx, store.RateType{Code: "CLOSING", Name: "Closing", AllowInverse: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostRates(ctx, []store.RateInput{in("manual", "1.1")}); err != nil {
		t.Fatal(err)
	}
	other := ctxFor(2)
	types, err := s.ListRateTypes(other)
	if err != nil || len(types) != 0 {
		t.Fatalf("tenant 2 rate types = %+v, %v", types, err)
	}
	hist, err := s.History(other, "CLOSING", "EUR", "USD")
	if err != nil || len(hist) != 0 {
		t.Fatalf("tenant 2 history = %+v, %v", hist, err)
	}
	book, err := s.CurrentBook(other, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := book.Resolve("CLOSING", "EUR", "USD", time.Now(), ""); !errors.Is(err, domain.ErrRateMissing) {
		t.Fatalf("tenant 2 resolve err = %v", err)
	}
	if _, err := s.PostRates(other, []store.RateInput{in("manual", "1.1")}); !errors.Is(err, domain.ErrInvalidRate) {
		t.Fatalf("tenant 2 must not post against tenant 1 rate type: %v", err)
	}
}

func TestPostRatesStoresValidFromAsUTCDate(t *testing.T) {
	s, pool, ctx := migrated(t)
	now := time.Now().UTC()
	offset := in("manual", "1.1")
	offset.ValidFrom = time.Date(now.Year(), now.Month(), now.Day(), 23, 0, 0, 0, time.UTC).In(time.FixedZone("plus14", 14*3600))
	if _, err := s.PostRates(ctx, []store.RateInput{offset}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, ctx, pool, `select count(*) from exchange_rate where valid_from = (now() at time zone 'utc')::date`); n != 1 {
		t.Fatalf("rows stored at UTC today = %d, want 1", n)
	}
}

func TestDeviationPct(t *testing.T) {
	cases := []struct{ previous, next, want string }{
		{"1", "1.12", "12.00"},
		{"1", "0.88", "12.00"},
		{"3", "3.001", "0.03"},
		{"8", "8.01", "0.13"},
		{"2", "2", "0.00"},
		{"0", "1", ""},
	}
	for _, c := range cases {
		if got := store.DeviationPct(decimal.RequireFromString(c.previous), decimal.RequireFromString(c.next)); got != c.want {
			t.Errorf("DeviationPct(%s, %s) = %q, want %q", c.previous, c.next, got, c.want)
		}
	}
}

func TestRunLog(t *testing.T) {
	s, pool, ctx := migrated(t)
	run, done, err := s.StartRun(ctx, "ecb", "CLOSING", valid)
	if err != nil || done || run.Status != store.RunRunning || run.RateType != "CLOSING" || run.Provider != "ecb" {
		t.Fatalf("start = %+v done=%v err=%v", run, done, err)
	}
	if err := s.RecordAttempt(ctx, run.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRunSucceeded(ctx, run.ID, 5, 3); err != nil {
		t.Fatal(err)
	}
	again, done, err := s.StartRun(ctx, "ecb", "CLOSING", valid.Add(5*time.Hour))
	if err != nil || !done || again.ID != run.ID || again.Status != store.RunSucceeded || again.PairsImported != 5 || again.PairsUnchanged != 3 || again.Attempts != 2 || again.LastSuccessAt == nil {
		t.Fatalf("second start = %+v done=%v err=%v", again, done, err)
	}
	if n := count(t, ctx, pool, `select count(*) from provider_import_run`); n != 1 {
		t.Fatalf("runs = %d, want 1", n)
	}
	if at, err := s.LastSuccess(ctx, "ecb", "CLOSING"); err != nil || at == nil {
		t.Fatalf("last success = %v, %v", at, err)
	}
	if at, err := s.LastSuccess(ctx, "csv", "CLOSING"); err != nil || at != nil {
		t.Fatalf("last success for other provider = %v, %v", at, err)
	}
	for provider, want := range map[string]int{"ecb": 1, "": 1, "csv": 0} {
		if runs, err := s.ListRuns(ctx, provider); err != nil || len(runs) != want {
			t.Fatalf("runs for %q = %+v, %v", provider, runs, err)
		}
	}
	if _, _, err := s.StartRun(ctx, "ecb", "NOPE", valid); !errors.Is(err, domain.ErrInvalidRate) {
		t.Fatalf("unknown rate type err = %v", err)
	}
	if _, _, err := s.StartRun(ctx, "", "CLOSING", valid); !errors.Is(err, domain.ErrInvalidRate) {
		t.Fatalf("empty provider err = %v", err)
	}
}

func TestListRunsNewestFirst(t *testing.T) {
	s, _, ctx := migrated(t)
	older, _, err := s.StartRun(ctx, "ecb", "CLOSING", valid.AddDate(0, 0, -1))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	newer, _, err := s.StartRun(ctx, "ecb", "CLOSING", valid)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRuns(ctx, "ecb")
	if err != nil || len(runs) != 2 || runs[0].ID != newer.ID || runs[1].ID != older.ID {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
}

func TestFinishRunFailedEnqueuesDegradedEventAndAllowsRestart(t *testing.T) {
	s, pool, ctx := migrated(t)
	if _, err := s.PostRates(ctx, []store.RateInput{in("ecb", "1.1")}); err != nil {
		t.Fatal(err)
	}
	rates := count(t, ctx, pool, `select count(*) from exchange_rate`)
	run, _, err := s.StartRun(ctx, "ecb", "CLOSING", valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRunFailed(ctx, run.ID, 4, errors.New("ecb: request failed: timeout"), true); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRuns(ctx, "ecb")
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunFailed || runs[0].Attempts != 4 || runs[0].Error != "ecb: request failed: timeout" || runs[0].FinishedAt == nil {
		t.Fatalf("failed run = %+v, %v", runs, err)
	}
	if n := count(t, ctx, pool, `select count(*) from outbox where type = 'dev.passage.fx.provider.degraded.v1' and aggregate_type = 'provider_import_run' and aggregate_id = '`+run.ID+`'`); n != 1 {
		t.Fatalf("degraded events = %d, want 1", n)
	}
	if n := count(t, ctx, pool, `select count(*) from outbox where payload->'data'->>'provider' = 'ecb' and payload->'data'->>'rate_type' = 'CLOSING'
		and payload->'data'->>'error' = 'ecb: request failed: timeout' and payload->'data'->'last_success_at' = 'null'::jsonb`); n != 1 {
		t.Fatalf("degraded payload rows = %d, want 1", n)
	}
	if n := count(t, ctx, pool, `select count(*) from outbox where aggregate_id = '`+run.ID+`' and payload->'data'->>'fallback' = 'none' and payload->'data'->>'retryable' = 'true' and payload->'data'->>'status' = 'failed'`); n != 1 {
		t.Fatalf("failed fallback rows = %d, want 1", n)
	}
	if got := count(t, ctx, pool, `select count(*) from exchange_rate`); got != rates {
		t.Fatalf("rates changed from %d to %d", rates, got)
	}
	restarted, done, err := s.StartRun(ctx, "ecb", "CLOSING", valid)
	if err != nil || done || restarted.ID != run.ID || restarted.Status != store.RunRunning || restarted.Attempts != 0 || restarted.Error != "" || restarted.FinishedAt != nil {
		t.Fatalf("restart = %+v done=%v err=%v", restarted, done, err)
	}
	if err := s.FinishRunSucceeded(ctx, run.ID, 1, 0); err != nil {
		t.Fatal(err)
	}
	next, _, err := s.StartRun(ctx, "ecb", "CLOSING", valid.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRunFailed(ctx, next.ID, 4, errors.New("down"), true); err != nil {
		t.Fatal(err)
	}
	if n := count(t, ctx, pool, `select count(*) from outbox where type = 'dev.passage.fx.provider.degraded.v1' and payload->'data'->>'last_success_at' is not null`); n != 1 {
		t.Fatalf("degraded events carrying last_success_at = %d, want 1", n)
	}
	missing := ids.New()
	if err := s.FinishRunFailed(ctx, missing, 1, errors.New("x"), true); !errors.Is(err, store.ErrRunNotFound) {
		t.Fatalf("failed unknown run err = %v", err)
	}
	if err := s.FinishRunSucceeded(ctx, missing, 1, 0); !errors.Is(err, store.ErrRunNotFound) {
		t.Fatalf("succeeded unknown run err = %v", err)
	}
	if err := s.RecordAttempt(ctx, missing, 1); !errors.Is(err, store.ErrRunNotFound) {
		t.Fatalf("attempt unknown run err = %v", err)
	}
}

func TestFinishRunDegradedMarksRunAndEnqueuesDegradedEvent(t *testing.T) {
	s, pool, ctx := migrated(t)
	ok, _, err := s.StartRun(ctx, "ecb", "CLOSING", valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRunSucceeded(ctx, ok.ID, 1, 0); err != nil {
		t.Fatal(err)
	}
	run, _, err := s.StartRun(ctx, "ecb", "CLOSING", valid.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRunDegraded(ctx, run.ID, 4, errors.New("ecb: unexpected status 503"), true); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRuns(ctx, "ecb")
	if err != nil || len(runs) != 2 || runs[0].Status != store.RunDegraded || runs[0].Attempts != 4 || runs[0].FinishedAt == nil {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	if n := count(t, ctx, pool, `select count(*) from outbox where type = 'dev.passage.fx.provider.degraded.v1' and aggregate_id = '`+run.ID+`'
		and payload->'data'->>'last_success_at' is not null and payload->'data'->>'fallback' = 'last_known' and payload->'data'->>'retryable' = 'true' and payload->'data'->>'status' = 'degraded'`); n != 1 {
		t.Fatalf("degraded events = %d, want 1", n)
	}
	restarted, done, err := s.StartRun(ctx, "ecb", "CLOSING", valid.AddDate(0, 0, 1))
	if err != nil || done || restarted.Status != store.RunRunning {
		t.Fatalf("restart = %+v done=%v err=%v", restarted, done, err)
	}
}

func TestFinishRunAbortedMarksFailedWithoutDegradedEvent(t *testing.T) {
	s, pool, ctx := migrated(t)
	run, _, err := s.StartRun(ctx, "ecb", "CLOSING", valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRunAborted(ctx, run.ID, 2, errors.New("context canceled")); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRuns(ctx, "ecb")
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunFailed || runs[0].Attempts != 2 || runs[0].Error != "context canceled" || runs[0].FinishedAt == nil {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	if n := count(t, ctx, pool, `select count(*) from outbox where type = 'dev.passage.fx.provider.degraded.v1'`); n != 0 {
		t.Fatalf("degraded events = %d, want 0", n)
	}
	if err := s.FinishRunAborted(ctx, ids.New(), 1, errors.New("x")); !errors.Is(err, store.ErrRunNotFound) {
		t.Fatalf("aborted unknown run err = %v", err)
	}
}

func TestPostRatesReportsUnchanged(t *testing.T) {
	s, pool, ctx := migrated(t)
	first, err := s.PostRates(ctx, []store.RateInput{in("ecb", "1.1")})
	if err != nil || first[0].Unchanged {
		t.Fatalf("first = %+v, %v", first, err)
	}
	again, err := s.PostRates(ctx, []store.RateInput{in("ecb", "1.1000000000")})
	if err != nil || !again[0].Unchanged || again[0].ID != first[0].ID || again[0].Version != 1 {
		t.Fatalf("identical repost = %+v, %v", again, err)
	}
	if rates, events := count(t, ctx, pool, `select count(*) from exchange_rate`), count(t, ctx, pool, `select count(*) from outbox`); rates != 1 || events != 1 {
		t.Fatalf("rates=%d events=%d after identical repost, want 1 and 1", rates, events)
	}
	changed, err := s.PostRates(ctx, []store.RateInput{in("ecb", "1.2")})
	if err != nil || changed[0].Unchanged || changed[0].Version != 2 {
		t.Fatalf("changed = %+v, %v", changed, err)
	}
	otherSource, err := s.PostRates(ctx, []store.RateInput{in("manual", "1.2")})
	if err != nil || otherSource[0].Unchanged || otherSource[0].Version != 3 {
		t.Fatalf("same rate from another source = %+v, %v", otherSource, err)
	}
	fresh := in("manual", "1.0")
	fresh.To = "CHF"
	mixed, err := s.PostRates(ctx, []store.RateInput{in("manual", "1.2"), fresh})
	if err != nil || !mixed[0].Unchanged || mixed[1].Unchanged {
		t.Fatalf("mixed batch = %+v, %v", mixed, err)
	}
	rates := count(t, ctx, pool, `select count(*) from exchange_rate`)
	published := count(t, ctx, pool, `select count(*) from outbox where type = 'dev.passage.fx.rate.published.v1'`)
	if rates != 4 || published != rates {
		t.Fatalf("rates=%d published=%d, want 4 and equal", rates, published)
	}
}

func TestRatePublishedCarriesPreviousRateAndDeviation(t *testing.T) {
	s, pool, ctx := migrated(t)
	if _, err := s.PostRates(ctx, []store.RateInput{in("ecb", "1.0")}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, ctx, pool, `select count(*) from outbox where payload->'data'->'previous_rate' = 'null'::jsonb and payload->'data'->>'deviation_pct' is null`); n != 1 {
		t.Fatalf("first event with null previous_rate and no deviation = %d, want 1", n)
	}
	if _, err := s.PostRates(ctx, []store.RateInput{in("ecb", "1.12")}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, ctx, pool, `select count(*) from outbox where payload->'data'->>'rate' = '1.12'
		and payload->'data'->>'previous_rate' = '1' and payload->'data'->>'deviation_pct' = '12.00'`); n != 1 {
		t.Fatalf("correction events with previous_rate and computed deviation = %d, want 1", n)
	}
	override := in("ecb", "1.5")
	override.DeviationPct = "50.00"
	if _, err := s.PostRates(ctx, []store.RateInput{override}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, ctx, pool, `select count(*) from outbox where payload->'data'->>'rate' = '1.5'
		and payload->'data'->>'previous_rate' = '1.12' and payload->'data'->>'deviation_pct' = '50.00'`); n != 1 {
		t.Fatalf("caller deviation override events = %d, want 1", n)
	}
	later := in("ecb", "1.14")
	later.ValidFrom = valid.AddDate(0, 0, 1)
	if _, err := s.PostRates(ctx, []store.RateInput{later}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, ctx, pool, `select count(*) from outbox where payload->'data'->>'rate' = '1.14' and payload->'data'->>'previous_rate' = '1.5'`); n != 1 {
		t.Fatalf("next-day event previous_rate = %d, want 1", n)
	}
}

func TestIdempotencyRoundTrip(t *testing.T) {
	s, _, ctx := migrated(t)
	if _, ok, err := s.GetIdempotency(ctx, "k1"); ok || err != nil {
		t.Fatalf("unknown key ok=%v err=%v", ok, err)
	}
	if err := s.PutIdempotency(ctx, store.IdempotencyRecord{Key: "k1", RequestHash: "h1", ResponseStatus: 200, ResponseBody: json.RawMessage(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIdempotency(ctx, store.IdempotencyRecord{Key: "k1", RequestHash: "h2", ResponseStatus: 500, ResponseBody: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := s.GetIdempotency(ctx, "k1")
	var body map[string]int
	if err != nil || !ok || rec.RequestHash != "h1" || rec.ResponseStatus != 200 || json.Unmarshal(rec.ResponseBody, &body) != nil || body["a"] != 1 {
		t.Fatalf("stored = %+v ok=%v err=%v", rec, ok, err)
	}
	if _, _, err := s.GetIdempotency(ctx, ""); !errors.Is(err, domain.ErrInvalidRate) {
		t.Fatalf("empty key get err = %v", err)
	}
	if err := s.PutIdempotency(ctx, store.IdempotencyRecord{RequestHash: "h"}); !errors.Is(err, domain.ErrInvalidRate) {
		t.Fatalf("empty key put err = %v", err)
	}
	if err := s.PutIdempotency(ctx, store.IdempotencyRecord{Key: "k2", RequestHash: "h", ResponseStatus: 200, ResponseBody: json.RawMessage(`not json`)}); err == nil {
		t.Fatal("non-JSON response body must be rejected")
	}
}

func TestRLSHidesOtherTenantRunsAndIdempotencyKeys(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := ctxFor(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "grant usage on schema "+schema+" to passage_app; grant select, insert, update, delete on all tables in schema "+schema+" to passage_app"); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	if _, err := s.PutRateType(ctx, store.RateType{Code: "CLOSING", Name: "Closing", AllowInverse: true}); err != nil {
		t.Fatal(err)
	}
	run, _, err := s.StartRun(ctx, "ecb", "CLOSING", valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRunFailed(ctx, run.ID, 4, errors.New("down"), true); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIdempotency(ctx, store.IdempotencyRecord{Key: "k1", RequestHash: "h1", ResponseStatus: 200, ResponseBody: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	other := ctxFor(2)
	if runs, err := s.ListRuns(other, ""); err != nil || len(runs) != 0 {
		t.Fatalf("tenant 2 runs = %+v, %v", runs, err)
	}
	if at, err := s.LastSuccess(other, "ecb", "CLOSING"); err != nil || at != nil {
		t.Fatalf("tenant 2 last success = %v, %v", at, err)
	}
	if _, _, err := s.StartRun(other, "ecb", "CLOSING", valid); !errors.Is(err, domain.ErrInvalidRate) {
		t.Fatalf("tenant 2 must not start a run on tenant 1 rate type: %v", err)
	}
	if err := s.FinishRunFailed(other, run.ID, 1, errors.New("x"), true); !errors.Is(err, store.ErrRunNotFound) {
		t.Fatalf("tenant 2 must not finish tenant 1 run: %v", err)
	}
	if _, ok, err := s.GetIdempotency(other, "k1"); ok || err != nil {
		t.Fatalf("tenant 2 idempotency ok=%v err=%v", ok, err)
	}
	if err := s.PutIdempotency(other, store.IdempotencyRecord{Key: "k1", RequestHash: "h2", ResponseStatus: 201, ResponseBody: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if rec, ok, err := s.GetIdempotency(ctx, "k1"); err != nil || !ok || rec.RequestHash != "h1" {
		t.Fatalf("tenant 1 key was overwritten: %+v ok=%v err=%v", rec, ok, err)
	}
}
