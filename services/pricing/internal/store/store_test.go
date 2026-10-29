package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
	"github.com/BenjaminSRussell/passage/services/pricing/migrations"
)

const (
	legalEntity      = "00000000-0000-0000-0001-000000000001"
	otherLegalEntity = "00000000-0000-0000-0001-000000000002"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func entry(from, to string) store.Entry {
	return store.Entry{LegalEntityID: legalEntity, Record: domain.Record{Type: domain.TypePrice, CustomerKind: domain.CustomerKindCustomer,
		CustomerKey: "C1", ProductKind: domain.ProductKindProduct, ProductKey: "P1", Currency: "USD", Unit: "EA",
		FromQuantity: decimal.Zero, Amount: decimal.RequireFromString("95"), ValidFrom: day(from), ValidTo: day(to)}}
}

func principal(n int, legalEntityID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: legalEntityID})
}

// appPool migrates a fresh schema as the admin role and returns a pool for the
// non-superuser application role, so row-level security is really enforced.
func appPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	_, app := adminAndAppPools(t)
	return app
}

func adminAndAppPools(t *testing.T) (admin, app *pgxpool.Pool) {
	t.Helper()
	admin, app = testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"grant usage on schema " + schema + " to passage_app",
		"grant select, insert, update, delete on all tables in schema " + schema + " to passage_app",
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	return admin, app
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

type outboxRow struct {
	Type string
	Data map[string]any
}

func outboxRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []outboxRow {
	t.Helper()
	var out []outboxRow
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select type, payload from outbox order by created_at, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ string
			var payload []byte
			if err := rows.Scan(&typ, &payload); err != nil {
				return err
			}
			var evt struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(payload, &evt); err != nil {
				return err
			}
			out = append(out, outboxRow{Type: typ, Data: evt.Data})
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPostgres_ActivateRejectsOverlapAndWritesEvents(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	first, err := s.CreateRecord(ctx, entry("2026-01-01", "2026-06-30"), "k1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateRecord(ctx, entry("2026-06-30", "2026-12-31"), "k2")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != domain.StatusDraft || first.Version != 1 {
		t.Fatalf("new record = %+v; want DRAFT version 1", first)
	}
	active, err := s.ActivateRecord(ctx, first.ID, 1, "act-1")
	if err != nil || active.Status != domain.StatusActive || active.Version != 2 {
		t.Fatalf("activate = %+v, %v", active, err)
	}
	// A retry with the same key replays the first response instead of failing on the version.
	replay, err := s.ActivateRecord(ctx, first.ID, 1, "act-1")
	if err != nil || replay.ID != first.ID || replay.Version != 2 || replay.Status != domain.StatusActive {
		t.Fatalf("replayed activate = %+v, %v; want the original response", replay, err)
	}
	if _, err := s.ActivateRecord(ctx, first.ID, 1, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version without a key = %v; want %s", err, store.CodeVersionConflict)
	}
	_, err = s.ActivateRecord(ctx, second.ID, 1, "act-2")
	var problem *httpx.Problem
	if !errors.As(err, &problem) || problem.Code != domain.CodeOverlap || problem.Status != http.StatusConflict {
		t.Fatalf("overlap must be 409 %s, got %v", domain.CodeOverlap, err)
	}
	if got, _ := s.GetRecord(ctx, second.ID); got.Status != domain.StatusDraft {
		t.Fatalf("rejected record = %s; want it left in DRAFT", got.Status)
	}
	recs, err := s.ActiveRecords(ctx, store.ActiveQuery{LegalEntityID: legalEntity, Currency: "USD", AsOf: day("2026-03-01")})
	if err != nil || len(recs) != 1 || recs[0].ID != first.ID {
		t.Fatalf("active records = %v, %v; want the first record only", recs, err)
	}
	events := outboxRows(t, ctx, pool)
	if len(events) != 1 || events[0].Type != "dev.passage.pricing.conditionrecord.activated.v1" {
		t.Fatalf("events = %+v; want one activated event (no event for the replay or the rejection)", events)
	}
	if events[0].Data["legal_entity_id"] != legalEntity || events[0].Data["retroactive"] != true || events[0].Data["version"] != float64(2) {
		t.Fatalf("activated payload = %v; want legal entity, retroactive=true (valid_from is in the past) and version 2", events[0].Data)
	}
}

func TestPostgres_WithdrawLifecycle(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	rec, err := s.CreateRecord(ctx, entry("2026-01-01", "2026-12-31"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithdrawRecord(ctx, rec.ID, 1, "typo", ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("withdrawing a DRAFT = %v; want %s", err, store.CodeStateInvalid)
	}
	if _, err := s.ActivateRecord(ctx, rec.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithdrawRecord(ctx, rec.ID, 1, "typo", ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version = %v; want %s", err, store.CodeVersionConflict)
	}
	out, err := s.WithdrawRecord(ctx, rec.ID, 2, "superseded", "wd-1")
	if err != nil || out.Status != domain.StatusWithdrawn || out.Version != 3 {
		t.Fatalf("withdraw = %+v, %v; want WITHDRAWN version 3", out, err)
	}
	if _, err := s.WithdrawRecord(ctx, rec.ID, 2, "another reason", "wd-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, different reason = %v; want %s", err, store.CodeIdempotencyUse)
	}
	if recs, _ := s.ActiveRecords(ctx, store.ActiveQuery{LegalEntityID: legalEntity, Currency: "USD", AsOf: day("2026-03-01")}); len(recs) != 0 {
		t.Fatalf("a withdrawn record must not price, got %v", recs)
	}
	events := outboxRows(t, ctx, pool)
	if len(events) != 2 || events[1].Type != "dev.passage.pricing.conditionrecord.withdrawn.v1" || events[1].Data["reason"] != "superseded" {
		t.Fatalf("events = %+v; want activated then withdrawn with the reason", events)
	}
	if _, err := s.GetRecord(ctx, "00000000-0000-0000-0000-00000000dead"); problemCode(err) != store.CodeRecordNotFound {
		t.Fatalf("unknown id = %v; want %s", err, store.CodeRecordNotFound)
	}
}

func TestPostgres_CreateIsIdempotent(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	first, err := s.CreateRecord(ctx, entry("2026-01-01", "2026-12-31"), "same")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateRecord(ctx, entry("2026-01-01", "2026-12-31"), "same")
	if err != nil || again.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the original record %s", again, err, first.ID)
	}
	changed := entry("2026-01-01", "2026-12-31")
	changed.Amount = decimal.RequireFromString("96")
	if _, err := s.CreateRecord(ctx, changed, "same"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, different body = %v; want %s", err, store.CodeIdempotencyUse)
	}
	list, err := s.ListRecords(ctx, store.Filter{})
	if err != nil || len(list) != 1 {
		t.Fatalf("records = %d, %v; want exactly one", len(list), err)
	}
}

func TestPostgres_ListFilters(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	a, _ := s.CreateRecord(ctx, entry("2026-01-01", "2026-06-30"), "")
	other := entry("2026-07-01", "2026-12-31")
	other.ProductKey = "P2"
	b, _ := s.CreateRecord(ctx, other, "")
	if _, err := s.ActivateRecord(ctx, b.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	asOf := day("2026-03-01")
	june, june30, year := day("2026-06-01"), day("2026-06-30"), day("2026-12-31")
	cases := []struct {
		name string
		f    store.Filter
		want []string
	}{
		{"status", store.Filter{Status: domain.StatusActive}, []string{b.ID}},
		{"product", store.Filter{Product: "P1"}, []string{a.ID}},
		{"as of", store.Filter{AsOf: &asOf}, []string{a.ID}},
		{"customer and type", store.Filter{Customer: "C1", Type: string(domain.TypePrice), Limit: 1}, []string{min(a.ID, b.ID)}},
		{"other legal entity", store.Filter{LegalEntityID: otherLegalEntity}, nil},
		{"valid to on June 30", store.Filter{ValidToFrom: &june30, ValidToTo: &june30}, []string{a.ID}},
		{"valid to before June", store.Filter{ValidToTo: &asOf}, nil},
		{"valid to from June", store.Filter{ValidToFrom: &june}, []string{a.ID, b.ID}},
		{"valid to up to year end, soonest first", store.Filter{ValidToTo: &year, Limit: 1}, []string{a.ID}},
		{"valid to from July", store.Filter{ValidToFrom: &year}, []string{b.ID}},
	}
	for _, c := range cases {
		got, err := s.ListRecords(ctx, c.f)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, e := range got {
			ids = append(ids, e.ID)
		}
		if len(ids) != len(c.want) || (len(ids) > 0 && ids[0] != c.want[0]) {
			t.Fatalf("%s: ids = %v; want %v", c.name, ids, c.want)
		}
	}
}

func TestPostgres_TenantAndLegalEntityIsolation(t *testing.T) {
	pool := appPool(t)
	s := store.New(pool)
	rec, err := s.CreateRecord(principal(2, ""), entry("2026-01-01", "2026-12-31"), "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.ListRecords(principal(3, ""), store.Filter{})
	if err != nil || len(other) != 0 {
		t.Fatalf("tenant B must see no records, got %v, %v", other, err)
	}
	if _, err := s.GetRecord(principal(3, ""), rec.ID); problemCode(err) != store.CodeRecordNotFound {
		t.Fatalf("tenant B reading tenant A's record = %v; want not found", err)
	}
	scoped, err := s.ListRecords(principal(2, otherLegalEntity), store.Filter{})
	if err != nil || len(scoped) != 0 {
		t.Fatalf("a caller scoped to another legal entity must see no records, got %v, %v", scoped, err)
	}
	if _, err := s.CreateRecord(principal(2, otherLegalEntity), entry("2026-01-01", "2026-12-31"), ""); err == nil {
		t.Fatal("a caller scoped to another legal entity must not create records for this one")
	}
}
