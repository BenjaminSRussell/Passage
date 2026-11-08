package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

func rule(t *testing.T, currency, purpose, unit, method string, entity *string) store.CurrencyRounding {
	t.Helper()
	return store.CurrencyRounding{CurrencyCode: currency, Purpose: purpose, Unit: decimal.RequireFromString(unit), Method: method, LegalEntityID: entity}
}

func mustPut(t *testing.T, s *store.Postgres, ctx context.Context, in store.CurrencyRounding) (store.CurrencyRounding, bool) {
	t.Helper()
	out, changed, err := s.PutCurrencyRounding(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return out, changed
}

func mustResolve(t *testing.T, s *store.Postgres, ctx context.Context, currency, purpose, entity string) store.RoundingRule {
	t.Helper()
	got, err := s.ResolveCurrencyRounding(ctx, currency, purpose, entity)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func assertRule(t *testing.T, got store.RoundingRule, unit string, method domain.Method, source string) {
	t.Helper()
	if !got.Unit.Equal(decimal.RequireFromString(unit)) || got.Method != method || got.Source != source {
		t.Fatalf("rule = %s %s %s; want %s %s %s", got.Unit, got.Method, got.Source, unit, method, source)
	}
}

func roundingEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []map[string]any {
	t.Helper()
	var payloads []map[string]any
	for _, row := range outboxRows(t, ctx, pool) {
		if row.Type != "dev.passage.org.currencyrounding.changed.v1" {
			continue
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(row.Payload, &envelope); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, envelope.Data)
	}
	return payloads
}

func TestCurrencyRounding_ResolveDefaultsFromMinorUnits(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	assertRule(t, mustResolve(t, s, ctx, "USD", "general", ""), "0.01", domain.HalfEven, store.RoundingSourceDefault)
	assertRule(t, mustResolve(t, s, ctx, "JPY", "cash", ""), "1", domain.HalfEven, store.RoundingSourceDefault)
	assertRule(t, mustResolve(t, s, ctx, "KWD", "price", ""), "0.001", domain.HalfEven, store.RoundingSourceDefault)
}

func TestCurrencyRounding_LegalEntityRowWinsOverTenantRow(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("EA"))
	b := mustCreate(t, s, ctx, newEntity("EB"))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.05", "half_up", nil))
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", a.ID), "0.05", domain.HalfUp, store.RoundingSourceTenant)

	mustPut(t, s, ctx, rule(t, "CHF", "cash", "1", "up", &a.ID))
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", a.ID), "1", domain.Up, store.RoundingSourceLegalEntity)
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", b.ID), "0.05", domain.HalfUp, store.RoundingSourceTenant)
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", ""), "0.05", domain.HalfUp, store.RoundingSourceTenant)
	assertRule(t, mustResolve(t, s, ctx, "CHF", "sales", a.ID), "0.01", domain.HalfEven, store.RoundingSourceDefault)
}

func TestCurrencyRounding_IdenticalPutWritesNoSecondEventAndKeepsUpdatedAt(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	first, changed := mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.05", "half_up", nil))
	again, changedAgain := mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.050", "half_up", nil))
	if !changed || changedAgain || again.ID != first.ID || !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("repeat = %+v changed=%v; first = %+v changed=%v", again, changedAgain, first, changed)
	}
	if events := roundingEvents(t, ctx, pool); len(events) != 1 {
		t.Fatalf("outbox rows = %d; want 1", len(events))
	}
	next, nextChanged := mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.1", "half_up", nil))
	if !nextChanged || next.ID != first.ID || !next.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("changed unit = %+v changed=%v", next, nextChanged)
	}
	events := roundingEvents(t, ctx, pool)
	want := map[string]any{"currency_code": "CHF", "purpose": "cash", "legal_entity_id": nil, "unit": "0.1", "method": "half_up"}
	if len(events) != 2 || !equalPayload(events[1], want) {
		t.Fatalf("events = %v; want second payload %v", events, want)
	}
}

