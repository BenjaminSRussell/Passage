package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
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
