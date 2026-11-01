package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestWithAllEntitiesRestoresTheScopeWhenTheCallbackFails(t *testing.T) {
	s, _ := newStore(t)
	entity := ids.New()
	ctx := tenant.With(testkit.Ctx(testkit.TenantID(1)), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entity, UserID: "user-test", Duties: testkit.ScopeDuties(entity)})
	boom := errors.New("callback failed")
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		scope := func() string {
			var v string
			if err := tx.QueryRow(ctx, `select coalesce(current_setting('passage.legal_entity_scopes', true), '')`).Scan(&v); err != nil {
				t.Fatal(err)
			}
			return v
		}
		if scope() != entity {
			t.Fatalf("scope before = %q, want %q", scope(), entity)
		}
		_, got := withAllEntities(ctx, tx, func() (int, error) {
			if scope() != "*" {
				t.Fatalf("scope inside = %q, want *", scope())
			}
			return 7, boom
		})
		if !errors.Is(got, boom) {
			t.Fatalf("error = %v, want the callback's", got)
		}
		if scope() != entity {
			t.Fatalf("scope after a failed callback = %q, want it restored to %q", scope(), entity)
		}
		n, got := withAllEntities(ctx, tx, func() (int, error) { return 3, nil })
		if got != nil || n != 3 || scope() != entity {
			t.Fatalf("success path: %d %v scope %q", n, got, scope())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