func equalPayload(got, want map[string]any) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func TestCurrencyRounding_Refusals(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	other := mustCreate(t, s, principalContext(2), newEntity("OT"))
	if _, err := s.SetReferenceStatus(ctx, store.KindCurrency, "EUR", "inactive"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   store.CurrencyRounding
		code string
	}{
		{"unit finer than minor unit", rule(t, "USD", "general", "0.001", "half_even", nil), domain.CodeRoundingUnit},
		{"unit not a supported multiple", rule(t, "USD", "general", "0.03", "half_even", nil), domain.CodeRoundingUnit},
		{"unknown currency", rule(t, "XXQ", "general", "0.01", "half_even", nil), store.CodeReferenceNotFound},
		{"inactive currency", rule(t, "EUR", "general", "0.01", "half_even", nil), store.CodeReferenceInactive},
		{"another tenant's legal entity", rule(t, "USD", "general", "0.01", "half_even", &other.ID), store.CodeLegalEntityNotFound},
		{"malformed legal entity id", rule(t, "USD", "general", "0.01", "half_even", ptr("bogus")), store.CodeLegalEntityNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, changed, err := s.PutCurrencyRounding(ctx, tt.in)
			if changed || problemCode(t, err) != tt.code {
				t.Fatalf("put = changed %v, %v; want %s", changed, err, tt.code)
			}
		})
	}
	var problem *httpx.Problem
	if _, _, err := s.PutCurrencyRounding(ctx, tests[0].in); !errors.As(err, &problem) || problem.Errors["unit"] == "" {
		t.Fatalf("unit refusal must name field unit: %v", err)
	}
	if _, _, err := s.PutCurrencyRounding(ctx, tests[3].in); !errors.As(err, &problem) || problem.Errors["currency"] == "" {
		t.Fatalf("inactive refusal must name field currency: %v", err)
	}
	if got := mustResolve(t, s, ctx, "EUR", "general", ""); got.Source != store.RoundingSourceDefault {
		t.Fatalf("an inactive currency must still resolve: %+v", got)
	}
	if _, err := s.ResolveCurrencyRounding(ctx, "XXQ", "general", ""); problemCode(t, err) != store.CodeReferenceNotFound {
		t.Fatalf("unknown currency resolve = %v", err)
	}
	if _, err := s.ResolveCurrencyRounding(ctx, "USD", "general", other.ID); problemCode(t, err) != store.CodeLegalEntityNotFound {
		t.Fatalf("unknown legal entity resolve = %v", err)
	}
	if _, err := s.ListCurrencyRoundings(ctx, "XXQ"); problemCode(t, err) != store.CodeReferenceNotFound {
		t.Fatalf("unknown currency list = %v", err)
	}
}

func TestCurrencyRounding_ListOrdersByPurposeThenTenantRowThenEntity(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("EA"))
	b := mustCreate(t, s, ctx, newEntity("EB"))
	first, second := a.ID, b.ID
	if first > second {
		first, second = second, first
	}
	if empty, err := s.ListCurrencyRoundings(ctx, "CHF"); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list = %#v, %v; want []", empty, err)
	}
	mustPut(t, s, ctx, rule(t, "CHF", "sales", "0.01", "half_up", nil))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "1", "up", &second))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.05", "half_up", nil))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.5", "down", &first))
	mustPut(t, s, ctx, rule(t, "EUR", "cash", "0.01", "half_even", nil))
	got, err := s.ListCurrencyRoundings(ctx, "CHF")
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		purpose string
		entity  string
	}
	want := []key{{"cash", ""}, {"cash", first}, {"cash", second}, {"sales", ""}}
	if len(got) != len(want) {
		t.Fatalf("list = %+v; want %d rows", got, len(want))
	}
	for i, w := range want {
		entity := ""
		if got[i].LegalEntityID != nil {
			entity = *got[i].LegalEntityID
		}
		if got[i].Purpose != w.purpose || entity != w.entity {
			t.Fatalf("row %d = %s/%s; want %s/%s", i, got[i].Purpose, entity, w.purpose, w.entity)
		}
	}
}

