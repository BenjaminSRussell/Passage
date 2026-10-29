package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
)

func TestGrants_AppRoleCannotRewriteComponentsOrOutbox(t *testing.T) {
	s, _, rls := setup(t)
	if !rls {
		t.Skip("PASSAGE_TEST_APP_DATABASE_URL is needed for the grants test")
	}
	ctx := as(1, "")
	for _, stmt := range []string{
		`update cost_component set name = name`,
		`delete from cost_component`,
		`delete from costing_version`,
		`delete from idempotency_keys`,
		`select count(*) from outbox`,
		`update outbox set type = type`,
	} {
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}

func TestGrants_LockedTransitionWorksAsAppRole(t *testing.T) {
	s, _, rls := setup(t)
	if !rls {
		t.Skip("PASSAGE_TEST_APP_DATABASE_URL is needed for the grants test")
	}
	ctx := as(1, "")
	v, _, err := s.CreateVersion(ctx, spec(ids.New(), "STD-GRANTS"), "k-grants")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.TransitionVersion(ctx, v.ID, 1, domain.ActionBlock); err != nil || got.Status != domain.VersionBlocked {
		t.Fatalf("transition as app role: %+v, %v", got, err)
	}
}
