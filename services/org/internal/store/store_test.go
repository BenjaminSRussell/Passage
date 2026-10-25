package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func principalContext(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n)})
}

func newStore(t *testing.T, n int) (*store.Postgres, context.Context) {
	pool := testkit.DB(t)
	ctx := principalContext(n)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, pool, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	return store.New(pool), ctx
}

func TestPostgres_TenantIsolationIdempotencyAndDuplicateCode(t *testing.T) {
	s, ctx := newStore(t, 1)
	in := store.LegalEntity{Code: "US01", Name: "Passage US", Country: "US", Currency: "USD", TimeZone: "UTC"}

	first, err := s.CreateLegalEntity(ctx, in, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateLegalEntity(ctx, in, "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("idempotent replay must return the original entity, got %+v, %v", replay, err)
	}
	var problem *httpx.Problem
	if _, err := s.CreateLegalEntity(ctx, store.LegalEntity{Code: "US02", Name: "Other", Country: "US", Currency: "USD", TimeZone: "UTC"}, "key-1"); !errors.As(err, &problem) || problem.Code != store.CodeIdempotencyReuse {
		t.Fatalf("reused key with different body must be %s, got %v", store.CodeIdempotencyReuse, err)
	}
	if _, err := s.CreateLegalEntity(ctx, in, "key-2"); !errors.As(err, &problem) || problem.Code != store.CodeLegalEntityCode {
		t.Fatalf("duplicate code must be %s, got %v", store.CodeLegalEntityCode, err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	own, err := s.ListLegalEntities(ctx, store.LegalEntityFilter{}, "", store.MaxPageSize)
	if err != nil || len(own.Items) != 1 {
		t.Fatalf("tenant 1 list = %v, %v; want 1 entity", own, err)
	}
	other, err := s.ListLegalEntities(principalContext(2), store.LegalEntityFilter{}, "", store.MaxPageSize)
	if err != nil || len(other.Items) != 0 {
		t.Fatalf("tenant 2 must see no rows, got %v, %v", other, err)
	}
}

func TestPostgres_ListPagesPastFirstPage(t *testing.T) {
	s, ctx := newStore(t, 3)
	for _, code := range []string{"E1", "E2", "E3"} {
		if _, err := s.CreateLegalEntity(ctx, store.LegalEntity{Code: code, Name: code, Country: "US", Currency: "USD", TimeZone: "UTC"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListLegalEntities(ctx, store.LegalEntityFilter{}, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextCode != "E2" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := s.ListLegalEntities(ctx, store.LegalEntityFilter{}, first.NextCode, 2)
	if err != nil || len(second.Items) != 1 || second.Items[0].Code != "E3" || second.NextCode != "" {
		t.Fatalf("second page = %+v, %v", second, err)
	}
}

type outboxRow struct {
	Type    string
	Payload []byte
}

func outboxRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []outboxRow {
	t.Helper()
	var rows []outboxRow
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `select type, payload from outbox order by created_at`)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var row outboxRow
			if err := r.Scan(&row.Type, &row.Payload); err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func legalEntityCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from legal_entity`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgres_CreateLegalEntityEnqueuesInSameTx(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalContext(4)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, pool, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	in := store.LegalEntity{Code: "US01", Name: "Passage US", Country: "US", Currency: "USD", TimeZone: "America/Chicago"}

	created, err := s.CreateLegalEntity(ctx, in, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	rows := outboxRows(t, ctx, pool)
	if len(rows) != 1 || rows[0].Type != "dev.passage.org.legalentity.created.v1" {
		t.Fatalf("outbox rows = %+v; want one legalentity.created", rows)
	}
	var evt struct {
		Subject string
		Data    map[string]string
	}
	if err := json.Unmarshal(rows[0].Payload, &evt); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"id": created.ID, "code": "US01", "name": "Passage US", "country_code": "US", "accounting_currency": "USD", "time_zone": "America/Chicago"}
	if evt.Subject != created.ID || len(evt.Data) != len(want) {
		t.Fatalf("event = %+v; want subject %s and data %v", evt, created.ID, want)
	}
	for k, v := range want {
		if evt.Data[k] != v {
			t.Fatalf("data[%s] = %q; want %q", k, evt.Data[k], v)
		}
	}

	if _, err := s.CreateLegalEntity(ctx, in, "key-1"); err != nil {
		t.Fatal(err)
	}
	var problem *httpx.Problem
	changed := in
	changed.TimeZone = "UTC"
	if _, err := s.CreateLegalEntity(ctx, changed, "key-1"); !errors.As(err, &problem) || problem.Code != store.CodeIdempotencyReuse {
		t.Fatalf("reused key with another time zone must be %s, got %v", store.CodeIdempotencyReuse, err)
	}
	if got := len(outboxRows(t, ctx, pool)); got != 1 {
		t.Fatalf("replay and key reuse must not enqueue; outbox rows = %d", got)
	}
}

func TestPostgres_CreateLegalEntityRollsBackWhenOutboxFails(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalContext(5)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, pool, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `alter table outbox add constraint outbox_reject check (false)`); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	if _, err := s.CreateLegalEntity(ctx, store.LegalEntity{Code: "US01", Name: "n", Country: "US", Currency: "USD", TimeZone: "UTC"}, ""); err == nil {
		t.Fatal("create must fail when the outbox insert fails")
	}
	if n := legalEntityCount(t, ctx, pool); n != 0 {
		t.Fatalf("legal entities after failed outbox = %d; want 0", n)
	}
	if _, err := pool.Exec(ctx, `alter table outbox drop constraint outbox_reject`); err != nil {
		t.Fatal(err)
	}
	if rows := outboxRows(t, ctx, pool); len(rows) != 0 {
		t.Fatalf("outbox rows after failed create = %d; want 0", len(rows))
	}
}

func TestPostgres_OutboxIsTenantIsolated(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctxA := principalContext(6)
	if err := dbx.Migrate(ctxA, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctxA, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(app).CreateLegalEntity(ctxA, store.LegalEntity{Code: "US01", Name: "n", Country: "US", Currency: "USD", TimeZone: "UTC"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := len(outboxRows(t, ctxA, app)); got != 1 {
		t.Fatalf("tenant A outbox rows = %d; want 1", got)
	}
	if got := len(outboxRows(t, principalContext(7), app)); got != 0 {
		t.Fatalf("tenant B must see no outbox rows, got %d", got)
	}
}