func TestCurrencyRounding_TenantIsolationAndUniqueKey(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	entity := mustCreate(t, s, ctx, newEntity("EA"))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.05", "half_up", nil))

	count := func(c context.Context) (n int) {
		t.Helper()
		err := dbx.WithTenantTx(c, pool, func(tx pgx.Tx) error {
			return tx.QueryRow(c, `select count(*) from currency_rounding`).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(ctx) != 1 || count(principalContext(2)) != 0 {
		t.Fatal("another tenant must not read a tenant's rounding rows")
	}
	insert := func(entityID *string) error {
		return dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into currency_rounding (tenant_id, id, currency_code, purpose, legal_entity_id, unit, method)
				values ($1, $2, 'CHF', 'cash', $3, 0.05, 'half_up')`, testkit.TenantID(1), ids.New(), entityID)
			return err
		})
	}
	var pgErr *pgconn.PgError
	if err := insert(nil); !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("a second tenant row for one (currency, purpose) must violate the unique index: %v", err)
	}
	if err := insert(&entity.ID); err != nil {
		t.Fatal(err)
	}
	if err := insert(&entity.ID); !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("a second legal-entity row must violate the unique index: %v", err)
	}
}

func TestCurrencyRounding_ScopedCallerSeesTenantRowsAndItsOwnEntityOnly(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("EA"))
	b := mustCreate(t, s, ctx, newEntity("EB"))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.05", "half_up", nil))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "1", "up", &a.ID))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.5", "down", &b.ID))

	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: a.ID})
	got, err := s.ListCurrencyRoundings(scoped, "CHF")
	if err != nil || len(got) != 2 || got[0].LegalEntityID != nil || *got[1].LegalEntityID != a.ID {
		t.Fatalf("scoped list = %+v, %v; want the tenant row and entity A's row", got, err)
	}
	assertRule(t, mustResolve(t, s, scoped, "CHF", "cash", a.ID), "1", domain.Up, store.RoundingSourceLegalEntity)
}

func TestCurrencyRounding_DeleteTenantRowRevertsToDefaultAndEmitsTheRuleInForce(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.05", "half_up", nil))
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", ""), "0.05", domain.HalfUp, store.RoundingSourceTenant)
	if err := s.DeleteCurrencyRounding(ctx, "CHF", "cash", nil); err != nil {
		t.Fatal(err)
	}
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", ""), "0.01", domain.HalfEven, store.RoundingSourceDefault)
	events := roundingEvents(t, ctx, pool)
	want := map[string]any{"currency_code": "CHF", "purpose": "cash", "legal_entity_id": nil, "unit": "0.01", "method": "half_even"}
	if len(events) != 2 || !equalPayload(events[1], want) {
		t.Fatalf("events = %v; want a second payload %v", events, want)
	}
	if err := s.DeleteCurrencyRounding(ctx, "CHF", "cash", nil); problemCode(t, err) != store.CodeRoundingNotFound {
		t.Fatalf("second delete = %v; want %s", err, store.CodeRoundingNotFound)
	}
	if got := roundingEvents(t, ctx, pool); len(got) != 2 {
		t.Fatalf("outbox rows after the refused delete = %d; want 2", len(got))
	}
}

func TestCurrencyRounding_DeleteLegalEntityRowFallsBackToTheTenantRow(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("EA"))
	b := mustCreate(t, s, ctx, newEntity("EB"))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.05", "half_up", nil))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "1", "up", &a.ID))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.5", "down", &b.ID))
	if err := s.DeleteCurrencyRounding(ctx, "CHF", "cash", &a.ID); err != nil {
		t.Fatal(err)
	}
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", a.ID), "0.05", domain.HalfUp, store.RoundingSourceTenant)
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", b.ID), "0.5", domain.Down, store.RoundingSourceLegalEntity)
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", ""), "0.05", domain.HalfUp, store.RoundingSourceTenant)
	events := roundingEvents(t, ctx, pool)
	want := map[string]any{"currency_code": "CHF", "purpose": "cash", "legal_entity_id": a.ID, "unit": "0.05", "method": "half_up"}
	if len(events) != 4 || !equalPayload(events[3], want) {
		t.Fatalf("events = %v; want a fourth payload %v", events, want)
	}
	if err := s.DeleteCurrencyRounding(ctx, "CHF", "cash", &a.ID); problemCode(t, err) != store.CodeRoundingNotFound {
		t.Fatalf("second delete = %v; want %s", err, store.CodeRoundingNotFound)
	}
	if err := s.DeleteCurrencyRounding(ctx, "CHF", "cash", &b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCurrencyRounding(ctx, "CHF", "cash", nil); err != nil {
		t.Fatal(err)
	}
	if events = roundingEvents(t, ctx, pool); len(events) != 6 || events[5]["unit"] != "0.01" || events[5]["legal_entity_id"] != nil {
		t.Fatalf("tenant delete after the entity rows: events = %v", events)
	}
}

func TestCurrencyRounding_DeleteRefusalsAndTenantIsolation(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	other := mustCreate(t, s, principalContext(2), newEntity("OT"))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.05", "half_up", nil))
	tests := []struct {
		name     string
		ctx      context.Context
		currency string
		entity   *string
		code     string
	}{
		{"unknown currency", ctx, "XXQ", nil, store.CodeReferenceNotFound},
		{"another tenant's legal entity", ctx, "CHF", &other.ID, store.CodeLegalEntityNotFound},
		{"malformed legal entity id", ctx, "CHF", ptr("bogus"), store.CodeLegalEntityNotFound},
		{"no entity row", ctx, "CHF", ptr(mustCreate(t, s, ctx, newEntity("EA")).ID), store.CodeRoundingNotFound},
		{"another tenant's rule", principalContext(2), "CHF", nil, store.CodeRoundingNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.DeleteCurrencyRounding(tt.ctx, tt.currency, "cash", tt.entity); problemCode(t, err) != tt.code {
				t.Fatalf("delete = %v; want %s", err, tt.code)
			}
		})
	}
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", ""), "0.05", domain.HalfUp, store.RoundingSourceTenant)
	if events := roundingEvents(t, ctx, pool); len(events) != 1 {
		t.Fatalf("outbox rows after refusals = %d; want 1", len(events))
	}
}

func TestCurrencyRounding_TenantAdminWithoutClaimWritesLegalEntityRows(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("EA"))
	admin := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "admin", Duties: map[string]struct{}{store.DutyReference: {}, tenant.DutyTenantAdmin: {}}})
	mustPut(t, s, admin, rule(t, "CHF", "cash", "1", "up", &a.ID))
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", a.ID), "1", domain.Up, store.RoundingSourceLegalEntity)
	if err := s.DeleteCurrencyRounding(admin, "CHF", "cash", &a.ID); err != nil {
		t.Fatal(err)
	}
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", a.ID), "0.01", domain.HalfEven, store.RoundingSourceDefault)
}

func TestCurrencyRounding_NoClaimAndDutyWithoutTenantAdminCannotWriteLegalEntityRows(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("EB"))
	noAdmin := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "admin", Duties: map[string]struct{}{store.DutyReference: {}}})
	if _, _, err := s.PutCurrencyRounding(noAdmin, rule(t, "CHF", "cash", "1", "up", &a.ID)); err == nil {
		t.Fatal("a caller without tenant.administer wrote a legal-entity rounding row")
	}
	assertRule(t, mustResolve(t, s, ctx, "CHF", "cash", a.ID), "0.01", domain.HalfEven, store.RoundingSourceDefault)
}

// An empty currency lists every rule of the tenant, the read the organization package export makes.
func TestCurrencyRounding_ListWithoutCurrencyListsEveryRule(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("EL"))
	mustPut(t, s, ctx, rule(t, "USD", "cash", "0.05", "half_up", nil))
	mustPut(t, s, ctx, rule(t, "CHF", "cash", "0.05", "half_up", nil))
	mustPut(t, s, ctx, rule(t, "CHF", "price", "0.1", "up", &a.ID))
	rules, err := s.ListCurrencyRoundings(ctx, "")
	if err != nil || len(rules) != 3 || rules[0].CurrencyCode != "CHF" || rules[2].CurrencyCode != "USD" {
		t.Fatalf("rules = %+v %v", rules, err)
	}
	if one, err := s.ListCurrencyRoundings(ctx, "CHF"); err != nil || len(one) != 2 {
		t.Fatalf("CHF rules = %+v %v", one, err)
	}
}
